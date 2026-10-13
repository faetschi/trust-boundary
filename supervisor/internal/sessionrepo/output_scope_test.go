//go:build linux

package sessionrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/delta"
)

func TestRunBashRejectsMissingOutputScopeBeforeViewOrRunner(t *testing.T) {
	store, _, _, _ := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "baseline\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalled := false
	_, err = store.RunBash(context.Background(), g0, lease("missing-output-scope"), allow("missing-output-scope"),
		CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "true"}}, CommandRunnerFunc(
			func(context.Context, *CommandView, CommandSpec) (CommandResult, CommandSettlement, error) {
				runnerCalled = true
				return CommandResult{}, CommandSettlement{}, nil
			}))
	if !errors.Is(err, ErrOutputScopeRequired) || runnerCalled || len(store.views) != 0 || len(store.generations) != 1 {
		t.Fatalf("missing scope refusal: err=%v runner=%t views=%d generations=%d", err, runnerCalled, len(store.views), len(store.generations))
	}
	trace, err := store.options.Journal.Trace()
	if err != nil || len(trace.Effects) != 1 {
		t.Fatalf("missing scope wrote an operation effect: effects=%d err=%v", len(trace.Effects), err)
	}
}

func TestOperationIntentV2RequiresCanonicalJSON(t *testing.T) {
	read := readIntent{SchemaVersion: OperationIntentSchemaVersion,
		Request: OperationRequest{Tool: "read"}, Path: "task.txt", MaxBytes: 64}
	bash := readIntent{SchemaVersion: OperationIntentSchemaVersion,
		Request: OperationRequest{Tool: "bash", OutputScopePaths: []string{"task.txt"}},
		Command: CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "true"},
			DeclaredOutputPaths: []string{"task.txt"}}, RunnerProfile: "test"}
	for _, tc := range []struct {
		name   string
		intent readIntent
	}{{"read", read}, {"bash", bash}} {
		raw, err := json.Marshal(tc.intent)
		if err != nil {
			t.Fatal(err)
		}
		_, decoded, err := decodeOperationIntent(raw)
		if err != nil {
			t.Fatalf("%s intent rejected: %v", tc.name, err)
		}
		roundTrip, err := json.Marshal(decoded)
		if err != nil || !bytes.Equal(roundTrip, raw) {
			t.Fatalf("%s intent did not round-trip canonically: %v", tc.name, err)
		}
	}
	bashRaw, err := json.Marshal(bash)
	if err != nil {
		t.Fatal(err)
	}
	duplicateTool := bytes.Replace(bashRaw, []byte(`"tool":"bash"`), []byte(`"tool":"bash","tool":"bash"`), 1)
	aliasTool := bytes.Replace(bashRaw, []byte(`"tool":"bash"`), []byte(`"tool":"bash","Tool":"bash"`), 1)
	missingScope := bytes.Replace(bashRaw, []byte(`,"output_scope_paths":["task.txt"]`), nil, 1)
	legacy := bytes.Replace(bashRaw, []byte(`"schema_version":"`+OperationIntentSchemaVersion+`",`), nil, 1)
	legacy = bytes.Replace(legacy, []byte(`,"path":"","max_bytes":0`), nil, 1)
	legacy = bytes.Replace(legacy, []byte(`,"output_scope_paths":["task.txt"]`), nil, 1)
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"duplicate nested key", duplicateTool}, {"case alias", aliasTool},
		{"missing scope", missingScope}, {"legacy unversioned Bash", legacy},
	} {
		if bytes.Equal(tc.raw, bashRaw) {
			t.Fatalf("%s fixture did not change the canonical input", tc.name)
		}
		if _, _, err := decodeOperationIntent(tc.raw); err == nil {
			t.Errorf("%s intent was accepted", tc.name)
		}
	}
}

func TestOutputScopeIsCanonicalExactAndDigestBound(t *testing.T) {
	if !errors.Is(ValidateOutputScopePaths(nil), ErrOutputScopeRequired) {
		t.Fatal("nil scope did not report that output scope is required")
	}
	if err := ValidateOutputScopePaths([]string{}); err != nil {
		t.Fatalf("explicit empty scope rejected: %v", err)
	}
	for _, invalid := range [][]string{{"z.txt", "a.txt"}, {"task.txt", "task.txt"}, {"../task.txt"}} {
		if err := ValidateOutputScopePaths(invalid); !errors.Is(err, ErrInvalidOutputScope) {
			t.Fatalf("non-canonical scope %q returned %v", invalid, err)
		}
	}
	change := []delta.ObjectChange{{Path: "build/result.txt"}}
	if !OutputScopeCoversChanges([]string{"build/result.txt"}, change) ||
		OutputScopeCoversChanges([]string{"build"}, change) || OutputScopeCoversChanges([]string{}, change) ||
		!OutputScopeCoversChanges([]string{}, nil) {
		t.Fatal("scope coverage did not use exact declared paths")
	}
	emptySpec := CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "true"}, DeclaredOutputPaths: []string{}}
	pathSpec := emptySpec
	pathSpec.DeclaredOutputPaths = []string{"task.txt"}
	emptyDigest, err := CommandArgumentsDigest(emptySpec)
	if err != nil {
		t.Fatal(err)
	}
	pathDigest, err := CommandArgumentsDigest(pathSpec)
	if err != nil {
		t.Fatal(err)
	}
	if emptyDigest == pathDigest {
		t.Fatal("command argument digest did not bind output scope")
	}
	emptyJSON, err := json.Marshal(emptySpec)
	if err != nil {
		t.Fatal(err)
	}
	pathJSON, err := json.Marshal(pathSpec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(emptyJSON, pathJSON) {
		t.Fatal("output scope changed the execution-only D09 command JSON")
	}
}
