package evalcorpus

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func loadManifestFileForDigest(t *testing.T, path string) Manifest {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

// T5 moves the injections of seven cases, so both digests move. Sign-offs stay out of ContentDigest.
func TestT5DraftMovesDigestsAndSignoffsStayOutOfContentDigest(t *testing.T) {
	original := loadManifestFileForDigest(t, "../../../corpus/pilot/provenance-2026-10-29/manifest.draft.json")
	draft := loadManifestFileForDigest(t, "../../../corpus/pilot/t5-2026-10-29/manifest.draft.json")
	originalDigest, err := Digest(original)
	if err != nil {
		t.Fatal(err)
	}
	draftDigest, err := Digest(draft)
	if err != nil {
		t.Fatal(err)
	}
	originalContent, err := ContentDigest(original)
	if err != nil {
		t.Fatal(err)
	}
	draftContent, err := ContentDigest(draft)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Digest original %s, T5 %s", originalDigest, draftDigest)
	t.Logf("ContentDigest original %s, T5 %s", originalContent, draftContent)
	if draftDigest == originalDigest {
		t.Error("Digest did not change with the T5 carrier change")
	}
	if draftContent == originalContent {
		t.Error("ContentDigest did not change with the T5 carrier change")
	}

	signed := draft
	signed.Cases = slices.Clone(draft.Cases)
	for i := range signed.Cases {
		signed.Cases[i].Signoff = Signoff{Reviewer: "reviewer", Human: true, Author: true, Decision: DecisionAccept, Audited: true}
	}
	if signedContent, err := ContentDigest(signed); err != nil || signedContent != draftContent {
		t.Errorf("signing off changed ContentDigest to %s (err %v), want %s", signedContent, err, draftContent)
	}
	if signedDigest, err := Digest(signed); err != nil || signedDigest == draftDigest {
		t.Errorf("signing off did not change Digest (err %v)", err)
	}

	reset := signed
	reset.Cases = slices.Clone(signed.Cases)
	for i := range reset.Cases {
		reset.Cases[i].Signoff = draft.Cases[i].Signoff
	}
	if resetContent, err := ContentDigest(reset); err != nil || resetContent != draftContent {
		t.Errorf("resetting sign-offs changed ContentDigest to %s (err %v), want %s", resetContent, err, draftContent)
	}
}

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
	want := `{"cases":[],"construct_review":{"content_digest":"","panel_audit_digest":"","panel_receipt_digest":"","panel_stats_digest":""},` +
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
	const wantDigest = "sha256:b13eac38d829f8f4c0d4ffd5039c20d8834c05a1bbb43fc1dfa4bb1c535a6cb7"
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

func TestContentDigestExcludesFreezePins(t *testing.T) {
	m := allocated(t, synthManifest(smallQuotas(), synthSeed), smallQuotas())
	content, _ := ContentDigest(m)
	full, _ := Digest(m)
	pins := reflect.ValueOf(&m.Pins).Elem()
	for i := 0; i < pins.NumField(); i++ {
		t.Run(pins.Type().Field(i).Name, func(t *testing.T) {
			old := pins.Field(i).String()
			pins.Field(i).SetString("sha256:" + strings.Repeat("f", 64))
			defer pins.Field(i).SetString(old)
			if got, err := ContentDigest(m); err != nil || got != content {
				t.Errorf("freeze pin changed panel content digest: %s %v", got, err)
			}
			if got, _ := Digest(m); got == full {
				t.Error("full Digest ignored freeze pin")
			}
		})
	}
}
func TestContentDigestIncludesEveryCaseField(t *testing.T) {
	m := allocated(t, synthManifest(smallQuotas(), synthSeed), smallQuotas())
	content, _ := ContentDigest(m)
	for i := 0; i < reflect.TypeOf(Case{}).NumField(); i++ {
		field := reflect.TypeOf(Case{}).Field(i)
		if field.Name == "Signoff" {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			edited := m
			edited.Cases = append([]Case(nil), m.Cases...)
			v := reflect.ValueOf(&edited.Cases[0]).Elem().Field(i)
			switch v.Kind() {
			case reflect.String:
				v.SetString(v.String() + "changed")
			case reflect.Pointer:
				if v.IsNil() {
					v.Set(reflect.New(v.Type().Elem()))
				} else {
					v.SetZero()
				}
			default:
				t.Fatalf("unhandled case field %s", field.Name)
			}
			if got, err := ContentDigest(edited); err != nil || got == content {
				t.Fatalf("case field ignored or invalid digest: %s %v", got, err)
			}
		})
	}
}
