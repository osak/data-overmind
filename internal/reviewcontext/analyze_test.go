package reviewcontext

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func TestExtractBlocksMatchesBrowserAnchors(t *testing.T) {
	document := mustParseHTML(t, `<!doctype html>
<html><head><title>Anchor fixture</title></head><body><div id="背景"></div>
<article class="doc-body extra">
  <p data-review-id="intro">Intro</p>
  <h2>背景</h2>
  <p>Before</p>
  <ul>
    <li>Outer <ul><li>Inner</li></ul></li>
    <li><p>Wrapped</p></li>
  </ul>
  <p data-review-id="intro">Duplicate explicit ID</p>
  <h3>詳細</h3>
  <p>After</p>
</article></body></html>`)

	blocks, title, err := extractBlocks(document)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Anchor fixture" {
		t.Fatalf("title = %q", title)
	}
	want := []struct {
		id      string
		text    string
		section string
	}{
		{id: "intro", text: "Intro"},
		{id: "背景-2::p-0", text: "Before", section: "背景"},
		{id: "背景-2::li-0", text: "Outer", section: "背景"},
		{id: "背景-2::li-1", text: "Inner", section: "背景"},
		{id: "背景-2::p-1", text: "Wrapped", section: "背景"},
		{id: "intro-2", text: "Duplicate explicit ID", section: "背景"},
		{id: "詳細::p-0", text: "After", section: "背景 › 詳細"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("len(blocks) = %d, want %d: %#v", len(blocks), len(want), blocks)
	}
	for i, expected := range want {
		if blocks[i].id != expected.id || blocks[i].text != expected.text || strings.Join(blocks[i].section, " › ") != expected.section {
			t.Errorf("blocks[%d] = {%q, %q, %q}, want {%q, %q, %q}", i, blocks[i].id, blocks[i].text, strings.Join(blocks[i].section, " › "), expected.id, expected.text, expected.section)
		}
	}
}

func TestAnalyzeResolvesEveryMatchStatus(t *testing.T) {
	htmlBytes := []byte(`<!doctype html><title>Current title</title><article class="doc-body">
<p data-review-id="exact">Exact text</p>
<h2>Section</h2>
<p data-review-id="changed">Current replacement</p>
<p data-review-id="new-location">Moved text</p>
<p data-review-id="duplicate-a">Repeated text</p>
<p data-review-id="duplicate-b">Repeated text</p>
</article>`)
	review := reviewDocument{
		SchemaVersion: 1,
		Source: reviewSource{
			Path:   "report.html",
			SHA256: hashBytes([]byte("old source")),
			Title:  "Saved title",
		},
		Review: reviewMetadata{Path: "report.html.review.json"},
		Comments: []reviewEntry{
			reviewComment("exact", "Exact text", "Keep exact"),
			reviewComment("old-location", "Moved text", "Follow moved"),
			reviewComment("changed", "Previous wording", "Update changed"),
			reviewComment("unknown", "Repeated text", "Do not guess"),
			reviewComment("gone", "Missing text", "Report missing"),
		},
	}
	result, err := analyzeBytes(htmlBytes, mustMarshalReview(t, review))
	if err != nil {
		t.Fatal(err)
	}
	if result.Document != "Saved title" || result.SourceStatus != SourceChanged {
		t.Fatalf("result header = %#v", result)
	}
	wantStatuses := []MatchStatus{MatchExact, MatchRelocated, MatchChanged, MatchAmbiguous, MatchMissing}
	for i, status := range wantStatuses {
		if result.Comments[i].Status != status {
			t.Errorf("comments[%d].status = %q, want %q", i, result.Comments[i].Status, status)
		}
	}
	if moved := result.Comments[1]; moved.TargetID != "new-location" || moved.Target != "Moved text" {
		t.Errorf("relocated comment = %#v", moved)
	}
	if changed := result.Comments[2]; changed.Target != "Current replacement" || changed.ReviewedTarget != "Previous wording" || strings.Join(changed.Section, " › ") != "Section" {
		t.Errorf("changed comment = %#v", changed)
	}
	if ambiguous := result.Comments[3]; ambiguous.CandidateCount != 2 || ambiguous.TargetID != "" || ambiguous.ReviewedTarget != "Repeated text" {
		t.Errorf("ambiguous comment = %#v", ambiguous)
	}
	if missing := result.Comments[4]; missing.ReviewedTarget != "Missing text" || missing.Target != "" || missing.TargetID != "" {
		t.Errorf("missing comment = %#v", missing)
	}
	if exact := result.Comments[0]; exact.After != "Current replacement" {
		t.Errorf("exact context = %#v", exact)
	}
}

func TestAnalyzeReportsCurrentSource(t *testing.T) {
	htmlBytes := []byte(`<!doctype html><title>Current</title><body><p>Text</p></body>`)
	review := reviewDocument{
		SchemaVersion: 1,
		Source:        reviewSource{Path: "current.html", SHA256: hashBytes(htmlBytes)},
		Review:        reviewMetadata{Path: "current.html.review.json"},
		Comments:      []reviewEntry{reviewComment("preamble::p-0", "Text", "Comment")},
	}
	result, err := analyzeBytes(htmlBytes, mustMarshalReview(t, review))
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceStatus != SourceCurrent || result.Document != "Current" || result.Comments[0].Status != MatchExact {
		t.Fatalf("result = %#v", result)
	}
}

