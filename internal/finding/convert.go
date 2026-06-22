package finding

import (
	"fmt"
	"strings"

	"github.com/open-code-review/open-code-review/internal/model"
)

// FromComment converts an upstream LlmComment into a RawFinding tagged with the
// given scanner source. Title is the first non-empty line of Content (capped at
// 120 chars). Detail is the full Content.
func FromComment(c model.LlmComment, src Source, idx int) RawFinding {
	return RawFinding{
		Path:           c.Path,
		StartLine:      c.StartLine,
		EndLine:        c.EndLine,
		Title:          extractTitle(c.Content),
		Detail:         c.Content,
		ExistingCode:   c.ExistingCode,
		SuggestionCode: c.SuggestionCode,
		Source:         src,
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
	}
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
