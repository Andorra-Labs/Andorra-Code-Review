package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/open-code-review/open-code-review/internal/ensemble"
	"github.com/open-code-review/open-code-review/internal/finding"
)

// TestBuildEnsembleJSON_NilSlicesMarshalAsArrays guards the JSON envelope
// against emitting null for scanners/groups/comments. The GitHub Actions
// summary script reads e.g. data.ensemble.groups.length directly, so a null
// (from a nil Go slice when the arbiter is skipped or yields no findings)
// would throw "Cannot read properties of null (reading 'length')".
func TestBuildEnsembleJSON_NilSlicesMarshalAsArrays(t *testing.T) {
	env := buildEnsembleJSON(nil, ensemble.Result{}, nil, finding.TokenUsage{}, nil, time.Second)

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		Comments []json.RawMessage `json:"comments"`
		Ensemble *struct {
			Scanners     []json.RawMessage `json:"scanners"`
			Groups       []json.RawMessage `json:"groups"`
			TokenSummary []json.RawMessage `json:"token_summary"`
		} `json:"ensemble"`
	}
	// json.Unmarshal of a JSON null into a slice leaves it nil; a JSON []
	// leaves it non-nil with len 0. We assert the latter for every field.
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v\njson: %s", err, out)
	}

	if decoded.Comments == nil {
		t.Errorf("comments serialized as null, want []\njson: %s", out)
	}
	if decoded.Ensemble == nil {
		t.Fatalf("ensemble missing\njson: %s", out)
	}
	if decoded.Ensemble.Scanners == nil {
		t.Errorf("ensemble.scanners serialized as null, want []\njson: %s", out)
	}
	if decoded.Ensemble.Groups == nil {
		t.Errorf("ensemble.groups serialized as null, want []\njson: %s", out)
	}
	if decoded.Ensemble.TokenSummary == nil {
		t.Errorf("ensemble.token_summary serialized as null, want []\njson: %s", out)
	}
}

// TestBuildEnsembleJSON_IncludesScannerRawOutput guards that each scanner's
// complete raw output is carried into the JSON envelope under scanners[].raw,
// so the GitHub Actions summary can render a per-scanner "full output" dropdown
// even when the arbiter fails and the accepted-only view is empty.
func TestBuildEnsembleJSON_IncludesScannerRawOutput(t *testing.T) {
	res := ensemble.Result{
		Scanners: []ensemble.ScannerResult{
			{
				Name:     "spark",
				Status:   "partial",
				Findings: 1,
				Raw: []finding.RawFinding{
					{Path: "main.go", StartLine: 10, EndLine: 12, Title: "nil map write", Detail: "writes to a nil map"},
				},
			},
		},
	}

	env := buildEnsembleJSON(nil, res, nil, finding.TokenUsage{}, nil, time.Second)
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		Ensemble struct {
			Scanners []struct {
				Raw []struct {
					Path  string `json:"path"`
					Title string `json:"title"`
				} `json:"raw"`
			} `json:"scanners"`
		} `json:"ensemble"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v\njson: %s", err, out)
	}
	if len(decoded.Ensemble.Scanners) != 1 {
		t.Fatalf("got %d scanners, want 1\njson: %s", len(decoded.Ensemble.Scanners), out)
	}
	if len(decoded.Ensemble.Scanners[0].Raw) != 1 {
		t.Fatalf("scanner raw output missing from envelope\njson: %s", out)
	}
	if got := decoded.Ensemble.Scanners[0].Raw[0].Title; got != "nil map write" {
		t.Errorf("raw finding title = %q, want %q", got, "nil map write")
	}
}

// TestBuildTokenRows_UsesResolvedModels guards against the token grid showing a
// raw "${env:...}" placeholder instead of the model actually used.
func TestBuildTokenRows_UsesResolvedModels(t *testing.T) {
	res := ensemble.Result{
		Scanners: []ensemble.ScannerResult{
			{Name: "spark", Model: "Qwen3.6-35B-A3B-NVFP4", Tokens: finding.TokenUsage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	rows := buildTokenRows(res, nil, "Qwen3.6-35B-A3B-NVFP4", finding.TokenUsage{})
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (scanner + arbiter)", len(rows))
	}
	if rows[0].Model != "Qwen3.6-35B-A3B-NVFP4" {
		t.Errorf("scanner row Model = %q, want resolved model", rows[0].Model)
	}
	if rows[len(rows)-1].Model != "Qwen3.6-35B-A3B-NVFP4" {
		t.Errorf("arbiter row Model = %q, want resolved model", rows[len(rows)-1].Model)
	}
}
