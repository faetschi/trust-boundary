package webview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	maxTestCases           = 256
	maxTestPackages        = 128
	maxTestOutputBytes     = 4096
	maxPackageOutput       = 2048
	maxProjectionItems     = 256
	maxRetainedRuns        = 16
	maxRetainedRunTests    = 64
	maxRetainedRunPackages = 32
)

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)

// SourceInfo deliberately omits filesystem paths. A source epoch changes when
// a configured input is replaced, truncated, or reopened after a server restart.
type SourceInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Epoch      string `json:"epoch,omitempty"`
	Continuity string `json:"continuity,omitempty"`
	Gap        bool   `json:"gap,omitempty"`
	GapReason  string `json:"gap_reason,omitempty"`
}

// SourceCheckpoint retains only a bounded stream position and full-prefix content
// fingerprint. It is a restart aid, not a source-authentication statement.
type SourceCheckpoint struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Epoch       string `json:"epoch"`
	RunID       string `json:"run_id,omitempty"`
	Offset      int64  `json:"offset"`
	Fingerprint string `json:"fingerprint"`
}

type RunSourceIdentity struct {
	SchemaVersion string `json:"schema_version"`
	Commit        string `json:"commit,omitempty"`
	SourceEpoch   string `json:"source_epoch,omitempty"`
	Dirty         *bool  `json:"dirty,omitempty"`
	SourceDigest  string `json:"source_digest,omitempty"`
}

// TestRunManifest is the versioned, operator-produced lifecycle record for an
// external `go test -json` process. tbound-web consumes it; it never starts or
// controls that process.
type TestRunManifest struct {
	SchemaVersion  string             `json:"schema_version"`
	RunID          string             `json:"run_id"`
	State          string             `json:"state"`
	StartedAt      time.Time          `json:"started_at"`
	FinishedAt     *time.Time         `json:"finished_at,omitempty"`
	ExitCode       *int               `json:"exit_code,omitempty"`
	GoVersion      string             `json:"go_version,omitempty"`
	Module         string             `json:"module,omitempty"`
	Packages       []string           `json:"packages,omitempty"`
	SourceIdentity *RunSourceIdentity `json:"source_identity,omitempty"`
}

type TestPackage struct {
	Name                  string    `json:"package"`
	State                 string    `json:"state"`
	Output                string    `json:"output,omitempty"`
	OutputTruncated       bool      `json:"output_truncated,omitempty"`
	NoTestFilesProvenance string    `json:"no_test_files_provenance,omitempty"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type TestCase struct {
	Package         string    `json:"package"`
	Name            string    `json:"test"`
	State           string    `json:"state"`
	Elapsed         *float64  `json:"elapsed,omitempty"`
	Output          string    `json:"output,omitempty"`
	OutputTruncated bool      `json:"output_truncated,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type TestCatalog struct {
	Packages []TestPackage `json:"packages"`
	Tests    []TestCase    `json:"tests"`
	Dropped  int           `json:"dropped"`
}

type RetainedRun struct {
	Run          TestRunManifest `json:"run"`
	Tests        TestCatalog     `json:"tests"`
	RetainedAt   time.Time       `json:"retained_at"`
	OutputPolicy string          `json:"output_policy"`
}

type RetainedRunCatalog struct {
	SchemaVersion string        `json:"schema_version"`
	CurrentRunID  string        `json:"current_run_id,omitempty"`
	Runs          []RetainedRun `json:"runs"`
}

type HistoryStatus struct {
	Enabled bool   `json:"enabled"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

type ObservationManifest struct {
	SchemaVersion string           `json:"schema_version"`
	Epoch         string           `json:"epoch"`
	Sources       []SourceInfo     `json:"sources"`
	History       HistoryStatus    `json:"history"`
	TestRun       *TestRunManifest `json:"test_run,omitempty"`
	TestRunError  string           `json:"test_run_error,omitempty"`
	Warnings      []string         `json:"warnings"`
}

type AuditRecordProjection struct {
	Sequence uint64 `json:"sequence"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Outcome  string `json:"outcome,omitempty"`
	Hash     string `json:"hash"`
}

