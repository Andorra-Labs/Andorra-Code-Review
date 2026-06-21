package llm

import (
	"encoding/json"
	"fmt"
	"os"
)

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
	ep.Model = stripModelSuffix(ep.Model)
	return ep, nil
}
