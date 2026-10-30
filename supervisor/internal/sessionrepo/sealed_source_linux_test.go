//go:build linux

package sessionrepo

import (
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
)

func TestSealedSourceAuditPrefixAllowsAppendButRejectsRewriteAndTruncate(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	openBundle := func(name string, payloads ...string) EvidenceBundle {
		t.Helper()
		directory := filepath.Join(base, name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		journal, err := audit.Open(filepath.Join(directory, "journal.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = journal.Close() })
		for i, payload := range payloads {
			if _, err := journal.Append(audit.Event{Kind: "source-prefix-fixture", ID: string(rune('a' + i)), Data: []byte(payload)}); err != nil {
				t.Fatal(err)
			}
		}
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := marshalAuditJournal(trace.Records)
		if err != nil {
			t.Fatal(err)
		}
		return EvidenceBundle{
			AuditJournal: encoded, AuditRecordCount: uint64(len(trace.Records)),
			AuditHeadHash: auditHeadHash(trace.Records),
		}
	}

	issued := openBundle("issued", "D06-source", "D06-private-git-ready")
	evidence := SealedSourceEvidence{StoreAuditHead: issued.AuditHeadHash, StoreAuditRecordCount: issued.AuditRecordCount}
	if !sealedSourceAuditPrefixMatches(issued, evidence) {
		t.Fatal("issued audit prefix did not match itself")
	}

	appended := openBundle("appended", "D06-source", "D06-private-git-ready", "successful-store-write")
	if !sealedSourceAuditPrefixMatches(appended, evidence) {
		t.Fatal("ordinary append after issuance invalidated the retained source prefix")
	}

	rewritten := openBundle("rewritten", "different-source-prefix", "D06-private-git-ready")
	if sealedSourceAuditPrefixMatches(rewritten, evidence) {
		t.Fatal("a validly re-hashed replacement prefix was accepted")
	}

	truncated := openBundle("truncated", "D06-source")
	if sealedSourceAuditPrefixMatches(truncated, evidence) {
		t.Fatal("a journal truncated before the issued head was accepted")
	}
}