type AuditEffectProjection struct {
	ID              string `json:"id"`
	IntentSequence  uint64 `json:"intent_sequence"`
	Outcome         string `json:"outcome,omitempty"`
	OutcomeSequence uint64 `json:"outcome_sequence,omitempty"`
	Unresolved      bool   `json:"unresolved"`
}

type AuditProjection struct {
	SchemaVersion string                  `json:"schema_version"`
	Status        string                  `json:"status"`
	Error         string                  `json:"error,omitempty"`
	RecordCount   uint64                  `json:"record_count"`
	HeadHash      string                  `json:"head_hash,omitempty"`
	EffectCount   int                     `json:"effect_count"`
	Records       []AuditRecordProjection `json:"records,omitempty"`
	Effects       []AuditEffectProjection `json:"effects,omitempty"`
	Truncated     bool                    `json:"truncated,omitempty"`
}

type GenerationProjection struct {
	SchemaVersion string   `json:"schema_version"`
	Status        string   `json:"status"`
	Error         string   `json:"error,omitempty"`
	Schema        string   `json:"evidence_schema,omitempty"`
	Baseline      string   `json:"baseline,omitempty"`
	Sealed        string   `json:"sealed,omitempty"`
	Generations   []string `json:"generations,omitempty"`
	AuditRecords  uint64   `json:"verified_audit_records,omitempty"`
	AuditHeadHash string   `json:"verified_audit_head_hash,omitempty"`
	Attempts      int      `json:"attempts,omitempty"`
	Decisions     int      `json:"decisions,omitempty"`
	Effects       int      `json:"effects,omitempty"`
	Successes     int      `json:"successes,omitempty"`
	Unknowns      int      `json:"unknowns,omitempty"`
	Denials       int      `json:"denials,omitempty"`
	Quarantined   bool     `json:"quarantined,omitempty"`
	Limitations   []string `json:"limitations,omitempty"`
}

type ExplorerSnapshot struct {
	SchemaVersion string               `json:"schema_version"`
	Epoch         string               `json:"epoch"`
	Cursor        string               `json:"cursor"`
	EarliestID    uint64               `json:"earliest_id"`
	Records       []Record             `json:"records"`
	Manifest      ObservationManifest  `json:"manifest"`
	Tests         TestCatalog          `json:"tests"`
	Audit         AuditProjection      `json:"audit"`
	Generations   GenerationProjection `json:"generations"`
}

type ReplayGap struct {
	SchemaVersion   string `json:"schema_version"`
	Reason          string `json:"reason"`
	RequestedCursor string `json:"requested_cursor"`
	Epoch           string `json:"epoch"`
	Earliest        uint64 `json:"earliest_id,omitempty"`
	Latest          uint64 `json:"latest_id"`
}

type goTestEvent struct {
	Time        time.Time `json:"Time,omitempty"`
	Action      string    `json:"Action"`
	Package     string    `json:"Package"`
	Test        string    `json:"Test,omitempty"`
	Elapsed     *float64  `json:"Elapsed,omitempty"`
	Output      string    `json:"Output,omitempty"`
	OutputType  string    `json:"OutputType,omitempty"`
	FailedBuild string    `json:"FailedBuild,omitempty"`
}

