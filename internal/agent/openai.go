package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// streamChunk is the minimal SSE payload shape for streaming chat completions.
// JSON via stdlib encoding/json.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// OpenAIClient is an OpenAI-compatible chat-completions provider.
// JSON via stdlib encoding/json.
// ponytail: single endpoint, no streaming, no retry beyond caller's JSON repair.
type OpenAIClient struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

const DefaultMaxTokens = 1024

// NewOpenAIClient constructs an OpenAIClient. baseURL is the API base including
// the version segment (e.g. https://api.openai.com/v1, or Gemini's
// https://generativelanguage.googleapis.com/v1beta/openai); "/chat/completions"
// is appended. Empty defaults to OpenAI.
func NewOpenAIClient(baseURL, apiKey, model string) *OpenAIClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIClient{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		HTTPClient: http.DefaultClient,
	}
}

// sdkClient assembles an openai-go client from the struct fields. WithBaseURL
// normalizes a non-slash-terminated path (so a Gemini-style
// .../v1beta/openai base still POSTs to .../v1beta/openai/chat/completions).
func (c *OpenAIClient) sdkClient() openai.Client {
	opts := []option.RequestOption{option.WithBaseURL(c.BaseURL)}
	if c.APIKey != "" {
		opts = append(opts, option.WithAPIKey(c.APIKey))
	}
	if c.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(c.HTTPClient))
	}
	return openai.NewClient(opts...)
}

// Complete sends a chat-completions request via the official openai-go SDK and
// returns the parsed response mapped back into our provider-neutral schema.
func (c *OpenAIClient) Complete(ctx context.Context, req Request) (Response, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(c.Model),
		Messages: toSDKMessages(req.Messages),
	}
	params.MaxTokens = param.NewOpt(int64(effectiveMaxTokens(req)))
	if len(req.Tools) > 0 {
		params.Tools = toSDKTools(req.Tools)
	}

	client := c.sdkClient()
	resp, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Response{}, truncErr(err)
	}
	return fromSDKResponse(resp), nil
}

// truncErr caps the provider error string at 512 bytes so raw provider detail
// (which may include request URLs / payload echoes) is not embedded in durable
// CR status or SQLite. SEC-001.
func truncErr(err error) error {
	s := err.Error()
	if len(s) > 512 {
		s = s[:512]
	}
	return fmt.Errorf("provider error: %s", s)
}

// toSDKMessages maps our provider-neutral messages onto the SDK param unions.
// Assistant messages carrying tool_calls use the explicit param struct (not the
// openai.AssistantMessage string helper), which cannot represent tool_calls.
func toSDKMessages(msgs []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system":
			out = append(out, openai.SystemMessage(m.Content))
		case "developer":
			out = append(out, openai.DeveloperMessage(m.Content))
		case "tool":
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		case "assistant":
			if len(m.ToolCalls) == 0 {
				out = append(out, openai.AssistantMessage(m.Content))
				break
			}
			asst := openai.ChatCompletionAssistantMessageParam{}
			if m.Content != "" {
				asst.Content.OfString = param.NewOpt(m.Content)
			}
			for _, tc := range m.ToolCalls {
				asst.ToolCalls = append(asst.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					},
				})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &asst})
		default: // user and anything unexpected
			out = append(out, openai.UserMessage(m.Content))
		}
	}
	return out
}

// toSDKTools maps our tool definitions onto the SDK's function-tool params.
func toSDKTools(tools []ToolDefinition) []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		fn := shared.FunctionDefinitionParam{Name: t.Function.Name}
		if t.Function.Description != "" {
			fn.Description = param.NewOpt(t.Function.Description)
		}
		if p, ok := t.Function.Parameters.(map[string]any); ok {
			fn.Parameters = p
		}
		out = append(out, openai.ChatCompletionFunctionTool(fn))
	}
	return out
}

// fromSDKResponse maps the SDK response back into our schema.
func fromSDKResponse(resp *openai.ChatCompletion) Response {
	out := Response{Usage: Usage{TotalTokens: int(resp.Usage.TotalTokens)}}
	for _, ch := range resp.Choices {
		msg := Message{Role: "assistant", Content: ch.Message.Content}
		for _, tc := range ch.Message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:       tc.ID,
				Type:     "function",
				Function: FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
		out.Choices = append(out.Choices, Choice{Message: msg, FinishReason: ch.FinishReason})
	}
	return out
}

// CompleteStream sends a streaming chat-completions request and calls onDelta
// for each content chunk. The returned Response.Choices[0].Message.Content
// holds the fully-accumulated text. Implements StreamingProvider.
// JSON via stdlib encoding/json.
func (c *OpenAIClient) CompleteStream(ctx context.Context, req Request, onDelta func(string) error) (Response, error) {
	req.Model = c.Model
	req.Stream = true
	req.MaxTokens = effectiveMaxTokens(req)
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Response{}, fmt.Errorf("provider error %d: %s", resp.StatusCode, b)
	}

	var accumulated strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[len("data: "):]
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // ponytail: skip malformed chunk; some providers emit non-JSON SSE lines
		}
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta.Content
			if delta != "" {
				accumulated.WriteString(delta)
				if err := onDelta(delta); err != nil {
					return Response{}, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, fmt.Errorf("read stream: %w", err)
	}

	return Response{
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: accumulated.String()},
			FinishReason: "stop",
		}},
	}, nil
}

func effectiveMaxTokens(req Request) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return DefaultMaxTokens
}
