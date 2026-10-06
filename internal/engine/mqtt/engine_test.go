package mqttengine

import (
	"bufio"
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

type fakeHitLogger struct {
	mu      sync.Mutex
	entries []*hitlog.Entry
}

func (f *fakeHitLogger) Record(e *hitlog.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeHitLogger) all() []*hitlog.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*hitlog.Entry, len(f.entries))
	copy(out, f.entries)
	return out
}

func registerOnFreePort(t *testing.T, e *Engine, m *mock.Definition) string {
	t.Helper()
	m.MQTT.Port = 0
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	ln, ok := e.listeners[m.ID]
	if !ok {
		t.Fatal("expected a listener to be registered")
	}
	return ln.Addr().String()
}

// testClient is a minimal hand-rolled MQTT client used only to exercise
// this mock broker end to end — it speaks just enough of the wire protocol
// (CONNECT, SUBSCRIBE, PUBLISH, read) to prove the broker's behavior
// without depending on a third-party MQTT client library.
type testClient struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
}

func dialClient(t *testing.T, addr, clientID string) *testClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	body := append(encodeString("MQTT"), 4, 2, 0, 60) // proto name, level, connect flags (clean session), keep-alive
	body = append(body, encodeString(clientID)...)
	if _, err := conn.Write(buildPacket(packetConnect<<4, body)); err != nil {
		t.Fatalf("write connect: %v", err)
	}

	// One bufio.Reader for the connection's whole lifetime — reconstructing
	// it per read would discard whatever it had already buffered ahead from
	// the socket but not yet handed back.
	c := &testClient{t: t, conn: conn, reader: bufio.NewReader(conn)}
	pkt := c.readRaw()
	if pkt.packetType != packetConnAck {
		t.Fatalf("expected CONNACK, got packet type %d", pkt.packetType)
	}
	return c
}

func (c *testClient) readRaw() *rawPacket {
	c.t.Helper()
	pkt, err := readPacket(c.reader)
	if err != nil {
		c.t.Fatalf("read packet: %v", err)
	}
	return pkt
}

func (c *testClient) subscribe(packetID uint16, filter string) {
	c.t.Helper()
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, packetID)
	body = append(body, encodeString(filter)...)
	body = append(body, 0) // requested QoS 0
	if _, err := c.conn.Write(buildPacket(packetSubscribe<<4|0x02, body)); err != nil {
		c.t.Fatalf("write subscribe: %v", err)
	}
	pkt := c.readRaw()
	if pkt.packetType != packetSubAck {
		c.t.Fatalf("expected SUBACK, got packet type %d", pkt.packetType)
	}
}

func (c *testClient) publish(topic, payload string) {
	c.t.Helper()
	if _, err := c.conn.Write(encodePublish(topic, []byte(payload))); err != nil {
		c.t.Fatalf("write publish: %v", err)
	}
}

func (c *testClient) expectPublish(wantTopic, wantPayload string) {
	c.t.Helper()
	pkt := c.readRaw()
	if pkt.packetType != packetPublish {
		c.t.Fatalf("expected PUBLISH, got packet type %d", pkt.packetType)
	}
	got, err := decodePublish(pkt.flags, pkt.body)
	if err != nil {
		c.t.Fatalf("decode publish: %v", err)
	}
	if got.topic != wantTopic || string(got.payload) != wantPayload {
		c.t.Fatalf("expected PUBLISH %s=%q, got %s=%q", wantTopic, wantPayload, got.topic, got.payload)
	}
}

