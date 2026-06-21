package main

// andorra_review.go is the fork's ensemble-mode review entrypoint.
//
// It is invoked from dispatch() when shouldRunEnsemble returns true; otherwise
// upstream runReview() handles the request unchanged.
//
// Phase 4 scope: parallel scanner fan-out + raw findings. Dedup + arbiter wire
// in via Phase 5 in this same file (Execute then runs through dedup -> arbiter
// -> renderEnsemble).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/open-code-review/open-code-review/internal/agent"
	"github.com/open-code-review/open-code-review/internal/arbiter"
	"github.com/open-code-review/open-code-review/internal/config/rules"
	"github.com/open-code-review/open-code-review/internal/config/template"
	"github.com/open-code-review/open-code-review/internal/config/toolsconfig"
	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/dedup"
	"github.com/open-code-review/open-code-review/internal/diff"
	"github.com/open-code-review/open-code-review/internal/ensemble"
	"github.com/open-code-review/open-code-review/internal/finding"
	"github.com/open-code-review/open-code-review/internal/gitcmd"
	"github.com/open-code-review/open-code-review/internal/llm"
	"github.com/open-code-review/open-code-review/internal/model"
	"github.com/open-code-review/open-code-review/internal/stdout"
	"github.com/open-code-review/open-code-review/internal/telemetry"
	"github.com/open-code-review/open-code-review/internal/tool"
)

// ensembleOptions are the fork-specific CLI flags layered on top of upstream
// reviewOptions. They are stripped from the args slice before being passed to
// upstream's parseReviewFlags so the legacy flag set never has to know about
// them.
type ensembleOptions struct {
	forceEnsemble   bool
	forceLegacy     bool
	scannerSubset   []string // --scanners opus,gpt
	arbiterOverride string   // --arbiter-model X
	verdictFilter  []string // --verdict-filter accepted,uncertain
	showProvenance bool
	showRejected   bool
	debugTrace     string
}

