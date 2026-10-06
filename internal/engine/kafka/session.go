package kafkaengine

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/kafka-go/protocol"
	"github.com/segmentio/kafka-go/protocol/apiversions"
	"github.com/segmentio/kafka-go/protocol/fetch"
	"github.com/segmentio/kafka-go/protocol/listoffsets"
	"github.com/segmentio/kafka-go/protocol/metadata"
	"github.com/segmentio/kafka-go/protocol/produce"

	"github.com/addictedabhi/airmock/internal/mock"
)

// brokerNodeID is the single fake broker's node ID every Metadata response
// advertises itself as, and the only leader/replica/ISR any partition ever
// reports — there's exactly one broker in this mock, so it's always the
// leader of everything.
const brokerNodeID = 1

// writeResponseMu serializes every protocol.WriteResponse call across ALL
// connections/mocks in this engine. kafka-go's encoder backs each call with
// a page buffer drawn from a package-level sync.Pool
// (github.com/segmentio/kafka-go/protocol/buffer.go) — under sustained
// concurrent load (many connections each producing/fetching at once,
// exactly this engine's normal shape) a real client (librdkafka, via kcat)
// occasionally received a Fetch response with a plausible-looking non-zero
// size that nonetheless decoded to zero usable records, reported as
// "Message might be too large to fetch." kafka-go's own decoder (used in
// this engine's tests) never flagged a CRC or framing problem reading the
// exact same kind of response, which points at something version-specific
// in how concurrent writers share that pool rather than a bug in this
// engine's own request handling (verified independently under `-race` with
// no reports). Serializing every encode call is cheap — response encoding
// is a handful of microseconds — and removes the shared-pool concurrency
// entirely as a variable, which is the pragmatic fix for a mock whose job
// is correctness over raw write throughput.
var writeResponseMu sync.Mutex

func writeKafkaResponse(w io.Writer, apiVersion int16, correlationID int32, msg protocol.Message) error {
	writeResponseMu.Lock()
	defer writeResponseMu.Unlock()
	return protocol.WriteResponse(w, apiVersion, correlationID, msg)
}

// handleConn runs for the life of one connection, unlike MQTT there's no
// initial handshake packet to wait for — Kafka's wire protocol has no
// CONNECT equivalent, the first bytes a client sends are just its first
// real request (almost always ApiVersions, to discover what this broker
// supports before deciding which version to use for everything else).
func (e *Engine) handleConn(conn net.Conn, def *mock.Definition) {
	defer conn.Close()
	regID := e.sessReg.Add(def.ID, "kafka", conn, conn.RemoteAddr().String(), nil)
	defer e.sessReg.Remove(def.ID, regID)

	for {
		frame, err := readKafkaFrame(conn)
		if err != nil {
			return
		}
		e.sessReg.Touch(def.ID, regID, 1, 0)
		if !e.handleFrame(conn, def, frame) {
			return
		}
	}
}

// handleFrame processes one already-buffered request frame, returning false
// when the connection should be closed (a decode error, an unsupported
// request type, or a write failure). Split out from handleConn's read loop
// purely to keep that loop itself trivial.
func (e *Engine) handleFrame(conn net.Conn, def *mock.Definition, frame []byte) bool {
	if correlationID, isFallback := bootstrapAPIVersionsFallback(frame); isFallback {
		return writeKafkaResponse(conn, 0, correlationID, handleAPIVersions(unsupportedVersion)) == nil
	}

	apiVersion, correlationID, _, msg, err := protocol.ReadRequest(bytes.NewReader(frame))
	if err != nil {
		return false
	}

	// A producer with acks=0 doesn't wait for (or even read) a Produce
	// response — writing one anyway would sit unread in the socket buffer
	// and desync the framing of whatever the client sends next, since it'd
	// be misread as part of the following response.
	if req, ok := msg.(interface{ HasResponse() bool }); ok && !req.HasResponse() {
		return true
	}

	if fetchReq, isFetch := msg.(*fetch.Request); isFetch {
		return e.handleFetchFrame(conn, def, apiVersion, correlationID, fetchReq)
	}

	resp := e.dispatch(def, apiVersion, msg, advertisedHost(conn))
	if resp == nil {
		return false // unsupported request type — nothing sane to send back
	}
	return writeKafkaResponse(conn, apiVersion, correlationID, resp) == nil
}

