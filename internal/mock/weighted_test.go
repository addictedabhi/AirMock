package mock

import "testing"

func TestPickWeightedResponse_distribution(t *testing.T) {
	cfg := &WeightedConfig{Responses: []WeightedResponse{
		{Weight: 90, Response: ResponseTemplate{StatusCode: 200}},
		{Weight: 10, Response: ResponseTemplate{StatusCode: 500}},
	}}

	var count200, count500 int
	const trials = 5000
	for i := 0; i < trials; i++ {
		switch PickWeightedResponse(cfg).StatusCode {
		case 200:
			count200++
		case 500:
			count500++
		default:
			t.Fatalf("unexpected status code")
		}
	}
	// Expect roughly 90/10 — generous tolerance since this is genuinely
	// random, just asserting it's not uniform (50/50) and not always one
	// side (100/0).
	if count200 < trials*7/10 || count200 > trials*98/100 {
		t.Fatalf("expected roughly 90%% 200s, got %d/%d", count200, trials)
	}
	if count500 == 0 {
		t.Fatalf("expected at least some 500s out of %d trials with weight 10", trials)
	}
}

func TestPickWeightedResponse_zeroWeightsFallBackToFirst(t *testing.T) {
	cfg := &WeightedConfig{Responses: []WeightedResponse{
		{Weight: 0, Response: ResponseTemplate{StatusCode: 201}},
		{Weight: 0, Response: ResponseTemplate{StatusCode: 500}},
	}}
	for i := 0; i < 20; i++ {
		if got := PickWeightedResponse(cfg).StatusCode; got != 201 {
			t.Fatalf("expected fallback to the first entry (201), got %d", got)
		}
	}
}

func TestPickWeightedResponse_negativeWeightIgnored(t *testing.T) {
	cfg := &WeightedConfig{Responses: []WeightedResponse{
		{Weight: -5, Response: ResponseTemplate{StatusCode: 999}},
		{Weight: 1, Response: ResponseTemplate{StatusCode: 200}},
	}}
	for i := 0; i < 20; i++ {
		if got := PickWeightedResponse(cfg).StatusCode; got != 200 {
			t.Fatalf("expected the only positive-weight entry (200), got %d", got)
		}
	}
}

func TestSelectResponse_precedence(t *testing.T) {
	def := &Definition{
		Response: ResponseTemplate{StatusCode: 1},
		ResponseRules: []ResponseRule{
			{Conditions: []Condition{{Field: "query.x", Operator: "exists"}}, Response: ResponseTemplate{StatusCode: 2}},
		},
	}
	reqCtx := RequestContext{Query: map[string]string{"x": "1"}}

	// ResponseRules wins over the plain default when Weighted is unset.
	if got := SelectResponse(def, reqCtx).StatusCode; got != 2 {
		t.Fatalf("expected ResponseRules (2) to win, got %d", got)
	}

	// Weighted, once set, wins over ResponseRules.
	def.Weighted = &WeightedConfig{Responses: []WeightedResponse{{Weight: 1, Response: ResponseTemplate{StatusCode: 3}}}}
	if got := SelectResponse(def, reqCtx).StatusCode; got != 3 {
		t.Fatalf("expected Weighted (3) to win over ResponseRules, got %d", got)
	}

	// An empty Weighted.Responses list falls back to ResponseRules/default,
	// same as if Weighted were unset.
	def.Weighted = &WeightedConfig{}
	if got := SelectResponse(def, reqCtx).StatusCode; got != 2 {
		t.Fatalf("expected empty Weighted to fall through to ResponseRules (2), got %d", got)
	}
}
