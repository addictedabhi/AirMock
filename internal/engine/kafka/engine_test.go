package kafkaengine

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/protocol"
	"github.com/segmentio/kafka-go/protocol/apiversions"
	"github.com/segmentio/kafka-go/protocol/fetch"
	"github.com/segmentio/kafka-go/protocol/metadata"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/session"
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

// registerOnFreePort registers m on an OS-assigned port, then writes the
// real bound port back onto m.Kafka.Port — Metadata responses advertise
// this port, and a real client that discovers a topic via Metadata (rather
// than being handed a raw net.Conn directly, as the tests below do) would
// otherwise be told to reconnect to port 0.
func registerOnFreePort(t *testing.T, e *Engine, m *mock.Definition) string {
	t.Helper()
	m.Kafka.Port = 0
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	ln, ok := e.listeners[m.ID]
	if !ok {
		t.Fatal("expected a listener to be registered")
	}
	addr := ln.Addr().(*net.TCPAddr)
	m.Kafka.Port = addr.Port
	return addr.String()
}

// dialConn wraps a real net.Conn to addr with kafka-go's own low-level
// client (github.com/segmentio/kafka-go.Conn), bound directly to topic —
// this is the exact "real client library completes a basic exchange" bar
// the mock is meant to clear, using the manual-offset (no consumer group)
// API a real test double's caller would actually reach for.
func dialConn(t *testing.T, addr, topic string) *kafka.Conn {
	t.Helper()
	netConn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn := kafka.NewConn(netConn, topic, 0)
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn
}

func newDef(name string, cfg *mock.KafkaConfig) *mock.Definition {
	return &mock.Definition{ID: "kafka-" + name, Name: name, ProtocolType: "kafka", Enabled: true, Kafka: cfg}
}

