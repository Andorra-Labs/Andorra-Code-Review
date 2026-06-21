package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-code-review/open-code-review/internal/configstore"
)

func newTestServer(t *testing.T) (*server, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"provider":"anthropic","model":"claude-opus-4-7"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return &server{configPath: p, csrfToken: "test-csrf-token"}, p
}

func TestOverviewRenders(t *testing.T) {
	s, _ := newTestServer(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "localhost:5484"
	w := httptest.NewRecorder()
	s.handleOverview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Overview") {
		t.Errorf("missing title: %s", body)
	}
	if !strings.Contains(body, "anthropic") {
		t.Errorf("upstream provider not surfaced: %s", body)
	}
}

func TestEnsembleGETRendersForm(t *testing.T) {
	s, _ := newTestServer(t)
	r := httptest.NewRequest("GET", "/ensemble", nil)
	r.Host = "localhost:5484"
	w := httptest.NewRecorder()
	s.handleEnsemble(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, "csrf_token") || !strings.Contains(body, "test-csrf-token") {
		t.Errorf("CSRF token not embedded: %s", body)
	}
	if !strings.Contains(body, "Scanners") {
		t.Errorf("scanners section missing: %s", body)
	}
}

func TestEnsemblePOSTRequiresCSRF(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{}
	form.Set("enabled", "true")
	form.Set("csrf_token", "wrong-token")
	r := httptest.NewRequest("POST", "/ensemble", strings.NewReader(form.Encode()))
	r.Host = "localhost:5484"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleEnsemble(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("code=%d, want 403", w.Code)
	}
}

func TestEnsemblePOSTValidatesBeforeSaving(t *testing.T) {
	s, path := newTestServer(t)
	form := url.Values{}
	form.Set("csrf_token", "test-csrf-token")
	form.Set("enabled", "true")
	// One scanner only -> validation should reject "at least 2 scanners"
	form.Set("scanner_name_0", "only")
	form.Set("scanner_provider_0", "anthropic")
	form.Set("scanner_model_0", "")
	form.Set("scanner_weight_0", "")
	form.Set("arbiter_provider", "")

	r := httptest.NewRequest("POST", "/ensemble", strings.NewReader(form.Encode()))
	r.Host = "localhost:5484"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleEnsemble(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with errors rendered, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "at least 2 scanners") {
		t.Errorf("validation error not surfaced: %s", w.Body)
	}
	// File should NOT have been modified
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "ensemble") {
		t.Errorf("invalid config got persisted to disk: %s", data)
	}
}

func TestEnsemblePOSTSavesValidConfig(t *testing.T) {
	s, path := newTestServer(t)
	form := url.Values{}
	form.Set("csrf_token", "test-csrf-token")
	form.Set("enabled", "true")
	form.Set("scanner_name_0", "opus")
	form.Set("scanner_provider_0", "anthropic")
	form.Set("scanner_model_0", "claude-opus-4-7")
	form.Set("scanner_weight_0", "1.0")
	form.Set("scanner_name_1", "gpt")
	form.Set("scanner_provider_1", "openai")
	form.Set("scanner_model_1", "gpt-5.5")
	form.Set("scanner_weight_1", "1.0")
	form.Set("arbiter_provider", "anthropic")
	form.Set("arbiter_model", "claude-opus-4-8")
	form.Set("arbiter_mode", "per_file")

	r := httptest.NewRequest("POST", "/ensemble", strings.NewReader(form.Encode()))
	r.Host = "localhost:5484"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleEnsemble(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
	// Reload and confirm
	ext, err := configstore.LoadAndorra(path)
	if err != nil {
		t.Fatalf("LoadAndorra: %v", err)
	}
	if ext.Ensemble == nil || !ext.Ensemble.Enabled || len(ext.Ensemble.Scanners) != 2 {
		t.Errorf("ensemble not persisted: %+v", ext.Ensemble)
	}
	// Confirm upstream block survived
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"provider": "anthropic"`) {
		t.Errorf("upstream provider lost: %s", raw)
	}
}

func TestExportGETRendersForm(t *testing.T) {
	s, _ := newTestServer(t)
	r := httptest.NewRequest("GET", "/export", nil)
	r.Host = "localhost:5484"
	w := httptest.NewRecorder()
	s.handleExport(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Placeholder") {
		t.Errorf("form text missing")
	}
}

func TestExportPOSTReturnsDownload(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{}
	form.Set("csrf_token", "test-csrf-token")
	form.Set("mode", "placeholder")
	r := httptest.NewRequest("POST", "/export", strings.NewReader(form.Encode()))
	r.Host = "localhost:5484"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleExport(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	if disp := w.Header().Get("Content-Disposition"); !strings.Contains(disp, "attachment") {
		t.Errorf("Content-Disposition=%q", disp)
	}
}

func TestExportPOSTRequiresCSRF(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{}
	form.Set("csrf_token", "wrong")
	r := httptest.NewRequest("POST", "/export", strings.NewReader(form.Encode()))
	r.Host = "localhost:5484"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleExport(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("code=%d, want 403", w.Code)
	}
}

func TestRandomTokenIsRandom(t *testing.T) {
	a, b := randomToken(), randomToken()
	if a == b {
		t.Error("tokens collided")
	}
	if len(a) != 32 {
		t.Errorf("len=%d, want 32", len(a))
	}
}
