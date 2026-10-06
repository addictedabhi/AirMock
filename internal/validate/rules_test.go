package validate

import (
	"errors"
	"testing"
)

func extractorFrom(values map[string]any) Extractor {
	return func(field string) (any, bool, error) {
		v, ok := values[field]
		return v, ok, nil
	}
}

func TestEvaluateRequiredFieldMissing(t *testing.T) {
	rules := []Rule{{Field: "body.orderId", Required: true}}
	errs := Evaluate(rules, extractorFrom(nil))
	if len(errs) != 1 || errs[0].Field != "body.orderId" {
		t.Fatalf("expected one required-field error, got %+v", errs)
	}
}

func TestEvaluateOptionalFieldMissingIsFine(t *testing.T) {
	rules := []Rule{{Field: "body.nickname", Required: false, Type: "string"}}
	errs := Evaluate(rules, extractorFrom(nil))
	if len(errs) != 0 {
		t.Fatalf("expected no errors for a missing optional field, got %+v", errs)
	}
}

func TestEvaluateTypeChecks(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		value   any
		wantErr bool
	}{
		{"number valid", Rule{Field: "f", Type: "number"}, float64(42), false},
		{"number invalid", Rule{Field: "f", Type: "number"}, "not-a-number", true},
		{"number coerced from string", Rule{Field: "f", Type: "number"}, "42", false},
		{"boolean valid", Rule{Field: "f", Type: "boolean"}, true, false},
		{"boolean invalid", Rule{Field: "f", Type: "boolean"}, "nope", true},
		{"string valid", Rule{Field: "f", Type: "string"}, "hello", false},
		{"string invalid (number given)", Rule{Field: "f", Type: "string"}, float64(1), true},
		{"enum valid", Rule{Field: "f", Type: "enum", AllowedValues: []string{"a", "b"}}, "a", false},
		{"enum invalid", Rule{Field: "f", Type: "enum", AllowedValues: []string{"a", "b"}}, "z", true},
		{"regex valid", Rule{Field: "f", Type: "regex", Pattern: `^\d+$`}, "123", false},
		{"regex invalid", Rule{Field: "f", Type: "regex", Pattern: `^\d+$`}, "abc", true},
		{"email valid", Rule{Field: "f", Type: "email"}, "a@b.com", false},
		{"email invalid", Rule{Field: "f", Type: "email"}, "not-an-email", true},
		{"uuid valid", Rule{Field: "f", Type: "uuid"}, "123e4567-e89b-12d3-a456-426614174000", false},
		{"uuid invalid", Rule{Field: "f", Type: "uuid"}, "not-a-uuid", true},
		{"date valid", Rule{Field: "f", Type: "date"}, "2026-01-02", false},
		{"date invalid", Rule{Field: "f", Type: "date"}, "not-a-date", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.rule.Required = true
			errs := Evaluate([]Rule{tt.rule}, extractorFrom(map[string]any{"f": tt.value}))
			if tt.wantErr && len(errs) == 0 {
				t.Fatalf("expected a validation error for value %v, got none", tt.value)
			}
			if !tt.wantErr && len(errs) != 0 {
				t.Fatalf("expected no validation error for value %v, got %+v", tt.value, errs)
			}
		})
	}
}

func TestEvaluateNumberBounds(t *testing.T) {
	min, max := 1.0, 10.0
	rule := Rule{Field: "f", Required: true, Type: "number", Min: &min, Max: &max}

	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": float64(5)})); len(errs) != 0 {
		t.Fatalf("expected 5 within [1,10] to pass, got %+v", errs)
	}
	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": float64(0)})); len(errs) == 0 {
		t.Fatal("expected 0 below min to fail")
	}
	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": float64(11)})); len(errs) == 0 {
		t.Fatal("expected 11 above max to fail")
	}
}

func TestEvaluateStringLength(t *testing.T) {
	minLen, maxLen := 2, 5
	rule := Rule{Field: "f", Required: true, Type: "string", MinLen: &minLen, MaxLen: &maxLen}

	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": "abc"})); len(errs) != 0 {
		t.Fatalf("expected 'abc' to pass length bounds, got %+v", errs)
	}
	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": "a"})); len(errs) == 0 {
		t.Fatal("expected a too-short string to fail")
	}
	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": "toolongvalue"})); len(errs) == 0 {
		t.Fatal("expected a too-long string to fail")
	}
}

// TestEvaluateEmptyTypeEnforcesMinMaxAndAllowedValues guards the Rule doc
// comment's documented promise: an empty Type checks Required plus
// whichever of Pattern/AllowedValues/Min/Max/MinLen/MaxLen are set, without
// needing an explicit Type — Min/Max/AllowedValues used to only be read
// under their own dedicated Type ("number"/"enum"), so a Type-less rule
// using them silently validated nothing.
func TestEvaluateEmptyTypeEnforcesMinMaxAndAllowedValues(t *testing.T) {
	min, max := 5.0, 10.0
	rule := Rule{Field: "f", Required: true, Min: &min, Max: &max} // no Type set

	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": float64(7)})); len(errs) != 0 {
		t.Fatalf("expected 7 within [5,10] to pass with no Type set, got %+v", errs)
	}
	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": float64(3)})); len(errs) == 0 {
		t.Fatal("expected 3 below min to fail even with no Type set")
	}
	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": float64(20)})); len(errs) == 0 {
		t.Fatal("expected 20 above max to fail even with no Type set")
	}
}

func TestEvaluateEmptyTypeEnforcesAllowedValues(t *testing.T) {
	rule := Rule{Field: "f", Required: true, AllowedValues: []string{"gold", "silver"}} // no Type set

	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": "gold"})); len(errs) != 0 {
		t.Fatalf("expected an allowed value to pass with no Type set, got %+v", errs)
	}
	if errs := Evaluate([]Rule{rule}, extractorFrom(map[string]any{"f": "bronze"})); len(errs) == 0 {
		t.Fatal("expected a disallowed value to fail even with no Type set")
	}
}

func TestEvaluateExtractorErrorSurfacesAsValidationError(t *testing.T) {
	rules := []Rule{{Field: "xpath.bad", Required: true}}
	badExtractor := func(field string) (any, bool, error) {
		return nil, false, errors.New("unknown field prefix")
	}
	errs := Evaluate(rules, badExtractor)
	if len(errs) != 1 {
		t.Fatalf("expected the extractor error to surface as one validation error, got %+v", errs)
	}
}
