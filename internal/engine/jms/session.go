package jmsengine

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
)

// linkState tracks the one piece of information this mock needs about an
// attached link: which role the CLIENT declared (sender = it produces
// messages to us; receiver = it wants to consume messages from us), the
// address involved (Target for a producer link, Source for a consumer
// link), and — for a consumer link only — how much credit the client has
// granted us to deliver on it.
type linkState struct {
	clientIsReceiver bool // true: client consumes from us (we are sender); false: client produces to us (we are receiver)
	address          string
	credit           uint32
}

// connState is the per-connection state this engine tracks: a single
// AMQP session per connection (this mock never needs more than one), its
// channel number, and every currently-attached link keyed by the client's
// own handle number.
type connState struct {
	conn    net.Conn
	writeMu sync.Mutex

	mu             sync.Mutex
	channel        uint16
	links          map[uint32]*linkState
	nextDeliveryID uint32
	nextIncomingID uint32 // the transfer-id this mock expects the peer's NEXT transfer to carry

	reg    *sessreg.Registry[*connState]
	mockID string
	regID  string
}

func (cs *connState) currentNextIncomingID() uint32 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.nextIncomingID
}

func (cs *connState) writeFrame(body []byte) error {
	cs.writeMu.Lock()
	err := writeFrame(cs.conn, cs.channel, body, nil)
	cs.writeMu.Unlock()
	if err == nil {
		cs.reg.Touch(cs.mockID, cs.regID, 0, 1)
	}
	return err
}

func (cs *connState) writeTransfer(handle uint32, payload string) error {
	cs.mu.Lock()
	deliveryID := cs.nextDeliveryID
	cs.nextDeliveryID++
	cs.mu.Unlock()

	body := encodeTransfer(handle, deliveryID, true)
	cs.writeMu.Lock()
	err := writeFrame(cs.conn, cs.channel, body, encodeDataBody(payload))
	cs.writeMu.Unlock()
	if err == nil {
		cs.reg.Touch(cs.mockID, cs.regID, 0, 1)
	}
	return err
}

// syncLinksMeta refreshes this connection's registry Meta["links"] to
// describe every currently-attached link — called after every
// attach/flow/detach so "Connected sessions" always shows what a client is
// actually attached to right now, not just at connect time.
func (cs *connState) syncLinksMeta() {
	cs.mu.Lock()
	parts := make([]string, 0, len(cs.links))
	for _, ls := range cs.links {
		role := "producer"
		if ls.clientIsReceiver {
			role = fmt.Sprintf("consumer(credit=%d)", ls.credit)
		}
		parts = append(parts, fmt.Sprintf("%s:%s", role, ls.address))
	}
	cs.mu.Unlock()
	cs.reg.UpdateMeta(cs.mockID, cs.regID, map[string]string{"links": strings.Join(parts, ", ")})
}

// handleConn runs for the life of one connection: exchanges the AMQP
// protocol header (no SASL — this mock accepts any client outright), then
// services open/begin/attach/flow/transfer/disposition/detach/end/close
// until the client disconnects or closes.
func (e *Engine) handleConn(conn net.Conn, def *mock.Definition) {
	defer conn.Close()

	if err := readProtocolHeader(conn); err != nil {
		return
	}
	if _, err := conn.Write(protocolHeader[:]); err != nil {
		return
	}

	cs := &connState{conn: conn, links: map[uint32]*linkState{}, reg: e.sessReg, mockID: def.ID}
	cs.regID = e.sessReg.Add(def.ID, "jms", cs, conn.RemoteAddr().String(), nil)
	defer e.sessReg.Remove(def.ID, cs.regID)

	for {
		fr, err := readFrame(conn)
		if err != nil {
			return
		}
		if fr.descriptor == 0 {
			continue // empty frame (heartbeat) — nothing to dispatch
		}
		e.sessReg.Touch(def.ID, cs.regID, 1, 0)
		if !e.dispatch(cs, def, fr) {
			return
		}
	}
}

// dispatch handles one decoded frame, returning false when the connection
// should be closed (after a Close, or a write failure).
func (e *Engine) dispatch(cs *connState, def *mock.Definition, fr *frame) bool {
	switch fr.descriptor {
	case descOpen:
		return cs.writeFrame(encodeOpen(originContainerID(def))) == nil
	case descBegin:
		cs.mu.Lock()
		cs.channel = fr.channel
		cs.nextIncomingID = asUint32(fieldAt(fr.fields, 1)) // peer's own next-outgoing-id
		cs.mu.Unlock()
		return cs.writeFrame(encodeBegin(fr.channel)) == nil
	case descAttach:
		return e.handleAttach(cs, fr)
	case descFlow:
		return e.handleFlow(cs, fr)
	case descTransfer:
		return e.handleTransfer(cs, def, fr)
	case descDisposition:
		return true // this mock always pre-settles its own sends; nothing to reconcile
	case descDetach:
		handle := asUint32(fieldAt(fr.fields, 0))
		cs.mu.Lock()
		delete(cs.links, handle)
		cs.mu.Unlock()
		cs.syncLinksMeta()
		return cs.writeFrame(encodeDetach(handle, true)) == nil
	case descEnd:
		return cs.writeFrame(encodeEnd()) == nil
	case descClose:
		cs.writeFrame(encodeClose())
		return false
	default:
		return true // unsupported/unknown performative — ignore rather than drop the connection
	}
}

func originContainerID(def *mock.Definition) string {
	if def.JMS != nil && def.Name != "" {
		return def.Name
	}
	return "airmock"
}

