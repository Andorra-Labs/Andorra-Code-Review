package configstore

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SecretMode controls how Export sanitizes secret fields. Use ModePlaceholder
// (default) when the exported file will be committed to a repo and CI will
// inject secrets via env vars. Use ModeStrip when CI sets secrets through
// `ocr config set ...` calls or env-var fallback (today's pattern).
type SecretMode int

const (
	ModePlaceholder SecretMode = iota
	ModeStrip
)

// ExportOptions configures Export.
type ExportOptions struct {
	Mode      SecretMode
	EnvPrefix string // default "OCR_"
}

// Export reads the config file and returns a sanitized JSON ready to commit
// to a repository. Upstream top-level keys (provider, model, providers,
// custom_providers, llm, language, telemetry, ensemble, plus any future
// additions) are preserved; only secret-shaped fields inside providers /
// custom_providers / llm are rewritten.
//
// Placeholder secrets are written as `${env:NAME}` strings. NAME is derived as
// `<EnvPrefix><PROVIDER_NAME_UPPER>_API_KEY` for provider entries and
// `<EnvPrefix>LLM_AUTH_TOKEN` for the legacy llm block.
func Export(srcPath string, opts ExportOptions) ([]byte, error) {
	if opts.EnvPrefix == "" {
		opts.EnvPrefix = "OCR_"
	}
	raw, err := loadRaw(srcPath)
	if err != nil {
		return nil, err
	}
	if msg, ok := raw["providers"]; ok {
		updated, err := sanitizeProviderMap(msg, opts, false)
		if err != nil {
			return nil, fmt.Errorf("sanitize providers: %w", err)
		}
		raw["providers"] = updated
	}
	if msg, ok := raw["custom_providers"]; ok {
		updated, err := sanitizeProviderMap(msg, opts, true)
		if err != nil {
			return nil, fmt.Errorf("sanitize custom_providers: %w", err)
		}
		raw["custom_providers"] = updated
	}
	if msg, ok := raw["llm"]; ok {
		updated, err := sanitizeLegacyLLM(msg, opts)
		if err != nil {
			return nil, fmt.Errorf("sanitize llm: %w", err)
		}
		raw["llm"] = updated
	}
	return json.MarshalIndent(raw, "", "    ")
}

func sanitizeProviderMap(msg json.RawMessage, opts ExportOptions, isCustom bool) (json.RawMessage, error) {
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(msg, &entries); err != nil {
		return msg, err
	}
	for name, entry := range entries {
		envName := envForProviderKey(opts.EnvPrefix, name)
		applySecretMode(entry, "api_key", envName, opts.Mode)
	}
	return json.Marshal(entries)
}

func sanitizeLegacyLLM(msg json.RawMessage, opts ExportOptions) (json.RawMessage, error) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(msg, &entry); err != nil {
		return msg, err
	}
	applySecretMode(entry, "auth_token", opts.EnvPrefix+"LLM_AUTH_TOKEN", opts.Mode)
	return json.Marshal(entry)
}

func applySecretMode(entry map[string]json.RawMessage, key, envName string, mode SecretMode) {
	switch mode {
	case ModeStrip:
		delete(entry, key)
	case ModePlaceholder:
		placeholder := fmt.Sprintf("${env:%s}", envName)
		b, _ := json.Marshal(placeholder)
		entry[key] = b
	}
}

// envForProviderKey produces a valid env-var name for a provider's API key.
// Example: "anthropic" -> "OCR_ANTHROPIC_API_KEY"; "my-gateway" -> "OCR_MY_GATEWAY_API_KEY".
// Characters outside [A-Z0-9_] are normalized to underscores, consecutive
// underscores are collapsed, and a leading digit is escaped with an underscore
// when the caller supplies no prefix, so exported placeholders actually match
// the runtime expander.
func envForProviderKey(prefix, providerName string) string {
	upper := strings.ToUpper(providerName)
	upper = strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, upper)
	// Collapse consecutive underscores and trim leading/trailing ones.
	upper = collapseUnderscores(upper)
	if upper == "" {
		upper = "PROVIDER"
	}
	// The default prefix supplies a leading alpha, but if the caller passes an
	// empty prefix we still need a valid identifier. Prefix an underscore when
	// the normalized provider name would start with a digit.
	if prefix == "" && upper[0] >= '0' && upper[0] <= '9' {
		upper = "_" + upper
	}
	return prefix + upper + "_API_KEY"
}

func collapseUnderscores(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevUnderscore := false
	for _, r := range s {
		if r == '_' {
			if !prevUnderscore {
				b.WriteRune(r)
			}
			prevUnderscore = true
			continue
		}
		prevUnderscore = false
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), "_")
}
