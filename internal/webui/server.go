// Package webui is the fork's local web config UI. It edits ONLY the
// ensemble extension block via internal/configstore; upstream provider /
// model / llm credentials stay editable via the existing CLI surfaces.
//
// The server mirrors internal/viewer/server.go in style (stdlib net/http,
// embed.FS, html/template, host-guard middleware) but does not import
// viewer — see internal/httpguard for the duplicated guard.
package webui

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/httpguard"
)

//go:embed templates/*.html static/*.css static/*.js
var assets embed.FS

// Options configure StartServer.
type Options struct {
	Addr        string // e.g. "localhost:5484"
	ConfigPath  string // absolute path to ~/.opencodereview/config.json
	OpenBrowser bool
}

// StartServer launches the web UI and blocks until the server stops.
func StartServer(opts Options) error {
	if opts.Addr == "" {
		opts.Addr = "localhost:5484"
	}
	if opts.ConfigPath == "" {
		p, err := configstore.DefaultPath()
		if err != nil {
			return err
		}
		opts.ConfigPath = p
	}

	srvState := &server{
		configPath: opts.ConfigPath,
		csrfToken:  randomToken(),
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS()))))
	mux.HandleFunc("/", srvState.handleOverview)
	mux.HandleFunc("/ensemble", srvState.handleEnsemble)
	mux.HandleFunc("/export", srvState.handleExport)

	allowed := httpguard.ResolveAllowedHostsFromEnv(opts.Addr)
	handler := httpguard.Middleware(allowed, mux)

	srv := &http.Server{
		Addr:         opts.Addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	fmt.Printf("\nAndorra OCR config UI: http://%s\n", opts.Addr)
	fmt.Printf("Editing: %s\n\n", opts.ConfigPath)
	if opts.OpenBrowser {
		go openBrowser("http://" + opts.Addr)
	}
	return srv.ListenAndServe()
}

type server struct {
	configPath string
	csrfToken  string
	flashMu    sync.Mutex
	flash      string
	flashKind  string // "success" | "error"
}

func (s *server) consumeFlash() (string, string) {
	s.flashMu.Lock()
	defer s.flashMu.Unlock()
	f, k := s.flash, s.flashKind
	s.flash, s.flashKind = "", ""
	return f, k
}

func (s *server) setFlash(msg, kind string) {
	s.flashMu.Lock()
	defer s.flashMu.Unlock()
	s.flash, s.flashKind = msg, kind
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ext, _ := configstore.LoadAndorra(s.configPath)
	overview := overviewData(s.configPath, ext)
	body := renderInner("overview.html", overview)
	s.renderLayout(w, "Overview", body, nil)
}