func TestAnalyzeRequiresSiblingReview(t *testing.T) {
	documentPath := filepath.Join(t.TempDir(), "report.html")
	if err := os.WriteFile(documentPath, []byte(`<body><p>Text</p></body>`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Analyze(documentPath)
	if err == nil || !strings.Contains(err.Error(), "report.html.review.json") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeReviewRejectsInvalidInput(t *testing.T) {
	valid := reviewDocument{
		SchemaVersion: 1,
		Source:        reviewSource{SHA256: hashBytes([]byte("source"))},
		Comments:      []reviewEntry{reviewComment("target", "Quote", "Comment")},
	}
	wrongHash := valid
	wrongHash.Comments = append([]reviewEntry(nil), valid.Comments...)
	wrongHash.Comments[0].Block.TextSHA256 = hashBytes([]byte("different"))
	wrongVersion := valid
	wrongVersion.SchemaVersion = 2

	tests := []struct {
		name     string
		contents []byte
		contains string
	}{
		{name: "malformed", contents: []byte(`{`), contains: "decode review"},
		{name: "unknown field", contents: []byte(`{"schemaVersion":1,"source":{"sha256":"` + hashBytes([]byte("source")) + `"},"review":{},"comments":[],"extra":true}`), contains: "unknown field"},
		{name: "trailing value", contents: append(mustMarshalReview(t, valid), []byte(` {}`)...), contains: "trailing JSON"},
		{name: "unsupported version", contents: mustMarshalReview(t, wrongVersion), contains: "unsupported"},
		{name: "wrong text hash", contents: mustMarshalReview(t, wrongHash), contains: "does not match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeReview(test.contents)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

func TestRenderersAreCompactAndStable(t *testing.T) {
	result := Result{
		Document:     "Report",
		SourceStatus: SourceChanged,
		Comments: []ResolvedComment{{
			Status:         MatchChanged,
			TargetID:       "section::p-0",
			Section:        []string{"Section", "Detail"},
			Before:         "Before",
			ReviewedTarget: "Previous target",
			Target:         "Target",
			After:          "After",
			Comment:        "First line\nSecond line",
		}},
	}
	var markdown bytes.Buffer
	if err := WriteMarkdown(&markdown, result); err != nil {
		t.Fatal(err)
	}
	wantFragments := []string{
		"# Review context: Report",
		"Source: changed",
		"Comments: 1; changed=1",
		"## 1. changed · `section::p-0`",
		"Section: Section › Detail",
		"> First line\n> Second line",
		"Reviewed target:\n> Previous target",
	}
	for _, fragment := range wantFragments {
		if !strings.Contains(markdown.String(), fragment) {
			t.Errorf("Markdown missing %q:\n%s", fragment, markdown.String())
		}
	}
	if strings.Contains(markdown.String(), "sha256:") {
		t.Errorf("Markdown leaked persistence metadata:\n%s", markdown.String())
	}

	var compact bytes.Buffer
	if err := WriteJSON(&compact, result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compact.String(), "\n  ") || !strings.Contains(compact.String(), `"sourceStatus":"changed"`) {
		t.Errorf("JSON is not compact: %q", compact.String())
	}
}

func TestClipTextPreservesUTF8(t *testing.T) {
	clipped := clipText(strings.Repeat("界", 10), 10)
	if !utf8.ValidString(clipped) || len(clipped) > 10 || !strings.HasSuffix(clipped, "…") {
		t.Fatalf("clipped = %q (%d bytes)", clipped, len(clipped))
	}
}

func TestExtractBlocksClipsQuoteBeforeHashing(t *testing.T) {
	longText := strings.Repeat("界", maxQuoteBytes)
	document := mustParseHTML(t, `<body><p>`+longText+`</p></body>`)
	blocks, _, err := extractBlocks(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || len(blocks[0].text) > maxQuoteBytes || !utf8.ValidString(blocks[0].text) {
		t.Fatalf("block text length = %d, valid UTF-8 = %t", len(blocks[0].text), utf8.ValidString(blocks[0].text))
	}
	if blocks[0].hash != hashBytes([]byte(blocks[0].text)) {
		t.Fatal("block hash was not computed from the browser-sized quote")
	}
}

func mustParseHTML(t *testing.T, source string) *html.Node {
	t.Helper()
	document, err := html.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func reviewComment(id, quote, comment string) reviewEntry {
	return reviewEntry{
		ID: "comment-" + id,
		Block: reviewBlock{
			ID:         id,
			SectionID:  "preamble",
			Quote:      quote,
			TextSHA256: hashBytes([]byte(normalizeText(quote))),
		},
		Comment: comment,
	}
}

func mustMarshalReview(t *testing.T, review reviewDocument) []byte {
	t.Helper()
	contents, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
