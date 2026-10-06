// Package validate is a small, reusable request-validation engine: a list
// of Rules, each naming a field via the same "body."/"header."/"query."
// path convention internal/mock.ExtractField uses for callback extraction
// (and phase 1.4's response rules will reuse), evaluated against whatever
// extractor function the caller supplies. This package intentionally has
// no dependency on internal/mock — the caller (httpengine) supplies field
// values via a plain closure, which is what keeps mock -> validate a
// one-way dependency instead of a cycle.
package validate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Extractor resolves a rule's Field path to a value (native Go type:
// string/float64/bool/nil/...) and whether it was present at all.
type Extractor func(field string) (value any, exists bool, err error)

// Rule is one field constraint. Type is optional — an empty Type only
// checks Required plus whichever of Pattern/AllowedValues/Min/Max/MinLen/
// MaxLen are non-zero, without asserting a specific underlying type.
type Rule struct {
	Field         string   `json:"field"` // e.g. "body.orderId", "query.limit", "header.X-Api-Key"
	Required      bool     `json:"required"`
	Type          string   `json:"type,omitempty"` // string|number|boolean|enum|regex|email|uuid|date
	Pattern       string   `json:"pattern,omitempty"`
	AllowedValues []string `json:"allowedValues,omitempty"`
	Min           *float64 `json:"min,omitempty"`
	Max           *float64 `json:"max,omitempty"`
	MinLen        *int     `json:"minLen,omitempty"`
	MaxLen        *int     `json:"maxLen,omitempty"`
}

type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Evaluate runs every rule against extract, short-circuiting per rule (not
// overall) so a request gets every applicable error back at once rather
// than one at a time across repeated round-trips.
func Evaluate(rules []Rule, extract Extractor) []ValidationError {
	var errs []ValidationError
	for _, rule := range rules {
		value, exists, err := extract(rule.Field)
		if err != nil {
			errs = append(errs, ValidationError{Field: rule.Field, Message: err.Error()})
			continue
		}
		if !exists {
			if rule.Required {
				msg := "required field is missing"
				if strings.HasPrefix(rule.Field, "body.variables.") {
					msg += ` (GraphQL: send it in the request's "variables" object or as an inline argument, e.g. field(` + strings.TrimPrefix(rule.Field, "body.variables.") + `: ...))`
				}
				errs = append(errs, ValidationError{Field: rule.Field, Message: msg})
			}
			continue
		}
		if msg, ok := checkValue(rule, value); !ok {
			errs = append(errs, ValidationError{Field: rule.Field, Message: msg})
		}
	}
	return errs
}

func checkValue(rule Rule, value any) (string, bool) {
	switch rule.Type {
	case "number":
		f, ok := asFloat(value)
		if !ok {
			return "must be a number", false
		}
		if rule.Min != nil && f < *rule.Min {
			return fmt.Sprintf("must be >= %g", *rule.Min), false
		}
		if rule.Max != nil && f > *rule.Max {
			return fmt.Sprintf("must be <= %g", *rule.Max), false
		}
		return "", true

	case "boolean":
		if _, ok := asBool(value); !ok {
			return "must be a boolean", false
		}
		return "", true

	case "enum":
		s := asString(value)
		for _, allowed := range rule.AllowedValues {
			if s == allowed {
				return "", true
			}
		}
		return fmt.Sprintf("must be one of %v", rule.AllowedValues), false

	case "regex":
		return matchPattern(rule.Pattern, asString(value))

	case "email":
		return matchPattern(emailPattern, asString(value))

	case "uuid":
		if _, err := uuid.Parse(asString(value)); err != nil {
			return "must be a valid UUID", false
		}
		return "", true

	case "date":
		s := asString(value)
		for _, layout := range dateLayouts {
			if _, err := time.Parse(layout, s); err == nil {
				return "", true
			}
		}
		return "must be a valid date", false

	case "string", "":
		s, isString := value.(string)
		if rule.Type == "string" && !isString {
			return "must be a string", false
		}
		if !isString {
			s = asString(value)
		}
		// An empty Type is documented (see the Rule doc comment) as
		// checking Required plus whichever of Pattern/AllowedValues/Min/
		// Max/MinLen/MaxLen are actually set, without asserting a specific
		// underlying type — AllowedValues/Min/Max used to only be read
		// under their own explicit Type ("enum"/"number"), so a rule like
		// {Field: "body.age", Min: 5, Max: 10} with no Type silently
		// validated nothing, contradicting that documented behavior.
		if len(rule.AllowedValues) > 0 {
			matched := false
			for _, allowed := range rule.AllowedValues {
				if s == allowed {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Sprintf("must be one of %v", rule.AllowedValues), false
			}
		}
		if rule.Min != nil || rule.Max != nil {
			f, ok := asFloat(value)
			if !ok {
				return "must be a number", false
			}
			if rule.Min != nil && f < *rule.Min {
				return fmt.Sprintf("must be >= %g", *rule.Min), false
			}
			if rule.Max != nil && f > *rule.Max {
				return fmt.Sprintf("must be <= %g", *rule.Max), false
			}
		}
		if rule.MinLen != nil && len(s) < *rule.MinLen {
			return fmt.Sprintf("must be at least %d characters", *rule.MinLen), false
		}
		if rule.MaxLen != nil && len(s) > *rule.MaxLen {
			return fmt.Sprintf("must be at most %d characters", *rule.MaxLen), false
		}
		if rule.Pattern != "" {
			return matchPattern(rule.Pattern, s)
		}
		return "", true

	default:
		return fmt.Sprintf("unknown validation type %q", rule.Type), false
	}
}

func matchPattern(pattern, s string) (string, bool) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Sprintf("invalid pattern %q", pattern), false
	}
	if !re.MatchString(s) {
		return fmt.Sprintf("must match pattern %q", pattern), false
	}
	return "", true
}

func asFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func asBool(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		b, err := strconv.ParseBool(v)
		return b, err == nil
	default:
		return false, false
	}
}

func asString(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

const emailPattern = `^[^\s@]+@[^\s@]+\.[^\s@]+$`

var dateLayouts = []string{time.RFC3339, "2006-01-02", "2006-01-02T15:04:05"}
