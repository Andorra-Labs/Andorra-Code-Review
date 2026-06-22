package finding

import (
	"strings"
	"testing"

	"github.com/open-code-review/open-code-review/internal/model"
)

func TestTokenUsageTotal(t *testing.T) {
	u := TokenUsage{InputTokens: 100, OutputTokens: 25, CacheReadTokens: 50}
	if u.Total() != 125 {
		t.Errorf("Total()=%d, want 125 (cache tokens should not be added)", u.Total())
	}
}

func TestTokenUsageAdd(t *testing.T) {
	a := TokenUsage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 2}
	b := TokenUsage{InputTokens: 200, OutputTokens: 20, CacheReadTokens: 8, CacheWriteTokens: 3}
	got := a.Add(b)
	want := TokenUsage{InputTokens: 300, OutputTokens: 30, CacheReadTokens: 13, CacheWriteTokens: 5}
	if got != want {
		t.Errorf("Add()=%+v, want %+v", got, want)
	}
}

func TestParseVerdict(t *testing.T) {
	if ParseVerdict("accepted_bug") != VerdictAccepted {
		t.Error("accepted_bug parse failed")
	}
	if ParseVerdict("nope") != "" {
		t.Error("unknown verdict should be empty")
	}
}

func TestExtractTitleFirstLine(t *testing.T) {
	got := extractTitle("Off-by-one in loop\n\nDetails follow on next line.")
	if got != "Off-by-one in loop" {
		t.Errorf("got %q", got)
	}
}

func TestExtractTitleTrims(t *testing.T) {
	got := extractTitle("   \n   leading whitespace\nmore")
	if got != "leading whitespace" {
		t.Errorf("got %q", got)
	}
}

func TestExtractTitleCaps(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := extractTitle(long)
	if len(got) != 120 {
		t.Errorf("len=%d, want 120", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected trailing ellipsis, got %q", got)
	}
}

func TestExtractTitleEmpty(t *testing.T) {
	if extractTitle("") != "" || extractTitle("   ") != "" {
		t.Error("empty/whitespace title should be empty")
	}
}

func TestFromCommentTagsSource(t *testing.T) {
	src := Source{Scanner: "opus", Provider: "anthropic", Model: "claude-opus-4-7"}
	c := model.LlmComment{
		Path:           "main.go",
		Content:        "Off-by-one in loop\n\nThis loop iterates one too many times.",
		StartLine:      10,
		EndLine:        15,
		ExistingCode:   "for i := 0; i <= n; i++ {",
		SuggestionCode: "for i := 0; i < n; i++ {",
		Thinking:       "noticed boundary condition",
	}
	r := FromComment(c, src, 7)
	if r.Path != "main.go" || r.StartLine != 10 || r.EndLine != 15 {
		t.Errorf("path/lines wrong: %+v", r)
	}
	if r.Title != "Off-by-one in loop" {
		t.Errorf("title=%q", r.Title)
	}
	if r.Detail != c.Content {
		t.Errorf("detail not full content: %q", r.Detail)
	}
	if r.Source != src {
		t.Errorf("source=%+v", r.Source)
	}
	if r.ExistingCode != c.ExistingCode || r.SuggestionCode != c.SuggestionCode {
		t.Errorf("code fields lost: %+v", r)
	}
	if r.Thinking != c.Thinking {
		t.Errorf("thinking lost: %q", r.Thinking)
	}
	if r.RawIndex != 7 {
		t.Errorf("RawIndex=%d", r.RawIndex)
	}
}

func TestFromCommentsBatch(t *testing.T) {
	src := Source{Scanner: "x"}
	in := []model.LlmComment{
		{Path: "a.go", Content: "bug1"},
		{Path: "b.go", Content: "bug2"},
	}
	out := FromComments(in, src)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0].RawIndex != 0 || out[1].RawIndex != 1 {
		t.Errorf("RawIndex sequence wrong")
	}
}

func TestSingletonWrapsRaw(t *testing.T) {
	r := RawFinding{
		Path: "x.go", StartLine: 1, EndLine: 2, Title: "t",
		Source: Source{Scanner: "s"},
	}
	g := Singleton(r, "g-1")
	if g.GroupID != "g-1" || g.Path != "x.go" {
		t.Errorf("group=%+v", g)
	}
	if len(g.Members) != 1 || g.Members[0].RawIndex != r.RawIndex {
		t.Errorf("members wrong: %+v", g.Members)
	}
	if len(g.Sources) != 1 || g.Sources[0].Scanner != "s" {
		t.Errorf("sources wrong: %+v", g.Sources)
	}
}

func mkFinal(verdict Verdict, members ...RawFinding) FinalFinding {
	srcs := make([]Source, 0, len(members))
	seen := map[string]struct{}{}
	for _, m := range members {
		key := m.Source.Scanner + "|" + m.Source.Model
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		srcs = append(srcs, m.Source)
	}
	var path string
	var sl, el int
	var title string
	if len(members) > 0 {
		path = members[0].Path
		sl = members[0].StartLine
		el = members[0].EndLine
		title = members[0].Title
	}
	return FinalFinding{
		Finding: Finding{
			GroupID: "g", Path: path, StartLine: sl, EndLine: el,
			Title: title, Members: members, Sources: srcs,
		},
		Verdict:    verdict,
		Confidence: 0.87,
	}
}

