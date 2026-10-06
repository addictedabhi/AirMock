package mock

import (
	"regexp"
	"strconv"
	"strings"
)

// Condition tests one field (same "body."/"header."/"query." convention as
// ExtractField/validate.Rule) against Value using Operator.
type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"` // equals|notEquals|contains|regex|gt|lt|gte|lte|exists|notExists
	Value    string `json:"value,omitempty"`
}

// ResponseRule matches when every Condition matches (AND); the first
// matching rule in a Definition.ResponseRules list wins.
type ResponseRule struct {
	Conditions []Condition      `json:"conditions"`
	Response   ResponseTemplate `json:"response"`
}

// MatchResponseRule evaluates rules in order and returns the first whose
// conditions all match, or (nil, false) if none do — callers fall back to
// the mock's plain Response in that case.
func MatchResponseRule(rules []ResponseRule, reqCtx RequestContext) (*ResponseTemplate, bool) {
	for i := range rules {
		if allConditionsMatch(rules[i].Conditions, reqCtx) {
			return &rules[i].Response, true
		}
	}
	return nil, false
}

func allConditionsMatch(conditions []Condition, reqCtx RequestContext) bool {
	for _, c := range conditions {
		if !conditionMatches(c, reqCtx) {
			return false
		}
	}
	return true
}

func conditionMatches(c Condition, reqCtx RequestContext) bool {
	value, exists, err := ExtractField(c.Field, reqCtx)
	if err != nil {
		return false
	}

	switch c.Operator {
	case "exists":
		return exists
	case "notExists":
		return !exists
	}
	if !exists {
		return false
	}

	switch c.Operator {
	case "equals":
		return stringifyValue(value) == c.Value
	case "notEquals":
		return stringifyValue(value) != c.Value
	case "contains":
		return strings.Contains(stringifyValue(value), c.Value)
	case "regex":
		re, err := regexp.Compile(c.Value)
		return err == nil && re.MatchString(stringifyValue(value))
	case "gt", "lt", "gte", "lte":
		return compareNumeric(c.Operator, value, c.Value)
	default:
		return false
	}
}

func compareNumeric(operator string, value any, target string) bool {
	f, ok := numericValue(value)
	cv, err := strconv.ParseFloat(target, 64)
	if !ok || err != nil {
		return false
	}
	switch operator {
	case "gt":
		return f > cv
	case "lt":
		return f < cv
	case "gte":
		return f >= cv
	case "lte":
		return f <= cv
	default:
		return false
	}
}

func numericValue(value any) (float64, bool) {
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

func stringifyValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}
