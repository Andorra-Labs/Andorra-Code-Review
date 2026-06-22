# Andorra OCR — Implementation Plan

## Context

Andorra Code Review today is a single-model CLI: one LLM reviews one diff. That has two recurring failure modes — models miss bugs that another model would catch, and many flagged "issues" are low-value nits the reviewer has to wade through. The Andorra OCR fork addresses both by running multiple models against the same diff (breadth) and adding an arbiter pass that filters merged findings down to real bugs (precision). A second motivation is operator ergonomics: setting up providers, models, prompts, and the new ensemble config is awkward through env vars and `ocr config set ...`, so we add a local web UI that edits the canonical config through the same backend code the CLI uses.

The shape of this work is **additive**. The default execution path stays single-model unless the user opts into ensemble. Existing CLI surfaces, config keys, JSON output schema, and the GitHub Actions workflow continue to work byte-for-byte.

## Recommended approach

**Wrap, don't extend, `Agent`.** Each scanner runs a complete `Agent.Run` cycle with its own `LLMClient`, `Model`, and `CommentCollector`. A new `internal/ensemble.Orchestrator` fans out to N scanners, joins their findings into a shared `[]finding.RawFinding`, runs `dedup.Group`, then `arbiter.Decide`, and returns `[]finding.FinalFinding`. The orchestrator is the only thing `runReview` needs to call when ensemble mode is active.

This avoids touching `internal/agent/agent.go` (1,488 lines of single-model assumptions) except for one new optional field on `Args` so we can pass precomputed diffs across scanners and avoid N parses of the same diff. All other layers — tool registry, comment collector, plan/main phases, compression, telemetry spans, session recording — stay untouched and each scanner gets its own session JSONL (`internal/viewer/` keeps working unchanged).

The legacy single-model path keeps using `Agent.Run` directly. Ensemble mode is gated by either `cfg.Ensemble.Enabled` or a `--ensemble` CLI flag (flag wins). Disabling at the flag level (`--no-ensemble`) always restores the legacy path.

## Fork isolation (upstream-rebase safety)

This is a fork that must continue to pull upstream Andorra Code Review updates cleanly. Every line of fork code that touches an upstream file is a future rebase conflict. The plan therefore enforces:

1. **No moves or restructures of upstream files.** `cmd/opencodereview/config_cmd.go`, `output.go`, `provider_*.go`, `internal/agent/agent.go`, `internal/llm/resolver.go`, `internal/viewer/*` keep their current shape.
2. **New code goes in new files / new packages.** Any new CLI subcommand becomes a new `cmd/opencodereview/andorra_*.go` file (Go compiles all `.go` in `package main` together, so this costs nothing). Any new behavior becomes a new `internal/<pkg>/` directory entirely owned by the fork.
3. **Unavoidable upstream edits are single-line dispatch hooks.** When the fork must intercept upstream behavior, do it with one line: `if andorraEnsembleEnabled() { return runAndorraReview(args) }` at the top of upstream entry points. Easy to re-apply on rebase even if upstream rewrites the surrounding code.
4. **Wrap, don't move.** The fork's `internal/configstore` does NOT relocate upstream's `Config` struct. It reads the same JSON file via `map[string]json.RawMessage`, owns only the new `ensemble` block, and on save merges back without touching upstream keys.
5. **Duplicate cheap, don't extract.** The fork's `internal/httpguard` is a ~120-line copy of `internal/viewer/hostguard.go` rather than an extraction that touches the viewer. Duplication is preferable to upstream churn.
6. **Naming**: every new fork-owned file outside a fork-owned directory is prefixed `andorra_` (e.g., `cmd/opencodereview/andorra_review.go`). New fork-owned packages live under `internal/` with descriptive names that don't shadow upstream packages.

Concrete upstream-file touchpoints across the entire plan:

| Upstream file | Edit | Conflict risk |
|---|---|---|
| `cmd/opencodereview/main.go` | One line in `dispatch()` switch to delegate `config ui`, `config export` etc. to `andorraDispatch()` | low |
| `cmd/opencodereview/review_cmd.go` | One early-return guard at top of `runReview` for ensemble mode | low |
| `cmd/opencodereview/config_cmd.go` | One case added to `runConfig` switch dispatching to `andorraConfig()` for new subcommands | low |
| `internal/agent/agent.go` | Single optional `PrecomputedDiffs []model.Diff` field on `Args` + 3-line early-return in `loadDiffs` | low |
| `internal/llm/resolver.go` | New exported `ResolveScanner` / `ResolveEnsemble` / `expandEnvPlaceholders` functions appended; existing functions untouched | low |

Every other change lives in fork-owned files or fork-owned packages.

## New packages

| Path | Responsibility |
|---|---|
| `internal/configstore/` | Fork-owned ensemble extension layer over `~/.opencodereview/config.json`. Does NOT move upstream `Config` — reads the file via `map[string]json.RawMessage`, owns only the new `ensemble` block, and on save merges back without touching upstream keys. Both the new web UI and the new CLI subcommands use it. |
| `internal/finding/` | `RawFinding`, `Finding`, `FinalFinding`, `Source`, `Verdict`, `FromComment`, `ToComment`. The normalized schema that travels from scanners through dedup and arbiter to output. |
| `internal/dedup/` | Pure-Go grouping of `[]RawFinding` → `[]Finding` by path + line-range IoU + title similarity (Jaro-Winkler) + identical `existing_code`. No LLM. |
| `internal/arbiter/` | One arbiter LLM call per file (default mode `per_file`) producing verdicts for each group via a `arbiter_verdict` tool call. |
| `internal/ensemble/` | `Orchestrator` that owns scanner concurrency, partial-failure capture, provenance tagging, and the dedup → arbiter pipeline. |
| `internal/httpguard/` | Fork-owned copy of `internal/viewer/hostguard.go` (~120 LoC duplicated, not extracted) for the web UI to use without touching upstream `viewer`. |
| `internal/webui/` | Local HTTP server that mirrors `internal/viewer/server.go` (stdlib `net/http`, `embed.FS`, `html/template`, host-guard). Read+write surface for `configstore` only. |

## Config schema additions

Nested under a top-level `ensemble` block so the legacy schema is untouched when absent. Defined in `internal/configstore`:

```go
type EnsembleConfig struct {
    Enabled  bool             `json:"enabled"`
    Scanners []ScannerSpec    `json:"scanners,omitempty"`
    Arbiter  *ArbiterSpec     `json:"arbiter,omitempty"`
    Dedup    *DedupConfig     `json:"dedup,omitempty"`
    Output   *EnsembleOutput  `json:"output,omitempty"`
}

type ScannerSpec struct {
    Name        string   `json:"name"`
    Provider    string   `json:"provider"`
    Model       string   `json:"model,omitempty"`
    Weight      float64  `json:"weight,omitempty"`
    Temperature *float64 `json:"temperature,omitempty"`
    MaxTokens   int      `json:"max_tokens,omitempty"`
    PromptTag   string   `json:"prompt_tag,omitempty"`
    Enabled     *bool    `json:"enabled,omitempty"`
}

type ArbiterSpec struct {
    Provider    string   `json:"provider"`
    Model       string   `json:"model,omitempty"`
    Temperature *float64 `json:"temperature,omitempty"`
    MaxTokens   int      `json:"max_tokens,omitempty"`
    Mode        string   `json:"mode,omitempty"` // "per_file" (default) | "per_group"
}

type DedupConfig struct {
    LineOverlapMinRatio    float64 `json:"line_overlap_min_ratio,omitempty"`    // default 0.5
    TitleSimilarityMin     float64 `json:"title_similarity_min,omitempty"`      // default 0.7
    RequireSamePath        bool    `json:"require_same_path,omitempty"`         // default true
    ExistingCodeExactBoost bool    `json:"existing_code_exact_boost,omitempty"` // default true
}

type EnsembleOutput struct {
    DefaultVerdicts []string `json:"default_verdicts,omitempty"` // default ["accepted_bug"]
    ShowProvenance  bool     `json:"show_provenance,omitempty"`  // default false
}
```