// splitEnsembleArgs separates ensemble-only flags from the rest. Returned
// `rest` is safe to pass to parseReviewFlags. Unknown flags pass through to
// upstream.
func splitEnsembleArgs(args []string) (ensembleOptions, []string) {
	var opts ensembleOptions
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, val, hasVal := parseFlag(arg)
		consume := func() string {
			if hasVal {
				return val
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch key {
		case "--ensemble":
			opts.forceEnsemble = true
		case "--no-ensemble":
			opts.forceLegacy = true
		case "--scanners":
			opts.scannerSubset = parseCSV(consume())
		case "--arbiter-model":
			opts.arbiterOverride = consume()
		case "--verdict-filter":
			opts.verdictFilter = parseCSV(consume())
		case "--show-provenance":
			opts.showProvenance = true
		case "--show-rejected":
			opts.showRejected = true
		case "--debug-trace":
			opts.debugTrace = consume()
		default:
			rest = append(rest, arg)
		}
	}
	return opts, rest
}

func parseFlag(arg string) (key, val string, hasVal bool) {
	if !strings.HasPrefix(arg, "--") {
		return arg, "", false
	}
	if i := strings.IndexByte(arg, '='); i > 0 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// shouldRunEnsemble decides whether the ensemble path applies. Called from
// dispatch() before runReview(). Failure to load configstore is treated as
// "no ensemble configured" so the legacy path still runs cleanly.
func shouldRunEnsemble(args []string) bool {
	eopts, _ := splitEnsembleArgs(args)
	if eopts.forceLegacy {
		return false
	}
	if eopts.forceEnsemble {
		return true
	}
	path, err := configstore.DefaultPath()
	if err != nil {
		return false
	}
	ext, err := configstore.LoadAndorra(path)
	if err != nil || ext == nil || ext.Ensemble == nil {
		return false
	}
	return ext.Ensemble.Enabled && len(ext.Ensemble.Scanners) >= 2 && ext.Ensemble.Arbiter != nil
}

// runAndorraReview executes the ensemble review pipeline. Mirrors the high-level
// shape of upstream's runReview but fans out N scanner Agents under a shared
// orchestrator and (post-Phase-5) routes the merged findings through dedup +
// arbiter before rendering.
func runAndorraReview(args []string) error {
	eopts, rest := splitEnsembleArgs(args)
	opts, err := parseReviewFlags(rest)
	if err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if opts.showHelp {
		printReviewUsage()
		fmt.Println()
		printEnsembleUsage()
		return nil
	}
	if err := requireGitRepo(opts.repoDir); err != nil {
		return err
	}

	tpl, err := template.LoadDefault()
	if err != nil {
		return fmt.Errorf("load default template: %w", err)
	}
	if opts.maxTools > 0 {
		tpl.MaxToolRequestTimes = opts.maxTools
	}
	if err := tpl.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	repoDir, err := resolveRepoDir(opts.repoDir)
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}
	if err := validateReviewRefs(repoDir, opts); err != nil {
		return err
	}
	if opts.commit != "" && opts.background == "" {
		if msg, err := getCommitMessage(repoDir, opts.commit); err == nil && msg != "" {
			opts.background = msg
		}
	}

	resolver, fileFilter, err := rules.NewResolver(repoDir, opts.rulePath)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	if opts.preview {
		return runPreview(repoDir, opts, fileFilter)
	}

	toolEntries, err := toolsconfig.Load(opts.toolConfigPath)
	if err != nil {
		return fmt.Errorf("load tools: %w", err)
	}
	planToolDefs := agent.BuildToolDefs(toolEntries, true)
	mainToolDefs := agent.BuildToolDefs(toolEntries, false)

	cfgPath, err := defaultConfigPath()
	if err != nil {
		return err
	}
	appCfg, err := LoadAppConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load app config: %w", err)
	}
	var lang string
	if appCfg != nil {
		lang = appCfg.Language
	}
	tpl.ApplyLanguage(lang)

	ext, err := configstore.LoadAndorra(cfgPath)
	if err != nil {
		return fmt.Errorf("load ensemble config: %w", err)
	}
	if ext == nil || ext.Ensemble == nil {
		return fmt.Errorf("ensemble mode requested but no ensemble config present in %s", cfgPath)
	}
	if errs := configstore.Validate(ext); len(errs) > 0 {
		return fmt.Errorf("ensemble config invalid: %v", errs)
	}

	scanners, err := resolveScanners(cfgPath, ext.Ensemble.Scanners, eopts.scannerSubset)
	if err != nil {
		return fmt.Errorf("resolve scanners: %w", err)
	}
	if len(scanners) < 2 {
		return fmt.Errorf("ensemble requires at least 2 scanners after filtering, got %d", len(scanners))
	}

	arbiterModel := ext.Ensemble.Arbiter.Model
	if eopts.arbiterOverride != "" {
		arbiterModel = eopts.arbiterOverride
	}
	arbiterEp, err := llm.ResolveProvider(cfgPath, ext.Ensemble.Arbiter.Provider, arbiterModel)
	if err != nil {
		return fmt.Errorf("resolve arbiter: %w", err)
	}
	arbiterEndpoint := &ensemble.ArbiterEndpoint{
		Spec:     *ext.Ensemble.Arbiter,
		Endpoint: arbiterEp,
	}

	gitRunner := gitcmd.New(opts.maxGitProcs)
	mode := tool.ParseReviewMode(opts.from, opts.to, opts.commit)
	ref, _ := mode.RefValue(opts.to, opts.commit)

	// Parse diffs once and share across scanners via PrecomputedDiffs.
	parsedDiffs, err := loadDiffsOnce(context.Background(), repoDir, opts, gitRunner)
	if err != nil {
		return fmt.Errorf("load diffs: %w", err)
	}

	// Inner concurrency floored at 2 so we don't blow rate limits.
	perScannerConcurrency := opts.concurrency / len(scanners)
	if perScannerConcurrency < 2 {
		perScannerConcurrency = 2
	}

	run := func(ctx context.Context, sep ensemble.ScannerEndpoint) ([]model.LlmComment, error) {
		client := llm.NewLLMClient(sep.Endpoint)
		collector := tool.NewCommentCollector()
		fr := &tool.FileReader{
			RepoDir: repoDir,
			Mode:    mode,
			Ref:     ref,
			Runner:  gitRunner,
		}
		tools := buildToolRegistry(collector, fr)

		ag := agent.New(agent.Args{
			RepoDir:               repoDir,
			From:                  opts.from,
			To:                    opts.to,
			Commit:                opts.commit,
			Template:              *tpl,
			SystemRule:            resolver,
			FileFilter:            fileFilter,
			LLMClient:             client,
			Tools:                 tools,
			PlanToolDefs:          planToolDefs,
			MainToolDefs:          mainToolDefs,
			CommentCollector:      collector,
			CommentWorkerPool:     agent.NewCommentWorkerPool(perScannerConcurrency),
			MaxConcurrency:        perScannerConcurrency,
			ConcurrentTaskTimeout: opts.perFileTimeout,
			Model:                 sep.Endpoint.Model,
			Background:            opts.background,
			GitRunner:             gitRunner,
			PrecomputedDiffs:      parsedDiffs,
		})
		comments, err := ag.Run(ctx)
		if err != nil {
			return comments, err
		}
		// Resolve line numbers per-scanner so the LlmComment ranges are valid.
		comments = diff.ResolveLineNumbers(comments, ag.Diffs())
		return comments, nil
	}

	orch := &ensemble.Orchestrator{
		Scanners: scanners,
		Arbiter:  arbiterEndpoint,
		Run:      run,
	}

	// Silence progress output in agent / JSON mode the same way runReview does.
	var unsilence func()
	if opts.outputFormat == "json" || opts.audience == "agent" {
		unsilence = stdout.Quiet()
		defer func() {
			if unsilence != nil {
				unsilence()
			}
		}()
	}

	ctx, span := telemetry.StartSpan(context.Background(), "ensemble.review.run")
	defer span.End()
	telemetry.Event(ctx, "ensemble.started",
		telemetry.AnyToAttr("scanner.count", len(scanners)),
		telemetry.AnyToAttr("arbiter.model", arbiterModel),
	)
	startTime := time.Now()

	result, err := orch.Execute(ctx)
	if err != nil {
		telemetry.SetAttr(span, "error", err.Error())
		return fmt.Errorf("ensemble run failed: %w", err)
	}
	duration := time.Since(startTime)
	for _, sr := range result.Scanners {
		telemetry.Event(ctx, "scanner.completed",
			telemetry.AnyToAttr("scanner.name", sr.Name),
			telemetry.AnyToAttr("scanner.status", sr.Status),
			telemetry.AnyToAttr("scanner.findings", sr.Findings),
			telemetry.AnyToAttr("scanner.duration_ms", sr.Duration.Milliseconds()),
		)
	}

	dedupCfg := dedup.FromConfigStore(ext.Ensemble.Dedup)
	groups := dedup.Group(result.Raw, dedupCfg)
	telemetry.Event(ctx, "dedup.completed",
		telemetry.AnyToAttr("raw_count", len(result.Raw)),
		telemetry.AnyToAttr("group_count", len(groups)),
	)

	diffsByPath := buildDiffMap(parsedDiffs)
	arbiterCfg := arbiter.FromConfigStore(*ext.Ensemble.Arbiter, arbiterModel)
	telemetry.Event(ctx, "arbiter.started",
		telemetry.AnyToAttr("arbiter.mode", arbiterCfg.Mode),
		telemetry.AnyToAttr("group.count", len(groups)),
	)
	arbiterClient := llm.NewLLMClient(arbiterEp)
	finals := arbiter.Decide(ctx, arbiterClient, arbiterCfg, groups, diffsByPath)
	verdictCounts := map[finding.Verdict]int{}
	for _, f := range finals {
		verdictCounts[f.Verdict]++
	}
	telemetry.Event(ctx, "arbiter.completed",
		telemetry.AnyToAttr("accepted", verdictCounts[finding.VerdictAccepted]),
		telemetry.AnyToAttr("rejected", verdictCounts[finding.VerdictRejected]),
		telemetry.AnyToAttr("uncertain", verdictCounts[finding.VerdictUncertain]),
		telemetry.AnyToAttr("style_only", verdictCounts[finding.VerdictStyleOnly]),
	)

	renderOpts := finding.RenderOptions{ShowProvenance: eopts.showProvenance}
	if ext.Ensemble.Output != nil && ext.Ensemble.Output.ShowProvenance {
		renderOpts.ShowProvenance = true
	}
	verdictFilter := pickVerdictFilter(eopts, ext.Ensemble.Output)
	comments := renderFindings(finals, verdictFilter, renderOpts)

	if opts.audience == "agent" && opts.outputFormat != "json" && unsilence != nil {
		unsilence()
		unsilence = nil
	}
	if opts.outputFormat == "json" {
		return outputEnsembleJSON(comments, result, finals, duration)
	}
	if opts.outputFormat != "json" {
		fmt.Fprintln(os.Stderr, ensembleSummary(result, finals))
	}
	outputTextWithWarnings(comments, nil)
	if eopts.debugTrace != "" {
		if err := writeDebugTrace(eopts.debugTrace, result, finals); err != nil {
			fmt.Fprintf(os.Stderr, "[ocr] failed to write debug trace: %v\n", err)
		}
	}
	return nil
}

