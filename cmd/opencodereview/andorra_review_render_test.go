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
