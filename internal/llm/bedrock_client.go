package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BedrockClient implements LLMClient against AWS Bedrock's InvokeModel API
// for Anthropic-shaped models. Model IDs go in the request URI; the body uses
// Bedrock's Anthropic envelope (top-level `anthropic_version`, `messages`,
// `system`, `tools`, etc. — NOT the same as the standard Anthropic Messages
// API shape, which is why we can't just point AnthropicClient at Bedrock).
//
// Auth uses Bedrock API keys (long-lived bearer tokens — Amazon-launched
// feature). For SigV4-signed requests with IAM credentials, use a custom
// provider with a Bedrock proxy in front; this client only handles API keys.
//
// For non-Anthropic Bedrock models (Llama, Mistral, etc.), configure a custom
// provider pointing at a Bedrock-compatible OpenAI proxy instead.
type BedrockClient struct {
	cfg    ClientConfig
	http   *http.Client
	region string
}

// NewBedrockClient builds a Bedrock client from a ResolvedEndpoint produced
// by ResolveBedrock. The endpoint's URL is the regional Bedrock runtime
// origin (e.g. https://bedrock-runtime.us-east-1.amazonaws.com); the Model
// field is the Bedrock model ID, slotted into the per-request path.
func NewBedrockClient(cfg ClientConfig) *BedrockClient {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	region := bedrockRegionFromURL(cfg.URL)
	return &BedrockClient{
		cfg:    cfg,
		http:   &http.Client{Timeout: cfg.Timeout},
		region: region,
	}
}

// CompletionsWithCtx translates ChatRequest → Bedrock InvokeModel and parses
// the response back into ChatResponse, preserving Usage so the per-model
// token grid stays accurate.
func (b *BedrockClient) CompletionsWithCtx(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = b.cfg.Model
	}
	if model == "" {
		return nil, fmt.Errorf("bedrock: model id is required (set via ScannerSpec.Model)")
	}
	body, err := buildBedrockBody(req)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(b.cfg.URL, "/") +
		"/model/" + url.PathEscape(model) + "/invoke"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)

	httpResp, err := b.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock request: %w", err)
	}
	defer httpResp.Body.Close()
	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("bedrock response read: %w", err)
	}
	if httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("bedrock %d: %s", httpResp.StatusCode, truncate(string(respBytes), 500))
	}
	return parseBedrockResponse(respBytes, model)
}

// buildBedrockBody constructs Bedrock's Anthropic-shaped request body. The
// shape is documented at
// https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html
// and the tool-use turn shape at
// https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-tool-use.html
//
// The encoder normalizes OpenAI-style tool turns into Anthropic-on-Bedrock
// content blocks:
//   - assistant messages with ToolCalls become content arrays of
//     [{type:"text"}, {type:"tool_use", id, name, input}]
//   - role="tool" + ToolCallID result messages become user-role content
//     [{type:"tool_result", tool_use_id, content}]
// Without this, multi-round tool-use loops (file_read, code_search,
// code_comment) fail past the first round on Bedrock scanners.
func buildBedrockBody(req ChatRequest) ([]byte, error) {
	type bedrockTool struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		InputSchema map[string]any `json:"input_schema"`
	}
	type bedrockMsg struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	body := map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	} else {
		body["max_tokens"] = 4096
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	var systemParts []string
	msgs := make([]bedrockMsg, 0, len(req.Messages))

	// flushPendingToolResults emits the buffered tool-result blocks as one
	// user message. Bedrock's Anthropic flow requires the user turn that
	// follows a multi-tool_use assistant turn to carry *all* corresponding
	// tool_result blocks in a single content array — emitting one user
	// message per tool result causes Bedrock to reject the request with
	// "missing tool_result" on the next round.
	var pending []map[string]any
	flushPendingToolResults := func() {
		if len(pending) == 0 {
			return
		}
		blocks := pending
		pending = nil
		msgs = append(msgs, bedrockMsg{Role: "user", Content: blocks})
	}

	for _, m := range req.Messages {
		switch {
		case m.Role == "system":
			flushPendingToolResults()
			if s, ok := m.Content.(string); ok && s != "" {
				systemParts = append(systemParts, s)
			}
		case m.Role == "tool" || m.ToolCallID != "":
			// Buffer; flushed when we hit a non-tool message or end-of-list.
			pending = append(pending, map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.ExtractText(),
			})
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			flushPendingToolResults()
			// Assistant tool-use turn: emit a content array carrying any text
			// the model returned alongside the tool_use blocks. Bedrock requires
			// the assistant turn to use blocks (not a string) when tool_use
			// is present.
			blocks := []map[string]any{}
			if text := m.ExtractText(); text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			for _, tc := range m.ToolCalls {
				var input any
				if tc.Function.Arguments == "" {
					input = map[string]any{}
				} else if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
					// Pass the raw string if it isn't valid JSON; Bedrock will
					// surface the schema error rather than us silently dropping it.
					input = tc.Function.Arguments
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": input,
				})
			}
			msgs = append(msgs, bedrockMsg{Role: "assistant", Content: blocks})
		default:
			flushPendingToolResults()
			msgs = append(msgs, bedrockMsg{Role: m.Role, Content: m.Content})
		}
	}
	flushPendingToolResults()
	if len(systemParts) > 0 {
		body["system"] = strings.Join(systemParts, "\n\n")
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		tools := make([]bedrockTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, bedrockTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
		body["tools"] = tools
	}
	return json.Marshal(body)
}

// parseBedrockResponse converts Bedrock's Anthropic-shaped response into the
// shared ChatResponse type. Tool-use blocks are mapped onto ToolCalls so the
// rest of the pipeline (arbiter especially) sees the same shape as for direct
// Anthropic calls.
func parseBedrockResponse(data []byte, model string) (*ChatResponse, error) {
	var raw struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			ID    string          `json:"id,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason,omitempty"`
		Usage      struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("bedrock parse: %w (body: %s)", err, truncate(string(data), 200))
	}

	var textParts []string
	var toolCalls []ToolCall
	for _, c := range raw.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				textParts = append(textParts, c.Text)
			}
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:   c.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      c.Name,
					Arguments: string(c.Input),
				},
			})
		}
	}
	text := strings.Join(textParts, "\n")
	resp := &ChatResponse{
		Model: model,
		Choices: []Choice{{
			Message: ResponseMessage{
				Role:      "assistant",
				Content:   &text,
				ToolCalls: toolCalls,
			},
			FinishReason: raw.StopReason,
		}},
		Usage: &UsageInfo{
			PromptTokens:     raw.Usage.InputTokens,
			CompletionTokens: raw.Usage.OutputTokens,
			TotalTokens:      raw.Usage.InputTokens + raw.Usage.OutputTokens,
			CacheReadTokens:  raw.Usage.CacheReadInputTokens,
			CacheWriteTokens: raw.Usage.CacheCreationInputTokens,
		},
	}
	return resp, nil
}

// bedrockRegionFromURL extracts the region from a bedrock-runtime URL.
// Used for diagnostics / logging only; the URL is the source of truth.
func bedrockRegionFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	const prefix = "bedrock-runtime."
	if !strings.HasPrefix(host, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(host, prefix)
	if i := strings.Index(rest, "."); i > 0 {
		return rest[:i]
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
