package templates_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/amjadjibon/kscribe/internal/web/templates"
)

func TestRenderMarkdown_Bold(t *testing.T) {
	comp := templates.RenderMarkdown("**bold**")
	var buf bytes.Buffer
	_ = comp.Render(nil, &buf) //nolint:staticcheck — nil ctx is fine for templ.Raw
	if !strings.Contains(buf.String(), "<strong>") {
		t.Errorf("want <strong> in output, got: %s", buf.String())
	}
}

func TestRenderMarkdown_Sanitize(t *testing.T) {
	payloads := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
	}
	for _, p := range payloads {
		comp := templates.RenderMarkdown(p)
		var buf bytes.Buffer
		_ = comp.Render(nil, &buf) //nolint:staticcheck
		out := buf.String()
		if strings.Contains(out, "<script>") {
			t.Errorf("sanitization failed: <script> found in output for payload %q", p)
		}
		if strings.Contains(out, "onerror=") {
			t.Errorf("sanitization failed: onerror= found in output for payload %q", p)
		}
	}
}
