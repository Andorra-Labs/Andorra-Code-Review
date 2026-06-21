package configstore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExportPlaceholderRewritesSecrets(t *testing.T) {
	path := writeFile(t, t.TempDir(), "c.json", `{
        "provider": "anthropic",
        "providers": {
            "anthropic": {"api_key": "sk-ant-secret", "models": ["claude-opus-4-7"]},
            "openai":    {"api_key": "sk-openai-secret"}
        },
        "custom_providers": {
            "my-gw": {"api_key": "k", "url": "https://gw.example.com/v1", "protocol": "openai"}
        },
        "llm": {"url": "https://x", "auth_token": "legacy-secret", "model": "m"}
    }`)
	out, err := Export(path, ExportOptions{Mode: ModePlaceholder})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "sk-ant-secret") || strings.Contains(s, "sk-openai-secret") || strings.Contains(s, "legacy-secret") || strings.Contains(s, "\"k\"") {
		t.Errorf("raw secrets leaked into export:\n%s", s)
	}
	if !strings.Contains(s, "${env:OCR_ANTHROPIC_API_KEY}") {
		t.Errorf("missing anthropic placeholder:\n%s", s)
	}
	if !strings.Contains(s, "${env:OCR_OPENAI_API_KEY}") {
		t.Errorf("missing openai placeholder:\n%s", s)
	}
	if !strings.Contains(s, "${env:OCR_MY_GW_API_KEY}") {
		t.Errorf("missing custom provider placeholder:\n%s", s)
	}
	if !strings.Contains(s, "${env:OCR_LLM_AUTH_TOKEN}") {
		t.Errorf("missing legacy llm token placeholder:\n%s", s)
	}

	// Verify upstream non-secret fields are preserved
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed["provider"] != "anthropic" {
		t.Errorf("provider lost")
	}
	custom := parsed["custom_providers"].(map[string]any)["my-gw"].(map[string]any)
	if custom["url"] != "https://gw.example.com/v1" {
		t.Errorf("url lost: %v", custom["url"])
	}
}

func TestExportStripRemovesSecrets(t *testing.T) {
	path := writeFile(t, t.TempDir(), "c.json", `{
        "providers": {"anthropic": {"api_key": "sk-x", "models": ["m"]}}
    }`)
	out, err := Export(path, ExportOptions{Mode: ModeStrip})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if strings.Contains(string(out), "sk-x") {
		t.Errorf("secret leaked")
	}
	if strings.Contains(string(out), "api_key") {
		t.Errorf("api_key key should be removed entirely:\n%s", out)
	}
	if !strings.Contains(string(out), `"models"`) {
		t.Errorf("non-secret field lost:\n%s", out)
	}
}

func TestExportPreservesEnsembleBlock(t *testing.T) {
	path := writeFile(t, t.TempDir(), "c.json", `{
        "ensemble": {"enabled": true, "scanners": [{"name":"a","provider":"anthropic"}]}
    }`)
	out, err := Export(path, ExportOptions{Mode: ModePlaceholder})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(string(out), `"ensemble"`) {
		t.Errorf("ensemble block lost:\n%s", out)
	}
}

func TestEnvForProviderKey(t *testing.T) {
	cases := map[string]string{
		"anthropic":     "OCR_ANTHROPIC_API_KEY",
		"openai":        "OCR_OPENAI_API_KEY",
		"my-gateway":    "OCR_MY_GATEWAY_API_KEY",
		"corp.gateway":  "OCR_CORP_GATEWAY_API_KEY",
	}
	for in, want := range cases {
		if got := envForProviderKey("OCR_", in); got != want {
			t.Errorf("envForProviderKey(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestExportMissingFileIsEmpty(t *testing.T) {
	out, err := Export("/nonexistent/file.json", ExportOptions{Mode: ModePlaceholder})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if strings.TrimSpace(string(out)) != "{}" {
		t.Errorf("expected empty object, got %q", out)
	}
}