// handleFetchFrame builds and sends a Fetch response, then patches a real
// bug in kafka-go's RecordBatch v2 writer: it hardcodes each batch's
// base_offset to 0 (confirmed directly in
// github.com/segmentio/kafka-go/protocol/record_v2.go — `e.writeInt64(0)
// // base offset`), regardless of the actual starting offset of the
// records it's given. A real client computes each record's absolute offset
// as base_offset+offset_delta; with base_offset always wrong whenever
// FetchOffset isn't itself 0 — i.e., any consumer that's already caught up
// and is fetching whatever comes next, which is the ordinary case once a
// long-lived consumer has been running for more than one poll — every
// record in the batch gets mislabeled as starting at offset 0. kafka-go's
// own (lenient) decoder doesn't care, but a stricter one does: confirmed
// against a real kcat/librdkafka client, which silently discarded such
// batches and eventually gave up reporting "message might be too large to
// fetch" — a response that was otherwise byte-perfect (valid CRC, correct
// lengths end to end, hand-verified against the wire format).
func (e *Engine) handleFetchFrame(conn net.Conn, def *mock.Definition, apiVersion int16, correlationID int32, req *fetch.Request) bool {
	resp := e.handleFetch(def, req, apiVersion)

	fetchOffsets := make([]int64, 0, len(req.Topics))
	for _, t := range req.Topics {
		for _, p := range t.Partitions {
			fetchOffsets = append(fetchOffsets, p.FetchOffset)
		}
	}

	writeResponseMu.Lock()
	defer writeResponseMu.Unlock()
	var buf bytes.Buffer
	if err := protocol.WriteResponse(&buf, apiVersion, correlationID, resp); err != nil {
		return false
	}
	patchFetchResponseBaseOffsets(buf.Bytes(), apiVersion, fetchOffsets)
	_, err := conn.Write(buf.Bytes())
	return err == nil
}

// patchFetchResponseBaseOffsets walks an already-encoded Fetch response
// buffer field by field — mirroring the exact wire layout kafka-go's own
// reflection codec produces for the given apiVersion (which fields are
// present varies by version; see the min/max tags on
// github.com/segmentio/kafka-go/protocol/fetch.Response and friends) —
// and overwrites each non-empty partition's RecordBatch base_offset (the
// first 8 bytes right after its 4-byte length prefix) with that
// partition's real FetchOffset, in request order. Limited to the API
// version range this engine actually advertises (see handleAPIVersions) —
// Fetch stays non-flexible (regular int32/int16-prefixed arrays and
// strings, not KIP-482 compact encoding) throughout v0-v11, so a single
// fixed field-by-field walk covers all of them; anything outside that
// range is left untouched rather than risk misparsing a layout this
// function was never verified against.
// fetchBufCursor is a tiny forward-only cursor over an already-encoded
// response buffer — just enough to walk fixed-size and length-prefixed
// fields without a full decoder, since patchFetchResponseBaseOffsets only
// ever needs to skip over fields it isn't changing.
type fetchBufCursor struct {
	buf []byte
	pos int
}

func (c *fetchBufCursor) skip(n int) { c.pos += n }

func (c *fetchBufCursor) readI32() int32 {
	v := int32(binary.BigEndian.Uint32(c.buf[c.pos:]))
	c.pos += 4
	return v
}

func (c *fetchBufCursor) skipString() { c.skip(int(c.readI16())) }

func (c *fetchBufCursor) readI16() int16 {
	v := int16(binary.BigEndian.Uint16(c.buf[c.pos:]))
	c.pos += 2
	return v
}

