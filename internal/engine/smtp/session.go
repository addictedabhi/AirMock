package smtpengine

import (
	"bufio"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/addictedabhi/airmock/internal/mock"
)

// okResponse is the plain "accepted, nothing more to say" reply shared by
// EHLO/MAIL FROM/RCPT TO/RSET/NOOP.
const okResponse = "250 OK\r\n"

// smtpSession holds the one piece of state a connection accumulates across
// commands — the envelope built up by MAIL FROM/RCPT TO — so handleLine can
// be a plain per-command dispatch instead of a single large stateful loop.
type smtpSession struct {
	engine   *Engine
	conn     net.Conn
	def      *mock.Definition
	hostname string
	from     string
	to       []string
	regID    string
	// refused counts recipients refused at RCPT TO time, so a DATA with no
	// accepted recipient left can be answered 554 instead of accepted.
	refused int
}

// write is writeLine plus bumping this session's registry activity/counter
// — the one place every outbound reply goes through, so "connected
// sessions" reflects real traffic instead of a static connect-time
// snapshot.
func (s *smtpSession) write(msg string) bool {
	ok := writeLine(s.conn, msg)
	if ok {
		s.engine.sessReg.Touch(s.def.ID, s.regID, 0, 1)
	}
	return ok
}

// handleConn runs for the life of one connection: a banner, then a normal
// SMTP command loop (EHLO/HELO, MAIL FROM, RCPT TO, DATA, RSET, NOOP, QUIT)
// until the client disconnects or sends QUIT. A single connection can send
// more than one message in sequence (RSET or a fresh MAIL FROM after a
// completed DATA both start a new envelope), matching how a real MTA
// connection is commonly reused.
func (e *Engine) handleConn(conn net.Conn, def *mock.Definition) {
	defer conn.Close()
	cfg := def.SMTP

	hostname := cfg.Hostname
	if hostname == "" {
		hostname = "airmock"
	}
	banner := cfg.Banner
	if banner == "" {
		banner = fmt.Sprintf("220 %s ESMTP AirMock", hostname)
	}
	regID := e.sessReg.Add(def.ID, "smtp", conn, conn.RemoteAddr().String(), nil)
	defer e.sessReg.Remove(def.ID, regID)

	if !writeLine(conn, banner+"\r\n") {
		return
	}
	e.sessReg.Touch(def.ID, regID, 0, 1)

	sess := &smtpSession{engine: e, conn: conn, def: def, hostname: hostname, regID: regID}
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		e.sessReg.Touch(def.ID, regID, 1, 0)
		if !sess.handleLine(reader, strings.TrimRight(line, "\r\n")) {
			return
		}
	}
}

// handleLine dispatches one command line, returning false once the
// connection should be closed (QUIT, or a write failure).
func (s *smtpSession) handleLine(reader *bufio.Reader, line string) bool {
	upper := strings.ToUpper(line)
	switch {
	case strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO"):
		return s.write(fmt.Sprintf("250 %s\r\n", s.hostname))
	case strings.HasPrefix(upper, "MAIL FROM:"):
		from := extractAddress(line[len("MAIL FROM:"):])
		if code, msg, refuse := envelopeVerdict(s.def.SMTP, "from", from); refuse {
			s.from, s.to, s.refused = "", nil, 0
			s.engine.recordHit(s.def, from, "", "", code, 0)
			return s.write(fmt.Sprintf("%d %s\r\n", code, msg))
		}
		s.from = from
		return s.write(okResponse)
	case strings.HasPrefix(upper, "RCPT TO:"):
		rcpt := extractAddress(line[len("RCPT TO:"):])
		if code, msg, refuse := envelopeVerdict(s.def.SMTP, "to", rcpt); refuse {
			s.refused++
			s.engine.recordHit(s.def, s.from, rcpt, "", code, 0)
			return s.write(fmt.Sprintf("%d %s\r\n", code, msg))
		}
		s.to = append(s.to, rcpt)
		return s.write(okResponse)
	case upper == "DATA":
		if len(s.to) == 0 && s.refused > 0 {
			return s.write("554 No valid recipients\r\n")
		}
		if !s.engine.handleData(s, reader) {
			return false
		}
		s.from, s.to, s.refused = "", nil, 0 // envelope resets once a message completes
		return true
	case upper == "RSET":
		s.from, s.to, s.refused = "", nil, 0
		return s.write(okResponse)
	case upper == "NOOP":
		return s.write(okResponse)
	case upper == "QUIT":
		s.write("221 Bye\r\n")
		return false
	default:
		return s.write("500 unrecognized command\r\n")
	}
}

func writeLine(conn net.Conn, s string) bool {
	_, err := conn.Write([]byte(s))
	return err == nil
}

