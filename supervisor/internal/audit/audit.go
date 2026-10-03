// Package audit provides a small append journal for the prototype supervisor.
// On Linux, Open holds an exclusive nonblocking advisory flock for the journal
// handle lifetime; Open fails closed on other platforms. A successful append
// means Write completed and File.Sync returned nil. This does not establish
// power-loss durability on an unqualified filesystem, prevent a privileged
// rewrite of the whole journal, or provide a transaction with an external
// effect. The caller must reconcile unresolved effects and must not replay them
// blindly.
package audit

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	recordVersion   = 1
	// MaxRecordBytes bounds each JSON frame during append and reconstruction.
	MaxRecordBytes  = 8 << 20
	intentKind      = "effect_intent"
	outcomeKind     = "effect_outcome"
	outcomeUnknown  = "unknown"
)

var (
	ErrClosed            = errors.New("audit journal is closed")
	ErrPoisoned          = errors.New("audit journal is fail-closed after an I/O error")
	ErrCorrupt           = errors.New("audit journal is corrupt or truncated")
	ErrInvalidEvent      = errors.New("invalid audit event")
	ErrRecordTooLarge    = errors.New("audit record exceeds the configured record limit")
	ErrDurability        = errors.New("audit record was not confirmed durable")
	ErrOutcomeUnknown    = errors.New("effect may have occurred; durable outcome is unavailable")
	ErrDuplicateEffect   = errors.New("effect ID has already been used")
	ErrUnresolvedEffect  = errors.New("effect ID has an unresolved prior attempt")
	ErrQuarantined       = errors.New("audit journal is quarantined by an unresolved effect")
	ErrAlreadyOpen       = errors.New("audit journal already has an exclusive writer")
	ErrLockUnsupported   = errors.New("exclusive audit journal locking is unsupported on this platform")
	ErrInsecurePermissions = errors.New("audit journal path has broader permissions than allowed")
	ErrInsecureOwnership = errors.New("audit journal path has an untrusted owner")
	ErrJournalSymlink    = errors.New("audit journal path may not be a symlink")
	ErrInvalidJournalFile = errors.New("audit journal must be a regular file in a private directory")
	ErrSequenceExhausted = errors.New("audit sequence exhausted")
)

// Event is the caller-owned evidence associated with one journal frame. Data
// is stored as base64 in the JSON frame so its exact bytes survive replay.
// IDs are evidence identifiers, never effect authorization or idempotency keys.
type Event struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Outcome string `json:"outcome,omitempty"`
	Data    []byte `json:"data,omitempty"`
}

// Record is one entry in the append-only hash chain. Hash commits to the
// version, sequence, previous hash, and complete event value.
type Record struct {
	Version      int    `json:"version"`
	Sequence     uint64 `json:"sequence"`
	PreviousHash string `json:"previous_hash"`
	Event        Event  `json:"event"`
	Hash         string `json:"hash"`
}

// EffectTrace reconstructs one effect attempt. Unresolved means there is no
// verified effect disposition; the effect may or may not have run, including
// when Outcome is unknown or a legacy failed callback result. Such an attempt
// needs reconciliation before any retry.
type EffectTrace struct {
	ID             string `json:"id"`
	Intent         []byte `json:"intent,omitempty"`
	IntentSequence uint64 `json:"intent_sequence"`
	Outcome        string `json:"outcome,omitempty"`
	Result         []byte `json:"result,omitempty"`
	OutcomeSequence uint64 `json:"outcome_sequence,omitempty"`
	Unresolved     bool   `json:"unresolved"`
}

// Trace is the verified journal prefix and its reconstructed effect attempts.
type Trace struct {
	Records []Record      `json:"records"`
	Effects []EffectTrace `json:"effects"`
}

type syncFile interface {
	io.Reader
	io.Writer
	Sync() error
	Close() error
	Seek(offset int64, whence int) (int64, error)
}

type effectState uint8

const (
	effectPending effectState = iota + 1
	effectComplete
)

// Journal serializes writers within one process. On Linux its OS advisory flock
// also prevents another cooperating process from opening the same file for
// writing. The target filesystem must honor flock semantics.
type Journal struct {
	mu       sync.Mutex
	file     syncFile
	lockFile *os.File
	directory *os.File
	sequence uint64
	head     string
	effects  map[string]effectState
	poisoned bool
	closed   bool
}

