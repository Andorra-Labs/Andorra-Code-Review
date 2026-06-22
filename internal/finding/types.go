// Package finding defines the normalized review-finding schema that travels
// from per-scanner output through dedup and arbiter to render time. It is the
// fork-owned counterpart to upstream's model.LlmComment.
//
// Three lifecycle stages are distinguished:
//
//   RawFinding   - one per LlmComment emitted by one scanner; carries source
//   Finding      - post-dedup group of one or more RawFindings
//   FinalFinding - post-arbiter Finding with verdict + confidence
//
// Converters in convert.go bridge to upstream's model.LlmComment.
package finding

// Verdict is the arbiter's classification of a Finding group.
type Verdict string

const (
	VerdictAccepted  Verdict = "accepted_bug"
	VerdictRejected  Verdict = "rejected_fp"
	VerdictUncertain Verdict = "uncertain"
	VerdictStyleOnly Verdict = "style_only"
)

// AllVerdicts is the canonical ordered list of verdict values.
var AllVerdicts = []Verdict{VerdictAccepted, VerdictRejected, VerdictUncertain, VerdictStyleOnly}

// ParseVerdict returns the matching verdict for s, or "" if unknown.
func ParseVerdict(s string) Verdict {
	for _, v := range AllVerdicts {
		if string(v) == s {
			return v
		}
	}
	return ""
}

// Source identifies the scanner that produced a RawFinding.
type Source struct {
	Scanner  string `json:"scanner"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// RawFinding is one finding emitted by one scanner. Confidence is
// scanner-reported in [0,1] or 0 if absent. RawIndex preserves the
// scanner's original output order for trace/debug purposes.
type RawFinding struct {
	Path           string  `json:"path"`
	StartLine      int     `json:"start_line"`
	EndLine        int     `json:"end_line"`
	Title          string  `json:"title"`
	Detail         string  `json:"detail"`
	ExistingCode   string  `json:"existing_code,omitempty"`
	SuggestionCode string  `json:"suggestion_code,omitempty"`
	Source         Source  `json:"source"`
	Severity       string  `json:"severity,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
	Thinking       string  `json:"thinking,omitempty"`
	// ExplicitTitle reports whether Title came from the LLM's title field
	// or was synthesized from the first line of Content. Synthesized titles
	// stay populated so dedup's title-similarity match keeps working, but
	// they must NOT be re-rendered as a bold header in the PR comment —
	// the header would duplicate the first line of Content. ToComment uses
	// this flag to gate LlmComment.Title propagation.
	ExplicitTitle bool `json:"explicit_title,omitempty"`
	RawIndex      int  `json:"-"`
}

// Finding is a post-dedup group. GroupID is unique within a single review run
// and is the stable handle the arbiter references in its verdicts.
type Finding struct {
	GroupID   string       `json:"group_id"`
	Path      string       `json:"path"`
	StartLine int          `json:"start_line"`
	EndLine   int          `json:"end_line"`
	Title     string       `json:"title"`
	Members   []RawFinding `json:"members"`
	Sources   []Source     `json:"sources"`
}

// FinalFinding is a Finding plus an arbiter verdict. Confidence is the
// arbiter-assigned confidence in [0,1], defaulting to 0.5 if missing.
type FinalFinding struct {
	Finding
	Verdict       Verdict `json:"verdict"`
	VerdictReason string  `json:"verdict_reason,omitempty"`
	ArbiterModel  string  `json:"arbiter_model,omitempty"`
	Confidence    float64 `json:"confidence"`
}

// RenderOptions controls how ToComment annotates the flattened content.
type RenderOptions struct {
	ShowProvenance bool
	ShowVerdict    bool
}

// TokenUsage captures the per-stage token spend for the ensemble pipeline.
// Both scanners and the arbiter report into this shape so the final summary
// can aggregate uniformly. Dedup and the verdict filter run no LLM calls and
// therefore never contribute to these counters.
type TokenUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

// Total returns InputTokens + OutputTokens. Cache tokens are excluded because
// for Anthropic they are already subsumed in InputTokens.
func (u TokenUsage) Total() int64 { return u.InputTokens + u.OutputTokens }

// Add returns the element-wise sum of two TokenUsage values.
func (u TokenUsage) Add(o TokenUsage) TokenUsage {
	return TokenUsage{
		InputTokens:      u.InputTokens + o.InputTokens,
		OutputTokens:     u.OutputTokens + o.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + o.CacheWriteTokens,
	}
}
