//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/workflow"
)

func trialTestTrace(events ...audit.Event) audit.Trace {
	trace := audit.Trace{Records: []audit.Record{}, Effects: []audit.EffectTrace{}}
	for i, event := range events {
		trace.Records = append(trace.Records, audit.Record{Version: 1, Sequence: uint64(i + 1), Event: event, Hash: "h" + strconv.Itoa(i+1)})
	}
	return trace
}

func trialCatalogEvent(t *testing.T, kind, admissionHash string) audit.Event {
	t.Helper()
	data, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash})
	if err != nil {
		t.Fatal(err)
	}
	return audit.Event{Kind: kind, ID: kind + "/" + admissionHash, Data: data}
}

var trialTestRoot = workflow.RootIdentity{Device: 7, Inode: 42}

const trialTestRegistration = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// trialAdmitted is the lifecycle record of a managed-root run that holds the live root.
func trialAdmitted(t *testing.T, root workflow.RootIdentity, runID string) (audit.Event, managedLiveRootRunAdmittedEvent) {
	t.Helper()
	admitted := managedLiveRootRunAdmittedEvent{SchemaVersion: "tbound-managed-live-root-run-admitted/v1", Root: root,
		RegistrationDigest: trialTestRegistration,
		Descriptor:         managedLiveRootDescriptorIdentity{Path: "/var/lib/tbound/live", Device: root.Device, Inode: root.Inode, MountID: 9, Mode: 0o700},
		ServiceCgroup:      piruntime.D09ManagedLiveRootCgroupIdentity{Path: "/system.slice/tbound-pi.service", Device: 1, Inode: 2, MountID: 3},
		RunID:              runID, WorkflowID: "wf-" + runID, PublicationID: "pub-" + runID, OwnerEpoch: 1, ProcessID: 100, ServiceUID: 990, ServiceGID: 990}
	data, err := json.Marshal(admitted)
	if err != nil {
		t.Fatal(err)
	}
	return audit.Event{Kind: "managed_live_root_run_admitted", ID: managedLiveRootRunEventID(root, trialTestRegistration, runID, 1), Data: data}, admitted
}

func trialReleased(t *testing.T, admitted managedLiveRootRunAdmittedEvent, sequence uint64, hash string) audit.Event {
	t.Helper()
	key := managedLiveRootRunEventID(admitted.Root, admitted.RegistrationDigest, admitted.RunID, admitted.OwnerEpoch)
	data, err := json.Marshal(managedLiveRootRunResolvedEvent{SchemaVersion: "tbound-managed-live-root-run-resolved/v1", Root: admitted.Root,
		RegistrationDigest: admitted.RegistrationDigest, RunID: admitted.RunID, WorkflowID: admitted.WorkflowID, PublicationID: admitted.PublicationID,
		OwnerEpoch: admitted.OwnerEpoch, AdmissionSequence: sequence, AdmissionRecordHash: hash, Outcome: "no-publication"})
	if err != nil {
		t.Fatal(err)
	}
	return audit.Event{Kind: "managed_live_root_run_released", ID: key + "/released", Data: data}
}

func TestTrialPendingFromIsQuietWhenEverythingIsSettled(t *testing.T) {
	admittedEvent, admitted := trialAdmitted(t, trialTestRoot, "run-1")
	catalog := trialTestTrace(trialCatalogEvent(t, "pi_workflow_admission_reserved", "a1"), trialCatalogEvent(t, "pi_workflow_terminal", "a1"))
	lifecycle := trialTestTrace(admittedEvent, trialReleased(t, admitted, 1, "h1"))
	if err := trialPendingFrom(catalog, lifecycle, trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("settled state: %v", err)
	}
	if err := trialPendingFrom(trialTestTrace(), trialTestTrace(), trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("empty journals: %v", err)
	}
}

func TestTrialPendingFromRefusesAWorkflowWithoutTerminal(t *testing.T) {
	catalog := trialTestTrace(
		trialCatalogEvent(t, "pi_workflow_admission_reserved", "a1"), trialCatalogEvent(t, "pi_workflow_terminal", "a1"),
		trialCatalogEvent(t, "pi_workflow_admission_reserved", "a2"), trialCatalogEvent(t, "pi_workflow_fresh_authorized", "a2"))
	err := trialPendingFrom(catalog, trialTestTrace(), trialTestRoot, trialTestRegistration)
	if err == nil || !strings.Contains(err.Error(), "a2") || strings.Contains(err.Error(), "a1") {
		t.Fatalf("err = %v, want a refusal naming a2 only", err)
	}
}

func TestTrialPendingFromRefusesARunThatHoldsTheRoot(t *testing.T) {
	admittedEvent, admitted := trialAdmitted(t, trialTestRoot, "run-1")
	lifecycle := trialTestTrace(admittedEvent)
	err := trialPendingFrom(trialTestTrace(), lifecycle, trialTestRoot, trialTestRegistration)
	if err == nil || !strings.Contains(err.Error(), "run-1") {
		t.Fatalf("err = %v, want a refusal naming run-1", err)
	}
	// A run on another root is not this root's concern.
	other, _ := trialAdmitted(t, workflow.RootIdentity{Device: 7, Inode: 43}, "run-2")
	if err := trialPendingFrom(trialTestTrace(), trialTestTrace(other), trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("a run on another root: %v", err)
	}
	// Resolving it settles it.
	lifecycle = trialTestTrace(admittedEvent, trialReleased(t, admitted, 1, "h1"))
	if err := trialPendingFrom(trialTestTrace(), lifecycle, trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("a released run: %v", err)
	}
}

func TestTrialPendingFromRefusesUnreadableRecords(t *testing.T) {
	bad := audit.Event{Kind: "pi_workflow_admission_reserved", ID: "x", Data: []byte("{")}
	if err := trialPendingFrom(trialTestTrace(bad), trialTestTrace(), trialTestRoot, trialTestRegistration); err == nil {
		t.Fatal("an unreadable catalog record was accepted")
	}
	badRun := audit.Event{Kind: "managed_live_root_run_admitted", ID: "x", Data: []byte("{}")}
	if err := trialPendingFrom(trialTestTrace(), trialTestTrace(badRun), trialTestRoot, trialTestRegistration); err == nil {
		t.Fatal("an unreadable lifecycle record was accepted")
	}
}

func TestCgroupPopulated(t *testing.T) {
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(path, "cgroup.events"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cgroupPopulated(dir); err == nil {
		t.Error("a cgroup without cgroup.events was accepted")
	}
	write("populated 0\nfrozen 0\n")
	if got, err := cgroupPopulated(dir); err != nil || got {
		t.Fatalf("empty cgroup: %v, %v", got, err)
	}
	write("populated 1\nfrozen 0\n")
	if got, err := cgroupPopulated(dir); err != nil || !got {
		t.Fatalf("populated cgroup: %v, %v", got, err)
	}
	for _, bad := range []string{"", "frozen 0\n", "populated 2\n", "populated x\n"} {
		write(bad)
		if _, err := cgroupPopulated(dir); err == nil {
			t.Errorf("cgroup.events %q was accepted", bad)
		}
	}
	// cgroup.events is read without following a symlink.
	other := filepath.Join(t.TempDir(), "events")
	if err := os.WriteFile(other, []byte("populated 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "cgroup.events")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(path, "cgroup.events")); err != nil {
		t.Fatal(err)
	}
	if _, err := cgroupPopulated(dir); err == nil {
		t.Error("a symlinked cgroup.events was followed")
	}
}
