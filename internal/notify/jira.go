// Package notify creates a Jira issue for diagnosis results via the Jira
// Cloud REST API. Stdlib only.
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

// Jira creates an issue in a Jira Cloud project (POST /rest/api/3/issue).
type Jira struct {
	BaseURL    string
	Email      string
	APIToken   string
	ProjectKey string
	HTTPClient *http.Client // defaults to a 10s-timeout client
}

type jiraIssueRequest struct {
	Fields jiraFields `json:"fields"`
}

type jiraFields struct {
	Project     jiraProject   `json:"project"`
	Summary     string        `json:"summary"`
	Description jiraADF       `json:"description"`
	IssueType   jiraIssueType `json:"issuetype"`
}

type jiraProject struct {
	Key string `json:"key"`
}

type jiraIssueType struct {
	Name string `json:"name"`
}

// jiraADF is a minimal single-paragraph Atlassian Document Format body.
type jiraADF struct {
	Type    string        `json:"type"`
	Version int           `json:"version"`
	Content []jiraADFNode `json:"content"`
}

type jiraADFNode struct {
	Type    string           `json:"type"`
	Content []jiraADFContent `json:"content"`
}

type jiraADFContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Notify creates a Jira issue summarizing the diagnosis.
func (j *Jira) Notify(ctx context.Context, n Notification) error {
	summary := Subject(n.Phase, n.Reason, n.Namespace, n.Object)
	body, err := json.Marshal(jiraIssueRequest{
		Fields: jiraFields{
			Project: jiraProject{Key: j.ProjectKey},
			Summary: summary,
			Description: jiraADF{
				Type:    "doc",
				Version: 1,
				Content: []jiraADFNode{{
					Type:    "paragraph",
					Content: []jiraADFContent{{Type: "text", Text: plainBody(n)}},
				}},
			},
			IssueType: jiraIssueType{Name: "Task"},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal jira issue: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.BaseURL+"/rest/api/3/issue", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(j.Email, j.APIToken)

	hc := j.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("create jira issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("jira error %d: %s", resp.StatusCode, rb)
	}
	return nil
}

// plainBody renders Summary/RootCause/Remediation as plain text, shared by
// notifiers that don't render HTML (Jira, Linear).
func plainBody(n Notification) string {
	var b []byte
	if n.Summary != "" {
		b = fmt.Appendf(b, "Summary: %s\n", n.Summary)
	}
	if n.RootCause != "" {
		b = fmt.Appendf(b, "Root cause: %s\n", n.RootCause)
	}
	for i, step := range n.Remediation {
		b = fmt.Appendf(b, "%d. %s\n", i+1, step)
	}
	return string(b)
}
