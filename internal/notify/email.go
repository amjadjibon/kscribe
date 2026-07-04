package notify

import (
	"fmt"
	"html"
	"strings"
)

// Subject builds the notification subject line.
func Subject(phase, reason, namespace, object string) string {
	return fmt.Sprintf("[kscribe] %s: %s %s/%s", phase, reason, namespace, object)
}

// HTML builds a minimal inline-styled email body. Every field is escaped —
// summaries and remediation come from an LLM and events, never trust them.
func HTML(phase, reason, namespace, object, summary, rootCause string, remediation []string) string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<div style="font-family:system-ui,sans-serif;max-width:640px">`)
	fmt.Fprintf(&b, `<h2 style="margin:0 0 4px">%s — %s</h2>`, esc(reason), esc(phase))
	fmt.Fprintf(&b, `<p style="color:#555;margin:0 0 16px">%s/%s</p>`, esc(namespace), esc(object))
	if summary != "" {
		fmt.Fprintf(&b, `<p><strong>Summary:</strong> %s</p>`, esc(summary))
	}
	if rootCause != "" {
		fmt.Fprintf(&b, `<p><strong>Root cause:</strong> %s</p>`, esc(rootCause))
	}
	if len(remediation) > 0 {
		b.WriteString(`<p><strong>Remediation:</strong></p><ol>`)
		for _, step := range remediation {
			fmt.Fprintf(&b, `<li>%s</li>`, esc(step))
		}
		b.WriteString(`</ol>`)
	}
	b.WriteString(`<p style="color:#888;font-size:12px;margin-top:24px">Sent by kscribe — see the dashboard for redacted context, trace, and chat.</p></div>`)
	return b.String()
}
