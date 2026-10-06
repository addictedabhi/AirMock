package mock

import "math/rand"

// WeightedConfig lets a single mock respond with different templates at
// configured probabilities (e.g. 90% success / 10% error) — a third,
// distinct response-selection mechanism alongside ResponseRules (branches on
// request content) and Scenario (branches on call sequence): this one
// branches on chance, for testing how a real client degrades against an
// endpoint that's merely UNRELIABLE rather than deterministically broken
// (ResponseRules) or in a specific state (Scenario).
type WeightedConfig struct {
	Responses []WeightedResponse `json:"responses"`
}

type WeightedResponse struct {
	// Weight is relative, not required to sum to 100 — {1,1,1} splits three
	// ways evenly just as {10,10,10} would.
	Weight   int              `json:"weight"`
	Response ResponseTemplate `json:"response"`
}

// PickWeightedResponse chooses one response by weighted random selection. A
// cfg with no positive weights falls back to the first entry rather than
// panicking on a zero-sized range — a config mistake should degrade to
// "always the first response," not a 500.
func PickWeightedResponse(cfg *WeightedConfig) ResponseTemplate {
	total := 0
	for _, wr := range cfg.Responses {
		if wr.Weight > 0 {
			total += wr.Weight
		}
	}
	if total <= 0 {
		return cfg.Responses[0].Response
	}
	roll := rand.Intn(total)
	for _, wr := range cfg.Responses {
		if wr.Weight <= 0 {
			continue
		}
		if roll < wr.Weight {
			return wr.Response
		}
		roll -= wr.Weight
	}
	return cfg.Responses[len(cfg.Responses)-1].Response
}

// SelectResponse is the one shared "which response template does this hit
// get" decision used by every HTTP-family matcher (REST/SOAP/GraphQL) that
// doesn't have its own Scenario support: Weighted (if configured) takes
// priority over ResponseRules, which takes priority over the mock's plain
// default Response — kept in one place so the three matchers can't drift
// out of sync on this precedence the way three separate copies eventually
// would.
func SelectResponse(def *Definition, reqCtx RequestContext) ResponseTemplate {
	if def.Weighted != nil && len(def.Weighted.Responses) > 0 {
		return PickWeightedResponse(def.Weighted)
	}
	if matched, ok := MatchResponseRule(def.ResponseRules, reqCtx); ok {
		return *matched
	}
	return def.Response
}
