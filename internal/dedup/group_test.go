package dedup

import (
	"strings"
	"testing"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/finding"
)

func mkRaw(scanner, path, title string, start, end int, code string) finding.RawFinding {
	return finding.RawFinding{
		Path:         path,
		StartLine:    start,
		EndLine:      end,
		Title:        title,
		Detail:       title,
		ExistingCode: code,
		Source:       finding.Source{Scanner: scanner, Provider: "p", Model: "m"},
	}
}

func TestJaroWinklerIdentical(t *testing.T) {
	if jaroWinkler("hello", "hello") != 1.0 {
		t.Error("identical should be 1.0")
	}
}

func TestJaroWinklerSimilar(t *testing.T) {
	if got := jaroWinkler("MARTHA", "MARHTA"); got < 0.95 {
		t.Errorf("MARTHA/MARHTA = %f, want >= 0.95", got)
	}
}

func TestJaroWinklerDifferent(t *testing.T) {
	if got := jaroWinkler("apple", "carrot"); got > 0.6 {
		t.Errorf("apple/carrot = %f, want < 0.6", got)
	}
}

func TestJaroWinklerEmpty(t *testing.T) {
	if jaroWinkler("", "x") != 0 || jaroWinkler("x", "") != 0 {
		t.Error("empty should be 0")
	}
}

func TestNormalizeTitleStripsStopwords(t *testing.T) {
	if got := normalizeTitle("The function IS not safe."); got != "function not safe" {
		t.Errorf("got %q", got)
	}
}

func TestLineIoUFullOverlap(t *testing.T) {
	if lineIoU(10, 20, 10, 20) != 1.0 {
		t.Error("identical ranges should be 1.0")
	}
}

func TestLineIoUPartialOverlap(t *testing.T) {
	got := lineIoU(10, 19, 15, 24)
	want := 5.0 / 15.0 // intersection 15..19 (5 lines), union 10..24 (15 lines)
	if absDelta(got, want) > 0.01 {
		t.Errorf("got %f, want ~%f", got, want)
	}
}

func TestLineIoUDisjoint(t *testing.T) {
	if lineIoU(10, 15, 20, 25) != 0 {
		t.Error("disjoint should be 0")
	}
}

func TestLineIoUDegenerate(t *testing.T) {
	if lineIoU(10, 5, 5, 10) != 0 {
		t.Error("inverted should be 0")
	}
}

func TestGroupIdenticalFindings(t *testing.T) {
	raw := []finding.RawFinding{
		mkRaw("opus", "a.go", "Off-by-one in loop", 10, 12, "for i:=0;i<=n;i++{"),
		mkRaw("gpt", "a.go", "Off-by-one in loop", 10, 12, "for i:=0;i<=n;i++{"),
	}
	groups := Group(raw, Default())
	if len(groups) != 1 {
		t.Fatalf("len=%d, want 1; %+v", len(groups), groups)
	}
	if len(groups[0].Members) != 2 {
		t.Errorf("members=%d, want 2", len(groups[0].Members))
	}
	if len(groups[0].Sources) != 2 {
		t.Errorf("sources=%d, want 2", len(groups[0].Sources))
	}
}

func TestGroupMergesOnExistingCode(t *testing.T) {
	raw := []finding.RawFinding{
		mkRaw("opus", "a.go", "Race condition", 50, 51, "go func(){mutate()}()"),
		mkRaw("gpt", "a.go", "Concurrent map write", 99, 99, "go func(){mutate()}()"),
	}
	groups := Group(raw, Default())
	if len(groups) != 1 {
		t.Errorf("len=%d, want 1 (merged by code_match boost); %+v", len(groups), groups)
	}
}

func TestGroupDoesNotMergeAcrossPaths(t *testing.T) {
	raw := []finding.RawFinding{
		mkRaw("opus", "a.go", "same bug", 10, 12, ""),
		mkRaw("gpt", "b.go", "same bug", 10, 12, ""),
	}
	if got := Group(raw, Default()); len(got) != 2 {
		t.Errorf("len=%d, want 2", len(got))
	}
}

