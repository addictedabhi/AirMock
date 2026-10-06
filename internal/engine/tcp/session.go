package tcpengine

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/addictedabhi/airmock/internal/mock"
)

const defaultLoginAttempts = 3

// handleConn runs for the life of one connection: an optional banner, an
// optional login gate, then a read-a-line/match/respond loop until the
// client disconnects, an interaction says to close, or the session idles
// past its configured timeout.
func (e *Engine) handleConn(conn net.Conn, def *mock.Definition) {
	defer conn.Close()
	cfg := def.TCP

	regID := e.sessReg.Add(def.ID, "tcp", conn, conn.RemoteAddr().String(), nil)
	defer e.sessReg.Remove(def.ID, regID)

	delim := cfg.LineDelimiter
	if delim == "" {
		delim = "\n"
	}

	if cfg.Banner != "" {
		if _, err := conn.Write([]byte(ensureLineEnding(cfg.Banner))); err != nil {
			return
		}
		e.sessReg.Touch(def.ID, regID, 0, 1)
	}

	reader := bufio.NewReader(conn)

	if cfg.Login != nil && !e.performLogin(conn, reader, def.ID, regID, cfg, delim) {
		return
	}

	e.interactionLoop(conn, reader, def, regID, delim)
}

// interactionLoop is the normal (post-login) read/match/respond cycle.
func (e *Engine) interactionLoop(conn net.Conn, reader *bufio.Reader, def *mock.Definition, regID, delim string) {
	cfg := def.TCP
	for {
		trimmed, readErr := readLine(conn, reader, delim, cfg.SessionTimeoutSecs)
		if trimmed == "" && readErr != nil {
			return
		}
		e.sessReg.Touch(def.ID, regID, 1, 0)
		if !e.handleTCPLine(conn, def, regID, trimmed, delim) || readErr != nil {
			return
		}
	}
}

// handleTCPLine matches trimmed against cfg's Interactions, writes the
// response (through fault injection), records the hit, and schedules any
// Async callback — reporting whether the connection is still usable
// (false: fault or a real write error means the caller should stop
// reading).
func (e *Engine) handleTCPLine(conn net.Conn, def *mock.Definition, regID, trimmed, delim string) bool {
	cfg := def.TCP
	start := time.Now()
	resp, closeAfter, matched := matchTCPInteraction(cfg, trimmed, def.ID, e.dynamicValues)
	wrote := true
	if resp != "" {
		wrote = writeInteractionResponse(conn, cfg, def.Fault, resp, delim)
		if wrote {
			e.sessReg.Touch(def.ID, regID, 0, 1)
		}
	}
	if wrote {
		e.recordHit(def, trimmed, resp, time.Since(start).Milliseconds())
	}

	// Scheduled regardless of `wrote`: fault injection (timeout/error) only
	// ever affects the immediate in-connection response, never an Async
	// interaction's separate, out-of-band webhook/email delivery — matching
	// REST/SOAP/GraphQL's serveAsync, which unconditionally schedules its
	// callback after writeTemplatedResponseWithDefaultContentType regardless
	// of what fault injection did to the ack. Previously the caller returned
	// before ever reaching this scheduling check, so a TCP interaction with
	// both Fault and Async configured had its callback silently, permanently
	// dropped whenever the fault fired.
	if matched != nil && matched.Async != nil {
		e.scheduleTCPAsyncCallback(def, matched.Async, trimmed)
	}

	return wrote && !closeAfter
}

// writeInteractionResponse applies fault injection and the configured
// response delay (if any), then writes resp (terminated with delim if it
// doesn't already end with it), reporting whether the write succeeded.
// def.Fault previously only did anything for REST/SOAP/GraphQL mocks — a
// TCP mock's FaultConfig was silently never read at all, so a raw TCP
// session had no way to simulate the same latency/timeout chaos an HTTP
// mock could. "error" is treated the same as "timeout" here (connection
// dropped without a response) since a raw line-oriented protocol has no
// generic "status code" concept to substitute the way HTTP does.
func writeInteractionResponse(conn net.Conn, cfg *mock.TCPConfig, fault *mock.FaultConfig, resp, delim string) bool {
	switch mock.RollFault(fault) {
	case "timeout", "error":
		return false
	}
	if fault != nil && fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(fault.LatencyJitterMs))
	}
	if cfg.ResponseDelayMs > 0 {
		time.Sleep(time.Duration(cfg.ResponseDelayMs) * time.Millisecond)
	}
	_, err := conn.Write([]byte(ensureLineEnding(resp)))
	return err == nil
}

