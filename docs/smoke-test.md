# Smoke Test

This PR exercises the **Andorra Review** workflow end to end after the dual
Tailscale OAuth change:

1. `TAILSCALE_ID` selects the OAuth client (`TS` default, `TS2` opt-in).
2. The validate step confirms the selected client's credentials are present.
3. The runner connects to Tailscale and reaches the LLM endpoint.
4. The ensemble review runs and posts its summary comment.

## Why this needs a code file, not just docs

The reviewer only scans files whose extension is in its allowlist
(`internal/config/allowlist/supported_file_types.json`). Markdown is **not**
allowlisted, so a docs-only diff is filtered out before any LLM call — the
binary exits `0` in ~0s with 0 tokens and a misleading "0 findings ✅". That
is a valid no-op for normal PRs but proves nothing about LLM reachability.

To actually drive an LLM call over the tailnet, this PR also changes a
reviewable Go file (`examples/smoketest/main.go`). If the endpoint is
unreachable or frozen, the scanner errors and the job fails — so a green run
genuinely confirms the model was reached.

Both files are throwaway and safe to delete once the smoke test passes.
