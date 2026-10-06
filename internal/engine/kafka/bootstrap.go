package kafkaengine

import (
	"encoding/binary"
	"io"

	"github.com/segmentio/kafka-go/protocol"
)

// unsupportedVersion is Kafka's real UNSUPPORTED_VERSION error code (35),
// used only for the ApiVersions bootstrap fallback below.
const unsupportedVersion int16 = 35

// readKafkaFrame reads one length-prefixed Kafka request frame in full —
// buffered in memory (frames are capped well under a megabyte by real
// clients) — returning it WITH its 4-byte size prefix intact, so the same
// bytes can be handed to protocol.ReadRequest afterward. Buffering the
// whole frame up front (rather than streaming straight into
// protocol.ReadRequest) is what makes the ApiVersions bootstrap fallback in
// handleConn possible: the header can be peeked without consuming
// anything protocol.ReadRequest would otherwise need to re-read itself.
func readKafkaFrame(r io.Reader) ([]byte, error) {
	var sizeBuf [4]byte
	if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(sizeBuf[:])
	frame := make([]byte, 4+size)
	copy(frame, sizeBuf[:])
	if _, err := io.ReadFull(r, frame[4:]); err != nil {
		return nil, err
	}
	return frame, nil
}

// bootstrapAPIVersionsFallback peeks a frame's api_key/api_version/
// correlation_id directly (bytes 4:6, 6:8, 8:12 — right after the 4-byte
// size prefix) and reports whether it's an ApiVersions request at a
// version this engine's codec (github.com/segmentio/kafka-go/protocol/
// apiversions, which only implements v0-v2) can't decode at all.
//
// This matters because ApiVersions is the one request every real client
// sends before it knows anything about this broker's capabilities — it's
// the bootstrap/negotiation step itself. Some clients (e.g. librdkafka,
// which is what kcat is built on) default to a newer ApiVersions request
// version (v3, KIP-482's flexible/tagged-field wire encoding) than
// kafka-go's ApiVersions type declares support for. protocol.ReadRequest
// does a hard version-range check before attempting to decode ANYTHING
// and errors out for v3, which previously made handleConn just close the
// connection — indistinguishable, from the client's side, from "not a
// Kafka broker at all" (confirmed against a real kcat/librdkafka client,
// which reported exactly that: "Disconnected while requesting ApiVersion").
//
// Real Kafka brokers handle this by always answering an ApiVersions
// request in the OLDEST (v0) wire format, with error_code=UNSUPPORTED_
// VERSION, regardless of what version was actually requested — v0 is
// guaranteed parseable by every client, however new, specifically so it
// can learn what the broker DOES support (still included in the response)
// and retry at one of those versions. There's nothing worth reading out of
// the request body at any version anyway (v3 only adds an optional
// client-software name/version this mock has no use for), so the body is
// never even inspected here.
func bootstrapAPIVersionsFallback(frame []byte) (correlationID int32, isFallback bool) {
	if len(frame) < 12 {
		return 0, false
	}
	apiKey := protocol.ApiKey(int16(binary.BigEndian.Uint16(frame[4:6])))
	apiVersion := int16(binary.BigEndian.Uint16(frame[6:8]))
	if apiKey != protocol.ApiVersions || (apiVersion >= 0 && apiVersion <= 2) {
		return 0, false
	}
	return int32(binary.BigEndian.Uint32(frame[8:12])), true
}
