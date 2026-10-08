package governanceview

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var fallbackEpoch uint64

// Store is a viewer-owned bounded ring. It does not persist, authorize, or
// alter any supervisor state. Slow subscribers are closed so a browser cannot
// become backpressure on a native Pi/runtime path.
type Store struct {
	mu       sync.Mutex
	capacity int
	epoch    string
	next     uint64
	records  []Record
	seen     map[string]struct{}
	subs     map[uint64]chan Record
	nextSub  uint64
	base     Snapshot
}

// NewStore validates and copies an initial snapshot. An empty snapshot is not
// accepted because unavailable is a truthful initial state and can be built by
// NewUnavailableSnapshot.
func NewStore(snapshot Snapshot, capacity int) (*Store, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	if snapshot.Verification == "" {
		snapshot.Verification = defaultVerificationScope(snapshot.Mode)
	}
	for index := range snapshot.Records {
		if snapshot.Records[index].Verification == "" {
			snapshot.Records[index].Verification = snapshot.Verification
		}
	}
	for index := range snapshot.Actions {
		if snapshot.Actions[index].Verification == "" {
			snapshot.Actions[index].Verification = snapshot.Verification
		}
	}
	if capacity <= 0 {
		capacity = DefaultRecordCapacity
	}
	if capacity > MaxRecordCapacity {
		capacity = MaxRecordCapacity
	}
	if len(snapshot.Records) > capacity {
		return nil, fmt.Errorf("snapshot has %d records but store capacity is %d", len(snapshot.Records), capacity)
	}
	store := &Store{
		capacity: capacity,
		epoch:    snapshot.Epoch,
		records:  make([]Record, 0, capacity),
		seen:     make(map[string]struct{}, len(snapshot.Records)),
		subs:     make(map[uint64]chan Record),
		base:     cloneSnapshot(snapshot),
	}
	for index, record := range snapshot.Records {
		record.Sequence = uint64(index + 1)
		store.records = append(store.records, cloneRecord(record))
		store.seen[record.ID] = struct{}{}
		store.next = record.Sequence
	}
	store.base.Records = nil
	store.base.Cursor = formatCursor(store.epoch, store.next)
	store.base.GeneratedAt = time.Now().UTC()
	return store, nil
}

// NewUnavailableSnapshot is the default production-safe state. It deliberately
// contains no Pi/session/profile proof and no synthetic records.
func NewUnavailableSnapshot() Snapshot {
	epoch := newEpoch()
	return Snapshot{
		SchemaVersion: ContractVersion,
		Epoch:         epoch,
		Cursor:        formatCursor(epoch, 0),
		Mode:          ModeUnavailable,
		Verification:  VerificationUnavailable,
		GeneratedAt:   time.Now().UTC(),
		Session:       SessionHeader{Status: StateUnknown},
		Preflight:     []PreflightCheck{{Name: "production observation adapter", State: PreflightUnavailable, Detail: "no registered production observation source"}},
		Sources:       []Source{{ID: "production", Label: "production observations", Mode: ModeUnavailable, Health: SourceUnavailable, Available: false, Reason: "no live Pi/runtime observation is registered"}},
		Publication:   PublicationView{State: PublicationUnavailable, Detail: "publication state is unavailable"},
		Recovery:      RecoveryView{State: RecoveryUnavailable, Detail: "recovery evidence is unavailable"},
		Generation:    GenerationView{MetadataState: "unavailable", ContentsState: "unavailable", DiffState: "not-comparable", Note: "no production generation observation is registered"},
		ProjectTests:  []ProjectTestOutput{{Name: "Pi project tests", State: StateUnknown, Detail: "no project-test observation is registered"}},
		Warnings:      []string{"production observations are unavailable; this view has no launch, stop, approval, or execution controls"},
	}
}

// Snapshot returns a consistent bounded view.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// Record returns a retained registered record by ID. It never resolves a path
// or reads an arbitrary client-selected file.
func (s *Store) Record(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.ID == id {
			return cloneRecord(record), true
		}
	}
	return Record{}, false
}

// UpdateSource replaces one registered source-health projection. It only
// changes viewer metadata and never creates, verifies, or settles an effect.
func (s *Store) UpdateSource(source Source) error {
	if err := validateSource(source); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if source.Mode != s.base.Mode {
		return fmt.Errorf("source %q mode does not match snapshot mode", source.ID)
	}
	for index := range s.base.Sources {
		if s.base.Sources[index].ID == source.ID {
			s.base.Sources[index] = source
			return nil
		}
	}
	if len(s.base.Sources) >= MaxSources {
		return fmt.Errorf("source count exceeds %d", MaxSources)
	}
	s.base.Sources = append(s.base.Sources, source)
	return nil
}

func (s *Store) snapshotLocked() Snapshot {
	snapshot := cloneSnapshot(s.base)
	snapshot.Epoch = s.epoch
	snapshot.Cursor = formatCursor(s.epoch, s.next)
	snapshot.GeneratedAt = time.Now().UTC()
	snapshot.Records = make([]Record, len(s.records))
	for index, record := range s.records {
		snapshot.Records[index] = cloneRecord(record)
	}
	return snapshot
}

