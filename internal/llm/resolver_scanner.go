package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// envPlaceholderRe matches ${env:NAME} where NAME is [A-Z_][A-Z0-9_]*.
var envPlaceholderRe = regexp.MustCompile(`\$\{env:([A-Z_][A-Z0-9_]*)\}`)

// ExpandEnvPlaceholders rewrites every ${env:NAME} occurrence in s with the
// value of the named environment variable. Unset variables become "" and
// produce an error so callers can fail early rather than silently using empty
// credentials.
func ExpandEnvPlaceholders(s string) (string, error) {
	if !envPlaceholderRe.MatchString(s) {
		return s, nil
	}
	var missing []string
	out := envPlaceholderRe.ReplaceAllStringFunc(s, func(match string) string {
		name := envPlaceholderRe.FindStringSubmatch(match)[1]
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("env-var placeholder(s) unset: %v", missing)
	}
	return out, nil
}

// ResolveProvider resolves a provider+model combination directly, regardless
// of what `cfg.Provider` / `cfg.Model` say in the config file. It is meant for
// ensemble scanners where each scanner references its own provider+model.
//
// providerName must match either a preset (from LookupProvider) or a key under
// `custom_providers`. modelName overrides the entry's model field. API key
// resolution follows the same rules as the existing single-provider path.
func ResolveProvider(configPath, providerName, modelName string) (ResolvedEndpoint, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ResolvedEndpoint{}, fmt.Errorf("config file %s does not exist", configPath)
		}
		return ResolvedEndpoint{}, fmt.Errorf("read config: %w", err)
	}
	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ResolvedEndpoint{}, fmt.Errorf("parse config: %w", err)
	}
	cfg.Provider = providerName
	cfg.Model = ""
	ep, ok, err := tryProviderConfig(cfg, modelName)
	if err != nil {
		return ResolvedEndpoint{}, err
	}
	if !ok {
		return ResolvedEndpoint{}, fmt.Errorf("provider %q did not produce a valid endpoint", providerName)
	}
	ep.Source = "scanner:" + providerName
	// Expand ${env:NAME} placeholders in the resolved endpoint so the config can
	// reference values by environment variable — e.g. a shared
	// "model": "${env:OCR_SPARK_LLM_MODEL}" across scanners and the arbiter, not
	// just the api_key. Done before stripModelSuffix so a suffix carried in the
	// env value is still trimmed.
	for _, dst := range []*string{&ep.Token, &ep.Model, &ep.URL} {
		expanded, err := ExpandEnvPlaceholders(*dst)
		if err != nil {
			return ResolvedEndpoint{}, fmt.Errorf("scanner %q: %w", providerName, err)
		}
		*dst = expanded
	}
	ep.Model = stripModelSuffix(ep.Model)
	return ep, nil
}