// DecodeTestRunManifest validates the exact versioned lifecycle contract.
// Unknown fields are rejected so an unrelated file cannot silently look like
// a trustworthy run manifest.
func DecodeTestRunManifest(raw []byte) (TestRunManifest, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return TestRunManifest{}, errors.New("run manifest is empty or exceeds 65536 bytes")
	}
	if err := validateExactObjectFields(raw, "schema_version", "run_id", "state", "started_at", "finished_at", "exit_code", "go_version", "module", "packages", "source_identity"); err != nil {
		return TestRunManifest{}, fmt.Errorf("run manifest JSON is invalid: %w", err)
	}
	var fieldMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fieldMap); err != nil {
		return TestRunManifest{}, err
	}
	if identity, ok := fieldMap["source_identity"]; ok && string(identity) != "null" {
		if err := validateExactObjectFields(identity, "schema_version", "commit", "source_epoch", "dirty", "source_digest"); err != nil {
			return TestRunManifest{}, fmt.Errorf("source_identity JSON is invalid: %w", err)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest TestRunManifest
	if err := decoder.Decode(&manifest); err != nil {
		return TestRunManifest{}, fmt.Errorf("decode run manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return TestRunManifest{}, errors.New("run manifest has trailing JSON data")
	}
	if manifest.SchemaVersion != "tbound-go-test-run/v1" {
		return TestRunManifest{}, errors.New("unsupported run manifest schema_version")
	}
	if !runIDPattern.MatchString(manifest.RunID) {
		return TestRunManifest{}, errors.New("run manifest run_id must contain 1-256 letters, digits, '.', '_', ':', or '-'")
	}
	switch manifest.State {
	case "running", "passed", "failed", "interrupted":
	default:
		return TestRunManifest{}, errors.New("run manifest state must be running, passed, failed, or interrupted")
	}
	if manifest.StartedAt.IsZero() {
		return TestRunManifest{}, errors.New("run manifest started_at is required")
	}
	if manifest.FinishedAt != nil && manifest.FinishedAt.Before(manifest.StartedAt) {
		return TestRunManifest{}, errors.New("run manifest finished_at precedes started_at")
	}
	if manifest.State == "running" && manifest.FinishedAt != nil {
		return TestRunManifest{}, errors.New("running manifest must not have finished_at")
	}
	if manifest.State != "running" && manifest.FinishedAt == nil {
		return TestRunManifest{}, errors.New("terminal manifest requires finished_at")
	}
	if (manifest.State == "passed" || manifest.State == "failed") && manifest.ExitCode == nil {
		return TestRunManifest{}, errors.New("passed/failed run manifest requires exit_code")
	}
	if manifest.ExitCode != nil {
		if *manifest.ExitCode < 0 {
			return TestRunManifest{}, errors.New("run manifest exit_code must not be negative")
		}
		if manifest.State == "passed" && *manifest.ExitCode != 0 {
			return TestRunManifest{}, errors.New("passed run manifest requires exit_code 0")
		}
		if manifest.State == "failed" && *manifest.ExitCode == 0 {
			return TestRunManifest{}, errors.New("failed run manifest requires a nonzero exit_code")
		}
	}
	if len(manifest.Packages) > maxTestPackages {
		return TestRunManifest{}, errors.New("run manifest package list exceeds limit")
	}
	for _, name := range manifest.Packages {
		if strings.TrimSpace(name) == "" || len(name) > 1024 {
			return TestRunManifest{}, errors.New("run manifest contains an invalid package name")
		}
	}
	if len(manifest.GoVersion) > 128 || len(manifest.Module) > 1024 {
		return TestRunManifest{}, errors.New("run manifest identity field exceeds limit")
	}
	if identity := manifest.SourceIdentity; identity != nil {
		if identity.SchemaVersion != "tbound-run-source-identity/v1" {
			return TestRunManifest{}, errors.New("unsupported source_identity schema_version")
		}
		if len(identity.Commit) != 0 && !validHex(identity.Commit, 40) && !validHex(identity.Commit, 64) {
			return TestRunManifest{}, errors.New("source_identity commit must be 40 or 64 hexadecimal characters")
		}
		if len(identity.SourceEpoch) > 128 || strings.Contains(identity.SourceEpoch, "\x00") {
			return TestRunManifest{}, errors.New("source_identity source_epoch exceeds limit")
		}
		if identity.SourceDigest != "" && !validSHA256Digest(identity.SourceDigest) {
			return TestRunManifest{}, errors.New("source_identity source_digest must be sha256:<64 hexadecimal characters>")
		}
		if identity.Commit == "" && identity.SourceEpoch == "" && identity.SourceDigest == "" {
			return TestRunManifest{}, errors.New("source_identity must include commit, source_epoch, or source_digest")
		}
	}
	return manifest, nil
}

func validHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return false
		}
	}
	return true
}

