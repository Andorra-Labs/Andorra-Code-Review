package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldRunEnsembleFallsBackWhenAllScannersDisabled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	// Two scanners, both disabled, plus a valid arbiter, and no top-level
	// provider/model. Without a runnable provider/model there is nothing to
	// auto-promote, so this falls back to legacy.
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

func TestShouldRunEnsembleAutoPromotesLegacyProviderModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	body := `{
	  "provider": "anthropic",
	  "model": "claude-sonnet-4-6",
	  "providers": {
	    "anthropic": {"api_key": "sk-test"}
	  }
	}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !shouldRunEnsemble([]string{"--config", cfgPath}) {
		t.Error("legacy provider/model config should route to ensemble, got legacy")
	}
}

func TestShouldRunEnsembleFallsBackWhenNoEnsembleAndNoProviderModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	body := `{
	  "providers": {
	    "anthropic": {"api_key": "sk-test"}
	  }
	}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if shouldRunEnsemble([]string{"--config", cfgPath}) {
		t.Error("config with no scanners and no provider/model should route to legacy, got ensemble")
	}
}

func TestShouldRunEnsembleNoEnsembleFlagForcesLegacyEvenWithProviderModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	body := `{
	  "provider": "anthropic",
	  "model": "claude-sonnet-4-6"
	}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if shouldRunEnsemble([]string{"--no-ensemble", "--config", cfgPath}) {
		t.Error("--no-ensemble should force legacy even with provider/model")
	}
}
