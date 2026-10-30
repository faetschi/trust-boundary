//go:build linux

package sessionrepo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
)

// A D06 journal written before xattr_authority existed carries the v1 label. It
// is refused with the unsupported-version error (an upgrade requires draining
// retained sessions first), not with a digest or field error, and the missing
// authority is not inferred as bounded.
func TestSealedSourceRefusesPreAuthorityD06RecordsAsAnUnsupportedVersion(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	bundleOf := func(name, sourceData, readyData string) EvidenceBundle {
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
		if _, err := journal.Append(audit.Event{Kind: "sessionlaunch_source_binding", ID: "source", Data: []byte(sourceData)}); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.Append(audit.Event{Kind: "sessionlaunch_private_git_ready", ID: "ready", Data: []byte(readyData)}); err != nil {
			t.Fatal(err)
		}
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := marshalAuditJournal(trace.Records)
		if err != nil {
			t.Fatal(err)
		}
		return EvidenceBundle{AuditJournal: encoded, AuditRecordCount: uint64(len(trace.Records)), AuditHeadHash: auditHeadHash(trace.Records)}
	}
	const (
		v1 = "tbound-sessionlaunch-provenance/v1"
		v2 = D06ProvenanceSchemaVersion
	)
	record := func(version string) string {
		return `{"schema_version":"` + version + `","provenance":{"schema_version":"` + version + `","binding_digest":"sha256:x"}}`
	}
	if v2 == v1 {
		t.Fatal("the D06 provenance label was not bumped")
	}

	_, err := verifyD06SourceRecords(bundleOf("old", record(v1), record(v1)))
	if !errors.Is(err, ErrUnsupportedProvenanceVersion) || !errors.Is(err, ErrSealedSourceUnavailable) ||
		!strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), v1) {
		t.Fatalf("pre-authority D06 records = %v, want the unsupported-version error", err)
	}
	// A mixed journal is refused the same way, whichever record is old.
	_, err = verifyD06SourceRecords(bundleOf("mixed", record(v2), record(v1)))
	if !errors.Is(err, ErrUnsupportedProvenanceVersion) {
		t.Fatalf("old private-Git-ready record = %v, want the unsupported-version error", err)
	}
	// Current-version records pass the version gate and fail on content instead.
	_, err = verifyD06SourceRecords(bundleOf("current", record(v2), record(v2)))
	if err == nil || errors.Is(err, ErrUnsupportedProvenanceVersion) {
		t.Fatalf("current-version records = %v, want a non-version refusal", err)
	}
}
