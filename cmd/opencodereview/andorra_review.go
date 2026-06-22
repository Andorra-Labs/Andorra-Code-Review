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
	"os/exec"
	"path/filepath"
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
	configPath     string // --config <path>
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
		case "--config":
			opts.configPath = consume()
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

// buildClient constructs the right LLMClient for a resolved endpoint. Bedrock
// scanners and the Bedrock arbiter use the dedicated BedrockClient (which
// speaks Bedrock's Anthropic InvokeModel envelope); everything else goes
// through the standard Anthropic / OpenAI factory.
func buildClient(ep llm.ResolvedEndpoint, bedrock bool) llm.LLMClient {
	if bedrock {
		return llm.NewBedrockClient(llm.ClientConfig{
			URL:     ep.URL,
			APIKey:  ep.Token,
			Model:   ep.Model,
			Timeout: 0, // BedrockClient applies its own default
		})
	}
	return llm.NewLLMClient(ep)
}

// extractRepoFlag scans args for --repo / --repo=value so callers running
// before parseReviewFlags can still locate the review target's .ocr/config.json.
// Returns "" when the flag isn't present (caller falls back to cwd).
func extractRepoFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--repo" && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(args[i], "--repo="):
			return strings.TrimPrefix(args[i], "--repo=")
		}
	}
	return ""
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
// dispatch() before runReview(). Any config with `ensemble.enabled=true`
// routes to the ensemble path — even if scanners or arbiter are missing —
// so that runAndorraReview surfaces a clear validation error rather than
// silently falling back to single-model review.
func shouldRunEnsemble(args []string) bool {
	eopts, _ := splitEnsembleArgs(args)
	if eopts.forceLegacy {
		return false
	}
	if eopts.forceEnsemble {
		return true
	}
	// shouldRunEnsemble runs before flag parsing, so we don't yet know
	// --repo. Best-effort: also probe the --repo target if explicitly given.
	repoDir := extractRepoFlag(args)
	path, err := resolveAndorraConfigPath(eopts.configPath, repoDir)
	if err != nil {
		return false
	}
	ext, err := configstore.LoadAndorra(path)
	if err != nil || ext == nil || ext.Ensemble == nil {
		return false
	}
	return ext.Ensemble.Enabled
}