func validSHA256Digest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validHex(strings.TrimPrefix(value, "sha256:"), 64)
}

func (b *Buffer) setSources(sources []SourceInfo) {
	b.mu.Lock()
	previous := make(map[string]SourceInfo, len(b.sources))
	for _, source := range b.sources {
		previous[source.Name+"\x00"+source.Kind] = source
	}
	b.sources = append([]SourceInfo(nil), sources...)
	for i := range b.sources {
		if old, ok := previous[b.sources[i].Name+"\x00"+b.sources[i].Kind]; ok {
			b.sources[i].Epoch = old.Epoch
			b.sources[i].Continuity = old.Continuity
			b.sources[i].Gap = old.Gap
			b.sources[i].GapReason = old.GapReason
		}
	}
	b.persistLocked()
	b.mu.Unlock()
	b.flushHistory()
}

func (b *Buffer) sourceCheckpoint(name string) (SourceCheckpoint, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	checkpoint, ok := b.checkpoints[name]
	return checkpoint, ok
}

func (b *Buffer) setSourceCheckpoint(checkpoint SourceCheckpoint) {
	b.mu.Lock()
	if b.checkpoints == nil {
		b.checkpoints = make(map[string]SourceCheckpoint)
	}
	b.checkpoints[checkpoint.Name] = checkpoint
	b.persistLocked()
	b.mu.Unlock()
	b.flushHistory()
}

func (b *Buffer) setSourceContinuity(name, continuity string) {
	b.mu.Lock()
	for i := range b.sources {
		if b.sources[i].Name == name {
			b.sources[i].Continuity = continuity
			b.persistLocked()
			b.mu.Unlock()
			b.flushHistory()
			return
		}
	}
	b.mu.Unlock()
}

func (b *Buffer) markSourceGap(name, reason string) {
	b.mu.Lock()
	reason = truncateUTF8(reason, 256)
	for i := range b.sources {
		if b.sources[i].Name == name {
			b.sources[i].Gap = true
			b.sources[i].Continuity = "gap"
			b.sources[i].GapReason = reason
		}
	}
	for key, test := range b.tests {
		test.State = "unresolved"
		test.UpdatedAt = time.Now().UTC()
		b.tests[key] = test
	}
	for key, pkg := range b.packages {
		pkg.State = "unresolved"
		pkg.UpdatedAt = time.Now().UTC()
		b.packages[key] = pkg
	}
	if b.run != nil {
		for _, packageName := range b.run.Packages {
			if _, exists := b.packages[packageName]; exists {
				continue
			}
			if len(b.packages) >= maxTestPackages {
				b.incrementDroppedLocked()
				continue
			}
			b.packages[packageName] = TestPackage{Name: packageName, State: "unresolved", UpdatedAt: time.Now().UTC()}
		}
	}
	b.addWarningLocked(name + ": source gap: " + reason)
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	b.publishLocked(Record{Source: name, Types: []string{"source_gap"}, Record: payload})
	b.mu.Unlock()
	b.flushHistory()
}

func (b *Buffer) addWarning(source, warning string) {
	b.mu.Lock()
	b.addWarningLocked(source + ": " + warning)
	b.persistLocked()
	b.mu.Unlock()
	b.flushHistory()
}

func (b *Buffer) addWarningLocked(warning string) {
	warning = truncateUTF8(warning, 512)
	for _, existing := range b.warnings {
		if existing == warning {
			return
		}
	}
	if len(b.warnings) >= 32 {
		copy(b.warnings, b.warnings[1:])
		b.warnings = b.warnings[:31]
	}
	b.warnings = append(b.warnings, warning)
}