`Set` learns `ensemble.enabled`, `ensemble.scanners`, `ensemble.arbiter.*`, `ensemble.dedup.*`, `ensemble.output.*`. `Validate` enforces: enabled ⇒ ≥2 scanners, each scanner's provider resolves against `Providers`/`CustomProviders`, no duplicate scanner names, non-empty arbiter, arbiter mode in `{"", "per_file", "per_group"}`.

## Finding schema

Three types in `internal/finding/`:

- `RawFinding` — one per `model.LlmComment` produced by one scanner. Tagged with `Source{Scanner, Provider, Model}`, normalized title.
- `Finding` — post-dedup group. Holds `Members []RawFinding`, deduped `Sources`, group `Title`/`StartLine`/`EndLine` from the highest-confidence member.
- `FinalFinding` — `Finding` + `Verdict` + `VerdictReason` + `ArbiterModel` + `Confidence`.

Converters:

- `FromComment(c model.LlmComment, src Source, idx int) RawFinding`
- `ToComment(f FinalFinding, opts RenderOptions) model.LlmComment` — flattens to legacy shape for existing renderers. Prefixes provenance/verdict only when `opts.ShowProvenance` / `opts.ShowVerdict` true.

This isolates ensemble concerns from `model.LlmComment` (which the GitHub workflow at `.github/workflows/andorra-ocr-review.yml` already parses) and disambiguates "no verdict" between single-model mode and ensemble accepted-by-default.

## Scanner orchestrator

`ensemble.Orchestrator.Run(ctx) ([]finding.RawFinding, []ScannerResult, error)`:

1. Parse diffs once via a shared `diff.Parse` call. Pass the parsed slice via a new optional `Args.PrecomputedDiffs []model.Diff` on `agent.Args`; `Agent.loadDiffs` (in `internal/agent/agent.go`) returns those directly when non-nil.
2. Fan out per scanner under an outer semaphore `MaxScannerConcurrency = min(len(Scanners), runtime.NumCPU())`.
3. Each scanner builds its own `agent.Args` (own `LLMClient` from `llm.NewLLMClient(ep)`, own `Model`, own `tool.NewCommentCollector()`) and calls `ag.Run(ctx)`. The existing per-file concurrency limit is divided across active scanners and floored at 2 so we don't blow rate limits.
4. Collect `(scannerName, []LlmComment, error, tokenStats, duration)` into a `ScannerResult`. A scanner with `Err != nil` but non-empty findings is treated as partial success.
5. Convert all comments to `[]RawFinding` via `finding.FromComment`.
6. Run is fatal only if every scanner returned `Err != nil` AND empty findings. Otherwise each scanner failure becomes an `agent.AgentWarning` (reuse the existing type) with `Type = "scanner_failed"`.

`gitcmd.Runner` is shared across scanners — already concurrency-limited — so we don't multiply git subprocess pressure.

## Dedup (v1)

Pure-Go, no LLM, no embeddings. For each `path`, union-find pairs whose `score` exceeds threshold:

```
overlap   = IoU(line ranges)
title_sim = JaroWinkler(normalize(a.Title), normalize(b.Title))
code_match = (a.ExistingCode == b.ExistingCode && a.ExistingCode != "")
merge if:
    code_match
  OR overlap >= 0.8
  OR (overlap >= LineOverlapMinRatio && title_sim >= TitleSimilarityMin)
```

`internal/dedup/group.go` is ~200 LoC including Jaro-Winkler. Singletons pass through as groups of size 1. Deferred to v2: embedding similarity on `Detail`, cross-file grouping, severity-aware merging.

## Arbiter

Default `per_file` mode: one LLM call per file that has groups. Prompt receives the file diff plus a JSON array of group payloads (group_id, line range, title, deduped detail, existing_code, suggestion_code, member_count, source scanner names — no member confidences, to avoid popularity bias). Arbiter responds via a `arbiter_verdict` tool call with `verdicts: [{group_id, verdict, reason, confidence}]`. Temperature pinned to 0.

Verdicts: `accepted_bug`, `rejected_fp`, `uncertain`, `style_only`.

