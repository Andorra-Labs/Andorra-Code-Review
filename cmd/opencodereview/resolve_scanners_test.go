package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-code-review/open-code-review/internal/configstore"
)

// TestResolveScanners_PerScannerTimeoutOverride verifies that a scanner's
// explicit timeout overrides the global llm.timeout, while scanners without one
// inherit the global value.
func TestResolveScanners_PerScannerTimeoutOverride(t *testing.T) {
	t.Setenv("OCR_LLM_TIMEOUT", "")

	cfgJSON := `{
      "provider": "Spark",
      "custom_providers": {
        "Spark": {
          "api_key": "test-key",
          "url": "http://spark.local:9090/v1",
          "protocol": "openai",
          "model": "qwen"
        }
      },
      "llm": { "timeout": 600 }
    }`
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	specs := []configstore.ScannerSpec{
		{Name: "fast", Provider: "Spark", Model: "qwen", Timeout: 120},
		{Name: "slow", Provider: "Spark", Model: "qwen"},
	}

	eps, err := resolveScanners(cfgPath, specs, nil)
	if err != nil {
		t.Fatalf("resolveScanners: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(eps))
	}

	got := map[string]time.Duration{}
	for _, e := range eps {
		got[e.Spec.Name] = e.Endpoint.Timeout
	}
	if got["fast"] != 120*time.Second {
		t.Errorf("fast scanner timeout = %v, want %v (per-scanner override)", got["fast"], 120*time.Second)
	}
	if got["slow"] != 600*time.Second {
		t.Errorf("slow scanner timeout = %v, want %v (inherited global)", got["slow"], 600*time.Second)
	}
}
