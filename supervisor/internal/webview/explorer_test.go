package webview

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
)

func TestVersionedRunManifestFixturesAndFailures(t *testing.T) {
	running, err := os.ReadFile(filepath.Join("testdata", "run-manifest-running.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeTestRunManifest(running)
	if err != nil {
		t.Fatalf("decode running fixture: %v", err)
	}
	if manifest.State != "running" || manifest.RunID != "fixture-run-1" || len(manifest.Packages) != 2 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}

	invalidVersion, err := os.ReadFile(filepath.Join("testdata", "run-manifest-invalid-version.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTestRunManifest(invalidVersion); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("invalid schema error = %v", err)
	}

	validTerminal := `{"schema_version":"tbound-go-test-run/v1","run_id":"r","state":"passed","started_at":"2026-10-05T10:00:00Z","finished_at":"2026-10-05T10:01:00Z","exit_code":0}`
	if _, err := DecodeTestRunManifest([]byte(validTerminal)); err != nil {
		t.Fatalf("decode terminal manifest: %v", err)
	}
	for name, raw := range map[string]string{
		"unknown field":    strings.TrimSuffix(validTerminal, "}") + `,"unexpected":true}`,
		"trailing value":   validTerminal + `{}`,
		"running finished": `{"schema_version":"tbound-go-test-run/v1","run_id":"r","state":"running","started_at":"2026-10-05T10:00:00Z","finished_at":"2026-10-05T10:01:00Z"}`,
		"passed nonzero":   `{"schema_version":"tbound-go-test-run/v1","run_id":"r","state":"passed","started_at":"2026-10-05T10:00:00Z","finished_at":"2026-10-05T10:01:00Z","exit_code":1}`,
		"missing finish":   `{"schema_version":"tbound-go-test-run/v1","run_id":"r","state":"interrupted","started_at":"2026-10-05T10:00:00Z"}`,
		"oversized":        strings.Repeat("x", 64<<10|1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTestRunManifest([]byte(raw)); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}
	identity := `{"schema_version":"tbound-go-test-run/v1","run_id":"identity-run","state":"running","started_at":"2026-10-05T10:00:00Z","source_identity":{"schema_version":"tbound-run-source-identity/v1","commit":"0123456789012345678901234567890123456789","source_epoch":"checkout-2026-10-05","dirty":false}}`
	if _, err := DecodeTestRunManifest([]byte(identity)); err != nil {
		t.Fatalf("valid self-declared source identity rejected: %v", err)
	}
	invalidUTF8 := append([]byte(`{"schema_version":"tbound-go-test-run/v1","run_id":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	for name, raw := range map[string][]byte{
		"duplicate top-level key": []byte(strings.Replace(validTerminal, `"state":"passed"`, `"state":"failed","state":"passed"`, 1)),
		"duplicate nested key":    []byte(strings.TrimSuffix(identity, "}") + `,"source_identity":{"schema_version":"tbound-run-source-identity/v1","schema_version":"tbound-run-source-identity/v1","commit":"0123456789012345678901234567890123456789"}}`),
		"invalid UTF-8":           invalidUTF8,
		"bad commit":              []byte(strings.Replace(identity, "0123456789012345678901234567890123456789", "bad", 1)),
		"excessive nesting":       []byte(strings.Repeat("[", maxJSONNestingDepth+1) + "0" + strings.Repeat("]", maxJSONNestingDepth+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTestRunManifest(raw); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestGoTestJSONFixtureLiveTransitionsAndManifestFinalization(t *testing.T) {
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "go-test.jsonl")
	manifestPath := filepath.Join(directory, "run.json")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join("testdata", "run-manifest-running.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(128)
	tailer, err := NewTailer([]Source{
		{Name: "Go test events", Path: eventsPath, Kind: "go-test"},
		{Name: "Go test lifecycle", Path: manifestPath, Kind: "manifest"},
	}, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := tailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer stopTestTailer(t, cancel, tailer)

	fixture, err := os.ReadFile(filepath.Join("testdata", "go-test-json-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(fixture); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	waitForTestCatalog(t, buffer, 6, 2)

	before := buffer.snapshot()
	if before.Manifest.TestRun == nil || before.Manifest.TestRun.State != "running" {
		t.Fatalf("run manifest was not loaded: %+v", before.Manifest)
	}
	assertTestState(t, before.Tests, "example.test/project/pkg", "TestPass", "passed")
	assertTestState(t, before.Tests, "example.test/project/pkg", "TestNested/child", "passed")
	assertTestState(t, before.Tests, "example.test/project/pkg", "TestNested/skip", "skipped")
	assertTestState(t, before.Tests, "example.test/project/pkg", "TestNested", "passed")
	assertPackageState(t, before.Tests, "example.test/project/empty", "no_test_files")

	finished := time.Date(2026, 10, 5, 10, 1, 0, 0, time.UTC)
	manifest := TestRunManifest{
		SchemaVersion: "tbound-go-test-run/v1", RunID: "fixture-run-1", State: "failed",
		StartedAt: time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC), FinishedAt: &finished,
		GoVersion: before.Manifest.TestRun.GoVersion, Module: before.Manifest.TestRun.Module,
		Packages: append([]string(nil), before.Manifest.TestRun.Packages...),
	}
	manifest.ExitCode = new(int)
	*manifest.ExitCode = 1
	terminal, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, terminal, 0o600); err != nil {
		t.Fatal(err)
	}
	after := waitForManifest(t, buffer, "failed")
	assertTestState(t, after.Tests, "example.test/project/pkg", "TestPass", "passed")
	assertTestState(t, after.Tests, "example.test/project/pkg", "TestFail", "failed")
	assertTestState(t, after.Tests, "example.test/project/pkg", "TestNested/child", "passed")
	assertTestState(t, after.Tests, "example.test/project/pkg", "TestNested/skip", "skipped")
	assertTestState(t, after.Tests, "example.test/project/pkg", "TestInterrupted", "interrupted")
	assertPackageState(t, after.Tests, "example.test/project/pkg", "failed")
	assertPackageState(t, after.Tests, "example.test/project/empty", "no_test_files")

	invalidTransition := manifest
	invalidTransition.State = "interrupted"
	terminal, err = json.Marshal(invalidTransition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, terminal, 0o600); err != nil {
		t.Fatal(err)
	}
	stale := waitForManifestError(t, buffer)
	if stale.Manifest.TestRun == nil || stale.Manifest.TestRun.State != "failed" {
		t.Fatalf("invalid terminal rewrite replaced last accepted run: %+v", stale.Manifest)
	}

	nextStart := finished.Add(time.Second)
	nextRun := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: "fixture-run-2", State: "running", StartedAt: nextStart}
	nextBytes, err := json.Marshal(nextRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, nextBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	reset := waitForRunID(t, buffer, "fixture-run-2")
	if reset.Manifest.TestRunError != "" || len(reset.Tests.Tests) != 0 {
		t.Fatalf("new run did not clear the previous catalog: %+v", reset)
	}
	got := buffer.retainedRunCatalog()
	if len(got.Runs) != 2 || got.CurrentRunID != "fixture-run-2" || !hasRetainedRun(got.Runs, "fixture-run-1") {
		t.Fatalf("prior run not retained after transition: %+v", got)
	}
}

func TestTerminalManifestWaitsForGoJSONAlreadyWrittenBetweenPolls(t *testing.T) {
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "events.jsonl")
	manifestPath := filepath.Join(directory, "run.json")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	running := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: "fixture-run-1", State: "running", StartedAt: started}
	writeManifestFile(t, manifestPath, running)
	buffer := NewBuffer(16)
	tailer, err := NewTailer([]Source{{Name: "terminal race Go JSON", Path: eventsPath, Kind: "go-test"}, {Name: "terminal race manifest", Path: manifestPath, Kind: "manifest"}}, buffer, TailerOptions{PollInterval: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := tailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer stopTestTailer(t, cancel, tailer)
	raw, err := os.ReadFile(filepath.Join("testdata", "go-test-json-real-test-pass.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, eventsPath, raw)
	finished := started.Add(time.Minute)
	code := 0
	running.State, running.FinishedAt, running.ExitCode = "passed", &finished, &code
	writeManifestFile(t, manifestPath, running)
	result := waitForManifest(t, buffer, "passed")
	assertTestState(t, result.Tests, "tbound/supervisor/internal/webview", "TestGoTestOutputAndCatalogAreBounded", "passed")
	assertPackageState(t, result.Tests, "tbound/supervisor/internal/webview", "passed")
}

func TestGoJSONHistoryCheckpointResumesEventsWrittenWhileViewerWasDown(t *testing.T) {
	directory := t.TempDir()
	historyPath := filepath.Join(directory, "history.json")
	eventsPath := filepath.Join(directory, "events.jsonl")
	manifestPath := filepath.Join(directory, "run.json")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	manifest := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: "resume-run", State: "running", StartedAt: started, Packages: []string{"p"}}
	writeManifestFile(t, manifestPath, manifest)
	buffer, err := NewHistoryBuffer(historyPath, 8)
	if err != nil {
		t.Fatal(err)
	}
	sources := []Source{{Name: "resumable Go JSON", Path: eventsPath, Kind: "go-test"}, {Name: "resumable manifest", Path: manifestPath, Kind: "manifest"}}
	tailer, err := NewTailer(sources, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := tailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	appendLine(t, eventsPath, `{"Action":"start","Package":"p"}`)
	appendLine(t, eventsPath, `{"Action":"run","Package":"p","Test":"TestWhileDown"}`)
	waitForTestState(t, buffer, "p", "TestWhileDown", "running")
	firstSize, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	waitForCheckpoint(t, buffer, "resumable Go JSON", firstSize.Size())
	stopTestTailer(t, cancel, tailer)

	appendLine(t, eventsPath, `{"Action":"pass","Package":"p","Test":"TestWhileDown","Elapsed":0.01}`)
	appendLine(t, eventsPath, `{"Action":"pass","Package":"p","Elapsed":0.02}`)
	finished := started.Add(time.Minute)
	code := 0
	manifest.State, manifest.FinishedAt, manifest.ExitCode = "passed", &finished, &code
	writeManifestFile(t, manifestPath, manifest)

	restored, err := NewHistoryBuffer(historyPath, 8)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewTailer(sources, restored, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	if err := restarted.Start(restartCtx); err != nil {
		t.Fatal(err)
	}
	defer stopTestTailer(t, restartCancel, restarted)
	result := waitForManifest(t, restored, "passed")
	assertTestState(t, result.Tests, "p", "TestWhileDown", "passed")
	assertPackageState(t, result.Tests, "p", "passed")
	for _, source := range result.Manifest.Sources {
		if source.Name == "resumable Go JSON" && (source.Gap || source.Continuity != "resumed") {
			t.Fatalf("validated source restart reported a gap: %+v", source)
		}
	}
}

func TestGoJSONWithoutCheckpointExposesSourceGapAndUnresolvedManifestPackages(t *testing.T) {
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "events.jsonl")
	manifestPath := filepath.Join(directory, "run.json")
	if err := os.WriteFile(eventsPath, []byte(`{"Action":"pass","Package":"p"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	manifest := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: "gap-run", State: "running", StartedAt: started, Packages: []string{"p"}}
	writeManifestFile(t, manifestPath, manifest)
	buffer := NewBuffer(8)
	tailer, err := NewTailer([]Source{{Name: "gap Go JSON", Path: eventsPath, Kind: "go-test"}, {Name: "gap manifest", Path: manifestPath, Kind: "manifest"}}, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := tailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer stopTestTailer(t, cancel, tailer)
	snapshot := buffer.snapshot()
	assertPackageState(t, snapshot.Tests, "p", "unresolved")
	foundGap := false
	for _, source := range snapshot.Manifest.Sources {
		if source.Name == "gap Go JSON" && source.Gap && source.Continuity == "gap" && source.GapReason != "" {
			foundGap = true
		}
	}
	if !foundGap {
		t.Fatalf("missing explicit source gap: %+v", snapshot.Manifest.Sources)
	}
	if !strings.Contains(strings.Join(snapshot.Manifest.Warnings, ";"), "source gap") {
		t.Fatalf("source-gap warning missing: %+v", snapshot.Manifest.Warnings)
	}
}

func TestGoJSONCheckpointNeverPersistsInsideAnIncompleteExistingLine(t *testing.T) {
	directory := t.TempDir()
	historyPath := filepath.Join(directory, "history.json")
	eventsPath := filepath.Join(directory, "events.jsonl")
	manifestPath := filepath.Join(directory, "run.json")
	partial := []byte(`{"Action":"run","Package":"p","Test":"TestPartial"}`)
	if err := os.WriteFile(eventsPath, partial, 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	manifest := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: "partial-run", State: "running", StartedAt: started, Packages: []string{"p"}}
	writeManifestFile(t, manifestPath, manifest)
	buffer, err := NewHistoryBuffer(historyPath, 8)
	if err != nil {
		t.Fatal(err)
	}
	sources := []Source{{Name: "partial Go JSON", Path: eventsPath, Kind: "go-test"}, {Name: "partial manifest", Path: manifestPath, Kind: "manifest"}}
	tailer, err := NewTailer(sources, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := tailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer stopTestTailer(t, cancel, tailer)
	time.Sleep(40 * time.Millisecond)
	if checkpoint, ok := buffer.sourceCheckpoint("partial Go JSON"); ok {
		t.Fatalf("checkpoint persisted inside an incomplete pre-existing line: %+v", checkpoint)
	}
	appendBytes(t, eventsPath, []byte("\n"))
	info, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := waitForCheckpoint(t, buffer, "partial Go JSON", info.Size())
	if checkpoint.Offset != info.Size() {
		t.Fatalf("checkpoint offset=%d, want complete line boundary %d", checkpoint.Offset, info.Size())
	}
}

func TestMultipleFinishedRunsWhileViewerWasDownCreateRunBoundSourceGap(t *testing.T) {
	directory := t.TempDir()
	historyPath := filepath.Join(directory, "history.json")
	eventsPath := filepath.Join(directory, "events.jsonl")
	manifestPath := filepath.Join(directory, "run.json")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	runOne := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: "run-one", State: "running", StartedAt: started, Packages: []string{"p1"}}
	writeManifestFile(t, manifestPath, runOne)
	buffer, err := NewHistoryBuffer(historyPath, 16)
	if err != nil {
		t.Fatal(err)
	}
	sources := []Source{{Name: "multi-run Go JSON", Path: eventsPath, Kind: "go-test"}, {Name: "multi-run manifest", Path: manifestPath, Kind: "manifest"}}
	tailer, err := NewTailer(sources, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := tailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{`{"Action":"start","Package":"p1"}`, `{"Action":"run","Package":"p1","Test":"TestOne"}`, `{"Action":"pass","Package":"p1","Test":"TestOne"}`, `{"Action":"pass","Package":"p1"}`} {
		appendLine(t, eventsPath, line)
	}
	waitForTestState(t, buffer, "p1", "TestOne", "passed")
	finished := started.Add(time.Minute)
	code := 0
	runOne.State, runOne.FinishedAt, runOne.ExitCode = "passed", &finished, &code
	writeManifestFile(t, manifestPath, runOne)
	waitForManifest(t, buffer, "passed")
	size, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	waitForCheckpoint(t, buffer, "multi-run Go JSON", size.Size())
	stopTestTailer(t, cancel, tailer)
	// A second, finished run appends while the viewer is down. The durable
	// checkpoint belongs to run-one, so those bytes must not be assigned to run-two.
	for _, line := range []string{`{"Action":"start","Package":"p2"}`, `{"Action":"run","Package":"p2","Test":"TestTwo"}`, `{"Action":"pass","Package":"p2","Test":"TestTwo"}`, `{"Action":"pass","Package":"p2"}`} {
		appendLine(t, eventsPath, line)
	}
	runTwoStarted := started.Add(2 * time.Minute)
	runTwoFinished := runTwoStarted.Add(time.Minute)
	runTwo := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: "run-two", State: "passed", StartedAt: runTwoStarted, FinishedAt: &runTwoFinished, ExitCode: &code, Packages: []string{"p2"}}
	writeManifestFile(t, manifestPath, runTwo)
	restored, err := NewHistoryBuffer(historyPath, 16)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewTailer(sources, restored, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	if err := restarted.Start(restartCtx); err != nil {
		t.Fatal(err)
	}
	defer stopTestTailer(t, restartCancel, restarted)
	result := waitForManifest(t, restored, "passed")
	assertPackageState(t, result.Tests, "p2", "unresolved")
	if _, ok := restored.retainedRun("run-one"); !ok {
		t.Fatal("finished prior run was not retained")
	}
	foundGap := false
	for _, source := range result.Manifest.Sources {
		if source.Name == "multi-run Go JSON" && source.Gap {
			foundGap = true
		}
	}
	if !foundGap {
		t.Fatalf("run-bound checkpoint mismatch was not surfaced: %+v", result.Manifest.Sources)
	}
}

func TestRetainedRunCatalogSurvivesRingEvictionAndHistoryRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	buffer, err := NewHistoryBuffer(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	makeRun := func(id string, state string, at time.Time) TestRunManifest {
		run := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: id, State: state, StartedAt: at}
		if state != "running" {
			finished := at.Add(time.Minute)
			run.FinishedAt = &finished
			code := 0
			if state == "failed" {
				code = 1
			}
			run.ExitCode = &code
		}
		return run
	}
	if err := buffer.setRunManifest(makeRun("run-one", "running", started), "manifest", "epoch"); err != nil {
		t.Fatal(err)
	}
	for _, event := range []goTestEvent{{Action: "run", Package: "p", Test: "TestOne"}, {Action: "output", Package: "p", Test: "TestOne", Output: "sensitive output"}, {Action: "pass", Package: "p", Test: "TestOne"}, {Action: "pass", Package: "p"}} {
		if err := buffer.applyGoTestEvent(event, "json", "epoch"); err != nil {
			t.Fatal(err)
		}
	}
	if err := buffer.setRunManifest(makeRun("run-one", "passed", started), "manifest", "epoch"); err != nil {
		t.Fatal(err)
	}
	if err := buffer.setRunManifest(makeRun("run-two", "running", started.Add(2*time.Minute)), "manifest", "epoch"); err != nil {
		t.Fatal(err)
	}
	for _, event := range []goTestEvent{{Action: "run", Package: "p", Test: "TestTwo"}, {Action: "fail", Package: "p", Test: "TestTwo"}, {Action: "fail", Package: "p"}} {
		if err := buffer.applyGoTestEvent(event, "json", "epoch"); err != nil {
			t.Fatal(err)
		}
	}
	if err := buffer.setRunManifest(makeRun("run-two", "failed", started.Add(2*time.Minute)), "manifest", "epoch"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		buffer.publish(Record{Source: "filler", Types: []string{"record"}, Record: json.RawMessage(`{"n":1}`)})
	}
	if got := len(buffer.Snapshot()); got != 2 {
		t.Fatalf("observation ring size=%d; want2", got)
	}
	restored, err := NewHistoryBuffer(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	catalog := restored.retainedRunCatalog()
	if len(catalog.Runs) != 2 || catalog.CurrentRunID != "run-two" {
		t.Fatalf("retained runs after eviction/restart: %+v", catalog)
	}
	var old RetainedRun
	found := false
	for _, run := range catalog.Runs {
		if run.Run.RunID == "run-one" {
			old = run
			found = true
		}
	}
	if !found {
		t.Fatal("first run not retained")
	}
	assertTestState(t, old.Tests, "p", "TestOne", "passed")
	if old.Tests.Tests[0].Output != "" || !old.Tests.Tests[0].OutputTruncated || old.OutputPolicy != "omitted-from-retained-run-summaries" {
		t.Fatalf("historical output was not omitted from bounded summary: %+v", old)
	}
	server := httptest.NewServer(NewHandler(restored))
	defer server.Close()
	response, err := http.Get(server.URL + "/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var endpoint RetainedRunCatalog
	if err := json.NewDecoder(response.Body).Decode(&endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint.CurrentRunID != "run-two" || len(endpoint.Runs) != 2 {
		t.Fatalf("/runs catalog=%+v", endpoint)
	}
	response, err = http.Get(server.URL + "/runs/run-one")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var detail RetainedRun
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Run.RunID != "run-one" {
		t.Fatalf("/runs/{id} detail=%+v", detail)
	}
	if got := httptest.NewRecorder(); func() int {
		NewHandler(restored).ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/runs/not-registered", nil))
		return got.Code
	}() != http.StatusNotFound {
		t.Fatalf("unregistered run status=%d", got.Code)
	}
	bounded := NewBuffer(1)
	for index := 0; index < maxRetainedRuns+2; index++ {
		at := started.Add(time.Duration(index) * time.Minute)
		finished := at.Add(time.Second)
		code := 0
		run := TestRunManifest{SchemaVersion: "tbound-go-test-run/v1", RunID: fmt.Sprintf("cap-%02d", index), State: "passed", StartedAt: at, FinishedAt: &finished, ExitCode: &code}
		if err := bounded.setRunManifest(run, "manifest", "epoch"); err != nil {
			t.Fatal(err)
		}
	}
	boundedCatalog := bounded.retainedRunCatalog()
	if len(boundedCatalog.Runs) != maxRetainedRuns || boundedCatalog.Runs[len(boundedCatalog.Runs)-1].Run.RunID != "cap-02" {
		t.Fatalf("retained run cap not enforced: %+v", boundedCatalog)
	}
}

func TestGoJSONBuildFailureUnknownActionWarningsAndNoTestProvenance(t *testing.T) {
	buffer := NewBuffer(8)
	for _, event := range []goTestEvent{{Action: "build-output", Package: "broken", Output: "# compile error\n", OutputType: "frame"}, {Action: "build-fail", Package: "broken", FailedBuild: "example/broken"}} {
		if err := buffer.applyGoTestEvent(event, "go-json", "epoch"); err != nil {
			t.Fatal(err)
		}
	}
	if err := buffer.applyGoTestEvent(goTestEvent{Action: "pass", Package: "broken"}, "go-json", "epoch"); err != nil {
		t.Fatal(err)
	}
	assertPackageState(t, buffer.snapshot().Tests, "broken", "failed")
	unknown := `{"Action":"mystery","Package":"p","Test":"TestUnknown"}`
	tailer := &Tailer{buffer: buffer, options: TailerOptions{MaxLineBytes: DefaultMaxLineBytes}}
	tailer.publishLine(Source{Name: "go-json", Kind: "go-test"}, []byte(unknown), "epoch")
	assertTestState(t, buffer.snapshot().Tests, "p", "TestUnknown", "unresolved")
	if len(buffer.snapshot().Manifest.Warnings) == 0 {
		t.Fatal("unsupported Action did not surface a source warning")
	}
	for name, raw := range map[string][]byte{
		"duplicate Action": []byte(`{"Action":"pass","Action":"fail","Package":"p"}`),
		"invalid UTF-8":    append([]byte(`{"Action":"pass","Package":"`), append([]byte{0xff}, []byte(`"}`)...)...),
		"incorrect case":   []byte(`{"action":"pass","Package":"p"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGoTestEvent(raw); err == nil {
				t.Fatal("invalid Go JSON event accepted")
			}
		})
	}
	marker := NewBuffer(4)
	if err := marker.applyGoTestEvent(goTestEvent{Action: "output", Package: "empty", Output: "?\t empty\t[no test files]\n"}, "go-json", "epoch"); err != nil {
		t.Fatal(err)
	}
	assertPackageState(t, marker.snapshot().Tests, "empty", "no_test_files")
	if marker.snapshot().Tests.Packages[0].NoTestFilesProvenance != "go-test-output-marker" {
		t.Fatalf("no-test provenance missing: %+v", marker.snapshot().Tests.Packages[0])
	}
}

func TestBrowserUsesGoPackageFieldAndBoundsDedupeKeys(t *testing.T) {
	raw, err := page.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	if !strings.Contains(html, "package: pkg.package") || !strings.Contains(html, "seenFIFO.shift()") || !strings.Contains(html, "seen.delete(removed)") {
		t.Fatal("browser package rows or bounded feed deduplication regressed")
	}
	if strings.Contains(html, "package: pkg.name") {
		t.Fatal("browser still reads a nonexistent Go package property")
	}
}

func TestGoTestOutputAndCatalogAreBounded(t *testing.T) {
	buffer := NewBuffer(4)
	if err := buffer.applyGoTestEvent(goTestEvent{Action: "run", Package: "p", Test: "TestBound"}, "test", "epoch"); err != nil {
		t.Fatal(err)
	}
	if err := buffer.applyGoTestEvent(goTestEvent{Action: "output", Package: "p", Test: "TestBound", Output: strings.Repeat("x", maxTestOutputBytes*4)}, "test", "epoch"); err != nil {
		t.Fatal(err)
	}
	snapshot := buffer.snapshot()
	if len(snapshot.Tests.Tests) != 1 || len(snapshot.Tests.Tests[0].Output) > maxTestOutputBytes || !snapshot.Tests.Tests[0].OutputTruncated {
		t.Fatalf("test output bound not enforced: %+v", snapshot.Tests)
	}
	if _, err := decodeGoTestEvent([]byte(`{"Package":"p"}`)); err == nil {
		t.Fatal("event without Action accepted")
	}
	falseMarker := NewBuffer(4)
	for _, event := range []goTestEvent{{Action: "output", Package: "p", Output: "test log mentions [no test files]\n"}, {Action: "skip", Package: "p"}} {
		if err := falseMarker.applyGoTestEvent(event, "test", "epoch"); err != nil {
			t.Fatal(err)
		}
	}
	assertPackageState(t, falseMarker.snapshot().Tests, "p", "skipped")
}

func TestRealGoTestJSONNoTestFilesFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "go-test-json-no-test-files.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		event, err := decodeGoTestEvent([]byte(line))
		if err != nil {
			t.Fatalf("decode go test -json fixture line: %v", err)
		}
		if err := buffer.applyGoTestEvent(event, "real go test fixture", "fixture-epoch"); err != nil {
			t.Fatal(err)
		}
	}
	assertPackageState(t, buffer.snapshot().Tests, "tbound/supervisor/internal/broker/correlation", "no_test_files")
}

func TestRealGoTestJSONPassFixtureIncludingOutputType(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "go-test-json-real-test-pass.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "go-test.jsonl")
	manifestPath := filepath.Join(directory, "run.json")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runManifest, err := os.ReadFile(filepath.Join("testdata", "run-manifest-running.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, runManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer, err := NewTailer([]Source{
		{Name: "real go test stream", Path: eventsPath, Kind: "go-test"},
		{Name: "real go test manifest", Path: manifestPath, Kind: "manifest"},
	}, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := tailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer stopTestTailer(t, cancel, tailer)
	file, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	waitForCheckpoint(t, buffer, "real go test stream", int64(len(raw)))
	snapshot := buffer.snapshot()
	assertPackageState(t, snapshot.Tests, "tbound/supervisor/internal/webview", "passed")
	assertTestState(t, snapshot.Tests, "tbound/supervisor/internal/webview", "TestGoTestOutputAndCatalogAreBounded", "passed")
}

func TestAuditProjectionUsesVerifyAndRedactsEventData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	fixture := makeAuditFrame(t, 1, "", "observation", "fixture-event")
	if _, err := audit.Verify(strings.NewReader(string(fixture))); err != nil {
		t.Fatalf("fixture is not accepted by audit.Verify: %v", err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer := &Tailer{buffer: buffer, options: TailerOptions{AuditMaxBytes: maxAuditProjectionBytes}}
	state := &fileState{source: Source{Name: "audit fixture", Path: path, Kind: "audit"}}
	tailer.projectAudit(state)
	projection := buffer.snapshot().Audit
	if projection.Status != "verified-prefix" || projection.RecordCount != 1 || projection.HeadHash == "" {
		t.Fatalf("valid audit projection = %+v", projection)
	}
	if len(projection.Records) != 1 || projection.Records[0].ID != "fixture-event" {
		t.Fatalf("audit record summary = %+v", projection.Records)
	}
	for _, record := range buffer.Snapshot() {
		if strings.Contains(string(record.Record), "Data") {
			t.Fatalf("raw audit data leaked in viewer record: %s", record.Record)
		}
	}
	tailer.options.AuditMaxBytes = 1
	tailer.projectAudit(state)
	if got := buffer.snapshot().Audit.Status; got != "limit-exceeded" {
		t.Fatalf("audit projection bound status = %q", got)
	}
	tailer.options.AuditMaxBytes = maxAuditProjectionBytes

	tampered, err := os.ReadFile(filepath.Join("testdata", "audit-tampered.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	tailer.projectAudit(state)
	projection = buffer.snapshot().Audit
	if projection.Status != "invalid" || projection.Error == "" {
		t.Fatalf("tampered audit was not marked invalid: %+v", projection)
	}
}

func TestHistoryPersistsEpochCatalogAndReportsEviction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "viewer-history.json")
	buffer, err := NewHistoryBuffer(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	buffer.setSources([]SourceInfo{{Name: "persisted source", Kind: "transcript"}})
	buffer.setSourceEpoch("persisted source", "source-epoch-before-restart", "initial fixture")
	epoch := buffer.Epoch()
	buffer.publish(Record{Source: "test", Types: []string{"one"}, Record: json.RawMessage(`{"n":1}`)})
	buffer.publish(Record{Source: "test", Types: []string{"two"}, Record: json.RawMessage(`{"n":2}`)})
	buffer.publish(Record{Source: "test", Types: []string{"three"}, Record: json.RawMessage(`{"n":3}`)})
	restored, err := NewHistoryBuffer(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Epoch() != epoch {
		t.Fatalf("history epoch = %q; want %q", restored.Epoch(), epoch)
	}
	records := restored.Snapshot()
	if len(records) != 2 || records[0].ID != 2 || records[1].ID != 3 {
		t.Fatalf("restored records = %+v", records)
	}
	subscription, gap := restored.SubscribeAfter(formatCursor(epoch, 1))
	subscription.Close()
	if gap != nil {
		t.Fatalf("cursor at last evicted record should have complete continuation: %+v", gap)
	}
	subscription, gap = restored.SubscribeAfter(formatCursor(epoch, 0))
	subscription.Close()
	if gap == nil || gap.Reason != "history_evicted" {
		t.Fatalf("old cursor gap = %+v", gap)
	}
	subscription, gap = restored.SubscribeAfter(formatCursor("another-epoch", 3))
	subscription.Close()
	if gap == nil || gap.Reason != "source_epoch_changed" {
		t.Fatalf("restart cursor gap = %+v", gap)
	}

	sourcePath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(sourcePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	restartedTailer, err := NewTailer([]Source{{Name: "persisted source", Path: sourcePath, Kind: "transcript"}}, restored, TailerOptions{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := restartedTailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var epochChange bool
	for _, record := range restored.Snapshot() {
		if strings.Contains(strings.Join(record.Types, ","), "source_epoch") {
			epochChange = true
		}
	}
	if !epochChange {
		cancel()
		<-restartedTailer.Done()
		t.Fatal("restart did not emit a source-epoch transition")
	}
	stopTestTailer(t, cancel, restartedTailer)
}

func TestHistoryTamperingFailsClosedAndStorageBoundIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHistoryBuffer(path, 4); err == nil {
		t.Fatal("corrupted history accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	buffer, err := NewHistoryBuffer(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	buffer.publish(Record{Source: "large", Types: []string{"record"}, Record: json.RawMessage(`"` + strings.Repeat("x", defaultHistoryBytes) + `"`)})
	snapshot := buffer.snapshot()
	if snapshot.Manifest.History.Healthy || !strings.Contains(snapshot.Manifest.History.Error, "exceeds") {
		t.Fatalf("history bound was not reported: %+v", snapshot.Manifest.History)
	}
}

func TestHistoryLoaderRejectsDuplicateKeysAndInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate-history.json")
	buffer, err := NewHistoryBuffer(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	buffer.publish(Record{Source: "fixture", Types: []string{"record"}, Record: json.RawMessage(`{"n":1}`)})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	key := `"schema_version":"` + historySchema + `"`
	duplicate := strings.Replace(string(raw), key, `"schema_version":"other",`+key, 1)
	if err := os.WriteFile(path, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHistoryBuffer(path, 4); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate history key error=%v", err)
	}
	if err := os.WriteFile(path, []byte{0xff, '{', '}'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHistoryBuffer(path, 4); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 history error=%v", err)
	}
}

func TestSSECursorReplayGapAndGETOnlySurface(t *testing.T) {
	buffer := NewBuffer(2)
	buffer.publish(Record{Source: "test", Types: []string{"record"}, Record: json.RawMessage(`{"n":1}`)})
	buffer.publish(Record{Source: "test", Types: []string{"record"}, Record: json.RawMessage(`{"n":2}`)})
	buffer.publish(Record{Source: "test", Types: []string{"record"}, Record: json.RawMessage(`{"n":3}`)})
	server := httptest.NewServer(NewHandler(buffer))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events?cursor="+formatCursor(buffer.Epoch(), 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	eventName, gapData := readSSEEvent(t, reader)
	if eventName != "gap" {
		t.Fatalf("first SSE event = %q; want gap", eventName)
	}
	var gap ReplayGap
	if err := json.Unmarshal([]byte(gapData), &gap); err != nil {
		t.Fatal(err)
	}
	if gap.Reason != "history_evicted" || gap.Earliest != 2 {
		t.Fatalf("gap = %+v", gap)
	}
	eventName, data := readSSEEvent(t, reader)
	if eventName != "message" {
		t.Fatalf("next SSE event = %q", eventName)
	}
	var replay Record
	if err := json.Unmarshal([]byte(data), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ID != 2 {
		t.Fatalf("first retained replay ID = %d; want 2", replay.ID)
	}
	cancel()
	_ = response.Body.Close()

	buffer.publish(Record{Source: "test", Types: []string{"record"}, Record: json.RawMessage(`{"n":4}`)})
	ctx, cancel = context.WithCancel(context.Background())
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", formatCursor(buffer.Epoch(), 3))
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, data = readSSEEvent(t, bufio.NewReader(response.Body))
	if err := json.Unmarshal([]byte(data), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ID != 4 {
		t.Fatalf("Last-Event-ID reconnect replay ID = %d; want 4", replay.ID)
	}
	cancel()
	_ = response.Body.Close()

	for _, path := range []string{"/", "/records", "/snapshot", "/manifest", "/tests", "/runs", "/runs/registered", "/events", "/arbitrary-file"} {
		response := httptest.NewRecorder()
		NewHandler(buffer).ServeHTTP(response, httptest.NewRequest(http.MethodPost, path+"?path=C%3A%5Csecret", strings.NewReader("mutate")))
		if path == "/arbitrary-file" {
			if response.Code != http.StatusNotFound {
				t.Fatalf("POST %s status = %d", path, response.Code)
			}
			continue
		}
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d; want 405", path, response.Code)
		}
	}
	page := httptest.NewRecorder()
	NewHandler(buffer).ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(page.Body.String(), "innerHTML") {
		t.Fatal("UI contains an HTML injection sink")
	}
}

func TestSSECursorReconnectAcrossHistoryAndUnretainedRestart(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "observations.json")
	first, err := NewHistoryBuffer(historyPath, 8)
	if err != nil {
		t.Fatal(err)
	}
	first.publish(Record{Source: "test", Types: []string{"record"}, Record: json.RawMessage(`{"n":1}`)})
	oldCursor := first.cursor()
	firstEpoch := first.Epoch()

	restarted, err := NewHistoryBuffer(historyPath, 8)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Epoch() != firstEpoch {
		t.Fatal("retained history did not preserve observation epoch")
	}
	restarted.publish(Record{Source: "test", Types: []string{"record"}, Record: json.RawMessage(`{"n":2}`)})
	server := httptest.NewServer(NewHandler(restarted))
	response, err := http.Get(server.URL + "/events?cursor=" + oldCursor)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	name, data := readSSEEvent(t, bufio.NewReader(response.Body))
	if name != "message" {
		t.Fatalf("retained restart emitted %q; want replayed record", name)
	}
	var record Record
	if err := json.Unmarshal([]byte(data), &record); err != nil {
		t.Fatal(err)
	}
	if record.ID != 2 {
		t.Fatalf("retained restart cursor resumed at ID %d; want 2", record.ID)
	}
	_ = response.Body.Close()
	server.Close()

	unretained := NewBuffer(8)
	unretained.publish(Record{Source: "test", Types: []string{"record"}, Record: json.RawMessage(`{"new_epoch":true}`)})
	server = httptest.NewServer(NewHandler(unretained))
	defer server.Close()
	response, err = http.Get(server.URL + "/events?cursor=" + oldCursor)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	name, data = readSSEEvent(t, reader)
	if name != "gap" {
		t.Fatalf("unretained restart first event = %q; want explicit gap", name)
	}
	var gap ReplayGap
	if err := json.Unmarshal([]byte(data), &gap); err != nil {
		t.Fatal(err)
	}
	if gap.Reason != "source_epoch_changed" {
		t.Fatalf("unretained restart gap = %+v", gap)
	}
	name, data = readSSEEvent(t, reader)
	if name != "message" || !strings.Contains(data, "new_epoch") {
		t.Fatalf("unretained restart replay = %q %s", name, data)
	}
	_ = response.Body.Close()
}

func TestSubscriberAndCursorBounds(t *testing.T) {
	buffer := NewBuffer(maxBufferCapacity + 1)
	if buffer.capacity != maxBufferCapacity {
		t.Fatalf("buffer capacity = %d; want max %d", buffer.capacity, maxBufferCapacity)
	}
	subscriptions := make([]*Subscription, 0, maxSubscribers)
	for index := 0; index < maxSubscribers; index++ {
		subscription, gap := buffer.SubscribeAfter("")
		if gap != nil {
			t.Fatalf("subscriber %d rejected: %+v", index, gap)
		}
		subscriptions = append(subscriptions, subscription)
	}
	limited, gap := buffer.SubscribeAfter(strings.Repeat("x", 4096))
	defer limited.Close()
	if gap == nil || gap.Reason != "subscriber_limit" || len(gap.RequestedCursor) > 256 {
		t.Fatalf("subscriber limit/gap not bounded: %+v", gap)
	}
	for _, subscription := range subscriptions {
		subscription.Close()
	}
	invalid, gap := buffer.SubscribeAfter(strings.Repeat("x", 4096))
	invalid.Close()
	if gap == nil || gap.Reason != "invalid_cursor" || len(gap.RequestedCursor) > 256 {
		t.Fatalf("oversized cursor error not bounded: %+v", gap)
	}
}

func TestConfiguredSourceCountAndIdentityBounds(t *testing.T) {
	buffer := NewBuffer(4)
	tooMany := make([]Source, maxConfiguredSources+1)
	if _, err := NewTailer(tooMany, buffer, TailerOptions{}); err == nil || !strings.Contains(err.Error(), "source count") {
		t.Fatalf("oversized source list error = %v", err)
	}
	if _, err := NewTailer([]Source{{Name: "duplicate", Path: "one", Kind: "transcript"}, {Name: "duplicate", Path: "two", Kind: "transcript"}}, buffer, TailerOptions{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate source identity error = %v", err)
	}
	threeJournals := []Source{{Name: "audit 1", Path: "one", Kind: "audit"}, {Name: "audit 2", Path: "two", Kind: "audit"}, {Name: "audit 3", Path: "three", Kind: "audit"}}
	if _, err := NewTailer(threeJournals, buffer, TailerOptions{}); err == nil || !strings.Contains(err.Error(), "at most two audit") {
		t.Fatalf("audit source cap error = %v", err)
	}
}

func TestEvidenceBundleProjectionTamperIsNotAccepted(t *testing.T) {
	_, err := reconstructGenerationProjection([]byte(`{"schema_version":"tbound-sessionrepo-evidence-bundle/v1","audit_journal":"tampered"}`))
	if err == nil {
		t.Fatal("incomplete/tampered evidence bundle accepted")
	}
}

func TestVersionedSnapshotAndManifestDoNotExposeConfiguredPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-source.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer, err := NewTailer([]Source{{Path: path, Kind: "transcript"}}, buffer, TailerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := buffer.snapshot()
	if snapshot.SchemaVersion != "tbound-observation-snapshot/v1" || snapshot.Manifest.SchemaVersion != "tbound-observation-manifest/v1" {
		t.Fatalf("versioned contract missing: %+v", snapshot)
	}
	if strings.Contains(snapshot.Manifest.Sources[0].Name, path) {
		t.Fatal("implicit source label exposed a configured filesystem path")
	}
	response := httptest.NewRecorder()
	NewHandler(buffer).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/manifest", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), path) {
		t.Fatalf("manifest exposed a source path or returned error: %d %s", response.Code, response.Body.String())
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	if err := tailer.Start(cancelCtx); err != nil {
		t.Fatal(err)
	}
	stopTestTailer(t, cancel, tailer)
}

func makeAuditFrame(t *testing.T, sequence uint64, previousHash, kind, id string) []byte {
	t.Helper()
	type event struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Outcome string `json:"outcome,omitempty"`
		Data    []byte `json:"data,omitempty"`
	}
	type unsignedRecord struct {
		Version      int    `json:"version"`
		Sequence     uint64 `json:"sequence"`
		PreviousHash string `json:"previous_hash"`
		Event        event  `json:"event"`
	}
	type record struct {
		Version      int    `json:"version"`
		Sequence     uint64 `json:"sequence"`
		PreviousHash string `json:"previous_hash"`
		Event        event  `json:"event"`
		Hash         string `json:"hash"`
	}
	unsigned := unsignedRecord{Version: 1, Sequence: sequence, PreviousHash: previousHash, Event: event{Kind: kind, ID: id}}
	encodedUnsigned, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encodedUnsigned)
	encoded, err := json.Marshal(record{Version: 1, Sequence: sequence, PreviousHash: previousHash, Event: unsigned.Event, Hash: hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func readSSEEvent(t *testing.T, reader *bufio.Reader) (string, string) {
	t.Helper()
	eventName := "message"
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.HasPrefix(line, "event: ") {
			eventName = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(line, "data: "))
		}
		if line == "" && data.Len() != 0 {
			return eventName, data.String()
		}
	}
}

func waitForTestCatalog(t *testing.T, buffer *Buffer, testCount, packageCount int) ExplorerSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := buffer.snapshot()
		if len(snapshot.Tests.Tests) >= testCount && len(snapshot.Tests.Packages) >= packageCount {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tests/packages: %+v", buffer.snapshot().Tests)
	return ExplorerSnapshot{}
}

func waitForManifest(t *testing.T, buffer *Buffer, state string) ExplorerSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := buffer.snapshot()
		if snapshot.Manifest.TestRun != nil && snapshot.Manifest.TestRun.State == state {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for run manifest %q: %+v", state, buffer.snapshot().Manifest)
	return ExplorerSnapshot{}
}

func waitForManifestError(t *testing.T, buffer *Buffer) ExplorerSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := buffer.snapshot()
		if snapshot.Manifest.TestRunError != "" {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for manifest transition error: %+v", buffer.snapshot().Manifest)
	return ExplorerSnapshot{}
}

func waitForRunID(t *testing.T, buffer *Buffer, runID string) ExplorerSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := buffer.snapshot()
		if snapshot.Manifest.TestRun != nil && snapshot.Manifest.TestRun.RunID == runID {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for run ID %q: %+v", runID, buffer.snapshot().Manifest)
	return ExplorerSnapshot{}
}

func writeManifestFile(t *testing.T, path string, manifest TestRunManifest) {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendBytes(t *testing.T, path string, value []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(value); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitForTestState(t *testing.T, buffer *Buffer, packageName, testName, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, item := range buffer.snapshot().Tests.Tests {
			if item.Package == packageName && item.Name == testName && item.State == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for test %s/%s state %q: %+v", packageName, testName, want, buffer.snapshot().Tests)
}

func waitForCheckpoint(t *testing.T, buffer *Buffer, name string, offset int64) SourceCheckpoint {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if checkpoint, ok := buffer.sourceCheckpoint(name); ok && checkpoint.Offset >= offset {
			return checkpoint
		}
		time.Sleep(5 * time.Millisecond)
	}
	checkpoint, _ := buffer.sourceCheckpoint(name)
	t.Fatalf("timed out waiting for source checkpoint at %d: %+v", offset, checkpoint)
	return SourceCheckpoint{}
}

func assertTestState(t *testing.T, catalog TestCatalog, packageName, testName, want string) {
	t.Helper()
	for _, item := range catalog.Tests {
		if item.Package == packageName && item.Name == testName {
			if item.State != want {
				t.Fatalf("test %s/%s state = %q; want %q", packageName, testName, item.State, want)
			}
			return
		}
	}
	t.Fatalf("test %s/%s not found", packageName, testName)
}

func assertPackageState(t *testing.T, catalog TestCatalog, name, want string) {
	t.Helper()
	for _, item := range catalog.Packages {
		if item.Name == name {
			if item.State != want {
				t.Fatalf("package %s state = %q; want %q", name, item.State, want)
			}
			return
		}
	}
	t.Fatalf("package %s not found", name)
}