func patchFetchResponseBaseOffsets(buf []byte, apiVersion int16, fetchOffsets []int64) {
	if apiVersion < 0 || apiVersion > 11 {
		return
	}
	c := &fetchBufCursor{buf: buf}
	c.skip(8) // size + correlation_id
	if apiVersion >= 1 {
		c.skip(4) // throttle_time_ms
	}
	if apiVersion >= 7 {
		c.skip(6) // error_code(2) + session_id(4)
	}
	topicsCount := c.readI32()
	idx := 0
	for t := int32(0); t < topicsCount; t++ {
		c.skipString() // topic name
		partitionsCount := c.readI32()
		for p := int32(0); p < partitionsCount; p++ {
			var fetchOffset int64
			if idx < len(fetchOffsets) {
				fetchOffset = fetchOffsets[idx]
			}
			c.patchOnePartition(apiVersion, fetchOffset)
			idx++
		}
	}
}

// patchOnePartition skips every ResponsePartition field it isn't touching
// (matching kafka-go's own min/max version tags on fetch.ResponsePartition
// field for field) and, if the partition carries a non-empty RecordBatch,
// overwrites its base_offset with fetchOffset.
func (c *fetchBufCursor) patchOnePartition(apiVersion int16, fetchOffset int64) {
	c.skip(4 + 2 + 8) // partition_index + error_code + high_watermark
	if apiVersion >= 4 {
		c.skip(8) // last_stable_offset
	}
	if apiVersion >= 5 {
		c.skip(8) // log_start_offset
	}
	if apiVersion >= 4 {
		abortedCount := c.readI32()
		c.skip(int(abortedCount) * 16) // producer_id(8) + first_offset(8) each
	}
	if apiVersion >= 11 {
		c.skip(4) // preferred_read_replica
	}
	recordsLen := c.readI32()
	if recordsLen > 0 {
		binary.BigEndian.PutUint64(c.buf[c.pos:c.pos+8], uint64(fetchOffset))
		c.skip(int(recordsLen))
	}
}

func (e *Engine) dispatch(def *mock.Definition, apiVersion int16, msg protocol.Message, host string) protocol.Message {
	switch req := msg.(type) {
	case *apiversions.Request:
		return handleAPIVersions(0)
	case *metadata.Request:
		return e.handleMetadata(def, req, host)
	case *produce.Request:
		return e.handleProduce(def, req)
	case *fetch.Request:
		return e.handleFetch(def, req, apiVersion)
	case *listoffsets.Request:
		return e.handleListOffsets(def, req)
	default:
		return nil
	}
}

// handleAPIVersions advertises the exact version ranges these four
// handlers actually support — every version within each range is handled
// automatically by kafka-go's own reflection-tag codec, so there's no
// separate per-version code path to keep in sync here. errorCode is 0 for
// a normal in-range ApiVersions request, or unsupportedVersion (35) for
// the bootstrap-fallback case in handleFrame — real Kafka brokers still
// return the full supported-API list even in the error case, specifically
// so a client that guessed too high a version can learn what to retry
// with instead.
func handleAPIVersions(errorCode int16) *apiversions.Response {
	return &apiversions.Response{
		ErrorCode: errorCode,
		ApiKeys: []apiversions.ApiKeyResponse{
			{ApiKey: int16(protocol.ApiVersions), MinVersion: 0, MaxVersion: 2},
			{ApiKey: int16(protocol.Metadata), MinVersion: 0, MaxVersion: 8},
			{ApiKey: int16(protocol.Produce), MinVersion: 0, MaxVersion: 8},
			{ApiKey: int16(protocol.Fetch), MinVersion: 0, MaxVersion: 11},
			{ApiKey: int16(protocol.ListOffsets), MinVersion: 1, MaxVersion: 5},
		},
	}
}

