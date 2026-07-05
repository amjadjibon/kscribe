package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Slack posts diagnosis notifications to a Slack incoming webhook.
type Slack struct {
	WebhookURL string
	HTTPClient *http.Client // defaults to a 10s-timeout client
}

// slackEscape escapes Slack's control entities (they are not HTML).
// https://api.slack.com/reference/surfaces/formatting#escaping
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Notify posts one mrkdwn-formatted message.
func (s *Slack) Notify(ctx context.Context, n Notification) error {
	esc := slackEscape.Replace
	var b strings.Builder
	fmt.Fprintf(&b, "*[kscribe] %s: %s %s/%s*\n", esc(n.Phase), esc(n.Reason), esc(n.Namespace), esc(n.Object))
	if n.Summary != "" {
		fmt.Fprintf(&b, "*Summary:* %s\n", esc(n.Summary))
	}
	if n.RootCause != "" {
		fmt.Fprintf(&b, "*Root cause:* %s\n", esc(n.RootCause))
	}
	for i, step := range n.Remediation {
		fmt.Fprintf(&b, "%d. %s\n", i+1, esc(step))
	}

	body, err := json.Marshal(map[string]string{"text": b.String()})
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	hc := s.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("post slack webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack error %d: %s", resp.StatusCode, rb)
	}
	return nil
}