func TestGroupMergesOnHighOverlap(t *testing.T) {
	// Overlap exceeds 0.8 → merges regardless of titles
	raw := []finding.RawFinding{
		mkRaw("opus", "a.go", "describes the bug well", 10, 20, ""),
		mkRaw("gpt", "a.go", "different wording entirely", 10, 20, ""),
	}
	if got := Group(raw, Default()); len(got) != 1 {
		t.Errorf("len=%d, want 1 (full overlap); %+v", len(got), got)
	}
}

func TestGroupDoesNotMergeAdjacentDifferentBugs(t *testing.T) {
	// Disjoint line ranges + different titles → separate groups
	raw := []finding.RawFinding{
		mkRaw("opus", "a.go", "unrelated thing A", 10, 11, ""),
		mkRaw("gpt", "a.go", "completely separate thing B", 50, 51, ""),
	}
	if got := Group(raw, Default()); len(got) != 2 {
		t.Errorf("len=%d, want 2", len(got))
	}
}

func TestGroupMergesOnTitleSimAndPartialOverlap(t *testing.T) {
	// IoU(10..19, 12..21) = 8/12 ≈ 0.67 > 0.5 default threshold
	// titles are near-identical → JW > 0.7
	raw := []finding.RawFinding{
		mkRaw("opus", "a.go", "potential null pointer dereference", 10, 19, ""),
		mkRaw("gpt", "a.go", "possible null pointer dereference", 12, 21, ""),
	}
	if got := Group(raw, Default()); len(got) != 1 {
		t.Errorf("len=%d, want 1 (partial overlap + title sim); %+v", len(got), got)
	}
}

func TestGroupSingletonsPassThrough(t *testing.T) {
	raw := []finding.RawFinding{
		mkRaw("opus", "a.go", "bug-1", 10, 11, ""),
	}
	got := Group(raw, Default())
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	if len(got[0].Members) != 1 {
		t.Errorf("members=%d, want 1", len(got[0].Members))
	}
}

func TestGroupStableIDs(t *testing.T) {
	raw := []finding.RawFinding{
		mkRaw("a", "a.go", "x", 1, 1, ""),
		mkRaw("b", "a.go", "y", 50, 51, ""),
	}
	g1 := Group(raw, Default())
	g2 := Group(raw, Default())
	if len(g1) != len(g2) {
		t.Fatalf("length mismatch")
	}
	for i := range g1 {
		if g1[i].GroupID != g2[i].GroupID {
			t.Errorf("non-deterministic GroupID: %s vs %s", g1[i].GroupID, g2[i].GroupID)
		}
		if !strings.HasPrefix(g1[i].GroupID, "g-") {
			t.Errorf("unexpected gid format: %s", g1[i].GroupID)
		}
	}
}

func TestLineIoUUnresolvedRangeReturnsZero(t *testing.T) {
	// When the diff-resolver fails, comments keep StartLine=0, EndLine=0.
	// Two such unresolved findings on the same file must NOT merge purely
	// because their (0,0) ranges nominally overlap 100%.
	if got := lineIoU(0, 0, 0, 0); got != 0 {
		t.Errorf("lineIoU(0,0,0,0)=%f, want 0", got)
	}
	if got := lineIoU(0, 0, 10, 20); got != 0 {
		t.Errorf("lineIoU(0,0,10,20)=%f, want 0", got)
	}
	if got := lineIoU(10, 20, 0, 0); got != 0 {
		t.Errorf("lineIoU(10,20,0,0)=%f, want 0", got)
	}
}

func TestGroupDoesNotMergeUnresolvedFindings(t *testing.T) {
	// Two distinct scanner findings whose line ranges failed to resolve
	// (0-0) must stay in separate groups absent a shared existing_code.
	raw := []finding.RawFinding{
		mkRaw("opus", "a.go", "concern about loop boundary", 0, 0, ""),
		mkRaw("gpt", "a.go", "totally unrelated nil deref", 0, 0, ""),
	}
	if got := Group(raw, Default()); len(got) != 2 {
		t.Errorf("len=%d, want 2 (unresolved ranges should not merge); %+v", len(got), got)
	}
}

