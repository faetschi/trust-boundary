package webview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"tbound/supervisor/internal/audit"
)

const previewBytes = 256
const maxAuditProjectionBytes = 16 << 20
const maxEvidenceBundleBytes = 32 << 20
const maxCheckpointPrefixBytes = 256 << 20

// Source names one explicit read-only input. Kinds are audit JSONL, transcript
// JSONL, go-test JSONL, a versioned run manifest, or a sessionrepo evidence bundle.
type Source struct {
	Name string
	Path string
	Kind string
}

// TailerOptions controls polling and per-line memory bounds. Zero values use
// the package defaults.
type TailerOptions struct {
	PollInterval  time.Duration
	MaxLineBytes  int
	AuditMaxBytes int64
}

type fileState struct {
	source                Source
	file                  *os.File
	info                  os.FileInfo
	offset                int64
	pending               []byte
	dropping              bool
	preview               []byte
	droppedByte           int64
	epoch                 string
	auditDirty            bool
	lastAudit             string
	controlInfo           os.FileInfo
	controlSize           int64
	controlMod            time.Time
	controlSame           os.FileInfo
	lastControlError      string
	lastAuditAt           time.Time
	parsedOffset          int64
	safeOffset            int64
	checkpointOffset      int64
	hashOffset            int64
	checkpointSaved       bool
	checkpointRunID       string
	pendingManifest       *TestRunManifest
	sourceGapReason       string
	prefixHasher          hash.Hash
	safeHashState         []byte
	checkpointLimitWarned bool
}

// Tailer follows transcript streams from their current end, resumes Go test
// streams only from a validated viewer-history checkpoint, verifies audit
// journals from the beginning, and polls typed manifest/evidence snapshots.
// It reads sources with os.Open (read-only); it never creates, truncates, or
// writes a source file.
type Tailer struct {
	lifecycleMu sync.Mutex
	states      []*fileState
	buffer      *Buffer
	options     TailerOptions
	started     bool
	done        chan error
}

// NewTailer creates a tailer but does not open any paths until Start.
func NewTailer(sources []Source, buffer *Buffer, options TailerOptions) (*Tailer, error) {
	if buffer == nil {
		return nil, errors.New("webview tailer requires a record buffer")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = DefaultPollInterval
	}
	if options.PollInterval < time.Millisecond {
		options.PollInterval = time.Millisecond
	}
	if options.MaxLineBytes <= 0 {
		options.MaxLineBytes = DefaultMaxLineBytes
	}
	if options.MaxLineBytes > DefaultMaxLineBytes {
		options.MaxLineBytes = DefaultMaxLineBytes
	}
	if options.AuditMaxBytes <= 0 {
		options.AuditMaxBytes = maxAuditProjectionBytes
	}
	if options.AuditMaxBytes > maxAuditProjectionBytes {
		options.AuditMaxBytes = maxAuditProjectionBytes
	}
	if len(sources) > maxConfiguredSources {
		return nil, fmt.Errorf("webview source count exceeds limit %d", maxConfiguredSources)
	}
	states := make([]*fileState, 0, len(sources))
	info := make([]SourceInfo, 0, len(sources))
	names := make(map[string]struct{}, len(sources))
	auditCount, evidenceCount, manifestCount := 0, 0, 0
	for _, source := range sources {
		if strings.TrimSpace(source.Path) == "" {
			return nil, errors.New("webview source path must not be empty")
		}
		if len(source.Path) > 32768 {
			return nil, errors.New("webview source path exceeds limit")
		}
		if strings.TrimSpace(source.Name) == "" {
			source.Name = fmt.Sprintf("source %d", len(states)+1)
		}
		if len(source.Name) > 1024 {
			return nil, errors.New("webview source name exceeds limit")
		}
		if _, exists := names[source.Name]; exists {
			return nil, fmt.Errorf("duplicate webview source name %q", source.Name)
		}
		names[source.Name] = struct{}{}
		switch source.Kind {
		case "audit", "transcript", "go-test", "manifest", "evidence":
		default:
			return nil, fmt.Errorf("unsupported webview source kind %q", source.Kind)
		}
		switch source.Kind {
		case "audit":
			auditCount++
		case "evidence":
			evidenceCount++
		case "manifest":
			manifestCount++
		}
		states = append(states, &fileState{source: source})
		info = append(info, SourceInfo{Name: source.Name, Kind: source.Kind})
	}
	if auditCount > 2 || evidenceCount > 1 || manifestCount > 1 {
		return nil, errors.New("webview allows at most two audit journals, one evidence bundle, and one run manifest")
	}
	buffer.setSources(info)
	return &Tailer{states: states, buffer: buffer, options: options}, nil
}

