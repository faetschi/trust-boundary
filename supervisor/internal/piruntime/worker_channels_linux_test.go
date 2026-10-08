//go:build linux

package piruntime

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiWorkerChannelsDescriptorBootstrapAndControlFrames(t *testing.T) {
	cwd := makePrivateTestDirectory(t, "worker-cwd")
	bundle := makePrivateTestDirectory(t, "worker-bundle")
	cwdHandle, err := os.Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer cwdHandle.Close()
	bundleHandle, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer bundleHandle.Close()
	channels, err := newPiWorkerChannels(cwdHandle, bundleHandle)
	if err != nil {
		t.Fatal(err)
	}
	defer channels.Close()

	bindings := channels.DescriptorBindings()
	if err := validatePiWorkerDescriptors(PiWorkerDescriptorProfile, bindings, cwdHandle, bundleHandle); err != nil {
		t.Fatalf("exact descriptor map rejected: %v", err)
	}
	if got := descriptorRoles(bindings); got != "4=worker-exposure;5=bootstrap;6=proposal-ipc;7=provider-bridge;8=worker-events;9=runtime-bundle" {
		t.Fatalf("descriptor roles = %q", got)
	}

	profile, _ := testHostProfile()
	bootstrap := PiWorkerBootstrap{
		SourceBindingDigest:    DigestBytes([]byte("source binding")),
		SourceProvenanceDigest: DigestBytes([]byte("source provenance")),
		SourceGenerationID:     "generation-1",
		SourceTreeDigest:       DigestBytes([]byte("source tree")),
		SourceManifestDigest:   DigestBytes([]byte("source manifest")),
		WorkerViewIdentity:     DigestBytes([]byte("worker view")),
		WorkerMountID:          1,
		SessionID:              "session-1",
		WorkflowID:             "workflow-1",
		ConversationID:         "conversation-1",
	}
	if err := channels.writeBootstrap(profile, bootstrap); err != nil {
		t.Fatalf("write bounded bootstrap: %v", err)
	}
	bootstrapBody := readPiTestFrame(t, channels.childBootstrap)
	var decoded piWorkerBootstrapWire
	if err := json.Unmarshal(bootstrapBody, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != PiWorkerSchemaVersion || decoded.ProfileID != profile.ID ||
		decoded.SourceBindingDigest != bootstrap.SourceBindingDigest ||
		decoded.SourceProvenanceDigest != bootstrap.SourceProvenanceDigest ||
		decoded.SourceGenerationID != bootstrap.SourceGenerationID || decoded.SourceTreeDigest != bootstrap.SourceTreeDigest ||
		decoded.SourceManifestDigest != bootstrap.SourceManifestDigest || decoded.WorkerViewIdentity != bootstrap.WorkerViewIdentity ||
		decoded.WorkerMountID != bootstrap.WorkerMountID ||
		decoded.IPCBindingToken != channels.BindingToken() || decoded.ProviderID != PiWorkerProviderID ||
		decoded.DescriptorProfile != PiWorkerDescriptorProfile || decoded.RuntimeBundleDigest != profile.PiRuntimeBundleDigest {
		t.Fatalf("worker bootstrap fields differ from host/channel binding: %+v", decoded)
	}

	controlContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := channels.sendControl(controlContext, PiWorkerControl{Type: "prompt", RequestID: "request-1", TaskID: "task-1", Text: "admitted fixture prompt"}); err != nil {
		t.Fatalf("send host control frame: %v", err)
	}
	controlBody := readPiTestFrame(t, channels.ChildStdin())
	var control PiWorkerControl
	if err := json.Unmarshal(controlBody, &control); err != nil {
		t.Fatal(err)
	}
	if control.Type != "prompt" || control.Sequence != 1 || control.RequestID != "request-1" || control.TaskID != "task-1" || control.Text != "admitted fixture prompt" {
		t.Fatalf("control frame differs from the admitted command: %+v", control)
	}
	if err := channels.Abort(controlContext, "request-1"); err != nil {
		t.Fatalf("send abort control: %v", err)
	}
	abortBody := readPiTestFrame(t, channels.ChildStdin())
	if err := json.Unmarshal(abortBody, &control); err != nil {
		t.Fatal(err)
	}
	if control.Type != "abort" || control.Sequence != 2 || control.RequestID != "request-1" {
		t.Fatalf("abort sequence/correlation differs: %+v", control)
	}

	eventBody, err := json.Marshal(PiWorkerEvent{Type: "turn_end", Sequence: 1, RequestID: "request-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writePiFrame(channels.childEvents, eventBody); err != nil {
		t.Fatalf("write child worker event: %v", err)
	}
	event, err := channels.NextEvent(controlContext)
	if err != nil || event.Type != "turn_end" || event.Sequence != 1 || event.RequestID != "request-1" {
		t.Fatalf("read structured child event: event=%+v err=%v", event, err)
	}
}

func TestPiWorkerDescriptorPlanRejectsRoleSwapAndKernelObjectAlias(t *testing.T) {
	cwd := makePrivateTestDirectory(t, "worker-cwd")
	bundle := makePrivateTestDirectory(t, "worker-bundle")
	cwdHandle, err := os.Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer cwdHandle.Close()
	bundleHandle, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer bundleHandle.Close()
	channels, err := newPiWorkerChannels(cwdHandle, bundleHandle)
	if err != nil {
		t.Fatal(err)
	}
	defer channels.Close()

	valid := channels.DescriptorBindings()
	swapped := cloneInheritedDescriptors(valid)
	swapped[0].Role, swapped[1].Role = swapped[1].Role, swapped[0].Role
	if err := validatePiWorkerDescriptors(PiWorkerDescriptorProfile, swapped, cwdHandle, bundleHandle); err == nil {
		t.Fatal("swapped descriptor roles were accepted")
	}
	aliased := cloneInheritedDescriptors(valid)
	aliased[1].File = valid[2].File
	if err := validatePiWorkerDescriptors(PiWorkerDescriptorProfile, aliased, cwdHandle, bundleHandle); err == nil {
		t.Fatal("duplicated descriptor handle was accepted")
	}
	first, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	aliased = cloneInheritedDescriptors(valid)
	aliased[1].File, aliased[2].File = first, second
	if err := validatePiWorkerDescriptors(PiWorkerDescriptorProfile, aliased, cwdHandle, bundleHandle); err == nil {
		t.Fatal("distinct os.File wrappers for the same kernel object were accepted")
	}
}

func TestPiWorkerEventReaderFailsClosedOnSequenceGapAndUnknownField(t *testing.T) {
	for name, body := range map[string][]byte{
		"sequence gap":  []byte(`{"type":"turn_end","sequence":2,"request_id":"request-1"}`),
		"unknown field": []byte(`{"type":"stopped","sequence":1,"secret":"sentinel"}`),
	} {
		t.Run(name, func(t *testing.T) {
			cwdHandle, err := os.Open(makePrivateTestDirectory(t, "worker-cwd"))
			if err != nil {
				t.Fatal(err)
			}
			defer cwdHandle.Close()
			bundleHandle, err := os.Open(makePrivateTestDirectory(t, "worker-bundle"))
			if err != nil {
				t.Fatal(err)
			}
			defer bundleHandle.Close()
			channels, err := newPiWorkerChannels(cwdHandle, bundleHandle)
			if err != nil {
				t.Fatal(err)
			}
			defer channels.Close()
			if err := writePiFrame(channels.childEvents, body); err != nil {
				t.Fatal(err)
			}
			if err := channels.childEvents.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := channels.NextEvent(ctx); err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("invalid worker event stream did not fail closed: %v", err)
			}
		})
	}
}

func TestPiWorkerReadyAcceptsAndBindsActualTypeScriptEventShape(t *testing.T) {
	profile, _ := testHostProfile()
	bootstrap := PiWorkerBootstrap{
		SourceBindingDigest: DigestBytes([]byte("dev binding")), SourceProvenanceDigest: DigestBytes([]byte("dev provenance")),
		SourceGenerationID: "DEVELOPMENT/UNKNOWN/fixture-1", SourceTreeDigest: DigestBytes([]byte("dev tree")),
		SourceManifestDigest: DigestBytes([]byte("dev manifest")), WorkerViewIdentity: DigestBytes([]byte("dev view")), WorkerMountID: 42,
	}
	// This is the exact ready payload emitted in governed-pi-worker.ts, including
	// its five additional source/view exposure-evidence fields.
	shape := map[string]any{
		"type": "ready", "sequence": 1, "schema_version": PiWorkerSchemaVersion,
		"profile_id": profile.ID, "profile_digest": profile.Digest,
		"source_binding_digest": bootstrap.SourceBindingDigest, "source_provenance_digest": bootstrap.SourceProvenanceDigest,
		"source_generation_id": bootstrap.SourceGenerationID, "source_tree_digest": bootstrap.SourceTreeDigest,
		"source_manifest_digest": bootstrap.SourceManifestDigest, "worker_view_identity": bootstrap.WorkerViewIdentity,
		"worker_mount_id": bootstrap.WorkerMountID, "runtime_bundle_digest": profile.PiRuntimeBundleDigest,
		"descriptor_profile": profile.DescriptorProfile, "descriptor_profile_digest": profile.DescriptorProfileDigest,
		"node_version": profile.NodeVersion, "pi_version": "0.87.1", "provider_id": PiWorkerProviderID,
		"model_id": PiWorkerModelID, "tools": []string{"read", "write", "edit", "bash"},
		"tool_schema_sha256": piWorkerToolSchemaDigests(),
	}
	body, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodePiWorkerEvent(body)
	if err != nil {
		t.Fatalf("decode actual TypeScript ready event shape: %v", err)
	}
	if err := validateReadyEvent(event, profile, bootstrap); err != nil {
		t.Fatalf("actual TypeScript ready event did not match bootstrap evidence: %v", err)
	}

	for name, mutate := range map[string]func(*PiWorkerEvent){
		"generation": func(event *PiWorkerEvent) { event.SourceGenerationID += "/other" },
		"tree":       func(event *PiWorkerEvent) { event.SourceTreeDigest = DigestBytes([]byte("other tree")) },
		"manifest":   func(event *PiWorkerEvent) { event.SourceManifestDigest = DigestBytes([]byte("other manifest")) },
		"view":       func(event *PiWorkerEvent) { event.WorkerViewIdentity = DigestBytes([]byte("other view")) },
		"mount":      func(event *PiWorkerEvent) { event.WorkerMountID++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := event
			mutate(&changed)
			if err := validateReadyEvent(changed, profile, bootstrap); err == nil {
				t.Fatal("ready event with mismatched exposure evidence was accepted")
			}
		})
	}
}

