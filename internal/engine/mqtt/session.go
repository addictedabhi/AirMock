package mqttengine

import (
	"bufio"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
)

// session tracks the state a connection accumulates: which topic filters
// it has subscribed to, so a broadcast reply knows whether to deliver to
// it. writeMu serializes writes since a broadcast (from another goroutine
// handling a different session's PUBLISH) and this session's own handling
// loop can both write to the same connection concurrently.
//
// regID/mockID/reg let the session update its own registry entry (bumping
// activity/filters metadata) from wherever it's handled, without every
// call site needing to separately track and pass its own session id.
type session struct {
	conn    net.Conn
	writeMu sync.Mutex

	subMu   sync.Mutex
	filters []string

	reg    *sessreg.Registry[*session]
	mockID string
	regID  string
}

// syncFiltersMeta refreshes this session's registry Meta["filters"] to
// match its current subscription list — called after every
// subscribe/unsubscribe so "Connected sessions" always shows what a
// client is actually subscribed to right now, not just at connect time.
func (s *session) syncFiltersMeta() {
	s.subMu.Lock()
	filters := strings.Join(s.filters, ", ")
	s.subMu.Unlock()
	s.reg.UpdateMeta(s.mockID, s.regID, map[string]string{"filters": filters})
}

func (s *session) writeRaw(b []byte) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.conn.Write(b)
	s.reg.Touch(s.mockID, s.regID, 0, 1)
}

func (s *session) addFilter(f string) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.filters = append(s.filters, f)
}

func (s *session) removeFilter(f string) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	out := s.filters[:0]
	for _, existing := range s.filters {
		if existing != f {
			out = append(out, existing)
		}
	}
	s.filters = out
}

func (s *session) subscribedTo(topic string) bool {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, f := range s.filters {
		if topicMatchesFilter(topic, f) {
			return true
		}
	}
	return false
}

// handleConn runs for the life of one connection: expects CONNECT first,
// then services SUBSCRIBE/UNSUBSCRIBE/PUBLISH/PINGREQ/DISCONNECT until the
// client disconnects.
func (e *Engine) handleConn(conn net.Conn, def *mock.Definition) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	first, err := readPacket(reader)
	if err != nil || first.packetType != packetConnect {
		return // not a valid MQTT client; nothing to do but drop it
	}
	connectInfo, err := decodeConnect(first.body)
	if err != nil {
		return
	}
	if _, err := conn.Write(encodeConnAck()); err != nil {
		return
	}

	sess := &session{conn: conn, reg: e.sessReg, mockID: def.ID}
	sess.regID = e.sessReg.Add(def.ID, "mqtt", sess, conn.RemoteAddr().String(), map[string]string{"clientId": connectInfo.clientID})
	defer e.sessReg.Remove(def.ID, sess.regID)

	for {
		pkt, err := readPacket(reader)
		if err != nil {
			return
		}
		if !e.handlePacket(sess, def, pkt) {
			return
		}
	}
}

// handlePacket dispatches one packet, returning false when the connection
// should be closed (DISCONNECT, or a write failure).
func (e *Engine) handlePacket(sess *session, def *mock.Definition, pkt *rawPacket) bool {
	sess.reg.Touch(sess.mockID, sess.regID, 1, 0)
	switch pkt.packetType {
	case packetPingReq:
		sess.writeRaw(encodePingResp())
		return true
	case packetSubscribe:
		return e.handleSubscribe(sess, pkt)
	case packetUnsubscribe:
		return e.handleUnsubscribe(sess, pkt)
	case packetPublish:
		return e.handlePublish(sess, def, pkt)
	case packetDisconnect:
		return false
	default:
		return true // ignore anything else rather than dropping the connection
	}
}

func (e *Engine) handleSubscribe(sess *session, pkt *rawPacket) bool {
	sub, err := decodeSubscribe(pkt.body)
	if err != nil {
		return false
	}
	for _, f := range sub.filters {
		sess.addFilter(f)
	}
	sess.syncFiltersMeta()
	sess.writeRaw(encodeSubAck(sub.packetID, len(sub.filters)))
	return true
}

func (e *Engine) handleUnsubscribe(sess *session, pkt *rawPacket) bool {
	sub, err := decodeUnsubscribe(pkt.body)
	if err != nil {
		return false
	}
	for _, f := range sub.filters {
		sess.removeFilter(f)
	}
	sess.syncFiltersMeta()
	sess.writeRaw(encodeUnsubAck(sub.packetID))
	return true
}

func (e *Engine) handlePublish(sess *session, def *mock.Definition, pkt *rawPacket) bool {
	pub, err := decodePublish(pkt.flags, pkt.body)
	if err != nil {
		return false
	}
	if pub.qos > 0 {
		sess.writeRaw(encodePubAck(pub.packetID))
	}

	replyTopic, replyPayload := evaluateMQTTRules(def.MQTT, pub.topic, string(pub.payload), def.ID, e.dynamicValues)
	// Record before broadcasting: the reply write is what a test/caller
	// actually synchronizes on (they can observe it arriving), so anything
	// this goroutine still needs to do — like logging the hit — must
	// happen first to guarantee it's visible by the time the reply lands.
	e.recordHit(def, pub.topic, pub.payload, replyTopic, replyPayload)
	if replyTopic == "" {
		return true
	}

	// def.Fault previously only did anything for REST/SOAP/GraphQL mocks —
	// applied here as "suppress the reply" rather than dropping the whole
	// connection, since an MQTT reply is a broadcast to OTHER subscribed
	// sessions, not a direct response to this one — severing this
	// publisher's connection over one flaky reply would disproportionately
	// take out every later publish on the same connection too.
	switch mock.RollFault(def.Fault) {
	case "timeout", "error":
		return true
	}
	if def.Fault != nil && def.Fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(def.Fault.LatencyJitterMs))
	}
	e.broadcastToSubscribers(def.ID, replyTopic, []byte(replyPayload))
	return true
}

// evaluateMQTTRules is first-match-wins over cfg.Rules: a rule matches when
// its TopicPattern matches the published topic AND (if PayloadMatch is set)
// the payload also matches. The matched rule's ReplyTopic/ReplyPayload are
// rendered via text/template+sprig with {{.Request.Body}} bound to the
// received payload before being returned.
func evaluateMQTTRules(cfg *mock.MQTTConfig, topic, payload, ownerID string, dv mock.DynamicValueSource) (replyTopic, replyPayload string) {
	for _, r := range cfg.Rules {
		if !topicMatchesFilter(topic, r.TopicPattern) {
			continue
		}
		if r.PayloadMatch != "" && !mqttPayloadMatches(r, payload) {
			continue
		}
		if r.ReplyTopic == "" {
			return "", ""
		}
		reqCtx := mock.RequestContext{Body: payload, BodyBytes: []byte(payload)}
		rendered, err := mock.RenderBody(r.ReplyPayload, reqCtx, mock.RenderOptions{OwnerID: ownerID, DynamicValues: dv})
		if err != nil {
			rendered = r.ReplyPayload
		}
		return r.ReplyTopic, rendered
	}
	return "", ""
}

func mqttPayloadMatches(r mock.MQTTRule, payload string) bool {
	switch r.MatchType {
	case "exact":
		return payload == r.PayloadMatch
	case "regex":
		matched, err := regexp.MatchString(r.PayloadMatch, payload)
		return err == nil && matched
	default: // "contains"
		return strings.Contains(payload, r.PayloadMatch)
	}
}
