package kafkaengine

import "sync"

// storedRecord is one produced record as kept in a topicLog — just enough
// to serve it back out again on a later Fetch.
type storedRecord struct {
	key, value  []byte
	timestampMs int64
}

// topicLog is a single-partition, append-only, in-memory record log — real
// Kafka mocking doesn't need multiple partitions or on-disk persistence for
// a test double; one ordered, growable slice per topic is enough for a
// manual-offset consumer to Fetch whatever's been Produced, in order, from
// any earlier offset, including one that starts polling after the fact.
type topicLog struct {
	mu      sync.Mutex
	records []storedRecord
}

// append adds one record and returns the offset it was assigned.
func (t *topicLog) append(key, value []byte, timestampMs int64) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	offset := int64(len(t.records))
	t.records = append(t.records, storedRecord{key: key, value: value, timestampMs: timestampMs})
	return offset
}

// highWatermark returns the offset the next appended record will get —
// what a real broker calls the "latest" offset when resolving a
// ListOffsets(-1) request.
func (t *topicLog) highWatermark() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return int64(len(t.records))
}

// from returns a copy of every record at or after fromOffset, plus the
// log's current high watermark (== len(records), the offset the next
// appended record will get). An out-of-range fromOffset (negative, or at/
// past the high watermark — the normal "caught up, nothing new yet" case)
// returns no records without it being an error.
func (t *topicLog) from(fromOffset int64) (records []storedRecord, highWatermark int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	highWatermark = int64(len(t.records))
	if fromOffset < 0 || fromOffset >= highWatermark {
		return nil, highWatermark
	}
	return append([]storedRecord(nil), t.records[fromOffset:]...), highWatermark
}

// topicStore is all topic logs for one mock. A log is created lazily on
// first reference — Produce, Fetch, or Metadata for a topic name never seen
// before all just materialize an empty log for it, mirroring how a real
// broker with auto-topic-creation enabled behaves, since this mock has no
// separate "create topic" administrative step.
type topicStore struct {
	mu     sync.Mutex
	topics map[string]*topicLog
}

func newTopicStore() *topicStore {
	return &topicStore{topics: map[string]*topicLog{}}
}

func (s *topicStore) get(name string) *topicLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.topics[name]
	if !ok {
		t = &topicLog{}
		s.topics[name] = t
	}
	return t
}

// names lists every topic this store already knows about — used to answer
// a Metadata request with an empty/omitted topic list ("describe
// everything").
func (s *topicStore) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.topics))
	for name := range s.topics {
		out = append(out, name)
	}
	return out
}
