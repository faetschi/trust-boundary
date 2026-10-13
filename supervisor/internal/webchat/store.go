package webchat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var fallbackEpoch uint64

// Store is a gateway-owned bounded ring. It does not persist, authorize, or
// alter any supervisor state. Slow subscribers are closed so a browser cannot
// become backpressure on a Pi/runtime path.
type Store struct {
	mu       sync.Mutex
	capacity int
	epoch    string
	next     uint64
	rev      uint64
	items    []Item
	seen     map[string]struct{}
	subs     map[uint64]chan Event
	nextSub  uint64
	base     Snapshot
}

// NewStore validates and copies an initial snapshot. An empty snapshot is not
// accepted because unavailable is a truthful initial state and can be built by
// NewUnavailableStore.
func NewStore(snapshot Snapshot, capacity int) (*Store, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	if snapshot.Verification == "" {
		snapshot.Verification = defaultVerification(snapshot.Source)
	}
	// Each item's verification scope is required and validated above; it is never
	// defaulted here, because a defaulted scope could later read as verified.
	if capacity <= 0 {
		capacity = DefaultItemCapacity
	}
	if capacity > MaxItemCapacity {
		capacity = MaxItemCapacity
	}
	if len(snapshot.Items) > capacity {
		return nil, fmt.Errorf("snapshot has %d items but store capacity is %d", len(snapshot.Items), capacity)
	}
	store := &Store{
		capacity: capacity,
		epoch:    snapshot.Epoch,
		items:    make([]Item, 0, capacity),
		seen:     make(map[string]struct{}, len(snapshot.Items)),
		subs:     make(map[uint64]chan Event),
		base:     cloneSnapshot(snapshot),
	}
	for index, item := range snapshot.Items {
		item.Sequence = uint64(index + 1)
		store.items = append(store.items, cloneItem(item))
		store.seen[item.ID] = struct{}{}
		store.next = item.Sequence
	}
	store.base.Items = nil
	store.base.Cursor = formatCursor(store.epoch, store.next)
	store.base.GeneratedAt = time.Now().UTC()
	if store.base.Session.CreatedAt.IsZero() {
		store.base.Session.CreatedAt = store.base.GeneratedAt
	}
	if store.base.Session.UpdatedAt.IsZero() {
		store.base.Session.UpdatedAt = store.base.Session.CreatedAt
	}
	return store, nil
}

// NewUnavailableStore is the default production-safe state. It contains no Pi,
// session, or profile proof and no synthetic records.
func NewUnavailableStore() (*Store, error) {
	epoch := newEpoch()
	snapshot := Snapshot{
		SchemaVersion: ContractVersion,
		Epoch:         epoch,
		Cursor:        formatCursor(epoch, 0),
		Source:        SourceUnavailable,
		Verification:  VerificationUnavailable,
		GeneratedAt:   time.Now().UTC(),
		Session:       SessionHeader{Status: SessionUnavailable, Title: "No session projection"},
		Panels: Panels{
			Publication:   PublicationPanel{State: PublicationUnavailable, Detail: "publication state is unavailable"},
			Recovery:      RecoveryPanel{State: RecoveryUnavailable, Detail: "recovery evidence is unavailable"},
			Provenance:    ProvenancePanel{Available: false, Note: "no production provenance observation is registered"},
			Qualification: QualificationPanel{G1: false, H1: "unexecuted", Containment: "not-established", ClaimBearing: false, Note: "G1=false; H1 unexecuted; containment not-established"},
		},
		Streams: []StreamStatus{{ID: "production", Label: "production observations", Health: StreamUnavailable, Available: false, Reason: "no live Pi/session observation source is registered"}},
		Warnings: []string{
			"production observations are unavailable; this workspace is read-only and exposes no launch, cancel, approval, or execution control",
		},
	}
	return NewStore(snapshot, DefaultItemCapacity)
}

// Snapshot returns a consistent bounded view.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// Session returns the current bounded session header.
func (s *Store) Session() SessionHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base.Session
}

// Item returns a retained projected item by ID. It never resolves a path or
// reads a client-selected file.
func (s *Store) Item(id string) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.ID == id {
			return cloneItem(item), true
		}
	}
	return Item{}, false
}

func (s *Store) snapshotLocked() Snapshot {
	snapshot := cloneSnapshot(s.base)
	snapshot.Epoch = s.epoch
	snapshot.Cursor = formatCursor(s.epoch, s.next)
	snapshot.GeneratedAt = time.Now().UTC()
	snapshot.Items = make([]Item, len(s.items))
	for index, item := range s.items {
		snapshot.Items[index] = cloneItem(item)
	}
	return snapshot
}

