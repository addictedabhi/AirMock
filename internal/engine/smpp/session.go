package smppengine

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fiorix/go-smpp/smpp/pdu"
	"github.com/fiorix/go-smpp/smpp/pdu/pdufield"

	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
)

// session tracks the one piece of state a connection accumulates: whether
// it's completed a bind_transceiver yet — submit_sm is only serviced once
// bound, matching how a real SMSC rejects traffic on an unbound session.
// writeMu serializes writes since a server-initiated deliver_sm (sent from
// the same goroutine handling a submit_sm) and any other write to this
// connection must not interleave their bytes on the wire. reg/mockID/regID
// let write/writeDeliverSM bump this session's own registry entry, the same
// pattern mqttengine's session.writeRaw uses.
type session struct {
	conn    net.Conn
	writeMu sync.Mutex
	bound   bool

	reg    *sessreg.Registry[*session]
	mockID string
	regID  string
}

func (s *session) write(body pdu.Body) error {
	s.writeMu.Lock()
	err := body.SerializeTo(s.conn)
	s.writeMu.Unlock()
	if err == nil {
		s.reg.Touch(s.mockID, s.regID, 0, 1)
	}
	return err
}

// nextMessageID is a process-wide counter for generating submit_sm_resp's
// message_id — real SMSCs return an opaque per-message identifier; a mock
// doesn't need it to mean anything beyond "unique enough to look real."
var nextMessageID uint64

func generateMessageID() string {
	return strconv.FormatUint(atomic.AddUint64(&nextMessageID, 1), 16)
}

// handleConn runs for the life of one connection: expects bind_transceiver
// first (like MQTT's CONNECT), then services submit_sm/enquire_link/unbind
// until the client disconnects or unbinds.
func (e *Engine) handleConn(conn net.Conn, def *mock.Definition) {
	defer conn.Close()

	sess := &session{conn: conn, reg: e.sessReg, mockID: def.ID}
	sess.regID = e.sessReg.Add(def.ID, "smpp", sess, conn.RemoteAddr().String(), nil)
	defer e.sessReg.Remove(def.ID, sess.regID)

	for {
		body, err := pdu.Decode(conn)
		if err != nil {
			return
		}
		e.sessReg.Touch(def.ID, sess.regID, 1, 0)
		if !e.handlePDU(sess, def, body) {
			return
		}
	}
}

// isResponseID reports whether id is a response-direction command (the
// high bit of the 32-bit command_id is set, per the SMPP spec) — used to
// silently absorb the deliver_sm_resp a client sends back for a
// server-initiated deliver_sm, which needs no further action.
func isResponseID(id pdu.ID) bool {
	return uint32(id)&0x80000000 != 0
}

// handlePDU dispatches one PDU, returning false when the connection should
// be closed (after an unbind, or a write failure).
func (e *Engine) handlePDU(sess *session, def *mock.Definition, body pdu.Body) bool {
	switch body.Header().ID {
	case pdu.BindTransceiverID:
		return e.handleBind(sess, def, body)
	case pdu.EnquireLinkID:
		return sess.write(pdu.NewEnquireLinkRespSeq(body.Header().Seq)) == nil
	case pdu.SubmitSMID:
		return e.handleSubmitSM(sess, def, body)
	case pdu.UnbindID:
		resp := pdu.NewUnbindResp()
		resp.Header().Seq = body.Header().Seq
		sess.write(resp)
		return false // a real SMSC expects the connection to close after unbind
	default:
		if isResponseID(body.Header().ID) {
			return true // e.g. deliver_sm_resp to our own server-initiated deliver_sm — nothing to do
		}
		// Anything else we don't implement (bind_transmitter/receiver,
		// submit_multi, query_sm, ...): NACK it rather than silently
		// dropping the connection, so a client that sent something we
		// don't support at least gets a defined error instead of a
		// mysterious disconnect.
		nack := pdu.NewGenericNACK()
		nack.Header().Seq = body.Header().Seq
		nack.Header().Status = 0x00000003 // invalid command id
		return sess.write(nack) == nil
	}
}