func TestDevelopmentWorkerSourceEvidenceIsExplicitAndFixtureBound(t *testing.T) {
	directory := makePrivateTestDirectory(t, "worker-fixture")
	fixture := filepath.Join(directory, "fixture.txt")
	if err := os.WriteFile(fixture, []byte("selected fixture v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	first, err := developmentWorkerSourceEvidence(handle)
	if err != nil {
		t.Fatalf("derive non-claiming development evidence: %v", err)
	}
	if !strings.HasPrefix(first.SourceGenerationID, "DEVELOPMENT/UNKNOWN/") ||
		!validDigest(first.SourceBindingDigest) || !validDigest(first.SourceProvenanceDigest) ||
		!validDigest(first.SourceTreeDigest) || !validDigest(first.SourceManifestDigest) ||
		!validDigest(first.WorkerViewIdentity) || first.WorkerMountID == 0 {
		t.Fatalf("development evidence is incomplete or not explicitly labelled UNKNOWN: %+v", first)
	}
	if err := os.WriteFile(fixture, []byte("selected fixture v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := developmentWorkerSourceEvidence(handle)
	if err != nil {
		t.Fatalf("rederive development evidence after fixture change: %v", err)
	}
	if first.SourceTreeDigest == second.SourceTreeDigest || first.SourceManifestDigest == second.SourceManifestDigest {
		t.Fatal("development source evidence did not change with selected fixture contents")
	}
}

func TestPiWorkerEventPumpDeliversTerminalEOFErrorAndObservationGap(t *testing.T) {
	t.Run("terminal EOF", func(t *testing.T) {
		worker := newPiWorkerEventPumpFixture(t, 1)
		if err := worker.Channels.childEvents.Close(); err != nil {
			t.Fatal(err)
		}
		waitWorkerEventPump(t, worker)
		if _, err := worker.NextEvent(context.Background()); !errors.Is(err, io.EOF) {
			t.Fatalf("terminal worker EOF = %v, want io.EOF", err)
		}
	})
	t.Run("terminal error", func(t *testing.T) {
		worker := newPiWorkerEventPumpFixture(t, 1)
		if err := writePiFrame(worker.Channels.childEvents, []byte(`{"type":"stopped","sequence":1,"unknown":true}`)); err != nil {
			t.Fatal(err)
		}
		if err := worker.Channels.childEvents.Close(); err != nil {
			t.Fatal(err)
		}
		waitWorkerEventPump(t, worker)
		if _, err := worker.NextEvent(context.Background()); err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("terminal worker error was not delivered: %v", err)
		}
	})
	t.Run("observation gap before EOF", func(t *testing.T) {
		worker := newPiWorkerEventPumpFixture(t, 1)
		for sequence := uint64(1); sequence <= 2; sequence++ {
			body, err := json.Marshal(PiWorkerEvent{Type: "turn_end", Sequence: sequence, RequestID: "request-gap"})
			if err != nil {
				t.Fatal(err)
			}
			if err := writePiFrame(worker.Channels.childEvents, body); err != nil {
				t.Fatal(err)
			}
		}
		if err := worker.Channels.childEvents.Close(); err != nil {
			t.Fatal(err)
		}
		waitWorkerEventPump(t, worker)
		first, err := worker.NextEvent(context.Background())
		if err != nil || first.Type != "turn_end" || first.Sequence != 1 {
			t.Fatalf("first buffered event=%+v err=%v", first, err)
		}
		gap, err := worker.NextEvent(context.Background())
		if err != nil || gap.Type != "observation_gap" || gap.DroppedEvents != 1 || gap.DroppedBytes == 0 {
			t.Fatalf("observation gap=%+v err=%v", gap, err)
		}
		if _, err := worker.NextEvent(context.Background()); !errors.Is(err, io.EOF) {
			t.Fatalf("worker EOF after observation gap=%v", err)
		}
	})
}

func TestPiWorkerChannelsCloseOwnedSourcesButBorrowDevelopmentSources(t *testing.T) {
	newChannels := func(t *testing.T) (*PiWorkerChannels, *os.File, *os.File) {
		t.Helper()
		cwd, err := os.Open(makePrivateTestDirectory(t, "worker-cwd"))
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := os.Open(makePrivateTestDirectory(t, "worker-bundle"))
		if err != nil {
			_ = cwd.Close()
			t.Fatal(err)
		}
		channels, err := newPiWorkerChannels(cwd, bundle)
		if err != nil {
			_ = cwd.Close()
			_ = bundle.Close()
			t.Fatal(err)
		}
		return channels, cwd, bundle
	}

	t.Run("borrowed development sources stay open", func(t *testing.T) {
		channels, cwd, bundle := newChannels(t)
		defer cwd.Close()
		defer bundle.Close()
		if err := channels.CloseChildEnds(); err != nil {
			t.Fatal(err)
		}
		if err := channels.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := cwd.Stat(); err != nil {
			t.Fatalf("borrowed fixture CWD descriptor was closed: %v", err)
		}
		if _, err := bundle.Stat(); err != nil {
			t.Fatalf("borrowed fixture bundle descriptor was closed: %v", err)
		}
	})
	t.Run("owned production sources close once", func(t *testing.T) {
		channels, cwd, bundle := newChannels(t)
		defer cwd.Close()
		defer bundle.Close()
		channels.ownedSourceFDs = true
		if err := channels.CloseChildEnds(); err != nil {
			t.Fatal(err)
		}
		if err := channels.CloseChildEnds(); err != nil {
			t.Fatalf("repeated child-end close was not idempotent: %v", err)
		}
		if err := channels.Close(); err != nil {
			t.Fatalf("close owned channel set: %v", err)
		}
		if _, err := cwd.Stat(); err == nil {
			t.Fatal("owned worker exposure descriptor remained open")
		}
		if _, err := bundle.Stat(); err == nil {
			t.Fatal("owned runtime bundle descriptor remained open")
		}
	})
}

func newPiWorkerEventPumpFixture(t *testing.T, queueSize int) *PiSDKWorker {
	t.Helper()
	cwdHandle, err := os.Open(makePrivateTestDirectory(t, "worker-cwd"))
	if err != nil {
		t.Fatal(err)
	}
	bundleHandle, err := os.Open(makePrivateTestDirectory(t, "worker-bundle"))
	if err != nil {
		_ = cwdHandle.Close()
		t.Fatal(err)
	}
	channels, err := newPiWorkerChannels(cwdHandle, bundleHandle)
	if err != nil {
		_ = cwdHandle.Close()
		_ = bundleHandle.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = channels.Close()
		_ = cwdHandle.Close()
		_ = bundleHandle.Close()
	})
	worker := &PiSDKWorker{Channels: channels, events: make(chan PiWorkerEvent, queueSize), readerDone: make(chan struct{})}
	go worker.pumpEvents()
	return worker
}

func waitWorkerEventPump(t *testing.T, worker *PiSDKWorker) {
	t.Helper()
	select {
	case <-worker.readerDone:
	case <-time.After(time.Second):
		t.Fatal("Pi worker event pump did not terminate")
	}
}

func makePrivateTestDirectory(t *testing.T, prefix string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), prefix)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readPiTestFrame(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		t.Fatal(err)
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > PiWorkerMaxFrameBytes {
		t.Fatalf("worker test frame length = %d", length)
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatal(err)
	}
	return body
}
