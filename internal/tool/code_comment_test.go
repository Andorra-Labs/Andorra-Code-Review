package tool

import (
	"testing"
)

func TestParseCommentsExtractsTitleAndSeverity(t *testing.T) {
	args := map[string]any{
		"path": "f.go",
		"comments": []any{
			map[string]any{
				"title":         "Off-by-one in slice index",
				"severity":      "P1",
				"content":       "The index can exceed len(s) when n is zero.",
				"existing_code": "return s[n-1]",
			},
		},
	}
	out, msg := ParseComments(args)
	if msg != "" {
		t.Fatalf("ParseComments error: %s", msg)
	}
	if len(out) != 1 {
		t.Fatalf("got %d comments, want 1", len(out))
	}
	got := out[0]
	if got.Title != "Off-by-one in slice index" {
		t.Errorf("title=%q", got.Title)
	}
	if got.Severity != "P1" {
		t.Errorf("severity=%q", got.Severity)
	}
	if got.Content == "" || got.ExistingCode == "" {
		t.Errorf("required fields lost: %+v", got)
	}
}

func TestParseCommentsAcceptsMissingTitleAndSeverity(t *testing.T) {
	args := map[string]any{
		"path": "f.go",
		"comments": []any{
			map[string]any{
				"content":       "Body",
				"existing_code": "x := 1",
			},
		},
	}
	out, msg := ParseComments(args)
	if msg != "" {
		t.Fatalf("ParseComments error: %s", msg)
	}
	if len(out) != 1 {
		t.Fatalf("got %d comments, want 1", len(out))
	}
	if out[0].Title != "" || out[0].Severity != "" {
		t.Errorf("expected empty optional fields, got title=%q severity=%q", out[0].Title, out[0].Severity)
	}
}
