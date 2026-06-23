package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/open-code-review/open-code-review/internal/agent"
	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/ensemble"
	"github.com/open-code-review/open-code-review/internal/finding"
	"github.com/open-code-review/open-code-review/internal/model"
)

// aggregateWarnings flattens per-scanner warnings into one slice so the text
// renderer surfaces partially-failed scanner runs the same way upstream
// runReview surfaces Agent warnings.
func aggregateWarnings(res ensemble.Result) []agent.AgentWarning {
	var out []agent.AgentWarning
	for _, sr := range res.Scanners {
		out = append(out, sr.Warnings...)
	}
	return out
}

// arbiterOutage reports whether the arbiter run effectively failed: every
// group fell back to VerdictUncertain with one of applyVerdicts' fallback
// reasons. Token usage alone isn't a reliable signal — the model may have
// reported usage while emitting no usable verdicts (wrong tool, unparsable
// args, mismatched group_ids) — so we key off the synthetic reasons the
// arbiter assigns when no verdict came back for a group.
func arbiterOutage(_ finding.TokenUsage, finals []finding.FinalFinding) bool {
	if len(finals) == 0 {
		return false
	}
	for _, f := range finals {
		if f.Verdict != finding.VerdictUncertain {
			return false
		}
		if !isArbiterFallbackReason(f.VerdictReason) {
			return false
		}
	}
	return true
}

// isArbiterFallbackReason matches the synthetic reasons applyVerdicts assigns
// when the arbiter call yields no verdict for a group. Kept in sync with the
// strings in internal/arbiter/arbiter.go applyVerdicts.
func isArbiterFallbackReason(reason string) bool {
	switch reason {
	case "arbiter unavailable", "arbiter omitted verdict":
		return true
	}
	return false
}

// injectArbiterOutageWarning appends a synthetic warning to the first
// scanner's Warnings slice so aggregateWarnings surfaces it through both
// stderr and the JSON `warnings` field. The warning is loud enough that a
// user can't mistake an arbiter outage for a clean PR.
func injectArbiterOutageWarning(res ensemble.Result, groupCount int) {
	if len(res.Scanners) == 0 {
		return
	}
	w := agent.AgentWarning{
		Type:    "arbiter_failed",
		Message: fmt.Sprintf("arbiter call failed or returned no verdicts — all %d candidate group(s) marked uncertain; rerun with --verdict-filter all to see the raw groups, or pick a different arbiter model", groupCount),
	}
	res.Scanners[0].Warnings = append(res.Scanners[0].Warnings, w)
}

// arbiterPartialOmission reports whether the arbiter returned a usable verdict
// for some groups but fell back to synthetic uncertain reasons for others.
// Those omitted groups are silently dropped by the default accepted-only
// filter, so we surface a warning.
func arbiterPartialOmission(finals []finding.FinalFinding) (bool, int) {
	if len(finals) == 0 {
		return false, 0
	}
	count := 0
	for _, f := range finals {
		if isArbiterFallbackReason(f.VerdictReason) {
			count++
		}
	}
	return count > 0 && count < len(finals), count
}

// injectArbiterPartialWarning appends a synthetic warning for partial arbiter
// omissions so users notice when some candidate findings were dropped.
func injectArbiterPartialWarning(res ensemble.Result, count int) {
	if len(res.Scanners) == 0 {
		return
	}
	w := agent.AgentWarning{
		Type:    "arbiter_partial",
		Message: fmt.Sprintf("arbiter omitted verdicts for %d candidate group(s); those findings were marked uncertain and may be filtered from default output", count),
	}
	res.Scanners[0].Warnings = append(res.Scanners[0].Warnings, w)
}

// buildDiffMap builds the path->diff lookup the arbiter uses for evidence.
func buildDiffMap(diffs []model.Diff) map[string]string {
	out := make(map[string]string, len(diffs))
	for _, d := range diffs {
		path := d.NewPath
		if path == "" {
			path = d.OldPath
		}
		out[path] = d.Diff
	}
	return out
}

