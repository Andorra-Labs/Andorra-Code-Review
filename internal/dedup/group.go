// Package dedup groups overlapping RawFindings into Findings before
// arbitration. Uses pure-Go heuristics: line-range IoU + Jaro-Winkler title
// similarity + exact existing_code match. No LLM, no embeddings (v2).
package dedup

import (
	"fmt"
	"strings"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/finding"
)

// Config tunes the merge heuristic. Default constructor mirrors the
// configstore defaults documented in the design.
type Config struct {
	LineOverlapMinRatio    float64
	TitleSimilarityMin     float64
	RequireSamePath        bool
	ExistingCodeExactBoost bool
}

// Default returns the recommended v1 thresholds.
func Default() Config {
	return Config{
		LineOverlapMinRatio:    0.5,
		TitleSimilarityMin:     0.7,
		RequireSamePath:        true,
		ExistingCodeExactBoost: true,
	}
}

// FromConfigStore converts a configstore.DedupConfig (which may be nil) into
// a Config. When cs is nil, returns Default(). When cs is non-nil, all fields
// come from cs directly — including booleans set to false, which would
// otherwise be silently overridden by the defaults via boolean OR.
func FromConfigStore(cs *configstore.DedupConfig) Config {
	if cs == nil {
		return Default()
	}
	cfg := Config{
		LineOverlapMinRatio:    cs.LineOverlapMinRatio,
		TitleSimilarityMin:     cs.TitleSimilarityMin,
		RequireSamePath:        cs.RequireSamePath,
		ExistingCodeExactBoost: cs.ExistingCodeExactBoost,
	}
	// Numeric zeroes are ambiguous (unset vs intentional 0). Treat zero as
	// unset and substitute the default to keep accidental-omission safe.
	if cfg.LineOverlapMinRatio == 0 {
		cfg.LineOverlapMinRatio = Default().LineOverlapMinRatio
	}
	if cfg.TitleSimilarityMin == 0 {
		cfg.TitleSimilarityMin = Default().TitleSimilarityMin
	}
	return cfg
}

// Group buckets raw findings by path, then merges within each bucket using
// union-find. Returns one Finding per cluster, with members in the same order
// the raw findings appeared and Sources deduplicated.
func Group(raw []finding.RawFinding, cfg Config) []finding.Finding {
	byPath := map[string][]int{}
	for i, r := range raw {
		key := r.Path
		if !cfg.RequireSamePath {
			key = "*"
		}
		byPath[key] = append(byPath[key], i)
	}

	groups := make([]finding.Finding, 0)
	gid := 0

	// Iterate paths in deterministic order so test fixtures are stable.
	for _, path := range sortedKeys(byPath) {
		idxs := byPath[path]
		uf := newUF(len(idxs))
		for i := 0; i < len(idxs); i++ {
			for j := i + 1; j < len(idxs); j++ {
				if shouldMerge(raw[idxs[i]], raw[idxs[j]], cfg) {
					uf.union(i, j)
				}
			}
		}
		clusters := uf.clusters()
		for _, c := range clusters {
			members := make([]finding.RawFinding, 0, len(c))
			for _, k := range c {
				members = append(members, raw[idxs[k]])
			}
			groups = append(groups, buildFinding(fmt.Sprintf("g-%d", gid), members))
			gid++
		}
	}
	return groups
}

// shouldMerge implements the v1 scoring rule.
func shouldMerge(a, b finding.RawFinding, cfg Config) bool {
	if cfg.ExistingCodeExactBoost && a.ExistingCode != "" && a.ExistingCode == b.ExistingCode {
		return true
	}
	overlap := lineIoU(a.StartLine, a.EndLine, b.StartLine, b.EndLine)
	if overlap >= 0.8 {
		return true
	}
	if overlap >= cfg.LineOverlapMinRatio {
		sim := jaroWinkler(normalizeTitle(a.Title), normalizeTitle(b.Title))
		if sim >= cfg.TitleSimilarityMin {
			return true
		}
	}
	return false
}

// lineIoU returns the intersection-over-union of two inclusive line ranges.
// Returns 0 if either range is degenerate (end < start), if either is the
// unresolved zero range (0,0) — line resolution failure leaves comments at
// 0-0 and we must not treat two such findings as "fully overlapping" — or if
// the inputs are negative.
func lineIoU(s1, e1, s2, e2 int) float64 {
	if s1 <= 0 && e1 <= 0 {
		return 0
	}
	if s2 <= 0 && e2 <= 0 {
		return 0
	}
	if e1 < s1 || e2 < s2 {
		return 0
	}
	if s1 < 0 || s2 < 0 {
		return 0
	}
	interStart := s1
	if s2 > interStart {
		interStart = s2
	}
	interEnd := e1
	if e2 < interEnd {
		interEnd = e2
	}
	if interEnd < interStart {
		return 0
	}
	intersection := interEnd - interStart + 1
	union := (e1 - s1 + 1) + (e2 - s2 + 1) - intersection
	if union <= 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// normalizeTitle lowercases and strips a tiny stopword set so simple
// re-phrasings like "the X is wrong" vs "X is wrong" merge correctly.
func normalizeTitle(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return ""
	}
	stopwords := map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "to": {}, "of": {}, "in": {}, "on": {}, "for": {},
	}
	parts := strings.Fields(t)
	out := parts[:0]
	for _, p := range parts {
		clean := strings.Trim(p, ".,:;!?\"'()[]{}")
		if _, ok := stopwords[clean]; ok {
			continue
		}
		out = append(out, clean)
	}
	return strings.Join(out, " ")
}

// buildFinding selects the group's representative title, line range, and
// deduped Sources list.
func buildFinding(gid string, members []finding.RawFinding) finding.Finding {
	if len(members) == 0 {
		return finding.Finding{GroupID: gid}
	}
	best := members[0]
	for _, m := range members[1:] {
		if m.Confidence > best.Confidence {
			best = m
		}
	}
	sources := make([]finding.Source, 0, len(members))
	seen := map[string]struct{}{}
	for _, m := range members {
		key := m.Source.Scanner + "|" + m.Source.Model
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		sources = append(sources, m.Source)
	}
	return finding.Finding{
		GroupID:   gid,
		Path:      best.Path,
		StartLine: best.StartLine,
		EndLine:   best.EndLine,
		Title:     best.Title,
		Members:   members,
		Sources:   sources,
	}
}

func sortedKeys(m map[string][]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// simple insertion sort; we never have many paths
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// --- union-find ---

type uf struct {
	parent []int
}

func newUF(n int) *uf {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &uf{parent: p}
}

func (u *uf) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *uf) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

func (u *uf) clusters() [][]int {
	buckets := map[int][]int{}
	for i := range u.parent {
		r := u.find(i)
		buckets[r] = append(buckets[r], i)
	}
	// deterministic order: by smallest member index
	roots := make([]int, 0, len(buckets))
	for r := range buckets {
		roots = append(roots, r)
	}
	for i := 1; i < len(roots); i++ {
		for j := i; j > 0 && buckets[roots[j-1]][0] > buckets[roots[j]][0]; j-- {
			roots[j-1], roots[j] = roots[j], roots[j-1]
		}
	}
	out := make([][]int, 0, len(roots))
	for _, r := range roots {
		out = append(out, buckets[r])
	}
	return out
}