// Open opens an existing journal or creates a new one with mode 0600. On Linux,
// it anchors path traversal to directory handles, requiring root- or
// supervisor-owned, non-group/other-writable ancestry, an exact supervisor-
// owned 0700 parent, and a supervisor-owned 0600 regular file. It acquires an
// exclusive nonblocking OS lock, verifies every existing frame, and
// synchronizes the containing directory before accepting writes. The target
// profile must support locking plus syncing both the journal file and its
// containing directory; this is fail-closed prototype behavior, not a
// qualification of the target filesystem.
func Open(path string) (*Journal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty path", ErrInvalidEvent)
	}
	journalPath, err := prepareJournalPath(path)
	if err != nil {
		return nil, err
	}
	file, directory, err := openJournalFiles(journalPath)
	if err != nil {
		return nil, fmt.Errorf("open audit journal: %w", err)
	}
	closeFiles := func() {
		_ = file.Close()
		_ = directory.Close()
	}
	if err := checkPrivateDirectory(directory); err != nil {
		closeFiles()
		return nil, err
	}
	if err := checkPrivateJournalFile(file); err != nil {
		closeFiles()
		return nil, err
	}
	if err := acquireJournalLock(file); err != nil {
		closeFiles()
		return nil, fmt.Errorf("lock audit journal: %w", err)
	}
	closeOnError := func(err error) (*Journal, error) {
		_ = releaseJournalLock(file)
		closeFiles()
		return nil, err
	}
	// Recheck after acquiring the lock and before reading the file.
	if err := checkPrivateDirectory(directory); err != nil {
		return closeOnError(err)
	}
	if err := checkPrivateJournalFile(file); err != nil {
		return closeOnError(err)
	}
	if err := file.Sync(); err != nil {
		return closeOnError(fmt.Errorf("sync audit journal on open: %w", err))
	}
	if err := directory.Sync(); err != nil {
		return closeOnError(fmt.Errorf("sync audit journal directory: %w", err))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return closeOnError(fmt.Errorf("seek audit journal: %w", err))
	}
	trace, err := Verify(file)
	if err != nil {
		return closeOnError(err)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return closeOnError(fmt.Errorf("seek audit journal end: %w", err))
	}

	j := &Journal{file: file, lockFile: file, directory: directory, effects: make(map[string]effectState)}
	if n := len(trace.Records); n > 0 {
		last := trace.Records[n-1]
		j.sequence, j.head = last.Sequence, last.Hash
	}
	for _, effect := range trace.Effects {
		if effect.Unresolved {
			j.effects[effect.ID] = effectPending
		} else {
			j.effects[effect.ID] = effectComplete
		}
	}
	return j, nil
}

// Append writes and synchronizes a non-effect evidence event. The method
// returns a record only after Write and Sync have both succeeded. Effect intent
// and outcome kinds are reserved for RunEffect so their ordering is checked.
func (j *Journal) Append(event Event) (Record, error) {
	if event.Kind == intentKind || event.Kind == outcomeKind {
		return Record{}, fmt.Errorf("%w: reserved kind %q", ErrInvalidEvent, event.Kind)
	}
	if err := validateEvent(event); err != nil {
		return Record{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.appendLocked(event)
}

// RunEffect durably records an intent before calling effect. If that append
// fails, effect is not called. It records a durable terminal outcome before
// returning result bytes. If outcome persistence fails, the result is withheld
// and ErrOutcomeUnknown is returned because the effect may already have run.
// The caller must supply a stable, supervisor-generated ID and reconcile
// unresolved attempts; this function is not an exactly-once mechanism. The
// callback runs while the writer lock is held and must not call methods on the
// same Journal. A callback error attempts to persist an unknown outcome and
// leaves the journal globally quarantined whether or not that append succeeds.
func (j *Journal) RunEffect(id string, intent []byte, effect func() ([]byte, error)) ([]byte, error) {
	if strings.TrimSpace(id) == "" || effect == nil {
		return nil, fmt.Errorf("%w: effect ID and callback are required", ErrInvalidEvent)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.checkWritableLocked(); err != nil {
		return nil, err
	}
	for priorID, state := range j.effects {
		if state != effectPending {
			continue
		}
		if priorID == id {
			return nil, fmt.Errorf("%w: %s", ErrUnresolvedEffect, priorID)
		}
		return nil, fmt.Errorf("%w: unresolved effect %q", ErrQuarantined, priorID)
	}
	if _, exists := j.effects[id]; exists {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateEffect, id)
	}
	_, err := j.appendLocked(Event{Kind: intentKind, ID: id, Data: cloneBytes(intent)})
	if err != nil {
		return nil, fmt.Errorf("%w: intent: %w", ErrDurability, err)
	}
	j.effects[id] = effectPending

	// Keep the writer lock through the callback so no concurrent audit write can
	// fail after intent durability but before this effect is released. Callbacks
	// must not call back into this Journal.
	result, effectErr := effect()
	outcome := "success"
	if effectErr != nil {
		outcome = outcomeUnknown
		result = nil
	}
	_, err = j.appendLocked(Event{Kind: outcomeKind, ID: id, Outcome: outcome, Data: cloneBytes(result)})
	if err == nil && effectErr == nil {
		j.effects[id] = effectComplete
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOutcomeUnknown, errors.Join(ErrDurability, err))
	}
	if effectErr != nil {
		return nil, fmt.Errorf("%w: callback reported an error: %w", ErrOutcomeUnknown, effectErr)
	}
	return cloneBytes(result), nil
}

// Close closes the journal. It does not add a record; all successful mutating
// calls already synchronized their frames.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	var unlockErr error
	if j.lockFile != nil {
		unlockErr = releaseJournalLock(j.lockFile)
	}
	closeErr := j.file.Close()
	var directoryErr error
	if j.directory != nil {
		directoryErr = j.directory.Close()
	}
	return errors.Join(unlockErr, closeErr, directoryErr)
}