func (s *server) handleEnsemble(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.renderEnsembleForm(w, nil, nil)
	case http.MethodPost:
		s.handleEnsembleSubmit(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) renderEnsembleForm(w http.ResponseWriter, ext *configstore.AndorraExt, errs []error) {
	if ext == nil {
		ext, _ = configstore.LoadAndorra(s.configPath)
	}
	if ext == nil {
		ext = &configstore.AndorraExt{}
	}
	data := ensembleData(ext, s.csrfToken)
	body := renderInner("ensemble.html", data)
	s.renderLayout(w, "Ensemble", body, errs)
}

func (s *server) handleEnsembleSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	if r.PostFormValue("csrf_token") != s.csrfToken {
		http.Error(w, "CSRF token mismatch", http.StatusForbidden)
		return
	}
	// Load the existing config first so we can preserve scanner / arbiter
	// fields the form does not expose (Enabled, Temperature, MaxTokens,
	// PromptTag, etc). Without this, every save would clobber those values.
	prior, err := configstore.LoadAndorra(s.configPath)
	if err != nil {
		s.renderEnsembleForm(w, nil, []error{err})
		return
	}
	ext := buildExtFromForm(r)
	mergeHiddenFields(ext, prior)
	if errs := configstore.Validate(ext); len(errs) > 0 {
		s.renderEnsembleForm(w, ext, errs)
		return
	}
	if err := configstore.SaveAndorra(s.configPath, ext); err != nil {
		s.renderEnsembleForm(w, ext, []error{err})
		return
	}
	s.setFlash("Ensemble configuration saved.", "success")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// mergeHiddenFields copies fields that are not exposed by the ensemble form
// (per-scanner Enabled / Temperature / MaxTokens / PromptTag; per-arbiter
// Temperature) from the on-disk config onto the form-built ext so a save
// never silently re-enables a disabled scanner or wipes a custom temperature.
func mergeHiddenFields(ext, prior *configstore.AndorraExt) {
	if ext == nil || ext.Ensemble == nil || prior == nil || prior.Ensemble == nil {
		return
	}
	priorByName := map[string]configstore.ScannerSpec{}
	for _, s := range prior.Ensemble.Scanners {
		priorByName[s.Name] = s
	}
	for i := range ext.Ensemble.Scanners {
		name := ext.Ensemble.Scanners[i].Name
		old, ok := priorByName[name]
		if !ok {
			continue
		}
		// The form does not expose these fields; carry them over verbatim.
		if ext.Ensemble.Scanners[i].Enabled == nil {
			ext.Ensemble.Scanners[i].Enabled = old.Enabled
		}
		if ext.Ensemble.Scanners[i].Temperature == nil {
			ext.Ensemble.Scanners[i].Temperature = old.Temperature
		}
		if ext.Ensemble.Scanners[i].MaxTokens == 0 {
			ext.Ensemble.Scanners[i].MaxTokens = old.MaxTokens
		}
		if ext.Ensemble.Scanners[i].PromptTag == "" {
			ext.Ensemble.Scanners[i].PromptTag = old.PromptTag
		}
	}
	if ext.Ensemble.Arbiter != nil && prior.Ensemble.Arbiter != nil {
		if ext.Ensemble.Arbiter.Temperature == nil {
			ext.Ensemble.Arbiter.Temperature = prior.Ensemble.Arbiter.Temperature
		}
		if ext.Ensemble.Arbiter.MaxTokens == 0 {
			ext.Ensemble.Arbiter.MaxTokens = prior.Ensemble.Arbiter.MaxTokens
		}
	}
	// Dedup booleans aren't exposed by the form. Without merging, tuning a
	// threshold would silently disable RequireSamePath / ExistingCodeExactBoost
	// (their zero values), changing grouping semantics on every save. When the
	// prior config had no dedup block, fall back to the documented defaults so
	// the first threshold tweak doesn't disable guards the user never saw.
	if ext.Ensemble.Dedup != nil {
		if prior.Ensemble.Dedup != nil {
			ext.Ensemble.Dedup.RequireSamePath = prior.Ensemble.Dedup.RequireSamePath
			ext.Ensemble.Dedup.ExistingCodeExactBoost = prior.Ensemble.Dedup.ExistingCodeExactBoost
		} else {
			ext.Ensemble.Dedup.RequireSamePath = true
			ext.Ensemble.Dedup.ExistingCodeExactBoost = true
		}
	}
}

func (s *server) handleExport(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		body := renderInner("export.html", map[string]any{"CSRFToken": s.csrfToken})
		s.renderLayout(w, "Export", body, nil)
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
			return
		}
		if r.PostFormValue("csrf_token") != s.csrfToken {
			http.Error(w, "CSRF token mismatch", http.StatusForbidden)
			return
		}
		mode := configstore.ModePlaceholder
		if r.PostFormValue("mode") == "strip" {
			mode = configstore.ModeStrip
		}
		data, err := configstore.Export(s.configPath, configstore.ExportOptions{Mode: mode})
		if err != nil {
			http.Error(w, "export: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="config.json"`)
		_, _ = w.Write(data)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func staticFS() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

var layoutTmpl = func() *template.Template {
	t, err := template.ParseFS(assets, "templates/layout.html")
	if err != nil {
		panic(err)
	}
	return t
}()

func (s *server) renderLayout(w http.ResponseWriter, title string, body template.HTML, errs []error) {
	flash, kind := s.consumeFlash()
	data := map[string]any{
		"Title":      title,
		"Body":       body,
		"Flash":      flash,
		"FlashKind":  kind,
		"Errors":     errsToStrings(errs),
		"ConfigPath": s.configPath,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := layoutTmpl.Execute(w, data); err != nil {
		fmt.Printf("[webui] layout execute: %v\n", err)
	}
}

// renderInner renders one inner template (e.g. overview.html) to a string
// suitable for {{.Body}} substitution in layout.html.
func renderInner(name string, data any) template.HTML {
	t, err := template.ParseFS(assets, "templates/"+name)
	if err != nil {
		return template.HTML(fmt.Sprintf("<pre>template parse %s: %v</pre>", name, err))
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return template.HTML(fmt.Sprintf("<pre>template execute %s: %v</pre>", name, err))
	}
	return template.HTML(b.String())
}

func errsToStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}

func randomToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fallback-token"
	}
	return hex.EncodeToString(b[:])
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, url)
	_ = exec.Command(cmd, args...).Start()
}