Failure handling: LLM error, JSON parse failure, or missing group IDs in response → groups default to `uncertain` with reason `"arbiter unavailable"` or `"arbiter omitted verdict"`. An `arbiter_failed` `AgentWarning` is recorded. Default-output of accepted-only suppresses everything in that case, which is the right failure mode (don't surface unvetted findings).

`per_group` mode is available for tougher reviews; documented as the higher-quality but N×-cost option.

Prompt lives in a new template family `internal/config/template/arbiter_template.json` so users can override it without recompiling.

## CLI changes

| Command / flag | Change |
|---|---|
| `ocr review --ensemble` | NEW flag — force ensemble even if config disabled |
| `ocr review --no-ensemble` | NEW flag — force legacy single-model even if config enabled |
| `ocr review --scanners opus,gpt` | NEW — subset override |
| `ocr review --arbiter-model X` | NEW — arbiter override |
| `ocr review --verdict-filter accepted,uncertain` | NEW — default `accepted`; `all` is shorthand |
| `ocr review --show-provenance` | NEW — annotate accepted findings with scanner sources |
| `ocr review --show-rejected` | NEW — equivalent to `--verdict-filter accepted,rejected` |
| `ocr review --debug-trace path.json` | NEW — dump full ensemble trace (per-scanner findings, dedup decisions, arbiter prompts/responses) |
| `ocr config ui` | NEW — launches web UI (default `localhost:5484`) |
| `ocr config export [--out path] [--placeholder-secrets\|--strip-secrets]` | NEW — write a share-safe config (default `./.ocr/config.json`) for committing to a repo |
| `ocr review --config <path>` | NEW — explicit config file override (used in CI) |
| `ocr config scanners` | NEW — interactive Bubble Tea TUI mirroring `provider_tui.go` patterns |
| `ocr config arbiter` | NEW — interactive arbiter model selection |
| `ocr config set ensemble.*` | NEW — handled by `configstore.Set` |
| `ocr llm test --all-scanners` | NEW flag on existing command — test every scanner + arbiter |

Decision point at the start of `runReview` (`cmd/opencodereview/review_cmd.go:23`): `--no-ensemble` → legacy; `--ensemble` OR `cfg.Ensemble.Enabled` → ensemble (require ≥2 scanners + arbiter, error otherwise); else → legacy.

In legacy mode, the new flags emit a single stderr warning and otherwise no-op.

## Web UI

Mirrors `internal/viewer/server.go`: stdlib `net/http`, `embed.FS` for templates and CSS, `html/template` rendering, `httpguard.HostGuard` for DNS-rebinding protection. Server-rendered HTML forms with POSTs — no fetch/JSON dance, no bundler, no JS framework. One small (~50 LoC) script for add/remove rows in the ensemble form.

Routes:

| Method | Path | Purpose |
|---|---|---|
| GET | `/` | Overview: active provider/model, ensemble status, scanner count |
| GET | `/providers` | Provider list with masked keys |
| GET POST | `/providers/{name}` | View/edit one provider |
| POST | `/providers/{name}/delete` | Remove custom provider |
| GET POST | `/ensemble` | Edit ensemble block atomically |
| GET POST | `/arbiter` | Edit arbiter spec |
| GET POST | `/runtime` | Language, telemetry, dedup, output defaults |
| GET POST | `/prompts` `/prompts/override` | Read effective template, set override path |
| POST | `/test/{provider}` | Run no-op LLM call via `internal/config/testconnection/`; returns JSON status |
| GET | `/static/*` | CSS + minimal JS |

Every POST loads via `configstore.Load`, applies via `configstore.Set` per field, runs `configstore.Validate`, then `configstore.Save` (atomic temp-file + `os.Rename`, mode `0o600` like today's `saveConfig`).

Security: localhost-only bind, `httpguard.HostGuard` reused, no auth (config file is already user-readable at mode `0o600`), CSRF via per-process `HttpOnly` + `SameSite=Strict` cookie + hidden form-field token, idle-shutdown after 30 minutes (`--idle-timeout 0` disables). API keys always rendered masked (`sk-ant-***`); empty submission preserves existing value; server never logs request bodies.

Launched via new `cmd/opencodereview/config_ui_cmd.go`, dispatched from `runConfig` in `config_cmd.go`. Default `localhost:5484` (viewer is `5483`). `--no-browser` flag suppresses auto-launch.

## Config portability: local UI → GitHub Actions

The web UI edits `~/.opencodereview/config.json` on the user's laptop. GitHub Actions runs in a fresh container that has no access to that file. The bridge is a **share-safe exported config committed into the same repository where the GitHub Action runs**, plus **env-var placeholder expansion** so secrets never get committed alongside it.

Two supported CI patterns. Both resolve through the same `configstore.Load` code path:

- **Option A — committed config file (recommended for ensemble).** User runs `ocr config export` (or clicks "Download config for CI" in the web UI), receives a `.ocr/config.json` where every secret is replaced by `${env:NAME}` placeholders. User commits the file to the repo (the same repo the Action runs in — same place `.github/workflows/*.yml` and `.eslintrc` live). The workflow sets the referenced env vars from `${{ secrets.* }}` and runs `ocr review`. Adding a scanner later = edit in UI, re-export, commit. PR review surfaces config diffs.

- **Option B — workflow-only config (today's pattern, still supported).** No committed file. The workflow YAML calls `ocr config set ...` for each field before `ocr review`. Tolerable for single-model legacy setups; becomes painful for ensemble's richer schema.

The web UI never pushes to GitHub itself — it's local + no-auth by design. Export-and-commit keeps the user in control of what lands in their repo and avoids the OAuth / hosted-service surface that direct integration would require.

### Pieces

1. **`ocr config export [--out path] [--placeholder-secrets|--strip-secrets]`** — NEW CLI command. Reads `~/.opencodereview/config.json` via `configstore.Load`, writes a sanitized copy.
   - `--placeholder-secrets` (default): each secret field (`api_key`, `llm.auth_token`) is replaced with `${env:OCR_PROVIDER_<NAME>_API_KEY}` (or a user-specified name). The exported file is safe to commit; CI provides the secrets via env vars.
   - `--strip-secrets`: secret fields are removed entirely. CI sets them via the existing env-var resolver fallback or via `ocr config set` in the workflow (today's pattern).
   - Default output path is `./.ocr/config.json` in the current repo.

2. **Repo-level config lookup in `internal/llm/resolver.go`** — when `ResolveEndpoint*` runs, the lookup order becomes: explicit `--config <path>` flag → `$PWD/.ocr/config.json` (repo-local, authoritative in CI) → `~/.opencodereview/config.json` (user-level, used locally) → existing env-var tiers. Repo-local beats user-level so CI deterministically uses the committed file.

3. **`${env:NAME}` expansion** — new `expandEnvPlaceholders(s string) string` in `internal/llm/resolver.go`. Walks loaded config and rewrites any string-valued secret field that matches the pattern. Unset env vars cause a clear validation error before any LLM call.

4. **Web UI download endpoint** — `GET /export?mode=placeholder|strip` returns the exported JSON as a download (`Content-Disposition: attachment; filename=ocr.config.json`). Same logic as the CLI command. A button on `/` reads "Download config for CI" with a brief explainer.

5. **Workflow simplification** — `.github/workflows/andorra-ocr-review.yml` shrinks from many `ocr config set ...` calls to:
   - `checkout` (brings `.ocr/config.json`)
   - export only the secrets the config references (one env-var per scanner)
   - `ocr review --from origin/${{ base }} --to ${{ sha }} --format json`
   - existing posting step unchanged

### Example flow

Local user runs `ocr config ui`, sets up two scanners + arbiter, clicks "Download config for CI", saves `.ocr/config.json` to the repo, commits. The file looks like:

```json
{
  "ensemble": {
    "enabled": true,
    "scanners": [
      {"name":"opus","provider":"anthropic","model":"claude-opus-4-7"},
      {"name":"gpt","provider":"openai","model":"gpt-5.5"}
    ],
    "arbiter": {"provider":"anthropic","model":"claude-opus-4-8"}
  },
  "providers": {
    "anthropic": {"api_key": "${env:OCR_ANTHROPIC_API_KEY}"},
    "openai":    {"api_key": "${env:OCR_OPENAI_API_KEY}"}
  }
}
```

CI sets `OCR_ANTHROPIC_API_KEY` and `OCR_OPENAI_API_KEY` from GitHub Secrets; the resolver expands placeholders at load time; the review runs with the same scanner/arbiter/dedup config the user designed locally.

### Why not push directly to GitHub

The UI is intentionally local + no-auth. Pushing to GitHub would require OAuth tokens, repo-write permissions, and a separate hosted service — all explicit non-goals. The export-then-commit flow keeps the UI local and the user in control of what lands in their repo.

## Output changes

JSON additions are strictly appended to `jsonOutput` in `cmd/opencodereview/output.go`:

```go
type jsonOutput struct {
    Status   string               `json:"status"`
    Message  string               `json:"message,omitempty"`
    Summary  *jsonSummary         `json:"summary,omitempty"`
    Comments []model.LlmComment   `json:"comments"`           // unchanged
    Warnings []agent.AgentWarning `json:"warnings,omitempty"`
    Ensemble *EnsembleReport      `json:"ensemble,omitempty"` // NEW; nil in single-model mode
}
```

`EnsembleReport` carries per-scanner reports, all `FinalFinding`s (regardless of verdict), a dedup summary, and arbiter status. The GitHub workflow keeps consuming `comments[]` unchanged.

Text output: filtered to `VerdictAccepted` by default. Each renders exactly like today via `renderComment` in `output.go`. One extra dim line above the location header when `--show-provenance`: `[ocr] scanners: opus, gpt | verdict: accepted (conf 0.87)`. Rejected/uncertain groups render after accepted in a dimmer style under their verdict header when included by `--verdict-filter`.

Additional summary line printed alongside `telemetry.PrintTraceSummary` (called at `review_cmd.go:178`):

`[ocr] Ensemble: 3 scanners ran (2 ok, 1 error), 12 raw → 8 groups, arbiter: 5 accepted, 2 rejected, 1 uncertain`

## Telemetry

All additions through existing `telemetry.Event` / `RecordX` helpers — no new exporter.

New events: `ensemble.started`, `scanner.started`, `scanner.completed`, `scanner.failed` (via `ErrorEvent`), `dedup.completed`, `arbiter.started`, `arbiter.completed`, `arbiter.failed`, `verdict.assigned`.

New metrics in `internal/telemetry/metrics.go` (followup pattern from `RecordLLMRequest` etc.): scanner runs counter (by status, scanner), scanner duration histogram, dedup group histogram, dedup merge-ratio histogram, arbiter requests counter (by status), arbiter verdicts counter (by verdict).

Spans: new `ensemble.run` child of `review.run`; per-scanner `scanner.run` child (per-file `Agent.Run` work nests under it via context propagation); `dedup.run` and `arbiter.run` siblings; `arbiter.file` grandchildren in per-file mode.

## Reused existing code (read-only — no modifications)

- `cmd/opencodereview/config_cmd.go` — `Config`, `ProviderEntry`, `LlmConfig`, `setConfigValue`, `saveConfig`, `loadOrCreateConfig` are READ by fork code paths but NOT modified or relocated. Where fork code needs identical behavior (e.g., key masking, atomic write), it calls upstream helpers when exported or duplicates them when not.
- `internal/llm/client.go` `LLMClient` interface and `NewLLMClient` are used as-is per scanner — no edits.
- `internal/llm/providers.go` `LookupProvider` / `ListProviders` are called by fork code — no edits.
- `internal/tool/comment_collector.go` `CommentCollector` reused per scanner — no edits.
- `internal/viewer/server.go` `embed.FS` + `parseTemplate` + `renderTemplate` + `hostGuard` patterns are mirrored (copied, not extracted) into `internal/webui/server.go`. Viewer untouched.
- `internal/config/testconnection/` is reused by the web UI `/test/{provider}` handler.

## Upstream files modified (single-line dispatch hooks only)

| File | Edit |
|---|---|
| `cmd/opencodereview/main.go` | One added line in the dispatch switch delegating new subcommands (`config ui`, `config export`) to `andorraDispatch` |
| `cmd/opencodereview/review_cmd.go` | One early-return guard at top of `runReview`: `if ensembleEnabled { return runAndorraReview(args) }` |
| `cmd/opencodereview/config_cmd.go` | One case added to the `runConfig` switch for the new fork subcommands |
| `internal/agent/agent.go` | Single optional `PrecomputedDiffs []model.Diff` field on `Args`, plus a 3-line early-return in `loadDiffs` if non-nil |
| `internal/llm/resolver.go` | New exported `ResolveScanner` / `ResolveEnsemble` / `expandEnvPlaceholders` functions appended; existing functions untouched |
| `internal/telemetry/{events.go,metrics.go}` | Additive only — new event constants and `RecordX` helpers appended; no existing code rewritten |

Every other change lives in fork-owned new files (`cmd/opencodereview/andorra_*.go`) or fork-owned new packages (`internal/configstore`, `internal/finding`, `internal/dedup`, `internal/arbiter`, `internal/ensemble`, `internal/httpguard`, `internal/webui`).

## Phased delivery

Each phase ends at a mergeable, fully-tested commit.

1. **configstore wrapper** (1–2 days). NEW package `internal/configstore/` that reads/writes the ensemble block over `~/.opencodereview/config.json` via `map[string]json.RawMessage` merge. Zero edits to any upstream file. Round-trip tests assert upstream blocks are byte-preserved across `LoadAndorra` → `SaveAndorra`.
2. **ensemble config schema** (1–2 days). Types + `Set` + `Validate` + `ResolveScanner` / `ResolveEnsemble`. CLI can write ensemble config; `review` still ignores it.
3. **finding contract** (1 day). Types + converters + unit tests. Unused by runtime yet.
4. **scanner orchestrator** (3–4 days). `--ensemble` / `--no-ensemble` flags wired. Parallel multi-model scan runs end-to-end with raw findings rendered (no dedup/arbiter — duplicates visible). Scanner telemetry live.
5. **dedup + arbiter** (3–4 days). `dedup.Group` + `arbiter.Decide` + new template + `--verdict-filter` / `--show-*` flags + JSON `EnsembleReport` block + ensemble summary line + dedup/arbiter telemetry. Full ensemble pipeline usable from CLI.
6. **GitHub workflow + output hardening + config export** (2–3 days). Workflow updated to recognize new optional `ensemble` field (backward-compatible). `ocr config export` CLI + `${env:NAME}` placeholder expansion in resolver + `--config <path>` flag + repo-local `.ocr/config.json` lookup. Workflow rewritten to use the exported file. `llm test --all-scanners`. Edge cases (zero accepted, all failed, unset env placeholder).
7. **httpguard copy + web UI skeleton** (2 days). `internal/httpguard/` created as a copy of viewer's hostguard (no upstream edits). Read-only `webui` server with `ocr config ui` launcher.
8. **web UI write surface + export download** (2–3 days). POST handlers for `/providers/{name}`, `/ensemble`, `/arbiter`, `/runtime`, `/prompts/override`. `/test/{provider}` connectivity check. `GET /export` download endpoint. CSRF + idle shutdown.
9. **hardening** (2–3 days). Per-scanner rate-limit retry tuning, `README.md` + `docs/ensemble.md` + `examples/ensemble.config.json`, log-level audit, dogfood on this repo.

## Verification

### Per-package
- `internal/configstore/`: `go test ./internal/configstore/...` — load/save round-trip preserves unknown fields, `Validate` rejects each invariant violation, `Set` accepts/rejects each new key form.
- `internal/finding/`: `go test ./internal/finding/...` — `FromComment` title extraction + source tagging; `ToComment` provenance/verdict prefixing under `RenderOptions`.
- `internal/dedup/`: `go test ./internal/dedup/...` — table-driven fixture suite covering identical/near-miss/false-merge/false-miss/singleton cases plus Jaro-Winkler unit tests.
- `internal/arbiter/`: `go test ./internal/arbiter/...` — mock `LLMClient` returning canned tool calls; verifies success path, missing-group-ID handling, LLM-error fallback to `uncertain`, mode-correct call counts.
- `internal/ensemble/`: `go test ./internal/ensemble/...` — multi-scanner orchestration with mocked clients, partial-failure pass-through, single-diff-parse reuse, sequential ordering under `MaxScannerConcurrency=1`.
- `internal/webui/`: `go test ./internal/webui/...` — `httptest.NewRecorder` + `httptest.NewRequest` for every route; CSRF rejection; host-header tampering rejection; validation-failure renders form without disk write; API key masking in GET responses.
- `cmd/opencodereview/`: extend `review_cmd_test.go` with `--no-ensemble` override, `--verdict-filter` filter behavior, single-model JSON byte-identical regression, ensemble JSON includes `ensemble` block.
- New `cmd/opencodereview/ensemble_smoke_test.go`: full pipeline with `httptest.Server` stubs for Anthropic + OpenAI + arbiter, against a `t.TempDir()` git fixture.

### End-to-end manual
1. Build: `make build` (`dist/opencodereview-linux-amd64`).
2. Configure ensemble: `ocr config set ensemble.enabled true && ocr config set ensemble.scanners '[{"name":"opus","provider":"anthropic","model":"claude-opus-4-7"},{"name":"gpt","provider":"openai","model":"gpt-5.5"}]' && ocr config set ensemble.arbiter.provider anthropic && ocr config set ensemble.arbiter.model claude-opus-4-8`.
3. Run ensemble review on a known-buggy diff and confirm: (a) two scanners ran (visible in summary line); (b) duplicates collapsed; (c) only accepted bugs in default output; (d) `--verdict-filter all` reveals rejected groups; (e) `--debug-trace /tmp/t.json` writes a valid trace file.
4. Disable ensemble: `ocr config set ensemble.enabled false`. Re-run; confirm JSON output is byte-identical to pre-fork single-model output (regression check).
5. Launch web UI: `ocr config ui --addr localhost:5484 --no-browser`. Visit `http://localhost:5484/`. Edit an API key, save, confirm `~/.opencodereview/config.json` updated. Submit a form without CSRF token → 403. Submit with tampered `Host` header → 403. Click "Download config for CI" → received file contains `${env:...}` placeholders, no raw keys.
6. Round-trip CI: `ocr config export --out /tmp/cfg.json --placeholder-secrets`; set the required env vars; `ocr review --config /tmp/cfg.json --from ... --to ... --format json` produces the same ensemble output as the local run.
7. Full test suite: `make test` (race detector + `count=1`).

## Risks

- **Cost & latency** — ensemble is N× LLM spend, capped by the slowest scanner. Mitigated by: default-off opt-in, configurable scanner concurrency, per-scanner `MaxTokens` / `Temperature`, per-scanner cost telemetry. Tiered "cheap-first, expensive-on-hit" deferred to v2.
- **Dedup precision** — false merges hide bugs, false misses defeat the value prop. Mitigated by: conservative default `LineOverlapMinRatio=0.5`, `code_match` only when both `ExistingCode` are non-empty, `--debug-trace` exposing dedup decisions, fully-tunable `DedupConfig`, embedding similarity planned for v2.
- **Arbiter behavior drift across providers** — Mitigated by: tool-call response shape (SDK-enforced schema), `temperature: 0`, swappable prompt template, `arbiter.failure_ratio` warning metric, fixtures covering Anthropic and OpenAI tool-call shapes.
- **Legacy compatibility regression** — moving `Config` into `configstore` is invasive. Mitigated by: phase 1 is pure code move with no behavior change, byte-identical JSON round-trip test over example configs, existing resolver entry point untouched, GitHub workflow's `comments[]` field unchanged.
- **Web UI as a credential write surface** — Mitigated by: masked API key rendering, empty submission preserves existing value, no body logging, host guard + CSRF + `SameSite=Strict`, idle-shutdown, response-body test asserting raw keys never leak in GET handlers.
