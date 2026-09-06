package fileserver

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	input := []byte("# Title\n\n- one\n- **two**\n\n[page](page.html)\n\n<script>alert(1)</script>")
	got := renderMarkdown(input, "docs")

	for _, want := range []string{
		"<h1>Title</h1>",
		"<ul>",
		"<strong>two</strong>",
		`href="/docs/page.html" target="_blank"`,
		"&lt;script&gt;alert(1)&lt;/script&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered Markdown does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Fatal("raw HTML was not escaped")
	}
}

func TestMarkdownRejectsUnsafeURL(t *testing.T) {
	got := renderMarkdown([]byte("[unsafe](javascript:alert(1))"), "")
	if strings.Contains(got, "href=") {
		t.Fatalf("unsafe URL was rendered as a link: %s", got)
	}
}
