//go:build linux

package main

import (
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/workflowrecovery"
)

// The binding gained xattr_authority, the corpus manifest digest and the corpus quota
// profile under the v2 names, so the names moved to v3. A v2 binding is refused with an
// unsupported-version error and is never hashed under the v3 domain.
func TestRuntimeBindingVersionIsV3AndV2IsRefused(t *testing.T) {
	if governedRecoveryRuntimeSchema != "tbound-governed-recovery-runtime/v3" ||
		governedRecoveryRuntimeBindingDomain != "tbound-workflow-runtime-binding/v3" {
		t.Fatalf("binding names are not v3: %q %q", governedRecoveryRuntimeSchema, governedRecoveryRuntimeBindingDomain)
	}
	binding := governedRecoveryRuntimeBinding{SchemaVersion: governedRecoveryRuntimeSchema, StoreRootIdentity: "store",
		CommandJournalIdentities: []string{}, BashOutputPaths: []string{}}
	if digest, err := binding.digest(); err != nil || !validBareWorkflowDigest(digest) {
		t.Fatalf("v3 binding refused: %q %v", digest, err)
	}
	for _, schema := range []string{"tbound-governed-recovery-runtime/v2", "tbound-governed-recovery-runtime/v1", "", "tbound-governed-recovery-runtime/v4"} {
		old := binding
		old.SchemaVersion = schema
		digest, err := old.digest()
		if !errors.Is(err, ErrGovernedRecoveryUnsupportedVersion) || digest != "" || !strings.Contains(err.Error(), "unsupported version") {
			t.Fatalf("schema %q was not refused as an unsupported version: %q %v", schema, digest, err)
		}
	}
}

// A workflow pinned under the v2 names fails recovery with the unsupported-version
// error, not with a bare digest mismatch, and any other mismatch is still a rollback.
func TestRuntimePinFromV2IsReportedAsAnUnsupportedVersion(t *testing.T) {
	binding := governedRecoveryRuntimeBinding{SchemaVersion: governedRecoveryRuntimeSchema, StoreRootIdentity: "store",
		CommandJournalIdentities: []string{}, BashOutputPaths: []string{}, CorpusManifestDigest: corpusTestDigest("corpus")}
	current, err := binding.digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkGovernedRecoveryRuntimePin(binding, current); err != nil {
		t.Fatalf("matching pin refused: %v", err)
	}
	legacy, err := binding.legacyV2Digest()
	if err != nil || legacy == current {
		t.Fatalf("the v2 digest must differ from the v3 digest: %q %q %v", legacy, current, err)
	}
	if err := checkGovernedRecoveryRuntimePin(binding, legacy); !errors.Is(err, ErrGovernedRecoveryUnsupportedVersion) {
		t.Fatalf("a v2 pin was not reported as an unsupported version: %v", err)
	}
	if err := checkGovernedRecoveryRuntimePin(binding, strings.Repeat("0", 64)); !errors.Is(err, workflowrecovery.ErrRollback) ||
		errors.Is(err, ErrGovernedRecoveryUnsupportedVersion) {
		t.Fatalf("an unrelated pin mismatch is not a plain rollback: %v", err)
	}
}