// Start opens configured streams read-only, loads snapshot sources, and begins
// polling until ctx is canceled or a stream read fails.
func (t *Tailer) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("webview tailer requires a context")
	}
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	if t.started {
		return errors.New("webview tailer has already been started")
	}
	t.buffer.beginHistoryBatch()
	defer t.buffer.endHistoryBatch()
	// Read the lifecycle manifest before stream catch-up so events following a
	// run transition are assigned to the new run. Terminal states are staged
	// until all complete stream lines have been drained.
	for _, state := range t.states {
		if state.source.Kind == "manifest" {
			state.epoch = newEpoch()
			t.buffer.setSourceEpoch(state.source.Name, state.epoch, "source attached or server restarted")
			if err := t.pollControlSourceMode(state, true); err != nil {
				return err
			}
		}
	}
	for _, state := range t.states {
		if state.source.Kind == "manifest" || state.source.Kind == "evidence" {
			continue
		}
		var err error
		if state.source.Kind == "go-test" {
			err = t.openGoTestState(state)
		} else {
			err = openState(state, false)
		}
		if err != nil {
			t.closeStates("tailer stopped during startup")
			return fmt.Errorf("open %s %q read-only: %w", state.source.Kind, state.source.Path, err)
		}
		t.buffer.setSourceEpoch(state.source.Name, state.epoch, "source attached or server restarted")
		if state.source.Kind == "go-test" {
			if state.sourceGapReason != "" {
				t.buffer.markSourceGap(state.source.Name, state.sourceGapReason)
			}
			if !t.buffer.hasRunManifestError() {
				if err := t.readAvailable(state); err != nil {
					t.closeStates("tailer stopped during startup")
					return err
				}
			}
		}
		if state.source.Kind == "audit" {
			t.projectAudit(state)
		}
	}
	for _, state := range t.states {
		if state.source.Kind == "manifest" {
			t.applyPendingManifest(state)
		}
	}
	for _, state := range t.states {
		if state.source.Kind != "manifest" && state.source.Kind != "evidence" {
			continue
		}
		if state.source.Kind == "evidence" {
			state.epoch = newEpoch()
			t.buffer.setSourceEpoch(state.source.Name, state.epoch, "source attached or server restarted")
			if err := t.pollControlSource(state); err != nil {
				t.buffer.publish(malformedRecord(state.source, "", err.Error(), state.epoch))
			}
		}
	}
	t.started = true
	t.done = make(chan error, 1)
	go func() {
		t.done <- t.run(ctx)
		close(t.done)
	}()
	return nil
}

// Done returns the channel which receives the tailer's termination error.
func (t *Tailer) Done() <-chan error {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	return t.done
}

func (t *Tailer) run(ctx context.Context) error {
	ticker := time.NewTicker(t.options.PollInterval)
	defer ticker.Stop()
	defer func() {
		t.buffer.beginHistoryBatch()
		t.closeStates("tailer stopped before the line ended")
		t.buffer.endHistoryBatch()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := t.pollCycle(); err != nil {
				return err
			}
		}
	}
}

func (t *Tailer) pollCycle() error {
	t.buffer.beginHistoryBatch()
	defer t.buffer.endHistoryBatch()
	for _, state := range t.states {
		if state.source.Kind == "manifest" {
			if err := t.pollControlSourceMode(state, true); err != nil {
				return fmt.Errorf("read run manifest %q: %w", state.source.Path, err)
			}
		}
	}
	for _, state := range t.states {
		if state.source.Kind == "manifest" || state.source.Kind == "evidence" {
			continue
		}
		if state.source.Kind == "go-test" && t.buffer.hasRunManifestError() {
			continue
		}
		if err := t.poll(state); err != nil {
			return fmt.Errorf("tail %q: %w", state.source.Path, err)
		}
	}
	for _, state := range t.states {
		if state.source.Kind == "manifest" {
			t.applyPendingManifest(state)
		}
	}
	for _, state := range t.states {
		if state.source.Kind == "evidence" {
			if err := t.pollControlSource(state); err != nil {
				return fmt.Errorf("poll evidence %q: %w", state.source.Path, err)
			}
		}
	}
	return nil
}

