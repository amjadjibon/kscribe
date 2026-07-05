package notify

import "context"

// Notification is the channel-neutral payload for a finished diagnosis.
// Fields come from the redacted RCA — renderers escape for their format.
type Notification struct {
	Phase       string
	Reason      string
	Namespace   string
	Object      string
	Summary     string
	RootCause   string
	Remediation []string
}

// Notifier delivers a diagnosis notification on one channel.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
