package configstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	ext, err := LoadAndorra(path)
	if err != nil {
		t.Fatalf("LoadAndorra: %v", err)
	}
	if ext == nil {
		t.Fatal("ext is nil")
	}
	if ext.Ensemble != nil {
		t.Errorf("Ensemble = %+v, want nil", ext.Ensemble)
	}
}

func TestLoadIgnoresUpstreamKeys(t *testing.T) {
	path := writeFile(t, t.TempDir(), "config.json", `{
        "provider": "anthropic",
        "model": "claude-opus-4-7",
        "providers": {"anthropic": {"api_key": "sk-secret"}}
    }`)
	ext, err := LoadAndorra(path)
	if err != nil {
		t.Fatalf("LoadAndorra: %v", err)
	}
	if ext.Ensemble != nil {
		t.Errorf("Ensemble = %+v, want nil for upstream-only file", ext.Ensemble)
	}
}

func TestLoadReadsEnsembleBlock(t *testing.T) {
	path := writeFile(t, t.TempDir(), "config.json", `{
        "ensemble": {
            "enabled": true,
            "scanners": [
                {"name": "opus", "provider": "anthropic", "model": "claude-opus-4-7"},
                {"name": "gpt",  "provider": "openai",    "model": "gpt-5.5"}
            ],
            "arbiter": {"provider": "anthropic", "model": "claude-opus-4-8", "mode": "per_file"}
        }
    }`)
	ext, err := LoadAndorra(path)
	if err != nil {
		t.Fatalf("LoadAndorra: %v", err)
	}
	if ext.Ensemble == nil {
		t.Fatal("Ensemble = nil")
	}
	if !ext.Ensemble.Enabled {
		t.Error("Enabled = false")
	}
	if len(ext.Ensemble.Scanners) != 2 {
		t.Fatalf("Scanners len = %d, want 2", len(ext.Ensemble.Scanners))
	}
	if ext.Ensemble.Scanners[0].Name != "opus" || ext.Ensemble.Scanners[1].Provider != "openai" {
		t.Errorf("Scanners = %+v", ext.Ensemble.Scanners)
	}
	if ext.Ensemble.Arbiter == nil || ext.Ensemble.Arbiter.Mode != "per_file" {
		t.Errorf("Arbiter = %+v", ext.Ensemble.Arbiter)
	}
}