// AppendItem adds one structurally validated projection element. Duplicate
// registered IDs are ignored, which makes reconnect/replay delivery idempotent.
func (s *Store) AppendItem(item Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.SchemaVersion == "" {
		item.SchemaVersion = ContractVersion
	}
	if item.OccurredAt.IsZero() {
		item.OccurredAt = time.Now().UTC()
	}
	scope := s.base.Verification
	if scope == "" {
		scope = defaultVerification(s.base.Source)
	}
	if err := validateItem(item, s.base.Source, scope); err != nil {
		return err
	}
	if _, exists := s.seen[item.ID]; exists {
		return nil
	}
	s.next++
	item.Sequence = s.next
	if len(s.items) == s.capacity {
		delete(s.seen, s.items[0].ID)
		s.items = append(s.items[:0], s.items[1:]...)
	}
	s.items = append(s.items, cloneItem(item))
	s.seen[item.ID] = struct{}{}
	s.broadcastLocked(Event{SchemaVersion: ContractVersion, Cursor: formatCursor(s.epoch, s.next), Kind: "item", Item: itemPointer(item)})
	return nil
}

// UpdateSession replaces the bounded session header and notifies subscribers.
func (s *Store) UpdateSession(header SessionHeader) error {
	if err := validateSession(header); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.base.Session = header
	s.rev++
	s.broadcastLocked(Event{SchemaVersion: ContractVersion, Cursor: formatCursor(s.epoch, s.next), Kind: "session", Session: &header, Panels: panelsPointer(s.base.Panels)})
	return nil
}

// UpdatePanels replaces the governance/publication/recovery panels and notifies
// subscribers. It only changes display metadata; it never creates, verifies, or
// settles an effect.
func (s *Store) UpdatePanels(panels Panels) error {
	if err := validatePanels(panels); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := clonePanels(panels)
	s.base.Panels = stored
	s.rev++
	s.broadcastLocked(s.sessionEvent())
	return nil
}

// UpdateStream replaces one registered stream-health projection.
func (s *Store) UpdateStream(stream StreamStatus) error {
	if err := validateStream(stream); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for index := range s.base.Streams {
		if s.base.Streams[index].ID == stream.ID {
			s.base.Streams[index] = stream
			replaced = true
			break
		}
	}
	if !replaced {
		if len(s.base.Streams) >= MaxStreams {
			return fmt.Errorf("stream count exceeds %d", MaxStreams)
		}
		s.base.Streams = append(s.base.Streams, stream)
	}
	// Stream health is part of the projected state, so notify subscribers.
	s.rev++
	s.broadcastLocked(s.sessionEvent())
	return nil
}

// RecordGap durably notes a projection continuity gap. A gap is surfaced to the
// client and is never converted into a success observation.
func (s *Store) RecordGap(gap GapRecord) error {
	if gap.ID == "" {
		gap.ID = fmt.Sprintf("gap-%d-%d", gap.FirstMissing, gap.LastMissing)
	}
	if gap.RecordedAt.IsZero() {
		gap.RecordedAt = time.Now().UTC()
	}
	if err := validateGap(gap); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.base.Gaps) >= MaxGaps {
		s.base.Gaps = s.base.Gaps[1:]
	}
	s.base.Gaps = append(s.base.Gaps, gap)
	return nil
}

func (s *Store) broadcastLocked(event Event) {
	for id, subscriber := range s.subs {
		// Every subscriber receives its own clone so a reader cannot mutate
		// store state through a delivered event.
		select {
		case subscriber <- cloneEvent(event):
		default:
			delete(s.subs, id)
			close(subscriber)
		}
	}
}

// Subscription is an atomic replay window plus a live bounded channel.
type Subscription struct {
	Initial []Item
	Session SessionHeader
	Panels  Panels
	Streams []StreamStatus
	Events  <-chan Event
	store   *Store
	id      uint64
	once    sync.Once
}

