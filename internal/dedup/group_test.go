package dedup

import (
	"strings"
	"testing"

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

func absDelta(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}
