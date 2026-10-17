//go:build linux

package piruntime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/sessionrepo"
)

func d09AuditHeadFromJournal(t *testing.T) string {
	t.Helper()
	journalDir := t.TempDir()
	if err := os.Chmod(journalDir, 0o700); err != nil { // t.TempDir is 0755 under umask 022
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(journalDir, "store-audit.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal fixture: %v", err)
	}
	record, err := journal.Append(audit.Event{Kind: "d09-store-audit-head-test", ID: "record-1"})
	closeErr := journal.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append/close audit journal fixture: append=%v close=%v", err, closeErr)
	}
	if !validBareAuditHead(record.Hash) || validDigest(record.Hash) {
		t.Fatalf("audit journal fixture did not produce a bare hash: %q", record.Hash)
	}
	return record.Hash
}

func TestD09AuditHeadDomainAcrossSourceShapeAndCapturedProof(t *testing.T) {
	head := d09AuditHeadFromJournal(t)
	sourceDir := t.TempDir()
	sourceFile, err := os.Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sourceFile.Close() })
	info, err := sourceFile.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("stat source descriptor: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("source descriptor has no Linux stat identity")
	}
	mountID, err := descriptorMountID(sourceFile.Fd())
	if err != nil {
		t.Fatalf("read source descriptor mount ID: %v", err)
	}
	if stat.Dev == 0 || stat.Ino == 0 || mountID == 0 {
		t.Fatalf("source descriptor identity is incomplete: dev=%d ino=%d mount=%d", stat.Dev, stat.Ino, mountID)
	}

	digest := func(value string) string { return DigestBytes([]byte(value)) }
	evidence := sessionrepo.SealedSourceEvidence{
		GenerationID: "generation-1", TreeDigest: digest("tree"),
		ManifestDigest: digest("manifest"), SourceBindingDigest: digest("binding"),
		ProvenanceDigest: digest("provenance"), StoreAuditHead: head,
		StoreAuditRecordCount: 1, SourceIdentityDigest: digest("identity"),
		SourceDevice: uint64(stat.Dev), SourceInode: stat.Ino, SourceMountID: mountID,
	}
	if err := validateD09WorkerSourceDescriptor(sourceFile, evidence); err != nil {
		t.Fatalf("actual audit journal head rejected by source descriptor validation: %v", err)
	}
	boundEvidence := d09SourceEvidenceForBindings(evidence)
	if err := boundEvidence.validateShape(); err != nil {
		t.Fatalf("actual audit journal head rejected by D09 source evidence shape: %v", err)
	}

	const launcherPID = 123
	proof := D09TCBProof{
		Schema: "tbound-pi-entrypoint/v1", ContainerName: "test-cell",
		LeaderPID: launcherPID + 1, ContainerLeaderPID: 1, LauncherProcessPID: launcherPID,
		ImageDigest: digest("image"), TemplateDigest: "template-profile",
		ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: 31, RuntimeMountID: 32,
		ConsumerWorkerMountID: 31, ConsumerRuntimeMountID: 32,
		SourceGenerationID: evidence.GenerationID, SourceTreeDigest: evidence.TreeDigest,
		SourceManifestDigest: evidence.ManifestDigest, SourceBindingDigest: evidence.SourceBindingDigest,
		SourceProvenanceDigest: evidence.ProvenanceDigest, SourceIdentityDigest: evidence.SourceIdentityDigest,
		SourceStoreAuditHead: head, SourceStoreAuditCount: evidence.StoreAuditRecordCount,
		SourceDevice: evidence.SourceDevice, SourceInode: evidence.SourceInode, SourceMountID: evidence.SourceMountID,
	}
	worker := &PiCLIWorker{
		Session:     &Session{child: &Process{cmd: &exec.Cmd{Process: &os.Process{Pid: launcherPID}}}},
		launchProof: proof,
	}
	if captured, ok := worker.D09LaunchProof(); !ok || captured.SourceStoreAuditHead != head {
		t.Fatal("actual audit journal head rejected by captured D09 proof validation")
	}

	badHeads := []string{
		strings.Repeat("A", 64),
		"sha256:" + head,
		head[:len(head)-1],
		strings.Repeat("g", 64),
		"",
	}
	for _, badHead := range badHeads {
		if validBareAuditHead(badHead) {
			t.Errorf("invalid audit head accepted by bare validator: %q", badHead)
		}
		badEvidence := evidence
		badEvidence.StoreAuditHead = badHead
		if err := validateD09WorkerSourceDescriptor(sourceFile, badEvidence); err == nil {
			t.Errorf("invalid audit head accepted by source descriptor validation: %q", badHead)
		}
		badBoundEvidence := d09SourceEvidenceForBindings(badEvidence)
		if err := badBoundEvidence.validateShape(); err == nil {
			t.Errorf("invalid audit head accepted by source evidence shape: %q", badHead)
		}
		badProof := proof
		badProof.SourceStoreAuditHead = badHead
		worker.launchProof = badProof
		if _, ok := worker.D09LaunchProof(); ok {
			t.Errorf("invalid audit head accepted by captured D09 proof validation: %q", badHead)
		}
	}
}