// pickVerdictFilter resolves the active verdict filter from CLI flags +
// EnsembleOutput defaults. Empty/default = ["accepted_bug"].
// "all" expands to every verdict.
func pickVerdictFilter(eopts ensembleOptions, out *configstore.EnsembleOutput) map[finding.Verdict]struct{} {
	var raw []string
	switch {
	case len(eopts.verdictFilter) > 0:
		raw = eopts.verdictFilter
	case eopts.showRejected:
		raw = []string{"accepted_bug", "rejected_fp"}
	case out != nil && len(out.DefaultVerdicts) > 0:
		raw = out.DefaultVerdicts
	default:
		raw = []string{"accepted_bug"}
	}
	set := map[finding.Verdict]struct{}{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "all" {
			for _, v := range finding.AllVerdicts {
				set[v] = struct{}{}
			}
			continue
		}
		if v := finding.ParseVerdict(s); v != "" {
			set[v] = struct{}{}
		}
	}
	if len(set) == 0 {
		set[finding.VerdictAccepted] = struct{}{}
	}
	return set
}

// filterIncludesNonAccepted reports whether the active verdict filter contains
// anything other than accepted_bug. When true, the renderer turns on
// ShowVerdict so users can tell rejected/uncertain/style-only findings apart
// from real bugs in the output.
func filterIncludesNonAccepted(filter map[finding.Verdict]struct{}) bool {
	for v := range filter {
		if v != finding.VerdictAccepted {
			return true
		}
	}
	return false
}

// renderFindings filters by verdict and flattens to upstream LlmComment form.
func renderFindings(finals []finding.FinalFinding, allowed map[finding.Verdict]struct{}, opts finding.RenderOptions) []model.LlmComment {
	out := make([]model.LlmComment, 0, len(finals))
	for _, f := range finals {
		if _, ok := allowed[f.Verdict]; !ok {
			continue
		}
		out = append(out, finding.ToComment(f, opts))
	}
	return out
}

// ensembleSummary formats the final stderr summary line.
func ensembleSummary(res ensemble.Result, finals []finding.FinalFinding) string {
	ok, errd, partial := 0, 0, 0
	for _, sr := range res.Scanners {
		switch sr.Status {
		case "ok":
			ok++
		case "partial":
			partial++
		case "error":
			errd++
		}
	}
	counts := map[finding.Verdict]int{}
	for _, f := range finals {
		counts[f.Verdict]++
	}
	verdictParts := []string{}
	for _, v := range finding.AllVerdicts {
		verdictParts = append(verdictParts, fmt.Sprintf("%d %s", counts[v], v))
	}
	return fmt.Sprintf("[ocr] Ensemble: %d scanners (%d ok, %d partial, %d error), %d raw → %d groups, arbiter: %s",
		len(res.Scanners), ok, partial, errd, len(res.Raw), len(finals), strings.Join(verdictParts, ", "))
}

// ensembleJSON is the JSON envelope appended to upstream output for ensemble runs.
type ensembleJSON struct {
	Status   string                `json:"status"`
	Comments []model.LlmComment    `json:"comments"`
	Warnings []agent.AgentWarning  `json:"warnings,omitempty"`
	Ensemble *ensembleJSONReport   `json:"ensemble,omitempty"`
}

type ensembleJSONReport struct {
	Scanners      []ensemble.ScannerResult `json:"scanners"`
	Groups        []finding.FinalFinding   `json:"groups"`
	DurationMS    int64                    `json:"duration_ms"`
	ArbiterStatus string                   `json:"arbiter_status"`
	ArbiterUsage  finding.TokenUsage       `json:"arbiter_usage"`
	TokenSummary  []tokenRow               `json:"token_summary"`
}

