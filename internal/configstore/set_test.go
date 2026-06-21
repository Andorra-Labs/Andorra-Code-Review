package configstore

import (
	"strings"
	"testing"
)

func TestSetEnabled(t *testing.T) {
	ext := &AndorraExt{}
	if err := Set(ext, "ensemble.enabled", "true"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if ext.Ensemble == nil || !ext.Ensemble.Enabled {
		t.Errorf("Enabled = false")
	}
	if err := Set(ext, "ensemble.enabled", "notabool"); err == nil {
		t.Error("expected error for bad bool")
	}
}

func TestSetScannersJSON(t *testing.T) {
	ext := &AndorraExt{}
	scs := `[{"name":"opus","provider":"anthropic","model":"claude-opus-4-7"},{"name":"gpt","provider":"openai"}]`
	if err := Set(ext, "ensemble.scanners", scs); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(ext.Ensemble.Scanners) != 2 {
		t.Fatalf("len=%d, want 2", len(ext.Ensemble.Scanners))
	}
	if ext.Ensemble.Scanners[0].Name != "opus" || ext.Ensemble.Scanners[1].Provider != "openai" {
		t.Errorf("scanners=%+v", ext.Ensemble.Scanners)
	}
}

func TestSetArbiterFields(t *testing.T) {
	ext := &AndorraExt{}
	must := func(k, v string) {
		t.Helper()
		if err := Set(ext, k, v); err != nil {
			t.Fatalf("Set(%s,%s): %v", k, v, err)
		}
	}
	must("ensemble.arbiter.provider", "anthropic")
	must("ensemble.arbiter.model", "claude-opus-4-8")
	must("ensemble.arbiter.mode", "per_file")
	must("ensemble.arbiter.temperature", "0.2")
	must("ensemble.arbiter.max_tokens", "4096")
	a := ext.Ensemble.Arbiter
	if a == nil {
		t.Fatal("Arbiter nil")
	}
	if a.Provider != "anthropic" || a.Model != "claude-opus-4-8" || a.Mode != "per_file" || a.MaxTokens != 4096 {
		t.Errorf("arbiter=%+v", a)
	}
	if a.Temperature == nil || *a.Temperature != 0.2 {
		t.Errorf("temperature=%v", a.Temperature)
	}

	if err := Set(ext, "ensemble.arbiter.mode", "bogus"); err == nil {
		t.Error("expected error for bogus mode")
	}
	if err := Set(ext, "ensemble.arbiter.max_tokens", "-1"); err == nil {
		t.Error("expected error for negative max_tokens")
	}
}

func TestSetDedupAndOutput(t *testing.T) {
	ext := &AndorraExt{}
	must := func(k, v string) {
		t.Helper()
		if err := Set(ext, k, v); err != nil {
			t.Fatalf("Set(%s,%s): %v", k, v, err)
		}
	}
	must("ensemble.dedup.line_overlap_min_ratio", "0.5")
	must("ensemble.dedup.title_similarity_min", "0.7")
	must("ensemble.dedup.require_same_path", "true")
	must("ensemble.dedup.existing_code_exact_boost", "false")
	must("ensemble.output.default_verdicts", "accepted_bug,uncertain")
	must("ensemble.output.show_provenance", "true")

	d := ext.Ensemble.Dedup
	if d == nil || d.LineOverlapMinRatio != 0.5 || d.TitleSimilarityMin != 0.7 || !d.RequireSamePath {
		t.Errorf("dedup=%+v", d)
	}
	if d.ExistingCodeExactBoost {
		t.Error("ExistingCodeExactBoost should be false")
	}
	o := ext.Ensemble.Output
	if o == nil || !o.ShowProvenance {
		t.Errorf("output=%+v", o)
	}
	if len(o.DefaultVerdicts) != 2 || o.DefaultVerdicts[0] != "accepted_bug" || o.DefaultVerdicts[1] != "uncertain" {
		t.Errorf("verdicts=%v", o.DefaultVerdicts)
	}
}

func TestSetDefaultVerdictsJSONArray(t *testing.T) {
	ext := &AndorraExt{}
	if err := Set(ext, "ensemble.output.default_verdicts", `["accepted_bug","style_only"]`); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := ext.Ensemble.Output.DefaultVerdicts
	if len(got) != 2 || got[0] != "accepted_bug" || got[1] != "style_only" {
		t.Errorf("verdicts=%v", got)
	}
}

func TestSetRejectsNonEnsembleKey(t *testing.T) {
	ext := &AndorraExt{}
	if err := Set(ext, "provider", "anthropic"); err == nil {
		t.Error("expected error for non-ensemble key")
	}
	if err := Set(ext, "ensemble.unknown_field", "x"); err == nil {
		t.Error("expected error for unknown ensemble key")
	}
}

func TestSetNilExt(t *testing.T) {
	if err := Set(nil, "ensemble.enabled", "true"); err == nil {
		t.Error("expected error for nil ext")
	}
}

func TestParseStringList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,, a ,c ", []string{"a", "b", "c"}},
		{`["a","b","c"]`, []string{"a", "b", "c"}},
		{"[a,b,c]", []string{"a", "b", "c"}},
	}
	for _, c := range cases {
		got := parseStringList(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseStringList(%q) len=%d, want %d", c.in, len(got), len(c.want))
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("parseStringList(%q)[%d]=%q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestSetEnsembleSaveRoundTrip(t *testing.T) {
	path := writeFile(t, t.TempDir(), "c.json", `{"provider":"anthropic"}`)
	ext, err := LoadAndorra(path)
	if err != nil {
		t.Fatalf("LoadAndorra: %v", err)
	}
	if err := Set(ext, "ensemble.enabled", "true"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Set(ext, "ensemble.scanners",
		`[{"name":"a","provider":"anthropic"},{"name":"b","provider":"openai"}]`); err != nil {
		t.Fatalf("Set scanners: %v", err)
	}
	if err := Set(ext, "ensemble.arbiter.provider", "anthropic"); err != nil {
		t.Fatalf("Set arbiter: %v", err)
	}
	if errs := Validate(ext); len(errs) != 0 {
		t.Fatalf("Validate: %v", errs)
	}
	if err := SaveAndorra(path, ext); err != nil {
		t.Fatalf("SaveAndorra: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, `"provider": "anthropic"`) {
		t.Errorf("upstream provider lost:\n%s", got)
	}
	if !strings.Contains(got, `"ensemble"`) {
		t.Errorf("ensemble missing:\n%s", got)
	}
}
