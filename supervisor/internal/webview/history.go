package webview

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const historySchema = "tbound-observation-history/v1"

var fallbackEpoch uint64

type historyState struct {
	SchemaVersion string               `json:"schema_version"`
	Epoch         string               `json:"epoch"`
	NextID        uint64               `json:"next_id"`
	Records       []Record             `json:"records"`
	Sources       []SourceInfo         `json:"sources"`
	Checkpoints   []SourceCheckpoint   `json:"source_checkpoints,omitempty"`
	Warnings      []string             `json:"warnings,omitempty"`
	Run           *TestRunManifest     `json:"test_run,omitempty"`
	RunError      string               `json:"test_run_error,omitempty"`
	Tests         []TestCase           `json:"tests"`
	Packages      []TestPackage        `json:"packages"`
	DroppedTests  int                  `json:"dropped_tests"`
	Audit         AuditProjection      `json:"audit"`
	Generations   GenerationProjection `json:"generations"`
	RetainedRuns  []RetainedRun        `json:"retained_runs,omitempty"`
}

// NewHistoryBuffer loads or creates a bounded viewer-owned replay file. The
// file is not a source of authority and is written only by this process; no
// HTTP endpoint exposes its path or allows clients to change it.
func NewHistoryBuffer(path string, capacity int) (*Buffer, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("history path must not be empty")
	}
	buffer := NewBuffer(capacity)
	buffer.historyPath = path
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return buffer, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect observation history: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("observation history must be a regular non-symlink file")
	}
	if info.Size() > defaultHistoryBytes {
		return nil, fmt.Errorf("observation history exceeds %d bytes", defaultHistoryBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read observation history: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect open observation history: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.New("observation history changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, defaultHistoryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read observation history: %w", err)
	}
	if len(raw) > defaultHistoryBytes {
		return nil, fmt.Errorf("observation history exceeds %d bytes", defaultHistoryBytes)
	}
	if err := validateJSONDocument(raw); err != nil {
		return nil, fmt.Errorf("validate observation history: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var state historyState
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("decode observation history: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("observation history has trailing JSON data")
	}
	if state.SchemaVersion != historySchema || state.Epoch == "" {
		return nil, errors.New("observation history schema or epoch is invalid")
	}
	if len(state.Epoch) > 128 || strings.Contains(state.Epoch, ":") || state.DroppedTests < 0 || len(state.Tests) > maxTestCases || len(state.Packages) > maxTestPackages || len(state.Sources) > maxConfiguredSources || len(state.Checkpoints) > maxConfiguredSources || len(state.Warnings) > 32 || len(state.RetainedRuns) > maxRetainedRuns {
		return nil, errors.New("observation history metadata exceeds configured bounds")
	}
	if len(state.Records) > buffer.capacity {
		return nil, errors.New("observation history record count exceeds configured buffer capacity")
	}
	checkpointNames := make(map[string]struct{}, len(state.Checkpoints))
	for _, checkpoint := range state.Checkpoints {
		if checkpoint.Name == "" || len(checkpoint.Name) > 1024 || checkpoint.Kind != "go-test" || len(checkpoint.Epoch) == 0 || len(checkpoint.Epoch) > 128 || len(checkpoint.RunID) > 256 || (checkpoint.RunID != "" && !runIDPattern.MatchString(checkpoint.RunID)) || checkpoint.Offset < 0 || !validSHA256Digest(checkpoint.Fingerprint) {
			return nil, errors.New("observation history source checkpoint is invalid")
		}
		if _, exists := checkpointNames[checkpoint.Name]; exists {
			return nil, errors.New("observation history has duplicate source checkpoints")
		}
		checkpointNames[checkpoint.Name] = struct{}{}
	}
	for _, source := range state.Sources {
		if source.Name == "" || len(source.Name) > 1024 || len(source.Kind) > 32 || len(source.Epoch) > 128 || len(source.Continuity) > 64 || len(source.GapReason) > 256 {
			return nil, errors.New("observation history source metadata is invalid")
		}
	}
	for _, warning := range state.Warnings {
		if len(warning) == 0 || len(warning) > 512 {
			return nil, errors.New("observation history source warning exceeds limit")
		}
	}
	var previous uint64
	for _, record := range state.Records {
		if record.ID == 0 || record.ID <= previous || record.ID > state.NextID {
			return nil, errors.New("observation history record cursors are invalid")
		}
		previous = record.ID
	}
	if len(state.Records) != 0 && state.Records[len(state.Records)-1].ID != state.NextID {
		return nil, errors.New("observation history next_id does not match retained records")
	}
	if state.NextID != 0 && len(state.Records) == 0 {
		return nil, errors.New("observation history cursor has no retained record")
	}
	if state.Run != nil {
		runBytes, err := json.Marshal(state.Run)
		if err != nil {
			return nil, errors.New("observation history run manifest is invalid")
		}
		if _, err := DecodeTestRunManifest(runBytes); err != nil {
			return nil, errors.New("observation history run manifest is invalid")
		}
	}
	for _, item := range state.Tests {
		if len(item.Package) == 0 || len(item.Package) > 1024 || len(item.Name) == 0 || len(item.Name) > 2048 || len(item.Output) > maxTestOutputBytes || !validTestState(item.State) {
			return nil, errors.New("observation history test catalog is invalid")
		}
	}
	for _, item := range state.Packages {
		if len(item.Name) == 0 || len(item.Name) > 1024 || len(item.Output) > maxPackageOutput || len(item.NoTestFilesProvenance) > 64 || !validPackageState(item.State) {
			return nil, errors.New("observation history package catalog is invalid")
		}
	}
	retainedIDs := make(map[string]struct{}, len(state.RetainedRuns))
	for _, retained := range state.RetainedRuns {
		runBytes, err := json.Marshal(retained.Run)
		if err != nil {
			return nil, errors.New("observation history retained run is invalid")
		}
		run, err := DecodeTestRunManifest(runBytes)
		if err != nil || run.State == "running" || len(retained.Tests.Tests) > maxRetainedRunTests || len(retained.Tests.Packages) > maxRetainedRunPackages || retained.Tests.Dropped < 0 || retained.OutputPolicy != "omitted-from-retained-run-summaries" {
			return nil, errors.New("observation history retained run exceeds configured bounds")
		}
		if _, exists := retainedIDs[run.RunID]; exists {
			return nil, errors.New("observation history contains duplicate retained run IDs")
		}
		retainedIDs[run.RunID] = struct{}{}
		for _, item := range retained.Tests.Tests {
			if item.Package == "" || len(item.Package) > 1024 || len(item.Name) == 0 || len(item.Name) > 2048 || len(item.Output) != 0 || !validTestState(item.State) {
				return nil, errors.New("observation history retained test summary is invalid")
			}
		}
		for _, item := range retained.Tests.Packages {
			if item.Name == "" || len(item.Name) > 1024 || len(item.Output) != 0 || !validPackageState(item.State) {
				return nil, errors.New("observation history retained package summary is invalid")
			}
		}
	}
	if len(state.Audit.Records) > maxProjectionItems || len(state.Audit.Effects) > maxProjectionItems || len(state.Generations.Generations) > 4096 || len(state.Generations.Limitations) > 32 {
		return nil, errors.New("observation history projection exceeds configured bounds")
	}
	for _, record := range state.Records {
		if len(record.Source) > 1024 || len(record.SourceEpoch) > 128 || len(record.Types) > 16 || len(record.RawLine) > DefaultMaxLineBytes || len(record.ParseError) > 4096 || (len(record.Record) != 0 && !json.Valid(record.Record)) {
			return nil, errors.New("observation history record is invalid")
		}
		if len(record.Record) != 0 {
			if err := validateJSONDocument(record.Record); err != nil {
				return nil, errors.New("observation history record JSON is invalid")
			}
		}
	}
	buffer.epoch = state.Epoch
	buffer.nextID = state.NextID
	buffer.sources = append([]SourceInfo(nil), state.Sources...)
	for _, checkpoint := range state.Checkpoints {
		buffer.checkpoints[checkpoint.Name] = checkpoint
	}
	buffer.warnings = append([]string(nil), state.Warnings...)
	if state.Run != nil {
		copy := *state.Run
		copy.Packages = append([]string(nil), state.Run.Packages...)
		copy.SourceIdentity = cloneSourceIdentity(state.Run.SourceIdentity)
		buffer.run = &copy
	}
	buffer.runError = state.RunError
	for _, item := range state.Tests {
		buffer.tests[item.Package+"\x00"+item.Name] = item
	}
	for _, item := range state.Packages {
		buffer.packages[item.Name] = item
	}
	buffer.droppedTests = state.DroppedTests
	buffer.audit = cloneAuditProjection(state.Audit)
	buffer.generations = cloneGenerationProjection(state.Generations)
	buffer.retainedRuns = make([]RetainedRun, len(state.RetainedRuns))
	for i, item := range state.RetainedRuns {
		buffer.retainedRuns[i] = cloneRetainedRun(item)
	}
	for _, item := range state.Records {
		index := buffer.count
		buffer.items[index] = cloneRecord(item)
		buffer.count++
	}
	return buffer, nil
}

func validTestState(state string) bool {
	switch state {
	case "pending", "running", "paused", "passed", "failed", "skipped", "interrupted", "unresolved":
		return true
	default:
		return false
	}
}

func validPackageState(state string) bool {
	switch state {
	case "pending", "running", "passed", "failed", "skipped", "no_test_files", "interrupted", "unresolved":
		return true
	default:
		return false
	}
}

func newEpoch() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("fallback-%x-%x", time.Now().UTC().UnixNano(), atomic.AddUint64(&fallbackEpoch, 1))
}

func formatCursor(epoch string, id uint64) string { return epoch + ":" + strconv.FormatUint(id, 10) }

func parseCursor(cursor string) (string, uint64, bool) {
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 || parts[0] == "" {
		return "", 0, false
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	return parts[0], id, err == nil
}

func (b *Buffer) persistLocked() {
	if b.historyPath == "" {
		return
	}
	b.historyDirty = true
	b.historyRevision++
}

func (b *Buffer) beginHistoryBatch() {
	b.mu.Lock()
	b.historyBatchDepth++
	b.mu.Unlock()
}

func (b *Buffer) endHistoryBatch() {
	b.mu.Lock()
	if b.historyBatchDepth > 0 {
		b.historyBatchDepth--
	}
	flush := b.historyBatchDepth == 0 && b.historyDirty
	b.mu.Unlock()
	if flush {
		b.flushHistory()
	}
}

// FlushHistory synchronously writes the latest consistent viewer snapshot. It
// is intentionally viewer-only; source files and authoritative audit state are
// never modified by this operation.
func (b *Buffer) FlushHistory() {
	b.flushHistory()
}

func (b *Buffer) flushHistory() {
	b.historyWriteMu.Lock()
	defer b.historyWriteMu.Unlock()
	for {
		b.mu.Lock()
		if b.historyPath == "" || b.historyBatchDepth != 0 || !b.historyDirty {
			b.mu.Unlock()
			return
		}
		state := b.historyStateLocked()
		revision := b.historyRevision
		path := b.historyPath
		b.mu.Unlock()

		encoded, err := json.Marshal(state)
		if err != nil {
			b.mu.Lock()
			b.historyErr = truncateUTF8(err.Error(), 256)
			b.mu.Unlock()
			return
		}
		if len(encoded) > defaultHistoryBytes {
			b.mu.Lock()
			if b.historyRevision != revision {
				b.mu.Unlock()
				continue
			}
			if b.count <= 1 {
				b.historyErr = "latest observation exceeds retained history byte limit"
				b.mu.Unlock()
				return
			}
			b.items[b.start] = Record{}
			b.start = (b.start + 1) % b.capacity
			b.count--
			b.historyRevision++
			b.mu.Unlock()
			continue
		}

		b.mu.Lock()
		if b.historyRevision != revision {
			b.mu.Unlock()
			continue
		}
		b.mu.Unlock()
		if err := replaceHistory(path, encoded); err != nil {
			b.mu.Lock()
			b.historyErr = "viewer history could not be persisted"
			b.mu.Unlock()
			return
		}
		b.mu.Lock()
		if b.historyRevision == revision {
			b.historyDirty = false
			b.historyErr = ""
		}
		dirty := b.historyDirty
		b.mu.Unlock()
		if !dirty {
			return
		}
	}
}

func (b *Buffer) historyStateLocked() historyState {
	tests := make([]TestCase, 0, len(b.tests))
	for _, item := range b.tests {
		tests = append(tests, cloneTestCase(item))
	}
	packages := make([]TestPackage, 0, len(b.packages))
	for _, item := range b.packages {
		packages = append(packages, item)
	}
	var run *TestRunManifest
	if b.run != nil {
		copy := *b.run
		copy.Packages = append([]string(nil), b.run.Packages...)
		copy.SourceIdentity = cloneSourceIdentity(b.run.SourceIdentity)
		run = &copy
	}
	checkpoints := make([]SourceCheckpoint, 0, len(b.checkpoints))
	for _, checkpoint := range b.checkpoints {
		checkpoints = append(checkpoints, checkpoint)
	}
	retained := make([]RetainedRun, len(b.retainedRuns))
	for i, item := range b.retainedRuns {
		retained[i] = cloneRetainedRun(item)
	}
	return historyState{SchemaVersion: historySchema, Epoch: b.epoch, NextID: b.nextID,
		Records: b.snapshotLocked(), Sources: append([]SourceInfo(nil), b.sources...), Checkpoints: checkpoints, Warnings: append([]string(nil), b.warnings...), Run: run, RunError: b.runError,
		Tests: tests, Packages: packages, DroppedTests: b.droppedTests,
		Audit: cloneAuditProjection(b.audit), Generations: cloneGenerationProjection(b.generations), RetainedRuns: retained}
}

func replaceHistory(path string, encoded []byte) error {
	directory := filepath.Dir(path)
	base := filepath.Base(path)
	temporary, err := os.CreateTemp(directory, "."+base+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create observation history replacement: %w", err)
	}
	tempName := temporary.Name()
	defer os.Remove(tempName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect observation history replacement: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write observation history replacement: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync observation history replacement: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close observation history replacement: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace observation history: %w", err)
	}
	return nil
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && (value[len(value)-1]&0xc0) == 0x80 {
		value = value[:len(value)-1]
	}
	return value
}
