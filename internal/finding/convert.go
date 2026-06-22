package finding

import (
	"fmt"
	"strings"

	"github.com/open-code-review/open-code-review/internal/model"
)

// FromComment converts an upstream LlmComment into a RawFinding tagged with the
// given scanner source. Title prefers the LLM-supplied c.Title (and sets
// ExplicitTitle = true); when absent it falls back to the first non-empty line
// of Content (capped at 120 chars) with ExplicitTitle = false, so dedup's
// title-similarity match still works but ToComment knows not to surface the
// synthesized title as a duplicate header.
// Detail is the full Content.
func FromComment(c model.LlmComment, src Source, idx int) RawFinding {
	title := strings.TrimSpace(c.Title)
	explicit := title != ""
	if title == "" {
		title = extractTitle(c.Content)
	}
	return RawFinding{
		Path:           c.Path,
		StartLine:      c.StartLine,
		EndLine:        c.EndLine,
		Title:          title,
		ExplicitTitle:  explicit,
		Detail:         c.Content,
		ExistingCode:   c.ExistingCode,
		SuggestionCode: c.SuggestionCode,
		Source:         src,
		Severity:       c.Severity,
		Thinking:       c.Thinking,
		RawIndex:       idx,
	}
}

// FromComments is a convenience wrapper that converts a whole slice.
func FromComments(cs []model.LlmComment, src Source) []RawFinding {
	out := make([]RawFinding, 0, len(cs))
	for i, c := range cs {
		out = append(out, FromComment(c, src, i))
	}
	return out
}

// Singleton wraps one RawFinding as a Finding of size 1 with the given group id.
func Singleton(r RawFinding, groupID string) Finding {
	return Finding{
		GroupID:   groupID,
		Path:      r.Path,
		StartLine: r.StartLine,
		EndLine:   r.EndLine,
		Title:     r.Title,
		Members:   []RawFinding{r},
		Sources:   []Source{r.Source},
	}
}

// ToComment flattens a FinalFinding back into an upstream LlmComment for
// legacy rendering. When opts.ShowVerdict or opts.ShowProvenance is set the
// content is prefixed with a single annotation line. Existing/Suggestion code
// comes from the highest-confidence member (or the first member if all are 0).
func ToComment(f FinalFinding, opts RenderOptions) model.LlmComment {
	body := combineDetail(f.Finding)
	var prefix string
	if opts.ShowVerdict && f.Verdict != "" {
		prefix = fmt.Sprintf("[%s] ", f.Verdict)
	}
	if opts.ShowProvenance && len(f.Sources) > 0 {
		names := make([]string, len(f.Sources))
		for i, s := range f.Sources {
			names[i] = s.Scanner
		}
		prefix += fmt.Sprintf("scanners: %s | conf %.2f\n", strings.Join(names, ", "), f.Confidence)
	}
	content := body
	if prefix != "" {
		content = prefix + content
	}
	existing, suggestion := pickCodeFields(f.Finding)
	return model.LlmComment{
		Path:           f.Path,
		Content:        content,
		SuggestionCode: suggestion,
		ExistingCode:   existing,
		StartLine:      f.StartLine,
		EndLine:        f.EndLine,
		Title:          pickExplicitTitle(f.Finding),
		Severity:       pickSeverity(f.Finding),
	}
}

// pickExplicitTitle returns the highest-confidence LLM-supplied title across
// the Finding's members, or "" if no member supplied one. Synthesized titles
// (extracted from Content's first line) are deliberately excluded so the PR
// renderer doesn't prefix a bold header that duplicates the comment body.
func pickExplicitTitle(f Finding) string {
	var best *RawFinding
	for i := range f.Members {
		m := &f.Members[i]
		if !m.ExplicitTitle {
			continue
		}
		if best == nil || m.Confidence > best.Confidence {
			best = m
		}
	}
	if best == nil {
		return ""
	}
	return best.Title
}

// pickSeverity returns one of the member-supplied severities for the group.
// Strategy: highest confidence wins; on confidence ties (including the common
// case where every member reports confidence 0 because scanners don't fill it
// in), the WORST severity wins (P1 > P2 > P3). Without the rank tie-breaker
// the result would be order-dependent — a P3-tagged member running first
// could shadow a P1 from another scanner.
func pickSeverity(f Finding) string {
	if len(f.Members) == 0 {
		return ""
	}
	var best *RawFinding
	for i := range f.Members {
		m := &f.Members[i]
		if m.Severity == "" {
			continue
		}
		if best == nil {
			best = m
			continue
		}
		if m.Confidence > best.Confidence {
			best = m
			continue
		}
		if m.Confidence == best.Confidence && severityRank(m.Severity) < severityRank(best.Severity) {
			best = m
		}
	}
	if best == nil {
		return ""
	}
	return best.Severity
}

// severityRank maps a severity string to an integer ordering where LOWER
// means worse (P1=1, P2=2, P3=3). Unknown / unset values sort last (99) so
// they never win a tie-break against a known severity.
func severityRank(s string) int {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	}
	return 99
}

func combineDetail(f Finding) string {
	if len(f.Members) == 0 {
		return f.Title
	}
	best := f.Members[0]
	for _, m := range f.Members[1:] {
		if len(m.Detail) > len(best.Detail) {
			best = m
		}
	}
	return best.Detail
}

func pickCodeFields(f Finding) (existing, suggestion string) {
	if len(f.Members) == 0 {
		return "", ""
	}
	// Prefer the highest-confidence member with non-empty existing_code.
	var bestExisting, bestSuggestion *RawFinding
	for i := range f.Members {
		m := &f.Members[i]
		if m.ExistingCode != "" && (bestExisting == nil || m.Confidence > bestExisting.Confidence) {
			bestExisting = m
		}
		if m.SuggestionCode != "" && (bestSuggestion == nil || m.Confidence > bestSuggestion.Confidence) {
			bestSuggestion = m
		}
	}
	if bestExisting != nil {
		existing = bestExisting.ExistingCode
	}
	if bestSuggestion != nil {
		suggestion = bestSuggestion.SuggestionCode
	}
	return existing, suggestion
}

func extractTitle(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	first := content
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		first = content[:i]
	}
	first = strings.TrimSpace(first)
	const maxLen = 120
	if len(first) > maxLen {
		first = first[:maxLen-3] + "..."
	}
	return first
}
