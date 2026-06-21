package llm

import (
	"fmt"
	"os"
	"strings"
)

// Environment variables consumed when a scanner or arbiter has Bedrock=true.
// Values come from the workflow env block (typically populated from GitHub
// repo/org secrets) — never from the committed config file.
const (
	EnvBedrockAPIKey = "OCR_BEDROCK_API_KEY"
	EnvBedrockRegion = "OCR_BEDROCK_REGION"
)

// ResolveBedrock builds a ResolvedEndpoint for an AWS Bedrock model. Bedrock
// supports Amazon-issued API keys (long-lived bearer tokens), so we treat it
// like an Anthropic-protocol endpoint pointed at the Bedrock runtime URL for
// the configured region.
//
// modelID is used verbatim (e.g. "anthropic.claude-opus-4-1-20251015-v1:0").
// region defaults to OCR_BEDROCK_REGION env, then "us-east-1".
// API key comes from OCR_BEDROCK_API_KEY env. Missing key or unset placeholder
// produces a clear error.
func ResolveBedrock(scannerName, modelID string) (ResolvedEndpoint, error) {
	apiKey := strings.TrimSpace(os.Getenv(EnvBedrockAPIKey))
	if apiKey == "" {
		return ResolvedEndpoint{}, fmt.Errorf("bedrock scanner %q: %s env var is not set", scannerName, EnvBedrockAPIKey)
	}
	region := strings.TrimSpace(os.Getenv(EnvBedrockRegion))
	if region == "" {
		region = "us-east-1"
	}
	url := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", region)
	return ResolvedEndpoint{
		URL:        url,
		Token:      apiKey,
		Model:      modelID,
		Protocol:   "anthropic",
		AuthHeader: "authorization",
		Source:     "bedrock:" + region,
	}, nil
}
