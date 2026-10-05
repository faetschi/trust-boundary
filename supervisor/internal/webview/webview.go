// Package webview implements the optional, untrusted, read-only presentation
// client for supervisor journals, transcripts, and external Go test runs. The
// CLI remains the required claim-bearing frontend; this package is not in the
// claim path and makes no security or containment claim.
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
	"sort"
	"sync"
	"time"
)

const (
	// DefaultBufferCapacity bounds replay history and memory retained by the
	// presentation client. Newer records evict the oldest records.
	DefaultBufferCapacity = 64
	// DefaultMaxLineBytes bounds input framing. Overlong lines are reported as
	// malformed records with a bounded preview, not silently discarded.
	DefaultMaxLineBytes  = 1 << 20
	DefaultPollInterval  = 100 * time.Millisecond
	subscriberQueueSize  = 8
	defaultHistoryBytes  = 16 << 20
	maxBufferCapacity    = 256
	maxSubscribers       = 8
	maxConfiguredSources = 32
)

// Record is one observed JSONL line. Record contains the parsed value for valid
// JSON; malformed lines retain their original text and a parse error instead.
type Record struct {
	ID          uint64          `json:"id"`
	Source      string          `json:"source"`
	SourceEpoch string          `json:"source_epoch,omitempty"`
	Types       []string        `json:"types"`
	ReceivedAt  time.Time       `json:"received_at"`
	Record      json.RawMessage `json:"record,omitempty"`
	RawLine     string          `json:"raw_line,omitempty"`
	ParseError  string          `json:"parse_error,omitempty"`
}

// Buffer stores a bounded, ordered replay window and broadcasts new records to
// SSE clients. Slow clients are disconnected rather than silently losing
// events; reconnecting clients receive the current replay window.
type Buffer struct {
	mu           sync.Mutex
	capacity     int
	items        []Record
	start        int
	count        int
	nextID       uint64
	nextSub      uint64
	subs         map[uint64]chan Record
	epoch        string
	historyPath  string
	historyErr   string
	sources      []SourceInfo
	checkpoints  map[string]SourceCheckpoint
	warnings     []string
	run          *TestRunManifest
	runError     string
	tests        map[string]TestCase
	packages     map[string]TestPackage
	droppedTests int
	retainedRuns []RetainedRun
	audit        AuditProjection
	generations  GenerationProjection
}

// NewBuffer creates a ring buffer. Non-positive capacities use the default.
func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = DefaultBufferCapacity
	}
	if capacity > maxBufferCapacity {
		capacity = maxBufferCapacity
	}
	return &Buffer{
		capacity:    capacity,
		items:       make([]Record, capacity),
		subs:        make(map[uint64]chan Record),
		checkpoints: make(map[string]SourceCheckpoint),
		epoch:       newEpoch(),
		tests:       make(map[string]TestCase),
		packages:    make(map[string]TestPackage),
		audit:       AuditProjection{SchemaVersion: "tbound-audit-projection/v1", Status: "not-configured"},
		generations: GenerationProjection{SchemaVersion: "tbound-generation-projection/v1", Status: "not-configured"},
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
	return b.publishLocked(record)
}

// publishLocked appends and broadcasts one observation while the caller holds
// b.mu. Typed catalog/projection updates use it to make state and cursor changes
// visible as one atomic snapshot.
func (b *Buffer) publishLocked(record Record) Record {
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
	b.persistLocked()
	return cloneRecord(record)
}

// Epoch identifies this observation stream. It remains stable when a bounded
// history file is configured, and changes when the server starts without one.
func (b *Buffer) Epoch() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.epoch
}

func (b *Buffer) cursor() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return formatCursor(b.epoch, b.nextID)
}