// handleListOffsets resolves the two sentinel timestamps a manual-offset
// consumer actually asks for in practice — -2 ("earliest", always 0 here
// since a topicLog never truncates) and -1 ("latest", the current high
// watermark). Any other literal timestamp (a real broker would binary
// search the log by wall-clock time) also resolves to the high watermark —
// exact time-based seeking isn't worth the complexity for a mock whose logs
// live only as long as the process does.
func (e *Engine) handleListOffsets(def *mock.Definition, req *listoffsets.Request) *listoffsets.Response {
	store := e.storeFor(def.ID)
	topics := make([]listoffsets.ResponseTopic, 0, len(req.Topics))
	for _, t := range req.Topics {
		log := store.get(t.Topic)
		highWatermark := log.highWatermark()
		parts := make([]listoffsets.ResponsePartition, 0, len(t.Partitions))
		for _, p := range t.Partitions {
			offset := highWatermark
			if p.Timestamp == -2 {
				offset = 0
			}
			parts = append(parts, listoffsets.ResponsePartition{Partition: p.Partition, Offset: offset, Timestamp: -1})
		}
		topics = append(topics, listoffsets.ResponseTopic{Topic: t.Topic, Partitions: parts})
	}
	return &listoffsets.Response{Topics: topics}
}

// advertisedHost is the address the client reached us on (the connection's
// local address), so a client on another machine is told to reconnect to a
// host it can actually route to rather than its own loopback.
func advertisedHost(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil || host == "" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "127.0.0.1"
	}
	return host
}

func (e *Engine) handleMetadata(def *mock.Definition, req *metadata.Request, host string) *metadata.Response {
	store := e.storeFor(def.ID)
	names := req.TopicNames
	if len(names) == 0 {
		// An empty/omitted topic list means "describe every topic" — report
		// every topic that's actually been produced/fetched so far, plus
		// every topic named in a Rule (as the match target or the reply
		// target), so a consumer can discover a reply topic before anything
		// has actually been written to it yet.
		seen := map[string]bool{}
		for _, n := range store.names() {
			seen[n] = true
		}
		if def.Kafka != nil {
			for _, r := range def.Kafka.Rules {
				if r.TopicPattern != "" {
					seen[r.TopicPattern] = true
				}
				if r.ReplyTopic != "" {
					seen[r.ReplyTopic] = true
				}
			}
		}
		for n := range seen {
			names = append(names, n)
		}
	}

	topics := make([]metadata.ResponseTopic, 0, len(names))
	for _, name := range names {
		store.get(name) // materialize it if this is the first time anyone's asked
		topics = append(topics, metadata.ResponseTopic{
			Name: name,
			Partitions: []metadata.ResponsePartition{
				{PartitionIndex: 0, LeaderID: brokerNodeID, ReplicaNodes: []int32{brokerNodeID}, IsrNodes: []int32{brokerNodeID}},
			},
		})
	}

	port := int32(0)
	if def.Kafka != nil {
		port = int32(def.Kafka.Port)
	}
	return &metadata.Response{
		Brokers:      []metadata.ResponseBroker{{NodeID: brokerNodeID, Host: host, Port: port}},
		ControllerID: brokerNodeID,
		Topics:       topics,
	}
}

func (e *Engine) handleProduce(def *mock.Definition, req *produce.Request) *produce.Response {
	store := e.storeFor(def.ID)
	respTopics := make([]produce.ResponseTopic, 0, len(req.Topics))

	for _, t := range req.Topics {
		log := store.get(t.Topic)
		respParts := make([]produce.ResponsePartition, 0, len(t.Partitions))
		for _, p := range t.Partitions {
			baseOffset := e.appendProducedRecords(def, store, log, t.Topic, p.RecordSet.Records)
			respParts = append(respParts, produce.ResponsePartition{Partition: p.Partition, BaseOffset: baseOffset})
		}
		respTopics = append(respTopics, produce.ResponseTopic{Topic: t.Topic, Partitions: respParts})
	}

	return &produce.Response{Topics: respTopics}
}

// appendProducedRecords drains every record out of a Produce request's
// partition, appending each to log and evaluating it against the mock's
// rules, returning the offset the FIRST record in the batch was assigned
// (what Produce's response BaseOffset field means — later records in the
// same batch are understood to have sequential offsets from there). store
// is passed in separately from log since a matched rule's reply is
// appended onto its OWN topic's log, which is usually a different topic
// than the one just produced to.
func (e *Engine) appendProducedRecords(def *mock.Definition, store *topicStore, log *topicLog, topic string, records protocol.RecordReader) int64 {
	if records == nil {
		return 0
	}
	baseOffset := int64(0)
	sawFirst := false
	for {
		rec, err := records.ReadRecord()
		if err != nil {
			break // io.EOF (or a decode error there's nothing more to do about) ends the batch
		}
		offset := e.appendOneRecord(def, store, log, topic, rec)
		if !sawFirst {
			baseOffset = offset
			sawFirst = true
		}
	}
	return baseOffset
}

