package apiclient

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/shlex"
)

// curlValueFlags maps a curl flag that takes a value to a handler applying
// that value to the RequestSpec under construction. Table-driven rather
// than a long switch/case, both to keep this readable and to keep
// ParseCurl's cognitive complexity down.
var curlValueFlags = map[string]func(spec *RequestSpec, state *curlParseState, value string){
	"-X": func(spec *RequestSpec, st *curlParseState, v string) {
		spec.Method = strings.ToUpper(v)
		st.methodExplicit = true
	},
	"--request": func(spec *RequestSpec, st *curlParseState, v string) {
		spec.Method = strings.ToUpper(v)
		st.methodExplicit = true
	},
	"-H":               addHeaderFromColonPair,
	"--header":         addHeaderFromColonPair,
	"-d":               collectData,
	"--data":           collectData,
	"--data-raw":       collectData,
	"--data-binary":    collectData,
	"--data-ascii":     collectData,
	"-u":               addBasicAuthHeader,
	"--user":           addBasicAuthHeader,
	"-F":               addFormField,
	"--form":           addFormField,
	"--data-urlencode": addURLEncodeField,
	"-b": func(spec *RequestSpec, _ *curlParseState, v string) {
		spec.Headers = append(spec.Headers, KV{Key: "Cookie", Value: v})
	},
	"--cookie": func(spec *RequestSpec, _ *curlParseState, v string) {
		spec.Headers = append(spec.Headers, KV{Key: "Cookie", Value: v})
	},
	"--url": func(spec *RequestSpec, st *curlParseState, v string) { st.url = v },
	"-A": func(spec *RequestSpec, _ *curlParseState, v string) {
		spec.Headers = append(spec.Headers, KV{Key: "User-Agent", Value: v})
	},
	"--user-agent": func(spec *RequestSpec, _ *curlParseState, v string) {
		spec.Headers = append(spec.Headers, KV{Key: "User-Agent", Value: v})
	},
}

// curlNoValueFlags are recognized but have no effect on the resulting
// RequestSpec (they only change curl's own CLI behavior). -k/--insecure is
// NOT here — see the dedicated case in ParseCurl — it used to be listed
// here too, silently discarded on import with no way to reproduce a
// self-signed-cert test after the fact.
var curlNoValueFlags = map[string]bool{
	"-s": true, "--silent": true,
	"-i": true, "--include": true, "-v": true, "--verbose": true,
	"-L": true, "--location": true,
}

type curlParseState struct {
	methodExplicit bool
	url            string
	forceGet       bool     // -G/--get: send any --data as query params on a GET, not a body on a POST
	dataValues     []string // raw --data/-d/etc. values, resolved into Body or Query once parsing finishes
}