// SubscribeAfter atomically returns retained events after cursor and a live
// subscription. A gap is explicit when the epoch changed, history was evicted,
// or the cursor is ahead of this source.
func (b *Buffer) SubscribeAfter(cursor string) (*Subscription, *ReplayGap) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs) >= maxSubscribers {
		closed := make(chan Record)
		close(closed)
		return &Subscription{Initial: b.snapshotLocked(), Records: closed, buffer: b}, &ReplayGap{SchemaVersion: "tbound-replay-gap/v1", Reason: "subscriber_limit", RequestedCursor: truncateUTF8(cursor, 256), Epoch: b.epoch, Latest: b.nextID}
	}
	b.nextSub++
	channel := make(chan Record, subscriberQueueSize)
	b.subs[b.nextSub] = channel
	all := b.snapshotLocked()
	subscription := &Subscription{Initial: all, Records: channel, buffer: b, id: b.nextSub}
	if cursor == "" {
		return subscription, nil
	}
	epoch, sequence, ok := parseCursor(cursor)
	gap := &ReplayGap{SchemaVersion: "tbound-replay-gap/v1", RequestedCursor: truncateUTF8(cursor, 256), Epoch: b.epoch}
	if len(cursor) > 256 || !ok {
		gap.Reason = "invalid_cursor"
		return subscription, gap
	}
	if epoch != b.epoch {
		gap.Reason = "source_epoch_changed"
		if len(all) > 0 {
			gap.Earliest = all[0].ID
		}
		gap.Latest = b.nextID
		return subscription, gap
	}
	if sequence > b.nextID {
		gap.Reason = "cursor_ahead"
		gap.Latest = b.nextID
		return subscription, gap
	}
	if len(all) > 0 && sequence < all[0].ID && all[0].ID-sequence > 1 {
		gap.Reason = "history_evicted"
		gap.Earliest = all[0].ID
		gap.Latest = b.nextID
		return subscription, gap
	}
	filtered := make([]Record, 0, len(all))
	for _, item := range all {
		if item.ID > sequence {
			filtered = append(filtered, item)
		}
	}
	subscription.Initial = filtered
	return subscription, nil
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
	subscription, _ := b.SubscribeAfter("")
	return subscription
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

// NewHandler returns the read-only HTTP presentation endpoints. The page and
// endpoints expose bounded observations only; no route accepts a mutation.
func NewHandler(buffer *Buffer) http.Handler {
	if buffer == nil {
		buffer = NewBuffer(DefaultBufferCapacity)
	}
	return newHTTPHandler(buffer)
}

func (b *Buffer) snapshot() ExplorerSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	tests := make([]TestCase, 0, len(b.tests))
	for _, item := range b.tests {
		tests = append(tests, item)
	}
	sort.Slice(tests, func(i, j int) bool {
		if tests[i].Package == tests[j].Package {
			return tests[i].Name < tests[j].Name
		}
		return tests[i].Package < tests[j].Package
	})
	packages := make([]TestPackage, 0, len(b.packages))
	for _, item := range b.packages {
		packages = append(packages, item)
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Name < packages[j].Name })
	records := b.snapshotLocked()
	earliest := uint64(0)
	if len(records) > 0 {
		earliest = records[0].ID
	}
	manifest := ObservationManifest{
		SchemaVersion: "tbound-observation-manifest/v1",
		Epoch:         b.epoch,
		Sources:       append([]SourceInfo(nil), b.sources...),
		History:       HistoryStatus{Enabled: b.historyPath != "", Healthy: b.historyErr == "", Error: b.historyErr},
		Warnings:      append([]string{"observations are untrusted and nonauthoritative", "the required CLI remains claim-bearing", "this viewer has no execution or mutation controls"}, b.warnings...),
	}
	if b.run != nil {
		copy := *b.run
		copy.Packages = append([]string(nil), b.run.Packages...)
		copy.SourceIdentity = cloneSourceIdentity(b.run.SourceIdentity)
		manifest.TestRun = &copy
	}
	manifest.TestRunError = b.runError
	return ExplorerSnapshot{
		SchemaVersion: "tbound-observation-snapshot/v1",
		Epoch:         b.epoch, Cursor: formatCursor(b.epoch, b.nextID), EarliestID: earliest,
		Records: records, Manifest: manifest, Tests: TestCatalog{Packages: packages, Tests: tests, Dropped: b.droppedTests},
		Audit: cloneAuditProjection(b.audit), Generations: cloneGenerationProjection(b.generations),
	}
}

func (b *Buffer) retainedRunCatalog() RetainedRunCatalog {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := RetainedRunCatalog{SchemaVersion: "tbound-retained-run-catalog/v1", Runs: make([]RetainedRun, 0, len(b.retainedRuns)+1)}
	for _, run := range b.retainedRuns {
		result.Runs = append(result.Runs, cloneRetainedRun(run))
	}
	if b.run != nil {
		result.CurrentRunID = b.run.RunID
		if !hasRetainedRun(result.Runs, b.run.RunID) {
			result.Runs = append(result.Runs, b.retainedRunLocked())
		}
	}
	sort.SliceStable(result.Runs, func(i, j int) bool { return result.Runs[i].Run.StartedAt.After(result.Runs[j].Run.StartedAt) })
	if len(result.Runs) > maxRetainedRuns {
		currentIndex := -1
		for i := range result.Runs {
			if result.Runs[i].Run.RunID == result.CurrentRunID {
				currentIndex = i
				break
			}
		}
		bounded := make([]RetainedRun, 0, maxRetainedRuns)
		if currentIndex >= 0 {
			bounded = append(bounded, result.Runs[currentIndex])
		}
		for i := range result.Runs {
			if i == currentIndex || len(bounded) >= maxRetainedRuns {
				continue
			}
			bounded = append(bounded, result.Runs[i])
		}
		sort.SliceStable(bounded, func(i, j int) bool { return bounded[i].Run.StartedAt.After(bounded[j].Run.StartedAt) })
		result.Runs = bounded
	}
	return result
}

