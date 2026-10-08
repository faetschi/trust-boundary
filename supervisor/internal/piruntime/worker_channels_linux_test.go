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
