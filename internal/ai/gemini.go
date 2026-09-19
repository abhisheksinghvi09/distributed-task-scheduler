package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
)

const (
	defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	defaultGeminiModel   = "gemini-3.5-flash-lite"
)

// GeminiProvider communicates with Google Generative Language API.
type GeminiProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewGeminiProvider creates a provider pointing to the production Gemini API.
func NewGeminiProvider(apiKey string) *GeminiProvider {
	return NewGeminiProviderWithURL(apiKey, defaultGeminiBaseURL)
}

// NewGeminiProviderWithURL allows injecting a mock URL for hermetic testing.
func NewGeminiProviderWithURL(apiKey, baseURL string) *GeminiProvider {
	return &GeminiProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (p *GeminiProvider) Name() string {
	return "gemini"
}

func (p *GeminiProvider) DefaultModel() string {
	return defaultGeminiModel
}

// Internal request schema for Gemini generateContent
type geminiPart struct {
	Text string `json:"text,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens int64   `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	ResponseMimeType string  `json:"response_mime_type,omitempty"`
	ResponseSchema   any     `json:"response_schema,omitempty"`
}

type geminiGenerateReq struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

type geminiUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
}

type geminiGenerateResp struct {
	Candidates    []geminiCandidate   `json:"candidates"`
	UsageMetadata geminiUsageMetadata `json:"usageMetadata"`
	ModelVersion  string              `json:"modelVersion"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

func (p *GeminiProvider) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if req.Prompt == "" {
		return nil, &task.ErrPermanent{Err: errors.New("gemini: prompt cannot be empty")}
	}

	model := req.Model
	if model == "" {
		model = p.DefaultModel()
	}

	bodyObj := geminiGenerateReq{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: req.Prompt}}},
		},
	}

	if req.System != "" {
		bodyObj.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: req.System}},
		}
	}

	if req.MaxTokens > 0 || req.Temperature != nil || req.ResponseSchema != nil {
		cfg := &geminiGenerationConfig{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
		}
		if req.ResponseSchema != nil {
			cfg.ResponseMimeType = "application/json"
			cfg.ResponseSchema = req.ResponseSchema
		}
		bodyObj.GenerationConfig = cfg
	}

	rawJSON, err := json.Marshal(bodyObj)
	if err != nil {
		return nil, &task.ErrPermanent{Err: fmt.Errorf("gemini: marshal request: %w", err)}
	}

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.baseURL, model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawJSON))
	if err != nil {
		return nil, fmt.Errorf("gemini: build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err // network/context error: retryable
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("gemini: read response body: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, classifyHTTPStatus(httpResp.StatusCode, httpResp.Header, respBytes)
	}

	var parsed geminiGenerateResp
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}

	if parsed.Error != nil {
		return nil, classifyHTTPStatus(parsed.Error.Code, httpResp.Header, []byte(parsed.Error.Message))
	}

	if len(parsed.Candidates) == 0 {
		return nil, &task.ErrPermanent{Err: errors.New("gemini: returned zero candidates")}
	}

	candidate := parsed.Candidates[0]
	if isSafetyRefusal(candidate.FinishReason) {
		return nil, &task.ErrPermanent{
			Err: fmt.Errorf("gemini: safety refusal (%s)", candidate.FinishReason),
		}
	}

	var sb strings.Builder
	for _, part := range candidate.Content.Parts {
		sb.WriteString(part.Text)
	}

	return &GenerateResponse{
		Text: sb.String(),
		Usage: Usage{
			InputTokens:          parsed.UsageMetadata.PromptTokenCount,
			OutputTokens:         parsed.UsageMetadata.CandidatesTokenCount,
			CacheReadInputTokens: parsed.UsageMetadata.CachedContentTokenCount,
		},
		FinishReason: candidate.FinishReason,
		Model:        model,
	}, nil
}

func isSafetyRefusal(finishReason string) bool {
	switch finishReason {
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return true
	default:
		return false
	}
}

func classifyHTTPStatus(statusCode int, header http.Header, body []byte) error {
	msg := string(body)
	baseErr := fmt.Errorf("gemini api error (status %d): %s", statusCode, msg)

	switch {
	case statusCode == http.StatusBadRequest || statusCode == http.StatusNotFound:
		return &task.ErrPermanent{Err: baseErr}
	case statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable:
		if d, ok := extractRetryAfter(header); ok {
			return &task.ErrRetryAfter{After: d, Err: baseErr}
		}
		return baseErr
	default:
		return baseErr
	}
}

func extractRetryAfter(header http.Header) (time.Duration, bool) {
	if header == nil {
		return 0, false
	}
	raw := header.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	sec, err := strconv.Atoi(raw)
	if err != nil || sec <= 0 {
		return 0, false
	}
	return time.Duration(sec) * time.Second, true
}