func (b *Buffer) setRunManifestError(message, source, sourceEpoch string) {
	b.mu.Lock()
	b.runError = truncateUTF8(message, 256)
	b.addWarningLocked(source + ": run manifest update rejected: " + b.runError)
	encoded, _ := json.Marshal(map[string]string{"status": "invalid", "error": truncateUTF8(message, 256)})
	b.publishLocked(Record{Source: source, SourceEpoch: sourceEpoch, Types: []string{"run_manifest_error"}, Record: encoded})
	b.mu.Unlock()
	b.flushHistory()
}

func (b *Buffer) currentRunID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.run == nil {
		return ""
	}
	return b.run.RunID
}

func (b *Buffer) hasRunManifestError() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.runError != ""
}

func (b *Buffer) setSourceEpoch(name, epoch, reason string) {
	b.mu.Lock()
	previous := ""
	for i := range b.sources {
		if b.sources[i].Name == name {
			previous = b.sources[i].Epoch
			b.sources[i].Epoch = epoch
			break
		}
	}
	if previous != "" && previous != epoch {
		payload, _ := json.Marshal(map[string]string{"previous_epoch": previous, "source_epoch": epoch, "reason": reason})
		b.publishLocked(Record{Source: name, SourceEpoch: epoch, Types: []string{"source_epoch"}, Record: payload})
	} else {
		b.persistLocked()
	}
	b.mu.Unlock()
	b.flushHistory()
}

func (b *Buffer) setRunManifest(manifest TestRunManifest, source, sourceEpoch string) error {
	b.mu.Lock()
	if b.run != nil {
		previous := *b.run
		if previous.RunID == manifest.RunID {
			if !sameRunIdentity(previous, manifest) {
				b.mu.Unlock()
				return errors.New("run manifest changed immutable run identity")
			}
			if previous.State != "running" && !sameTerminalRun(previous, manifest) {
				b.mu.Unlock()
				return errors.New("terminal run manifest state cannot be changed")
			}
		} else {
			if previous.State == "running" {
				previous.State = "interrupted"
				finished := time.Now().UTC()
				previous.FinishedAt = &finished
				previous.ExitCode = nil
				b.run = &previous
				for key, test := range b.tests {
					if test.State == "running" || test.State == "paused" || test.State == "pending" {
						test.State = "unresolved"
						b.tests[key] = test
					}
				}
				for key, pkg := range b.packages {
					if pkg.State == "running" || pkg.State == "pending" {
						pkg.State = "unresolved"
						b.packages[key] = pkg
					}
				}
				b.addWarningLocked("run manifest: previous run was superseded before its terminal process exit was observed")
			}
			b.archiveCurrentRunLocked()
			b.tests = make(map[string]TestCase)
			b.packages = make(map[string]TestPackage)
			b.droppedTests = 0
		}
	}
	copy := manifest
	copy.Packages = append([]string(nil), manifest.Packages...)
	copy.SourceIdentity = cloneSourceIdentity(manifest.SourceIdentity)
	b.run = &copy
	if manifest.State == "running" {
		b.trimRetainedRunsLocked(maxRetainedRuns - 1)
	}
	b.runError = ""
	if manifest.State != "running" {
		for key, test := range b.tests {
			if test.State == "running" || test.State == "paused" {
				test.State = "interrupted"
				test.UpdatedAt = time.Now().UTC()
				b.tests[key] = test
			} else if test.State == "pending" {
				test.State = "unresolved"
				test.UpdatedAt = time.Now().UTC()
				b.tests[key] = test
			}
		}
		for key, pkg := range b.packages {
			if pkg.State == "running" {
				pkg.State = "interrupted"
				pkg.UpdatedAt = time.Now().UTC()
				b.packages[key] = pkg
			} else if pkg.State == "pending" {
				pkg.State = "unresolved"
				pkg.UpdatedAt = time.Now().UTC()
				b.packages[key] = pkg
			}
		}
		for _, packageName := range manifest.Packages {
			if _, observed := b.packages[packageName]; !observed {
				if len(b.packages) >= maxTestPackages {
					b.incrementDroppedLocked()
					continue
				}
				b.packages[packageName] = TestPackage{Name: packageName, State: "unresolved", UpdatedAt: time.Now().UTC()}
			}
		}
	}
	if manifest.State != "running" {
		for _, pkg := range b.packages {
			if (pkg.State == "failed" || pkg.State == "unresolved" || pkg.State == "interrupted") && manifest.State == "passed" {
				b.addWarningLocked("run manifest: process exit reports passed while Go package events include a failure or unresolved package")
			}
		}
		for _, test := range b.tests {
			if (test.State == "failed" || test.State == "unresolved" || test.State == "interrupted") && manifest.State == "passed" {
				b.addWarningLocked("run manifest: process exit reports passed while Go test events include a failure or unresolved test")
			}
		}
		b.archiveCurrentRunLocked()
	}
	encoded, _ := json.Marshal(manifest)
	b.publishLocked(Record{Source: source, SourceEpoch: sourceEpoch, Types: []string{"run_manifest"}, Record: encoded})
	b.mu.Unlock()
	b.flushHistory()
	return nil
}

