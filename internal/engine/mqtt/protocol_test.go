package mqttengine

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// TestReadPacketRejectsAnOversizedDeclaredLength guards against a real DoS:
// MQTT's variable-length encoding lets a client claim up to ~256MB before
// sending a single byte of that body — readPacket used to allocate a
// buffer of exactly that claimed size immediately, with no cap, so a
// 5-byte header alone could force a ~256MB allocation. It must now reject
// anything over maxPacketBodyLength before allocating.
func TestReadPacketRejectsAnOversizedDeclaredLength(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(packetPublish << 4) // fixed header: type=PUBLISH, flags=0
	buf.Write(encodeRemainingLength(maxPacketBodyLength + 1))
	// Deliberately no body bytes follow — if readPacket allocated based on
	// the claimed length and then blocked in io.ReadFull, this test would
	// hang instead of returning promptly; asserting an error here proves
	// it rejected the length before ever trying to read a body.

	_, err := readPacket(bufio.NewReader(&buf))
	if err == nil {
		t.Fatal("expected an error for a declared length over maxPacketBodyLength, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected a 'too large' error, got %v", err)
	}
}

func TestReadPacketAcceptsALengthAtTheCap(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(packetPublish << 4)
	buf.Write(encodeRemainingLength(4))
	buf.WriteString("test")

	pkt, err := readPacket(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("expected a small, well-formed packet to be accepted, got %v", err)
	}
	if string(pkt.body) != "test" {
		t.Fatalf("expected body %q, got %q", "test", pkt.body)
	}
}
