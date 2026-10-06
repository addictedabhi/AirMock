package smppengine

import (
	"regexp"
	"strconv"
	"strings"
)

var rangePattern = regexp.MustCompile(`^\d+\s*-\s*\d+$`)

// ruleDestMatchType is the match type for a rule's destination. An explicit
// DestAddrMatchType wins. Otherwise, a rule that has no messageMatch (so its
// generic matchType has nothing else to describe) lends its matchType to the
// destination, because that is what people reach for first: {destAddrPattern:
// "1555", matchType: "prefix"}. "contains" is excluded: it is the UI's
// default on every rule and must not loosen an exact destination.
func ruleDestMatchType(destType, matchType, messageMatch string) string {
	if destType != "" {
		return destType
	}
	if messageMatch == "" && matchType != "" && matchType != "contains" {
		return matchType
	}
	return ""
}

// destAddrMatches reports whether an SMPP address (a submit_sm
// destination_addr) matches a rule's DestAddrPattern.
//
// matchType is "exact", "prefix", "contains", "wildcard" (* any run of characters, ? one
// character), "regex" (unanchored, like the other matchers) or "range"
// (inclusive numeric low-high). When it is blank it is inferred from the
// pattern: a pattern containing * or ? is a wildcard, "low-high" digits is a
// range, anything else is exact, so existing rules keep working and a
// pattern like "1555*" does what it looks like. A blank pattern matches
// every address.
//
// Except for regex, the pattern may list comma-separated alternatives, and a
// leading "+" (E.164) is ignored on both the pattern and the address.
func destAddrMatches(matchType, pattern, addr string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true
	}
	if matchType == "regex" {
		matched, err := regexp.MatchString(pattern, addr)
		return err == nil && matched
	}
	addr = strings.TrimPrefix(addr, "+")
	for _, alt := range strings.Split(pattern, ",") {
		alt = strings.TrimPrefix(strings.TrimSpace(alt), "+")
		if alt == "" {
			continue
		}
		if altMatches(matchType, alt, addr) {
			return true
		}
	}
	return false
}

func altMatches(matchType, alt, addr string) bool {
	if matchType == "" {
		switch {
		case strings.ContainsAny(alt, "*?"):
			matchType = "wildcard"
		case rangePattern.MatchString(alt):
			matchType = "range"
		default:
			matchType = "exact"
		}
	}
	switch matchType {
	case "prefix":
		return strings.HasPrefix(addr, alt)
	case "contains":
		return strings.Contains(addr, alt)
	case "wildcard":
		return wildcardMatch(alt, addr)
	case "range":
		return inRange(alt, addr)
	default: // "exact"
		return alt == addr
	}
}

// wildcardMatch is a glob match over the whole string: * matches any run of
// characters (including none), ? matches exactly one.
func wildcardMatch(pattern, s string) bool {
	var re strings.Builder
	re.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			re.WriteString(".*")
		case '?':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	re.WriteString("$")
	ok, err := regexp.MatchString(re.String(), s)
	return err == nil && ok
}

func inRange(spec, addr string) bool {
	lo, hi, found := strings.Cut(spec, "-")
	if !found {
		return false
	}
	l, err1 := strconv.ParseUint(strings.TrimSpace(lo), 10, 64)
	h, err2 := strconv.ParseUint(strings.TrimSpace(hi), 10, 64)
	n, err3 := strconv.ParseUint(addr, 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	return n >= l && n <= h
}