func sameRunIdentity(left, right TestRunManifest) bool {
	if !left.StartedAt.Equal(right.StartedAt) || left.GoVersion != right.GoVersion || left.Module != right.Module || len(left.Packages) != len(right.Packages) || !sameSourceIdentity(left.SourceIdentity, right.SourceIdentity) {
		return false
	}
	for i := range left.Packages {
		if left.Packages[i] != right.Packages[i] {
			return false
		}
	}
	return true
}

func sameSourceIdentity(left, right *RunSourceIdentity) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	if left == nil {
		return true
	}
	if left.SchemaVersion != right.SchemaVersion || left.Commit != right.Commit || left.SourceEpoch != right.SourceEpoch || left.SourceDigest != right.SourceDigest || (left.Dirty == nil) != (right.Dirty == nil) {
		return false
	}
	return left.Dirty == nil || *left.Dirty == *right.Dirty
}

func cloneSourceIdentity(identity *RunSourceIdentity) *RunSourceIdentity {
	if identity == nil {
		return nil
	}
	copy := *identity
	if identity.Dirty != nil {
		dirty := *identity.Dirty
		copy.Dirty = &dirty
	}
	return &copy
}

func sameTerminalRun(left, right TestRunManifest) bool {
	if left.State != right.State || !sameRunIdentity(left, right) {
		return false
	}
	if (left.ExitCode == nil) != (right.ExitCode == nil) {
		return false
	}
	if left.ExitCode != nil && *left.ExitCode != *right.ExitCode {
		return false
	}
	if (left.FinishedAt == nil) != (right.FinishedAt == nil) {
		return false
	}
	return left.FinishedAt == nil || left.FinishedAt.Equal(*right.FinishedAt)
}

func (b *Buffer) applyGoTestEvent(event goTestEvent, source, sourceEpoch string) error {
	if len(event.Package) == 0 || len(event.Package) > 1024 || len(event.Test) > 2048 || len(event.Output) > DefaultMaxLineBytes || len(event.Action) > 64 || len(event.OutputType) > 64 || len(event.FailedBuild) > 1024 {
		return errors.New("go test JSON event exceeds a field limit")
	}
	if !validGoTestAction(event.Action) {
		return fmt.Errorf("unsupported go test JSON Action %q", event.Action)
	}
	if event.Elapsed != nil && (*event.Elapsed < 0 || *event.Elapsed > 1e9) {
		return errors.New("go test JSON elapsed value is outside the supported range")
	}
	now := time.Now().UTC()
	var projection any
	b.mu.Lock()
	if event.Test == "" {
		pkg, exists := b.packages[event.Package]
		if !exists && len(b.packages) >= maxTestPackages {
			b.incrementDroppedLocked()
			b.persistLocked()
			b.mu.Unlock()
			b.flushHistory()
			return nil
		}
		if !exists {
			pkg = TestPackage{Name: event.Package, State: "pending"}
		}
		if event.Action == "pass" {
			if aggregate := b.packageFailureStateLocked(event.Package); aggregate != "" {
				pkg.State = aggregate
				pkg.UpdatedAt = now
			} else {
				applyPackageEvent(&pkg, event, now)
			}
		} else {
			applyPackageEvent(&pkg, event, now)
		}
		b.packages[event.Package] = pkg
		projection = pkg
	} else {
		key := event.Package + "\x00" + event.Test
		test, exists := b.tests[key]
		if !exists && len(b.tests) >= maxTestCases {
			b.incrementDroppedLocked()
			b.persistLocked()
			b.mu.Unlock()
			b.flushHistory()
			return nil
		}
		if !exists {
			test = TestCase{Package: event.Package, Name: event.Test, State: "pending"}
		}
		applyTestEvent(&test, event, now)
		b.tests[key] = test
		projection = test
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		b.mu.Unlock()
		return err
	}
	b.publishLocked(Record{Source: source, SourceEpoch: sourceEpoch, Types: []string{"go_test"}, Record: encoded})
	b.mu.Unlock()
	b.flushHistory()
	return nil
}

