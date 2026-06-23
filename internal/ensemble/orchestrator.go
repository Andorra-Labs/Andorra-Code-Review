// Package ensemble owns the multi-scanner fan-out for the fork's ensemble
// review pipeline. It is intentionally agnostic about how each scanner runs:
// callers inject a RunScanner callback that wraps the upstream Agent so the
// orchestrator can stay decoupled from the upstream agent.Args shape.
//
// In Phase 4 the orchestrator emits raw findings only. Phase 5 wires dedup +
// arbiter on top via the dedup and arbiter packages.
package ensemble

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/open-code-review/open-code-review/internal/agent"
	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/finding"
	"github.com/open-code-review/open-code-review/internal/llm"
	"github.com/open-code-review/open-code-review/internal/model"
)

// ScannerEndpoint pairs a scanner config entry with its resolved LLM endpoint.
type ScannerEndpoint struct {
	Spec     configstore.ScannerSpec
	Endpoint llm.ResolvedEndpoint
}

// ArbiterEndpoint pairs the arbiter config with its resolved endpoint. Phase 5
// consumes it; Phase 4 just carries it through.
type ArbiterEndpoint struct {
	Spec     configstore.ArbiterSpec
	Endpoint llm.ResolvedEndpoint
}

// RunScannerFunc executes one scanner against the shared review target and
// returns its raw comments, token spend, any non-fatal warnings the Agent
// recorded (e.g. per-file subtask failures), and a hard error if the scanner
// as a whole could not produce anything. Errors are scanner-scoped — the
// orchestrator records them but does not abort the whole run on a single
// failure.
type RunScannerFunc func(ctx context.Context, sep ScannerEndpoint) (comments []model.LlmComment, usage finding.TokenUsage, warnings []agent.AgentWarning, err error)

// Orchestrator owns the scanner fan-out. Construct one per review run.
type Orchestrator struct {
	Scanners       []ScannerEndpoint
	Arbiter        *ArbiterEndpoint
	MaxConcurrency int            // 0 = min(len(Scanners), runtime.NumCPU())
	Run            RunScannerFunc // required
}

// ScannerResult captures the outcome of one scanner for diagnostic reporting.
type ScannerResult struct {
	Name     string
	Provider string
	Model    string
	Status   string // "ok" | "error" | "partial"
	Err      string
	Findings int
	Duration time.Duration
	Tokens   finding.TokenUsage
	Warnings []agent.AgentWarning
}

// Result is the orchestrator's full output for a review run.
type Result struct {
	Raw      []finding.RawFinding
	Scanners []ScannerResult
}

// Execute fans out all configured scanners under a bounded semaphore, collects
// their raw comments, tags them with provenance, and returns the aggregate.
// The run fails (returns a non-nil error) only when every scanner errors AND
// produces zero findings. Otherwise per-scanner errors are surfaced via
// ScannerResult and the run is considered degraded but successful.
func (o *Orchestrator) Execute(ctx context.Context) (Result, error) {
	if o.Run == nil {
		return Result{}, errors.New("ensemble.Orchestrator: Run callback is required")
	}
	scanners := o.activeScanners()
	if len(scanners) == 0 {
		return Result{}, errors.New("ensemble.Orchestrator: no enabled scanners")
	}
	limit := o.MaxConcurrency
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	if limit > len(scanners) {
		limit = len(scanners)
	}

	results := make([]ScannerResult, len(scanners))
	rawPerScanner := make([][]finding.RawFinding, len(scanners))

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, sep := range scanners {
		i, sep := i, sep
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now()
			comments, tokens, warnings, err := o.Run(ctx, sep)
			dur := time.Since(start)

			src := finding.Source{
				Scanner:  sep.Spec.Name,
				Provider: sep.Spec.Provider,
				Model:    endpointModel(sep),
			}
			raws := finding.FromComments(comments, src)
			rawPerScanner[i] = raws

			// Tag scanner name onto each warning so the final aggregate makes
			// it clear which scanner skipped which file.
			tagged := make([]agent.AgentWarning, len(warnings))
			for j, w := range warnings {
				if w.Type == "" {
					w.Type = "scanner_warning"
				}
				if !strings.HasPrefix(w.Message, "["+sep.Spec.Name+"] ") {
					w.Message = "[" + sep.Spec.Name + "] " + w.Message
				}
				tagged[j] = w
			}

			res := ScannerResult{
				Name:     sep.Spec.Name,
				Provider: sep.Spec.Provider,
				Model:    src.Model,
				Findings: len(raws),
				Duration: dur,
				Tokens:   tokens,
				Warnings: tagged,
			}
			switch {
			case err == nil:
				res.Status = "ok"
			case len(raws) > 0:
				res.Status = "partial"
				res.Err = err.Error()
				res.Warnings = append(res.Warnings, agent.AgentWarning{
					Type:    "scanner_partial",
					Message: fmt.Sprintf("[%s] scanner returned %d finding(s) but errored: %s", sep.Spec.Name, len(raws), err.Error()),
				})
			default:
				res.Status = "error"
				res.Err = err.Error()
				res.Warnings = append(res.Warnings, agent.AgentWarning{
					Type:    "scanner_failed",
					Message: fmt.Sprintf("[%s] scanner failed with no findings: %s", sep.Spec.Name, err.Error()),
				})
			}
			results[i] = res
		}()
	}
	wg.Wait()

	all := make([]finding.RawFinding, 0)
	successCount := 0
	for i, rs := range results {
		all = append(all, rawPerScanner[i]...)
		if rs.Status == "ok" || rs.Status == "partial" {
			successCount++
		}
	}
	if successCount == 0 {
		// Every scanner failed and emitted nothing — true failure.
		return Result{Scanners: results}, errors.New("ensemble: all scanners failed with no findings")
	}
	return Result{Raw: all, Scanners: results}, nil
}

func (o *Orchestrator) activeScanners() []ScannerEndpoint {
	out := make([]ScannerEndpoint, 0, len(o.Scanners))
	for _, s := range o.Scanners {
		if s.Spec.Enabled != nil && !*s.Spec.Enabled {
			continue
		}
		out = append(out, s)
	}
	return out
}

// endpointModel returns the model to display/report for a scanner. It prefers
// the resolved endpoint model (which has any ${env:...} placeholder expanded and
// is what actually gets sent to the LLM) over the raw spec model from config.
func endpointModel(sep ScannerEndpoint) string {
	if sep.Endpoint.Model != "" {
		return sep.Endpoint.Model
	}
	return sep.Spec.Model
}