// --- view-model builders ---

func overviewData(configPath string, ext *configstore.AndorraExt) map[string]any {
	// Read just the upstream provider/model fields from the same file.
	var upstream struct {
		Provider string          `json:"provider"`
		Model    string          `json:"model"`
		Llm      json.RawMessage `json:"llm"`
	}
	raw, _ := readRaw(configPath)
	if pBytes, ok := raw["provider"]; ok {
		_ = json.Unmarshal(pBytes, &upstream.Provider)
	}
	if mBytes, ok := raw["model"]; ok {
		_ = json.Unmarshal(mBytes, &upstream.Model)
	}

	enabled := false
	scannerCount := 0
	arbiterModel := ""
	if ext != nil && ext.Ensemble != nil {
		scannerCount = len(ext.Ensemble.Scanners)
		// "Enabled" in the UI status means the ensemble path will run —
		// which now depends on scanner presence, not a separate flag.
		enabled = scannerCount > 0
		if ext.Ensemble.Arbiter != nil {
			arbiterModel = ext.Ensemble.Arbiter.Model
		}
	}
	return map[string]any{
		"Provider":        upstream.Provider,
		"Model":           upstream.Model,
		"EnsembleEnabled": enabled,
		"ScannerCount":    scannerCount,
		"ArbiterModel":    arbiterModel,
	}
}

func ensembleData(ext *configstore.AndorraExt, token string) map[string]any {
	e := ext.Ensemble
	if e == nil {
		e = &configstore.EnsembleConfig{}
	}
	a := e.Arbiter
	if a == nil {
		a = &configstore.ArbiterSpec{}
	}
	d := e.Dedup
	if d == nil {
		d = &configstore.DedupConfig{}
	}
	o := e.Output
	if o == nil {
		o = &configstore.EnsembleOutput{}
	}
	scanners := e.Scanners
	if len(scanners) == 0 {
		scanners = []configstore.ScannerSpec{{}}
	}
	return map[string]any{
		"E":                 e,
		"Scanners":          scanners,
		"A":                 a,
		"D":                 d,
		"O":                 o,
		"OutputVerdictsCSV": strings.Join(o.DefaultVerdicts, ", "),
		"CSRFToken":         token,
	}
}