func (e *Engine) handleAttach(cs *connState, fr *frame) bool {
	name := asString(fieldAt(fr.fields, 0))
	handle := asUint32(fieldAt(fr.fields, 1))
	clientIsReceiver := asBool(fieldAt(fr.fields, 2))

	var address string
	if clientIsReceiver {
		address = addressOf(fieldAt(fr.fields, 5)) // source
	} else {
		address = addressOf(fieldAt(fr.fields, 6)) // target
	}

	cs.mu.Lock()
	cs.links[handle] = &linkState{clientIsReceiver: clientIsReceiver, address: address}
	cs.mu.Unlock()
	cs.syncLinksMeta()

	iAmSender := clientIsReceiver
	if err := cs.writeFrame(encodeAttach(name, handle, iAmSender, address)); err != nil {
		return false
	}

	if !clientIsReceiver {
		// The client is a producer: it can't transfer anything until we
		// (the receiving end of this link) grant it credit.
		if err := cs.writeFrame(encodeFlow(cs.currentNextIncomingID(), handle, 0, 1000)); err != nil {
			return false
		}
	}
	return true
}

func (e *Engine) handleFlow(cs *connState, fr *frame) bool {
	handleField := fieldAt(fr.fields, 4)
	if handleField == nil {
		return true // session-level flow only, no link to update
	}
	handle := asUint32(handleField)
	linkCredit := asUint32(fieldAt(fr.fields, 6))

	cs.mu.Lock()
	if ls := cs.links[handle]; ls != nil {
		ls.credit = linkCredit
	}
	cs.mu.Unlock()
	cs.syncLinksMeta()
	return true
}

func (e *Engine) handleTransfer(cs *connState, def *mock.Definition, fr *frame) bool {
	handle := asUint32(fieldAt(fr.fields, 0))
	deliveryID := asUint32(fieldAt(fr.fields, 1))
	settled := asBool(fieldAt(fr.fields, 4))

	cs.mu.Lock()
	ls := cs.links[handle]
	cs.nextIncomingID++
	cs.mu.Unlock()
	if ls == nil {
		return true // transfer on an unknown/already-detached link — nothing sane to do
	}

	body := decodeMessageBody(fr.payload)

	replyAddress, replyPayload := evaluateJMSRules(def.JMS, ls.address, body, def.ID, e.dynamicValues)
	// Record before delivering the reply — the reply itself becomes
	// observable to a consumer, which is what a test/caller actually
	// synchronizes on, so logging must happen first to guarantee it's
	// visible by the time that's possible.
	e.recordHit(def, ls.address, body, replyAddress, replyPayload)

	if !settled {
		if err := cs.writeFrame(encodeDispositionAccepted(deliveryID)); err != nil {
			return false
		}
	}

	if replyPayload == "" {
		return true
	}
	switch mock.RollFault(def.Fault) {
	case "timeout", "error":
		return true
	}
	if def.Fault != nil && def.Fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(def.Fault.LatencyJitterMs))
	}
	e.deliverToConsumers(def.ID, replyAddress, replyPayload)
	return true
}

// deliverToConsumers delivers one message to every currently-attached
// consumer link (on any connection to this mock) whose source address
// matches and that has available credit — mirroring the MQTT/Kafka
// engines' "reply on another channel/topic" pattern, adapted to AMQP
// links: a consumer with no credit granted simply doesn't receive it
// (this mock holds no durable queue behind an address — there's nothing
// to redeliver later once credit is granted).
func (e *Engine) deliverToConsumers(mockID, address, payload string) {
	if address == "" {
		return
	}
	for _, cs := range e.connsFor(mockID) {
		for _, h := range cs.consumerHandlesFor(address) {
			cs.writeTransfer(h, payload)
		}
	}
}

// consumerHandlesFor returns the handles of every consumer link matching
// address with credit available, decrementing each one's credit by one —
// the actual network write happens outside any lock, in the caller.
func (cs *connState) consumerHandlesFor(address string) []uint32 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var handles []uint32
	for handle, ls := range cs.links {
		if ls.clientIsReceiver && ls.address == address && ls.credit > 0 {
			ls.credit--
			handles = append(handles, handle)
		}
	}
	return handles
}

// evaluateJMSRules is first-match-wins over cfg.Rules: a rule matches when
// its AddressPattern equals the produced-to address (or is blank) AND (if
// PayloadMatch is set) the message body also matches. The matched rule's
// ReplyAddress/ReplyPayload are rendered via text/template+sprig with
// {{.Request.Body}} bound to the received body before being returned.
func evaluateJMSRules(cfg *mock.JMSConfig, address, body, ownerID string, dv mock.DynamicValueSource) (replyAddress, replyPayload string) {
	if cfg == nil {
		return "", ""
	}
	for _, r := range cfg.Rules {
		if r.AddressPattern != "" && r.AddressPattern != address {
			continue
		}
		if r.PayloadMatch != "" && !jmsPayloadMatches(r, body) {
			continue
		}
		if r.ReplyPayload == "" {
			return "", ""
		}
		reqCtx := mock.RequestContext{Body: body, BodyBytes: []byte(body)}
		rendered, err := mock.RenderBody(r.ReplyPayload, reqCtx, mock.RenderOptions{OwnerID: ownerID, DynamicValues: dv})
		if err != nil {
			rendered = r.ReplyPayload
		}
		return r.ReplyAddress, rendered
	}
	return "", ""
}

func jmsPayloadMatches(r mock.JMSRule, body string) bool {
	switch r.MatchType {
	case "exact":
		return body == r.PayloadMatch
	case "regex":
		matched, err := regexp.MatchString(r.PayloadMatch, body)
		return err == nil && matched
	default: // "contains"
		return strings.Contains(body, r.PayloadMatch)
	}
}