func (b *Buffer) packageFailureStateLocked(packageName string) string {
	unresolved := false
	for _, test := range b.tests {
		if test.Package != packageName {
			continue
		}
		if test.State == "failed" {
			return "failed"
		}
		if test.State == "unresolved" || test.State == "interrupted" {
			unresolved = true
		}
	}
	if unresolved {
		return "unresolved"
	}
	return ""
}

func (b *Buffer) incrementDroppedLocked() {
	if b.droppedTests < int(^uint(0)>>1) {
		b.droppedTests++
	}
}

func (b *Buffer) markGoTestEventUnresolved(event goTestEvent, source, reason string) {
	b.mu.Lock()
	if event.Package != "" && len(event.Package) <= 1024 {
		pkg, packageExists := b.packages[event.Package]
		if packageExists || len(b.packages) < maxTestPackages {
			pkg.Name, pkg.State, pkg.UpdatedAt = event.Package, "unresolved", time.Now().UTC()
			b.packages[event.Package] = pkg
		} else {
			b.incrementDroppedLocked()
		}
		if event.Test == "" {
			for key, test := range b.tests {
				if test.Package == event.Package {
					test.State = "unresolved"
					test.UpdatedAt = time.Now().UTC()
					b.tests[key] = test
				}
			}
		} else if len(event.Test) <= 2048 {
			key := event.Package + "\x00" + event.Test
			test, exists := b.tests[key]
			if exists || len(b.tests) < maxTestCases {
				test.Package, test.Name, test.State, test.UpdatedAt = event.Package, event.Test, "unresolved", time.Now().UTC()
				b.tests[key] = test
			} else {
				b.incrementDroppedLocked()
			}
		}
	}
	b.addWarningLocked(source + ": " + reason)
	b.persistLocked()
	b.mu.Unlock()
	b.flushHistory()
}

func applyPackageEvent(pkg *TestPackage, event goTestEvent, now time.Time) {
	pkg.UpdatedAt = now
	switch event.Action {
	case "start":
		pkg.State = "running"
	case "pass":
		if pkg.State != "no_test_files" && pkg.State != "failed" && pkg.State != "unresolved" && pkg.State != "interrupted" {
			pkg.State = "passed"
		}
	case "fail":
		pkg.State = "failed"
	case "build-fail":
		pkg.State = "failed"
	case "skip":
		if hasNoTestFilesMarker(pkg.Output) {
			pkg.State = "no_test_files"
		} else {
			pkg.State = "skipped"
		}
	case "output":
		pkg.Output, pkg.OutputTruncated = appendBounded(pkg.Output, event.Output, maxPackageOutput, pkg.OutputTruncated)
		if hasNoTestFilesMarker(pkg.Output) {
			pkg.State = "no_test_files"
			pkg.NoTestFilesProvenance = "go-test-output-marker"
		}
	case "build-output":
		pkg.Output, pkg.OutputTruncated = appendBounded(pkg.Output, event.Output, maxPackageOutput, pkg.OutputTruncated)
	}
}