func TestFromConfigStoreAllowsFalse(t *testing.T) {
	// User explicitly disables RequireSamePath and ExistingCodeExactBoost.
	// Previously these were silently re-asserted to true by `||` against
	// the defaults.
	cs := &configstore.DedupConfig{
		LineOverlapMinRatio:    0.4,
		TitleSimilarityMin:     0.6,
		RequireSamePath:        false,
		ExistingCodeExactBoost: false,
	}
	cfg := FromConfigStore(cs)
	if cfg.RequireSamePath {
		t.Error("RequireSamePath=true, want false (user explicitly disabled it)")
	}
	if cfg.ExistingCodeExactBoost {
		t.Error("ExistingCodeExactBoost=true, want false")
	}
	if cfg.LineOverlapMinRatio != 0.4 {
		t.Errorf("LineOverlapMinRatio=%f, want 0.4", cfg.LineOverlapMinRatio)
	}
}

func TestFromConfigStoreNilReturnsDefault(t *testing.T) {
	cfg := FromConfigStore(nil)
	def := Default()
	if cfg != def {
		t.Errorf("FromConfigStore(nil)=%+v, want %+v", cfg, def)
	}
}

func TestFromConfigStoreZeroNumericsFallBackToDefault(t *testing.T) {
	// Boolean zero-value (false) is honored exactly. Numeric zeroes remain
	// ambiguous — accidentally omitting the field shouldn't disable dedup
	// entirely — so we substitute the default for zero numerics.
	cs := &configstore.DedupConfig{}
	cfg := FromConfigStore(cs)
	def := Default()
	if cfg.LineOverlapMinRatio != def.LineOverlapMinRatio {
		t.Errorf("zero overlap ratio not substituted: %f", cfg.LineOverlapMinRatio)
	}
	if cfg.TitleSimilarityMin != def.TitleSimilarityMin {
		t.Errorf("zero title sim not substituted: %f", cfg.TitleSimilarityMin)
	}
	// Bools stay at their zero value (false).
	if cfg.RequireSamePath || cfg.ExistingCodeExactBoost {
		t.Error("booleans should remain false (zero value), not default to true")
	}
}

func TestGroupPicksHighestConfidenceTitle(t *testing.T) {
	raw := []finding.RawFinding{
		{Path: "a.go", StartLine: 10, EndLine: 10, Title: "low-conf title",
			Source: finding.Source{Scanner: "a"}, Confidence: 0.2},
		{Path: "a.go", StartLine: 10, EndLine: 10, Title: "high-conf title",
			Source: finding.Source{Scanner: "b"}, Confidence: 0.9},
	}
	got := Group(raw, Default())
	if len(got) != 1 {
		t.Fatalf("expected 1 group")
	}
	if got[0].Title != "high-conf title" {
		t.Errorf("title=%q", got[0].Title)
	}
}

func TestGroupPrefersResolvedLineRange(t *testing.T) {
	// When one scanner could not resolve existing_code back to a line (0,0)
	// but another did, the group representative should carry the resolved
	// range so the finding can render inline rather than as unresolved.
	raw := []finding.RawFinding{
		{Path: "a.go", StartLine: 0, EndLine: 0, Title: "unresolved",
			Source: finding.Source{Scanner: "a"}, Confidence: 0.0},
		{Path: "a.go", StartLine: 15, EndLine: 17, Title: "resolved",
			Source: finding.Source{Scanner: "b"}, Confidence: 0.0},
	}
	got := Group(raw, Default())
	if len(got) != 1 {
		t.Fatalf("expected 1 group")
	}
	if got[0].StartLine != 15 || got[0].EndLine != 17 {
		t.Errorf("range=%d-%d, want 15-17", got[0].StartLine, got[0].EndLine)
	}
}

func absDelta(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}