// resolveAndorraConfigPath implements the fork's config lookup order:
//   explicit --config flag → <gitRoot(repoDir)>/.ocr/config.json
//   → <cwd>/.ocr/config.json → ~/.opencodereview/config.json
// Repo-local beats user-level so CI deterministically uses the committed file.
// The gitRoot lookup means `ocr review --repo path/to/subdir` still finds
// the config committed at the repository root.
func resolveAndorraConfigPath(explicit, repoDir string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	// Walk up to the git toplevel from repoDir (or cwd) so a subdirectory
	// invocation still locates the committed .ocr/config.json at the root.
	root := gitTopLevel(repoDir)
	if root != "" {
		repoLocal := filepath.Join(root, ".ocr", "config.json")
		if _, err := os.Stat(repoLocal); err == nil {
			return repoLocal, nil
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		repoLocal := filepath.Join(cwd, ".ocr", "config.json")
		if _, err := os.Stat(repoLocal); err == nil {
			return repoLocal, nil
		}
	}
	return configstore.DefaultPath()
}

// gitTopLevel runs `git rev-parse --show-toplevel` against dir (or cwd when
// dir is empty) and returns the absolute path of the enclosing repository
// root. Returns "" when the directory is not inside a git repo, in which
// case the caller falls back to cwd-relative probing.
func gitTopLevel(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

	cfgPath, err := resolveAndorraConfigPath(eopts.configPath, repoDir)
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
	if ext.Ensemble.Arbiter == nil {
		return fmt.Errorf("ensemble mode requested but ensemble.arbiter is not configured in %s", cfgPath)
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
	var arbiterEp llm.ResolvedEndpoint
	if ext.Ensemble.Arbiter.Bedrock {
		arbiterEp, err = llm.ResolveBedrock("arbiter", arbiterModel)
	} else {
		arbiterEp, err = llm.ResolveProvider(cfgPath, ext.Ensemble.Arbiter.Provider, arbiterModel)
	}
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

	// Honor the requested total concurrency as a budget shared across the
	// scanner fan-out. With many scanners and a small --concurrency, each
	// scanner runs files sequentially rather than multiplying total
	// in-flight LLM requests beyond what the user asked for.
	perScannerConcurrency := opts.concurrency / len(scanners)
	if perScannerConcurrency < 1 {
		perScannerConcurrency = 1
	}
	// Cap how many scanner goroutines run in parallel so that
	// scannerFanOut × perScannerConcurrency never exceeds opts.concurrency.
	// Without this cap, the orchestrator defaults to runtime.NumCPU() and
	// blows past the requested budget even when per-scanner concurrency is 1.
	scannerFanOut := opts.concurrency / perScannerConcurrency
	if scannerFanOut < 1 {
		scannerFanOut = 1
	}
	if scannerFanOut > len(scanners) {
		scannerFanOut = len(scanners)
	}

	run := func(ctx context.Context, sep ensemble.ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, []agent.AgentWarning, error) {
		client := buildClient(sep.Endpoint, sep.Spec.Bedrock)
		collector := tool.NewCommentCollector()
		fr := &tool.FileReader{
			RepoDir: repoDir,
			Mode:    mode,
			Ref:     ref,
			Runner:  gitRunner,
		}
		tools := buildToolRegistry(collector, fr)

		// Per-scanner template so per-scanner MaxTokens override actually
		// caps the prompt budget for this Agent without leaking to siblings.
		scannerTpl := *tpl
		if sep.Spec.MaxTokens > 0 {
			scannerTpl.MaxTokens = sep.Spec.MaxTokens
		}
		ag := agent.New(agent.Args{
			RepoDir:               repoDir,
			From:                  opts.from,
			To:                    opts.to,
			Commit:                opts.commit,
			Template:              scannerTpl,
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
			Temperature:           sep.Spec.Temperature,
			Background:            opts.background,
			GitRunner:             gitRunner,
			PrecomputedDiffs:      parsedDiffs,
		})
		comments, err := ag.Run(ctx)
		usage := finding.TokenUsage{
			InputTokens:      ag.TotalInputTokens(),
			OutputTokens:     ag.TotalOutputTokens(),
			CacheReadTokens:  ag.TotalCacheReadTokens(),
			CacheWriteTokens: ag.TotalCacheWriteTokens(),
		}
		warnings := ag.Warnings()
		if err != nil {
			return comments, usage, warnings, err
		}
		// Resolve line numbers per-scanner so the LlmComment ranges are valid.
		comments = diff.ResolveLineNumbers(comments, ag.Diffs())
		return comments, usage, warnings, nil
	}

	orch := &ensemble.Orchestrator{
		Scanners:       scanners,
		Arbiter:        arbiterEndpoint,
		Run:            run,
		MaxConcurrency: scannerFanOut,
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
	arbiterClient := buildClient(arbiterEp, ext.Ensemble.Arbiter.Bedrock)
	finals, arbiterUsage := arbiter.Decide(ctx, arbiterClient, arbiterCfg, groups, diffsByPath)
	// An arbiter outage produces all-uncertain verdicts AND zero usage.
	// Without a loud warning, the default accepted-only filter drops every
	// finding and the PR looks clean. Surface a synthetic AgentWarning so
	// stderr / JSON warnings make the outage unmistakable.
	if arbiterOutage(arbiterUsage, finals) {
		injectArbiterOutageWarning(result, len(groups))
	}
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
	// Label verdicts on every rendered finding when the filter includes any
	// non-accepted verdict, otherwise rejected/uncertain/style-only findings
	// look indistinguishable from real bugs in the output.
	if filterIncludesNonAccepted(verdictFilter) {
		renderOpts.ShowVerdict = true
	}
	comments := renderFindings(finals, verdictFilter, renderOpts)

	if opts.audience == "agent" && opts.outputFormat != "json" && unsilence != nil {
		unsilence()
		unsilence = nil
	}
	tokenRows := buildTokenRows(result, ext.Ensemble.Arbiter, arbiterUsage)
	EnrichTokenRowsFromSpecs(tokenRows, ext.Ensemble.Scanners)
	if opts.outputFormat == "json" {
		return outputEnsembleJSON(comments, result, finals, arbiterUsage, tokenRows, duration)
	}
	fmt.Fprintln(os.Stderr, ensembleSummary(result, finals))
	outputTextWithWarnings(comments, aggregateWarnings(result))
	fmt.Fprintln(os.Stderr, renderTokenGrid(tokenRows))
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
// LLM endpoints, optionally filtered by the --scanners CLI subset. Bedrock
// scanners route through llm.ResolveBedrock instead of the per-provider path.
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
		var ep llm.ResolvedEndpoint
		var err error
		if s.Bedrock {
			ep, err = llm.ResolveBedrock(s.Name, s.Model)
		} else {
			ep, err = llm.ResolveProvider(cfgPath, s.Provider, s.Model)
		}
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
