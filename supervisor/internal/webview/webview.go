// Package webview implements the optional, untrusted, read-only presentation
// client for supervisor journals and transcripts. The CLI remains the required
// claim-bearing frontend; this package is not in the claim path and makes no
// security or containment claim.
//
// Thesis scoping: MSE_MA_Thesis/agentic-harness/tbound-thesis.md (around lines
// 113, 463, and 963) keeps the web UI optional; AH-technical/06-1-language-
// decision.md (lines 11 and 16) calls a browser client deferred and notes that
// remote access needs separate authentication; AH-technical/00-roadmap.md:77,
// todo-implementation-tbound.md:60, and visualizations-plan.md:417,543 keep
// polished dashboards outside the claim-bearing package.
package webview

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	// DefaultBufferCapacity bounds replay history and memory retained by the
	// presentation client. Newer records evict the oldest records.
	DefaultBufferCapacity = 64
	// DefaultMaxLineBytes bounds input framing. Overlong lines are reported as
	// malformed records with a bounded preview, not silently discarded.
	DefaultMaxLineBytes = 1 << 20
	DefaultPollInterval = 100 * time.Millisecond
	subscriberQueueSize = 64
)

// Record is one observed JSONL line. Record contains the parsed value for valid
// JSON; malformed lines retain their original text and a parse error instead.
type Record struct {
	ID         uint64          `json:"id"`
	Source     string          `json:"source"`
	Types      []string        `json:"types"`
	ReceivedAt time.Time       `json:"received_at"`
	Record     json.RawMessage `json:"record,omitempty"`
	RawLine    string          `json:"raw_line,omitempty"`
	ParseError string          `json:"parse_error,omitempty"`
}

// Buffer stores a bounded, ordered replay window and broadcasts new records to
// SSE clients. Slow clients are disconnected rather than silently losing
// events; reconnecting clients receive the current replay window.
type Buffer struct {
	mu       sync.Mutex
	capacity int
	items    []Record
	start    int
	count    int
	nextID   uint64
	nextSub  uint64
	subs     map[uint64]chan Record
}

// NewBuffer creates a ring buffer. Non-positive capacities use the default.
func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = DefaultBufferCapacity
	}
	return &Buffer{
		capacity: capacity,
		items:    make([]Record, capacity),
		subs:     make(map[uint64]chan Record),
	}
}

// Snapshot returns the current buffer in oldest-to-newest order.
func (b *Buffer) Snapshot() []Record {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

func (b *Buffer) snapshotLocked() []Record {
	result := make([]Record, 0, b.count)
	for index := 0; index < b.count; index++ {
		item := b.items[(b.start+index)%b.capacity]
		result = append(result, cloneRecord(item))
	}
	return result
}

func (b *Buffer) publish(record Record) Record {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	record.ID = b.nextID
	if record.ReceivedAt.IsZero() {
		record.ReceivedAt = time.Now().UTC()
	}
	record = cloneRecord(record)
	if b.count < b.capacity {
		index := (b.start + b.count) % b.capacity
		b.items[index] = record
		b.count++
	} else {
		b.items[b.start] = record
		b.start = (b.start + 1) % b.capacity
	}
	for id, subscriber := range b.subs {
		select {
		case subscriber <- cloneRecord(record):
		default:
			// Do not hide dropped live events. Close this slow subscriber so a
			// reconnect gets a consistent replay from the bounded ring.
			delete(b.subs, id)
			close(subscriber)
		}
	}
	return cloneRecord(record)
}

// Subscription atomically captures the current replay window and begins
// receiving records published after that snapshot.
type Subscription struct {
	Initial []Record
	Records <-chan Record
	buffer  *Buffer
	id      uint64
	once    sync.Once
}

// Subscribe returns an atomic replay snapshot and a live event channel.
func (b *Buffer) Subscribe() *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextSub++
	channel := make(chan Record, subscriberQueueSize)
	b.subs[b.nextSub] = channel
	return &Subscription{
		Initial: b.snapshotLocked(),
		Records: channel,
		buffer:  b,
		id:      b.nextSub,
	}
}

// Close unregisters the subscription. It is safe to call more than once.
func (s *Subscription) Close() {
	if s == nil || s.buffer == nil {
		return
	}
	s.once.Do(func() {
		s.buffer.mu.Lock()
		defer s.buffer.mu.Unlock()
		if channel, ok := s.buffer.subs[s.id]; ok {
			delete(s.buffer.subs, s.id)
			close(channel)
		}
	})
}

func cloneRecord(record Record) Record {
	record.Types = append([]string(nil), record.Types...)
	record.Record = append(json.RawMessage(nil), record.Record...)
	return record
}

// NewHandler returns the read-only HTTP presentation endpoints. The page and
// endpoints expose buffered observations only; no route accepts a mutation.
func NewHandler(buffer *Buffer) http.Handler {
	if buffer == nil {
		buffer = NewBuffer(DefaultBufferCapacity)
	}
	return newHTTPHandler(buffer)
}