func TestToCommentDefault(t *testing.T) {
	r := RawFinding{
		Path: "f.go", StartLine: 1, EndLine: 1, Title: "bug",
		Detail: "full detail text",
		Source: Source{Scanner: "opus"},
	}
	out := ToComment(mkFinal(VerdictAccepted, r), RenderOptions{})
	if strings.Contains(out.Content, "[accepted_bug]") {
		t.Errorf("default render should not show verdict: %q", out.Content)
	}
	if strings.Contains(out.Content, "scanners:") {
		t.Errorf("default render should not show provenance: %q", out.Content)
	}
	if out.Content != "full detail text" {
		t.Errorf("content=%q, want full detail", out.Content)
	}
}

func TestToCommentShowVerdict(t *testing.T) {
	r := RawFinding{Path: "f.go", Detail: "d", Source: Source{Scanner: "x"}}
	out := ToComment(mkFinal(VerdictRejected, r), RenderOptions{ShowVerdict: true})
	if !strings.HasPrefix(out.Content, "[rejected_fp] ") {
		t.Errorf("content=%q", out.Content)
	}
}

func TestToCommentShowProvenance(t *testing.T) {
	r1 := RawFinding{Path: "f.go", Detail: "d", Source: Source{Scanner: "opus"}}
	r2 := RawFinding{Path: "f.go", Detail: "d longer", Source: Source{Scanner: "gpt"}}
	out := ToComment(mkFinal(VerdictAccepted, r1, r2), RenderOptions{ShowProvenance: true})
	if !strings.Contains(out.Content, "scanners: opus, gpt") {
		t.Errorf("missing scanner list: %q", out.Content)
	}
	if !strings.Contains(out.Content, "conf 0.87") {
		t.Errorf("missing confidence: %q", out.Content)
	}
}

func TestToCommentPicksLongestDetail(t *testing.T) {
	r1 := RawFinding{Path: "f.go", Detail: "short", Source: Source{Scanner: "a"}}
	r2 := RawFinding{Path: "f.go", Detail: "much longer detail body", Source: Source{Scanner: "b"}}
	out := ToComment(mkFinal(VerdictAccepted, r1, r2), RenderOptions{})
	if !strings.Contains(out.Content, "much longer detail body") {
		t.Errorf("did not pick longest detail: %q", out.Content)
	}
}

func TestToCommentPicksHighestConfidenceCode(t *testing.T) {
	r1 := RawFinding{Path: "f.go", Detail: "d", Confidence: 0.3, ExistingCode: "a", SuggestionCode: "b"}
	r2 := RawFinding{Path: "f.go", Detail: "d", Confidence: 0.9, ExistingCode: "x", SuggestionCode: "y"}
	out := ToComment(mkFinal(VerdictAccepted, r1, r2), RenderOptions{})
	if out.ExistingCode != "x" || out.SuggestionCode != "y" {
		t.Errorf("code picked wrong: %+v", out)
	}
}

func TestToCommentEmptyMembers(t *testing.T) {
	out := ToComment(FinalFinding{
		Finding: Finding{GroupID: "g", Path: "f.go", Title: "title-only"},
		Verdict: VerdictAccepted,
	}, RenderOptions{})
	if out.Content != "title-only" {
		t.Errorf("expected title fallback, got %q", out.Content)
	}
}

func TestFromCommentPrefersLLMTitle(t *testing.T) {
	c := model.LlmComment{
		Path: "f.go", Content: "First line of content\n\nBody",
		Title:    "Explicit title from LLM",
		Severity: "P1",
	}
	r := FromComment(c, Source{Scanner: "opus"}, 0)
	if r.Title != "Explicit title from LLM" {
		t.Errorf("title=%q, want explicit", r.Title)
	}
	if r.Severity != "P1" {
		t.Errorf("severity=%q, want P1", r.Severity)
	}
}

func TestFromCommentFallsBackToContentFirstLine(t *testing.T) {
	c := model.LlmComment{Path: "f.go", Content: "Off-by-one in loop\n\nDetail"}
	r := FromComment(c, Source{Scanner: "opus"}, 0)
	if r.Title != "Off-by-one in loop" {
		t.Errorf("title=%q, want first-line fallback", r.Title)
	}
	if r.Severity != "" {
		t.Errorf("severity=%q, want empty when LLM omits it", r.Severity)
	}
}

func TestToCommentPropagatesTitleAndSeverity(t *testing.T) {
	r1 := RawFinding{Path: "f.go", Detail: "d", Severity: "P2", Confidence: 0.4}
	r2 := RawFinding{Path: "f.go", Detail: "d longer", Severity: "P1", Confidence: 0.9}
	f := mkFinal(VerdictAccepted, r1, r2)
	f.Title = "Gate routing on enabled scanners"
	out := ToComment(f, RenderOptions{})
	if out.Title != "Gate routing on enabled scanners" {
		t.Errorf("title=%q", out.Title)
	}
	// Higher-confidence member wins on severity.
	if out.Severity != "P1" {
		t.Errorf("severity=%q, want P1 (highest-confidence wins)", out.Severity)
	}
}

func TestToCommentSeverityEmptyWhenAllMembersUnset(t *testing.T) {
	r1 := RawFinding{Path: "f.go", Detail: "d"}
	r2 := RawFinding{Path: "f.go", Detail: "d2"}
	out := ToComment(mkFinal(VerdictAccepted, r1, r2), RenderOptions{})
	if out.Severity != "" {
		t.Errorf("severity=%q, want empty", out.Severity)
	}
}
