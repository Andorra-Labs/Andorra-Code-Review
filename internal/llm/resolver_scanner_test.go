package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestResolveProviderPreset(t *testing.T) {
	path := writeCfg(t, `{
        "providers": {
            "anthropic": {"api_key": "sk-ant-x", "models": ["claude-opus-4-7"]}
        }
    }`)
	ep, err := ResolveProvider(path, "anthropic", "claude-opus-4-7")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if ep.Token != "sk-ant-x" {
		t.Errorf("Token=%q", ep.Token)
	}
	if ep.Model != "claude-opus-4-7" {
		t.Errorf("Model=%q", ep.Model)
	}
	if ep.Protocol != "anthropic" {
		t.Errorf("Protocol=%q", ep.Protocol)
	}
	if ep.Source != "scanner:anthropic" {
		t.Errorf("Source=%q", ep.Source)
	}
}

func TestResolveProviderCustom(t *testing.T) {
	path := writeCfg(t, `{
        "custom_providers": {
            "my-gw": {"api_key":"k","url":"https://gw.example.com/v1","protocol":"openai","model":"llama"}
        }
    }`)
	ep, err := ResolveProvider(path, "my-gw", "llama")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if ep.URL != "https://gw.example.com/v1" || ep.Protocol != "openai" || ep.Model != "llama" {
		t.Errorf("ep=%+v", ep)
	}
}

func TestResolveProviderExpandsModelPlaceholder(t *testing.T) {
	t.Setenv("OCR_SPARK_LLM_MODEL", "Qwen3.6-27B-Text-NVFP4-MTP")
	path := writeCfg(t, `{
        "custom_providers": {
            "Spark": {
                "api_key": "k",
                "url": "http://spark.local:9090/v1",
                "protocol": "openai",
                "models": ["${env:OCR_SPARK_LLM_MODEL}"]
            }
        }
    }`)
	ep, err := ResolveProvider(path, "Spark", "${env:OCR_SPARK_LLM_MODEL}")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if ep.Model != "Qwen3.6-27B-Text-NVFP4-MTP" {
		t.Errorf("Model=%q, want expanded env value", ep.Model)
	}
}

func TestResolveProviderExpandsModelPlaceholderStripsSuffix(t *testing.T) {
	t.Setenv("OCR_SPARK_LLM_MODEL", "Qwen3.6-27B[1m]")
	path := writeCfg(t, `{
        "custom_providers": {
            "Spark": {"api_key":"k","url":"http://spark.local/v1","protocol":"openai","models":["${env:OCR_SPARK_LLM_MODEL}"]}
        }
    }`)
	ep, err := ResolveProvider(path, "Spark", "${env:OCR_SPARK_LLM_MODEL}")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if ep.Model != "Qwen3.6-27B" {
		t.Errorf("Model=%q, want suffix stripped after env expansion", ep.Model)
	}
}

func TestResolveProviderModelPlaceholderUnsetErrors(t *testing.T) {
	t.Setenv("OCR_SPARK_LLM_MODEL", "")
	path := writeCfg(t, `{
        "custom_providers": {
            "Spark": {"api_key":"k","url":"http://spark.local/v1","protocol":"openai","models":["${env:OCR_SPARK_LLM_MODEL}"]}
        }
    }`)
	if _, err := ResolveProvider(path, "Spark", "${env:OCR_SPARK_LLM_MODEL}"); err == nil {
		t.Error("expected error when OCR_SPARK_LLM_MODEL is unset")
	}
}

func TestResolveProviderUnknown(t *testing.T) {
	path := writeCfg(t, `{"providers": {"anthropic": {"api_key": "x"}}}`)
	if _, err := ResolveProvider(path, "openai", "gpt"); err == nil {
		t.Error("expected error for unconfigured provider")
	}
}

func TestResolveProviderMissingFile(t *testing.T) {
	if _, err := ResolveProvider(filepath.Join(t.TempDir(), "missing.json"), "anthropic", "x"); err == nil {
		t.Error("expected error for missing file")
	}
}
