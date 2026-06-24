// Package arbiter runs the post-dedup classification pass that turns Findings
// into FinalFindings (accepted_bug, rejected_fp, uncertain, style_only).
//
// Default mode is per_file: one LLM call per file with all that file's groups
// in a single request. per_group mode emits one LLM call per group. The arbiter
// prefers a tool-call schema so the response is structured and the SDK enforces
// shape, but falls back to parsing a JSON object from the message content when
// the endpoint does not support tool/function calling.
package arbiter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/finding"
	"github.com/open-code-review/open-code-review/internal/llm"
)

// Config configures one arbiter run.
type Config struct {
	Model        string
	Mode         string  // "per_file" (default) | "per_group"
	Temperature  float64 // default 0
	MaxTokens    int     // default 2048
	SystemPrompt string  // optional override; default is built-in
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
//
// The returned error is non-nil when one or more arbiter calls failed (LLM
// error, no tool call, unparseable verdicts). It is diagnostic only — the
// findings/usage are always returned regardless — so callers can surface *why*
// the arbiter produced no verdicts instead of reporting a silent outage. Each
// failure is labelled by file path (per_file) or group id (per_group).
func Decide(ctx context.Context, client llm.LLMClient, cfg Config, groups []finding.Finding, diffsByPath map[string]string) ([]finding.FinalFinding, finding.TokenUsage, error) {
	if len(groups) == 0 {
		return nil, finding.TokenUsage{}, nil
	}
	out := make([]finding.FinalFinding, 0, len(groups))
	var totalUsage finding.TokenUsage
	var errs []error

	switch cfg.Mode {
	case "per_group":
		for _, g := range groups {
			verdict, reason, conf, usage, err := callArbiter(ctx, client, cfg, []finding.Finding{g}, diffsByPath[g.Path])
			totalUsage = totalUsage.Add(usage)
			if err != nil {
				errs = append(errs, fmt.Errorf("group %s: %w", g.GroupID, err))
			}
			out = append(out, applyVerdicts([]finding.Finding{g}, verdict, reason, conf, cfg.Model)...)
		}
	default: // per_file
		byPath := groupByPath(groups)
		for _, path := range sortedPaths(byPath) {
			pg := byPath[path]
			verdict, reason, conf, usage, err := callArbiter(ctx, client, cfg, pg, diffsByPath[path])
			totalUsage = totalUsage.Add(usage)
			if err != nil {
				label := path
				if label == "" {
					label = "(no path)"
				}
				errs = append(errs, fmt.Errorf("%s: %w", label, err))
			}
			out = append(out, applyVerdicts(pg, verdict, reason, conf, cfg.Model)...)
		}
	}
	return out, totalUsage, errors.Join(errs...)
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

// callArbiter classifies one batch of groups. It first tries tool/function
// calling (schema-enforced, preferred). If that path fails, it retries once
// without tools, asking for the verdict as a plain JSON object parsed from the
// message content. The fallback exists because many self-hosted or
// openai-compatible endpoints (e.g. vLLM served without a tool-call parser)
// either 500 on a `tools` request or ignore it and answer in prose; the scanner
// pass works on those endpoints because it never uses tools, so the arbiter
// should not be the lone component that hard-requires them.
//
// Token usage from both attempts is accumulated. The returned error, when
// non-nil, explains why no verdicts came back — naming both the tool-call and
// JSON attempts — so the caller can surface it instead of treating every
// failure as an opaque outage.
func callArbiter(ctx context.Context, client llm.LLMClient, cfg Config, groups []finding.Finding, diff string) (map[string]finding.Verdict, map[string]string, map[string]float64, finding.TokenUsage, error) {
	verdicts, reasons, confs, usage, err := callArbiterOnce(ctx, client, cfg, groups, diff, true)
	if err == nil {
		return verdicts, reasons, confs, usage, nil
	}
	// Fallback: retry without tools and parse JSON from the content. Carry the
	// first attempt's usage forward so token accounting reflects both calls.
	v2, r2, c2, u2, err2 := callArbiterOnce(ctx, client, cfg, groups, diff, false)
	u2 = usage.Add(u2)
	if err2 == nil {
		return v2, r2, c2, u2, nil
	}
	return v2, r2, c2, u2, fmt.Errorf("tool-call path: %v; json fallback: %w", err, err2)
}

// callArbiterOnce performs a single arbiter request. When useTools is true the
// request carries the arbiter_verdict tool and verdicts are read from the tool
// call; otherwise the request asks for a JSON object and verdicts are parsed
// from the message content. Empty result maps with a non-nil error signal
// failure; applyVerdicts then assigns VerdictUncertain.
func callArbiterOnce(ctx context.Context, client llm.LLMClient, cfg Config, groups []finding.Finding, diff string, useTools bool) (map[string]finding.Verdict, map[string]string, map[string]float64, finding.TokenUsage, error) {
	verdicts := map[string]finding.Verdict{}
	reasons := map[string]string{}
	confs := map[string]float64{}
	var usage finding.TokenUsage

	prompt, err := buildPrompt(groups, diff, useTools)
	if err != nil {
		return verdicts, reasons, confs, usage, fmt.Errorf("build prompt: %w", err)
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
		Temperature: &temp,
		MaxTokens:   cfg.MaxTokens,
	}
	if useTools {
		req.Tools = []llm.ToolDef{verdictToolDef}
	}

	resp, err := client.CompletionsWithCtx(ctx, req)
	if err != nil {
		return verdicts, reasons, confs, usage, fmt.Errorf("LLM call failed: %w", err)
	}
	if resp == nil {
		return verdicts, reasons, confs, usage, errors.New("LLM call returned no response")
	}
	if resp.Usage != nil {
		usage = finding.TokenUsage{
			InputTokens:      resp.Usage.PromptTokens,
			OutputTokens:     resp.Usage.CompletionTokens,
			CacheReadTokens:  resp.Usage.CacheReadTokens,
			CacheWriteTokens: resp.Usage.CacheWriteTokens,
		}
	}

	var raws []rawVerdict
	if useTools {
		raws, err = extractToolVerdicts(resp)
	} else {
		raws, err = extractJSONVerdicts(resp)
	}
	if err != nil {
		return verdicts, reasons, confs, usage, err
	}

	for _, v := range raws {
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
	if len(verdicts) == 0 {
		return verdicts, reasons, confs, usage, errors.New("response contained no usable verdicts")
	}
	return verdicts, reasons, confs, usage, nil
}

// extractToolVerdicts reads rawVerdicts from the arbiter_verdict tool call.
func extractToolVerdicts(resp *llm.ChatResponse) ([]rawVerdict, error) {
	calls := resp.ToolCalls()
	if len(calls) == 0 {
		return nil, errors.New("response contained no tool call (model may have replied in prose)")
	}
	var out []rawVerdict
	var parseErr error
	sawVerdictTool := false
	for _, call := range calls {
		if call.Function.Name != "arbiter_verdict" {
			continue
		}
		sawVerdictTool = true
		var args struct {
			Verdicts []rawVerdict `json:"verdicts"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			parseErr = fmt.Errorf("could not parse arbiter_verdict arguments: %w", err)
			continue
		}
		out = append(out, args.Verdicts...)
	}
	if len(out) > 0 {
		return out, nil
	}
	switch {
	case parseErr != nil:
		return nil, parseErr
	case !sawVerdictTool:
		return nil, errors.New("response made tool call(s) but none named arbiter_verdict")
	default:
		return nil, errors.New("arbiter_verdict call contained no verdicts")
	}
}

// extractJSONVerdicts reads rawVerdicts from a JSON object embedded in the
// message content, tolerating markdown fences and surrounding prose.
func extractJSONVerdicts(resp *llm.ChatResponse) ([]rawVerdict, error) {
	obj := extractJSONObject(resp.Content())
	if obj == "" {
		return nil, errors.New("response content had no JSON object (model may have replied in prose)")
	}
	var args struct {
		Verdicts []rawVerdict `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(obj), &args); err != nil {
		return nil, fmt.Errorf("could not parse JSON verdicts: %w", err)
	}
	if len(args.Verdicts) == 0 {
		return nil, errors.New("JSON response contained no verdicts")
	}
	return args.Verdicts, nil
}

// extractJSONObject returns the substring from the first '{' to the last '}'.
// This strips ```json fences, language tags, and any surrounding prose the
// model added despite instructions. Returns "" when no object is present.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
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

func buildPrompt(groups []finding.Finding, diff string, useTools bool) (string, error) {
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
	b.WriteString("\n```\n\n")
	if useTools {
		b.WriteString("Return exactly one tool call with the verdict for each group_id.")
	} else {
		b.WriteString("Respond with ONLY a JSON object of this exact shape and nothing else:\n")
		b.WriteString(`{"verdicts":[{"group_id":"<id>","verdict":"accepted_bug|rejected_fp|uncertain|style_only","reason":"<short>","confidence":0.0}]}` + "\n")
		b.WriteString("Include one entry per group_id. Do not wrap it in markdown fences or add any commentary.")
	}
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

Be strict. Prefer accepted_bug only when the diff clearly shows the defect. Reject overcautious or speculative findings. Return one verdict entry per group_id.`

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
