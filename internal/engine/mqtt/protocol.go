// Package mqttengine implements a minimal MQTT 3.1.1 mock broker — just
// enough of the wire protocol (CONNECT/CONNACK, SUBSCRIBE/SUBACK,
// UNSUBSCRIBE/UNSUBACK, PUBLISH/PUBACK, PINGREQ/PINGRESP, DISCONNECT) to let
// a real MQTT client library connect, publish, and subscribe against it,
// without depending on a third-party broker or client library — the same
// hand-rolled-protocol approach already used for the TCP and SMTP engines.
package mqttengine

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// Packet types (MQTT 3.1.1 §2.2.1) — only what this mock needs to
// understand or emit; QoS 2 (PUBREC/PUBREL/PUBCOMP) is deliberately
// unsupported, same simplification TCP/SMTP make elsewhere (fewer states to
// implement correctly, and QoS 2 is rare in practice for test/mock use).
const (
	packetConnect     = 1
	packetConnAck     = 2
	packetPublish     = 3
	packetPubAck      = 4
	packetSubscribe   = 8
	packetSubAck      = 9
	packetUnsubscribe = 10
	packetUnsubAck    = 11
	packetPingReq     = 12
	packetPingResp    = 13
	packetDisconnect  = 14
)

// rawPacket is one decoded MQTT control packet: the fixed header's type
// (upper 4 bits of byte 0) and flags (lower 4 bits), plus the full
// variable-header+payload body, still unparsed — each packet type's own
// decode function slices into body as needed.
type rawPacket struct {
	packetType byte
	flags      byte
	body       []byte
}

// maxPacketBodyLength bounds a single packet's declared remaining length.
// MQTT's own variable-length encoding allows up to ~256MB, entirely
// attacker-controlled and read BEFORE any of that body has actually
// arrived — without a cap, a client need only send a 5-byte header
// claiming the protocol maximum to force a ~256MB allocation per packet,
// repeatable per connection for straightforward memory exhaustion. Matches
// the FTP engine's existing 10MB upload cap for the same class of risk;
// real MQTT test/mock payloads are expected to be well under this.
const maxPacketBodyLength = 10 << 20 // 10MB

// readPacket reads one full MQTT control packet off the wire: the 1-byte
// fixed header, the variable-length-encoded remaining length, then exactly
// that many body bytes.
func readPacket(r *bufio.Reader) (*rawPacket, error) {
	first, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	length, err := readRemainingLength(r)
	if err != nil {
		return nil, err
	}
	if length > maxPacketBodyLength {
		return nil, fmt.Errorf("packet body too large: %d bytes (max %d)", length, maxPacketBodyLength)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return &rawPacket{packetType: first >> 4, flags: first & 0x0F, body: body}, nil
}

// readRemainingLength decodes MQTT's variable-length-encoded remaining
// length (§2.2.3): up to 4 bytes, 7 data bits each, MSB is a continuation
// flag.
func readRemainingLength(r *bufio.Reader) (int, error) {
	multiplier := 1
	value := 0
	for i := 0; i < 4; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value += int(b&0x7F) * multiplier
		if b&0x80 == 0 {
			return value, nil
		}
		multiplier *= 128
	}
	return 0, fmt.Errorf("malformed remaining length")
}

