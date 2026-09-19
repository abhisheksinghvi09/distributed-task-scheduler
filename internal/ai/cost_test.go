package ai

import (
	"testing"
	"time"
)

func TestCostMicrocents_ClaudeExactIntegerMath(t *testing.T) {
	usage := Usage{
		InputTokens:          1_000_000,
		OutputTokens:         1_000_000,
		CacheReadInputTokens: 1_000_000,
	}
	got, err := CostMicrocents("claude-opus-5", usage)
	if err != nil {
		t.Fatalf("CostMicrocents: %v", err)
	}

	// $5 + $25 + $0.50 (10% cache-read discount) = $30.50 = 3,050,000,000 microcents.
	want := int64(500_000_000 + 2_500_000_000 + 50_000_000)
	if got != want {
		t.Fatalf("CostMicrocents() = %d, want %d", got, want)
	}
}

func TestCostMicrocents_GeminiExactIntegerMath(t *testing.T) {
	usage := Usage{
		InputTokens:          100_000,
		OutputTokens:         50_000,
		CacheReadInputTokens: 20_000,
	}
	got, err := CostMicrocents("gemini-3.5-flash-lite", usage)
	if err != nil {
		t.Fatalf("CostMicrocents(gemini-3.5-flash-lite): %v", err)
	}

	// 100k * 8 + 50k * 30 + 20k * 2 = 800k + 1.5M + 40k = 2,340,000 microcents
	want := int64(100_000*8 + 50_000*30 + 20_000*2)
	if got != want {
		t.Fatalf("CostMicrocents() = %d, want %d", got, want)
	}
}

func TestCostMicrocents_SmallUsageNoRounding(t *testing.T) {
	usage := Usage{InputTokens: 3, OutputTokens: 7}
	got, err := CostMicrocents("claude-opus-5", usage)
	if err != nil {
		t.Fatalf("CostMicrocents: %v", err)
	}
	want := int64(3*500 + 7*2500)
	if got != want {
		t.Fatalf("CostMicrocents() = %d, want %d (exact integer math, no float drift)", got, want)
	}
}

func TestCostMicrocents_UnknownModelErrors(t *testing.T) {
	if _, err := CostMicrocents("not-a-real-model", Usage{}); err == nil {
		t.Fatal("CostMicrocents() with an unknown model returned nil error, want an error (never silently charge zero)")
	}
}

func TestFirstOfMonth(t *testing.T) {
	got := firstOfMonth(time.Date(2026, 3, 17, 14, 30, 0, 0, time.UTC))
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("firstOfMonth() = %v, want %v", got, want)
	}
}
