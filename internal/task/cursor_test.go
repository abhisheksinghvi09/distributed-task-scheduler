package task

import (
	"testing"
	"time"
)

func TestCursor_RoundTrips(t *testing.T) {
	want := time.Date(2026, 3, 4, 5, 6, 7, 8000, time.UTC)
	id := "8b2ff14c-b511-4722-ac66-d28b7c6ee9e2"

	encoded := encodeCursor(want, id)
	gotTime, gotID, err := decodeCursor(encoded)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if gotID != id {
		t.Errorf("id = %q, want %q", gotID, id)
	}
	parsed, err := time.Parse(time.RFC3339Nano, gotTime)
	if err != nil {
		t.Fatalf("parse decoded time: %v", err)
	}
	if !parsed.Equal(want) {
		t.Errorf("time = %v, want %v", parsed, want)
	}
}

func TestCursor_MalformedRejected(t *testing.T) {
	if _, _, err := decodeCursor("no-separator-here"); err == nil {
		t.Fatal("decodeCursor() with no '|' returned nil error, want an error")
	}
}
