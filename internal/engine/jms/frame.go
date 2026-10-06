package jmsengine

import (
	"encoding/binary"
	"fmt"
	"io"
)

// AMQP performative/type descriptor codes (RFC-numbered in the AMQP 1.0
// spec) — see codec.go's package doc for how these were pinned down.
const (
	descOpen        = 0x10
	descBegin       = 0x11
	descAttach      = 0x12
	descFlow        = 0x13
	descTransfer    = 0x14
	descDisposition = 0x15
	descDetach      = 0x16
	descEnd         = 0x17
	descClose       = 0x18
	descSource      = 0x28
	descTarget      = 0x29
	descAccepted    = 0x24
)

// protocolHeader is the 8-byte AMQP protocol header both sides exchange
// before any frames flow: "AMQP", protocol id (0 = plain AMQP, no SASL),
// major, minor, revision.
var protocolHeader = [8]byte{'A', 'M', 'Q', 'P', 0, 1, 0, 0}

func readProtocolHeader(r io.Reader) error {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return err
	}
	if buf[0] != 'A' || buf[1] != 'M' || buf[2] != 'Q' || buf[3] != 'P' {
		return fmt.Errorf("jms: not an AMQP client (bad protocol header %x)", buf[:4])
	}
	// Protocol id is deliberately not enforced beyond "looks like AMQP" —
	// this mock only implements the plain (id=0) protocol, never SASL, so
	// there's nothing useful to negotiate on the id byte itself.
	return nil
}

// frame is one decoded AMQP frame: the channel it arrived on, the
// performative descriptor and its positional field list, and (transfer
// only) whatever payload bytes followed the performative in the same
// frame body.
type frame struct {
	channel    uint16
	descriptor uint64
	fields     []any
	payload    []byte
}

// readFrame reads one complete AMQP frame from r. DOFF is assumed to
// always be 2 (no extended header) — this mock never sends one and has no
// reason to expect a real client to either.
func readFrame(r io.Reader) (*frame, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(hdr[0:4])
	if size < 8 {
		return nil, fmt.Errorf("jms: invalid frame size %d", size)
	}
	channel := binary.BigEndian.Uint16(hdr[6:8])
	body := make([]byte, size-8)
	if len(body) > 0 {
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
	}
	if len(body) == 0 {
		// An empty frame body is a valid AMQP heartbeat — nothing to
		// dispatch, but not an error either.
		return &frame{channel: channel}, nil
	}

	d := newDecoder(body)
	v, err := d.readValue()
	if err != nil {
		return nil, fmt.Errorf("jms: decode frame body: %w", err)
	}
	desc, ok := v.(*described)
	if !ok {
		return nil, fmt.Errorf("jms: frame body is not a described performative (got %T)", v)
	}
	fields, _ := desc.Value.([]any)
	payload := body[len(body)-d.remaining():]
	return &frame{channel: channel, descriptor: desc.Descriptor, fields: fields, payload: payload}, nil
}

// writeFrame writes one AMQP frame: an 8-byte header (size patched in
// after encoding) followed by the described-list body and any extra
// payload bytes (transfer only).
func writeFrame(w io.Writer, channel uint16, body []byte, payload []byte) error {
	total := 8 + len(body) + len(payload)
	out := make([]byte, 8, total)
	binary.BigEndian.PutUint32(out[0:4], uint32(total))
	out[4] = 2 // doff
	out[5] = 0 // type: AMQP
	binary.BigEndian.PutUint16(out[6:8], channel)
	out = append(out, body...)
	out = append(out, payload...)
	_, err := w.Write(out)
	return err
}

// --- field helpers for positional described-list access ---

