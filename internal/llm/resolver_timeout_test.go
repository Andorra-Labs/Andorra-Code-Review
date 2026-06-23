package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLLMTimeout_Precedence(t *testing.T) {
	tests := []struct {
		name          string
		configSeconds int
		env           string
		want          time.Duration
	}{
		{name: "neither set -> client default (0)", configSeconds: 0, env: "", want: 0},
		{name: "config only", configSeconds: 600, env: "", want: 600 * time.Second},
		{name: "env only", configSeconds: 0, env: "900", want: 900 * time.Second},
		{name: "env overrides config", configSeconds: 600, env: "900", want: 900 * time.Second},
		{name: "blank env keeps config", configSeconds: 600, env: "  ", want: 600 * time.Second},
		{name: "invalid env keeps config", configSeconds: 600, env: "abc", want: 600 * time.Second},
		{name: "non-positive env keeps config", configSeconds: 600, env: "0", want: 600 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envOCRLLMTimeout, tt.env)
			if got := llmTimeout(tt.configSeconds); got != tt.want {
				t.Errorf("llmTimeout(%d) with env %q = %v, want %v", tt.configSeconds, tt.env, got, tt.want)
			}
		})
	}
}

// writeProviderConfig writes a minimal custom-provider config carrying an
// optional llm.timeout, and returns its path.
func writeProviderConfig(t *testing.T, llmTimeoutSeconds int) string {
	t.Helper()
	cfg := configFile{
		Provider: "Spark",
		CustomProviders: map[string]providerEntryConfig{
			"Spark": {
				APIKey:   "test-key",
				URL:      "http://spark.local:9090/v1",
				Protocol: "openai",
				Model:    "qwen",
			},
		},
		Llm: llmFileConfig{Timeout: llmTimeoutSeconds},
	}
	data, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func TestResolveProvider_TimeoutFromConfig(t *testing.T) {
	t.Setenv(envOCRLLMTimeout, "")
	cfgPath := writeProviderConfig(t, 600)

	ep, err := ResolveProvider(cfgPath, "Spark", "qwen")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if ep.Timeout != 600*time.Second {
		t.Errorf("ep.Timeout = %v, want %v", ep.Timeout, 600*time.Second)
	}
}

func TestResolveProvider_TimeoutEnvOverridesConfig(t *testing.T) {
	t.Setenv(envOCRLLMTimeout, "900")
	cfgPath := writeProviderConfig(t, 600)

	ep, err := ResolveProvider(cfgPath, "Spark", "qwen")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if ep.Timeout != 900*time.Second {
		t.Errorf("ep.Timeout = %v, want %v (env should override config)", ep.Timeout, 900*time.Second)
	}
}

func TestResolveProvider_TimeoutUnsetIsZero(t *testing.T) {
	t.Setenv(envOCRLLMTimeout, "")
	cfgPath := writeProviderConfig(t, 0)

	ep, err := ResolveProvider(cfgPath, "Spark", "qwen")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if ep.Timeout != 0 {
		t.Errorf("ep.Timeout = %v, want 0 (client applies its own default)", ep.Timeout)
	}
}
