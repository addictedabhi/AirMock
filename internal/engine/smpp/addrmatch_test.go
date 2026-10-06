package smppengine

import (
	"testing"

	"github.com/addictedabhi/airmock/internal/mock"
)

func TestDestAddrMatching(t *testing.T) {
	cases := []struct {
		name, matchType, pattern, addr string
		want                           bool
	}{
		// exact, and the default when no type is given
		{"exact hit", "exact", "15551234", "15551234", true},
		{"exact miss on a longer number", "exact", "15551234", "155512345", false},
		{"blank pattern matches anything", "", "", "999", true},
		{"inferred exact", "", "15551234", "15551234", true},
		{"inferred exact miss", "", "15551234", "15551235", false},
		// prefix
		{"prefix hit", "prefix", "1555", "15551234", true},
		{"prefix miss", "prefix", "1555", "14441234", false},
		// wildcard, including the inferred form
		{"wildcard star", "wildcard", "1555*", "15551234", true},
		{"wildcard question mark", "wildcard", "155512?4", "15551234", true},
		{"wildcard anchored at the end", "wildcard", "*1234", "15551234", true},
		{"wildcard miss", "wildcard", "1555*9", "15551234", false},
		{"inferred wildcard from a star", "", "1555*", "15551234", true},
		{"inferred wildcard miss", "", "1555*", "14441234", false},
		// regex (unanchored, like the other matchers)
		{"regex hit", "regex", `^1555\d{4}$`, "15551234", true},
		{"regex miss", "regex", `^1555\d{4}$`, "155512345", false},
		{"bad regex never matches", "regex", `(`, "1", false},
		// numeric range, including the inferred form
		{"range inside", "range", "15550000-15559999", "15551234", true},
		{"range lower bound inclusive", "range", "100-200", "100", true},
		{"range upper bound inclusive", "range", "100-200", "200", true},
		{"range outside", "range", "100-200", "201", false},
		{"inferred range", "", "100-200", "150", true},
		{"range with a non-numeric address", "range", "100-200", "abc", false},
		// E.164 plus sign is ignored on both sides
		{"plus on the address", "exact", "15551234", "+15551234", true},
		{"plus on the pattern", "prefix", "+1555", "15551234", true},
		// comma-separated alternatives
		{"alternatives, second matches", "prefix", "1444,1555", "15551234", true},
		{"alternatives mixing ranges", "range", "1-5,100-200", "150", true},
		{"alternatives none match", "prefix", "1444,1666", "15551234", false},
		{"alternatives with spaces", "exact", "111, 222", "222", true},
	}
	for _, c := range cases {
		if got := destAddrMatches(c.matchType, c.pattern, c.addr); got != c.want {
			t.Errorf("%s: destAddrMatches(%q, %q, %q) = %v, want %v", c.name, c.matchType, c.pattern, c.addr, got, c.want)
		}
	}
}

// A rule's generic matchType is documented as applying to messageMatch. People
// naturally set it to "prefix" or "regex" on a rule that only filters on the
// destination; with no messageMatch for it to describe, it is used for the
// destination too.
func TestRuleMatchTypeFallsBackToTheDestinationWhenThereIsNoMessageMatch(t *testing.T) {
	cases := []struct {
		name    string
		rule    mockRule
		dest    string
		wantHit bool
	}{
		{"plain prefix via matchType", mockRule{pattern: "1555", matchType: "prefix"}, "15551234", true},
		{"plain prefix miss", mockRule{pattern: "1555", matchType: "prefix"}, "14441234", false},
		{"regex via matchType", mockRule{pattern: `^1555\d{4}$`, matchType: "regex"}, "15551234", true},
		{"regex miss", mockRule{pattern: `^1555\d{4}$`, matchType: "regex"}, "155512345", false},
		// "contains" is the UI's default matchType on every rule, so it must NOT
		// loosen an existing exact destination into a substring match.
		{"default contains does not change the destination", mockRule{pattern: "5551", matchType: "contains"}, "15551234", false},
		{"explicit destAddrMatchType wins over matchType", mockRule{pattern: "1555", matchType: "exact", destType: "prefix"}, "15551234", true},
		// With a messageMatch present, matchType belongs to the message, so the
		// destination keeps its own default (exact here).
		{"matchType stays the message's when messageMatch is set", mockRule{pattern: "1555", matchType: "prefix", message: "hello"}, "15551234", false},
	}
	for _, c := range cases {
		cfgRule := c.rule.toRule()
		_, _, matched := evaluateSMPPRules(&mock.SMPPConfig{Rules: []mock.SMPPRule{cfgRule}}, c.dest, "hello", "id", nil)
		if matched != c.wantHit {
			t.Errorf("%s: matched=%v, want %v", c.name, matched, c.wantHit)
		}
	}
}

type mockRule struct{ pattern, matchType, destType, message string }

func (r mockRule) toRule() mock.SMPPRule {
	return mock.SMPPRule{DestAddrPattern: r.pattern, MatchType: r.matchType, DestAddrMatchType: r.destType, MessageMatch: r.message}
}

func TestContainsIsAValidDestinationMatchType(t *testing.T) {
	if !destAddrMatches("contains", "5551", "15551234") || destAddrMatches("contains", "9999", "15551234") {
		t.Fatal("contains should match a substring of the destination")
	}
}