func (j *Journal) appendLocked(event Event) (Record, error) {
	if err := j.checkWritableLocked(); err != nil {
		return Record{}, err
	}
	if j.sequence == ^uint64(0) {
		return Record{}, ErrSequenceExhausted
	}
	unsigned := unsignedRecord{
		Version: recordVersion, Sequence: j.sequence + 1,
		PreviousHash: j.head, Event: cloneEvent(event),
	}
	hash, err := hashRecord(unsigned)
	if err != nil {
		return Record{}, j.failLocked(err)
	}
	record := Record{
		Version: unsigned.Version, Sequence: unsigned.Sequence,
		PreviousHash: unsigned.PreviousHash, Event: unsigned.Event, Hash: hash,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return Record{}, j.failLocked(err)
	}
	if len(encoded)+1 > MaxRecordBytes {
		return Record{}, ErrRecordTooLarge
	}
	encoded = append(encoded, '\n')
	if err := writeFull(j.file, encoded); err != nil {
		return Record{}, j.failLocked(err)
	}
	if err := j.file.Sync(); err != nil {
		return Record{}, j.failLocked(err)
	}
	j.sequence, j.head = record.Sequence, record.Hash
	return cloneRecord(record), nil
}

func (j *Journal) checkWritableLocked() error {
	if j.closed {
		return ErrClosed
	}
	if j.poisoned {
		return ErrPoisoned
	}
	return nil
}

func (j *Journal) failLocked(cause error) error {
	j.poisoned = true
	return fmt.Errorf("%w: %w", ErrPoisoned, cause)
}