func (e *Engine) handleBind(sess *session, def *mock.Definition, body pdu.Body) bool {
	resp := pdu.NewBindTransceiverResp()
	resp.Header().Seq = body.Header().Seq

	cfg := def.SMPP
	systemID := fieldString(body, pdufield.SystemID)
	password := fieldString(body, pdufield.Password)
	if cfg != nil && cfg.SystemID != "" && systemID != cfg.SystemID {
		resp.Header().Status = 0x0000000F // invalid system id
		sess.write(resp)
		return false
	}
	if cfg != nil && cfg.Password != "" && password != cfg.Password {
		resp.Header().Status = 0x0000000E // invalid password
		sess.write(resp)
		return false
	}

	respondingSystemID := "AirMock"
	if cfg != nil && cfg.SystemID != "" {
		respondingSystemID = cfg.SystemID
	}
	resp.Fields().Set(pdufield.SystemID, respondingSystemID)
	sess.bound = true
	sess.reg.UpdateMeta(sess.mockID, sess.regID, map[string]string{"systemId": systemID})
	return sess.write(resp) == nil
}

func (e *Engine) handleSubmitSM(sess *session, def *mock.Definition, body pdu.Body) bool {
	resp := pdu.NewSubmitSMResp()
	resp.Header().Seq = body.Header().Seq

	if !sess.bound {
		resp.Header().Status = 0x00000004 // incorrect bind status for given command
		return sess.write(resp) == nil
	}

	destAddr := fieldString(body, pdufield.DestinationAddr)
	sourceAddr := fieldString(body, pdufield.SourceAddr)
	message := fieldString(body, pdufield.ShortMessage)

	resp.Fields().Set(pdufield.MessageID, generateMessageID())
	if err := sess.write(resp); err != nil {
		return false
	}

	replySource, replyMessage, matched := evaluateSMPPRules(def.SMPP, destAddr, message, def.ID, e.dynamicValues)
	if replySource == "" {
		replySource = destAddr
	}
	e.recordHit(def, destAddr, message, replyMessage, matched)
	if replyMessage == "" {
		return true
	}

	switch mock.RollFault(def.Fault) {
	case "timeout", "error":
		return true
	}
	if def.Fault != nil && def.Fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(def.Fault.LatencyJitterMs))
	}

	// A write error sending the (best-effort, unsolicited) reply shouldn't
	// itself tear down a connection that's otherwise fine — submit_sm_resp
	// already went out successfully above.
	sess.writeDeliverSM(nextDeliverSequence(), replySource, sourceAddr, replyMessage)
	return true
}

// fieldString reads a mandatory PDU field as a string, treating a missing
// field (e.g. one that failed to decode, or genuinely wasn't sent) as an
// empty string rather than a nil-pointer panic.
func fieldString(body pdu.Body, name pdufield.Name) string {
	f, ok := body.Fields()[name]
	if !ok || f == nil {
		return ""
	}
	return f.String()
}

// evaluateSMPPRules is first-match-wins over cfg.Rules: a rule matches when
// its DestAddrPattern equals the submitted destination_addr (or is blank)
// AND (if MessageMatch is set) the short_message also matches. The matched
// rule's ReplySourceAddr/ReplyMessage are rendered via text/template+sprig
// with {{.Request.Body}} bound to the received message before being
// returned. matched reports whether ANY rule matched, even one with no
// reply configured, so callers can tell "no rule matched" apart from
// "matched, but this rule sends no reply" for hit-logging purposes.
func evaluateSMPPRules(cfg *mock.SMPPConfig, destAddr, message, ownerID string, dv mock.DynamicValueSource) (replySourceAddr, replyMessage string, matched bool) {
	if cfg == nil {
		return "", "", false
	}
	for _, r := range cfg.Rules {
		if !destAddrMatches(ruleDestMatchType(r.DestAddrMatchType, r.MatchType, r.MessageMatch), r.DestAddrPattern, destAddr) {
			continue
		}
		if r.MessageMatch != "" && !smppMessageMatches(r, message) {
			continue
		}
		if r.ReplyMessage == "" {
			return "", "", true
		}
		reqCtx := mock.RequestContext{Body: message, BodyBytes: []byte(message)}
		rendered, err := mock.RenderBody(r.ReplyMessage, reqCtx, mock.RenderOptions{OwnerID: ownerID, DynamicValues: dv})
		if err != nil {
			rendered = r.ReplyMessage
		}
		return r.ReplySourceAddr, rendered, true
	}
	return "", "", false
}

func smppMessageMatches(r mock.SMPPRule, message string) bool {
	switch r.MatchType {
	case "exact":
		return message == r.MessageMatch
	case "regex":
		matched, err := regexp.MatchString(r.MessageMatch, message)
		return err == nil && matched
	default: // "contains"
		return strings.Contains(message, r.MessageMatch)
	}
}
