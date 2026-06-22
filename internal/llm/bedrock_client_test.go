package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBedrockClientShapesRequestAndParsesResponse(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
            "content": [
                {"type":"text","text":"hi there"},
                {"type":"tool_use","id":"tu_1","name":"arbiter_verdict","input":{"verdicts":[]}}
            ],
            "stop_reason":"end_turn",
            "usage": {"input_tokens": 12, "output_tokens": 5, "cache_read_input_tokens": 3}
        }`))
	}))
	defer srv.Close()

	c := NewBedrockClient(ClientConfig{
		URL:    srv.URL,
		APIKey: "br-test-key",
	})

	temp := 0.0
	resp, err := c.CompletionsWithCtx(context.Background(), ChatRequest{
		Model:    "anthropic.claude-opus-4-1-20251015-v1:0",
		Messages: []Message{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hi"},
		},
		MaxTokens:   2048,
		Temperature: &temp,
	})
	if err != nil {
		t.Fatalf("CompletionsWithCtx: %v", err)
	}

	if gotMethod != "POST" {
		t.Errorf("method=%s, want POST", gotMethod)
	}
	wantPath := "/model/anthropic.claude-opus-4-1-20251015-v1:0/invoke"
	if gotPath != wantPath {
		t.Errorf("path=%s, want %s", gotPath, wantPath)
	}
	if gotAuth != "Bearer br-test-key" {
		t.Errorf("auth=%s, want 'Bearer br-test-key'", gotAuth)
	}

	if gotBody["anthropic_version"] != "bedrock-2023-05-31" {
		t.Errorf("missing anthropic_version, got %v", gotBody["anthropic_version"])
	}
	if gotBody["max_tokens"].(float64) != 2048 {
		t.Errorf("max_tokens=%v, want 2048", gotBody["max_tokens"])
	}
	if gotBody["system"] != "be helpful" {
		t.Errorf("system=%v, want 'be helpful'", gotBody["system"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("messages len=%d, want 1 (system should be top-level, not a message)", len(msgs))
	}

	if resp.Content() != "hi there" {
		t.Errorf("content=%q, want 'hi there'", resp.Content())
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Function.Name != "arbiter_verdict" {
		t.Errorf("tool calls wrong: %+v", calls)
	}
	if !strings.Contains(calls[0].Function.Arguments, "verdicts") {
		t.Errorf("tool args missing 'verdicts': %s", calls[0].Function.Arguments)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 5 || resp.Usage.CacheReadTokens != 3 {
		t.Errorf("usage=%+v", resp.Usage)
	}
}

func TestBedrockClientRequiresModel(t *testing.T) {
	c := NewBedrockClient(ClientConfig{URL: "https://bedrock-runtime.us-east-1.amazonaws.com", APIKey: "x"})
	_, err := c.CompletionsWithCtx(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for missing model id")
	}
	if !strings.Contains(err.Error(), "model id is required") {
		t.Errorf("err=%v", err)
	}
}

func TestBedrockClientSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"throttled"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := NewBedrockClient(ClientConfig{URL: srv.URL, APIKey: "x"})
	_, err := c.CompletionsWithCtx(context.Background(), ChatRequest{
		Model:    "anthropic.claude-opus-4-1-v1:0",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "throttled") {
		t.Errorf("err=%v", err)
	}
}

func TestBedrockClientEncodesToolTurnsAsContentBlocks(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	c := NewBedrockClient(ClientConfig{URL: srv.URL, APIKey: "x"})
	_, err := c.CompletionsWithCtx(context.Background(), ChatRequest{
		Model: "anthropic.claude-opus-4-1-v1:0",
		Messages: []Message{
			{Role: "user", Content: "look at file foo"},
			// Assistant turn carrying a tool_use call (OpenAI/upstream shape).
			{
				Role:    "assistant",
				Content: "I'll read it.",
				ToolCalls: []ToolCall{{
					ID:   "tu_1",
					Type: "function",
					Function: FunctionCall{
						Name:      "file_read",
						Arguments: `{"file_path":"foo.go"}`,
					},
				}},
			},
			// Tool-result message (OpenAI shape: role=tool + ToolCallID + content).
			NewToolResultMessage("tu_1", "package foo\nfunc Bar() {}"),
			{Role: "user", Content: "now what?"},
		},
	})
	if err != nil {
		t.Fatalf("CompletionsWithCtx: %v", err)
	}

	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages len=%d, want 4", len(msgs))
	}

	// msgs[1] should be the assistant tool_use turn — content must be a
	// block array, NOT a plain string, and must contain a tool_use block.
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("msgs[1].role=%v, want assistant", asst["role"])
	}
	asstBlocks, ok := asst["content"].([]any)
	if !ok {
		t.Fatalf("assistant tool-use turn content is not a block array: %T %v", asst["content"], asst["content"])
	}
	var foundToolUse bool
	for _, b := range asstBlocks {
		bm := b.(map[string]any)
		if bm["type"] == "tool_use" {
			foundToolUse = true
			if bm["id"] != "tu_1" || bm["name"] != "file_read" {
				t.Errorf("tool_use block fields wrong: %+v", bm)
			}
			input := bm["input"].(map[string]any)
			if input["file_path"] != "foo.go" {
				t.Errorf("tool_use input not deserialized: %+v", input)
			}
		}
	}
	if !foundToolUse {
		t.Errorf("no tool_use block emitted; assistant blocks: %+v", asstBlocks)
	}

	// msgs[2] should be the tool-result, converted to user-role with a
	// tool_result content block. It must NOT have role=tool.
	tr := msgs[2].(map[string]any)
	if tr["role"] != "user" {
		t.Errorf("tool-result turn role=%v, want user", tr["role"])
	}
	trBlocks, ok := tr["content"].([]any)
	if !ok {
		t.Fatalf("tool-result content is not a block array: %T", tr["content"])
	}
	trBlock := trBlocks[0].(map[string]any)
	if trBlock["type"] != "tool_result" || trBlock["tool_use_id"] != "tu_1" {
		t.Errorf("tool_result block fields wrong: %+v", trBlock)
	}
	if trBlock["content"] != "package foo\nfunc Bar() {}" {
		t.Errorf("tool_result content lost: %v", trBlock["content"])
	}
}

func TestBedrockRegionFromURL(t *testing.T) {
	cases := map[string]string{
		"https://bedrock-runtime.us-east-1.amazonaws.com":     "us-east-1",
		"https://bedrock-runtime.eu-west-2.amazonaws.com/":    "eu-west-2",
		"https://other.example.com":                            "",
		"":                                                     "",
	}
	for in, want := range cases {
		if got := bedrockRegionFromURL(in); got != want {
			t.Errorf("bedrockRegionFromURL(%q)=%q, want %q", in, got, want)
		}
	}
}