func buildExtFromForm(r *http.Request) *configstore.AndorraExt {
	ext := &configstore.AndorraExt{
		Ensemble: &configstore.EnsembleConfig{
			Enabled: r.PostFormValue("enabled") == "true",
		},
	}
	// Scanners
	for i := 0; ; i++ {
		name := strings.TrimSpace(r.PostFormValue(fmt.Sprintf("scanner_name_%d", i)))
		provider := strings.TrimSpace(r.PostFormValue(fmt.Sprintf("scanner_provider_%d", i)))
		_, present := r.PostForm[fmt.Sprintf("scanner_name_%d", i)]
		if !present {
			break
		}
		if name == "" && provider == "" {
			continue
		}
		s := configstore.ScannerSpec{
			Name:     name,
			Provider: provider,
			Model:    strings.TrimSpace(r.PostFormValue(fmt.Sprintf("scanner_model_%d", i))),
			Bedrock:  r.PostFormValue(fmt.Sprintf("scanner_bedrock_%d", i)) == "true",
			Local:    r.PostFormValue(fmt.Sprintf("scanner_local_%d", i)) == "true",
		}
		if w := strings.TrimSpace(r.PostFormValue(fmt.Sprintf("scanner_weight_%d", i))); w != "" {
			if f, err := strconv.ParseFloat(w, 64); err == nil {
				s.Weight = f
			}
		}
		if v := strings.TrimSpace(r.PostFormValue(fmt.Sprintf("scanner_cost_in_%d", i))); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				s.CostPerMInputUSD = f
			}
		}
		if v := strings.TrimSpace(r.PostFormValue(fmt.Sprintf("scanner_cost_out_%d", i))); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				s.CostPerMOutputUSD = f
			}
		}
		ext.Ensemble.Scanners = append(ext.Ensemble.Scanners, s)
	}
	// Arbiter
	arb := &configstore.ArbiterSpec{
		Provider: strings.TrimSpace(r.PostFormValue("arbiter_provider")),
		Model:    strings.TrimSpace(r.PostFormValue("arbiter_model")),
		Mode:     strings.TrimSpace(r.PostFormValue("arbiter_mode")),
		Bedrock:  r.PostFormValue("arbiter_bedrock") == "true",
		Local:    r.PostFormValue("arbiter_local") == "true",
	}
	if v := strings.TrimSpace(r.PostFormValue("arbiter_cost_in")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			arb.CostPerMInputUSD = f
		}
	}
	if v := strings.TrimSpace(r.PostFormValue("arbiter_cost_out")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			arb.CostPerMOutputUSD = f
		}
	}
	if arb.Provider != "" || arb.Model != "" || arb.Mode != "" || arb.Bedrock || arb.Local {
		ext.Ensemble.Arbiter = arb
	}
	// Dedup
	d := &configstore.DedupConfig{}
	if v := r.PostFormValue("dedup_line_overlap"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			d.LineOverlapMinRatio = f
		}
	}
	if v := r.PostFormValue("dedup_title_sim"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			d.TitleSimilarityMin = f
		}
	}
	if d.LineOverlapMinRatio != 0 || d.TitleSimilarityMin != 0 {
		ext.Ensemble.Dedup = d
	}
	// Output
	o := &configstore.EnsembleOutput{
		ShowProvenance: r.PostFormValue("output_show_provenance") == "true",
	}
	for _, v := range strings.Split(r.PostFormValue("output_default_verdicts"), ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			o.DefaultVerdicts = append(o.DefaultVerdicts, v)
		}
	}
	if o.ShowProvenance || len(o.DefaultVerdicts) > 0 {
		ext.Ensemble.Output = o
	}
	return ext
}

// readRaw is a small helper that loads the file as a top-level map of raw
// messages, mirroring configstore.loadRaw without exporting it.
func readRaw(path string) (map[string]json.RawMessage, error) {
	data, err := readFile(path)
	if err != nil {
		return map[string]json.RawMessage{}, err
	}
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	return raw, nil
}

func readFile(path string) ([]byte, error) {
	f, err := openReadable(path)
	if err != nil {
		if errors.Is(err, errMissing) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var buf []byte
	chunk := make([]byte, 4096)
	for {
		n, err := f.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf, nil
}
