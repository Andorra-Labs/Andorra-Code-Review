package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldRunEnsembleFallsBackWhenAllScannersDisabled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	// Two scanners, both disabled, plus a valid arbiter. The user wants to
	// suspend ensemble reviews without deleting the saved definitions.
	body := `{
	  "ensemble": {
	    "scanners": [
	      {"name": "a", "provider": "anthropic", "enabled": false},
	      {"name": "b", "provider": "openai",    "enabled": false}
	    ],
	    "arbiter": {"provider": "anthropic"}
	  }
	}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if shouldRunEnsemble([]string{"--config", cfgPath}) {
		t.Error("all-disabled scanners should route to legacy, got ensemble")
	}
}

func TestShouldRunEnsembleRoutesWhenAtLeastOneScannerEnabled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	body := `{
	  "ensemble": {
	    "scanners": [
	      {"name": "a", "provider": "anthropic", "enabled": false},
	      {"name": "b", "provider": "openai"}
	    ],
	    "arbiter": {"provider": "anthropic"}
	  }
	}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !shouldRunEnsemble([]string{"--config", cfgPath}) {
		t.Error("one enabled scanner should route to ensemble, got legacy")
	}
}