func outputEnsembleJSON(comments []model.LlmComment, res ensemble.Result, finals []finding.FinalFinding, arbiterUsage finding.TokenUsage, rows []tokenRow, dur time.Duration) error {
	env := buildEnsembleJSON(comments, res, finals, arbiterUsage, rows, dur)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// buildEnsembleJSON assembles the JSON envelope for an ensemble run. Slice
// fields are coerced to empty (never nil) so they marshal as [] instead of
// null — consumers like the GitHub Actions summary script read .length on
// scanners/groups/comments directly and a null would throw at runtime.
func buildEnsembleJSON(comments []model.LlmComment, res ensemble.Result, finals []finding.FinalFinding, arbiterUsage finding.TokenUsage, rows []tokenRow, dur time.Duration) ensembleJSON {
	if comments == nil {
		comments = []model.LlmComment{}
	}
	scanners := res.Scanners
	if scanners == nil {
		scanners = []ensemble.ScannerResult{}
	}
	if finals == nil {
		finals = []finding.FinalFinding{}
	}
	if rows == nil {
		rows = []tokenRow{}
	}
	return ensembleJSON{
		Status:   "ok",
		Comments: comments,
		Warnings: aggregateWarnings(res),
		Ensemble: &ensembleJSONReport{
			Scanners:      scanners,
			Groups:        finals,
			DurationMS:    dur.Milliseconds(),
			ArbiterStatus: arbiterStatus(arbiterUsage, len(finals)),
			ArbiterUsage:  arbiterUsage,
			TokenSummary:  rows,
		},
	}
}

func arbiterStatus(usage finding.TokenUsage, finalCount int) string {
	if finalCount == 0 {
		return "skipped"
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return "failed"
	}
	return "ok"
}

// --- token + cost grid ---

// tokenRow is one row in the per-model token/cost summary table.
type tokenRow struct {
	Label    string             `json:"label"`     // "opus (scanner)" or "arbiter"
	Model    string             `json:"model"`     // human-readable model id
	Tokens   finding.TokenUsage `json:"tokens"`
	IsLocal  bool               `json:"is_local"`
	IsBedrock bool              `json:"is_bedrock"`
	CostUSD  float64            `json:"cost_usd"` // 0 when not applicable
	CostKnown bool              `json:"cost_known"`
}

// buildTokenRows compiles per-scanner + arbiter rows from the orchestrator
// result and the arbiter's reported usage. Cost is derived from the spec's
// CostPerM*USD fields; Local spec rows are tagged so the renderer can show
// "(local)" instead of a dollar value.
// arbiterModel is the resolved arbiter endpoint model (placeholders expanded);
// it is preferred over arbiterSpec.Model so the token grid shows the model that
// was actually used rather than a raw "${env:...}" config value.
func buildTokenRows(res ensemble.Result, arbiterSpec *configstore.ArbiterSpec, arbiterModel string, arbiterUsage finding.TokenUsage) []tokenRow {
	rows := make([]tokenRow, 0, len(res.Scanners)+1)

	// Scanner rows. ScannerResult doesn't carry the spec, so we re-attach
	// cost rates via the orchestrator's per-scanner data. The ensemble.Result
	// already includes everything we need (Name, Model, Tokens).
	for _, sr := range res.Scanners {
		rows = append(rows, tokenRow{
			Label:  sr.Name + " (scanner)",
			Model:  sr.Model,
			Tokens: sr.Tokens,
		})
	}

	// Arbiter row (always last, even when usage is zero, so the table is uniform).
	arbiterRow := tokenRow{Label: "arbiter", Tokens: arbiterUsage}
	if arbiterModel == "" && arbiterSpec != nil {
		arbiterModel = arbiterSpec.Model
	}
	arbiterRow.Model = arbiterModel
	if arbiterSpec != nil {
		arbiterRow.IsLocal = arbiterSpec.Local
		arbiterRow.IsBedrock = arbiterSpec.Bedrock
		if arbiterSpec.CostPerMInputUSD > 0 || arbiterSpec.CostPerMOutputUSD > 0 {
			arbiterRow.CostUSD = perMillion(arbiterUsage, arbiterSpec.CostPerMInputUSD, arbiterSpec.CostPerMOutputUSD)
			arbiterRow.CostKnown = true
		}
	}
	rows = append(rows, arbiterRow)
	return rows
}

// EnrichTokenRowsFromSpecs walks the ScannerSpec list and pastes cost rates,
// Bedrock/Local flags onto the matching rows (by scanner name). Decoupled
// from buildTokenRows because ensemble.ScannerResult doesn't carry the spec.
func EnrichTokenRowsFromSpecs(rows []tokenRow, specs []configstore.ScannerSpec) {
	specByName := map[string]configstore.ScannerSpec{}
	for _, s := range specs {
		specByName[s.Name] = s
	}
	for i := range rows {
		// The label is "<name> (scanner)" — strip the suffix.
		name := strings.TrimSuffix(rows[i].Label, " (scanner)")
		if name == rows[i].Label {
			continue // arbiter row, already handled
		}
		s, ok := specByName[name]
		if !ok {
			continue
		}
		rows[i].IsLocal = s.Local
		rows[i].IsBedrock = s.Bedrock
		if s.CostPerMInputUSD > 0 || s.CostPerMOutputUSD > 0 {
			rows[i].CostUSD = perMillion(rows[i].Tokens, s.CostPerMInputUSD, s.CostPerMOutputUSD)
			rows[i].CostKnown = true
		}
	}
}

func perMillion(u finding.TokenUsage, inputRate, outputRate float64) float64 {
	return (float64(u.InputTokens)*inputRate + float64(u.OutputTokens)*outputRate) / 1_000_000
}

// renderTokenGrid formats the per-model token + cost table for stderr.
// Cost column shows "(local)" for local models, "$X.XXXX" when rates set,
// and "—" otherwise. A totals row at the bottom sums tokens and known cost.
func renderTokenGrid(rows []tokenRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n[ocr] Token usage:\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  Model\tInput\tOutput\tTotal\tCost (USD)")
	var totals finding.TokenUsage
	var totalCost float64
	anyCostKnown := false
	for _, r := range rows {
		totals = totals.Add(r.Tokens)
		if r.CostKnown {
			totalCost += r.CostUSD
			anyCostKnown = true
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			labelWithModel(r),
			fmtInt(r.Tokens.InputTokens),
			fmtInt(r.Tokens.OutputTokens),
			fmtInt(r.Tokens.Total()),
			costCell(r),
		)
	}
	fmt.Fprintln(tw, "  ---\t---\t---\t---\t---")
	totalCostCell := "—"
	if anyCostKnown {
		totalCostCell = fmt.Sprintf("$%.4f", totalCost)
	}
	fmt.Fprintf(tw, "  Total\t%s\t%s\t%s\t%s\n",
		fmtInt(totals.InputTokens),
		fmtInt(totals.OutputTokens),
		fmtInt(totals.Total()),
		totalCostCell,
	)
	tw.Flush()
	return b.String()
}

func labelWithModel(r tokenRow) string {
	if r.Model == "" {
		return r.Label
	}
	return fmt.Sprintf("%s [%s]", r.Label, r.Model)
}

func costCell(r tokenRow) string {
	if r.IsLocal {
		return "(local)"
	}
	if !r.CostKnown {
		return "—"
	}
	return fmt.Sprintf("$%.4f", r.CostUSD)
}

// fmtInt thousand-separates an int64 for the token columns.
func fmtInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas from the right.
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

// writeDebugTrace dumps the per-scanner results + grouped findings to disk.
func writeDebugTrace(path string, res ensemble.Result, finals []finding.FinalFinding) error {
	payload := map[string]any{
		"scanners":     res.Scanners,
		"raw_findings": res.Raw,
		"groups":       finals,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
