package ensemble

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-code-review/open-code-review/internal/configstore"
	"github.com/open-code-review/open-code-review/internal/finding"
	"github.com/open-code-review/open-code-review/internal/llm"
	"github.com/open-code-review/open-code-review/internal/model"
)

func mkScanner(name, provider, llmModel string, enabled *bool) ScannerEndpoint {
	return ScannerEndpoint{
		Spec: configstore.ScannerSpec{
			Name:     name,
			Provider: provider,
			Model:    llmModel,
			Enabled:  enabled,
		},
		Endpoint: llm.ResolvedEndpoint{Model: llmModel, Protocol: "anthropic"},
	}
}

func TestExecuteRequiresCallback(t *testing.T) {
	o := &Orchestrator{Scanners: []ScannerEndpoint{mkScanner("a", "p", "m", nil)}}
	if _, err := o.Execute(context.Background()); err == nil {
		t.Error("expected error for missing Run callback")
	}
}

func TestExecuteNoScanners(t *testing.T) {
	o := &Orchestrator{
		Run: func(ctx context.Context, sep ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, error) {
			return nil, finding.TokenUsage{}, nil
		},
	}
	if _, err := o.Execute(context.Background()); err == nil {
		t.Error("expected error for empty scanner list")
	}
}

func TestExecuteAllSucceed(t *testing.T) {
	o := &Orchestrator{
		Scanners: []ScannerEndpoint{
			mkScanner("opus", "anthropic", "claude-opus-4-7", nil),
			mkScanner("gpt", "openai", "gpt-5.5", nil),
		},
		Run: func(ctx context.Context, sep ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, error) {
			return []model.LlmComment{
				{Path: "x.go", Content: sep.Spec.Name + " finding"},
			}, finding.TokenUsage{InputTokens: 10, OutputTokens: 2}, nil
		},
	}
	res, err := o.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Raw) != 2 {
		t.Errorf("Raw len=%d, want 2", len(res.Raw))
	}
	for _, sr := range res.Scanners {
		if sr.Status != "ok" {
			t.Errorf("scanner %s status=%s, want ok", sr.Name, sr.Status)
		}
		if sr.Findings != 1 {
			t.Errorf("scanner %s findings=%d, want 1", sr.Name, sr.Findings)
		}
		if sr.Tokens.InputTokens != 10 || sr.Tokens.OutputTokens != 2 {
			t.Errorf("scanner %s tokens=%+v, want input=10 output=2", sr.Name, sr.Tokens)
		}
	}
}

func TestExecuteTagsProvenance(t *testing.T) {
	o := &Orchestrator{
		Scanners: []ScannerEndpoint{
			mkScanner("opus", "anthropic", "claude-opus-4-7", nil),
		},
		Run: func(ctx context.Context, sep ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, error) {
			return []model.LlmComment{{Path: "x.go", Content: "bug"}}, finding.TokenUsage{}, nil
		},
	}
	res, _ := o.Execute(context.Background())
	if len(res.Raw) != 1 {
		t.Fatalf("Raw len=%d", len(res.Raw))
	}
	s := res.Raw[0].Source
	if s.Scanner != "opus" || s.Provider != "anthropic" || s.Model != "claude-opus-4-7" {
		t.Errorf("source=%+v", s)
	}
}

func TestExecutePartialFailureContinues(t *testing.T) {
	o := &Orchestrator{
		Scanners: []ScannerEndpoint{
			mkScanner("ok-one", "anthropic", "m1", nil),
			mkScanner("err-one", "openai", "m2", nil),
		},
		Run: func(ctx context.Context, sep ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, error) {
			if sep.Spec.Name == "err-one" {
				return nil, finding.TokenUsage{}, errors.New("rate limited")
			}
			return []model.LlmComment{{Path: "x.go", Content: "found"}}, finding.TokenUsage{}, nil
		},
	}
	res, err := o.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v (one scanner succeeded, should be nil)", err)
	}
	if len(res.Raw) != 1 {
		t.Errorf("Raw len=%d, want 1", len(res.Raw))
	}
	var okCount, errCount int
	for _, sr := range res.Scanners {
		switch sr.Status {
		case "ok":
			okCount++
		case "error":
			errCount++
		}
	}
	if okCount != 1 || errCount != 1 {
		t.Errorf("status counts: ok=%d err=%d, want 1/1", okCount, errCount)
	}
}

func TestExecutePartialStatusWhenErrButHasFindings(t *testing.T) {
	o := &Orchestrator{
		Scanners: []ScannerEndpoint{mkScanner("s", "p", "m", nil)},
		Run: func(ctx context.Context, sep ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, error) {
			return []model.LlmComment{{Path: "x.go", Content: "got something"}}, finding.TokenUsage{InputTokens: 5, OutputTokens: 1}, errors.New("timeout on last file")
		},
	}
	res, err := o.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Scanners[0].Status != "partial" {
		t.Errorf("status=%s, want partial", res.Scanners[0].Status)
	}
	if res.Scanners[0].Err == "" {
		t.Errorf("expected error message to be recorded")
	}
}

func TestExecuteAllFailFatal(t *testing.T) {
	o := &Orchestrator{
		Scanners: []ScannerEndpoint{
			mkScanner("a", "p1", "m1", nil),
			mkScanner("b", "p2", "m2", nil),
		},
		Run: func(ctx context.Context, sep ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, error) {
			return nil, finding.TokenUsage{}, errors.New("dead")
		},
	}
	if _, err := o.Execute(context.Background()); err == nil {
		t.Error("expected error when all scanners fail with no findings")
	}
}

func TestExecuteSkipsDisabledScanners(t *testing.T) {
	off := false
	o := &Orchestrator{
		Scanners: []ScannerEndpoint{
			mkScanner("on1", "p", "m1", nil),
			mkScanner("off", "p", "m2", &off),
			mkScanner("on2", "p", "m3", nil),
		},
		Run: func(ctx context.Context, sep ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, error) {
			if sep.Spec.Name == "off" {
				t.Errorf("disabled scanner was invoked")
			}
			return []model.LlmComment{{Path: "x.go", Content: "ok"}}, finding.TokenUsage{}, nil
		},
	}
	res, _ := o.Execute(context.Background())
	if len(res.Scanners) != 2 {
		t.Errorf("expected 2 scanner results, got %d", len(res.Scanners))
	}
}

func TestExecuteConcurrencyBound(t *testing.T) {
	var inflight, peak int32
	o := &Orchestrator{
		Scanners: []ScannerEndpoint{
			mkScanner("a", "p", "m", nil),
			mkScanner("b", "p", "m", nil),
			mkScanner("c", "p", "m", nil),
			mkScanner("d", "p", "m", nil),
		},
		MaxConcurrency: 2,
		Run: func(ctx context.Context, sep ScannerEndpoint) ([]model.LlmComment, finding.TokenUsage, error) {
			n := atomic.AddInt32(&inflight, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&inflight, -1)
			return []model.LlmComment{{Path: "x.go", Content: "ok"}}, finding.TokenUsage{}, nil
		},
	}
	if _, err := o.Execute(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if peak > 2 {
		t.Errorf("peak inflight=%d, want <= 2", peak)
	}
}