// tcpWriteEnding is the line ending ALWAYS appended after a Banner/
// Response/DefaultResponse/login success-or-failure message — deliberately
// NOT tied to LineDelimiter (which governs only how an INCOMING line is
// parsed/split, see readUntilDelimiter/trimDelimiter, and can be any custom
// value, e.g. "###", for a mock's own wire framing). These are two
// independent concerns on purpose: LineDelimiter is this mock's protocol
// framing choice, which might deliberately be something a real terminal
// would never render as a line break; a human at an interactive telnet
// session (or any client reading the raw bytes) still needs SOME real line
// break to see separate lines at all. There is no configuration knob for
// this — every human-facing TCP mock benefits from readable output, and a
// client parsing this mock's stream for its own custom delimiter simply
// keeps working the same way it already does, with one harmless extra CRLF
// alongside whatever delimiter it's actually looking for.
const tcpWriteEnding = "\r\n"

// ensureLineEnding normalizes s to end with exactly one copy of
// tcpWriteEnding — a Banner/Response/DefaultResponse/login success-or-
// failure message left without its own trailing line ending otherwise runs
// straight into whatever the client types next (or the next thing this
// mock writes) on the very same line, garbling terminal output like
// "LOGGED IN SUCCESSFULHello" into something unreadable. Login prompts
// (Username:/Password:) are deliberately NOT run through this — those are
// meant to sit right before the client's typed response on the same line,
// like a real login prompt.
//
// Strips any trailing CR/LF the configured text already carries (a very
// common authoring habit — typing a literal trailing "\n" into a Banner/
// Response field) before appending tcpWriteEnding, rather than only
// appending when NOT already present: a plain suffix check would have left
// a config authored with a bare "\n" doubled up into "...\n\r\n" instead of
// the single correct line ending.
func ensureLineEnding(s string) string {
	if s == "" {
		return s
	}
	return strings.TrimRight(s, "\r\n") + tcpWriteEnding
}

// performLogin exchanges a username/password check with the client up to
// Login.MaxAttempts times, returning whether the client authenticated.
// Login.Mode selects which shape that check takes — see loginAttempt
// (the default, interactive two-prompt sequence) vs lineLoginAttempt (a
// single custom login line, e.g. "LOGIN:admin:secret", for a scripted
// client's own login command).
func (e *Engine) performLogin(conn net.Conn, reader *bufio.Reader, mockID, regID string, cfg *mock.TCPConfig, delim string) bool {
	maxAttempts := cfg.Login.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultLoginAttempts
	}
	attempt := e.loginAttempt
	if cfg.Login.Mode == "line" {
		attempt = e.lineLoginAttempt
	}
	for i := 0; i < maxAttempts; i++ {
		ok, fatal := attempt(conn, reader, mockID, regID, cfg, delim)
		if fatal {
			return false
		}
		if ok {
			return true
		}
	}
	return false
}

// loginAttempt runs one username/password round. fatal means a read/write
// error occurred and the caller should stop retrying entirely, as opposed
// to ok=false which just means the credentials were wrong.
func (e *Engine) loginAttempt(conn net.Conn, reader *bufio.Reader, mockID, regID string, cfg *mock.TCPConfig, delim string) (ok, fatal bool) {
	login := cfg.Login

	if _, err := conn.Write([]byte(orDefault(login.UsernamePrompt, "Username: "))); err != nil {
		return false, true
	}
	e.sessReg.Touch(mockID, regID, 0, 1)
	username, err := readLine(conn, reader, delim, cfg.SessionTimeoutSecs)
	if err != nil {
		return false, true
	}
	e.sessReg.Touch(mockID, regID, 1, 0)

	if _, err := conn.Write([]byte(orDefault(login.PasswordPrompt, "Password: "))); err != nil {
		return false, true
	}
	e.sessReg.Touch(mockID, regID, 0, 1)
	password, err := readLine(conn, reader, delim, cfg.SessionTimeoutSecs)
	if err != nil {
		return false, true
	}
	e.sessReg.Touch(mockID, regID, 1, 0)

	if username == login.Username && password == login.Password {
		if login.SuccessMessage != "" {
			conn.Write([]byte(ensureLineEnding(login.SuccessMessage)))
			e.sessReg.Touch(mockID, regID, 0, 1)
		}
		return true, false
	}
	if login.FailureMessage != "" {
		conn.Write([]byte(ensureLineEnding(login.FailureMessage)))
		e.sessReg.Touch(mockID, regID, 0, 1)
	}
	return false, false
}