func TestSavePreservesUpstreamKeys(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.json", `{
        "provider": "anthropic",
        "model": "claude-opus-4-7",
        "providers": {
            "anthropic": {"api_key": "sk-ant-secret", "models": ["claude-opus-4-7"]}
        },
        "language": "en",
        "telemetry": {"enabled": true, "exporter": "console"}
    }`)
	ext := &AndorraExt{
		Ensemble: &EnsembleConfig{
			Enabled: true,
			Scanners: []ScannerSpec{
				{Name: "opus", Provider: "anthropic", Model: "claude-opus-4-7"},
				{Name: "gpt", Provider: "openai", Model: "gpt-5.5"},
			},
			Arbiter: &ArbiterSpec{Provider: "anthropic", Model: "claude-opus-4-8"},
		},
	}
	if err := SaveAndorra(path, ext); err != nil {
		t.Fatalf("SaveAndorra: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &got); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got["provider"] != "anthropic" {
		t.Errorf("provider lost: %v", got["provider"])
	}
	if got["model"] != "claude-opus-4-7" {
		t.Errorf("model lost: %v", got["model"])
	}
	if got["language"] != "en" {
		t.Errorf("language lost: %v", got["language"])
	}
	providers, ok := got["providers"].(map[string]any)
	if !ok {
		t.Fatalf("providers lost or wrong type: %T %v", got["providers"], got["providers"])
	}
	anthropic, ok := providers["anthropic"].(map[string]any)
	if !ok {
		t.Fatalf("providers.anthropic lost: %v", providers)
	}
	if anthropic["api_key"] != "sk-ant-secret" {
		t.Errorf("api_key changed: %v", anthropic["api_key"])
	}
	if telem, ok := got["telemetry"].(map[string]any); !ok || telem["enabled"] != true {
		t.Errorf("telemetry block lost or mangled: %v", got["telemetry"])
	}
	ens, ok := got["ensemble"].(map[string]any)
	if !ok {
		t.Fatalf("ensemble block missing: %v", got)
	}
	if ens["enabled"] != true {
		t.Errorf("ensemble.enabled = %v, want true", ens["enabled"])
	}
}

func TestSavePreservesUnknownFields(t *testing.T) {
	// Upstream may add new top-level keys in future releases. The fork must
	// pass them through untouched on save so a rebase does not require
	// configstore changes.
	dir := t.TempDir()
	path := writeFile(t, dir, "config.json", `{
        "provider": "anthropic",
        "future_upstream_field": {"nested": [1, 2, 3], "flag": true},
        "another_new_one": "hello"
    }`)
	if err := SaveAndorra(path, &AndorraExt{
		Ensemble: &EnsembleConfig{
			Enabled: true,
			Scanners: []ScannerSpec{
				{Name: "a", Provider: "anthropic"},
				{Name: "b", Provider: "openai"},
			},
			Arbiter: &ArbiterSpec{Provider: "anthropic"},
		},
	}); err != nil {
		t.Fatalf("SaveAndorra: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &got); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	fut, ok := got["future_upstream_field"].(map[string]any)
	if !ok {
		t.Fatalf("future_upstream_field lost: %v", got["future_upstream_field"])
	}
	if fut["flag"] != true {
		t.Errorf("future.flag changed: %v", fut["flag"])
	}
	nested, ok := fut["nested"].([]any)
	if !ok || len(nested) != 3 {
		t.Errorf("future.nested mangled: %v", fut["nested"])
	}
	if got["another_new_one"] != "hello" {
		t.Errorf("another_new_one lost: %v", got["another_new_one"])
	}
}

func TestSaveNilEnsembleRemovesKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.json", `{
        "provider": "anthropic",
        "ensemble": {"enabled": true, "scanners": []}
    }`)
	if err := SaveAndorra(path, &AndorraExt{Ensemble: nil}); err != nil {
		t.Fatalf("SaveAndorra: %v", err)
	}
	content := readFile(t, path)
	if strings.Contains(content, "ensemble") {
		t.Errorf("ensemble key not removed:\n%s", content)
	}
	if !strings.Contains(content, "anthropic") {
		t.Errorf("upstream key lost:\n%s", content)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	temp := 0.2
	want := &AndorraExt{
		Ensemble: &EnsembleConfig{
			Enabled: true,
			Serial:  true,
			Scanners: []ScannerSpec{
				{Name: "opus", Provider: "anthropic", Model: "claude-opus-4-7", Weight: 1.5, Temperature: &temp, MaxTokens: 4096, PromptTag: "deep", Iterations: 3},
				{Name: "gpt", Provider: "openai", Model: "gpt-5.5"},
			},
			Arbiter: &ArbiterSpec{Provider: "anthropic", Model: "claude-opus-4-8", Mode: "per_file"},
			Dedup:   &DedupConfig{LineOverlapMinRatio: 0.5, TitleSimilarityMin: 0.7, RequireSamePath: true},
			Output:  &EnsembleOutput{DefaultVerdicts: []string{"accepted_bug"}, ShowProvenance: false},
		},
	}
	if err := SaveAndorra(path, want); err != nil {
		t.Fatalf("SaveAndorra: %v", err)
	}
	got, err := LoadAndorra(path)
	if err != nil {
		t.Fatalf("LoadAndorra: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got.Ensemble, want.Ensemble)
	}
}

func TestSaveCreatesDirAndFileMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	path := filepath.Join(dir, "config.json")
	if err := SaveAndorra(path, &AndorraExt{}); err != nil {
		t.Fatalf("SaveAndorra: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
}

func TestValidateAcceptsNilAndEmpty(t *testing.T) {
	if errs := Validate(nil); errs != nil {
		t.Errorf("Validate(nil) returned %v", errs)
	}
	if errs := Validate(&AndorraExt{}); errs != nil {
		t.Errorf("Validate(empty) returned %v", errs)
	}
}

func TestValidateRejectsScannersWithoutArbiter(t *testing.T) {
	ext := &AndorraExt{Ensemble: &EnsembleConfig{
		Scanners: []ScannerSpec{{Name: "only", Provider: "anthropic"}},
	}}
	errs := Validate(ext)
	if len(errs) == 0 {
		t.Fatal("expected errors")
	}
	if !strings.Contains(joinErrs(errs), "arbiter") {
		t.Errorf("missing arbiter error: %v", errs)
	}
}

func TestValidateAcceptsEmptyEnsemble(t *testing.T) {
	ext := &AndorraExt{Ensemble: &EnsembleConfig{}}
	if errs := Validate(ext); errs != nil {
		t.Errorf("Validate(empty ensemble) returned %v", errs)
	}
}

func TestValidateRejectsDuplicateScannerNames(t *testing.T) {
	ext := &AndorraExt{Ensemble: &EnsembleConfig{
		Enabled: true,
		Scanners: []ScannerSpec{
			{Name: "dup", Provider: "anthropic"},
			{Name: "dup", Provider: "openai"},
		},
		Arbiter: &ArbiterSpec{Provider: "anthropic"},
	}}
	errs := Validate(ext)
	if !containsErr(errs, "duplicate name") {
		t.Errorf("missing duplicate-name error: %v", errs)
	}
}

func TestValidateAcceptsValidEnsemble(t *testing.T) {
	ext := &AndorraExt{Ensemble: &EnsembleConfig{
		Enabled: true,
		Scanners: []ScannerSpec{
			{Name: "opus", Provider: "anthropic", Model: "claude-opus-4-7"},
			{Name: "gpt", Provider: "openai", Model: "gpt-5.5"},
		},
		Arbiter: &ArbiterSpec{Provider: "anthropic", Mode: "per_file"},
	}}
	if errs := Validate(ext); len(errs) != 0 {
		t.Errorf("Validate returned %v", errs)
	}
}

func TestValidateRejectsNegativeIterations(t *testing.T) {
	ext := &AndorraExt{Ensemble: &EnsembleConfig{
		Enabled: true,
		Scanners: []ScannerSpec{
			{Name: "a", Provider: "anthropic", Iterations: -1},
			{Name: "b", Provider: "openai"},
		},
		Arbiter: &ArbiterSpec{Provider: "anthropic"},
	}}
	if !containsErr(Validate(ext), "iterations") {
		t.Errorf("expected iterations error")
	}
}

func TestValidateRejectsBadArbiterMode(t *testing.T) {
	ext := &AndorraExt{Ensemble: &EnsembleConfig{
		Enabled: true,
		Scanners: []ScannerSpec{
			{Name: "a", Provider: "anthropic"},
			{Name: "b", Provider: "openai"},
		},
		Arbiter: &ArbiterSpec{Provider: "anthropic", Mode: "bogus"},
	}}
	if !containsErr(Validate(ext), "mode") {
		t.Errorf("expected arbiter.mode error")
	}
}

func TestValidateRejectsOutOfRangeDedup(t *testing.T) {
	ext := &AndorraExt{Ensemble: &EnsembleConfig{
		Dedup: &DedupConfig{LineOverlapMinRatio: 1.5, TitleSimilarityMin: -0.1},
	}}
	errs := Validate(ext)
	if !containsErr(errs, "line_overlap_min_ratio") {
		t.Errorf("missing line_overlap_min_ratio error: %v", errs)
	}
	if !containsErr(errs, "title_similarity_min") {
		t.Errorf("missing title_similarity_min error: %v", errs)
	}
}

func TestDefaultPathReturnsExpectedSuffix(t *testing.T) {
	p, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(".opencodereview", "config.json")
	if !strings.HasSuffix(p, want) {
		t.Errorf("DefaultPath = %q, want suffix %q", p, want)
	}
}

func joinErrs(errs []error) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "\n")
}

func containsErr(errs []error, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), sub) {
			return true
		}
	}
	return false
}