func openState(state *fileState, fromStart bool) error {
	// os.Open is intentionally used instead of OpenFile with write-capable flags.
	file, err := os.Open(state.source.Path)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return errors.New("source is not a regular file")
	}
	position := int64(0)
	if !fromStart {
		position, err = file.Seek(0, io.SeekEnd)
	} else {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = file.Close()
		return err
	}
	state.file = file
	state.info = info
	state.epoch = newEpoch()
	state.offset = position
	state.parsedOffset = position
	state.safeOffset = position
	state.checkpointSaved = false
	state.pending = nil
	state.dropping = false
	state.preview = nil
	state.droppedByte = 0
	return nil
}

func (t *Tailer) openGoTestState(state *fileState) error {
	file, err := os.Open(state.source.Path)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return errors.New("source is not a regular file")
	}
	checkpoint, hasCheckpoint := t.buffer.sourceCheckpoint(state.source.Name)
	currentRunID := t.buffer.currentRunID()
	checkpointRunChanged := hasCheckpoint && checkpoint.RunID != currentRunID
	emptyNewRunStream := checkpointRunChanged && currentRunID != "" && info.Size() == 0
	if hasCheckpoint && checkpoint.Kind == "go-test" && checkpoint.Offset >= 0 && checkpoint.Offset <= info.Size() {
		hasher, hashState, fpErr := hashPrefixState(file, checkpoint.Offset)
		fingerprint := fingerprintFromHashState(hashState)
		if fpErr == nil && fingerprint == checkpoint.Fingerprint && (!checkpointRunChanged || info.Size() == checkpoint.Offset) {
			if _, err := file.Seek(checkpoint.Offset, io.SeekStart); err != nil {
				_ = file.Close()
				return err
			}
			state.file, state.info, state.epoch = file, info, checkpoint.Epoch
			state.offset, state.parsedOffset, state.safeOffset = checkpoint.Offset, checkpoint.Offset, checkpoint.Offset
			state.checkpointOffset = checkpoint.Offset
			state.checkpointSaved = true
			state.checkpointRunID = checkpoint.RunID
			if checkpointRunChanged {
				state.checkpointSaved = false
				if emptyNewRunStream {
					t.buffer.setSourceContinuity(state.source.Name, "new-empty-run-stream")
				} else {
					t.buffer.setSourceContinuity(state.source.Name, "resumed-at-run-boundary")
				}
			} else {
				t.buffer.setSourceContinuity(state.source.Name, "resumed")
			}
			state.prefixHasher, state.safeHashState = hasher, hashState
			state.hashOffset = checkpoint.Offset
			return nil
		}
	}
	position, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		_ = file.Close()
		return err
	}
	state.file, state.info, state.epoch = file, info, newEpoch()
	state.offset, state.parsedOffset, state.safeOffset = position, position, position
	state.checkpointSaved = false
	state.prefixHasher, state.safeHashState, err = hashPrefixState(file, position)
	state.hashOffset = position
	if err != nil {
		state.prefixHasher = nil
		state.safeHashState = nil
	} else if !endsAtLineBoundary(file, position) {
		// Existing incomplete bytes are outside this attachment's observation
		// window. Do not persist a resume offset inside that line.
		state.safeOffset = -1
		state.safeHashState = nil
	}
	if emptyNewRunStream {
		t.buffer.setSourceContinuity(state.source.Name, "new-empty-run-stream")
	} else if checkpointRunChanged && position > 0 {
		state.sourceGapReason = "Go test JSON bytes accumulated after the retained checkpoint belonged to an unobserved run transition; run attribution is incomplete"
	} else if hasCheckpoint && checkpoint.Offset > position {
		state.sourceGapReason = "Go test JSON stream was truncated before its retained resume offset; earlier events may be unavailable"
	} else if hasCheckpoint && position > 0 {
		state.sourceGapReason = "Go test JSON stream no longer matches its retained resume checkpoint; bytes since the last validated point were not replayed"
	} else if position > 0 {
		state.sourceGapReason = "Go test JSON stream already contained bytes, but no retained resume checkpoint was available; earlier events were not replayed"
	} else {
		t.buffer.setSourceContinuity(state.source.Name, "observing-from-empty-stream")
	}
	if err != nil {
		t.buffer.addWarning(state.source.Name, "resume checkpoint unavailable because the source prefix exceeds the verification bound")
	}
	return nil
}

