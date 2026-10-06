package mock

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/antchfx/xmlquery"
	"github.com/tidwall/gjson"
)

// ExtractField resolves a "body."/"header."/"query."/"xpath." prefixed path
// against a request context and returns its value using the same native Go
// types encoding/json would produce for JSON bodies (string/float64/bool/
// map/slice/nil) or a string for xpath matches, plus whether the field was
// present at all. This is the one shared extraction mechanism behind async
// callback target resolution here, validation (phase 1.3), conditional
// response rules (phase 1.4), and now SOAP envelope field access — a
// "variables." prefix will join it once GraphQL exists (phase 1.6).
func ExtractField(field string, reqCtx RequestContext) (value any, exists bool, err error) {
	prefix, key, ok := strings.Cut(field, ".")
	if !ok {
		return nil, false, fmt.Errorf("field path %q must be prefixed with body./header./query.", field)
	}

	switch prefix {
	case "body":
		if len(reqCtx.BodyBytes) == 0 {
			return nil, false, nil
		}
		result := gjson.GetBytes(reqCtx.BodyBytes, key)
		if !result.Exists() {
			return nil, false, nil
		}
		return result.Value(), true, nil
	case "header":
		v, ok := reqCtx.Header[key]
		if !ok {
			return nil, false, nil
		}
		return v, true, nil
	case "query":
		v, ok := reqCtx.Query[key]
		if !ok {
			return nil, false, nil
		}
		return v, true, nil
	case "xpath":
		return extractXPath(key, reqCtx.BodyBytes)
	default:
		return nil, false, fmt.Errorf("unknown field prefix %q (want body/header/query/xpath)", prefix)
	}
}

func extractXPath(expr string, bodyBytes []byte) (any, bool, error) {
	if len(bodyBytes) == 0 {
		return nil, false, nil
	}
	doc, err := xmlquery.Parse(bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, false, fmt.Errorf("parse XML body: %w", err)
	}
	node := xmlquery.FindOne(doc, expr)
	if node == nil {
		return nil, false, nil
	}
	return strings.TrimSpace(node.InnerText()), true, nil
}

// ExtractCallbackURL is ExtractField specialized to "the value must be a
// present, non-empty string" — exactly what a callback target needs.
func ExtractCallbackURL(path string, reqCtx RequestContext) (string, error) {
	value, exists, err := ExtractField(path, reqCtx)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("field %q not found on request", path)
	}
	s, ok := value.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("field %q is not a non-empty string", path)
	}
	return s, nil
}
