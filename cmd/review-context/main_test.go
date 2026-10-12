package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	documentPath := filepath.Join(dir, "report.html")
	htmlBytes := []byte(`<!doctype html><title>Report</title><body><p data-review-id="target">Text</p></body>`)
	if err := os.WriteFile(documentPath, htmlBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	review := map[string]any{
		"schemaVersion": 1,
		"source": map[string]any{
			"path":   "report.html",
			"sha256": sha256String(htmlBytes),
			"title":  "Report",
		},
		"review": map[string]any{
			"path":        "report.html.review.json",
			"submittedAt": "2026-09-25T00:00:00Z",
		},
		"comments": []any{},
	}
	reviewBytes, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(documentPath+".review.json", reviewBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("markdown", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{documentPath}, &stdout, &stderr); code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "# Review context: Report") || !strings.Contains(stdout.String(), "Source: current") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})

	t.Run("json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--format", "json", documentPath}, &stdout, &stderr); code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"sourceStatus":"current"`) {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})

	t.Run("archived review", func(t *testing.T) {
		archived := filepath.Join(dir, "archived.json")
		if err := os.WriteFile(archived, reviewBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(documentPath + ".review.json"); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--review", archived, documentPath}, &stdout, &stderr); code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Source: current") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
}

func TestRunRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"--format", "xml", "report.html"}, {"one.html", "two.html"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
	}
}

func sha256String(contents []byte) string {
	// Keep this CLI test independent of the internal package's private hash
	// helper while producing the persisted review format.
	return "sha256:" + fmt.Sprintf("%x", sha256.Sum256(contents))
}
