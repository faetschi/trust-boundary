//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"tbound/supervisor/internal/sessionrepo"
)

func TestCanonicalServeBashOutputPaths(t *testing.T) {
	if paths, err := canonicalServeBashOutputPaths(nil, false); err != nil || paths != nil {
		t.Fatalf("absent scope = %#v, %v; want nil", paths, err)
	}
	empty, err := canonicalServeBashOutputPaths(nil, true)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("no-output scope = %#v, %v", empty, err)
	}
	raw := []string{"z.txt", "a.txt"}
	paths, err := canonicalServeBashOutputPaths(raw, false)
	if err != nil || !reflect.DeepEqual(paths, []string{"a.txt", "z.txt"}) || raw[0] != "z.txt" {
		t.Fatalf("canonicalization altered its input or returned invalid scope: %q, %v", paths, err)
	}
	oversized := make([]string, 129)
	for i := range oversized {
		oversized[i] = fmt.Sprintf("path-%03d", i)
	}
	for _, invalid := range [][]string{{"task.txt", "task.txt"}, {"../task.txt"}, {""}, oversized} {
		if _, err := canonicalServeBashOutputPaths(invalid, false); !errors.Is(err, sessionrepo.ErrInvalidOutputScope) {
			t.Fatalf("invalid scope returned %v", err)
		}
	}
	if _, err := canonicalServeBashOutputPaths([]string{"task.txt"}, true); err == nil {
		t.Fatal("conflicting scope flags accepted")
	}
}

func TestServeBashOutputFlagsFailClosedBeforeServiceUse(t *testing.T) {
	if err := runServe([]string{"--pi", "--bash-output", "../escape"}, io.Discard, io.Discard); !errors.Is(err, sessionrepo.ErrInvalidOutputScope) {
		t.Fatalf("invalid scope returned %v", err)
	}
	for _, args := range [][]string{
		{"--pi", "--service", "--bash-no-outputs"},
		{"--pi", "--dev-fixture", "--bash-no-outputs"},
		{"--pi", "--bash-output", "task.txt", "--bash-no-outputs"},
	} {
		if err := runServe(args, io.Discard, io.Discard); err == nil {
			t.Fatalf("invalid scope mode accepted: %q", args)
		}
	}
}

func TestPiServiceBashScopePreservesLegacyAndEmptyPayloads(t *testing.T) {
	request := piServiceRequest{RequestID: "request-1", TaskID: "task-1", Prompt: "inspect"}
	legacy := `{"request_id":"request-1","task_id":"task-1","prompt":"inspect"}`
	encoded, err := json.Marshal(request)
	if err != nil || string(encoded) != legacy {
		t.Fatalf("nil scope changed legacy JSON: %s, %v", encoded, err)
	}
	legacyDigest, err := piServicePayloadDigest(request)
	if err != nil || legacyDigest != digestBytes([]byte(legacy)) {
		t.Fatalf("nil scope changed legacy digest: %q, %v", legacyDigest, err)
	}
	if err := validatePiServiceRequest(request); err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	request.BashOutputPaths = &empty
	if err := validatePiServiceRequest(request); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(request)
	if err != nil || string(encoded) != legacy[:len(legacy)-1]+`,"bash_output_paths":[]}` {
		t.Fatalf("empty scope JSON: %s, %v", encoded, err)
	}
	emptyDigest, err := piServicePayloadDigest(request)
	if err != nil || emptyDigest == legacyDigest {
		t.Fatalf("empty scope not digest-bound: %q, %v", emptyDigest, err)
	}
	var decoded piServiceRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.BashOutputPaths == nil || len(*decoded.BashOutputPaths) != 0 {
		t.Fatalf("empty scope did not round-trip: %#v, %v", decoded.BashOutputPaths, err)
	}
	var explicitNull piServiceRequest
	if err := json.Unmarshal([]byte(legacy[:len(legacy)-1]+`,"bash_output_paths":null}`), &explicitNull); err != nil || explicitNull.BashOutputPaths != nil {
		t.Fatalf("null scope must disable Bash: %#v, %v", explicitNull.BashOutputPaths, err)
	}
}

func TestPiServiceAndProductionConfigRejectForgedBashScopes(t *testing.T) {
	for _, paths := range [][]string{{"z.txt", "a.txt"}, {"task.txt", "task.txt"}, {"../task.txt"}} {
		request := piServiceRequest{RequestID: "request-1", TaskID: "task-1", Prompt: "inspect", BashOutputPaths: &paths}
		if err := validatePiServiceRequest(request); !errors.Is(err, ErrPiServiceRefused) || !errors.Is(err, sessionrepo.ErrInvalidOutputScope) {
			t.Fatalf("forged request returned %v", err)
		}
		if err := (productionServeConfig{BashOutputPaths: paths}).validate(); !errors.Is(err, sessionrepo.ErrInvalidOutputScope) {
			t.Fatalf("forged configuration returned %v", err)
		}
	}
	if err := (productionServeConfig{}).validate(); err != nil {
		t.Fatal(err)
	}
	if err := (productionServeConfig{BashOutputPaths: []string{}}).validate(); err != nil {
		t.Fatal(err)
	}
}
