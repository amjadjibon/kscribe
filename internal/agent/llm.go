package agent

import "context"

// Provider sends a chat-completions request and returns the response.
// The interface is narrow so tests can inject a fake without network calls.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}