// SubscribeAfter returns a subscription whose Initial items continue after the
// requested cursor. When the cursor cannot be served from the retained window a
// ReplayGap is returned and the client must recover from a fresh snapshot rather
// than assume continuity.
func (s *Store) SubscribeAfter(cursor string) (*Subscription, *ReplayGap) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) >= MaxSubscribers {
		closed := make(chan Event)
		close(closed)
		// Return no replay buffer: an over-limit subscriber must not trigger a
		// full retained-window replay.
		return &Subscription{Events: closed, store: s}, &ReplayGap{SchemaVersion: GapVersion, Reason: "subscriber_limit", RequestedCursor: truncate(cursor, 256), Epoch: s.epoch, Latest: s.next}
	}
	s.nextSub++
	channel := make(chan Event, SubscriberQueueSize)
	s.subs[s.nextSub] = channel
	result := &Subscription{Initial: s.currentItemsLocked(), Session: s.base.Session, Panels: clonePanels(s.base.Panels), Streams: append([]StreamStatus(nil), s.base.Streams...), Events: channel, store: s, id: s.nextSub}
	if cursor == "" {
		return result, nil
	}
	epoch, sequence, ok := parseCursor(cursor)
	gap := &ReplayGap{SchemaVersion: GapVersion, RequestedCursor: truncate(cursor, 256), Epoch: s.epoch, Latest: s.next}
	if len(cursor) > 256 || !ok {
		gap.Reason = "invalid_cursor"
		return result, gap
	}
	if epoch != s.epoch {
		gap.Reason = "source_epoch_changed"
		if len(s.items) > 0 {
			gap.Earliest = s.items[0].Sequence
		}
		return result, gap
	}
	if sequence > s.next {
		gap.Reason = "cursor_ahead"
		return result, gap
	}
	if len(s.items) > 0 && sequence+1 < s.items[0].Sequence {
		gap.Reason = "history_evicted"
		gap.Earliest = s.items[0].Sequence
		result.Initial = s.currentItemsLocked()
		return result, gap
	}
	filtered := make([]Item, 0, len(s.items))
	for _, item := range s.items {
		if item.Sequence > sequence {
			filtered = append(filtered, cloneItem(item))
		}
	}
	result.Initial = filtered
	return result, nil
}

func (s *Store) currentItemsLocked() []Item {
	result := make([]Item, len(s.items))
	for index, item := range s.items {
		result[index] = cloneItem(item)
	}
	return result
}

// Close detaches the subscription and stops delivery.
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

func itemPointer(item Item) *Item {
	copy := cloneItem(item)
	return &copy
}

func panelsPointer(panels Panels) *Panels {
	copy := clonePanels(panels)
	return &copy
}

func cloneItem(item Item) Item { return item }

// initialized reports whether a Store was produced by NewStore. A zero-value
// Store is rejected by the handler rather than panicking on first use.
func (s *Store) initialized() bool {
	return s != nil && s.epoch != "" && s.seen != nil && s.subs != nil && s.items != nil
}

// sessionEvent builds a session event that carries the full non-item projection
// (session, panels and stream health), so a subscriber never sees stale state.
func (s *Store) sessionEvent() Event {
	header := s.base.Session
	panels := clonePanels(s.base.Panels)
	streams := append([]StreamStatus(nil), s.base.Streams...)
	return Event{
		SchemaVersion: ContractVersion,
		Cursor:        formatCursor(s.epoch, s.next),
		Kind:          "session",
		Session:       &header,
		Panels:        &panels,
		Streams:       streams,
	}
}

// clonePanels deep-copies the panel projection, including the ObservedAt
// pointer, so a caller cannot mutate retained store state through a returned
// snapshot or a delivered event.
func clonePanels(panels Panels) Panels {
	out := panels
	if panels.Publication.ObservedAt != nil {
		observed := *panels.Publication.ObservedAt
		out.Publication.ObservedAt = &observed
	}
	return out
}

func cloneEvent(event Event) Event {
	out := event
	if event.Item != nil {
		item := cloneItem(*event.Item)
		out.Item = &item
	}
	if event.Session != nil {
		session := *event.Session
		out.Session = &session
	}
	if event.Panels != nil {
		panels := clonePanels(*event.Panels)
		out.Panels = &panels
	}
	return out
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	copy := snapshot
	copy.Panels = clonePanels(snapshot.Panels)
	copy.Streams = append([]StreamStatus(nil), snapshot.Streams...)
	copy.Gaps = append([]GapRecord(nil), snapshot.Gaps...)
	copy.Warnings = append([]string(nil), snapshot.Warnings...)
	copy.Items = make([]Item, len(snapshot.Items))
	for index, item := range snapshot.Items {
		copy.Items[index] = cloneItem(item)
	}
	return copy
}