func TestMQTTMockConnectSubscribePublishReply(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m1", Name: "sensor-mock", Enabled: true, ProtocolType: "mqtt",
		MQTT: &mock.MQTTConfig{
			Rules: []mock.MQTTRule{
				{TopicPattern: "sensors/+/read", ReplyTopic: "sensors/reply", ReplyPayload: `{"echo":"{{.Request.Body}}"}`},
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	client := dialClient(t, addr, "test-client")
	client.subscribe(1, "sensors/reply")
	client.publish("sensors/kitchen/read", "ping")
	client.expectPublish("sensors/reply", `{"echo":"ping"}`)

	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one hit logged, got %d", len(entries))
	}
	if entries[0].Path != "sensors/kitchen/read" || entries[0].RequestBody != "ping" {
		t.Fatalf("unexpected hit entry: %+v", entries[0])
	}
}

func TestMQTTMockNoReplyWhenNotSubscribed(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m2", Name: "no-sub", Enabled: true, ProtocolType: "mqtt",
		MQTT: &mock.MQTTConfig{
			Rules: []mock.MQTTRule{{TopicPattern: "cmd", ReplyTopic: "result", ReplyPayload: "done"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	client := dialClient(t, addr, "publisher-only")
	client.publish("cmd", "go")

	// No subscription to "result" — nothing should arrive; PINGREQ/PINGRESP
	// is used as a liveness probe to prove the connection is still healthy
	// and simply has nothing queued, rather than racing a fixed sleep.
	if _, err := client.conn.Write(buildPacket(packetPingReq<<4, nil)); err != nil {
		t.Fatalf("write pingreq: %v", err)
	}
	pkt := client.readRaw()
	if pkt.packetType != packetPingResp {
		t.Fatalf("expected PINGRESP (no queued publish), got packet type %d", pkt.packetType)
	}
}

func TestMQTTMockPayloadMatchGatesReply(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m3", Name: "payload-gate", Enabled: true, ProtocolType: "mqtt",
		MQTT: &mock.MQTTConfig{
			Rules: []mock.MQTTRule{
				{TopicPattern: "cmd", PayloadMatch: "reboot", MatchType: "exact", ReplyTopic: "ack", ReplyPayload: "rebooting"},
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	client := dialClient(t, addr, "gate-test")
	client.subscribe(1, "ack")

	client.publish("cmd", "status")
	// Unmatched payload: prove no reply queued via a ping round-trip.
	if _, err := client.conn.Write(buildPacket(packetPingReq<<4, nil)); err != nil {
		t.Fatalf("write pingreq: %v", err)
	}
	if pkt := client.readRaw(); pkt.packetType != packetPingResp {
		t.Fatalf("expected PINGRESP for an unmatched payload, got packet type %d", pkt.packetType)
	}

	client.publish("cmd", "reboot")
	client.expectPublish("ack", "rebooting")
}

// TestReRegisteringAMockClosesAlreadyConnectedSessions guards a real
// connection/goroutine leak: RegisterMock (called again for the same mock
// ID, e.g. when a user edits and saves a mock) used to replace the
// sessions map wholesale without ever closing the actual connections
// tracked in the old one — leaving each already-connected client's
// handleConn goroutine and socket running forever, since MQTT connections
// are typically long-lived and have no way to learn their mock changed out
// from under them.
func TestReRegisteringAMockClosesAlreadyConnectedSessions(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{ID: "m-leak", Name: "leak-test", Enabled: true, ProtocolType: "mqtt", MQTT: &mock.MQTTConfig{}}
	addr := registerOnFreePort(t, e, m)
	c := dialClient(t, addr, "client-1")

	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("re-RegisterMock: %v", err)
	}

	c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := c.conn.Read(buf); err == nil {
		t.Fatal("expected the old session's connection to be closed after re-registering the mock, got no error (connection still open)")
	}
}

// TestUnregisteringAMockClosesAlreadyConnectedSessions is the same guard as
// above for the delete/disable path (UnregisterMock).
func TestUnregisteringAMockClosesAlreadyConnectedSessions(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{ID: "m-leak-2", Name: "leak-test-2", Enabled: true, ProtocolType: "mqtt", MQTT: &mock.MQTTConfig{}}
	addr := registerOnFreePort(t, e, m)
	c := dialClient(t, addr, "client-1")

	if err := e.UnregisterMock(m.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}

	c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := c.conn.Read(buf); err == nil {
		t.Fatal("expected the session's connection to be closed after unregistering the mock, got no error (connection still open)")
	}
}

func TestUnregisterMockClosesListener(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{ID: "m4", Name: "temp", Enabled: true, ProtocolType: "mqtt", MQTT: &mock.MQTTConfig{}}
	registerOnFreePort(t, e, m)

	if err := e.UnregisterMock(m.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}
	if _, ok := e.listeners[m.ID]; ok {
		t.Fatal("expected the listener to be removed")
	}
}

func TestRegisterMockDisabledDoesNotListen(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{ID: "m5", Name: "disabled", Enabled: false, ProtocolType: "mqtt", MQTT: &mock.MQTTConfig{Port: 0}}
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	if _, ok := e.listeners[m.ID]; ok {
		t.Fatal("expected no listener for a disabled mock")
	}
}

// TestMQTTMockFaultLatencyJitter guards against a real gap: def.Fault was
// only ever read by the HTTP-family matchers — an MQTT mock's FaultConfig
// was silently never applied, so LatencyJitterMs did nothing.
func TestMQTTMockFaultLatencyJitter(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "fault-latency", Name: "fault-latency", Enabled: true, ProtocolType: "mqtt",
		Fault: &mock.FaultConfig{LatencyJitterMs: 200},
		MQTT: &mock.MQTTConfig{
			Rules: []mock.MQTTRule{{TopicPattern: "cmd", ReplyTopic: "result", ReplyPayload: "done"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	client := dialClient(t, addr, "jitter-client")
	client.subscribe(1, "result")

	// LatencyJitterMs is uniformly random in [0, 200ms] per hit, so assert
	// on the max across several trials rather than one sample.
	var maxElapsed time.Duration
	for i := 0; i < 8; i++ {
		start := time.Now()
		client.publish("cmd", "go")
		client.expectPublish("result", "done")
		if elapsed := time.Since(start); elapsed > maxElapsed {
			maxElapsed = elapsed
		}
	}
	if maxElapsed < 50*time.Millisecond {
		t.Fatalf("expected at least one of 8 trials to show noticeable latency jitter (up to 200ms), max observed was %s", maxElapsed)
	}
}

// TestMQTTMockFaultSuppressesReply guards against the same gap for the
// "reply never arrives" side of fault injection. Unlike TCP/SMTP (where
// "error"/"timeout" drop the whole connection), MQTT's reply is a broadcast
// to OTHER subscribed sessions, not a direct response to the publisher — so
// the fault here suppresses just the reply, without severing the
// publisher's connection (proven via the same PINGREQ/PINGRESP liveness
// probe TestMQTTMockNoReplyWhenNotSubscribed uses).
func TestMQTTMockFaultSuppressesReply(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "fault-timeout", Name: "fault-timeout", Enabled: true, ProtocolType: "mqtt",
		Fault: &mock.FaultConfig{TimeoutRatePercent: 100},
		MQTT: &mock.MQTTConfig{
			Rules: []mock.MQTTRule{{TopicPattern: "cmd", ReplyTopic: "result", ReplyPayload: "done"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	client := dialClient(t, addr, "fault-client")
	client.subscribe(1, "result")
	client.publish("cmd", "go")

	if _, err := client.conn.Write(buildPacket(packetPingReq<<4, nil)); err != nil {
		t.Fatalf("write pingreq: %v", err)
	}
	pkt := client.readRaw()
	if pkt.packetType != packetPingResp {
		t.Fatalf("expected PINGRESP (reply suppressed by fault, connection still alive), got packet type %d", pkt.packetType)
	}
}
