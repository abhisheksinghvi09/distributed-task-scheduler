package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicProvider communicates with Anthropic Claude API.
type AnthropicProvider struct {
	client anthropic.Client
}

// NewAnthropicProvider initializes an AnthropicProvider with the given API key.
// SDK retries are disabled (WithMaxRetries(0)) so the scheduler owns durability.
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	return &AnthropicProvider{
		client: anthropic.NewClient(opts...),
	}
}

// NewAnthropicProviderWithClient wraps an existing Anthropic client.
func NewAnthropicProviderWithClient(client anthropic.Client) *AnthropicProvider {
	return &AnthropicProvider{client: client}
}

func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

func (p *AnthropicProvider) DefaultModel() string {
	return "claude-opus-5"
}

func (p *AnthropicProvider) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if req.Prompt == "" {
		return nil, &task.ErrPermanent{Err: errors.New("anthropic: prompt cannot be empty")}
	}

	model := req.Model
	if model == "" {
		model = p.DefaultModel()
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt)),
		},
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	stream := p.client.Messages.NewStreaming(ctx, params)
	message := anthropic.Message{}
	for stream.Next() {
		message.Accumulate(stream.Current())
	}
	if err := stream.Err(); err != nil {
		return nil, classifyAPIError(err)
	}

	if message.StopReason == anthropic.StopReasonRefusal {
		return nil, &task.ErrPermanent{
			Err: fmt.Errorf("anthropic: safety refusal (%s)", message.StopDetails.Category),
		}
	}

	var sb strings.Builder
	for _, block := range message.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}

	return &GenerateResponse{
		Text: sb.String(),
		Usage: Usage{
			InputTokens:          message.Usage.InputTokens,
			OutputTokens:         message.Usage.OutputTokens,
			CacheReadInputTokens: message.Usage.CacheReadInputTokens,
		},
		FinishReason: string(message.StopReason),
		Model:        model,
	}, nil
}