func addHeaderFromColonPair(spec *RequestSpec, _ *curlParseState, raw string) {
	k, v, ok := strings.Cut(raw, ":")
	if ok {
		spec.Headers = append(spec.Headers, KV{Key: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
	}
}

// collectData defers -d/--data/etc. values rather than applying them
// immediately: whether they end up as the request Body (implying POST) or
// appended to Query (implying GET) depends on whether -G/--get is present
// anywhere in the command, which — since curl doesn't care about flag
// order — may not be known yet at the point a --data flag is parsed.
// Real curl also concatenates multiple --data occurrences with "&" into
// one body rather than the last one silently winning, which resolveData
// mirrors.
func collectData(_ *RequestSpec, st *curlParseState, v string) {
	st.dataValues = append(st.dataValues, v)
}

// resolveData applies the collected --data values once every token has
// been seen and forceGet is therefore known for certain.
func resolveData(spec *RequestSpec, st *curlParseState) {
	if len(st.dataValues) == 0 {
		return
	}
	if st.forceGet {
		if !st.methodExplicit {
			spec.Method = "GET"
		}
		for _, d := range st.dataValues {
			if d == "" {
				continue
			}
			key, value, _ := strings.Cut(d, "=")
			spec.Query = append(spec.Query, KV{Key: queryUnescape(key), Value: queryUnescape(value)})
		}
		return
	}
	body := strings.Join(st.dataValues, "&")
	if body == "" {
		// Real curl still forces POST here (the mere presence of --data
		// switches its default method, regardless of the value) — but an
		// entirely empty --data/-d is virtually always a vestigial
		// placeholder some other tool left behind on what's actually a GET
		// endpoint, not a deliberate "send an empty POST body", so this
		// importer deliberately diverges from real curl and leaves the
		// method alone rather than reproducing that specific gotcha.
		return
	}
	spec.Body = body
	if !st.methodExplicit {
		spec.Method = "POST"
	}
}

func addBasicAuthHeader(spec *RequestSpec, _ *curlParseState, userPass string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(userPass))
	spec.Headers = append(spec.Headers, KV{Key: "Authorization", Value: "Basic " + encoded})
}

// addFormField handles -F/--form, which used to fall through to the
// generic "unrecognized flag" case and silently lose every multipart field
// on import — a pasted curl command with a file upload (-F 'file=@a.pdf')
// or a plain form field imported with no visible error, no BodyMode set,
// and the fields just gone. A "@path" value is curl's own file-upload
// syntax; the actual bytes at that path aren't available to this importer
// (it only sees the command text), so the field is marked as a file with
// FileName set from the path and no content — the user re-attaches the
// real file once in the UI, same as any other file field.
func addFormField(spec *RequestSpec, _ *curlParseState, raw string) {
	spec.BodyMode = "formdata"
	key, value, _ := strings.Cut(raw, "=")
	if after, ok := strings.CutPrefix(value, "@"); ok {
		spec.FormFields = append(spec.FormFields, KV{Key: key, Type: "file", FileName: after})
		return
	}
	spec.FormFields = append(spec.FormFields, KV{Key: key, Value: value})
}

// addURLEncodeField handles --data-urlencode, which curl uses specifically
// for a urlencoded-body field (as opposed to -d/--data, which sends the raw
// joined string as-is) — this used to fall through to the generic
// "unrecognized flag" case, silently discarding the flag AND its value, so
// every field of a urlencoded-body request (and any request round-tripped
// through this app's own "copy as curl", which renders urlencoded bodies as
// --data-urlencode) was dropped entirely on import.
//
// Mirrors curl's own accepted forms: "name=content" (the common case) is
// split into a key/value field; "name@filename" reads the value from a file
// at send time, same simplification addFormField already applies to -F's
// own "@path" syntax (marked as a file field with no content, the user
// re-attaches the real file); anything else (bare "content", or a leading
// "=content" with the name stripped) is stored as a single value-only field
// rather than dropped.
func addURLEncodeField(spec *RequestSpec, st *curlParseState, raw string) {
	spec.BodyMode = "urlencoded"
	if !st.methodExplicit {
		spec.Method = "POST"
	}
	if key, value, ok := strings.Cut(raw, "="); ok {
		spec.FormFields = append(spec.FormFields, KV{Key: key, Value: value})
		return
	}
	if key, file, ok := strings.Cut(raw, "@"); ok {
		spec.FormFields = append(spec.FormFields, KV{Key: key, Type: "file", FileName: file})
		return
	}
	spec.FormFields = append(spec.FormFields, KV{Value: raw})
}

// curlLineContinuationRE matches a trailing line-continuation marker plus
// its newline: backslash (bash/zsh/sh, and what a browser devtools or
// Postman "Copy as cURL" export produces), caret (Windows cmd.exe), or
// backtick (PowerShell) — the three ways a real terminal or tool actually
// pastes a curl command across multiple lines, all joined into one before
// tokenizing since shlex has no concept of shell line continuation itself
// (it treats a bare trailing backslash as an escape for the newline that
// follows, merging it into the next token instead of removing both, which
// silently corrupted the URL and every flag after the first continued
// line).
var curlLineContinuationRE = regexp.MustCompile("[\\\\^`][ \t]*\r?\n")

func stripLineContinuations(cmd string) string {
	return curlLineContinuationRE.ReplaceAllString(cmd, " ")
}

// ParseCurl turns a curl command line into a RequestSpec — hand-rolled
// rather than a third-party curl-parsing library (most are unmaintained or
// JS-targeted), using shlex for shell-quote-aware tokenizing.
func ParseCurl(cmd string) (*RequestSpec, error) {
	tokens, err := shlex.Split(stripLineContinuations(cmd))
	if err != nil {
		return nil, fmt.Errorf("tokenize curl command: %w", err)
	}

	spec := &RequestSpec{Method: "GET"}
	state := &curlParseState{}

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case tok == "curl":
			continue
		case tok == "-G" || tok == "--get":
			state.forceGet = true
		case tok == "-k" || tok == "--insecure":
			spec.Insecure = true
		case curlValueFlags[tok] != nil:
			if i+1 < len(tokens) {
				i++
				curlValueFlags[tok](spec, state, tokens[i])
			}
		case curlNoValueFlags[tok]:
			continue
		case strings.HasPrefix(tok, "-"):
			// unrecognized flag: skip its value defensively if the next
			// token isn't itself a flag, to avoid misreading it as the URL
			if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
				i++
			}
		default:
			state.url = tok
		}
	}

	spec.URL = state.url
	splitQueryFromURL(spec)
	resolveData(spec, state)
	return spec, nil
}

// splitQueryFromURL moves query parameters out of a raw curl URL (which
// naturally has them embedded, e.g. "https://x.com/orders?status=pending")
// into Query, matching how every other part of this app treats query
// params as their own field distinct from the base URL (RequestSpec.Query,
// appendQuery in runner.go, the Query tab in the UI). Without this, an
// imported request showed the right data but all of it stuck unmanageable
// inside the URL string, with the Query tab silently empty.
//
// Splits on plain "?"/"&"/"=" rather than net/url.Parse: this app's URLs
// routinely contain {{var}} template placeholders, and hand-rolled
// splitting avoids any risk of url.Parse normalizing/re-encoding them
// during a round trip.
func splitQueryFromURL(spec *RequestSpec) {
	base, rawQuery, hasQuery := strings.Cut(spec.URL, "?")
	if !hasQuery {
		return
	}
	spec.URL = base
	for _, pair := range strings.Split(rawQuery, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		spec.Query = append(spec.Query, KV{Key: queryUnescape(key), Value: queryUnescape(value)})
	}
}

