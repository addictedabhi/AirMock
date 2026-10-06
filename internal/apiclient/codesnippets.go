package apiclient

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

const contentTypeHeader = "Content-Type"

// dropHeader removes any entry matching name (case-insensitive) from a
// key/value entries slice, preserving order of the rest.
func dropHeader(entries [][2]string, name string) [][2]string {
	out := entries[:0]
	for _, h := range entries {
		if !strings.EqualFold(h[0], name) {
			out = append(out, h)
		}
	}
	return out
}

// jsonQuote renders s as a JSON/JS string literal — valid syntax in both
// languages, and correctly escapes quotes/backslashes/newlines regardless
// of what the user typed, which hand-rolled quoting would risk getting
// wrong on edge-case input.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ToJSFetch renders a RequestSpec as a copy-pasteable browser/Node fetch()
// call — the "copy as JavaScript" action, sitting alongside ToCurl and
// ToPythonRequests as the three snippet languages the request panel offers.
// {{var}} placeholders are left as-is in the output (same convention as
// ToCurl) since a snippet is meant to be portable, not pre-resolved against
// whatever environment happened to be active when it was copied.
func ToJSFetch(spec RequestSpec) string {
	var b strings.Builder
	b.WriteString("fetch(" + jsonQuote(curlFullURL(spec)) + ", {\n")
	b.WriteString("  method: " + jsonQuote(strings.ToUpper(spec.Method)) + ",\n")

	headerLines, hasContentType := jsHeaderLines(spec)
	if authLine, ok := jsAuthHeaderLine(spec.Auth); ok {
		headerLines = append(headerLines, authLine)
		hasContentType = hasContentType || strings.EqualFold(authHeaderName(spec.Auth), contentTypeHeader)
	}
	bodyLine := jsBodyLine(spec, hasContentType, &headerLines)

	if len(headerLines) > 0 {
		b.WriteString("  headers: {\n")
		for i, h := range headerLines {
			b.WriteString("    " + h)
			if i < len(headerLines)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("  },\n")
	}
	if bodyLine != "" {
		b.WriteString("  body: " + bodyLine + ",\n")
	}
	b.WriteString("})\n  .then((res) => res.text())\n  .then(console.log);")
	return b.String()
}

func jsHeaderLines(spec RequestSpec) ([]string, bool) {
	var lines []string
	hasContentType := false
	for _, h := range spec.Headers {
		if h.Disabled {
			continue
		}
		if strings.EqualFold(h.Key, contentTypeHeader) {
			hasContentType = true
		}
		lines = append(lines, jsonQuote(h.Key)+": "+jsonQuote(h.Value))
	}
	return lines, hasContentType
}

func authHeaderName(auth *Auth) string {
	if auth == nil {
		return ""
	}
	switch auth.Type {
	case AuthBearer:
		return "Authorization"
	case AuthAPIKey:
		if auth.AddTo == "header" {
			return auth.KeyName
		}
	}
	return ""
}

func jsAuthHeaderLine(auth *Auth) (string, bool) {
	if auth == nil {
		return "", false
	}
	switch auth.Type {
	case AuthBearer:
		if auth.Token == "" {
			return "", false
		}
		return jsonQuote("Authorization") + ": " + jsonQuote("Bearer "+auth.Token), true
	case AuthBasic:
		// fetch has no dedicated basic-auth option (unlike Python's
		// requests.auth=(user, pass)) — pre-encoding the credentials here,
		// rather than emitting a runtime btoa(...) call, sidesteps btoa not
		// being a global in every Node version the copied snippet might run
		// under.
		encoded := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
		return jsonQuote("Authorization") + ": " + jsonQuote("Basic "+encoded), true
	case AuthAPIKey:
		if auth.AddTo == "header" && auth.KeyName != "" {
			return jsonQuote(auth.KeyName) + ": " + jsonQuote(auth.KeyValue), true
		}
	}
	return "", false
}

func jsBodyLine(spec RequestSpec, hasContentType bool, headerLines *[]string) string {
	switch spec.BodyMode {
	case "none":
		return ""
	case "urlencoded":
		var parts []string
		for _, f := range spec.FormFields {
			if f.Disabled {
				continue
			}
			parts = append(parts, "  "+jsonQuote(f.Key)+": "+jsonQuote(f.Value))
		}
		return "new URLSearchParams({\n" + strings.Join(parts, ",\n") + "\n  }).toString()"
	case "formdata":
		var b strings.Builder
		b.WriteString("(() => {\n    const fd = new FormData();\n")
		for _, f := range spec.FormFields {
			if f.Disabled {
				continue
			}
			b.WriteString("    fd.append(" + jsonQuote(f.Key) + ", " + jsonQuote(f.Value) + ");\n")
		}
		b.WriteString("    return fd;\n  })()")
		return b.String()
	default: // "" — raw
		if spec.Body == "" {
			return ""
		}
		if !hasContentType {
			*headerLines = append(*headerLines, jsonQuote(contentTypeHeader)+": "+jsonQuote(rawContentType(spec.RawContentType)))
		}
		return jsonQuote(spec.Body)
	}
}

// ToPythonRequests renders a RequestSpec as a copy-pasteable script using
// the `requests` library — Python's de facto standard HTTP client, and the
// most-requested "copy as code" target after curl/JS for an API testing
// tool's audience.
func ToPythonRequests(spec RequestSpec) string {
	// Collected as plain data first (queryEntries/headerEntries/basicAuthLine)
	// so every "does params/headers/auth exist" decision below reads from an
	// explicit slice/string, never from re-scanning already-written output —
	// the params dict in particular must exist whenever an API-key auth adds
	// to it even if spec.Query itself is empty, so building it as one
	// combined list up front avoids emitting a params[...] = ... reference
	// to a dict that was never declared.
	var queryEntries [][2]string
	for _, q := range spec.Query {
		if q.Disabled {
			continue
		}
		queryEntries = append(queryEntries, [2]string{q.Key, q.Value})
	}

	headerEntries, hasContentType := pyHeaderEntries(spec)
	basicAuthLine := ""
	if spec.Auth != nil {
		switch spec.Auth.Type {
		case AuthBearer:
			if spec.Auth.Token != "" {
				headerEntries = append(headerEntries, [2]string{"Authorization", "Bearer " + spec.Auth.Token})
			}
		case AuthBasic:
			basicAuthLine = "auth = (" + pyQuote(spec.Auth.Username) + ", " + pyQuote(spec.Auth.Password) + ")\n"
		case AuthAPIKey:
			if spec.Auth.KeyName != "" {
				if spec.Auth.AddTo == "query" {
					queryEntries = append(queryEntries, [2]string{spec.Auth.KeyName, spec.Auth.KeyValue})
				} else {
					headerEntries = append(headerEntries, [2]string{spec.Auth.KeyName, spec.Auth.KeyValue})
				}
			}
		}
	}

	bodyLine, bodyVar := pyBodyLine(spec, hasContentType, &headerEntries)

	var b strings.Builder
	b.WriteString("import requests\n\n")
	b.WriteString("url = " + pyQuote(spec.URL) + "\n")
	if len(queryEntries) > 0 {
		b.WriteString("params = {\n")
		for _, q := range queryEntries {
			b.WriteString("    " + pyQuote(q[0]) + ": " + pyQuote(q[1]) + ",\n")
		}
		b.WriteString("}\n")
	}
	if len(headerEntries) > 0 {
		b.WriteString("headers = {\n")
		for _, h := range headerEntries {
			b.WriteString("    " + pyQuote(h[0]) + ": " + pyQuote(h[1]) + ",\n")
		}
		b.WriteString("}\n")
	}
	if basicAuthLine != "" {
		b.WriteString(basicAuthLine)
	}
	if bodyLine != "" {
		b.WriteString(bodyLine)
	}

	b.WriteString("\nresponse = requests." + pyMethodCall(spec.Method) + "url")
	if len(headerEntries) > 0 {
		b.WriteString(", headers=headers")
	}
	if len(queryEntries) > 0 {
		b.WriteString(", params=params")
	}
	if basicAuthLine != "" {
		b.WriteString(", auth=auth")
	}
	if bodyVar != "" {
		b.WriteString(", " + bodyVar)
	}
	b.WriteString(")\n")
	b.WriteString("print(response.status_code)\nprint(response.text)")
	return b.String()
}

// pyQuote renders s as a Python single-quoted string literal. Written by
// hand rather than reusing jsonQuote's JSON-style escaping: JSON must
// escape every double quote regardless of which quote character wraps the
// result, but Python's single-quoted strings only need '\” and '\\'
// escaped — reusing jsonQuote would produce technically-valid but needlessly
// noisy output like '{\"foo\":\"bar\"}' instead of the natural '{"foo":"bar"}'.
func pyQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

func pyHeaderEntries(spec RequestSpec) ([][2]string, bool) {
	var entries [][2]string
	hasContentType := false
	for _, h := range spec.Headers {
		if h.Disabled {
			continue
		}
		if strings.EqualFold(h.Key, contentTypeHeader) {
			hasContentType = true
		}
		entries = append(entries, [2]string{h.Key, h.Value})
	}
	return entries, hasContentType
}

func pyBodyLine(spec RequestSpec, hasContentType bool, headerEntries *[][2]string) (string, string) {
	switch spec.BodyMode {
	case "none":
		return "", ""
	case "urlencoded", "formdata":
		var b strings.Builder
		b.WriteString("data = {\n")
		for _, f := range spec.FormFields {
			if f.Disabled {
				continue
			}
			b.WriteString("    " + pyQuote(f.Key) + ": " + pyQuote(f.Value) + ",\n")
		}
		b.WriteString("}\n")
		return b.String(), "data=data"
	default: // "" — raw
		if spec.Body == "" {
			return "", ""
		}
		if !hasContentType {
			*headerEntries = append(*headerEntries, [2]string{contentTypeHeader, rawContentType(spec.RawContentType)})
		}
		return "data = " + pyQuote(spec.Body) + "\n", "data=data"
	}
}

const (
	bearerPrefix      = "Bearer "
	goHeaderSetPrefix = "req.Header.Set("
	goURLEncodedCT    = "application/x-www-form-urlencoded"
)

// ToGo renders a RequestSpec as a copy-pasteable Go program using only the
// standard library net/http — the fourth "copy as code" language alongside
// curl/JS/Python, following the same {{var}}-left-as-is convention (a
// snippet is meant to be portable, not pre-resolved against whatever
// environment happened to be active when it was copied).
func ToGo(spec RequestSpec) string {
	headerEntries, hasContentType := pyHeaderEntries(spec) // key/value extraction is language-agnostic — reuse as-is
	if spec.BodyMode == "formdata" {
		// A multipart Content-Type is only valid together with the exact
		// boundary mw.FormDataContentType() generates at runtime — any
		// user-supplied Content-Type header is unconditionally overwritten
		// by goFormDataBody's own req.Header.Set(...) further down (Go's
		// Header.Set replaces, so the request itself was never actually
		// broken by this), but emitting BOTH lines in the generated snippet
		// looked like a bug (two conflicting Content-Type sets) rather than
		// the harmless dead code it actually was. Dropped here instead, so
		// the generated code has exactly one Content-Type line, matching
		// what the request will really send.
		headerEntries = dropHeader(headerEntries, contentTypeHeader)
	}
	basicAuthLine := goAuthParts(spec.Auth, &headerEntries)
	decl, bodyExpr, contentTypeLine, imports := goBodyParts(spec, hasContentType)

	var b strings.Builder
	b.WriteString("package main\n\nimport (\n")
	for _, imp := range imports {
		b.WriteString("\t" + jsonQuote(imp) + "\n")
	}
	b.WriteString(")\n\nfunc main() {\n")
	b.WriteString(decl)
	b.WriteString("\treq, err := http.NewRequest(" + jsonQuote(strings.ToUpper(spec.Method)) + ", " + jsonQuote(curlFullURL(spec)) + ", " + bodyExpr + ")\n")
	b.WriteString("\tif err != nil {\n\t\tpanic(err)\n\t}\n")
	for _, h := range headerEntries {
		b.WriteString("\t" + goHeaderSetPrefix + jsonQuote(h[0]) + ", " + jsonQuote(h[1]) + ")\n")
	}
	if contentTypeLine != "" {
		b.WriteString("\t" + contentTypeLine + "\n")
	}
	if basicAuthLine != "" {
		b.WriteString("\t" + basicAuthLine)
	}
	b.WriteString("\n\tclient := &http.Client{}\n")
	b.WriteString("\tresp, err := client.Do(req)\n")
	b.WriteString("\tif err != nil {\n\t\tpanic(err)\n\t}\n")
	b.WriteString("\tdefer resp.Body.Close()\n\n")
	b.WriteString("\tbody, _ := io.ReadAll(resp.Body)\n")
	b.WriteString("\tfmt.Println(resp.StatusCode)\n")
	b.WriteString("\tfmt.Println(string(body))\n")
	b.WriteString("}")
	return b.String()
}

// goAuthParts folds Auth into either an appended header entry (Bearer,
// header-mode API key) or a returned req.SetBasicAuth(...) statement —
// basic auth has a dedicated net/http method, so it's more idiomatic Go
// than a manually-built Authorization header line.
func goAuthParts(auth *Auth, headerEntries *[][2]string) string {
	if auth == nil {
		return ""
	}
	switch auth.Type {
	case AuthBearer:
		if auth.Token != "" {
			*headerEntries = append(*headerEntries, [2]string{"Authorization", bearerPrefix + auth.Token})
		}
	case AuthBasic:
		return "req.SetBasicAuth(" + jsonQuote(auth.Username) + ", " + jsonQuote(auth.Password) + ")\n"
	case AuthAPIKey:
		if auth.KeyName != "" && auth.AddTo == "header" {
			*headerEntries = append(*headerEntries, [2]string{auth.KeyName, auth.KeyValue})
		}
	}
	return ""
}

// goBodyParts returns the request-body setup statements (already indented
// for main()'s body, "" when there's nothing to declare), the expression
// passed as http.NewRequest's body argument, a req.Header.Set(...) line to
// append after the request is built when Go itself computes the
// Content-Type (a multipart boundary can't be a fixed string), or "" if
// none is needed, and exactly the stdlib imports the returned snippet
// actually references — a copy-pasted Go program with an unused import
// fails to compile, so this can't just always emit a fixed import set the
// way the other three languages' generators do.
func goBodyParts(spec RequestSpec, hasContentType bool) (decl, bodyExpr, contentTypeLine string, imports []string) {
	base := []string{"fmt", "io", "net/http"}
	var extra []string
	switch spec.BodyMode {
	case "none":
		decl, bodyExpr = "", "nil"
	case "urlencoded":
		decl, bodyExpr, contentTypeLine = goURLEncodedBody(spec, hasContentType)
		extra = []string{"net/url", "strings"}
	case "formdata":
		decl, bodyExpr, contentTypeLine = goFormDataBody(spec)
		extra = []string{"bytes", "mime/multipart"}
	default: // "" — raw
		if spec.Body == "" {
			decl, bodyExpr = "", "nil"
			break
		}
		decl, bodyExpr, contentTypeLine = goRawBody(spec, hasContentType)
		extra = []string{"strings"}
	}
	imports = append(base, extra...)
	sort.Strings(imports)
	return decl, bodyExpr, contentTypeLine, imports
}

func goURLEncodedBody(spec RequestSpec, hasContentType bool) (decl, bodyExpr, contentTypeLine string) {
	var b strings.Builder
	b.WriteString("\tform := url.Values{}\n")
	for _, f := range spec.FormFields {
		if f.Disabled {
			continue
		}
		b.WriteString("\tform.Set(" + jsonQuote(f.Key) + ", " + jsonQuote(f.Value) + ")\n")
	}
	if !hasContentType {
		contentTypeLine = goHeaderSetPrefix + jsonQuote(contentTypeHeader) + ", " + jsonQuote(goURLEncodedCT) + ")"
	}
	return b.String(), "strings.NewReader(form.Encode())", contentTypeLine
}

func goFormDataBody(spec RequestSpec) (decl, bodyExpr, contentTypeLine string) {
	var b strings.Builder
	b.WriteString("\tvar buf bytes.Buffer\n\tmw := multipart.NewWriter(&buf)\n")
	for _, f := range spec.FormFields {
		if f.Disabled {
			continue
		}
		if f.Type == "file" {
			b.WriteString("\t// " + f.Key + " is a file field — write its bytes via mw.CreateFormFile(...) here\n")
			continue
		}
		b.WriteString("\tmw.WriteField(" + jsonQuote(f.Key) + ", " + jsonQuote(f.Value) + ")\n")
	}
	b.WriteString("\tmw.Close()\n")
	return b.String(), "&buf", goHeaderSetPrefix + jsonQuote(contentTypeHeader) + ", mw.FormDataContentType())"
}

func goRawBody(spec RequestSpec, hasContentType bool) (decl, bodyExpr, contentTypeLine string) {
	if !hasContentType {
		contentTypeLine = goHeaderSetPrefix + jsonQuote(contentTypeHeader) + ", " + jsonQuote(rawContentType(spec.RawContentType)) + ")"
	}
	return "", "strings.NewReader(" + jsonQuote(spec.Body) + ")", contentTypeLine
}

var pyRequestsMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
}

// pyMethodCall returns everything between "requests." and the "url" arg,
// including the opening paren — e.g. "get(" or "request('PURGE', " — so the
// one call site can just concatenate requests. + pyMethodCall(...) + "url".
func pyMethodCall(method string) string {
	m := strings.ToUpper(method)
	if pyRequestsMethods[m] {
		return strings.ToLower(m) + "("
	}
	return "request(" + pyQuote(m) + ", "
}
