package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderMarkdown(t *testing.T) {
	out := string(renderMarkdown("Here is **bold**, *italic*, and `code`:\n\n1. First item\n2. Second item\n\n- Bullet\n  - Nested\n\nSee [docs](https://example.com)."))
	require.Contains(t, out, "<strong>bold</strong>")
	require.Contains(t, out, "<em>italic</em>")
	require.Contains(t, out, "<code>code</code>")
	require.Contains(t, out, "<ol>")
	require.Contains(t, out, "<ul>")
	require.Contains(t, out, `href="https://example.com"`)
	require.Contains(t, out, `target="_blank"`)
	require.Contains(t, out, `rel="noopener noreferrer"`)
}

func TestRenderMarkdownSanitizesUnsafeHTMLAndLinks(t *testing.T) {
	out := string(renderMarkdown(`<script>alert(1)</script> <img src=x onerror=alert(1)> [click](javascript:alert(1))`))
	require.NotContains(t, out, "<script>")
	require.NotContains(t, out, "onerror=")
	require.NotContains(t, out, "javascript:")
}
