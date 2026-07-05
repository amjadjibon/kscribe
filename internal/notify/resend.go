// Package notify sends diagnosis-result email notifications via the Resend
// API. Stdlib only — Resend is a single authenticated HTTPS POST.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultBaseURL = "https://api.resend.com"

// Resend sends email through the Resend API (POST /emails).
type Resend struct {
	APIKey     string
	From       string
	To         []string
	BaseURL    string       // defaults to https://api.resend.com
	HTTPClient *http.Client // defaults to a 10s-timeout client
}

type emailRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

// Notify renders and sends the notification as email.
func (r *Resend) Notify(ctx context.Context, n Notification) error {
	subject := Subject(n.Phase, n.Reason, n.Namespace, n.Object)
	html := HTML(n.Phase, n.Reason, n.Namespace, n.Object, n.Summary, n.RootCause, n.Remediation)
	return r.Send(ctx, subject, html)
}

// Send posts one email. Non-2xx responses become an error carrying the status
// and at most 512 bytes of body (SEC-001: no unbounded provider detail).
func (r *Resend) Send(ctx context.Context, subject, html string) error {
	body, err := json.Marshal(emailRequest{
		From:    r.From,
		To:      r.To,
		Subject: subject,
		HTML:    html,
	})
	if err != nil {
		return fmt.Errorf("marshal email: %w", err)
	}

	base := r.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.APIKey)

	hc := r.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("resend error %d: %s", resp.StatusCode, b)
	}
	return nil
}
