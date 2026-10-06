package apiclient

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEnsureSchemeLeavesAlreadySchemedURLUnchanged(t *testing.T) {
	got := EnsureScheme("https://example.com/orders", "http://")
	if got != "https://example.com/orders" {
		t.Fatalf("expected the existing scheme to be left alone, got %q", got)
	}
}

func TestEnsureSchemePrependsDefaultWhenMissing(t *testing.T) {
	got := EnsureScheme("localhost:8080/orders", "http://")
	if got != "http://localhost:8080/orders" {
		t.Fatalf("got %q", got)
	}
}

func TestEnsureSchemeSupportsNonHTTPDefault(t *testing.T) {
	got := EnsureScheme("localhost:8080/events", "ws://")
	if got != "ws://localhost:8080/events" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstituteVarsResolvesEnvironmentVariables(t *testing.T) {
	got := SubstituteVars("{{baseUrl}}/orders/{{id}}", map[string]string{"baseUrl": "http://api", "id": "42"})
	if got != "http://api/orders/42" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstituteVarsLeavesUnknownPlaceholdersUntouched(t *testing.T) {
	got := SubstituteVars("{{typoedName}}", map[string]string{"realName": "value"})
	if got != "{{typoedName}}" {
		t.Fatalf("expected an unresolved placeholder to stay visible as a typo signal, got %q", got)
	}
}

func TestSubstituteVarsResolvesTimestamp(t *testing.T) {
	before := time.Now().Unix()
	got := SubstituteVars("{{$timestamp}}", nil)
	after := time.Now().Unix()

	n, err := strconv.ParseInt(got, 10, 64)
	if err != nil {
		t.Fatalf("expected a unix timestamp, got %q: %v", got, err)
	}
	if n < before || n > after {
		t.Fatalf("expected timestamp between %d and %d, got %d", before, after, n)
	}
}

func TestSubstituteVarsResolvesIsoTimestamp(t *testing.T) {
	got := SubstituteVars("{{$isoTimestamp}}", nil)
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("expected an RFC3339 timestamp, got %q: %v", got, err)
	}
}

func TestSubstituteVarsResolvesRandomUUID(t *testing.T) {
	got := SubstituteVars("{{$randomUUID}}", nil)
	if len(got) != 36 || strings.Count(got, "-") != 4 {
		t.Fatalf("expected a UUID-shaped string, got %q", got)
	}
}

func TestSubstituteVarsResolvesRandomIntWithinRange(t *testing.T) {
	got := SubstituteVars("{{$randomInt}}", nil)
	n, err := strconv.Atoi(got)
	if err != nil {
		t.Fatalf("expected an integer, got %q: %v", got, err)
	}
	if n < 0 || n >= 1000 {
		t.Fatalf("expected a value in [0, 1000), got %d", n)
	}
}

func TestSubstituteVarsGivesEachDynamicVarReferenceItsOwnFreshValue(t *testing.T) {
	got := SubstituteVars("{{$randomUUID}}|{{$randomUUID}}", nil)
	uuids := strings.Split(got, "|")
	if len(uuids) != 2 || uuids[0] == uuids[1] {
		t.Fatalf("expected two independently generated UUIDs joined by '|', got %q", got)
	}
}

func TestSubstituteVarsLeavesUnknownDynamicVarUntouched(t *testing.T) {
	got := SubstituteVars("{{$notARealDynamicVar}}", nil)
	if got != "{{$notARealDynamicVar}}" {
		t.Fatalf("expected an unknown $-prefixed placeholder to stay visible, got %q", got)
	}
}

func TestSubstituteVarsAppliesDynamicVarsEvenWithNoEnvironmentVars(t *testing.T) {
	// Guards the early-return in SubstituteVars: dynamic-var resolution must
	// not be gated behind len(vars) == 0, or {{$timestamp}} would never
	// resolve for a request that has no environment selected at all.
	got := SubstituteVars("id-{{$randomInt}}", map[string]string{})
	if strings.HasPrefix(got, "id-{{$randomInt}}") {
		t.Fatalf("expected {{$randomInt}} to resolve even with an empty vars map, got %q", got)
	}
}
