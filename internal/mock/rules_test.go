package mock

import "testing"

func ctxWithBody(jsonBody string) RequestContext {
	return RequestContext{BodyBytes: []byte(jsonBody)}
}

func TestConditionOperators(t *testing.T) {
	tests := []struct {
		name string
		cond Condition
		ctx  RequestContext
		want bool
	}{
		{"equals true", Condition{Field: "body.type", Operator: "equals", Value: "premium"}, ctxWithBody(`{"type":"premium"}`), true},
		{"equals false", Condition{Field: "body.type", Operator: "equals", Value: "premium"}, ctxWithBody(`{"type":"basic"}`), false},
		{"notEquals true", Condition{Field: "body.type", Operator: "notEquals", Value: "premium"}, ctxWithBody(`{"type":"basic"}`), true},
		{"contains true", Condition{Field: "body.name", Operator: "contains", Value: "ann"}, ctxWithBody(`{"name":"annual"}`), true},
		{"contains false", Condition{Field: "body.name", Operator: "contains", Value: "zzz"}, ctxWithBody(`{"name":"annual"}`), false},
		{"regex true", Condition{Field: "body.code", Operator: "regex", Value: `^[A-Z]{3}$`}, ctxWithBody(`{"code":"ABC"}`), true},
		{"regex false", Condition{Field: "body.code", Operator: "regex", Value: `^[A-Z]{3}$`}, ctxWithBody(`{"code":"abc"}`), false},
		{"gt true", Condition{Field: "body.amount", Operator: "gt", Value: "1000"}, ctxWithBody(`{"amount":1500}`), true},
		{"gt false", Condition{Field: "body.amount", Operator: "gt", Value: "1000"}, ctxWithBody(`{"amount":500}`), false},
		{"lt true", Condition{Field: "body.amount", Operator: "lt", Value: "1000"}, ctxWithBody(`{"amount":500}`), true},
		{"gte boundary true", Condition{Field: "body.amount", Operator: "gte", Value: "1000"}, ctxWithBody(`{"amount":1000}`), true},
		{"lte boundary true", Condition{Field: "body.amount", Operator: "lte", Value: "1000"}, ctxWithBody(`{"amount":1000}`), true},
		{"exists true", Condition{Field: "body.type", Operator: "exists"}, ctxWithBody(`{"type":"x"}`), true},
		{"exists false", Condition{Field: "body.missing", Operator: "exists"}, ctxWithBody(`{"type":"x"}`), false},
		{"notExists true", Condition{Field: "body.missing", Operator: "notExists"}, ctxWithBody(`{"type":"x"}`), true},
		{"notExists false", Condition{Field: "body.type", Operator: "notExists"}, ctxWithBody(`{"type":"x"}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conditionMatches(tt.cond, tt.ctx); got != tt.want {
				t.Errorf("conditionMatches(%+v) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}

func TestMatchResponseRuleFirstMatchWins(t *testing.T) {
	rules := []ResponseRule{
		{
			Conditions: []Condition{{Field: "query.type", Operator: "equals", Value: "premium"}},
			Response:   ResponseTemplate{StatusCode: 200, BodyTemplate: "premium response"},
		},
		{
			Conditions: []Condition{{Field: "body.amount", Operator: "gt", Value: "1000"}},
			Response:   ResponseTemplate{StatusCode: 200, BodyTemplate: "large order response"},
		},
	}

	premiumCtx := RequestContext{Query: map[string]string{"type": "premium"}, BodyBytes: []byte(`{}`)}
	resp, ok := MatchResponseRule(rules, premiumCtx)
	if !ok || resp.BodyTemplate != "premium response" {
		t.Fatalf("expected the premium rule to match first, got %+v ok=%v", resp, ok)
	}

	largeOrderCtx := RequestContext{Query: map[string]string{}, BodyBytes: []byte(`{"amount":5000}`)}
	resp, ok = MatchResponseRule(rules, largeOrderCtx)
	if !ok || resp.BodyTemplate != "large order response" {
		t.Fatalf("expected the large-order rule to match, got %+v ok=%v", resp, ok)
	}

	noMatchCtx := RequestContext{Query: map[string]string{}, BodyBytes: []byte(`{"amount":10}`)}
	if _, ok := MatchResponseRule(rules, noMatchCtx); ok {
		t.Fatal("expected no rule to match, caller should fall back to the default response")
	}
}

func TestMatchResponseRuleAllConditionsMustMatch(t *testing.T) {
	rules := []ResponseRule{
		{
			Conditions: []Condition{
				{Field: "query.type", Operator: "equals", Value: "premium"},
				{Field: "body.amount", Operator: "gt", Value: "1000"},
			},
			Response: ResponseTemplate{StatusCode: 200, BodyTemplate: "both matched"},
		},
	}

	onlyOneMatches := RequestContext{Query: map[string]string{"type": "premium"}, BodyBytes: []byte(`{"amount":10}`)}
	if _, ok := MatchResponseRule(rules, onlyOneMatches); ok {
		t.Fatal("expected no match when only one of two AND'd conditions is satisfied")
	}

	bothMatch := RequestContext{Query: map[string]string{"type": "premium"}, BodyBytes: []byte(`{"amount":5000}`)}
	if _, ok := MatchResponseRule(rules, bothMatch); !ok {
		t.Fatal("expected a match when both AND'd conditions are satisfied")
	}
}
