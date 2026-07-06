// Package notify creates a Linear issue for diagnosis results via the Linear
// GraphQL API. Stdlib only.
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

const linearAPIURL = "https://api.linear.app/graphql"

const linearIssueCreateMutation = `mutation IssueCreate($input: IssueCreateInput!) { issueCreate(input: $input) { success } }`

// Linear creates an issue via the Linear GraphQL API.
type Linear struct {
	APIKey     string
	TeamID     string
	BaseURL    string       // defaults to https://api.linear.app/graphql
	HTTPClient *http.Client // defaults to a 10s-timeout client
}

type linearRequest struct {
	Query     string          `json:"query"`
	Variables linearVariables `json:"variables"`
}

type linearVariables struct {
	Input linearIssueInput `json:"input"`
}

type linearIssueInput struct {
	TeamID      string `json:"teamId"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type linearResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Notify creates a Linear issue summarizing the diagnosis.
func (l *Linear) Notify(ctx context.Context, n Notification) error {
	title := Subject(n.Phase, n.Reason, n.Namespace, n.Object)
	body, err := json.Marshal(linearRequest{
		Query: linearIssueCreateMutation,
		Variables: linearVariables{
			Input: linearIssueInput{
				TeamID:      l.TeamID,
				Title:       title,
				Description: plainBody(n),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal linear issue: %w", err)
	}

	url := l.BaseURL
	if url == "" {
		url = linearAPIURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.APIKey) // Linear uses the raw API key, no "Bearer " prefix

	hc := l.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("create linear issue: %w", err)
	}
	defer resp.Body.Close()

	// Read enough to parse a GraphQL error envelope even when it echoes back
	// input (validation errors); truncate only the message shown to the operator.
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	truncated := rb
	if len(truncated) > 512 {
		truncated = truncated[:512]
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("linear error %d: %s", resp.StatusCode, truncated)
	}

	// GraphQL returns 200 even when the mutation fails, so check the body.
	var lr linearResponse
	if err := json.Unmarshal(rb, &lr); err == nil && len(lr.Errors) > 0 {
		return fmt.Errorf("linear graphql error: %s", truncated)
	}
	return nil
}