// appendOneRecord stores a single produced record, evaluates it against the
// mock's rules, records the hit, and — unless a fault suppresses it —
// appends a matched rule's reply onto its own topic. Split out from
// appendProducedRecords purely to keep that loop's body to one call.
func (e *Engine) appendOneRecord(def *mock.Definition, store *topicStore, log *topicLog, topic string, rec *protocol.Record) int64 {
	key, _ := protocol.ReadAll(rec.Key)
	value, _ := protocol.ReadAll(rec.Value)
	ts := rec.Time.UnixMilli()
	if rec.Time.IsZero() {
		ts = time.Now().UnixMilli()
	}
	offset := log.append(key, value, ts)

	replyTopic, replyPayload := evaluateKafkaRules(def.Kafka, topic, string(value), def.ID, e.dynamicValues)
	// Record before applying the reply — the reply itself becomes
	// observable to a consumer polling Fetch, which is what a test/caller
	// actually synchronizes on, so logging must happen first to guarantee
	// it's visible by the time that's possible.
	e.recordHit(def, topic, value, replyTopic, replyPayload)
	if replyTopic == "" {
		return offset
	}
	switch mock.RollFault(def.Fault) {
	case "timeout", "error":
		return offset
	}
	if def.Fault != nil && def.Fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(def.Fault.LatencyJitterMs))
	}
	store.get(replyTopic).append(nil, []byte(replyPayload), time.Now().UnixMilli())
	return offset
}

// emptyAwareRecordSet builds a RecordSet for one Fetch response partition,
// working around a real bug in kafka-go's RecordBatch v2 writer
// (protocol.RecordSet.writeToVersion2): it hard-fails with ErrNoRecord for
// a genuinely empty batch, rather than writing a valid empty one the way
// its v1 (MessageSet) counterpart does. Since ErrNoRecord propagates all
// the way up through protocol.WriteResponse, ANY partition with zero new
// records to return — the single most common Fetch outcome, "polled,
// nothing new yet" — silently killed the ENTIRE response (every topic and
// partition in it, not just the empty one), confirmed by watching kafka-go
// itself produce zero bytes and close the connection. Version 1's writer
// correctly returns nil for zero records (verified directly against
// kafka-go: it encodes as a valid 4-byte "0 records" marker instead of
// erroring), so it's used here whenever there's nothing to send —
// regardless of which Fetch API version was actually negotiated, since a
// real client interprets an empty records field identically either way.
// The requested version is used as normal whenever there IS data.
func emptyAwareRecordSet(version int8, records []protocol.Record) protocol.RecordSet {
	if len(records) == 0 {
		version = 1
	}
	return protocol.RecordSet{Version: version, Records: protocol.NewRecordReader(records...)}
}

// fetchPollInterval is how often handleFetch rechecks for new data while
// long-polling — short enough that new data shows up promptly, long enough
// not to busy-spin the mutex.
const fetchPollInterval = 20 * time.Millisecond

// handleFetch blocks (like a real broker) until at least one requested
// partition has new data, or MaxWaitTime elapses — it does NOT respond
// instantly to an empty poll. Without this, a real client polling an idle
// topic hammers the connection with thousands of Fetch requests per second
// (confirmed: 9.5MB of client-side protocol log in a few seconds), which is
// both wasteful and, in at least one observed case against kcat/librdkafka,
// eventually tripped a client-side "message too large" parse error under
// that sustained rapid-fire load — a real broker's actual long-poll
// blocking never produces traffic dense enough to hit whatever edge case
// that was.
func (e *Engine) handleFetch(def *mock.Definition, req *fetch.Request, apiVersion int16) *fetch.Response {
	store := e.storeFor(def.ID)
	// Real Kafka only switches Fetch responses to RecordBatch v2 starting at
	// Fetch API v4 (older versions expect the legacy MessageSet v1 format) —
	// mirrored here since a real client library picks its decoding path
	// based on the version IT negotiated, not by sniffing the bytes.
	recordSetVersion := int8(1)
	if apiVersion >= 4 {
		recordSetVersion = 2
	}

	deadline := time.Now().Add(time.Duration(req.MaxWaitTime) * time.Millisecond)
	for {
		topics, anyNew := e.buildFetchResponse(store, req, recordSetVersion)
		if anyNew || req.MaxWaitTime <= 0 || !time.Now().Before(deadline) {
			return &fetch.Response{Topics: topics}
		}
		time.Sleep(fetchPollInterval)
	}
}

