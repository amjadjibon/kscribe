package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/bytedance/sonic"
)

// OpenAIClient is an OpenAI-compatible chat-completions provider.
// CON-003: sonic for all JSON encoding/decoding.
// ponytail: single endpoint, no streaming, no retry beyond caller's JSON repair.
type OpenAIClient struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

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

// Complete sends a chat-completions request and returns the parsed response.
func (c *OpenAIClient) Complete(ctx context.Context, req Request) (Response, error) {
	req.Model = c.Model
	body, err := sonic.Marshal(req)
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
		// Truncate to 512 bytes so raw provider detail is not embedded in durable CR status / SQLite.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Response{}, fmt.Errorf("provider error %d: %s", resp.StatusCode, b)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("read response body: %w", err)
	}
	var out Response
	if err := sonic.Unmarshal(b, &out); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return out, nil
}