func encodeRemainingLength(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

// buildPacket assembles a full wire packet from a type/flags byte and an
// already-encoded variable-header+payload body.
func buildPacket(typeAndFlags byte, body []byte) []byte {
	out := append([]byte{typeAndFlags}, encodeRemainingLength(len(body))...)
	return append(out, body...)
}

// mqttString reads one length-prefixed UTF-8 string (§1.5.3) from buf
// starting at offset, returning the string and the offset just past it.
func mqttString(buf []byte, offset int) (string, int, error) {
	if offset+2 > len(buf) {
		return "", 0, fmt.Errorf("truncated string length")
	}
	n := int(binary.BigEndian.Uint16(buf[offset : offset+2]))
	offset += 2
	if offset+n > len(buf) {
		return "", 0, fmt.Errorf("truncated string body")
	}
	return string(buf[offset : offset+n]), offset + n, nil
}

func encodeString(s string) []byte {
	out := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(out[:2], uint16(len(s)))
	copy(out[2:], s)
	return out
}

// connectInfo is everything decodeConnect pulls out of a CONNECT packet
// that this mock actually uses.
type connectInfo struct {
	clientID string
}

func decodeConnect(body []byte) (*connectInfo, error) {
	protoName, offset, err := mqttString(body, 0)
	if err != nil {
		return nil, err
	}
	if protoName != "MQTT" && protoName != "MQIsdp" {
		return nil, fmt.Errorf("unrecognized protocol name %q", protoName)
	}
	// protocol level (1 byte) + connect flags (1 byte) + keep-alive (2 bytes)
	if offset+4 > len(body) {
		return nil, fmt.Errorf("truncated connect variable header")
	}
	offset += 4
	clientID, _, err := mqttString(body, offset)
	if err != nil {
		return nil, err
	}
	return &connectInfo{clientID: clientID}, nil
}

// encodeConnAck builds a CONNACK accepting the connection with no prior
// session (this mock never persists sessions across connections).
func encodeConnAck() []byte {
	return buildPacket(packetConnAck<<4, []byte{0x00, 0x00})
}

// publishInfo is a decoded PUBLISH packet's topic/payload/QoS/packet-id.
type publishInfo struct {
	topic    string
	payload  []byte
	qos      int
	packetID uint16 // only meaningful when qos > 0
}

func decodePublish(flags byte, body []byte) (*publishInfo, error) {
	qos := int((flags >> 1) & 0x03)
	topic, offset, err := mqttString(body, 0)
	if err != nil {
		return nil, err
	}
	var packetID uint16
	if qos > 0 {
		if offset+2 > len(body) {
			return nil, fmt.Errorf("truncated publish packet id")
		}
		packetID = binary.BigEndian.Uint16(body[offset : offset+2])
		offset += 2
	}
	return &publishInfo{topic: topic, payload: body[offset:], qos: qos, packetID: packetID}, nil
}

// encodePublish builds a QoS-0 PUBLISH — every reply this mock sends is
// fire-and-forget QoS 0, matching how TCP/SMTP responses aren't
// acknowledged either; a real client subscribing normally still receives
// it fine at QoS 0.
func encodePublish(topic string, payload []byte) []byte {
	body := append(encodeString(topic), payload...)
	return buildPacket(packetPublish<<4, body)
}

func encodePubAck(packetID uint16) []byte {
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, packetID)
	return buildPacket(packetPubAck<<4, body)
}

// subscribeInfo is a decoded SUBSCRIBE packet: its packet id (echoed back
// in SUBACK) and the list of topic filters requested (their requested QoS
// is read but not tracked — every grant in this mock is QoS 0/1 passthrough).
type subscribeInfo struct {
	packetID uint16
	filters  []string
}

func decodeSubscribe(body []byte) (*subscribeInfo, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("truncated subscribe packet id")
	}
	packetID := binary.BigEndian.Uint16(body[:2])
	offset := 2
	var filters []string
	for offset < len(body) {
		filter, next, err := mqttString(body, offset)
		if err != nil {
			return nil, err
		}
		filters = append(filters, filter)
		offset = next + 1 // skip the requested-QoS byte
	}
	return &subscribeInfo{packetID: packetID, filters: filters}, nil
}

// encodeSubAck grants every requested filter at QoS 0.
func encodeSubAck(packetID uint16, count int) []byte {
	body := make([]byte, 2+count)
	binary.BigEndian.PutUint16(body[:2], packetID)
	// bytes after the packet id default to 0x00 (QoS 0 granted) already
	return buildPacket(packetSubAck<<4, body)
}

func decodeUnsubscribe(body []byte) (*subscribeInfo, error) {
	return decodeSubscribeLikeFilters(body)
}

// decodeSubscribeLikeFilters parses UNSUBSCRIBE's payload, which is
// structurally identical to SUBSCRIBE's except filters have no trailing
// QoS byte.
func decodeSubscribeLikeFilters(body []byte) (*subscribeInfo, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("truncated unsubscribe packet id")
	}
	packetID := binary.BigEndian.Uint16(body[:2])
	offset := 2
	var filters []string
	for offset < len(body) {
		filter, next, err := mqttString(body, offset)
		if err != nil {
			return nil, err
		}
		filters = append(filters, filter)
		offset = next
	}
	return &subscribeInfo{packetID: packetID, filters: filters}, nil
}

func encodeUnsubAck(packetID uint16) []byte {
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, packetID)
	return buildPacket(packetUnsubAck<<4, body)
}

func encodePingResp() []byte {
	return buildPacket(packetPingResp<<4, nil)
}
