package agent

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
)

// Outcome is the result of a single diagnosis run.
type Outcome struct {
	Phase      kscribev1alpha1.DiagnosisPhase
	RCA        *RCAResult // non-nil when Phase is Done
	TokensUsed int64
	RawError   string // human-readable reason for Partial or Failed
}

// DiagnosisAgent orchestrates an LLM tool-call loop to produce a root-cause analysis.
// ponytail: no retry/backoff beyond one JSON repair; no provider plugin system.
type DiagnosisAgent struct {
	Provider Provider
	Executor ToolExecutor    // may be nil; tool calls return an error message if so
	Tools    []ToolDefinition
	MaxIter  int             // 0 falls back to 5
}

const systemPrompt = `You are a Kubernetes SRE expert. Analyse the provided cluster context and produce a root-cause analysis (RCA). Use the available tools if you need more information. When you have sufficient information, respond ONLY with a JSON object matching this exact schema (no prose, no markdown fences):
{"summary":"...","rootCause":"...","contributingFactors":["..."],"remediationSteps":["..."],"confidence":0.9}`

// Run executes the diagnosis loop and returns an Outcome.
// Done  = valid RCA parsed (first try or after one JSON-repair retry).
// Partial = max iterations hit or repair also failed but model responded.
// Failed  = provider error or no usable response.
func (a *DiagnosisAgent) Run(ctx context.Context, snapshotJSON []byte) Outcome {
	maxIter := a.MaxIter
	if maxIter <= 0 {
		maxIter = 5 // ponytail: sensible default; configure via policy
	}

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "Cluster context:\n" + string(snapshotJSON)},
	}

	var totalTokens int64

	for i := 0; i < maxIter; i++ {
		resp, err := a.Provider.Complete(ctx, Request{Messages: messages, Tools: a.Tools})
		if err != nil {
			return Outcome{
				Phase:      kscribev1alpha1.DiagnosisPhaseFailed,
				TokensUsed: totalTokens,
				RawError:   err.Error(),
			}
		}
		totalTokens += int64(resp.Usage.TotalTokens)

		if len(resp.Choices) == 0 {
			return Outcome{
				Phase:      kscribev1alpha1.DiagnosisPhaseFailed,
				TokensUsed: totalTokens,
				RawError:   "provider returned empty choices",
			}
		}

		msg := resp.Choices[0].Message
		messages = append(messages, msg)

		// No tool calls → model is done; parse RCA.
		if len(msg.ToolCalls) == 0 {
			rca, err := parseRCA(msg.Content)
			if err == nil {
				return Outcome{
					Phase:      kscribev1alpha1.DiagnosisPhaseDone,
					RCA:        rca,
					TokensUsed: totalTokens,
				}
			}
			// Single JSON-repair retry (per spec).
			rca, repairErr := a.repairRCA(ctx, messages, &totalTokens)
			if repairErr == nil {
				return Outcome{
					Phase:      kscribev1alpha1.DiagnosisPhaseDone,
					RCA:        rca,
					TokensUsed: totalTokens,
				}
			}
			return Outcome{
				Phase:      kscribev1alpha1.DiagnosisPhasePartial,
				TokensUsed: totalTokens,
				RawError:   fmt.Sprintf("rca parse: %v; repair: %v", err, repairErr),
			}
		}

		// Execute tool calls and append results.
		for _, tc := range msg.ToolCalls {
			result := a.callTool(ctx, tc)
			messages = append(messages, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
	}

	return Outcome{
		Phase:      kscribev1alpha1.DiagnosisPhasePartial,
		TokensUsed: totalTokens,
		RawError:   "max iterations reached without final RCA",
	}
}

// repairRCA sends one re-prompt asking the model for valid JSON only.
func (a *DiagnosisAgent) repairRCA(ctx context.Context, messages []Message, totalTokens *int64) (*RCAResult, error) {
	repair := make([]Message, len(messages), len(messages)+1)
	copy(repair, messages)
	repair = append(repair, Message{
		Role:    "user",
		Content: "Your previous response was not valid JSON. Respond ONLY with the JSON RCA object — no prose, no markdown.",
	})
	resp, err := a.Provider.Complete(ctx, Request{Messages: repair})
	if err != nil {
		return nil, err
	}
	*totalTokens += int64(resp.Usage.TotalTokens)
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty repair response")
	}
	return parseRCA(resp.Choices[0].Message.Content)
}

// callTool executes a single tool call, returning a string result for the model.
func (a *DiagnosisAgent) callTool(ctx context.Context, tc ToolCall) string {
	if a.Executor == nil {
		return fmt.Sprintf("tool %q: no executor configured", tc.Function.Name)
	}
	result, err := a.Executor.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return result
}

// parseRCA unmarshals content as an RCAResult via sonic (CON-003).
func parseRCA(content string) (*RCAResult, error) {
	var rca RCAResult
	if err := sonic.UnmarshalString(content, &rca); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if rca.Summary == "" || rca.RootCause == "" {
		return nil, fmt.Errorf("incomplete rca: missing summary or rootCause")
	}
	return &rca, nil
}