// queryUnescape decodes a query-string component, falling back to the
// original raw text if it isn't validly percent-encoded (e.g. a literal
// "{{var}}" template placeholder) rather than dropping the value.
func queryUnescape(s string) string {
	decoded, err := url.QueryUnescape(s)
	if err != nil {
		return s
	}
	return decoded
}

// ToCurl renders a RequestSpec back into a properly quoted curl command —
// the "copy as curl" action. -u/--user round-trips as an explicit
// Authorization: Basic header (see addBasicAuthHeader) rather than being
// reconstructed as -u, which is an equally valid, equally correct way to
// express the same request.
func ToCurl(spec RequestSpec) string {
	var b strings.Builder
	b.WriteString("curl -X ")
	b.WriteString(spec.Method)

	hasContentType := false
	for _, h := range spec.Headers {
		if h.Disabled {
			continue
		}
		if strings.EqualFold(h.Key, "Content-Type") {
			hasContentType = true
		}
		b.WriteString(" -H ")
		b.WriteString(shellQuote(h.Key + ": " + h.Value))
	}

	b.WriteString(curlAuthArgs(spec.Auth))
	b.WriteString(curlBodyArgs(spec, hasContentType))
	if spec.Insecure {
		b.WriteString(" -k")
	}

	b.WriteString(" ")
	b.WriteString(shellQuote(curlFullURL(spec)))
	return b.String()
}

// curlFullURL appends Query (previously silently dropped — a real gap, not
// just a style choice) and, for an apikey Auth added to the query string,
// that param too, so the copied command actually reproduces the request.
func curlFullURL(spec RequestSpec) string {
	fullURL := spec.URL
	parts := make([]string, 0, len(spec.Query)+1)
	for _, q := range spec.Query {
		if q.Disabled {
			continue
		}
		parts = append(parts, q.Key+"="+q.Value)
	}
	if spec.Auth != nil && spec.Auth.Type == AuthAPIKey && spec.Auth.AddTo == "query" && spec.Auth.KeyName != "" {
		parts = append(parts, spec.Auth.KeyName+"="+spec.Auth.KeyValue)
	}
	if len(parts) == 0 {
		return fullURL
	}
	sep := "?"
	if strings.Contains(fullURL, "?") {
		sep = "&"
	}
	return fullURL + sep + strings.Join(parts, "&")
}

func curlAuthArgs(auth *Auth) string {
	if auth == nil {
		return ""
	}
	switch auth.Type {
	case AuthBearer:
		if auth.Token == "" {
			return ""
		}
		return " -H " + shellQuote("Authorization: Bearer "+auth.Token)
	case AuthBasic:
		return " -u " + shellQuote(auth.Username+":"+auth.Password)
	case AuthAPIKey:
		if auth.AddTo == "header" && auth.KeyName != "" {
			return " -H " + shellQuote(auth.KeyName+": "+auth.KeyValue)
		}
	}
	return ""
}

// curlFormDataArgs renders every enabled form field as a -F argument. A
// file-type field references just its original filename (curl's own upload
// syntax needs a real path on disk; a mock request's file field only ever
// holds the file's base64 content plus its original name, not a path) — the
// user re-points it at wherever the file actually lives before running the
// copied command.
func curlFormDataArgs(fields []KV) string {
	var b strings.Builder
	for _, f := range fields {
		if f.Disabled {
			continue
		}
		b.WriteString(" -F ")
		if f.Type == "file" {
			b.WriteString(shellQuote(f.Key + "=@" + f.FileName))
		} else {
			b.WriteString(shellQuote(f.Key + "=" + f.Value))
		}
	}
	return b.String()
}

func curlBodyArgs(spec RequestSpec, hasContentType bool) string {
	switch spec.BodyMode {
	case "none":
		return ""
	case "urlencoded":
		var b strings.Builder
		for _, f := range spec.FormFields {
			if f.Disabled {
				continue
			}
			b.WriteString(" --data-urlencode ")
			b.WriteString(shellQuote(f.Key + "=" + f.Value))
		}
		return b.String()
	case "formdata":
		return curlFormDataArgs(spec.FormFields)
	default: // "" — raw
		if spec.Body == "" {
			return ""
		}
		var b strings.Builder
		if !hasContentType {
			b.WriteString(" -H ")
			b.WriteString(shellQuote("Content-Type: " + rawContentType(spec.RawContentType)))
		}
		b.WriteString(" --data-raw ")
		b.WriteString(shellQuote(spec.Body))
		return b.String()
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
