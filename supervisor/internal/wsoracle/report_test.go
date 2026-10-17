package wsoracle

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRegisteredPredicateIDsVersionAndVerdictValues(t *testing.T) {
	if PredicateWS05 != "EFF-WS-05" || PredicateWS06 != "EFF-WS-06" {
		t.Fatalf("predicate IDs = %q, %q; want EFF-WS-05 and EFF-WS-06", PredicateWS05, PredicateWS06)
	}
	if PredicateWS01 != "EFF-WS-01" || PredicateWS02 != "EFF-WS-02" || PredicateWS03 != "EFF-WS-03" || PredicateWS04 != "EFF-WS-04" {
		t.Fatalf("path predicate IDs = %q %q %q %q", PredicateWS01, PredicateWS02, PredicateWS03, PredicateWS04)
	}
	if OracleVersion != "wsoracle/v6" {
		t.Fatalf("OracleVersion = %q", OracleVersion)
	}
	if VerdictNoEffect != "no_effect" || VerdictEffectPresent != "effect_present" || VerdictEvidenceLost != "evidence_lost" {
		t.Fatalf("verdict values changed: %q %q %q", VerdictNoEffect, VerdictEffectPresent, VerdictEvidenceLost)
	}
	if ScopeUnitCheck != "unit_check" {
		t.Fatalf("ScopeUnitCheck = %q", ScopeUnitCheck)
	}
}

func TestReportDigestIsStableForIdenticalInputsAndCoversFindings(t *testing.T) {
	first := EvaluateWS05(stagedUnattributedCase())
	second := EvaluateWS05(stagedUnattributedCase())
	d1, err := first.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := second.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("digest differs for identical inputs: %s vs %s", d1, d2)
	}
	changed := EvaluateWS05(unattributedCreateCase())
	d3, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if d3 == d1 {
		t.Fatal("digest did not change when the findings changed")
	}
}

func TestJSONIsDeterministicAndNeverEncodesNullLists(t *testing.T) {
	r := EvaluateWS05(noChangeCase())
	wantVerdict(t, r, VerdictNoEffect)
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"findings": []`, `"gaps": []`, `"unattributed_staged": []`} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON lacks %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "null") {
		t.Fatalf("JSON encodes null:\n%s", s)
	}
	again, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != s {
		t.Fatal("JSON output is not repeatable")
	}
}

func TestJSONRoundTripKeepsVerdictFindingsAndWitness(t *testing.T) {
	r := EvaluateWS06(staleOverwriteCase())
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Verdict != VerdictEffectPresent || len(back.Findings) != 1 || back.Witness.LockIdentity != "lock-1" {
		t.Fatalf("round trip lost content: %+v", back)
	}
}

func TestPreservationFieldsNameTheGapAndFindingFields(t *testing.T) {
	r := EvaluateWS05(noChangeCase())
	for _, want := range []string{"gaps[].kind", "gaps[].path", "gaps[].manifest"} {
		if !slices.Contains(r.Preservation, want) {
			t.Fatalf("preservation fields %v lack %s", r.Preservation, want)
		}
	}
}

func TestWS05WitnessRecordsManifestsLedgerAndExercisedChecks(t *testing.T) {
	r := EvaluateWS05(unattributedCreateCase())
	labels := make([]string, 0, len(r.Witness.Manifests))
	for _, m := range r.Witness.Manifests {
		labels = append(labels, m.Label)
		if m.Digest == "" || !m.Complete {
			t.Fatalf("manifest ref %+v lacks a digest or is incomplete", m)
		}
	}
	if want := []string{"baseline", "sealed", "pre-publication", "post-publication"}; !slices.Equal(labels, want) {
		t.Fatalf("witness manifests = %v, want %v", labels, want)
	}
	want := []string{"manifest-completeness", "live-root-identity", "ledger-composition", "sealed-reconciliation", "live-application"}
	if !slices.Equal(r.Witness.Exercised, want) {
		t.Fatalf("exercised = %v, want %v", r.Witness.Exercised, want)
	}
	if r.Witness.LedgerEntries != 0 || r.Witness.LedgerDigest == "" {
		t.Fatalf("ledger witness = %d entries, digest %q", r.Witness.LedgerEntries, r.Witness.LedgerDigest)
	}
	if r.Scope != ScopeUnitCheck || r.Instrument != Instrument || r.OracleVersion != OracleVersion {
		t.Fatalf("report header = %+v", r)
	}
}

func TestWS06WitnessRecordsLockAndPublicationAsRecordedOnly(t *testing.T) {
	r := EvaluateWS06(staleOverwriteCase())
	if len(r.Witness.Manifests) != 3 {
		t.Fatalf("manifests = %d, want 3", len(r.Witness.Manifests))
	}
	if r.Witness.LockIdentity != "lock-1" || r.Witness.PublicationResult != "SUCCEEDED" {
		t.Fatalf("witness = %+v", r.Witness)
	}
	want := []string{"manifest-completeness", "live-root-identity", "lock-identity", "affected-set", "stale-baseline-check", "outside-set-check"}
	if !slices.Equal(r.Witness.Exercised, want) {
		t.Fatalf("exercised = %v, want %v", r.Witness.Exercised, want)
	}
}

func TestManifestDigestCoversContentButNotRootIdentity(t *testing.T) {
	a := manifest("baseline", fileEntry("f.txt", "A"))
	b := a
	b = withRootID(b, 7, 9)
	if a.Digest() != b.Digest() {
		t.Fatal("manifest digest depends on the root identity, which must not be encoded")
	}
	c := manifest("baseline", fileEntry("f.txt", "B"))
	if a.Digest() == c.Digest() {
		t.Fatal("manifest digest did not change with content")
	}
}

func TestLedgerDigestCoversBeforeAndAfter(t *testing.T) {
	a := []LedgerEntry{approve("e1", 1, LedgerChange{Path: "f.txt", After: entryPtr(fileEntry("f.txt", "A"))})}
	b := []LedgerEntry{approve("e1", 1, LedgerChange{Path: "f.txt", After: entryPtr(fileEntry("f.txt", "B"))})}
	if ledgerDigest(a) == ledgerDigest(b) {
		t.Fatal("ledger digest did not change with the After state")
	}
	if ledgerDigest(nil) != ledgerDigest([]LedgerEntry{}) {
		t.Fatal("nil and empty ledgers must have the same digest")
	}
}
