package arbiter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/finding"
	"github.com/open-code-review/open-code-review/internal/llm"
)

func configstoreArbiter(provider, model, mode string, temp *float64, maxTokens int) configstore.ArbiterSpec {
	return configstore.ArbiterSpec{
		Provider:    provider,
		Model:       model,
		Mode:        mode,
		Temperature: temp,
		MaxTokens:   maxTokens,
	}
}

type fakeClient struct {
	calls int
	resp  *llm.ChatResponse
	err   error
}

func (f *fakeClient) CompletionsWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.calls++
	return f.resp, f.err
}

func mkResp(verdicts []rawVerdict) *llm.ChatResponse {
	args := struct {
		Verdicts []rawVerdict `json:"verdicts"`
	}{Verdicts: verdicts}
	b, _ := json.Marshal(args)
	return &llm.ChatResponse{
		Choices: []llm.Choice{{
			Message: llm.ResponseMessage{
				ToolCalls: []llm.ToolCall{{
					ID:   "tc1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "arbiter_verdict",
						Arguments: string(b),
					},
				}},
			},
		}},
	}
}

func mkGroup(gid, path, title string, sl, el int) finding.Finding {
	return finding.Finding{
		GroupID:   gid,
		Path:      path,
		StartLine: sl,
		EndLine:   el,
		Title:     title,
		Members: []finding.RawFinding{{
			Path: path, StartLine: sl, EndLine: el, Title: title, Detail: title,
			Source: finding.Source{Scanner: "opus"},
		}},
		Sources: []finding.Source{{Scanner: "opus"}},
	}
}

func TestDecideSuccessfulPerFile(t *testing.T) {
	groups := []finding.Finding{
		mkGroup("g-0", "a.go", "real bug", 1, 1),
		mkGroup("g-1", "a.go", "fake bug", 5, 5),
	}
	client := &fakeClient{
		resp: mkResp([]rawVerdict{
			{GroupID: "g-0", Verdict: "accepted_bug", Confidence: 0.9},
			{GroupID: "g-1", Verdict: "rejected_fp", Reason: "constant condition", Confidence: 0.8},
		}),
	}
	out := Decide(context.Background(), client, Config{Model: "claude-opus-4-8", Mode: "per_file"}, groups, nil)
	if client.calls != 1 {
		t.Errorf("calls=%d, want 1 for per_file with single path", client.calls)
	}
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0].Verdict != finding.VerdictAccepted || out[1].Verdict != finding.VerdictRejected {
		t.Errorf("verdicts wrong: %+v %+v", out[0].Verdict, out[1].Verdict)
	}
	if out[0].Confidence != 0.9 {
		t.Errorf("conf[0]=%f", out[0].Confidence)
	}
	if out[1].VerdictReason != "constant condition" {
		t.Errorf("reason[1]=%q", out[1].VerdictReason)
	}
	if out[0].ArbiterModel != "claude-opus-4-8" {
		t.Errorf("model=%q", out[0].ArbiterModel)
	}
}

func TestDecidePerFileBatchesByPath(t *testing.T) {
	groups := []finding.Finding{
		mkGroup("g-0", "a.go", "x", 1, 1),
		mkGroup("g-1", "a.go", "y", 5, 5),
		mkGroup("g-2", "b.go", "z", 10, 10),
	}
	client := &fakeClient{
		resp: mkResp([]rawVerdict{
			{GroupID: "g-0", Verdict: "accepted_bug"},
			{GroupID: "g-1", Verdict: "rejected_fp"},
			{GroupID: "g-2", Verdict: "accepted_bug"},
		}),
	}
	Decide(context.Background(), client, Config{Mode: "per_file"}, groups, nil)
	if client.calls != 2 {
		t.Errorf("calls=%d, want 2 (one per path)", client.calls)
	}
}

func TestDecidePerGroupOneCallEach(t *testing.T) {
	groups := []finding.Finding{
		mkGroup("g-0", "a.go", "x", 1, 1),
		mkGroup("g-1", "a.go", "y", 5, 5),
		mkGroup("g-2", "b.go", "z", 10, 10),
	}
	client := &fakeClient{
		resp: mkResp([]rawVerdict{{GroupID: "g-0", Verdict: "accepted_bug"}}),
	}
	Decide(context.Background(), client, Config{Mode: "per_group"}, groups, nil)
	if client.calls != 3 {
		t.Errorf("calls=%d, want 3", client.calls)
	}
}

