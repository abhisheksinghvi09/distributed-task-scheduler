package ai

import "testing"

func TestValidateQueryFilters_AcceptsAllowlistedColumns(t *testing.T) {
	filters := []QueryFilter{{Column: "status", Operator: "eq", Value: "failed"}}
	got, err := ValidateQueryFilters(filters)
	if err != nil {
		t.Fatalf("ValidateQueryFilters() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("ValidateQueryFilters() returned %d filters, want 1", len(got))
	}
}

// TestValidateQueryFilters_RejectsArbitraryColumn is the security-critical
// case: this is the entire defense against a model (possibly influenced by
// injected content elsewhere in the system) naming a column outside the
// allowlist -- e.g. reaching for something SQL-injection-shaped instead of
// a real column. No prompt wording protects against this; only this check
// does.
func TestValidateQueryFilters_RejectsArbitraryColumn(t *testing.T) {
	filters := []QueryFilter{{Column: "id; DROP TABLE tasks; --", Operator: "eq", Value: "x"}}
	if _, err := ValidateQueryFilters(filters); err == nil {
		t.Fatal("ValidateQueryFilters() accepted a non-allowlisted column, want a rejection")
	}
}

func TestValidateQueryFilters_RejectsArbitraryOperator(t *testing.T) {
	filters := []QueryFilter{{Column: "status", Operator: "OR 1=1", Value: "x"}}
	if _, err := ValidateQueryFilters(filters); err == nil {
		t.Fatal("ValidateQueryFilters() accepted a non-allowlisted operator, want a rejection")
	}
}

func TestValidateQueryFilters_EmptyIsFine(t *testing.T) {
	got, err := ValidateQueryFilters(nil)
	if err != nil {
		t.Fatalf("ValidateQueryFilters(nil) error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("ValidateQueryFilters(nil) = %v, want empty", got)
	}
}
