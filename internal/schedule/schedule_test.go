package schedule

import (
	"errors"
	"testing"
	"time"
)

func TestValidateCronExpr_Valid(t *testing.T) {
	cases := []string{
		"* * * * *",
		"0 9 * * 1-5",
		"@daily",
		"@hourly",
		"*/5 * * * *",
	}
	for _, expr := range cases {
		if _, err := ValidateCronExpr(expr); err != nil {
			t.Errorf("ValidateCronExpr(%q) error = %v, want nil", expr, err)
		}
	}
}

func TestValidateCronExpr_Invalid(t *testing.T) {
	cases := []string{
		"",
		"not a cron expression",
		"60 * * * *", // minute out of range
		"* * * *",    // too few fields
	}
	for _, expr := range cases {
		if _, err := ValidateCronExpr(expr); !errors.Is(err, ErrInvalidCronExpr) {
			t.Errorf("ValidateCronExpr(%q) error = %v, want ErrInvalidCronExpr", expr, err)
		}
	}
}

func TestValidateTimezone_Valid(t *testing.T) {
	cases := []string{"", "UTC", "America/New_York", "Asia/Kolkata", "Europe/London"}
	for _, tz := range cases {
		if _, err := ValidateTimezone(tz); err != nil {
			t.Errorf("ValidateTimezone(%q) error = %v, want nil", tz, err)
		}
	}
}

func TestValidateTimezone_Invalid(t *testing.T) {
	cases := []string{"Not/A_Timezone", "PST", "GMT+5"}
	for _, tz := range cases {
		if _, err := ValidateTimezone(tz); !errors.Is(err, ErrInvalidTimezone) {
			t.Errorf("ValidateTimezone(%q) error = %v, want ErrInvalidTimezone", tz, err)
		}
	}
}

func TestValidateCronExpr_NextIsMonotonic(t *testing.T) {
	sched, err := ValidateCronExpr("*/15 * * * *")
	if err != nil {
		t.Fatalf("ValidateCronExpr: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next := sched.Next(base)
	if !next.After(base) {
		t.Fatalf("Next(%v) = %v, want a time after base", base, next)
	}

	next2 := sched.Next(next)
	if !next2.After(next) {
		t.Fatalf("Next(%v) = %v, want strictly increasing occurrences", next, next2)
	}
}