// Append adds one structurally validated observation. Duplicate registered IDs
// are ignored, which makes reconnect/replay delivery idempotent.
func (s *Store) Append(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.Mode == "" {
		record.Mode = s.base.Mode
	}
	if record.OccurredAt.IsZero() {
		record.OccurredAt = time.Now().UTC()
	}
	scope := s.base.Verification
	if scope == "" {
		scope = defaultVerificationScope(s.base.Mode)
	}
	if err := validateRecord(record, s.base.Mode, scope); err != nil {
		return err
	}
	if _, exists := s.seen[record.ID]; exists {
		return nil
	}
	s.next++
	record.Sequence = s.next
	if len(s.records) == s.capacity {
		delete(s.seen, s.records[0].ID)
		s.records = append(s.records[:0], s.records[1:]...)
	}
	s.records = append(s.records, cloneRecord(record))
	s.seen[record.ID] = struct{}{}
	for id, subscriber := range s.subs {
		select {
		case subscriber <- cloneRecord(record):
		default:
			delete(s.subs, id)
			close(subscriber)
		}
	}
	return nil
}

// Subscription is an atomic replay window plus a live bounded channel.
type Subscription struct {
	Initial []Record
	Records <-chan Record
	store   *Store
	id      uint64
	once    sync.Once
}

func (s *Store) SubscribeAfter(cursor string) (*Subscription, *ReplayGap) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) >= MaxSubscribers {
		closed := make(chan Record)
		close(closed)
		return &Subscription{Initial: s.currentRecordsLocked(), Records: closed, store: s}, &ReplayGap{SchemaVersion: ReplayGapVersion, Reason: "subscriber_limit", RequestedCursor: truncate(cursor, 256), Epoch: s.epoch, Latest: s.next}
	}
	s.nextSub++
	channel := make(chan Record, SubscriberQueueSize)
	s.subs[s.nextSub] = channel
	result := &Subscription{Initial: s.currentRecordsLocked(), Records: channel, store: s, id: s.nextSub}
	if cursor == "" {
		return result, nil
	}
	epoch, sequence, ok := parseCursor(cursor)
	gap := &ReplayGap{SchemaVersion: ReplayGapVersion, RequestedCursor: truncate(cursor, 256), Epoch: s.epoch, Latest: s.next}
	if len(cursor) > 256 || !ok {
		gap.Reason = "invalid_cursor"
		return result, gap
	}
	if epoch != s.epoch {
		gap.Reason = "source_epoch_changed"
		if len(s.records) > 0 {
			gap.Earliest = s.records[0].Sequence
		}
		return result, gap
	}
	if sequence > s.next {
		gap.Reason = "cursor_ahead"
		return result, gap
	}
	if len(s.records) > 0 && sequence+1 < s.records[0].Sequence {
		gap.Reason = "history_evicted"
		gap.Earliest = s.records[0].Sequence
		result.Initial = s.currentRecordsLocked()
		return result, gap
	}
	filtered := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		if record.Sequence > sequence {
			filtered = append(filtered, cloneRecord(record))
		}
	}
	result.Initial = filtered
	return result, nil
}

func (s *Store) currentRecordsLocked() []Record {
	result := make([]Record, len(s.records))
	for index, record := range s.records {
		result[index] = cloneRecord(record)
	}
	return result
}

func (subscription *Subscription) Close() {
	if subscription == nil || subscription.store == nil {
		return
	}
	subscription.once.Do(func() {
		subscription.store.mu.Lock()
		defer subscription.store.mu.Unlock()
		if channel, ok := subscription.store.subs[subscription.id]; ok {
			delete(subscription.store.subs, subscription.id)
			close(channel)
		}
	})
}

func formatCursor(epoch string, sequence uint64) string {
	return fmt.Sprintf("%s:%d", epoch, sequence)
}

func newEpoch() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("fallback-%d-%d", time.Now().UTC().UnixNano(), atomic.AddUint64(&fallbackEpoch, 1))
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func cloneRecord(record Record) Record {
	copy := record
	copy.Evidence = append([]EvidenceReference(nil), record.Evidence...)
	return copy
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	copy := snapshot
	copy.Preflight = append([]PreflightCheck(nil), snapshot.Preflight...)
	copy.Sources = append([]Source(nil), snapshot.Sources...)
	copy.Actions = append([]ActionOutput(nil), snapshot.Actions...)
	copy.ProjectTests = append([]ProjectTestOutput(nil), snapshot.ProjectTests...)
	copy.Warnings = append([]string(nil), snapshot.Warnings...)
	copy.Records = make([]Record, len(snapshot.Records))
	for index, record := range snapshot.Records {
		copy.Records[index] = cloneRecord(record)
	}
	return copy
}
