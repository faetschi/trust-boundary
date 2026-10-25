package evalcorpus

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// goldenPinsManifest has no cases; its canonical form is written out by hand below.
func goldenPinsManifest() Manifest {
	return Manifest{
		SchemaVersion: SchemaVersion,
		SplitSeed:     "golden-seed-v1",
		Pins: Pins{
			EnvironmentDigest: "sha256:" + strings.Repeat("0", 64),
			ModelDigest:       "sha256:" + strings.Repeat("1", 64),
			ProfileDigest:     "sha256:" + strings.Repeat("2", 64),
			PolicyDigest:      "sha256:" + strings.Repeat("3", 64),
		},
	}
}

// The expected digest was computed outside this package: SHA-256 over the exact bytes of
// the canonical string below (python3 hashlib).
func TestCanonicalJSONGoldenEmptyManifest(t *testing.T) {
	want := `{"cases":[],"construct_review":{"content_digest":"","panel_audit_digest":"","panel_receipt_digest":""},` +
		`"pins":{"environment_digest":"sha256:` + strings.Repeat("0", 64) +
		`","model_digest":"sha256:` + strings.Repeat("1", 64) +
		`","policy_digest":"sha256:` + strings.Repeat("3", 64) +
		`","profile_digest":"sha256:` + strings.Repeat("2", 64) +
		`"},"schema_version":"tbound-evalcorpus/v1","split_seed":"golden-seed-v1"}`
	got, err := CanonicalJSON(goldenPinsManifest())
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if string(got) != want {
		t.Errorf("canonical JSON =\n%s\nwant\n%s", got, want)
	}
	digest, err := Digest(goldenPinsManifest())
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	const wantDigest = "sha256:4c667423ea812aa6fe1f7d2d30dc61d25feafd58182a9863b6bcac7f1f1ae6ca"
	if digest != wantDigest {
		t.Errorf("Digest = %s; want %s", digest, wantDigest)
	}
}

func TestCanonicalStringEscaping(t *testing.T) {
	var buf bytes.Buffer
	if err := writeCanonicalString(&buf, "a\"b\\c\n\r\t\b\f\x01<>&é "); err != nil {
		t.Fatalf("writeCanonicalString: %v", err)
	}
	// RFC 8785 minimal escaping: quote, backslash, and control characters only; <, >, &,
	// non-ASCII, and U+2028 are written raw.
	want := `"a\"b\\c\n\r\t\b\f\u0001<>&é` + " " + `"`
	if buf.String() != want {
		t.Errorf("canonical string = %s; want %s", buf.String(), want)
	}
}

func TestCanonicalStringRejectsInvalidUTF8(t *testing.T) {
	var buf bytes.Buffer
	if err := writeCanonicalString(&buf, "bad\xff"); err == nil {
		t.Error("invalid UTF-8 string accepted")
	}
}

func TestDigestSurvivesJSONRoundTrip(t *testing.T) {
	q := smallQuotas()
	orig := allocated(t, synthManifest(q, synthSeed), q)
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Manifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want, _ := Digest(orig)
	got, err := Digest(back)
	if err != nil {
		t.Fatalf("Digest after round trip: %v", err)
	}
	if got != want {
		t.Errorf("digest after round trip = %s; want %s", got, want)
	}
}

func TestDigestTreatsNilAndEmptyArraysAlike(t *testing.T) {
	q := smallQuotas()
	a := allocated(t, synthManifest(q, synthSeed), q)
	b := allocated(t, synthManifest(q, synthSeed), q)
	// Synthetic cases use []string{} for lost_distinctions; a nil slice must give the same bytes.
	findCase(t, &b, "SYNTH-ADV-TM-A-001").Adversarial.Adaptation.LostDistinctions = nil
	da, err := Digest(a)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	db, err := Digest(b)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if da != db {
		t.Errorf("nil and empty arrays digest differently: %s vs %s", da, db)
	}
}

func TestDigestChangesWithContent(t *testing.T) {
	q := smallQuotas()
	a := allocated(t, synthManifest(q, synthSeed), q)
	b := allocated(t, synthManifest(q, synthSeed), q)
	findCase(t, &b, "SYNTH-AMB-01").Ambiguous.Rubric.Ref = "other-rubric"
	da, _ := Digest(a)
	db, _ := Digest(b)
	if da == db {
		t.Error("changing a rubric reference did not change the digest")
	}
	if !strings.HasPrefix(da, "sha256:") || len(da) != len("sha256:")+64 {
		t.Errorf("digest %q is not sha256 plus 64 hex digits", da)
	}
}

// ContentDigest is the digest the panel binds to: it ignores exactly the fields the advisor
// writes after the panel run.
func TestContentDigestIgnoresOnlySignoffsAndConstructReview(t *testing.T) {
	q := smallQuotas()
	base := allocated(t, synthManifest(q, synthSeed), q)
	want, err := ContentDigest(base)
	if err != nil {
		t.Fatalf("ContentDigest: %v", err)
	}
	full, _ := Digest(base)
	if want == full {
		t.Error("ContentDigest equals Digest although signoffs and construct_review are set")
	}

	signed := base
	signed.Cases = append([]Case(nil), base.Cases...)
	signed.Cases[3].Signoff = Signoff{Reviewer: "someone-else", Human: false, Author: true, Decision: "reject", Audited: true, Resolution: "r"}
	signed.ConstructReview = ConstructReview{ContentDigest: "x", PanelReceiptDigest: "y", PanelAuditDigest: "z"}
	if got, _ := ContentDigest(signed); got != want {
		t.Errorf("ContentDigest changed with a signoff or construct_review: %s vs %s", got, want)
	}
	if got, _ := Digest(signed); got == full {
		t.Error("Digest ignored a signoff or construct_review change")
	}
	if signed.Cases[3].Signoff.Reviewer != "someone-else" || signed.ConstructReview.ContentDigest != "x" {
		t.Error("ContentDigest cleared the fields of its argument")
	}

	edited := allocated(t, synthManifest(q, synthSeed), q)
	findCase(t, &edited, "SYNTH-AMB-01").Ambiguous.Rubric.Ref = "other-rubric"
	if got, _ := ContentDigest(edited); got == want {
		t.Error("ContentDigest ignored a change of case content")
	}
	reordered := base
	reordered.Cases = append([]Case(nil), base.Cases...)
	reordered.Cases[0], reordered.Cases[1] = reordered.Cases[1], reordered.Cases[0]
	if got, _ := ContentDigest(reordered); got == want {
		t.Error("ContentDigest ignored case order")
	}
}