// loadDiffsOnce computes the same []model.Diff that Agent.loadDiffs would,
// so all scanners can share it via PrecomputedDiffs.
func loadDiffsOnce(ctx context.Context, repoDir string, opts reviewOptions, runner *gitcmd.Runner) ([]model.Diff, error) {
	var provider *diff.Provider
	switch {
	case opts.commit != "":
		provider = diff.NewCommitProvider(repoDir, opts.commit, runner)
	case opts.from != "" && opts.to != "":
		provider = diff.NewProvider(repoDir, opts.from, opts.to, runner)
	default:
		provider = diff.NewWorkspaceProvider(repoDir, runner)
	}
	return provider.GetDiff(ctx)
}

// resolveScanners turns ScannerSpec entries into ScannerEndpoint with resolved
// LLM endpoints, optionally filtered by the --scanners CLI subset.
func resolveScanners(cfgPath string, specs []configstore.ScannerSpec, subset []string) ([]ensemble.ScannerEndpoint, error) {
	subsetSet := map[string]struct{}{}
	for _, n := range subset {
		subsetSet[n] = struct{}{}
	}
	out := make([]ensemble.ScannerEndpoint, 0, len(specs))
	for _, s := range specs {
		if s.Enabled != nil && !*s.Enabled {
			continue
		}
		if len(subsetSet) > 0 {
			if _, ok := subsetSet[s.Name]; !ok {
				continue
			}
		}
		ep, err := llm.ResolveProvider(cfgPath, s.Provider, s.Model)
		if err != nil {
			return nil, fmt.Errorf("scanner %q: %w", s.Name, err)
		}
		out = append(out, ensemble.ScannerEndpoint{Spec: s, Endpoint: ep})
	}
	return out, nil
}

func printEnsembleUsage() {
	fmt.Println(`Ensemble-mode flags (Andorra OCR):
  --ensemble              force ensemble mode even if config has it disabled
  --no-ensemble           force legacy single-model mode (overrides config)
  --scanners <csv>        only run the named scanner subset (e.g. "opus,gpt")
  --arbiter-model <m>     override the configured arbiter model for this run
  --verdict-filter <csv>  filter output by verdict (default "accepted_bug"; "all" = every verdict)
  --show-provenance       annotate each finding with the scanners that produced it
  --show-rejected         shorthand for --verdict-filter accepted_bug,rejected_fp
  --debug-trace <path>    write a JSON trace of scanner/dedup/arbiter decisions`)
}