// TestKafkaMockFetchWithNoNewRecordsStillAnswers reproduces a third real
// bug found via kcat/librdkafka — the most severe of the three, since it
// breaks the single most common Fetch outcome: polling a topic that has
// never been produced to (or one a consumer has already caught up with).
// kafka-go's RecordBatch v2 writer (protocol.RecordSet.writeToVersion2)
// hard-fails with ErrNoRecord for a genuinely empty batch, which
// propagates all the way up through protocol.WriteResponse and silently
// kills the ENTIRE response — confirmed directly: the server wrote zero
// bytes and closed the connection rather than send a valid "0 records"
// answer. This talks to a fetch.Request built by hand at v11 against a
// topic that's never had anything produced to it, and requires getting a
// well-formed (if empty) response back rather than a dropped connection.
func TestKafkaMockFetchWithNoNewRecordsStillAnswers(t *testing.T) {
	e := New()
	def := newDef("empty-fetch", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	// Reference the topic via Metadata first (as a real client always
	// does) without ever producing to it — the exact scenario that
	// crashed the response.
	metaConn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer metaConn.Close()
	metaConn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := protocol.WriteRequest(metaConn, 1, 1, "test-client", &metadata.Request{TopicNames: []string{"empty-topic"}}); err != nil {
		t.Fatalf("WriteRequest (metadata): %v", err)
	}
	if _, _, err := protocol.ReadResponse(metaConn, protocol.Metadata, 1); err != nil {
		t.Fatalf("ReadResponse (metadata): %v", err)
	}

	req := &fetch.Request{
		MaxWaitTime: 100,
		MinBytes:    1,
		MaxBytes:    1 << 20,
		Topics: []fetch.RequestTopic{{
			Topic:      "empty-topic",
			Partitions: []fetch.RequestPartition{{Partition: 0, FetchOffset: 0, PartitionMaxBytes: 1 << 20}},
		}},
	}
	if err := protocol.WriteRequest(metaConn, 11, 2, "test-client", req); err != nil {
		t.Fatalf("WriteRequest (fetch): %v", err)
	}
	_, msg, err := protocol.ReadResponse(metaConn, protocol.Fetch, 11)
	if err != nil {
		t.Fatalf("ReadResponse (fetch should NOT have dropped the connection): %v", err)
	}
	resp, ok := msg.(*fetch.Response)
	if !ok || len(resp.Topics) != 1 || len(resp.Topics[0].Partitions) != 1 {
		t.Fatalf("unexpected response shape: %+v", msg)
	}
	part := resp.Topics[0].Partitions[0]
	if part.HighWatermark != 0 || part.LastStableOffset != 0 {
		t.Fatalf("expected watermark/stable offset 0 for a never-produced-to topic, got %+v", part)
	}
	// A decoded empty record set legitimately comes back with Records ==
	// nil (RecordSet.ReadFrom returns early for a zero-length records
	// field) — that's the library's normal representation of "no
	// records," not a bug. Only check that there's nothing to read if a
	// reader is present at all.
	if part.RecordSet.Records != nil {
		if rec, err := part.RecordSet.Records.ReadRecord(); err == nil {
			t.Fatalf("expected zero records, got one: %+v", rec)
		}
	}
}

// TestKafkaMockFetchLongPollsInsteadOfBusySpinning covers the fourth real
// issue found via kcat/librdkafka: handleFetch used to answer an empty poll
// instantly regardless of MaxWaitTime, so a real client tailing an idle
// topic hammered the connection with thousands of Fetch requests per
// second (observed directly: 9.5MB of client-side protocol log within a
// few seconds) — a real broker's genuine long-poll block never produces
// traffic that dense. This asserts both halves of the fix: a Fetch with
// nothing new actually blocks for roughly MaxWaitTime (not zero), and one
// that gets satisfied by data arriving mid-wait returns promptly rather
// than sitting out the full timeout regardless.
func TestKafkaMockFetchLongPollsInsteadOfBusySpinning(t *testing.T) {
	e := New()
	def := newDef("long-poll", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	metaConn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer metaConn.Close()
	metaConn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := protocol.WriteRequest(metaConn, 1, 1, "test-client", &metadata.Request{TopicNames: []string{"orders"}}); err != nil {
		t.Fatalf("WriteRequest (metadata): %v", err)
	}
	if _, _, err := protocol.ReadResponse(metaConn, protocol.Metadata, 1); err != nil {
		t.Fatalf("ReadResponse (metadata): %v", err)
	}

	fetchOnce := func(maxWait int32) (*fetch.Response, time.Duration) {
		req := &fetch.Request{
			MaxWaitTime: maxWait, MinBytes: 1, MaxBytes: 1 << 20,
			Topics: []fetch.RequestTopic{{Topic: "orders", Partitions: []fetch.RequestPartition{{Partition: 0, FetchOffset: 0, PartitionMaxBytes: 1 << 20}}}},
		}
		start := time.Now()
		if err := protocol.WriteRequest(metaConn, 11, 2, "test-client", req); err != nil {
			t.Fatalf("WriteRequest (fetch): %v", err)
		}
		_, msg, err := protocol.ReadResponse(metaConn, protocol.Fetch, 11)
		if err != nil {
			t.Fatalf("ReadResponse (fetch): %v", err)
		}
		return msg.(*fetch.Response), time.Since(start)
	}

	// Nothing produced: a 300ms MaxWaitTime should make the call take
	// roughly that long, not return instantly.
	_, elapsed := fetchOnce(300)
	if elapsed < 250*time.Millisecond {
		t.Fatalf("expected the empty poll to block for ~300ms, returned after only %s", elapsed)
	}

	// Produce shortly after issuing a long fetch — the response should
	// arrive promptly once the data does, not wait out the full timeout.
	go func() {
		time.Sleep(100 * time.Millisecond)
		producer := dialConn(t, addr, "orders")
		producer.WriteMessages(kafka.Message{Value: []byte("hello")})
	}()
	resp, elapsed := fetchOnce(5000)
	if elapsed > 2*time.Second {
		t.Fatalf("expected the fetch to return promptly once data arrived, took %s", elapsed)
	}
	part := resp.Topics[0].Partitions[0]
	if part.HighWatermark != 1 {
		t.Fatalf("expected the produced record to be visible, HighWatermark=%d", part.HighWatermark)
	}
}

// TestKafkaMockAnswersApiVersionsV3BootstrapWithoutDisconnecting reproduces
// what librdkafka (and hence kcat) actually does on its very first
// connection: send an ApiVersions request at v3 (KIP-482's flexible
// encoding) before it knows anything about this broker. kafka-go's own
// ApiVersions codec only implements v0-v2, so without the bootstrap
// fallback in handleFrame, this exact request made the engine silently
// close the connection — reported firsthand against a real kcat client as
// "Disconnected while requesting ApiVersion". The bytes here are built by
// hand (not via kafka-go, which can't even construct a v3 ApiVersions
// request itself) specifically to reproduce that real client's behavior.
func TestKafkaMockAnswersApiVersionsV3BootstrapWithoutDisconnecting(t *testing.T) {
	e := New()
	def := newDef("bootstrap-fallback", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	const correlationID = 42
	header := make([]byte, 8)
	binary.BigEndian.PutUint16(header[0:2], uint16(protocol.ApiVersions))
	binary.BigEndian.PutUint16(header[2:4], 3) // the version kafka-go's codec can't decode
	binary.BigEndian.PutUint32(header[4:8], correlationID)
	// client_id (empty, non-flexible-length-prefixed) + a few arbitrary
	// trailing bytes standing in for v3's flexible body (client software
	// name/version, tagged fields) — the fallback path never inspects
	// anything past the header fields above, so their exact encoding
	// doesn't matter for this test.
	rest := []byte{0x00, 0x00, 0x00, 0x00, 0x00}
	payload := append(header, rest...)
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, uint32(len(payload)))
	if _, err := conn.Write(append(size, payload...)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The fallback always answers at v0, per real Kafka broker behavior —
	// read the response at that fixed version regardless of what was sent.
	gotCorrelationID, msg, err := protocol.ReadResponse(conn, protocol.ApiVersions, 0)
	if err != nil {
		t.Fatalf("ReadResponse (connection should NOT have been dropped): %v", err)
	}
	if gotCorrelationID != correlationID {
		t.Fatalf("expected correlation_id %d echoed back, got %d", correlationID, gotCorrelationID)
	}
	resp, ok := msg.(*apiversions.Response)
	if !ok {
		t.Fatalf("expected *apiversions.Response, got %T", msg)
	}
	if resp.ErrorCode != 35 { // UNSUPPORTED_VERSION
		t.Fatalf("expected error_code 35 (UNSUPPORTED_VERSION), got %d", resp.ErrorCode)
	}
	foundProduce := false
	for _, k := range resp.ApiKeys {
		if protocol.ApiKey(k.ApiKey) == protocol.Produce {
			foundProduce = true
			if k.MaxVersion < 3 {
				t.Fatalf("expected Produce to advertise at least v3 (RecordBatch v2), got max v%d", k.MaxVersion)
			}
		}
	}
	if !foundProduce {
		t.Fatal("expected the fallback response to still list Produce among supported APIs, so the client can retry")
	}
}

func TestKafkaMockProduceAndFetch(t *testing.T) {
	e := New()
	def := newDef("produce-fetch", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	producer := dialConn(t, addr, "orders")
	if _, err := producer.WriteMessages(kafka.Message{Value: []byte("order-1")}); err != nil {
		t.Fatalf("WriteMessages: %v", err)
	}

	consumer := dialConn(t, addr, "orders")
	msg, err := consumer.ReadMessage(1e6)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(msg.Value) != "order-1" {
		t.Fatalf("expected value %q, got %q", "order-1", msg.Value)
	}
	if msg.Offset != 0 {
		t.Fatalf("expected offset 0, got %d", msg.Offset)
	}
}

func TestKafkaMockFetchSeesRecordsProducedBeforeTheConsumerConnected(t *testing.T) {
	e := New()
	def := newDef("late-consumer", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	producer := dialConn(t, addr, "orders")
	for _, v := range []string{"a", "b", "c"} {
		if _, err := producer.WriteMessages(kafka.Message{Value: []byte(v)}); err != nil {
			t.Fatalf("WriteMessages: %v", err)
		}
	}

	// A brand-new connection, opened only AFTER all three were produced —
	// proves the log is durable across the produce/consume boundary, not
	// just a broadcast-to-currently-connected-sessions mechanism like MQTT.
	//
	// Reads via one ReadBatch + repeated Batch.ReadMessage, not repeated
	// top-level Conn.ReadMessage calls: Conn.ReadMessage's own doc comment
	// warns it's "provided for convenience... much less efficient" — each
	// call opens a fresh Batch, and closing a Batch after reading only one
	// record out of a multi-record response doesn't reliably rewind the
	// connection's next-fetch-offset to just past that one record.
	// ReadBatch is the library's own recommended way to read more than one
	// message off a connection.
	consumer := dialConn(t, addr, "orders")
	batch := consumer.ReadBatch(1, 1e6)
	defer batch.Close()
	for i, want := range []string{"a", "b", "c"} {
		msg, err := batch.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage %d: %v", i, err)
		}
		if string(msg.Value) != want {
			t.Fatalf("record %d: expected %q, got %q", i, want, msg.Value)
		}
	}
}

func TestKafkaMockRuleReplyGoesToItsOwnTopic(t *testing.T) {
	e := New()
	def := newDef("rule-reply", &mock.KafkaConfig{
		Rules: []mock.KafkaRule{
			{TopicPattern: "requests", ReplyTopic: "replies", ReplyPayload: `ack:{{.Request.Body}}`},
		},
	})
	addr := registerOnFreePort(t, e, def)

	producer := dialConn(t, addr, "requests")
	if _, err := producer.WriteMessages(kafka.Message{Value: []byte("ping")}); err != nil {
		t.Fatalf("WriteMessages: %v", err)
	}

	replyConsumer := dialConn(t, addr, "replies")
	msg, err := replyConsumer.ReadMessage(1e6)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(msg.Value) != "ack:ping" {
		t.Fatalf("expected reply %q, got %q", "ack:ping", msg.Value)
	}
}

// TestKafkaMockFetchNeverClaimsAReplicaRedirect reproduces two real bugs
// found via kcat/librdkafka in fetch.ResponsePartition fields this engine
// never set explicitly, both defaulting to Go's zero value (0) — which is
// NOT a safe default for either:
//
//   - PreferredReadReplica (Fetch v11+): 0 is a REAL broker ID, not a "no
//     redirect" sentinel. A client reading a response that both contains
//     records AND claims a preferred replica treats that as contradictory,
//     discards the records, and retries forever trying to locate replica 0
//     (this mock's only broker is ID 1). -1 is the actual "don't redirect"
//     sentinel.
//   - LastStableOffset (Fetch v4+): tells a read-committed consumer how far
//     it's safe to read given any in-flight transaction — this mock never
//     has one, so it must always equal HighWatermark. Left at 0, a real
//     client compares its fetch offset against THIS field rather than
//     HighWatermark to decide whether there's anything new: with both
//     stuck at 0, it silently reports "reached end of topic" and never
//     surfaces a single record, no matter how many are actually present in
//     the response bytes (confirmed via kcat's own verbose protocol log —
//     it received a 156-byte non-empty FetchResponse and still exited
//     having consumed nothing).
func TestKafkaMockFetchNeverClaimsAReplicaRedirect(t *testing.T) {
	e := New()
	def := newDef("preferred-replica", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	producer := dialConn(t, addr, "orders")
	if _, err := producer.WriteMessages(kafka.Message{Value: []byte("hi")}); err != nil {
		t.Fatalf("WriteMessages: %v", err)
	}

	netConn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer netConn.Close()
	netConn.SetDeadline(time.Now().Add(3 * time.Second))

	req := &fetch.Request{
		MaxWaitTime: 100,
		MinBytes:    1,
		MaxBytes:    1 << 20,
		Topics: []fetch.RequestTopic{{
			Topic:      "orders",
			Partitions: []fetch.RequestPartition{{Partition: 0, FetchOffset: 0, PartitionMaxBytes: 1 << 20}},
		}},
	}
	// v11 specifically — that's where PreferredReadReplica exists on the wire.
	if err := protocol.WriteRequest(netConn, 11, 1, "test-client", req); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	_, msg, err := protocol.ReadResponse(netConn, protocol.Fetch, 11)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	resp, ok := msg.(*fetch.Response)
	if !ok || len(resp.Topics) != 1 || len(resp.Topics[0].Partitions) != 1 {
		t.Fatalf("unexpected response shape: %+v", msg)
	}
	if got := resp.Topics[0].Partitions[0].PreferredReadReplica; got != -1 {
		t.Fatalf("expected PreferredReadReplica -1 (no redirect), got %d", got)
	}
	if got, want := resp.Topics[0].Partitions[0].LastStableOffset, resp.Topics[0].Partitions[0].HighWatermark; got != want {
		t.Fatalf("expected LastStableOffset (%d) to match HighWatermark (%d) — no transaction is ever in flight in this mock", got, want)
	}
}

// TestKafkaMockFetchReportsCorrectAbsoluteRecordOffsets reproduces the
// fifth (and root-cause) real bug found via kcat/librdkafka. kafka-go's
// RecordBatch v2 writer (protocol.RecordSet.writeToVersion2) hardcodes
// every batch's base_offset to 0 — confirmed directly in the dependency's
// own source (`e.writeInt64(0) // base offset`) — regardless of what
// offset the records it's given actually start at. A real client computes
// each record's absolute offset as base_offset+offset_delta, so whenever
// FetchOffset isn't itself 0 (any consumer that's already caught up and is
// fetching whatever comes next — the ordinary case for a long-lived
// consumer, and exactly what happens under concurrent produce+fetch),
// every record in the response was mislabeled as starting at absolute
// offset 0. kafka-go's own decoder doesn't cross-check that, but a
// stricter one does: confirmed against a real kcat/librdkafka client,
// which silently discarded such batches and eventually gave up reporting
// "message might be too large to fetch" — despite the response being
// otherwise byte-perfect (valid CRC, correct lengths, hand-verified
// against the wire format down to the byte). This produces three records
// (so real offsets are 0,1,2), then Fetches starting at offset 2 — asking
// specifically for the LAST record — and checks the raw wire bytes decode
// to absolute offset 2, not 0.
func TestKafkaMockFetchReportsCorrectAbsoluteRecordOffsets(t *testing.T) {
	e := New()
	def := newDef("absolute-offsets", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	producer := dialConn(t, addr, "orders")
	for _, v := range []string{"a", "b", "c"} {
		if _, err := producer.WriteMessages(kafka.Message{Value: []byte(v)}); err != nil {
			t.Fatalf("WriteMessages: %v", err)
		}
	}

	rawConn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer rawConn.Close()
	rawConn.SetDeadline(time.Now().Add(3 * time.Second))

	req := &fetch.Request{
		MaxWaitTime: 100, MinBytes: 1, MaxBytes: 1 << 20,
		Topics: []fetch.RequestTopic{{
			Topic:      "orders",
			Partitions: []fetch.RequestPartition{{Partition: 0, FetchOffset: 2, PartitionMaxBytes: 1 << 20}},
		}},
	}
	if err := protocol.WriteRequest(rawConn, 11, 1, "test-client", req); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	_, msg, err := protocol.ReadResponse(rawConn, protocol.Fetch, 11)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	resp := msg.(*fetch.Response)
	part := resp.Topics[0].Partitions[0]
	if part.RecordSet.Records == nil {
		t.Fatal("expected the last record to be returned")
	}
	rec, err := part.RecordSet.Records.ReadRecord()
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	value, _ := protocol.ReadAll(rec.Value)
	if string(value) != "c" {
		t.Fatalf("expected value %q, got %q", "c", value)
	}
	if rec.Offset != 2 {
		t.Fatalf("expected the record to report absolute offset 2 (base_offset patched correctly), got %d — this is exactly what confused a real client into reporting \"message might be too large\"", rec.Offset)
	}
}

func TestKafkaMockPayloadMatchGatesReply(t *testing.T) {
	e := New()
	def := newDef("payload-match", &mock.KafkaConfig{
		Rules: []mock.KafkaRule{
			{TopicPattern: "commands", PayloadMatch: "reboot", MatchType: "exact", ReplyTopic: "acks", ReplyPayload: "rebooting"},
		},
	})
	addr := registerOnFreePort(t, e, def)

	producer := dialConn(t, addr, "commands")
	if _, err := producer.WriteMessages(kafka.Message{Value: []byte("status")}); err != nil {
		t.Fatalf("WriteMessages: %v", err)
	}
	if _, err := producer.WriteMessages(kafka.Message{Value: []byte("reboot")}); err != nil {
		t.Fatalf("WriteMessages: %v", err)
	}

	replyConsumer := dialConn(t, addr, "acks")
	msg, err := replyConsumer.ReadMessage(1e6)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(msg.Value) != "rebooting" {
		t.Fatalf("expected reply %q, got %q", "rebooting", msg.Value)
	}
	if msg.Offset != 0 {
		t.Fatalf("expected the ONLY reply at offset 0 (the non-matching 'status' produce must not have triggered one), got offset %d", msg.Offset)
	}
}

func TestKafkaMockRecordsHits(t *testing.T) {
	e := New()
	logger := &fakeHitLogger{}
	e.SetHitLogger(logger)
	def := newDef("hit-logging", &mock.KafkaConfig{
		Rules: []mock.KafkaRule{{TopicPattern: "orders", ReplyTopic: "receipts", ReplyPayload: "ok"}},
	})
	addr := registerOnFreePort(t, e, def)

	producer := dialConn(t, addr, "orders")
	if _, err := producer.WriteMessages(kafka.Message{Value: []byte("order-1")}); err != nil {
		t.Fatalf("WriteMessages: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(logger.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("expected 1 hit-log entry, got %d", len(entries))
	}
	if entries[0].Path != "orders" || entries[0].RequestBody != "order-1" || entries[0].ResponseBody != "PRODUCE receipts: ok" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

func TestKafkaMockFaultSuppressesReply(t *testing.T) {
	e := New()
	def := newDef("fault-suppress", &mock.KafkaConfig{
		Rules: []mock.KafkaRule{{TopicPattern: "orders", ReplyTopic: "receipts", ReplyPayload: "ok"}},
	})
	def.Fault = &mock.FaultConfig{ErrorRatePercent: 100, ErrorStatusCodes: []int{500}}
	addr := registerOnFreePort(t, e, def)

	producer := dialConn(t, addr, "orders")
	if _, err := producer.WriteMessages(kafka.Message{Value: []byte("order-1")}); err != nil {
		t.Fatalf("WriteMessages: %v", err)
	}

	receiptLog := e.storeFor(def.ID).get("receipts")
	if hw := receiptLog.highWatermark(); hw != 0 {
		t.Fatalf("expected no reply appended under a 100%% fault, got %d records", hw)
	}
}

func TestReRegisteringAKafkaMockClosesAlreadyConnectedConns(t *testing.T) {
	e := New()
	def := newDef("re-register", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond) // let acceptLoop's goroutine register the conn

	registerOnFreePort(t, e, def) // re-register the same mock ID

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the old connection to be closed after re-registering the mock")
	}
}

func TestUnregisteringAKafkaMockClosesAlreadyConnectedConns(t *testing.T) {
	e := New()
	def := newDef("unregister", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	if err := e.UnregisterMock(def.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the connection to be closed after unregistering the mock")
	}
}

// TestKafkaSessionsListAndClose covers the admin "Connected sessions" API
// surface: a connected client shows up in ListSessions, and CloseSession
// forcibly drops the connection. Kafka has no SendToSession (see
// ListSessions' own doc comment) since a consumer only ever pulls via Fetch.
func TestKafkaSessionsListAndClose(t *testing.T) {
	e := New()
	def := newDef("sessions", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var sessions []session.Info
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sessions = e.ListSessions(def.ID)
		if len(sessions) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 connected session, got %d", len(sessions))
	}
	if sessions[0].Protocol != "kafka" {
		t.Fatalf("expected protocol %q, got %q", "kafka", sessions[0].Protocol)
	}

	if err := e.CloseSession(def.ID, sessions[0].ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the connection to be closed after CloseSession")
	}

	if err := e.CloseSession(def.ID, "no-such-session"); err == nil {
		t.Fatal("expected an error closing a session id that doesn't exist")
	}
}

func TestUnregisterKafkaMockClosesListener(t *testing.T) {
	e := New()
	def := newDef("unregister-listener", &mock.KafkaConfig{})
	addr := registerOnFreePort(t, e, def)

	if err := e.UnregisterMock(def.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("expected dial to fail after unregistering the mock")
	}
}

func TestRegisterKafkaMockDisabledDoesNotListen(t *testing.T) {
	e := New()
	def := newDef("disabled", &mock.KafkaConfig{Port: 0})
	def.Enabled = false
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	if _, ok := e.listeners[def.ID]; ok {
		t.Fatal("expected no listener for a disabled mock")
	}
}