func (b *Buffer) retainedRun(runID string) (RetainedRun, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, run := range b.retainedRuns {
		if run.Run.RunID == runID {
			return cloneRetainedRun(run), true
		}
	}
	if b.run != nil && b.run.RunID == runID {
		return b.retainedRunLocked(), true
	}
	return RetainedRun{}, false
}

func hasRetainedRun(runs []RetainedRun, runID string) bool {
	for _, run := range runs {
		if run.Run.RunID == runID {
			return true
		}
	}
	return false
}

func (b *Buffer) retainedRunLocked() RetainedRun {
	if b.run == nil {
		return RetainedRun{}
	}
	return summarizeRun(*b.run, b.tests, b.packages, b.droppedTests, time.Now().UTC())
}

func (b *Buffer) archiveCurrentRunLocked() {
	if b.run == nil || b.run.State == "running" {
		return
	}
	for index := range b.retainedRuns {
		if b.retainedRuns[index].Run.RunID == b.run.RunID {
			copy(b.retainedRuns[index:], b.retainedRuns[index+1:])
			b.retainedRuns = b.retainedRuns[:len(b.retainedRuns)-1]
			break
		}
	}
	b.retainedRuns = append(b.retainedRuns, summarizeRun(*b.run, b.tests, b.packages, b.droppedTests, time.Now().UTC()))
	b.trimRetainedRunsLocked(maxRetainedRuns)
}

func (b *Buffer) trimRetainedRunsLocked(limit int) {
	if len(b.retainedRuns) > limit {
		b.retainedRuns = append([]RetainedRun(nil), b.retainedRuns[len(b.retainedRuns)-limit:]...)
	}
}

func summarizeRun(run TestRunManifest, tests map[string]TestCase, packages map[string]TestPackage, dropped int, retainedAt time.Time) RetainedRun {
	copyRun := run
	copyRun.Packages = append([]string(nil), run.Packages...)
	copyRun.SourceIdentity = cloneSourceIdentity(run.SourceIdentity)
	testRows := make([]TestCase, 0, len(tests))
	for _, item := range tests {
		testRows = append(testRows, item)
	}
	sort.Slice(testRows, func(i, j int) bool {
		if testRows[i].Package == testRows[j].Package {
			return testRows[i].Name < testRows[j].Name
		}
		return testRows[i].Package < testRows[j].Package
	})
	testLimitDropped := max(0, len(testRows)-maxRetainedRunTests)
	if len(testRows) > maxRetainedRunTests {
		testRows = testRows[:maxRetainedRunTests]
	}
	for i := range testRows {
		testRows[i].Output = ""
		testRows[i].OutputTruncated = true
	}
	packageRows := make([]TestPackage, 0, len(packages))
	for _, item := range packages {
		packageRows = append(packageRows, item)
	}
	sort.Slice(packageRows, func(i, j int) bool { return packageRows[i].Name < packageRows[j].Name })
	packageLimitDropped := max(0, len(packageRows)-maxRetainedRunPackages)
	if len(packageRows) > maxRetainedRunPackages {
		packageRows = packageRows[:maxRetainedRunPackages]
	}
	for i := range packageRows {
		packageRows[i].Output = ""
		packageRows[i].OutputTruncated = true
	}
	return RetainedRun{Run: copyRun, Tests: TestCatalog{Packages: packageRows, Tests: testRows, Dropped: dropped + testLimitDropped + packageLimitDropped}, RetainedAt: retainedAt, OutputPolicy: "omitted-from-retained-run-summaries"}
}

func cloneRetainedRun(value RetainedRun) RetainedRun {
	value.Run.Packages = append([]string(nil), value.Run.Packages...)
	value.Run.SourceIdentity = cloneSourceIdentity(value.Run.SourceIdentity)
	value.Tests.Tests = append([]TestCase(nil), value.Tests.Tests...)
	value.Tests.Packages = append([]TestPackage(nil), value.Tests.Packages...)
	return value
}

func cloneRecord(record Record) Record {
	record.Types = append([]string(nil), record.Types...)
	record.Record = append(json.RawMessage(nil), record.Record...)
	return record
}
