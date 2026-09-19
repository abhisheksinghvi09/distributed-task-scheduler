package ai

import (
	"context"
)

// Usage tracks token consumption for cost attribution and metrics.
type Usage struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
}

// GenerateRequest defines the input to any LLM provider.
type GenerateRequest struct {
	Prompt         string
	System         string
	Model          string
	MaxTokens      int64
	Temperature    *float64
	ResponseSchema any // JSON schema if structured output is requested
}

// GenerateResponse defines the normalized output from an LLM provider.
type GenerateResponse struct {
	Text         string
	Usage        Usage
	FinishReason string
	Model        string
}

// LLMProvider encapsulates communication with a specific LLM vendor API.
type LLMProvider interface {
	Name() string
	DefaultModel() string
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)
}