// lineLoginAttempt runs one custom single-line login round (Login.Mode ==
// "line"): optionally writes LineHint as a one-time hint (there's only one
// line to read here, so unlike interactive mode there's no second
// prompt), reads that one line, and matches it against Login.LineFormat to
// pull out a username/password pair. fatal means a read/write error
// occurred, same meaning as loginAttempt's.
func (e *Engine) lineLoginAttempt(conn net.Conn, reader *bufio.Reader, mockID, regID string, cfg *mock.TCPConfig, delim string) (ok, fatal bool) {
	login := cfg.Login

	if login.LineHint != "" {
		if _, err := conn.Write([]byte(login.LineHint)); err != nil {
			return false, true
		}
		e.sessReg.Touch(mockID, regID, 0, 1)
	}
	line, err := readLine(conn, reader, delim, cfg.SessionTimeoutSecs)
	if err != nil {
		return false, true
	}
	e.sessReg.Touch(mockID, regID, 1, 0)

	username, password, matched := parseLoginLine(login.LineFormat, line)
	if matched && username == login.Username && password == login.Password {
		if login.SuccessMessage != "" {
			conn.Write([]byte(ensureLineEnding(login.SuccessMessage)))
			e.sessReg.Touch(mockID, regID, 0, 1)
		}
		return true, false
	}
	if login.FailureMessage != "" {
		conn.Write([]byte(ensureLineEnding(login.FailureMessage)))
		e.sessReg.Touch(mockID, regID, 0, 1)
	}
	return false, false
}

// loginFormatPlaceholder matches the two placeholders a login line format
// may contain — {username} and {password} — so buildLoginLineRegex can
// walk a template left-to-right and tell literal text from a placeholder.
var loginFormatPlaceholder = regexp.MustCompile(`\{username\}|\{password\}`)

// buildLoginLineRegex turns a human-authored template like
// "LOGIN:{username}:{password}" into an anchored regular expression with
// named capture groups, without requiring the mock's author to know any
// regex syntax themselves. Literal text (everything outside the two
// placeholders) is matched via regexp.QuoteMeta, so punctuation in the
// format — the colons in that example — is treated literally rather than
// as regex metacharacters. Each placeholder becomes a non-greedy
// "(?P<name>.+?)" group; anchored with ^...$, non-greedy is what lets
// {username} stop at the next literal character in the template (e.g. the
// colon) rather than swallowing the rest of the line. format must contain
// exactly one {username} and one {password}, in either order — anything
// else is a configuration error.
func buildLoginLineRegex(format string) (*regexp.Regexp, error) {
	matches := loginFormatPlaceholder.FindAllStringIndex(format, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("login line format has no {username}/{password} placeholders")
	}

	var out strings.Builder
	out.WriteByte('^')
	var sawUsername, sawPassword bool
	last := 0
	for _, m := range matches {
		out.WriteString(regexp.QuoteMeta(format[last:m[0]]))
		switch format[m[0]:m[1]] {
		case "{username}":
			if sawUsername {
				return nil, fmt.Errorf("login line format must contain {username} exactly once")
			}
			sawUsername = true
			out.WriteString("(?P<username>.+?)")
		case "{password}":
			if sawPassword {
				return nil, fmt.Errorf("login line format must contain {password} exactly once")
			}
			sawPassword = true
			out.WriteString("(?P<password>.+?)")
		}
		last = m[1]
	}
	out.WriteString(regexp.QuoteMeta(format[last:]))
	out.WriteByte('$')

	if !sawUsername || !sawPassword {
		return nil, fmt.Errorf("login line format must contain both {username} and {password}")
	}
	return regexp.Compile(out.String())
}

