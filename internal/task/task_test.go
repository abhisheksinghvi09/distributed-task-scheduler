package task

import (
	"testing"
	"time"
)

func TestBackoff_MonotonicAndCapped(t *testing.T) {
	prevMax := time.Duration(0)
	for attempt := int32(1); attempt <= 10; attempt++ {
		expected := backoffBase * time.Duration(1<<uint(attempt-1))
		if expected > backoffCap {
			expected = backoffCap
		}

		lo := time.Duration(float64(expected) * 0.5)
		hi := time.Duration(float64(expected) * 1.5)

		for i := 0; i < 20; i++ {
			d := Backoff(attempt)
			if d < lo || d >= hi {
				t.Fatalf("attempt %d: Backoff() = %v, want in [%v, %v)", attempt, d, lo, hi)
			}
		}

		if expected < prevMax {
			t.Fatalf("attempt %d: expected backoff %v is less than previous %v", attempt, expected, prevMax)
		}
		prevMax = expected
	}
}

func TestBackoff_ClampsLowAttempt(t *testing.T) {
	d0 := Backoff(0)
	d1 := Backoff(1)
	// Both should fall in attempt-1's range: Backoff treats <1 as 1.
	lo := time.Duration(float64(backoffBase) * 0.5)
	hi := time.Duration(float64(backoffBase) * 1.5)
	if d0 < lo || d0 >= hi {
		t.Fatalf("Backoff(0) = %v, want in [%v, %v)", d0, lo, hi)
	}
	if d1 < lo || d1 >= hi {
		t.Fatalf("Backoff(1) = %v, want in [%v, %v)", d1, lo, hi)
	}
}