func endsAtLineBoundary(file *os.File, size int64) bool {
	if size == 0 {
		return true
	}
	var last [1]byte
	_, err := file.ReadAt(last[:], size-1)
	return err == nil && last[0] == '\n'
}

func hashPrefixState(file *os.File, offset int64) (hash.Hash, []byte, error) {
	if offset < 0 {
		return nil, nil, errors.New("negative source checkpoint offset")
	}
	if offset > maxCheckpointPrefixBytes {
		return nil, nil, fmt.Errorf("source prefix exceeds the %d-byte checkpoint verification bound", maxCheckpointPrefixBytes)
	}
	hasher := sha256.New()
	if _, err := io.CopyBuffer(hasher, io.NewSectionReader(file, 0, offset), make([]byte, 32*1024)); err != nil {
		return nil, nil, err
	}
	marshaler, ok := hasher.(encoding.BinaryMarshaler)
	if !ok {
		return nil, nil, errors.New("SHA-256 checkpoint state is unavailable")
	}
	state, err := marshaler.MarshalBinary()
	return hasher, state, err
}

func fingerprintFromHashState(state []byte) string {
	if len(state) == 0 {
		return ""
	}
	hasher := sha256.New()
	unmarshaler, ok := hasher.(encoding.BinaryUnmarshaler)
	if !ok || unmarshaler.UnmarshalBinary(state) != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func (t *Tailer) poll(state *fileState) error {
	if state.file == nil {
		if err := openState(state, true); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		t.buffer.setSourceEpoch(state.source.Name, state.epoch, "source file became available")
		if state.source.Kind == "go-test" {
			t.resetGoTestHash(state)
			t.buffer.markSourceGap(state.source.Name, "Go test JSON source became available after observation began")
		}
		if state.source.Kind == "audit" {
			state.auditDirty = true
		}
	}
	if err := t.readAvailable(state); err != nil {
		return err
	}
	pathInfo, err := os.Stat(state.source.Path)
	if errors.Is(err, os.ErrNotExist) {
		// Keep the old descriptor while a rotated path is temporarily absent.
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(state.info, pathInfo) {
		t.finishPartial(state, "file was rotated before the line ended")
		if err := state.file.Close(); err != nil {
			return err
		}
		state.file = nil
		if err := openState(state, true); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		t.buffer.setSourceEpoch(state.source.Name, state.epoch, "source file was replaced")
		if state.source.Kind == "go-test" {
			t.resetGoTestHash(state)
			t.buffer.markSourceGap(state.source.Name, "Go test JSON source was replaced; earlier source bytes may be unavailable")
		}
		if state.source.Kind == "audit" {
			state.auditDirty = true
		}
		return t.readAvailable(state)
	}
	if pathInfo.Size() < state.offset {
		t.finishPartial(state, "file was truncated before the line ended")
		if err := state.file.Close(); err != nil {
			return err
		}
		state.file = nil
		if err := openState(state, true); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		t.buffer.setSourceEpoch(state.source.Name, state.epoch, "source file was truncated")
		if state.source.Kind == "go-test" {
			t.resetGoTestHash(state)
			t.buffer.markSourceGap(state.source.Name, "Go test JSON source was truncated; earlier source bytes may be unavailable")
		}
		if state.source.Kind == "audit" {
			state.auditDirty = true
		}
		return t.readAvailable(state)
	}
	return nil
}

func (t *Tailer) readAvailable(state *fileState) error {
	var chunk [32 * 1024]byte
	for {
		count, err := state.file.Read(chunk[:])
		if count > 0 {
			state.offset += int64(count)
			t.consume(state, chunk[:count])
		}
		if errors.Is(err, io.EOF) {
			t.maybeProjectAudit(state)
			if err := t.persistGoTestCheckpoint(state); err != nil {
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		if count == 0 {
			t.maybeProjectAudit(state)
			if err := t.persistGoTestCheckpoint(state); err != nil {
				return err
			}
			return nil
		}
	}
}

func (t *Tailer) maybeProjectAudit(state *fileState) {
	if !state.auditDirty {
		return
	}
	if !state.lastAuditAt.IsZero() && time.Since(state.lastAuditAt) < time.Second {
		return
	}
	state.auditDirty = false
	state.lastAuditAt = time.Now()
	t.projectAudit(state)
}

func (t *Tailer) consume(state *fileState, chunk []byte) {
	if state.source.Kind == "audit" && len(chunk) != 0 {
		state.auditDirty = true
	}
	hashStart := 0
	for index, current := range chunk {
		if state.source.Kind == "go-test" {
			state.parsedOffset++
		}
		if state.dropping {
			if current == '\n' {
				if state.source.Kind == "go-test" {
					if hashGoTestBytes(state, chunk, hashStart, index+1) {
						state.safeOffset = state.parsedOffset
						state.safeHashState = marshalHashState(state.prefixHasher)
					}
					hashStart = index + 1
				}
				if state.source.Kind != "audit" {
					t.publishOversized(state)
				}
				state.dropping = false
				state.preview = nil
				state.droppedByte = 0
			} else if state.droppedByte < int64(^uint64(0)>>1) {
				state.droppedByte++
			}
			continue
		}
		if current == '\n' {
			if state.source.Kind == "go-test" {
				if hashGoTestBytes(state, chunk, hashStart, index+1) {
					state.safeOffset = state.parsedOffset
					state.safeHashState = marshalHashState(state.prefixHasher)
				}
				hashStart = index + 1
			}
			line := state.pending
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if state.source.Kind == "audit" {
				state.auditDirty = true
			} else {
				t.publishLine(state.source, line, state.epoch)
			}
			state.pending = nil
			continue
		}
		if len(state.pending) < t.options.MaxLineBytes {
			state.pending = append(state.pending, current)
			continue
		}
		state.preview = append(state.preview, state.pending[:min(len(state.pending), previewBytes)]...)
		if len(state.preview) > previewBytes {
			state.preview = state.preview[:previewBytes]
		}
		if len(state.preview) < previewBytes {
			state.preview = append(state.preview, current)
		}
		state.dropping = true
		state.droppedByte = int64(len(state.pending) + 1)
		state.pending = nil
	}
	if state.source.Kind == "go-test" && hashStart < len(chunk) {
		_ = hashGoTestBytes(state, chunk, hashStart, len(chunk))
	}
}

func hashGoTestBytes(state *fileState, chunk []byte, start, end int) bool {
	if state.prefixHasher == nil {
		return false
	}
	length := int64(end - start)
	if length < 0 || state.hashOffset+length > maxCheckpointPrefixBytes {
		remaining := maxCheckpointPrefixBytes - state.hashOffset
		if remaining > 0 {
			n := int(min(length, remaining))
			_, _ = state.prefixHasher.Write(chunk[start : start+n])
			state.hashOffset += int64(n)
		}
		state.prefixHasher = nil
		return false
	}
	_, _ = state.prefixHasher.Write(chunk[start:end])
	state.hashOffset += length
	return true
}

func marshalHashState(hasher hash.Hash) []byte {
	if hasher == nil {
		return nil
	}
	marshaler, ok := hasher.(encoding.BinaryMarshaler)
	if !ok {
		return nil
	}
	state, err := marshaler.MarshalBinary()
	if err != nil {
		return nil
	}
	return state
}

func (t *Tailer) resetGoTestHash(state *fileState) {
	if state.file == nil {
		return
	}
	state.parsedOffset, state.safeOffset, state.checkpointOffset = 0, 0, -1
	state.checkpointSaved = false
	state.checkpointRunID = ""
	state.prefixHasher, state.safeHashState, _ = hashPrefixState(state.file, 0)
	state.hashOffset = 0
	state.safeHashState = marshalHashState(state.prefixHasher)
}

func (t *Tailer) persistGoTestCheckpoint(state *fileState) error {
	if state.source.Kind == "go-test" && state.parsedOffset > maxCheckpointPrefixBytes && !state.checkpointLimitWarned {
		state.checkpointLimitWarned = true
		t.buffer.addWarning(state.source.Name, "resume checkpoint advancement stopped at the 256 MiB prefix-verification bound; a later restart may replay more input")
		t.buffer.setSourceContinuity(state.source.Name, "checkpoint-verification-limit")
	}
	if state.source.Kind != "go-test" || state.safeOffset < 0 || state.safeHashState == nil {
		return nil
	}
	runID := t.buffer.currentRunID()
	if state.checkpointSaved && state.checkpointOffset == state.safeOffset && state.checkpointRunID == runID {
		return nil
	}
	fingerprint := fingerprintFromHashState(state.safeHashState)
	if fingerprint == "" {
		return errors.New("could not encode Go test source checkpoint")
	}
	t.buffer.setSourceCheckpoint(SourceCheckpoint{Name: state.source.Name, Kind: "go-test", Epoch: state.epoch, RunID: runID, Offset: state.safeOffset, Fingerprint: fingerprint})
	state.checkpointSaved, state.checkpointOffset = true, state.safeOffset
	state.checkpointRunID = runID
	return nil
}

func (t *Tailer) publishOversized(state *fileState) {
	if state.source.Kind == "go-test" {
		t.buffer.markSourceGap(state.source.Name, "an oversized Go test JSON line was not parsed; catalog state may be incomplete")
	}
	record := malformedRecord(state.source, string(state.preview), fmt.Sprintf(
		"line exceeds the configured maximum of %d bytes (at least %d bytes)",
		t.options.MaxLineBytes, state.droppedByte,
	), state.epoch)
	t.buffer.addWarning(state.source.Name, "an oversized JSONL line was not parsed; package/test state may be incomplete")
	t.buffer.publish(record)
}

func (t *Tailer) publishLine(source Source, line []byte, sourceEpoch string) {
	if source.Kind == "go-test" {
		event, err := decodeGoTestEvent(line)
		if err != nil {
			if event.Package != "" {
				t.buffer.markGoTestEventUnresolved(event, source.Name, err.Error())
			} else {
				t.buffer.markSourceGap(source.Name, "invalid Go test JSON event could not be attributed: "+err.Error())
			}
			t.buffer.publish(malformedRecord(source, string(line), "invalid go test -json event: "+err.Error(), sourceEpoch))
			return
		}
		if err := t.buffer.applyGoTestEvent(event, source.Name, sourceEpoch); err != nil {
			if event.Package != "" {
				t.buffer.markGoTestEventUnresolved(event, source.Name, err.Error())
			} else {
				t.buffer.markSourceGap(source.Name, "Go test event could not be projected: "+err.Error())
			}
			t.buffer.publish(malformedRecord(source, string(line), err.Error(), sourceEpoch))
		}
		return
	}
	if err := validateJSONDocument(line); err != nil {
		t.buffer.addWarning(source.Name, "invalid JSONL record: "+err.Error())
		t.buffer.publish(malformedRecord(source, string(line), "invalid JSON: "+err.Error(), sourceEpoch))
		return
	}
	// validateJSONDocument has already consumed and validated the complete
	// value, including UTF-8, duplicate keys, nesting, and trailing data. Keep
	// the original validated bytes instead of decoding them a second time.
	parsed := append(json.RawMessage(nil), line...)
	t.buffer.publish(Record{
		Source: source.Name, SourceEpoch: sourceEpoch, Types: classify(source, parsed),
		ReceivedAt: time.Now().UTC(), Record: parsed,
	})
}

func (t *Tailer) pollControlSource(state *fileState) error {
	return t.pollControlSourceMode(state, false)
}

func (t *Tailer) pollControlSourceMode(state *fileState, preflight bool) error {
	file, err := os.Open(state.source.Path)
	if err != nil {
		return t.controlSourceError(state, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return t.controlSourceError(state, err)
	}
	if !info.Mode().IsRegular() {
		return t.controlSourceError(state, errors.New("configured control/evidence source is not a regular file"))
	}
	if state.controlInfo != nil && os.SameFile(state.controlInfo, info) && info.Size() == state.controlSize && info.ModTime().Equal(state.controlMod) {
		return nil
	}
	limit := int64(64 << 10)
	if state.source.Kind == "evidence" {
		limit = maxEvidenceBundleBytes
	}
	if info.Size() > limit {
		return t.controlSourceError(state, fmt.Errorf("configured %s source exceeds %d bytes", state.source.Kind, limit))
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return t.controlSourceError(state, err)
	}
	if int64(len(raw)) > limit {
		return t.controlSourceError(state, fmt.Errorf("configured %s source exceeds %d bytes", state.source.Kind, limit))
	}
	if state.controlSame != nil && !os.SameFile(state.controlSame, info) {
		state.epoch = newEpoch()
		t.buffer.setSourceEpoch(state.source.Name, state.epoch, "configured source file was replaced")
	}
	state.controlSame = info
	state.controlInfo, state.controlSize, state.controlMod = info, info.Size(), info.ModTime()
	state.lastControlError = ""
	if state.source.Kind == "manifest" {
		manifest, err := DecodeTestRunManifest(raw)
		if err != nil {
			t.buffer.setRunManifestError(err.Error(), state.source.Name, state.epoch)
			return nil
		}
		if preflight {
			if manifest.RunID != t.buffer.currentRunID() {
				if manifest.State != "running" {
					terminal := manifest
					manifest.State, manifest.FinishedAt, manifest.ExitCode = "running", nil, nil
					state.pendingManifest = &terminal
				}
				if err := t.buffer.setRunManifest(manifest, state.source.Name, state.epoch); err != nil {
					t.buffer.setRunManifestError(err.Error(), state.source.Name, state.epoch)
				}
			} else if manifest.State == "running" {
				if err := t.buffer.setRunManifest(manifest, state.source.Name, state.epoch); err != nil {
					t.buffer.setRunManifestError(err.Error(), state.source.Name, state.epoch)
				}
			} else {
				state.pendingManifest = &manifest
			}
			return nil
		}
		if err := t.buffer.setRunManifest(manifest, state.source.Name, state.epoch); err != nil {
			t.buffer.setRunManifestError(err.Error(), state.source.Name, state.epoch)
		}
		return nil
	}
	projection, err := reconstructGenerationProjection(raw)
	if err != nil {
		status := "invalid"
		if strings.Contains(err.Error(), "available only on Linux") {
			status = "unavailable"
		}
		projection = GenerationProjection{SchemaVersion: "tbound-generation-projection/v1", Status: status, Error: truncateUTF8(err.Error(), 256)}
	}
	t.buffer.setGeneration(projection, state.source.Name, state.epoch)
	return nil
}

func (t *Tailer) applyPendingManifest(state *fileState) {
	if state.pendingManifest == nil {
		return
	}
	manifest := *state.pendingManifest
	state.pendingManifest = nil
	if err := t.buffer.setRunManifest(manifest, state.source.Name, state.epoch); err != nil {
		t.buffer.setRunManifestError(err.Error(), state.source.Name, state.epoch)
	}
}

func (t *Tailer) controlSourceError(state *fileState, err error) error {
	message := "configured source is unavailable or unreadable"
	if strings.Contains(err.Error(), "exceeds") || strings.Contains(err.Error(), "regular file") {
		message = truncateUTF8(err.Error(), 256)
	}
	if state.lastControlError == message {
		return nil
	}
	state.lastControlError = message
	if state.source.Kind == "manifest" {
		t.buffer.setRunManifestError(message, state.source.Name, state.epoch)
	} else {
		t.buffer.setGeneration(GenerationProjection{SchemaVersion: "tbound-generation-projection/v1", Status: "unavailable", Error: message}, state.source.Name, state.epoch)
	}
	return nil
}

func (t *Tailer) projectAudit(state *fileState) {
	state.lastAuditAt = time.Now()
	projection := AuditProjection{SchemaVersion: "tbound-audit-projection/v1", Status: "unverified"}
	file, err := os.Open(state.source.Path)
	if err != nil {
		projection.Status, projection.Error = "unavailable", "configured audit source is unavailable or unreadable"
		t.publishAuditIfChanged(state, projection)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		projection.Status, projection.Error = "unavailable", "configured audit source metadata is unavailable"
		t.publishAuditIfChanged(state, projection)
		return
	}
	if info.Size() > t.options.AuditMaxBytes {
		projection.Status, projection.Error = "limit-exceeded", "audit journal exceeds configured projection byte limit"
		t.publishAuditIfChanged(state, projection)
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, t.options.AuditMaxBytes+1))
	if err != nil {
		projection.Status, projection.Error = "unavailable", "configured audit source could not be read completely"
		t.publishAuditIfChanged(state, projection)
		return
	}
	if int64(len(data)) > t.options.AuditMaxBytes {
		projection.Status, projection.Error = "limit-exceeded", "audit journal exceeds configured projection byte limit"
		t.publishAuditIfChanged(state, projection)
		return
	}
	trace, err := audit.Verify(bytes.NewReader(data))
	if err != nil {
		projection.Status, projection.Error = "invalid", truncateUTF8(err.Error(), 256)
		t.publishAuditIfChanged(state, projection)
		return
	}
	projection.Status = "verified-prefix"
	projection.RecordCount = uint64(len(trace.Records))
	projection.EffectCount = len(trace.Effects)
	start := max(0, len(trace.Records)-maxProjectionItems)
	projection.Truncated = start > 0
	projection.Records = make([]AuditRecordProjection, 0, len(trace.Records)-start)
	for _, record := range trace.Records[start:] {
		projection.Records = append(projection.Records, AuditRecordProjection{Sequence: record.Sequence, Kind: record.Event.Kind, ID: record.Event.ID, Outcome: record.Event.Outcome, Hash: record.Hash})
	}
	if len(trace.Records) > 0 {
		projection.HeadHash = trace.Records[len(trace.Records)-1].Hash
	}
	effectStart := max(0, len(trace.Effects)-maxProjectionItems)
	projection.Truncated = projection.Truncated || effectStart > 0
	projection.Effects = make([]AuditEffectProjection, 0, len(trace.Effects)-effectStart)
	for _, effect := range trace.Effects[effectStart:] {
		projection.Effects = append(projection.Effects, AuditEffectProjection{ID: effect.ID, IntentSequence: effect.IntentSequence, Outcome: effect.Outcome, OutcomeSequence: effect.OutcomeSequence, Unresolved: effect.Unresolved})
	}
	t.publishAuditIfChanged(state, projection)
}

func (t *Tailer) publishAuditIfChanged(state *fileState, projection AuditProjection) {
	encoded, _ := json.Marshal(projection)
	if string(encoded) == state.lastAudit {
		return
	}
	state.lastAudit = string(encoded)
	t.buffer.setAudit(projection, state.source.Name, state.epoch)
}

func malformedRecord(source Source, line, reason, sourceEpoch string) Record {
	line = truncateUTF8(line, DefaultMaxLineBytes)
	reason = truncateUTF8(reason, 1024)
	types := []string{"malformed"}
	if source.Kind == "audit" {
		types = append(types, "audit")
	}
	return Record{
		Source: source.Name, SourceEpoch: sourceEpoch, Types: types, ReceivedAt: time.Now().UTC(),
		RawLine: line, ParseError: reason,
	}
}

func classify(source Source, parsed json.RawMessage) []string {
	if source.Kind == "audit" {
		return []string{"audit"}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(parsed, &object); err != nil || object == nil {
		return []string{"record"}
	}
	types := make([]string, 0, 4)
	for _, name := range []string{"proposal", "correlation", "decision", "result"} {
		if _, ok := object[name]; ok {
			types = append(types, name)
		}
	}
	if len(types) != 0 {
		return types
	}
	var explicitType string
	if raw, ok := object["type"]; ok && json.Unmarshal(raw, &explicitType) == nil {
		switch explicitType {
		case "proposal", "correlation", "decision", "result", "audit":
			return []string{explicitType}
		}
	}
	return []string{"record"}
}

func (t *Tailer) finishPartial(state *fileState, reason string) {
	if state.source.Kind == "audit" {
		state.auditDirty = true
	} else if state.dropping {
		record := malformedRecord(state.source, string(state.preview), fmt.Sprintf(
			"line exceeds the configured maximum of %d bytes and %s",
			t.options.MaxLineBytes, reason,
		), state.epoch)
		t.buffer.addWarning(state.source.Name, "a source line ended before completion; projections may be incomplete")
		t.buffer.publish(record)
	} else if len(state.pending) != 0 {
		line := bytes.TrimSuffix(state.pending, []byte{'\r'})
		t.buffer.addWarning(state.source.Name, "an incomplete JSONL line was not parsed")
		t.buffer.publish(malformedRecord(state.source, string(line), "incomplete JSONL line: "+reason, state.epoch))
	}
	state.pending = nil
	state.preview = nil
	state.dropping = false
	state.droppedByte = 0
}

func (t *Tailer) closeStates(reason string) {
	for _, state := range t.states {
		if state.file == nil {
			continue
		}
		t.finishPartial(state, reason)
		_ = state.file.Close()
		state.file = nil
	}
}