// buildFetchResponse builds one Fetch response snapshot and reports whether
// any partition in it actually had new data — the signal handleFetch polls
// on to decide whether to keep waiting.
func (e *Engine) buildFetchResponse(store *topicStore, req *fetch.Request, recordSetVersion int8) (topics []fetch.ResponseTopic, anyNew bool) {
	topics = make([]fetch.ResponseTopic, 0, len(req.Topics))
	for _, t := range req.Topics {
		log := store.get(t.Topic)
		parts := make([]fetch.ResponsePartition, 0, len(t.Partitions))
		for _, p := range t.Partitions {
			stored, highWatermark := log.from(p.FetchOffset)
			if len(stored) > 0 {
				anyNew = true
			}
			records := make([]protocol.Record, len(stored))
			for i, r := range stored {
				records[i] = protocol.Record{
					Offset: p.FetchOffset + int64(i),
					Time:   time.UnixMilli(r.timestampMs),
					Key:    protocol.NewBytes(r.key),
					Value:  protocol.NewBytes(r.value),
				}
			}
			parts = append(parts, fetch.ResponsePartition{
				Partition:     p.Partition,
				HighWatermark: highWatermark,
				// LastStableOffset tells a read-committed consumer how far
				// it's safe to read up to when a transaction might still be
				// in flight. This mock never has an in-flight transaction —
				// every record is stable the instant it's appended — so it
				// must always equal HighWatermark. Left unset (Go zero
				// value 0), a real client (confirmed against kcat/
				// librdkafka) compares its fetch offset against THIS field
				// rather than HighWatermark to decide whether there's
				// anything new: with both stuck at 0, it never surfaces a
				// single record no matter how many are actually in the
				// response, silently reporting "reached end of topic"
				// instead.
				LastStableOffset: highWatermark,
				// PreferredReadReplica only exists at Fetch v11+, and its
				// zero value (0) is NOT "no preference" — it's broker ID 0,
				// a real (if here nonexistent, since this mock's only
				// broker is brokerNodeID=1) redirect target. Left unset, a
				// real client (confirmed against kcat/librdkafka) reads a
				// response that BOTH contains records AND claims to want a
				// redirect, calls that contradictory, discards the records,
				// and retries forever trying to locate replica 0. -1 is the
				// actual "no redirect, this data is authoritative" sentinel.
				PreferredReadReplica: -1,
				RecordSet:            emptyAwareRecordSet(recordSetVersion, records),
			})
		}
		topics = append(topics, fetch.ResponseTopic{Topic: t.Topic, Partitions: parts})
	}
	return topics, anyNew
}

// evaluateKafkaRules is first-match-wins over cfg.Rules: a rule matches
// when its TopicPattern equals the produced-to topic AND (if PayloadMatch
// is set) the record's value also matches. The matched rule's
// ReplyTopic/ReplyPayload are rendered via text/template+sprig with
// {{.Request.Body}} bound to the received value before being returned.
func evaluateKafkaRules(cfg *mock.KafkaConfig, topic, payload, ownerID string, dv mock.DynamicValueSource) (replyTopic, replyPayload string) {
	if cfg == nil {
		return "", ""
	}
	for _, r := range cfg.Rules {
		if r.TopicPattern != topic {
			continue
		}
		if r.PayloadMatch != "" && !kafkaPayloadMatches(r, payload) {
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

func kafkaPayloadMatches(r mock.KafkaRule, payload string) bool {
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