// Verify checks framing, canonical encoding, sequence, previous-hash links,
// record hashes, and effect intent/outcome pairing. A trailing partial line is
// rejected; recovery never silently discards it.
func Verify(input io.Reader) (Trace, error) {
	trace := Trace{Records: make([]Record, 0), Effects: make([]EffectTrace, 0)}
	effectIndex := make(map[string]int)
	reader := bufio.NewReaderSize(input, 64*1024)
	var expectedSequence uint64 = 1
	previousHash := ""
	for {
		line, err := readLineLimited(reader)
		if errors.Is(err, io.EOF) {
			return trace, nil
		}
		if err != nil {
			return Trace{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		var record Record
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return Trace{}, fmt.Errorf("%w: decode frame: %v", ErrCorrupt, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return Trace{}, fmt.Errorf("%w: trailing JSON value", ErrCorrupt)
		}
		canonical, err := json.Marshal(record)
		if err != nil || !bytes.Equal(canonical, line) {
			return Trace{}, fmt.Errorf("%w: non-canonical frame", ErrCorrupt)
		}
		if record.Version != recordVersion || record.Sequence != expectedSequence || record.PreviousHash != previousHash {
			return Trace{}, fmt.Errorf("%w: version, sequence, or predecessor mismatch at %d", ErrCorrupt, expectedSequence)
		}
		unsigned := unsignedRecord{
			Version: record.Version, Sequence: record.Sequence,
			PreviousHash: record.PreviousHash, Event: record.Event,
		}
		hash, err := hashRecord(unsigned)
		if err != nil || hash != record.Hash {
			return Trace{}, fmt.Errorf("%w: hash mismatch at %d", ErrCorrupt, expectedSequence)
		}
		if err := acceptTraceEvent(&trace, effectIndex, record); err != nil {
			return Trace{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		trace.Records = append(trace.Records, cloneRecord(record))
		previousHash = record.Hash
		if expectedSequence == ^uint64(0) {
			return Trace{}, ErrSequenceExhausted
		}
		expectedSequence++
	}
}

type unsignedRecord struct {
	Version      int    `json:"version"`
	Sequence     uint64 `json:"sequence"`
	PreviousHash string `json:"previous_hash"`
	Event        Event  `json:"event"`
}

func acceptTraceEvent(trace *Trace, effects map[string]int, record Record) error {
	event := record.Event
	if strings.TrimSpace(event.Kind) == "" || strings.TrimSpace(event.ID) == "" {
		return ErrInvalidEvent
	}
	switch event.Kind {
	case intentKind:
		if event.Outcome != "" {
			return fmt.Errorf("intent has an outcome")
		}
		if _, exists := effects[event.ID]; exists {
			return fmt.Errorf("duplicate effect intent %q", event.ID)
		}
		effects[event.ID] = len(trace.Effects)
		trace.Effects = append(trace.Effects, EffectTrace{
			ID: event.ID, Intent: cloneBytes(event.Data), IntentSequence: record.Sequence, Unresolved: true,
		})
	case outcomeKind:
		index, exists := effects[event.ID]
		if !exists || !trace.Effects[index].Unresolved || trace.Effects[index].OutcomeSequence != 0 {
			return fmt.Errorf("outcome without a unique unresolved intent %q", event.ID)
		}
		if event.Outcome != "success" && event.Outcome != outcomeUnknown && event.Outcome != "failed" {
			return fmt.Errorf("invalid effect outcome")
		}
		if event.Outcome != "success" && len(event.Data) != 0 {
			return fmt.Errorf("unknown effect outcome contains result data")
		}
		effect := &trace.Effects[index]
		effect.Outcome = event.Outcome
		effect.Result = cloneBytes(event.Data)
		effect.OutcomeSequence = record.Sequence
		effect.Unresolved = event.Outcome != "success"
	default:
		if event.Outcome != "" {
			return fmt.Errorf("non-effect event contains an outcome")
		}
	}
	return nil
}

func readLineLimited(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > MaxRecordBytes {
			return nil, ErrRecordTooLarge
		}
		line = append(line, part...)
		switch {
		case err == nil:
			line = line[:len(line)-1]
			if len(line) == 0 {
				return nil, fmt.Errorf("empty frame")
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) == 0:
			return nil, io.EOF
		case errors.Is(err, io.EOF):
			return nil, fmt.Errorf("trailing frame has no newline")
		default:
			return nil, err
		}
	}
}

func hashRecord(record unsignedRecord) (string, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateEvent(event Event) error {
	if strings.TrimSpace(event.Kind) == "" || strings.TrimSpace(event.ID) == "" || event.Outcome != "" {
		return fmt.Errorf("%w: kind and ID are required; outcome is reserved", ErrInvalidEvent)
	}
	return nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if n < 0 || n > len(data) {
			return fmt.Errorf("invalid write count %d", n)
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func prepareJournalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve audit journal path: %w", err)
	}
	// Linux validates every path component while walking it through directory
	// handles in openJournalFiles. Do not inspect and then reopen this path here:
	// a name-based preflight would introduce a replacement race.
	return filepath.Clean(absolute), nil
}

func checkPrivateDirectory(directory *os.File) error {
	info, err := directory.Stat()
	if err != nil {
		return fmt.Errorf("stat audit journal directory handle: %w", err)
	}
	if !info.IsDir() {
		return ErrInvalidJournalFile
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: parent directory mode %04o", ErrInsecurePermissions, info.Mode().Perm())
	}
	return checkSupervisorOwnership(info)
}

func checkPrivateJournalFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat audit journal handle: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidJournalFile
	}
	if err := checkSupervisorOwnership(info); err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%w: journal file mode %04o", ErrInsecurePermissions, info.Mode().Perm())
	}
	return nil
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

func cloneEvent(event Event) Event {
	event.Data = cloneBytes(event.Data)
	return event
}

func cloneRecord(record Record) Record {
	record.Event = cloneEvent(record.Event)
	return record
}