// handleData reads the DATA block, evaluates it against the mock's rules,
// responds accept/reject, and records the hit. Returns false if the
// connection should be closed (a write failed).
func (e *Engine) handleData(sess *smtpSession, reader *bufio.Reader) bool {
	def := sess.def
	if !sess.write("354 Start mail input; end with <CRLF>.<CRLF>\r\n") {
		return false
	}
	start := time.Now()
	body, err := readDataBlock(reader)
	if err != nil {
		return false
	}
	subject := extractSubject(body)
	toJoined := strings.Join(sess.to, ", ")
	from := sess.from

	// def.Fault previously only did anything for REST/SOAP/GraphQL mocks —
	// an SMTP mock's FaultConfig was silently never read at all. "error" is
	// treated the same as "timeout" (connection dropped, no accept/reject
	// reply) rather than substituting one of ErrorStatusCodes as a fake SMTP
	// reply code — those are configured as HTTP-flavored status codes
	// elsewhere in the same FaultConfig, so reusing them here would produce
	// meaningless SMTP codes (e.g. 404) instead of a real 4xx/5xx.
	switch mock.RollFault(def.Fault) {
	case "timeout", "error":
		return false
	}
	if def.Fault != nil && def.Fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(def.Fault.LatencyJitterMs))
	}

	code, message := evaluateSMTPRules(def.SMTP, from, toJoined, subject, body)
	if !sess.write(fmt.Sprintf("%d %s\r\n", code, message)) {
		return false
	}
	e.recordHit(def, from, toJoined, body, code, time.Since(start).Milliseconds())
	return true
}

// envelopeVerdict applies the rules that can be decided before DATA: a
// "from" rule at MAIL FROM and a "to" rule at RCPT TO (one recipient at a
// time). The first rule for that field that matches decides: an accept rule
// lets the command through, a reject rule refuses it right there with the
// rule's code — so a client sees a recipient-stage refusal (and can carry on
// with other recipients) instead of a late failure after the whole body.
// "subject" and "body" rules still run after DATA.
func envelopeVerdict(cfg *mock.SMTPConfig, field, value string) (code int, message string, refuse bool) {
	for _, r := range cfg.Rules {
		if r.MatchField != field || !smtpRuleMatches(r, value) {
			continue
		}
		if r.Accept {
			return 0, "", false
		}
		code, message = resolveRuleResponse(r)
		return code, message, true
	}
	return 0, "", false
}

// extractAddress pulls the address out of a MAIL FROM:/RCPT TO: parameter,
// which real clients send angle-bracketed ("<user@example.com>") and
// sometimes followed by ESMTP parameters like " SIZE=1234" — both are
// tolerated here rather than requiring exact syntax.
func extractAddress(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "<"); i != -1 {
		if j := strings.Index(s, ">"); j != -1 && j > i {
			return s[i+1 : j]
		}
	}
	if i := strings.Index(s, " "); i != -1 {
		s = s[:i]
	}
	return s
}

// readDataBlock accumulates lines until the terminating "." line,
// undoing SMTP dot-stuffing (a leading ".." on a line means a literal
// single "." — RFC 5321 §4.5.2) along the way.
// maxDataBlockSize bounds a single message's accumulated DATA block. A
// client that never sends the terminating "." line (or just sends a huge
// message) would otherwise grow this accumulator unbounded for the life of
// the connection — matches the FTP engine's existing 10MB upload cap for
// the same class of risk; real test/mock email bodies are expected to be
// well under this.
const maxDataBlockSize = 10 << 20 // 10MB

func readDataBlock(reader *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return sb.String(), err
		}
		if line == ".\r\n" || line == ".\n" {
			break
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		if sb.Len()+len(line) > maxDataBlockSize {
			return sb.String(), fmt.Errorf("DATA block exceeds %d bytes", maxDataBlockSize)
		}
		sb.WriteString(line)
	}
	return sb.String(), nil
}

// extractSubject reads the Subject header out of the message headers
// (before the first blank line) — enough for rule-matching and hit-log
// display without a full MIME parse.
func extractSubject(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "" {
			break
		}
		if len(trimmed) >= 8 && strings.EqualFold(trimmed[:8], "subject:") {
			return strings.TrimSpace(trimmed[8:])
		}
	}
	return ""
}

// evaluateSMTPRules is first-match-wins over cfg.Rules; DefaultAccept
// decides the outcome when nothing matches.
func evaluateSMTPRules(cfg *mock.SMTPConfig, from, to, subject, body string) (code int, message string) {
	for _, r := range cfg.Rules {
		var field string
		switch r.MatchField {
		case "from":
			field = from
		case "to":
			field = to
		case "subject":
			field = subject
		case "body":
			field = body
		}
		if !smtpRuleMatches(r, field) {
			continue
		}
		return resolveRuleResponse(r)
	}
	if cfg.DefaultAccept {
		return 250, "OK"
	}
	return 550, "Rejected"
}

func resolveRuleResponse(r mock.SMTPRule) (code int, message string) {
	code = r.ResponseCode
	if code == 0 {
		if r.Accept {
			code = 250
		} else {
			code = 550
		}
	}
	message = r.ResponseMessage
	if message == "" {
		if r.Accept {
			message = "OK"
		} else {
			message = "Rejected"
		}
	}
	return code, message
}

func smtpRuleMatches(r mock.SMTPRule, field string) bool {
	switch r.MatchType {
	case "exact":
		return field == r.Match
	case "regex":
		matched, err := regexp.MatchString(r.Match, field)
		return err == nil && matched
	default: // "contains"
		return strings.Contains(field, r.Match)
	}
}