func TestDecideMissingVerdictsBecomeUncertain(t *testing.T) {
	groups := []finding.Finding{
		mkGroup("g-0", "a.go", "x", 1, 1),
		mkGroup("g-1", "a.go", "y", 5, 5),
	}
	client := &fakeClient{
		resp: mkResp([]rawVerdict{
			{GroupID: "g-0", Verdict: "accepted_bug"},
			// g-1 omitted
		}),
	}
	out := Decide(context.Background(), client, Config{Mode: "per_file"}, groups, nil)
	if out[1].Verdict != finding.VerdictUncertain {
		t.Errorf("g-1 verdict=%s", out[1].Verdict)
	}
	if out[1].VerdictReason != "arbiter omitted verdict" {
		t.Errorf("g-1 reason=%q", out[1].VerdictReason)
	}
	if out[1].Confidence != 0.5 {
		t.Errorf("g-1 default confidence not applied: %f", out[1].Confidence)
	}
}

func TestDecideLLMErrorAllUncertain(t *testing.T) {
	groups := []finding.Finding{
		mkGroup("g-0", "a.go", "x", 1, 1),
		mkGroup("g-1", "b.go", "y", 1, 1),
	}
	client := &fakeClient{err: errors.New("rate limited")}
	out := Decide(context.Background(), client, Config{Mode: "per_file"}, groups, nil)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	for _, f := range out {
		if f.Verdict != finding.VerdictUncertain {
			t.Errorf("verdict=%s, want uncertain", f.Verdict)
		}
		if f.VerdictReason != "arbiter unavailable" {
			t.Errorf("reason=%q", f.VerdictReason)
		}
	}
}

func TestDecideMalformedToolCallAllUncertain(t *testing.T) {
	groups := []finding.Finding{mkGroup("g-0", "a.go", "x", 1, 1)}
	client := &fakeClient{
		resp: &llm.ChatResponse{
			Choices: []llm.Choice{{
				Message: llm.ResponseMessage{
					ToolCalls: []llm.ToolCall{{
						Function: llm.FunctionCall{Name: "arbiter_verdict", Arguments: "{not valid json"},
					}},
				},
			}},
		},
	}
	out := Decide(context.Background(), client, Config{Mode: "per_file"}, groups, nil)
	if out[0].Verdict != finding.VerdictUncertain {
		t.Errorf("verdict=%s, want uncertain", out[0].Verdict)
	}
}

func TestDecideEmptyGroupsNoCall(t *testing.T) {
	client := &fakeClient{}
	out := Decide(context.Background(), client, Config{}, nil, nil)
	if out != nil {
		t.Errorf("expected nil output, got %+v", out)
	}
	if client.calls != 0 {
		t.Errorf("calls=%d, want 0", client.calls)
	}
}

func TestDecideClampsConfidence(t *testing.T) {
	groups := []finding.Finding{mkGroup("g-0", "a.go", "x", 1, 1)}
	client := &fakeClient{
		resp: mkResp([]rawVerdict{{GroupID: "g-0", Verdict: "accepted_bug", Confidence: 2.5}}),
	}
	out := Decide(context.Background(), client, Config{}, groups, nil)
	if out[0].Confidence != 1.0 {
		t.Errorf("confidence=%f, want 1.0", out[0].Confidence)
	}
}

func TestFromConfigStoreDefaults(t *testing.T) {
	temp := 0.3
	spec := configstoreArbiter("anthropic", "claude-opus-4-8", "per_group", &temp, 1024)
	cfg := FromConfigStore(spec, "claude-opus-4-8")
	if cfg.Model != "claude-opus-4-8" || cfg.Mode != "per_group" || cfg.MaxTokens != 1024 || cfg.Temperature != 0.3 {
		t.Errorf("cfg=%+v", cfg)
	}
}

func TestFromConfigStoreFillsDefaults(t *testing.T) {
	spec := configstoreArbiter("anthropic", "m", "", nil, 0)
	cfg := FromConfigStore(spec, "resolved-model")
	if cfg.Mode != "per_file" {
		t.Errorf("default mode=%q", cfg.Mode)
	}
	if cfg.MaxTokens != 2048 {
		t.Errorf("default max_tokens=%d", cfg.MaxTokens)
	}
	if cfg.Model != "resolved-model" {
		t.Errorf("model not from arg: %q", cfg.Model)
	}
}
