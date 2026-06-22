package main

import (
	"strings"
	"testing"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/dedup"
	"github.com/open-code-review/open-code-review/internal/ensemble"
	"github.com/open-code-review/open-code-review/internal/llm"
	"github.com/open-code-review/open-code-review/internal/model"
)

func TestAppendPriorFindingsContext_EmptyPriorIsNoop(t *testing.T) {
	got := appendPriorFindingsContext("orig", nil)
	if got != "orig" {
		t.Errorf("got %q, want unchanged %q", got, "orig")
	}
}

func TestAppendPriorFindingsContext_FormatsBulletList(t *testing.T) {
	prior := []model.LlmComment{
		{Path: "a.go", StartLine: 10, EndLine: 12, Content: "Null deref when x is nil\nmore detail"},
		{Path: "b.go", StartLine: 1, EndLine: 1, Content: "Off-by-one in loop"},
	}
	got := appendPriorFindingsContext("background here", prior)
	if !strings.HasPrefix(got, "background here\n\n") {
		t.Errorf("prior background not preserved as prefix: %q", got)
	}
	if !strings.Contains(got, "ADDITIONAL bugs") {
		t.Errorf("missing 'find additional bugs' steering: %q", got)
	}
	if !strings.Contains(got, "- [a.go:10-12] Null deref when x is nil") {
		t.Errorf("missing first bullet (title only, no body): %q", got)
	}
	if !strings.Contains(got, "- [b.go:1-1] Off-by-one in loop") {
		t.Errorf("missing second bullet: %q", got)
	}
	if strings.Contains(got, "more detail") {
		t.Errorf("body beyond title should not be included: %q", got)
	}
}

func TestAppendPriorFindingsContext_BlankBackgroundOmitsLeadingNewlines(t *testing.T) {
	prior := []model.LlmComment{{Path: "a.go", StartLine: 1, EndLine: 1, Content: "bug"}}
	got := appendPriorFindingsContext("   ", prior)
	if strings.HasPrefix(got, "\n") {
		t.Errorf("should not start with newline when background is blank: %q", got)
	}
}

func testScannerEndpoint() ensemble.ScannerEndpoint {
	return ensemble.ScannerEndpoint{
		Spec:     configstore.ScannerSpec{Name: "test", Provider: "openai"},
		Endpoint: llm.ResolvedEndpoint{Model: "gpt-test"},
	}
}

func TestHasNewFindings_EmptyPriorReturnsTrueForAnyNext(t *testing.T) {
	sep := testScannerEndpoint()
	next := []model.LlmComment{{Path: "a.go", StartLine: 1, EndLine: 1, Content: "bug"}}
	if !hasNewFindings(nil, next, sep, dedup.Default()) {
		t.Error("expected new findings when prior is empty")
	}
}

func TestHasNewFindings_EmptyNextReturnsFalse(t *testing.T) {
	sep := testScannerEndpoint()
	prior := []model.LlmComment{{Path: "a.go", StartLine: 1, EndLine: 1, Content: "bug"}}
	if hasNewFindings(prior, nil, sep, dedup.Default()) {
		t.Error("expected no new findings when next is empty")
	}
}

func TestHasNewFindings_DuplicateNextReturnsFalse(t *testing.T) {
	sep := testScannerEndpoint()
	prior := []model.LlmComment{{Path: "a.go", StartLine: 1, EndLine: 1, Content: "bug"}}
	next := []model.LlmComment{{Path: "a.go", StartLine: 1, EndLine: 1, Content: "same bug"}}
	if hasNewFindings(prior, next, sep, dedup.Default()) {
		t.Error("expected duplicate finding not to count as new")
	}
}

func TestHasNewFindings_NewFindingReturnsTrue(t *testing.T) {
	sep := testScannerEndpoint()
	prior := []model.LlmComment{{Path: "a.go", StartLine: 1, EndLine: 1, Content: "bug"}}
	next := []model.LlmComment{{Path: "b.go", StartLine: 5, EndLine: 5, Content: "different bug"}}
	if !hasNewFindings(prior, next, sep, dedup.Default()) {
		t.Error("expected different finding to count as new")
	}
}