// parseLoginLine extracts (username, password) from line using format, a
// plain-text template (see buildLoginLineRegex). A format that fails to
// build (missing/duplicated placeholders) or simply doesn't match line
// reports matched=false rather than panicking or erroring —
// lineLoginAttempt then treats that exactly like wrong credentials
// (another attempt, up to MaxAttempts), not a connection-ending error, so
// a misconfigured format or a mistyped login command behave the same way
// a real wrong password would.
func parseLoginLine(format, line string) (username, password string, matched bool) {
	re, err := buildLoginLineRegex(format)
	if err != nil {
		return "", "", false
	}
	m := re.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	for i, name := range re.SubexpNames() {
		switch name {
		case "username":
			username = m[i]
		case "password":
			password = m[i]
		}
	}
	return username, password, true
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// readLine reads up to delim (applying an idle read deadline first if
// timeoutSecs > 0) and returns the line with delim stripped.
func readLine(conn net.Conn, reader *bufio.Reader, delim string, timeoutSecs int) (string, error) {
	if timeoutSecs > 0 {
		conn.SetReadDeadline(time.Now().Add(time.Duration(timeoutSecs) * time.Second))
	}
	line, err := readUntilDelimiter(reader, delim)
	if err == nil {
		discardBufferedLineNoise(reader, delim)
	}
	return trimDelimiter(line, delim), err
}

// discardBufferedLineNoise drops a trailing CR/LF that's ALREADY sitting
// in reader's buffer right after a non-newline delim was found — e.g. a
// real telnet client (or any terminal in canonical line mode) sends "\r\n"
// the moment the user presses Enter, regardless of what mid-line
// delimiter (say ";") the mock is actually configured to split on. Without
// this, that "\r\n" is never consumed by the read that found ";" (delim
// isn't "\n", so readUntilDelimiter stops right at the ";") and instead
// sits at the front of the reader's buffer, silently prepended to the
// NEXT line read — turning a clean "PING" into "\r\nPING", which then
// fails an exact-match interaction that "PING" alone would have matched.
// A delimiter that's already newline-based (bare "\n", "\r\n", or "\r")
// has no such leftover: whatever the client's Enter key sends IS the
// delimiter being matched, so there's nothing extra to drain.
//
// Only bytes reader.Buffered() already reports as available are peeked/
// discarded — never enough to trigger a new network Read() — so an
// automated client that sends nothing after its own delimiter (no stray
// CRLF) is never blocked waiting for noise that will never arrive, and
// its genuinely-next message is never mistaken for noise and eaten.
func discardBufferedLineNoise(r *bufio.Reader, delim string) {
	if delim == "\n" || delim == "\r\n" || delim == "\r" {
		return
	}
	for r.Buffered() > 0 {
		b, err := r.Peek(1)
		if err != nil || (b[0] != '\r' && b[0] != '\n') {
			return
		}
		r.Discard(1)
	}
}

// readUntilDelimiter accumulates bytes until they end with delim (which may
// be multi-byte, e.g. "\r\n") — a single ReadString(byte) call can't express
// a multi-byte delimiter, which is what made a configured "\r\n" or any
// delimiter other than a literal single "\n" character silently not work.
func readUntilDelimiter(r *bufio.Reader, delim string) (string, error) {
	var buf bytes.Buffer
	dl := []byte(delim)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return buf.String(), err
		}
		buf.WriteByte(b)
		if bytes.HasSuffix(buf.Bytes(), dl) {
			return buf.String(), nil
		}
	}
}

func trimDelimiter(line, delim string) string {
	trimmed := strings.TrimSuffix(line, delim)
	if delim == "\n" {
		// Robustness against clients that send CRLF even when the
		// configured delimiter is plain LF.
		trimmed = strings.TrimSuffix(trimmed, "\r")
	}
	return trimmed
}

