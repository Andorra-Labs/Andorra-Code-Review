# Smoke Test

This file exists only to trigger the **Andorra Review** workflow on a pull
request so we can confirm the end-to-end path is healthy after the dual
Tailscale OAuth change:

1. `TAILSCALE_ID` selects the OAuth client (`TS` default, `TS2` opt-in).
2. The validate step confirms the selected client's credentials are present.
3. The runner connects to Tailscale and reaches the LLM endpoint.
4. The ensemble review runs and posts its summary comment.

It carries no functional code and is safe to delete once the smoke test passes.
