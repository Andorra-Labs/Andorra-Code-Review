// Package arbiter runs the post-dedup classification pass that turns Findings
// into FinalFindings (accepted_bug, rejected_fp, uncertain, style_only).
//
// Default mode is per_file: one LLM call per file with all that file's groups
// in a single request. per_group mode emits one LLM call per group. Both modes
// use a tool-call schema so the response is structured and the SDK enforces
// shape.
package arbiter

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/finding"
	"github.com/open-code-review/open-code-review/internal/llm"
)

// Config configures one arbiter run.
type Config struct {
	Model       string
	Mode        string  // "per_file" (default) | "per_group"
	Temperature float64 // default 0
	MaxTokens   int     // default 2048
	SystemPrompt string // optional override; default is built-in
}

// FromConfigStore converts a configstore.ArbiterSpec into Config defaults.
func FromConfigStore(spec configstore.ArbiterSpec, resolvedModel string) Config {
	cfg := Config{
		Model:     resolvedModel,
		Mode:      spec.Mode,
		MaxTokens: spec.MaxTokens,
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 2048
	}
	if spec.Temperature != nil {
		cfg.Temperature = *spec.Temperature
	}
	if cfg.Mode == "" {
		cfg.Mode = "per_file"
	}
	return cfg
}

// Decide runs the arbiter against the given groups, returning one FinalFinding
// per input Finding plus the aggregate token usage across every LLM call this
// arbiter run made. diffsByPath provides the per-file diff context the
// arbiter sees as evidence; missing entries mean the arbiter sees only the
// group payloads for that file.
//
// Per-file mode batches all groups for one file into one LLM call. Per-group
// mode emits one call per group. On LLM failure or malformed response, every
// affected group is marked VerdictUncertain with VerdictReason populated; the
// function still returns the partial findings + accumulated usage so the
// caller can render degraded output.
func Decide(ctx context.Context, client llm.LLMClient, cfg Config, groups []finding.Finding, diffsByPath map[string]string) ([]finding.FinalFinding, finding.TokenUsage) {
	if len(groups) == 0 {
		return nil, finding.TokenUsage{}
	}
	out := make([]finding.FinalFinding, 0, len(groups))
	var totalUsage finding.TokenUsage

	switch cfg.Mode {
	case "per_group":
		for _, g := range groups {
			verdict, reason, conf, usage := callArbiter(ctx, client, cfg, []finding.Finding{g}, diffsByPath[g.Path])
			totalUsage = totalUsage.Add(usage)
			out = append(out, applyVerdicts([]finding.Finding{g}, verdict, reason, conf, cfg.Model)...)
		}
	default: // per_file
		byPath := groupByPath(groups)
		for _, path := range sortedPaths(byPath) {
			pg := byPath[path]
			verdict, reason, conf, usage := callArbiter(ctx, client, cfg, pg, diffsByPath[path])
			totalUsage = totalUsage.Add(usage)
			out = append(out, applyVerdicts(pg, verdict, reason, conf, cfg.Model)...)
		}
	}
	return out, totalUsage
}

func groupByPath(groups []finding.Finding) map[string][]finding.Finding {
	out := map[string][]finding.Finding{}
	for _, g := range groups {
		out[g.Path] = append(out[g.Path], g)
	}
	return out
}

func sortedPaths(m map[string][]finding.Finding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type rawVerdict struct {
	GroupID    string  `json:"group_id"`
	Verdict    string  `json:"verdict"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

// callArbiter returns the per-group verdict/reason/confidence maps plus the
// token usage reported by the LLM for this single call. Empty maps signal
// failure; applyVerdicts then assigns VerdictUncertain.
func callArbiter(ctx context.Context, client llm.LLMClient, cfg Config, groups []finding.Finding, diff string) (map[string]finding.Verdict, map[string]string, map[string]float64, finding.TokenUsage) {
	verdicts := map[string]finding.Verdict{}
	reasons := map[string]string{}
	confs := map[string]float64{}
	var usage finding.TokenUsage

	prompt, err := buildPrompt(groups, diff)
	if err != nil {
		return verdicts, reasons, confs, usage
	}

	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}

	temp := cfg.Temperature
	req := llm.ChatRequest{
		Model: cfg.Model,
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Tools:       []llm.ToolDef{verdictToolDef},
		Temperature: &temp,
		MaxTokens:   cfg.MaxTokens,
	}
	resp, err := client.CompletionsWithCtx(ctx, req)
	if err != nil || resp == nil {
		return verdicts, reasons, confs, usage
	}
	if resp.Usage != nil {
		usage = finding.TokenUsage{
			InputTokens:      resp.Usage.PromptTokens,
			OutputTokens:     resp.Usage.CompletionTokens,
			CacheReadTokens:  resp.Usage.CacheReadTokens,
			CacheWriteTokens: resp.Usage.CacheWriteTokens,
		}
	}
	calls := resp.ToolCalls()
	if len(calls) == 0 {
		return verdicts, reasons, confs, usage
	}
	for _, call := range calls {
		if call.Function.Name != "arbiter_verdict" {
			continue
		}
		var args struct {
			Verdicts []rawVerdict `json:"verdicts"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			continue
		}
		for _, v := range args.Verdicts {
			parsed := finding.ParseVerdict(v.Verdict)
			if parsed == "" {
				continue
			}
			verdicts[v.GroupID] = parsed
			if v.Reason != "" {
				reasons[v.GroupID] = v.Reason
			}
			if v.Confidence > 0 {
				confs[v.GroupID] = clamp(v.Confidence, 0, 1)
			}
		}
	}
	return verdicts, reasons, confs, usage
}

