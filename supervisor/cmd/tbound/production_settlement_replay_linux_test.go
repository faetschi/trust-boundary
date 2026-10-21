//go:build linux && tboundreplay

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/workflowrecovery"

	_ "modernc.org/sqlite"
)

// TestReplayPriorSettlementOnExportedGuestState runs the settlement verification that a service
// start's first admission runs over every journaled prior (journal.begin), on a read-only export of
// a guest's state. The current bindings are the export's etc/tbound files; the request journal,
// catalog, protected root, owner store and audit journals are the export's var/lib/tbound, which must
// be mounted at /var/lib/tbound, and the test must run as the service uid (the stores accept only
// files of their own uid). TBOUND_REPLAY_ETC names the exported etc/tbound directory.
//
// Stubbed, because they measure the live host: the live root's identity is taken from the
// registration instead of measured (replayLiveRoot); the registration is decoded without its
// signature check; the device- and inode-bound identities of the protected root and the owner
// stores are the ones their records hold (workflowrecovery.ReplayRecordedHostIdentities), after the
// same file checks, so the records must still agree with each other. The template and profile are
// decoded and the profile's signature is verified, without the root-owned, read-only file checks of
// the service loaders.
func TestReplayPriorSettlementOnExportedGuestState(t *testing.T) {
	etc := os.Getenv("TBOUND_REPLAY_ETC")
	if etc == "" {
		t.Skip("TBOUND_REPLAY_ETC is not set")
	}
	raw, err := os.ReadFile(filepath.Join(etc, "d09-cell.json"))
	if err != nil {
		t.Fatal(err)
	}
	template, err := piruntime.DecodeD09CellTemplate(raw)
	if err != nil {
		t.Fatalf("current template: %v", err)
	}
	profile, err := piruntime.VerifyLinuxSignedHostProfileFiles(filepath.Join(etc, "pi-host-profile.json"),
		filepath.Join(etc, "pi-host-profile.ed25519"), filepath.Join(etc, "pi-host-profile.pub"))
	if err != nil {
		t.Fatalf("current profile: %v", err)
	}
	raw, err = os.ReadFile(filepath.Join(etc, "managed-live-root.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registration piruntime.D09ManagedLiveRootRegistration
	if err := json.Unmarshal(raw, &registration); err != nil {
		t.Fatalf("current registration: %v", err)
	}
	t.Logf("current bindings: profile %s %s, template %s, registration %s",
		profile.ID, profile.Digest, profile.ContainmentProfileDigest, registration.Digest)

	requestJournal, err := audit.Open(piServiceRequestJournalPath)
	if err != nil {
		t.Fatalf("open request journal: %v", err)
	}
	defer requestJournal.Close()
	requests, err := newPiServiceRequestJournal(requestJournal)
	if err != nil {
		t.Fatalf("replay request journal: %v", err)
	}
	catalog, err := openPiServiceWorkflowCatalog(piServiceWorkflowCatalogPath)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	defer catalog.Close()
	if len(requests.entries) == 0 {
		t.Fatal("the request journal has no prior")
	}
	stores := make(map[string]string)
	for _, prior := range requests.entries {
		if entry, ok := catalog.admissionEntry(prior.acceptedRecord.Hash); ok {
			stores[entry.OwnerDBPath] = entry.OwnerStoreIdentity.Digest
		}
	}
	workflowrecovery.ReplayRecordedHostIdentities(piServiceWorkflowRootDBPath, recordedProtectedRootIdentity(t), stores)

	for _, prior := range requests.entries {
		err := catalog.verifyCompletedServiceRequest(context.Background(), prior, replayLiveRoot{registration}, template, profile)
		entry, _ := catalog.admissionEntry(prior.acceptedRecord.Hash)
		t.Logf("prior %s: err=%v terminal=%v latest=%s run=%s transaction=%s", prior.accepted.RequestID, err,
			entry.Terminal, entry.LatestCheckpointDigest, entry.TerminalRunID, entry.TerminalTransactionID)
		if err != nil {
			t.Errorf("prior %s is not settled: %v", prior.accepted.RequestID, err)
		}
	}
}

// recordedProtectedRootIdentity reads the identity the protected root recorded for itself.
func recordedProtectedRootIdentity(t *testing.T) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+piServiceWorkflowRootDBPath+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var identity string
	if err := db.QueryRow("SELECT root_identity_digest FROM workflow_root_metadata WHERE singleton=1").Scan(&identity); err != nil {
		t.Fatalf("read recorded protected-root identity: %v", err)
	}
	return identity
}

// replayLiveRoot stands in for the verified managed live root: its identity is the one the
// registration records, as the live host would measure it.
type replayLiveRoot struct {
	registration piruntime.D09ManagedLiveRootRegistration
}

func (r replayLiveRoot) Registration() piruntime.D09ManagedLiveRootRegistration {
	return r.registration
}

func (r replayLiveRoot) measureRoot(context.Context) (managedLiveRootDescriptorIdentity, error) {
	return managedLiveRootDescriptorIdentity{Path: r.registration.RootPath, Device: r.registration.RootDevice,
		Inode: r.registration.RootInode, MountID: r.registration.RootMountID, Filesystem: r.registration.RootFilesystemMagic,
		UID: uint32(r.registration.RootUID), GID: uint32(r.registration.RootGID), Mode: r.registration.RootMode}, nil
}