func validGoTestAction(action string) bool {
	switch action {
	case "start", "run", "pause", "cont", "pass", "bench", "fail", "output", "skip", "build-output", "build-fail":
		return true
	}
	return false
}

func hasNoTestFilesMarker(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) == 5 && fields[0] == "?" && fields[1] != "" && fields[2] == "[no" && fields[3] == "test" && fields[4] == "files]" {
			return true
		}
	}
	return false
}

func applyTestEvent(test *TestCase, event goTestEvent, now time.Time) {
	test.UpdatedAt = now
	if event.Elapsed != nil {
		elapsed := *event.Elapsed
		test.Elapsed = &elapsed
	}
	switch event.Action {
	case "run":
		test.State = "running"
	case "pause":
		test.State = "paused"
	case "cont":
		test.State = "running"
	case "pass":
		if test.State != "failed" && test.State != "unresolved" && test.State != "interrupted" {
			test.State = "passed"
		}
	case "bench":
		if test.State != "failed" && test.State != "unresolved" && test.State != "interrupted" {
			test.State = "passed"
		}
	case "build-fail":
		test.State = "failed"
	case "fail":
		test.State = "failed"
	case "skip":
		test.State = "skipped"
	case "output":
		test.Output, test.OutputTruncated = appendBounded(test.Output, event.Output, maxTestOutputBytes, test.OutputTruncated)
	case "build-output":
		test.Output, test.OutputTruncated = appendBounded(test.Output, event.Output, maxTestOutputBytes, test.OutputTruncated)
	}
}

func appendBounded(previous, next string, limit int, truncated bool) (string, bool) {
	combined := previous + next
	if len(combined) <= limit {
		return combined, truncated
	}
	combined = combined[len(combined)-limit:]
	for len(combined) > 0 && (combined[0]&0xc0) == 0x80 {
		combined = combined[1:]
	}
	return combined, true
}

func decodeGoTestEvent(raw []byte) (goTestEvent, error) {
	if len(raw) > DefaultMaxLineBytes {
		return goTestEvent{}, errors.New("go test event exceeds line limit")
	}
	var event goTestEvent
	if err := validateExactObjectFields(raw, "Time", "Action", "Package", "Test", "Elapsed", "Output", "OutputType", "FailedBuild"); err != nil {
		return goTestEvent{}, err
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return goTestEvent{}, err
	}
	if !validGoTestAction(event.Action) {
		return event, fmt.Errorf("unsupported go test JSON Action %q", event.Action)
	}
	return event, nil
}

func (b *Buffer) setAudit(projection AuditProjection, source, sourceEpoch string) {
	b.mu.Lock()
	b.audit = cloneAuditProjection(projection)
	encoded, _ := json.Marshal(projection)
	b.publishLocked(Record{Source: source, SourceEpoch: sourceEpoch, Types: []string{"audit_projection"}, Record: encoded})
	b.mu.Unlock()
	b.flushHistory()
}

func (b *Buffer) setGeneration(projection GenerationProjection, source, sourceEpoch string) {
	b.mu.Lock()
	b.generations = cloneGenerationProjection(projection)
	encoded, _ := json.Marshal(projection)
	b.publishLocked(Record{Source: source, SourceEpoch: sourceEpoch, Types: []string{"generation_projection"}, Record: encoded})
	b.mu.Unlock()
	b.flushHistory()
}

func cloneAuditProjection(value AuditProjection) AuditProjection {
	value.Records = append([]AuditRecordProjection(nil), value.Records...)
	value.Effects = append([]AuditEffectProjection(nil), value.Effects...)
	return value
}

func cloneGenerationProjection(value GenerationProjection) GenerationProjection {
	value.Generations = append([]string(nil), value.Generations...)
	value.Limitations = append([]string(nil), value.Limitations...)
	return value
}
