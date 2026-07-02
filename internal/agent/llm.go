package agent

import "context"

// Provider sends a chat-completions request and returns the response.
// The interface is narrow so tests can inject a fake without network calls.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// StreamingProvider extends Provider with token-by-token streaming.
// onDelta is called for each content chunk as it arrives; returning an error
// aborts the stream. The returned Response carries the fully-accumulated text
// so callers don't have to reassemble it themselves.
type StreamingProvider interface {
	Provider
	CompleteStream(ctx context.Context, req Request, onDelta func(string) error) (Response, error)
}

// StreamOrComplete delegates to CompleteStream when p satisfies StreamingProvider,
// otherwise calls Complete and invokes onDelta once with the full content.
// This is the single home for the non-streaming fallback; callers never branch.
func StreamOrComplete(ctx context.Context, p Provider, req Request, onDelta func(string) error) (Response, error) {
	if sp, ok := p.(StreamingProvider); ok {
		return sp.CompleteStream(ctx, req, onDelta)
	}
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	if len(resp.Choices) > 0 {
		if err := onDelta(resp.Choices[0].Message.Content); err != nil {
			return Response{}, err
		}
	}
	return resp, nil
}
