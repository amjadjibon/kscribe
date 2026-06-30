package templates

import (
	"bytes"
	"html/template"

	"github.com/a-h/templ"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
)

// RenderMarkdown converts an untrusted Markdown string to a sanitized templ.Component.
// SEC-001: all LLM output passes through bluemonday.UGCPolicy() before rendering.
func RenderMarkdown(md string) templ.Component {
	var buf bytes.Buffer
	_ = goldmark.Convert([]byte(md), &buf)
	safe := bluemonday.UGCPolicy().SanitizeBytes(buf.Bytes())
	return templ.Raw(template.HTML(safe)) // nolint:gosec — sanitized above
}
