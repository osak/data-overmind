package reviewcontext

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// WriteMarkdown writes the default compact agent-facing representation.
func WriteMarkdown(w io.Writer, result Result) error {
	counts := make(map[MatchStatus]int)
	for _, comment := range result.Comments {
		counts[comment.Status]++
	}
	if _, err := fmt.Fprintf(w, "# Review context: %s\n\nSource: %s\nComments: %d", result.Document, result.SourceStatus, len(result.Comments)); err != nil {
		return err
	}
	for _, status := range []MatchStatus{MatchExact, MatchRelocated, MatchChanged, MatchAmbiguous, MatchMissing} {
		if counts[status] > 0 {
			if _, err := fmt.Fprintf(w, "; %s=%d", status, counts[status]); err != nil {
				return err
			}
		}
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}

	for i, comment := range result.Comments {
		if _, err := fmt.Fprintf(w, "\n## %d. %s", i+1, comment.Status); err != nil {
			return err
		}
		if comment.TargetID != "" {
			if _, err := fmt.Fprintf(w, " · `%s`", escapeCodeSpan(comment.TargetID)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
		if len(comment.Section) > 0 {
			if _, err := fmt.Fprintf(w, "Section: %s\n", strings.Join(comment.Section, " › ")); err != nil {
				return err
			}
		}
		if comment.CandidateCount > 0 {
			if _, err := fmt.Fprintf(w, "Candidates: %d\n", comment.CandidateCount); err != nil {
				return err
			}
		}
		for _, field := range []struct {
			label string
			value string
		}{
			{label: "Before", value: comment.Before},
			{label: "Reviewed target", value: comment.ReviewedTarget},
			{label: "Target", value: comment.Target},
			{label: "After", value: comment.After},
			{label: "Comment", value: comment.Comment},
		} {
			if field.value == "" {
				continue
			}
			if _, err := fmt.Fprintf(w, "%s:\n%s\n", field.label, blockquote(field.value)); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteJSON writes the compact structured representation without persistence
// metadata from the source review file.
func WriteJSON(w io.Writer, result Result) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func blockquote(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = "> " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func escapeCodeSpan(value string) string {
	return strings.ReplaceAll(value, "`", "'")
}
