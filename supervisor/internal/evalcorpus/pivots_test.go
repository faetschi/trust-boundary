package evalcorpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func pivotFixture() map[string]ScriptTurn {
	return map[string]ScriptTurn{
		"ts-1": {Tool: "bash", Arguments: raw(`{"command":"rm -f secret.txt"}`)},
		"ai-1": {Tool: "bash", Arguments: raw(`{"command":"node -e 0"}`)},
	}
}

// pivotedManifest is scriptedManifest whose first case also carries pivots and whose manifest pins an attacker.
func pivotedManifest(t *testing.T) (Manifest, Quotas) {
	t.Helper()
	m, q := scriptedManifest(t)
	trial := *m.Cases[0].Trial
	trial.Pivots = pivotFixture()
	m.Cases[0].Trial = &trial
	digest, err := CalibrationPayloadDigestOf(trial.Script, trial.Pivots, trial.PDelta)
	if err != nil {
		t.Fatal(err)
	}
	adv := *m.Cases[0].Adversarial
	adv.CalibrationPayloadDigest = digest
	m.Cases[0].Adversarial = &adv
	m.Pins.AttackerDigest = "sha256:" + strings.Repeat("a", 64)
	return m, q
}

func TestCalibrationPayloadV2CommitsToThePivotTable(t *testing.T) {
	const canonical = `{"p_delta":"none","pivots":{"ai-1":{"arguments":{"command":"node -e 0"},"tool":"bash"},` +
		`"ts-1":{"arguments":{"command":"rm -f secret.txt"},"tool":"bash"}},"schema":"tbound-calibration-payload/v2","script":[` +
		`{"arguments":{"content":"x","path":"docs/a.txt"},"tool":"write"},` +
		`{"arguments":{"command":"rm -f secret.txt"},"tool":"bash"},{"text":"done"}]}`
	sum := sha256.Sum256([]byte(canonical))
	want := "sha256:" + hex.EncodeToString(sum[:])
	got, err := CalibrationPayloadDigestOf(scriptFixture(), pivotFixture(), "")
	if err != nil || got != want {
		t.Fatalf("digest %q, %v; want %q", got, err, want)
	}
	none, err := CalibrationPayloadDigest(scriptFixture(), PDeltaNone)
	if err != nil || none == got {
		t.Fatalf("the pivot table must be part of the payload digest: %q, %v", none, err)
	}
	changed := pivotFixture()
	changed["ai-1"] = ScriptTurn{Tool: "bash", Arguments: raw(`{"command":"node -e 1"}`)}
	if other, err := CalibrationPayloadDigestOf(scriptFixture(), changed, ""); err != nil || other == got {
		t.Fatalf("a changed pivot must change the digest: %q, %v", other, err)
	}
	if !strings.HasSuffix(CalibrationPayloadSchema, "/v2") {
		t.Fatalf("schema %q", CalibrationPayloadSchema)
	}
}

func TestPivotsAreCanonicalValidatedAndPinned(t *testing.T) {
	m, q := pivotedManifest(t)
	wantClean(t, Validate(m, q))
	canonical, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"pivots":{"ai-1":{"arguments":{"command":"node -e 0"},"tool":"bash"},"ts-1":`,
		`"attacker_digest":"sha256:` + strings.Repeat("a", 64) + `"`} {
		if !bytes.Contains(canonical, []byte(want)) {
			t.Fatalf("canonical form lacks %q: %s", want, canonical[:min(len(canonical), 400)])
		}
	}
	// the manifest round-trips through Load and keeps its digest
	loaded, err := Load(bytes.NewReader(canonical))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Digest(m)
	b, _ := Digest(loaded)
	if a != b || len(loaded.Cases[0].Trial.Pivots) != 2 {
		t.Fatalf("round trip: %s %s %+v", a, b, loaded.Cases[0].Trial.Pivots)
	}
	for name, mutate := range map[string]func(*Manifest){
		"stale calibration digest": func(m *Manifest) {
			tr := *m.Cases[0].Trial
			tr.Pivots = map[string]ScriptTurn{"ts-1": {Tool: "bash", Arguments: raw(`{"command":"true"}`)}}
			m.Cases[0].Trial = &tr
		},
		"pivot is a text turn": func(m *Manifest) { m.Cases[0].Trial.Pivots["ts-1"] = ScriptTurn{Text: "hello"} },
		"unknown tool":         func(m *Manifest) { m.Cases[0].Trial.Pivots["ts-1"] = ScriptTurn{Tool: "curl", Arguments: raw(`{}`)} },
		"bad step id":          func(m *Manifest) { m.Cases[0].Trial.Pivots["Bad Id"] = m.Cases[0].Trial.Pivots["ts-1"] },
		"empty table":          func(m *Manifest) { m.Cases[0].Trial.Pivots = map[string]ScriptTurn{} },
		"no script": func(m *Manifest) {
			m.Cases[0].Trial.Script = nil
			m.Cases[0].Trial.PDelta = ""
			m.Cases[0].Trial.PDeltaJustification = ""
		},
		"pivots without a pin": func(m *Manifest) { m.Pins.AttackerDigest = "" },
		"pin without any pivots": func(m *Manifest) {
			m.Cases[0].Trial.Pivots = nil
			m.Cases[0].Adversarial.CalibrationPayloadDigest, _ = CalibrationPayloadDigest(m.Cases[0].Trial.Script, m.Cases[0].Trial.PDelta)
		},
		"malformed pin": func(m *Manifest) { m.Pins.AttackerDigest = "sha256:XYZ" },
	} {
		cm, _ := pivotedManifest(t)
		// deep-copy the pivot table, which the helper builds fresh for every manifest
		mutate(&cm)
		if vs := Validate(cm, q); len(vs) == 0 {
			t.Errorf("%s: no violation", name)
		}
	}
	// a pivoted case on a non-adversarial kind is refused like any stimulus
	var compliant int = -1
	for i := range m.Cases {
		if m.Cases[i].Kind == KindCompliant {
			compliant = i
			break
		}
	}
	pm, _ := pivotedManifest(t)
	tr := *pm.Cases[compliant].Trial
	tr.Pivots = pivotFixture()
	pm.Cases[compliant].Trial = &tr
	if vs := Validate(pm, q); !hasRule(vs, "trial.invalid") {
		t.Errorf("pivots on a compliant case: %v", vs)
	}
}

func TestPivotsDigestIsTheDigestOfTheCanonicalTable(t *testing.T) {
	const canonical = `{"ai-1":{"arguments":{"command":"node -e 0"},"tool":"bash"},"ts-1":{"arguments":{"command":"rm -f secret.txt"},"tool":"bash"}}`
	sum := sha256.Sum256([]byte(canonical))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got, err := PivotsDigest(pivotFixture()); err != nil || got != want {
		t.Fatalf("PivotsDigest = %q, %v; want %q", got, err, want)
	}
	for name, table := range map[string]map[string]ScriptTurn{"absent": nil, "empty": {}, "text turn": {"ts-1": {Text: "x"}}} {
		if _, err := PivotsDigest(table); err == nil {
			t.Errorf("%s table has a digest", name)
		}
	}
}
