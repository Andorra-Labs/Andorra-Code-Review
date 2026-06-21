package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/ensemble"
	"github.com/open-code-review/open-code-review/internal/finding"
	"github.com/open-code-review/open-code-review/internal/model"
)

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
	Status   string                  `json:"status"`
	Comments []model.LlmComment      `json:"comments"`
	Ensemble *ensembleJSONReport     `json:"ensemble,omitempty"`
}

type ensembleJSONReport struct {
	Scanners      []ensemble.ScannerResult `json:"scanners"`
	Groups        []finding.FinalFinding   `json:"groups"`
	DurationMS    int64                    `json:"duration_ms"`
	ArbiterStatus string                   `json:"arbiter_status"`
}

func outputEnsembleJSON(comments []model.LlmComment, res ensemble.Result, finals []finding.FinalFinding, dur time.Duration) error {
	if comments == nil {
		comments = []model.LlmComment{}
	}
	env := ensembleJSON{
		Status:   "ok",
		Comments: comments,
		Ensemble: &ensembleJSONReport{
			Scanners:      res.Scanners,
			Groups:        finals,
			DurationMS:    dur.Milliseconds(),
			ArbiterStatus: "skipped", // Phase 4: not yet wired; Phase 5 sets ok/failed
		},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
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