// matchTCPInteraction returns the rendered response, whether to close the
// connection after sending it, and the matched interaction itself (nil for
// DefaultResponse/no-match) so the caller can act on its Async config.
// First-match-wins over Interactions in declared order. A response that
// fails to render is treated as no response at all rather than sending a
// raw template string.
func matchTCPInteraction(cfg *mock.TCPConfig, line, ownerID string, dv mock.DynamicValueSource) (string, bool, *mock.TCPInteraction) {
	opts := mock.RenderOptions{OwnerID: ownerID, DynamicValues: dv}
	for i := range cfg.Interactions {
		it := &cfg.Interactions[i]
		if !tcpInteractionMatches(*it, line) {
			continue
		}
		resp, err := mock.RenderBody(it.Response, mock.RequestContext{Body: line}, opts)
		if err != nil {
			return "", it.CloseAfter, it
		}
		return resp, it.CloseAfter, it
	}
	if cfg.DefaultResponse == "" {
		return "", false, nil
	}
	resp, err := mock.RenderBody(cfg.DefaultResponse, mock.RequestContext{Body: line}, opts)
	if err != nil {
		return "", false, nil
	}
	return resp, false, nil
}

func tcpInteractionMatches(it mock.TCPInteraction, line string) bool {
	switch it.MatchType {
	case "exact":
		return line == it.Match
	case "regex":
		matched, err := regexp.MatchString(it.Match, line)
		return err == nil && matched
	default: // "contains"
		return strings.Contains(line, it.Match)
	}
}

// scheduleTCPAsyncCallback mirrors httpengine's serveAsync (target
// resolution, optional named-email-template indirection, template
// rendering, then persisting a durable job) for a TCP interaction that
// matched with Async set — the same HTTP-webhook-or-email delivery any
// REST async mock uses, just triggered by a line of TCP input instead of
// an HTTP request. BodyBytes is set to the raw line so a "body.field"
// extraction path still works when the line itself is JSON; RequestContext
// has no headers/query/path-params to offer here since raw TCP has none.
func (e *Engine) scheduleTCPAsyncCallback(def *mock.Definition, cfg *mock.AsyncConfig, line string) {
	if e.scheduler == nil {
		log.Printf("airmock: tcp mock %q interaction is async but no callback scheduler is configured; callback dropped", def.Name)
		return
	}
	reqCtx := mock.RequestContext{Body: line, BodyBytes: []byte(line)}

	target, err := mock.ResolveCallbackTarget(cfg, reqCtx)
	if err != nil {
		log.Printf("airmock: tcp mock %q: resolve callback target: %v", def.Name, err)
		return
	}

	subjectTemplate, bodyTemplate, _, terr := mock.ResolveEmailContent(cfg, e.emailTemplates)
	if terr != nil {
		log.Printf("airmock: tcp mock %q references email template %q which failed to resolve, falling back to its own inline subject/body: %v", def.Name, cfg.EmailTemplateID, terr)
		subjectTemplate, bodyTemplate = cfg.EmailSubjectTemplate, cfg.CallbackBodyTemplate
	}

	payload, err := mock.RenderBody(bodyTemplate, reqCtx, mock.RenderOptions{OwnerID: def.ID, DynamicValues: e.dynamicValues})
	if err != nil {
		log.Printf("airmock: tcp mock %q: callback body template error: %v", def.Name, err)
		return
	}
	payload = mock.DefaultCallbackPayload(cfg, bodyTemplate, payload)
	subject := ""
	if cfg.CallbackChannel == "email" {
		if subject, err = mock.RenderBody(subjectTemplate, reqCtx, mock.RenderOptions{OwnerID: def.ID, DynamicValues: e.dynamicValues}); err != nil {
			log.Printf("airmock: tcp mock %q: email subject template error: %v", def.Name, err)
			return
		}
	}

	method := cfg.CallbackMethod
	if method == "" {
		method = "POST"
	}
	err = e.scheduler.ScheduleCallback(def.ID, mock.CallbackRequest{
		TargetURL:   target,
		Method:      method,
		Headers:     cfg.CallbackHeaders,
		Payload:     payload,
		DelayMs:     cfg.CallbackDelayMs,
		MaxAttempts: cfg.MaxAttempts,
		Channel:     cfg.CallbackChannel,
		Subject:     subject,
	})
	if err != nil {
		log.Printf("airmock: failed to schedule tcp callback for mock %q: %v", def.Name, err)
	}
}