func applyVerdicts(groups []finding.Finding, verdicts map[string]finding.Verdict, reasons map[string]string, confs map[string]float64, model string) []finding.FinalFinding {
	out := make([]finding.FinalFinding, 0, len(groups))
	for _, g := range groups {
		v, ok := verdicts[g.GroupID]
		reason := reasons[g.GroupID]
		conf, hasConf := confs[g.GroupID]
		if !ok {
			v = finding.VerdictUncertain
			if reason == "" {
				if len(verdicts) == 0 {
					reason = "arbiter unavailable"
				} else {
					reason = "arbiter omitted verdict"
				}
			}
		}
		if !hasConf {
			conf = 0.5
		}
		out = append(out, finding.FinalFinding{
			Finding:       g,
			Verdict:       v,
			VerdictReason: reason,
			ArbiterModel:  model,
			Confidence:    conf,
		})
	}
	return out
}

type groupPayload struct {
	GroupID        string   `json:"group_id"`
	StartLine      int      `json:"start_line"`
	EndLine        int      `json:"end_line"`
	Title          string   `json:"title"`
	Detail         string   `json:"detail"`
	ExistingCode   string   `json:"existing_code,omitempty"`
	SuggestionCode string   `json:"suggestion_code,omitempty"`
	MemberCount    int      `json:"member_count"`
	Sources        []string `json:"sources"`
}

func buildPrompt(groups []finding.Finding, diff string) (string, error) {
	payloads := make([]groupPayload, 0, len(groups))
	for _, g := range groups {
		// Concatenate distinct member details (deduped sentences).
		detail := bestDetail(g)
		ec, sc := codeFields(g)
		srcs := make([]string, 0, len(g.Sources))
		for _, s := range g.Sources {
			srcs = append(srcs, s.Scanner)
		}
		payloads = append(payloads, groupPayload{
			GroupID:        g.GroupID,
			StartLine:      g.StartLine,
			EndLine:        g.EndLine,
			Title:          g.Title,
			Detail:         detail,
			ExistingCode:   ec,
			SuggestionCode: sc,
			MemberCount:    len(g.Members),
			Sources:        srcs,
		})
	}
	payloadJSON, err := json.MarshalIndent(payloads, "", "  ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if path := groups[0].Path; path != "" {
		fmt.Fprintf(&b, "File: %s\n\n", path)
	}
	if diff != "" {
		b.WriteString("Diff:\n```\n")
		b.WriteString(diff)
		b.WriteString("\n```\n\n")
	}
	b.WriteString("Candidate findings (JSON):\n```json\n")
	b.Write(payloadJSON)
	b.WriteString("\n```\n\nReturn exactly one tool call with the verdict for each group_id.")
	return b.String(), nil
}

func bestDetail(g finding.Finding) string {
	if len(g.Members) == 0 {
		return g.Title
	}
	best := g.Members[0]
	for _, m := range g.Members[1:] {
		if len(m.Detail) > len(best.Detail) {
			best = m
		}
	}
	return best.Detail
}

func codeFields(g finding.Finding) (existing, suggestion string) {
	for _, m := range g.Members {
		if existing == "" && m.ExistingCode != "" {
			existing = m.ExistingCode
		}
		if suggestion == "" && m.SuggestionCode != "" {
			suggestion = m.SuggestionCode
		}
	}
	return existing, suggestion
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

const defaultSystemPrompt = `You are the arbiter for a multi-model code review. You receive candidate findings produced by independent scanner models and a diff of the file under review. For each candidate, decide whether it is:

- accepted_bug: a real defect a maintainer would want to fix
- rejected_fp: a false positive or noise the scanners got wrong
- uncertain: ambiguous; needs human judgement
- style_only: a low-priority style/nit comment, not a real bug

Be strict. Prefer accepted_bug only when the diff clearly shows the defect. Reject overcautious or speculative findings. Return one tool call to arbiter_verdict containing one entry per group_id.`

var verdictToolDef = llm.ToolDef{
	Type: "function",
	Function: llm.FunctionDef{
		Name:        "arbiter_verdict",
		Description: "Return one verdict per candidate finding group_id.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdicts": map[string]any{
					"type":        "array",
					"description": "One verdict object per candidate group, keyed by group_id.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"group_id":   map[string]any{"type": "string"},
							"verdict":    map[string]any{"type": "string", "enum": []string{"accepted_bug", "rejected_fp", "uncertain", "style_only"}},
							"reason":     map[string]any{"type": "string"},
							"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
						},
						"required": []string{"group_id", "verdict"},
					},
				},
			},
			"required": []string{"verdicts"},
		},
	},
}