func fieldAt(fields []any, i int) any {
	if i < 0 || i >= len(fields) {
		return nil
	}
	return fields[i]
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asUint32(v any) uint32 {
	switch n := v.(type) {
	case uint64:
		return uint32(n)
	case int64:
		return uint32(n)
	}
	return 0
}

// addressOf reads the Address (field 0) out of a decoded Source or Target
// described value — the only field of either this mock ever looks at.
func addressOf(v any) string {
	d, ok := v.(*described)
	if !ok {
		return ""
	}
	fields, _ := d.Value.([]any)
	return asString(fieldAt(fields, 0))
}

func omitOr(present bool, f func(e *encoder)) field {
	if !present {
		return omittedField()
	}
	return fieldOf(f)
}

// encodeOpen builds an Open performative body. Only ContainerID is ever
// set — every other field (hostname, max-frame-size, channel-max, idle-
// timeout, locales, capabilities, properties) is left at its spec default
// by omitting it entirely.
func encodeOpen(containerID string) []byte {
	e := &encoder{}
	e.writeDescribedList(descOpen, []field{
		fieldOf(func(e *encoder) { e.writeString(containerID) }),
	})
	return e.buf
}

// encodeBegin builds a Begin performative body replying to a client's
// begin on the given remote channel.
func encodeBegin(remoteChannel uint16) []byte {
	e := &encoder{}
	e.writeDescribedList(descBegin, []field{
		fieldOf(func(e *encoder) { e.writeUint16(remoteChannel) }), // remote-channel
		fieldOf(func(e *encoder) { e.writeUint32(0) }),             // next-outgoing-id
		fieldOf(func(e *encoder) { e.writeUint32(2147483647) }),    // incoming-window
		fieldOf(func(e *encoder) { e.writeUint32(2147483647) }),    // outgoing-window
	})
	return e.buf
}

// encodeSourceOrTarget builds a Source (descAddr must be descSource) or
// Target (descAddr must be descTarget) described value carrying only an
// address — every other field (durability, expiry, filters, outcomes,
// capabilities, ...) is omitted.
func encodeSourceOrTarget(descAddr uint64, address string) field {
	return fieldOf(func(e *encoder) {
		e.writeDescribedList(descAddr, []field{
			fieldOf(func(e *encoder) { e.writeString(address) }),
		})
	})
}

// encodeAttach builds an Attach performative body. iAmSender is this
// mock's own role (the opposite of whatever the client declared).
func encodeAttach(name string, handle uint32, iAmSender bool, address string) []byte {
	e := &encoder{}
	fields := []field{
		fieldOf(func(e *encoder) { e.writeString(name) }),
		fieldOf(func(e *encoder) { e.writeUint32(handle) }),
		fieldOf(func(e *encoder) { e.writeBool(!iAmSender) }), // role: false=sender, true=receiver
		omittedField(), // snd-settle-mode: default (mixed)
		omittedField(), // rcv-settle-mode: default (first)
	}
	if iAmSender {
		fields = append(fields,
			encodeSourceOrTarget(descSource, address),
			omittedField(), // target
		)
	} else {
		fields = append(fields,
			omittedField(), // source
			encodeSourceOrTarget(descTarget, address),
		)
	}
	fields = append(fields,
		omittedField(), // unsettled
		omittedField(), // incomplete-unsettled
	)
	if iAmSender {
		fields = append(fields, fieldOf(func(e *encoder) { e.writeUint32(0) })) // initial-delivery-count
	} else {
		fields = append(fields, omittedField())
	}
	e.writeDescribedList(descAttach, fields)
	return e.buf
}

// encodeFlow builds a Flow performative body granting linkCredit more
// transfers on the given handle. next-incoming-id MUST be set once a
// session is established (which, in this engine, is always true by the
// time a Flow is ever sent) — a real client library rejects a Flow that
// omits it as a protocol error. Session-level window fields are set
// generously since this mock never actually enforces session-level flow
// control, only per-link credit.
func encodeFlow(nextIncomingID, handle, deliveryCount, linkCredit uint32) []byte {
	e := &encoder{}
	e.writeDescribedList(descFlow, []field{
		fieldOf(func(e *encoder) { e.writeUint32(nextIncomingID) }),
		fieldOf(func(e *encoder) { e.writeUint32(2147483647) }), // incoming-window
		fieldOf(func(e *encoder) { e.writeUint32(0) }),          // next-outgoing-id
		fieldOf(func(e *encoder) { e.writeUint32(2147483647) }), // outgoing-window
		fieldOf(func(e *encoder) { e.writeUint32(handle) }),
		fieldOf(func(e *encoder) { e.writeUint32(deliveryCount) }),
		fieldOf(func(e *encoder) { e.writeUint32(linkCredit) }),
	})
	return e.buf
}

// encodeTransfer builds a Transfer performative body (the payload is
// returned separately since, per the wire format, it's appended after the
// performative list rather than encoded as one of its fields).
func encodeTransfer(handle, deliveryID uint32, settled bool) []byte {
	e := &encoder{}
	e.writeDescribedList(descTransfer, []field{
		fieldOf(func(e *encoder) { e.writeUint32(handle) }),
		fieldOf(func(e *encoder) { e.writeUint32(deliveryID) }),
		fieldOf(func(e *encoder) { e.writeBinary([]byte{byte(deliveryID)}) }), // delivery-tag
		// message-format MUST be set for the first (here, only) transfer of
		// a delivery — omitting it is legal only on a continuation transfer
		// of an already-started multi-transfer delivery, which this mock
		// never sends (every reply is one message in one transfer). 0 is
		// the standard AMQP 1.0 message format.
		fieldOf(func(e *encoder) { e.writeUint32(0) }),
		omitOr(settled, func(e *encoder) { e.writeBool(true) }),
	})
	return e.buf
}

// encodeDispositionAccepted builds a Disposition performative accepting
// and settling deliveryID — sent after every unsettled inbound transfer so
// a producing client's blocking send completes instead of timing out.
func encodeDispositionAccepted(deliveryID uint32) []byte {
	e := &encoder{}
	accepted := &encoder{}
	accepted.writeDescribedList(descAccepted, nil)
	e.writeDescribedList(descDisposition, []field{
		fieldOf(func(e *encoder) { e.writeBool(true) }), // role: receiver
		fieldOf(func(e *encoder) { e.writeUint32(deliveryID) }),
		omittedField(), // last (defaults to first)
		fieldOf(func(e *encoder) { e.writeBool(true) }), // settled
		{bytes: accepted.buf},
	})
	return e.buf
}

func encodeDetach(handle uint32, closed bool) []byte {
	e := &encoder{}
	e.writeDescribedList(descDetach, []field{
		fieldOf(func(e *encoder) { e.writeUint32(handle) }),
		omitOr(closed, func(e *encoder) { e.writeBool(true) }),
	})
	return e.buf
}

func encodeEnd() []byte {
	e := &encoder{}
	e.writeDescribedList(descEnd, nil)
	return e.buf
}

func encodeClose() []byte {
	e := &encoder{}
	e.writeDescribedList(descClose, nil)
	return e.buf
}
