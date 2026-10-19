package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/routematrix"
	"tbound/supervisor/internal/runner"
)

// goTestLine renders one `go test -json` event. test may be empty for a package event.
func goTestLine(action, pkg, test string) string {
	if test == "" {
		return fmt.Sprintf(`{"Time":"2026-10-18T10:00:00Z","Action":%q,"Package":%q}`, action, pkg)
	}
	return fmt.Sprintf(`{"Time":"2026-10-18T10:00:00Z","Action":%q,"Package":%q,"Test":%q}`, action, pkg, test)
}

func goTestJSON(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

func outcomesOf(t *testing.T, docs ...[]byte) *testOutcomes {
	t.Helper()
	o := newTestOutcomes()
	for _, d := range docs {
		if err := o.add(d); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	return o
}

func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

const (
	pkgA = "tbound/supervisor/internal/alpha"
	pkgB = "tbound/supervisor/internal/beta"
	pkgC = "tbound/supervisor/internal/gamma"
)

func TestGoTestJSONOutcomes(t *testing.T) {
	// TestSubSkip and TestSubOK carry subtests; the run/result pairs of the subtests are listed by name.
	data := wellFormed(
		pkgRun{pkg: pkgA, tests: []testRun{
			{"TestPass", "pass"}, {"TestFail", "fail"}, {"TestSkip", "skip"},
			{"TestSubSkip/x", "skip"}, {"TestSubSkip", "pass"},
			{"TestSubOK/y", "pass"}, {"TestSubOK", "pass"},
			{"TestTwiceOneFails", "pass"}, {"TestTwiceOneFails", "fail"},
		}, result: "fail"},
		pkgRun{pkg: pkgB, tests: []testRun{{"TestPass", "pass"}}, result: "fail"},
		pkgRun{pkg: pkgC, tests: []testRun{{"TestPass", "pass"}, {"TestSubOK/y", "pass"}, {"TestSubOK", "pass"}}},
	)
	o := outcomesOf(t, data)
	for _, c := range []struct {
		pkg, name string
		want      bool
	}{
		{pkgA, "TestPass", false}, // its package failed
		{pkgA, "TestFail", false},
		{pkgB, "TestPass", false}, // the package failed as a whole
		{pkgC, "TestPass", true},
		{pkgC, "TestSubOK", true},
		{pkgC, "TestMissing", false},
		{"tbound/supervisor/internal/unknown", "TestPass", false},
	} {
		if got := o.passed(c.pkg, c.name); got != c.want {
			t.Errorf("passed(%s, %s) = %t (%s), want %t", c.pkg, c.name, got, o.state(c.pkg, c.name), c.want)
		}
	}
	// Each outcome on its own in a package that otherwise finishes clean.
	for name, tests := range map[string][]testRun{
		"failed":          {{"TestX", "fail"}},
		"skipped":         {{"TestX", "skip"}},
		"skipped subtest": {{"TestX/s", "skip"}, {"TestX", "pass"}},
		"mixed":           {{"TestX", "pass"}, {"TestX", "fail"}},
	} {
		res := "pass"
		if name == "failed" || name == "mixed" {
			res = "fail"
		}
		if outcomesOf(t, wellFormed(pkgRun{pkg: pkgA, tests: tests, result: res})).passed(pkgA, "TestX") {
			t.Errorf("%s: passed", name)
		}
	}
}

func TestGoTestJSONMergesFilesFailClosed(t *testing.T) {
	linux := wellFormed(passingPkg(pkgA, "TestX"))
	other := wellFormed(pkgRun{pkg: pkgA, tests: []testRun{{"TestX", "skip"}}})
	if o := outcomesOf(t, linux); !o.passed(pkgA, "TestX") {
		t.Fatal("a pass alone must pass")
	}
	if o := outcomesOf(t, linux, other); o.passed(pkgA, "TestX") {
		t.Fatal("a pass in one file and a skip in another must not pass")
	}
}

func TestGoTestJSONRefusesMalformedInput(t *testing.T) {
	for name, data := range map[string]string{
		"empty":      "",
		"not json":   "FAIL\tpkg\t0.1s\n",
		"no events":  "{}\n",
		"no action":  `{"Package":"p","Test":"TestX"}` + "\n",
		"no package": `{"Action":"pass","Test":"TestX"}` + "\n",
	} {
		if err := newTestOutcomes().add([]byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Output events carry extra text but only known members.
	out := `{"Time":"2026-10-18T10:00:00Z","Action":"output","Package":"p","Test":"TestX","Output":"ok\n","Elapsed":0.1}` + "\n"
	if err := newTestOutcomes().add([]byte(out + goTestLine("pass", "p", "TestX") + "\n")); err != nil {
		t.Errorf("output event refused: %v", err)
	}
}

func mappingDoc(obligations string) []byte {
	return []byte(`{"schema":"tbound-evq-mapping/v1","obligations":[` + obligations + `]}`)
}

const oblTests = `{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":["TestPass","TestOther"]}}`

func TestParseEVQMappingStrict(t *testing.T) {
	good := mappingDoc(oblTests + `,{"id":"EVQ-B","title":"b","tests":{},"artifacts":["guest-capability"]},{"id":"EVQ-C","title":"c","note":"why not"}`)
	m, err := parseEVQMapping(good)
	if err != nil {
		t.Fatalf("good mapping refused: %v", err)
	}
	if len(m.Obligations) != 3 || m.Obligations[0].ID != "EVQ-A" || m.Digest != sha256Of(good) || m.Obligations[2].Note != "why not" {
		t.Fatalf("mapping = %+v", m)
	}
	for name, doc := range map[string][]byte{
		"empty":                  nil,
		"no obligations":         mappingDoc(""),
		"wrong schema":           []byte(`{"schema":"x","obligations":[{"id":"EVQ-A","title":"a","note":"n"}]}`),
		"unknown member":         mappingDoc(`{"id":"EVQ-A","title":"a","note":"n","extra":1}`),
		"unknown top member":     []byte(`{"schema":"tbound-evq-mapping/v1","obligations":[{"id":"EVQ-A","title":"a","note":"n"}],"x":1}`),
		"duplicate key":          mappingDoc(`{"id":"EVQ-A","id":"EVQ-B","title":"a","note":"n"}`),
		"trailing data":          append(mappingDoc(`{"id":"EVQ-A","title":"a","note":"n"}`), []byte(`{}`)...),
		"repeated id":            mappingDoc(`{"id":"EVQ-A","title":"a","note":"n"},{"id":"EVQ-A","title":"b","note":"n"}`),
		"bad id":                 mappingDoc(`{"id":"evq a","title":"a","note":"n"}`),
		"empty id":               mappingDoc(`{"id":"","title":"a","note":"n"}`),
		"no title":               mappingDoc(`{"id":"EVQ-A","note":"n"}`),
		"unmapped without note":  mappingDoc(`{"id":"EVQ-A","title":"a"}`),
		"blank note":             mappingDoc(`{"id":"EVQ-A","title":"a","note":"  "}`),
		"mapped with a note":     mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":["TestX"]},"note":"n"}`),
		"subtest name":           mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":["TestX/sub"]}}`),
		"not a test":             mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":["Helper"]}}`),
		"bad package":            mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"../x":["TestX"]}}`),
		"empty test list":        mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":[]}}`),
		"repeated test":          mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":["TestX","TestX"]}}`),
		"bad artifact kind":      mappingDoc(`{"id":"EVQ-A","title":"a","artifacts":["Bad Kind"]}`),
		"artifact without check": mappingDoc(`{"id":"EVQ-A","title":"a","artifacts":["probe"]}`),
		"repeated artifact":      mappingDoc(`{"id":"EVQ-A","title":"a","artifacts":["guest-capability","guest-capability"]}`),
		"null obligations":       []byte(`{"schema":"tbound-evq-mapping/v1","obligations":null}`),
	} {
		if _, err := parseEVQMapping(doc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildEVQResults(t *testing.T) {
	m, err := parseEVQMapping(mappingDoc(
		`{"id":"EVQ-PASS","title":"p","tests":{"` + pkgA + `":["TestPass"]}},` +
			`{"id":"EVQ-SKIP","title":"s","tests":{"` + pkgA + `":["TestPass","TestSkip"]}},` +
			`{"id":"EVQ-FAIL","title":"f","tests":{"` + pkgB + `":["TestFail"]}},` +
			`{"id":"EVQ-MISSING","title":"m","tests":{"` + pkgA + `":["TestPass","TestGone"]}},` +
			`{"id":"EVQ-ART","title":"a","artifacts":["guest-capability"]},` +
			`{"id":"EVQ-BOTH","title":"b","tests":{"` + pkgA + `":["TestPass"]},"artifacts":["guest-capability","guest-netsink"]},` +
			`{"id":"EVQ-BAD","title":"d","artifacts":["guest-provider-exchange"]},` +
			`{"id":"EVQ-UNMAPPED","title":"u","note":"nothing yet"}`))
	if err != nil {
		t.Fatal(err)
	}
	tj := wellFormed(
		pkgRun{pkg: pkgA, tests: []testRun{{"TestPass", "pass"}, {"TestSkip", "skip"}}},
		pkgRun{pkg: pkgB, tests: []testRun{{"TestFail", "fail"}}, result: "fail"})
	capDigest := sha256Of([]byte("capture record"))
	in := evqInputs{
		Outcomes: outcomesOf(t, tj), Commit: commit, CaptureDigest: capDigest, TestJSON: sha256Of(tj),
		Artifacts:      map[string]string{"guest-capability": sha256Of([]byte("probe"))},
		ArtifactErrors: map[string]string{"guest-provider-exchange": "forged"},
	}
	results, statuses := buildEVQResults(m, in)
	if err := results.Validate(); err != nil {
		t.Fatalf("results invalid: %v", err)
	}
	want := map[string]bool{"EVQ-PASS": true, "EVQ-SKIP": false, "EVQ-FAIL": false, "EVQ-MISSING": false,
		"EVQ-ART": true, "EVQ-BOTH": false, "EVQ-BAD": false, "EVQ-UNMAPPED": false}
	if len(results) != len(want) {
		t.Fatalf("got %d results", len(results))
	}
	for i, r := range results {
		if r.ID != m.Obligations[i].ID {
			t.Errorf("result %d is %s, mapping order says %s", i, r.ID, m.Obligations[i].ID)
		}
		if r.Passed != want[r.ID] {
			t.Errorf("%s passed = %t, want %t (%v)", r.ID, r.Passed, want[r.ID], statuses[i])
		}
		if r.Passed && !evqEvidenceBoundTo(r, commit) {
			t.Errorf("%s passed without the frozen commit in evidence_ref %q", r.ID, r.EvidenceRef)
		}
		if !r.Passed && r.EvidenceRef != "" {
			t.Errorf("%s not passed but carries evidence_ref %q", r.ID, r.EvidenceRef)
		}
		if statuses[i].ID != r.ID || statuses[i].Passed != r.Passed {
			t.Errorf("%s status = %+v", r.ID, statuses[i])
		}
	}
	// The evidence reference binds the commit, the capture, the mapping, the test JSON and the artifact bytes.
	pass := results[0].EvidenceRef
	for _, part := range []string{"commit=" + commit, "evq-capture=" + capDigest, "evq-mapping=" + m.Digest, "go-test-json=" + sha256Of(tj)} {
		if !strings.Contains(pass, part) {
			t.Errorf("EVQ-PASS evidence_ref %q lacks %q", pass, part)
		}
	}
	if evqEvidenceBoundTo(results[0], strings.Repeat("0", 40)) {
		t.Error("evidence is bound to a commit it was not made for")
	}
	art := results[4].EvidenceRef
	if !strings.Contains(art, "artifact:guest-capability="+sha256Of([]byte("probe"))) {
		t.Errorf("EVQ-ART evidence_ref %q lacks the artifact digest", art)
	}
	if strings.Contains(art, "go-test-json") {
		t.Errorf("EVQ-ART needs no test JSON, evidence_ref %q", art)
	}
	for _, id := range []string{"EVQ-MISSING", "EVQ-BOTH", "EVQ-UNMAPPED", "EVQ-BAD"} {
		for _, s := range statuses {
			if s.ID == id && len(s.Reasons) == 0 {
				t.Errorf("%s not passed without a reason", id)
			}
		}
	}
	if !strings.Contains(strings.Join(statuses[6].Reasons, "\n"), "forged") {
		t.Errorf("EVQ-BAD reasons %v do not carry the validator's refusal", statuses[6].Reasons)
	}
	if !statuses[7].Unmapped || statuses[7].Note != "nothing yet" || statuses[0].Unmapped {
		t.Errorf("unmapped flags wrong: %+v %+v", statuses[0], statuses[7])
	}
}

func TestBuildEVQResultsChangedArtifactChangesEvidence(t *testing.T) {
	m, _ := parseEVQMapping(mappingDoc(`{"id":"EVQ-ART","title":"a","artifacts":["guest-capability"]}`))
	ref := func(content string) string {
		r, _ := buildEVQResults(m, evqInputs{Outcomes: newTestOutcomes(), Commit: commit,
			Artifacts: map[string]string{"guest-capability": sha256Of([]byte(content))}})
		return r[0].EvidenceRef
	}
	if ref("a") == ref("b") || ref("a") != ref("a") {
		t.Fatal("evidence_ref must follow the artifact digest")
	}
}

// ---- the evq command ----

var (
	guestBinary = fixtureDigest("tbound-profile built from the freeze commit")
	profilePin  = fixtureDigest("profile")
	sourcePin   = fixtureDigest("source")
)

type evqEnv struct {
	dir, mapping, config, capture string
	mappingData                   []byte
	out                           bytes.Buffer
}

func newEVQEnv(t *testing.T) *evqEnv {
	t.Helper()
	e := &evqEnv{dir: t.TempDir()}
	e.mapping = filepath.Join(e.dir, "mapping.json")
	e.mappingData = mappingDoc(`{"id":"EVQ-T","title":"t","tests":{"` + pkgA + `":["TestPass"]}},` +
		`{"id":"EVQ-G","title":"g","artifacts":["guest-capability"]},{"id":"EVQ-U","title":"u","note":"no evidence yet"}`)
	if err := os.WriteFile(e.mapping, e.mappingData, 0o600); err != nil {
		t.Fatal(err)
	}
	e.config = filepath.Join(e.dir, "run.json")
	cfg := `{"freeze_commit":"` + commit + `","pins":{"profile_hash":"` + profilePin + `","frozen_source_digest":"` + sourcePin + `"}}`
	if err := os.WriteFile(e.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	e.capture = filepath.Join(e.dir, "capture")
	if err := os.Mkdir(e.capture, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCapture(t, e.capture, capSpec{pkgs: []pkgRun{passingPkg(pkgA, "TestPass", "TestOther")}, mapping: e.mappingData})
	return e
}

func (e *evqEnv) run(t *testing.T, args ...string) error {
	t.Helper()
	a := &app{out: &e.out, errOut: &bytes.Buffer{}}
	return a.run(append([]string{"evq"}, args...))
}

func (e *evqEnv) base(out string) []string {
	return []string{"--mapping", e.mapping, "--config", e.config, "--capture", e.capture, "--out", out}
}

// capabilityChain is a delegation chain that enables cpu, memory and pids at every level to the cells cgroup.
func capabilityChain() []guestevidence.CgroupLevel {
	var chain []guestevidence.CgroupLevel
	for _, p := range guestevidence.CapabilityChain {
		chain = append(chain, guestevidence.CgroupLevel{Path: p, Type: "domain", Controllers: []string{"cpu", "memory", "pids"}, SubtreeControl: []string{"cpu", "memory", "pids"}})
	}
	return chain
}

func capabilityCellsProbe() guestevidence.CellsProbe {
	p := guestevidence.CellsProbe{UID: guestevidence.ServiceUID, GID: guestevidence.ServiceGID, ChildCreated: true, ChildRemoved: true}
	for _, name := range guestevidence.CellsProbeFiles {
		p.Files = append(p.Files, guestevidence.CellsProbeFile{Name: name, Exists: true, Writable: true})
	}
	return p
}

func capabilityArtifact(t *testing.T, mutate func(*guestevidence.Capability)) []byte {
	t.Helper()
	c := guestevidence.Capability{KernelRelease: "6.8.0-synthetic", Arch: "amd64",
		Cgroup: guestevidence.CapabilityCgroup{Mount: "/sys/fs/cgroup", FSType: "cgroup2", Controllers: []string{"cpu", "memory", "pids"},
			ServiceCgroup: guestevidence.ServiceCgroup, ServiceCgroupType: "domain", KillInterface: true,
			Chain: capabilityChain(), CellsProbe: capabilityCellsProbe()},
		Landlock: guestevidence.CapabilityLandlock{ABI: 8, ErrataSupported: true, Errata: 1},
		Seccomp:  guestevidence.CapabilitySeccomp{FilterInstalled: true, Mode: 2, ProfileSHA256: fixtureDigest("seccomp")}}
	if mutate != nil {
		mutate(&c)
	}
	data, err := guestevidence.Encode(guestevidence.KindCapability, commit, guestBinary, c)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEVQCommandWritesResultsRunJSONAccepts(t *testing.T) {
	e := newEVQEnv(t)
	probe := filepath.Join(e.dir, "capability.json")
	if err := os.WriteFile(probe, capabilityArtifact(t, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(e.dir, "evq_results.json")
	if err := e.run(t, append(e.base(out), "--guest-binary-sha256", guestBinary, "--artifact", "guest-capability="+probe)...); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	results, err := runner.ParseEVQResults(data)
	if err != nil {
		t.Fatalf("ParseEVQResults: %v", err)
	}
	got := map[string]bool{}
	for _, r := range results {
		got[r.ID] = r.Passed
		if r.Passed && !evqEvidenceBoundTo(r, commit) {
			t.Errorf("%s passed without the commit: %q", r.ID, r.EvidenceRef)
		}
	}
	if !got["EVQ-T"] || !got["EVQ-G"] || got["EVQ-U"] {
		t.Fatalf("results = %v", got)
	}
	capData, _ := os.ReadFile(filepath.Join(e.capture, evqCaptureRecord))
	if !strings.Contains(results[0].EvidenceRef, "evq-capture="+sha256Of(capData)) {
		t.Errorf("evidence_ref %q does not name the capture record", results[0].EvidenceRef)
	}
	// run.json takes the file by its digest and the IDs from the same mapping.
	res, digest, err := loadEVQResults(pinnedFile{Path: out, SHA256: sha256Of(data)})
	if err != nil || len(res) != 3 || digest != sha256Of(data) {
		t.Fatalf("loadEVQResults: %v %v %s", res, err, digest)
	}
	if err := (runner.Attest{EVQ: []string{"EVQ-T", "EVQ-G", "EVQ-U"}, EVQResults: res,
		Pins: runner.ReceiptPins{ProfileHash: "p", ContainmentProfileDigest: "c", FrozenSourceDigest: "f"}}).Validate(); err != nil {
		t.Fatalf("Attest.Validate: %v", err)
	}
	text := e.out.String()
	for _, want := range []string{"EVQ-T passed", "EVQ-G passed", "EVQ-U not_passed unmapped", "note: no evidence yet",
		`evq_obligations ["EVQ-T","EVQ-G","EVQ-U"]`, "results_sha256 " + sha256Of(data), "passed 2", "not_passed 1", "commit " + commit} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestEVQCommandDeterministicAndNeverOverwrites(t *testing.T) {
	e := newEVQEnv(t)
	o1, o2 := filepath.Join(e.dir, "o1.json"), filepath.Join(e.dir, "o2.json")
	for _, o := range []string{o1, o2} {
		if err := e.run(t, e.base(o)...); err != nil {
			t.Fatal(err)
		}
	}
	d1, _ := os.ReadFile(o1)
	d2, _ := os.ReadFile(o2)
	if !bytes.Equal(d1, d2) {
		t.Fatal("same inputs gave different bytes")
	}
	if err := e.run(t, e.base(o1)...); err == nil {
		t.Fatal("an existing results file was overwritten")
	}
	// Without the artifact, the artifact obligation is not passed.
	var r struct{ Obligations []runner.EVQResult }
	if err := json.Unmarshal(d1, &r); err != nil {
		t.Fatal(err)
	}
	for _, ob := range r.Obligations {
		if ob.ID == "EVQ-G" && ob.Passed {
			t.Fatal("artifact obligation passed without the artifact")
		}
	}
}

// A supplied guest artifact that does not pass its validator never passes the obligation, and the
// report says why. The command itself still writes the results: the obligation is not passed.
func TestEVQCommandRefusesInvalidGuestArtifacts(t *testing.T) {
	e := newEVQEnv(t)
	valid := capabilityArtifact(t, nil)
	otherCommit := strings.Repeat("d", 40)
	wrongCommit, err := guestevidence.Encode(guestevidence.KindCapability, otherCommit, guestBinary, guestevidence.Capability{})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"empty file":          {},
		"not json":            []byte("PASS cgroup-v2\n"),
		"probe output":        []byte(`{"cgroup-v2":"PASS"}`),
		"wrong commit":        wrongCommit,
		"other binary":        bytes.Replace(valid, []byte(guestBinary), []byte(fixtureDigest("another binary")), 1),
		"landlock abi 1":      capabilityArtifact(t, func(c *guestevidence.Capability) { c.Landlock.ABI = 1 }),
		"no cgroup kill":      capabilityArtifact(t, func(c *guestevidence.Capability) { c.Cgroup.KillInterface = false }),
		"no memory":           capabilityArtifact(t, func(c *guestevidence.Capability) { c.Cgroup.Controllers = []string{"cpu", "pids"} }),
		"no seccomp filter":   capabilityArtifact(t, func(c *guestevidence.Capability) { c.Seccomp.FilterInstalled = false }),
		"unknown member":      bytes.Replace(valid, []byte(`"kind"`), []byte(`"extra": 1, "kind"`), 1),
		"duplicate key":       bytes.Replace(valid, []byte(`"kind"`), []byte(`"kind": "guest-capability", "kind"`), 1),
		"trailing data":       append(append([]byte{}, valid...), []byte(`{}`)...),
		"another kind's file": mustEncode(t, guestevidence.KindNetSink, guestevidence.NetSink{}),
	} {
		probe := filepath.Join(e.dir, "a.json")
		if err := os.WriteFile(probe, data, 0o600); err != nil {
			t.Fatal(err)
		}
		e.out.Reset()
		out := filepath.Join(e.dir, strings.ReplaceAll(name, " ", "_")+".json")
		if err := e.run(t, append(e.base(out), "--guest-binary-sha256", guestBinary, "--artifact", "guest-capability="+probe)...); err != nil {
			t.Errorf("%s: command failed: %v", name, err)
			continue
		}
		text := e.out.String()
		if strings.Contains(text, "EVQ-G passed") || !strings.Contains(text, "artifact guest-capability: refused") {
			t.Errorf("%s: EVQ-G was not refused:\n%s", name, text)
		}
	}
	// The valid one passes, to show the cases above fail for their own reason.
	probe := filepath.Join(e.dir, "valid.json")
	if err := os.WriteFile(probe, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	e.out.Reset()
	if err := e.run(t, append(e.base(filepath.Join(e.dir, "ok.json")), "--guest-binary-sha256", guestBinary, "--artifact", "guest-capability="+probe)...); err != nil || !strings.Contains(e.out.String(), "EVQ-G passed") {
		t.Fatalf("valid artifact: %v\n%s", err, e.out.String())
	}
}

func mustEncode(t *testing.T, kind string, data any) []byte {
	t.Helper()
	b, err := guestevidence.Encode(kind, commit, guestBinary, data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEVQCommandRefusals(t *testing.T) {
	e := newEVQEnv(t)
	good := capabilityArtifact(t, nil)
	probe := filepath.Join(e.dir, "probe.json")
	if err := os.WriteFile(probe, good, 0o600); err != nil {
		t.Fatal(err)
	}
	out := func(n string) string { return filepath.Join(e.dir, n) }
	other := filepath.Join(e.dir, "other.json")
	if err := os.WriteFile(other, []byte(strings.Replace(string(e.mappingData), "EVQ-T", "EVQ-X", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongCommitCfg := filepath.Join(e.dir, "other-run.json")
	if err := os.WriteFile(wrongCommitCfg, []byte(`{"freeze_commit":"`+strings.Repeat("e", 40)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"no mapping":                         {"--config", e.config, "--capture", e.capture, "--out", out("a")},
		"no config":                          {"--mapping", e.mapping, "--capture", e.capture, "--out", out("b")},
		"no capture":                         {"--mapping", e.mapping, "--config", e.config, "--out", out("c")},
		"no out":                             {"--mapping", e.mapping, "--config", e.config, "--capture", e.capture},
		"positional":                         append(e.base(out("d")), "extra"),
		"old test-json":                      append(e.base(out("e")), "--test-json", filepath.Join(e.capture, evqCaptureTestJSON)),
		"artifact without the binary digest": append(e.base(out("f")), "--artifact", "guest-capability="+probe),
		"artifact with a bad binary digest":  append(e.base(out("g")), "--guest-binary-sha256", "deadbeef", "--artifact", "guest-capability="+probe),
		"unknown kind":                       append(e.base(out("h")), "--guest-binary-sha256", guestBinary, "--artifact", "typo="+probe),
		"kind not in the mapping":            append(e.base(out("i")), "--guest-binary-sha256", guestBinary, "--artifact", "guest-netsink="+probe),
		"repeated kind":                      append(e.base(out("j")), "--guest-binary-sha256", guestBinary, "--artifact", "guest-capability="+probe, "--artifact", "guest-capability="+probe),
		"artifact no equals":                 append(e.base(out("k")), "--guest-binary-sha256", guestBinary, "--artifact", "guest-capability"),
		"missing mapping":                    {"--mapping", filepath.Join(e.dir, "none"), "--config", e.config, "--capture", e.capture, "--out", out("l")},
		"mapping that is not the commit's":   {"--mapping", other, "--config", e.config, "--capture", e.capture, "--out", out("m")},
		"run.json freezes another commit":    {"--mapping", e.mapping, "--config", wrongCommitCfg, "--capture", e.capture, "--out", out("n")},
		"missing capture":                    {"--mapping", e.mapping, "--config", e.config, "--capture", filepath.Join(e.dir, "none"), "--out", out("o")},
		"missing config":                     {"--mapping", e.mapping, "--config", filepath.Join(e.dir, "none"), "--capture", e.capture, "--out", out("p")},
	} {
		err := e.run(t, args...)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		switch name {
		case "no mapping", "no config", "no capture", "no out", "positional", "old test-json", "artifact without the binary digest",
			"artifact with a bad binary digest", "unknown kind", "kind not in the mapping", "repeated kind", "artifact no equals":
			if !errors.Is(err, errUsage) {
				t.Errorf("%s: %v is not a usage error", name, err)
			}
		}
	}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"} {
		if _, err := os.Stat(out(n)); err == nil {
			t.Errorf("a refused run wrote %s", n)
		}
	}
}

// ---- the pinned mapping ----

// repoRoot is the repository root seen from cmd/tbound-run.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

const modulePrefix = "tbound/supervisor/"

// testExists reports whether package pkg (a module import path) declares a top-level test name
// in one of its *_test.go files.
func testExists(t *testing.T, root, pkg, name string) bool {
	t.Helper()
	rel, ok := strings.CutPrefix(pkg, modulePrefix)
	if !ok {
		return false
	}
	dir := filepath.Join(root, "supervisor", filepath.FromSlash(rel))
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil || len(files) == 0 {
		return false
	}
	re := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(t \*testing\.T\)`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if re.Match(data) {
			return true
		}
	}
	return false
}

func readRepoFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	// A checkout with CRLF conversion would change the pinned digest; the pin is over LF bytes.
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

const (
	evqMappingPath  = "infra/conformance/evq-mapping.json"
	routeMatrixPath = "infra/conformance/route-matrix.json"
)

// The obligations that have no implementable evidence yet (none). Mapping one of them is a deliberate
// change that edits this list.
var wantUnmappedEVQ []string

// The obligations that need a guest artifact, and so are not passed from tests alone.
var wantGuestEVQ = []string{"EVQ-LINUX-CAPABILITY-GUEST", "EVQ-NETWORK-SINK-GUEST", "EVQ-ORACLE-PROCESS", "EVQ-PROVIDER-REAL-EXCHANGE", "EVQ-SURFACE-ADAPTER-EQUIVALENCE"}

func TestPinnedEVQMapping(t *testing.T) {
	m, err := parseEVQMapping(readRepoFile(t, evqMappingPath))
	if err != nil {
		t.Fatalf("pinned mapping: %v", err)
	}
	var unmapped []string
	guest := map[string][]string{}
	for _, ob := range m.Obligations {
		if len(ob.Artifacts) > 0 {
			guest[ob.ID] = slices.Sorted(slices.Values(ob.Artifacts))
		}
		if ob.unmapped() {
			unmapped = append(unmapped, ob.ID)
			continue
		}
		for pkg, names := range ob.Tests {
			for _, n := range names {
				if !testExists(t, repoRoot(t), pkg, n) {
					t.Errorf("%s maps %s.%s, which is not a top-level test in the repository", ob.ID, pkg, n)
				}
			}
		}
	}
	slices.Sort(unmapped)
	if !slices.Equal(unmapped, wantUnmappedEVQ) {
		t.Errorf("unmapped obligations = %v, want %v", unmapped, wantUnmappedEVQ)
	}
	wantGuest := map[string][]string{
		"EVQ-LINUX-CAPABILITY-GUEST":      {guestevidence.KindCapability},
		"EVQ-NETWORK-SINK-GUEST":          {guestevidence.KindNetSink, guestevidence.KindLandlockConnectTests},
		"EVQ-ORACLE-PROCESS":              {guestevidence.KindProcOracleTests},
		"EVQ-PROVIDER-REAL-EXCHANGE":      {guestevidence.KindProviderExchange},
		"EVQ-SURFACE-ADAPTER-EQUIVALENCE": {guestevidence.KindAdapterTests},
	}
	if !reflect.DeepEqual(guest, wantGuest) {
		t.Errorf("guest artifacts = %v, want %v", guest, wantGuest)
	}
	// Every kind a guest validator exists for is used, and the process oracle's real-cgroup tests are
	// required from the guest, not from a capture host where they skip.
	var kinds []string
	for _, ks := range guest {
		kinds = append(kinds, ks...)
	}
	// A route cell of the pinned matrix names its own guest artifact, so the route kinds are used there.
	routeMatrix, err := parseRouteMatrix(readRepoFile(t, routeMatrixPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range routeMatrix.Cells {
		if c.GuestArtifact != "" {
			kinds = append(kinds, c.GuestArtifact)
		}
	}
	slices.Sort(kinds)
	if want := slices.Sorted(slices.Values(guestevidence.Kinds())); !slices.Equal(kinds, want) {
		t.Errorf("artifact kinds used = %v, validators exist for %v", kinds, want)
	}
	for _, ob := range m.Obligations {
		if ob.ID == "EVQ-ORACLE-PROCESS" {
			for _, n := range ob.Tests["tbound/supervisor/internal/procoracle"] {
				if strings.HasPrefix(n, "TestReal") {
					t.Errorf("%s is required of the capture host, but it skips without delegated cgroups", n)
				}
			}
		}
	}
	if len(m.Obligations)-len(unmapped) < 15 {
		t.Errorf("only %d obligations are mapped", len(m.Obligations)-len(unmapped))
	}
}

// With no test results at all the pinned mapping passes nothing: the generator fails closed.
func TestPinnedEVQMappingFailsClosedWithoutEvidence(t *testing.T) {
	m, err := parseEVQMapping(readRepoFile(t, evqMappingPath))
	if err != nil {
		t.Fatal(err)
	}
	results, _ := buildEVQResults(m, evqInputs{Outcomes: newTestOutcomes(), Commit: commit})
	for _, r := range results {
		if r.Passed {
			t.Errorf("%s passed without evidence", r.ID)
		}
	}
}

// pinnedTestsDoc is a complete, well-formed test stream in which every mapped test passed.
func pinnedTestsDoc(t *testing.T, m evqMapping) []byte {
	t.Helper()
	byPkg := map[string][]string{}
	for _, ob := range m.Obligations {
		for pkg, names := range ob.Tests {
			byPkg[pkg] = append(byPkg[pkg], names...)
		}
	}
	var pkgs []pkgRun
	for _, pkg := range slices.Sorted(func(yield func(string) bool) {
		for p := range byPkg {
			if !yield(p) {
				return
			}
		}
	}) {
		pkgs = append(pkgs, passingPkg(pkg, slices.Compact(slices.Sorted(slices.Values(byPkg[pkg])))...))
	}
	return wellFormed(pkgs...)
}

// With every mapped test passing and no guest artifact, exactly the unmapped obligations and the ones
// that need the guest stay not passed.
func TestPinnedEVQMappingPassesOnlyMappedObligations(t *testing.T) {
	m, err := parseEVQMapping(readRepoFile(t, evqMappingPath))
	if err != nil {
		t.Fatal(err)
	}
	tj := pinnedTestsDoc(t, m)
	results, _ := buildEVQResults(m, evqInputs{Outcomes: outcomesOf(t, tj), Commit: commit, CaptureDigest: sha256Of(tj), TestJSON: sha256Of(tj)})
	var notPassed []string
	for _, r := range results {
		if !r.Passed {
			notPassed = append(notPassed, r.ID)
		}
	}
	slices.Sort(notPassed)
	want := slices.Sorted(func(yield func(string) bool) {
		for _, id := range wantUnmappedEVQ {
			if !yield(id) {
				return
			}
		}
		for _, id := range wantGuestEVQ {
			if !yield(id) {
				return
			}
		}
	})
	if !slices.Equal(notPassed, want) {
		t.Errorf("not passed = %v, want %v", notPassed, want)
	}
	// With every artifact accepted, only the unmapped obligations stay not passed.
	arts := map[string]string{}
	for _, k := range guestevidence.Kinds() {
		arts[k] = fixtureDigest("artifact " + k)
	}
	results, _ = buildEVQResults(m, evqInputs{Outcomes: outcomesOf(t, tj), Commit: commit, CaptureDigest: sha256Of(tj), TestJSON: sha256Of(tj), Artifacts: arts})
	notPassed = nil
	for _, r := range results {
		if !r.Passed {
			notPassed = append(notPassed, r.ID)
		}
	}
	slices.Sort(notPassed)
	if !slices.Equal(notPassed, wantUnmappedEVQ) {
		t.Errorf("not passed with all artifacts = %v, want %v", notPassed, wantUnmappedEVQ)
	}
}

// ---- route report ----

func routeCell(route, class, treatment, status, reason string, ev ...string) string {
	var evs []string
	for i := 0; i+1 < len(ev); i += 2 {
		evs = append(evs, fmt.Sprintf(`{"kind":%q,"test_ref":%q}`, ev[i], ev[i+1]))
	}
	return fmt.Sprintf(`{"route":%q,"class":%q,"treatment":%q,"status":%q,"reason":%q,"evidence":[%s]}`,
		route, class, treatment, status, reason, strings.Join(evs, ","))
}

func routeDoc(classes string, cells ...string) []byte {
	return []byte(`{"schema":"tbound-route-matrix/v1","classes":[` + classes + `],"cells":[` + strings.Join(cells, ",") + `]}`)
}

// Each reference is in its own package, so that one package failing leaves the other cells alone.
const (
	refDR1 = modulePrefix + "internal/r1.TestDeclaredAttempt"
	refDR2 = modulePrefix + "internal/r2.TestDeclaredSuccess"
	refBV1 = modulePrefix + "internal/r3.TestBashAttempt"
	refBV2 = modulePrefix + "internal/r4.TestBashSuccess"
	refRT1 = modulePrefix + "internal/r5.TestRuntimeAttempt"
	refRT2 = modulePrefix + "internal/r6.TestRuntimeSuccess"
	refPB  = modulePrefix + "internal/r7.TestBrokerDisclosure"
)

// fullRouteDoc covers the one class on all four routes with every required evidence kind.
func fullRouteDoc() []byte {
	return routeDoc(`"c1"`,
		routeCell("declared-read", "c1", "unreachable", "Covered", "", "AttemptedRead", refDR1, "SuccessfulRead", refDR2),
		routeCell("bash-view", "c1", "unreachable", "Covered", "", "AttemptedRead", refBV1, "SuccessfulRead", refBV2),
		routeCell("direct-runtime", "c1", "unreachable", "Covered", "", "AttemptedRead", refRT1, "SuccessfulRead", refRT2),
		routeCell("provider-broker", "c1", "proposal-denied", "Covered", "", "ProviderChannelDisclosure", refPB))
}

func splitRef(ref string) (string, string) {
	i := strings.LastIndex(ref, ".Test")
	return ref[:i], ref[i+1:]
}

// routeTests renders a complete stream in which each reference has the given action (pass when action
// is empty) and a reference listed in absent has no events at all.
func routeTests(refs []string, action map[string]string, absent ...string) []byte {
	var pkgs []pkgRun
	for _, ref := range refs {
		if slices.Contains(absent, ref) {
			continue
		}
		pkg, name := splitRef(ref)
		act := action[ref]
		if act == "" {
			act = "pass"
		}
		res := "pass"
		if act == "fail" {
			res = "fail"
		}
		pkgs = append(pkgs, pkgRun{pkg: pkg, tests: []testRun{{name, act}}, result: res})
	}
	return wellFormed(pkgs...)
}

func passAll(refs ...string) []byte { return routeTests(refs, nil) }

func TestParseRouteMatrixStrict(t *testing.T) {
	m, err := parseRouteMatrix(fullRouteDoc())
	if err != nil {
		t.Fatalf("good matrix refused: %v", err)
	}
	if len(m.Cells) != 4 || len(m.Classes) != 1 || m.Cells[0].Route != routematrix.RouteDeclaredRead ||
		m.Cells[3].Evidence[0].Kind != routematrix.EvidenceProviderChannelDisclosure {
		t.Fatalf("matrix = %+v", m)
	}
	good := string(fullRouteDoc())
	for name, doc := range map[string][]byte{
		"empty":         nil,
		"wrong schema":  []byte(strings.Replace(good, "tbound-route-matrix/v1", "x", 1)),
		"unknown field": []byte(strings.Replace(good, `"classes"`, `"extra":1,"classes"`, 1)),
		"duplicate key": []byte(strings.Replace(good, `"classes"`, `"classes":["c1"],"classes"`, 1)),
		"trailing data": append(fullRouteDoc(), '{', '}'),
		"cell unknown":  []byte(strings.Replace(good, `"reason"`, `"extra":1,"reason"`, 1)),
	} {
		if _, err := parseRouteMatrix(doc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

var allRouteRefs = []string{refDR1, refDR2, refBV1, refBV2, refRT1, refRT2, refPB}

func TestBuildRouteReportComplete(t *testing.T) {
	m, err := parseRouteMatrix(fullRouteDoc())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := buildRouteReport(m, outcomesOf(t, passAll(allRouteRefs...)))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Complete || rep.Total != 4 || rep.ByStatus[routematrix.StatusCovered] != 4 || len(rep.Reasons) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestBuildRouteReportFailsClosed(t *testing.T) {
	m, err := parseRouteMatrix(fullRouteDoc())
	if err != nil {
		t.Fatal(err)
	}
	// Each ref in turn failing, skipped or absent: the report is incomplete and its cell is not Covered.
	for _, ref := range allRouteRefs {
		for _, action := range []string{"fail", "skip", "absent"} {
			var o *testOutcomes
			if action == "absent" {
				o = outcomesOf(t, routeTests(allRouteRefs, nil, ref))
			} else {
				o = outcomesOf(t, routeTests(allRouteRefs, map[string]string{ref: action}))
			}
			rep, err := buildRouteReport(m, o)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Complete {
				t.Errorf("%s %q: complete", ref, action)
			}
			if rep.ByStatus[routematrix.StatusCovered] != 3 {
				t.Errorf("%s %q: covered = %d, want 3", ref, action, rep.ByStatus[routematrix.StatusCovered])
			}
			if !strings.Contains(strings.Join(rep.Reasons, "\n"), ref) {
				t.Errorf("%s %q: reasons %v do not name the ref", ref, action, rep.Reasons)
			}
		}
	}
	// A stream whose packages never finished, or that holds only result events, covers nothing.
	synthetic := []string{}
	for _, r := range allRouteRefs {
		pkg, name := splitRef(r)
		synthetic = append(synthetic, goTestLine("pass", pkg, name))
	}
	rep, err := buildRouteReport(m, outcomesOf(t, goTestJSON(synthetic...)))
	if err != nil || rep.Complete || rep.ByStatus[routematrix.StatusCovered] != 0 {
		t.Fatalf("synthetic pass-only stream: %+v %v", rep, err)
	}
}

func TestBuildRouteReportNonCoveredCellsStayAsDeclared(t *testing.T) {
	doc := routeDoc(`"c1"`,
		routeCell("declared-read", "c1", "unreachable", "Missing", "no success test", "AttemptedRead", refDR1),
		routeCell("bash-view", "c1", "unreachable", "Pending_Guest", "needs the guest"),
		routeCell("direct-runtime", "c1", "unreachable", "Missing", ""),
		routeCell("provider-broker", "c1", "proposal-denied", "Missing", "out of H1"))
	m, err := parseRouteMatrix(doc)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := buildRouteReport(m, outcomesOf(t, passAll(refDR1)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Complete || rep.ByStatus[routematrix.StatusMissing] != 3 || rep.ByStatus[routematrix.StatusPendingGuest] != 1 {
		t.Fatalf("report = %+v", rep)
	}
	// The one present attempt resolves, so no reference is reported missing.
	if strings.Contains(strings.Join(rep.Reasons, "\n"), "not found") {
		t.Fatalf("reasons = %v", rep.Reasons)
	}
}

func TestBuildRouteReportRefusesInvalidMatrix(t *testing.T) {
	m, err := parseRouteMatrix(routeDoc(`"c1"`,
		routeCell("declared-read", "c1", "unreachable", "Missing", "")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildRouteReport(m, newTestOutcomes()); !errors.Is(err, routematrix.ErrMissingCell) {
		t.Fatalf("err = %v, want ErrMissingCell", err)
	}
}

type routeEnv struct {
	dir, matrix, config, capture string
	out                          bytes.Buffer
}

func newRouteEnv(t *testing.T, matrix []byte, pkgs []pkgRun) *routeEnv {
	t.Helper()
	e := &routeEnv{dir: t.TempDir()}
	e.matrix, e.config, e.capture = filepath.Join(e.dir, "matrix.json"), filepath.Join(e.dir, "run.json"), filepath.Join(e.dir, "capture")
	if err := os.WriteFile(e.matrix, matrix, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.config, []byte(`{"freeze_commit":"`+commit+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.capture, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCapture(t, e.capture, capSpec{pkgs: pkgs, matrix: matrix})
	return e
}

func (e *routeEnv) run(args ...string) error {
	a := &app{out: &e.out, errOut: &bytes.Buffer{}}
	return a.run(append([]string{"route-report"}, args...))
}

func (e *routeEnv) base(out string) []string {
	return []string{"--matrix", e.matrix, "--config", e.config, "--capture", e.capture, "--out", out}
}

func passingRoutePkgs(refs ...string) []pkgRun {
	var pkgs []pkgRun
	for _, r := range refs {
		pkg, name := splitRef(r)
		pkgs = append(pkgs, passingPkg(pkg, name))
	}
	return pkgs
}

func TestRouteReportCommandIsAcceptedByRunConfig(t *testing.T) {
	e := newRouteEnv(t, fullRouteDoc(), passingRoutePkgs(allRouteRefs...))
	out := filepath.Join(e.dir, "route_report.json")
	if err := e.run(e.base(out)...); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	cov, err := loadRoute(out, commit)
	if err != nil {
		t.Fatalf("loadRoute: %v", err)
	}
	if !cov.Complete || cov.Digest != sha256Of(data) {
		t.Fatalf("coverage = %+v", cov)
	}
	if _, err := loadRoute(out, strings.Repeat("f", 40)); err == nil {
		t.Fatal("a report made for one commit was accepted for another")
	}
	var rep routematrix.Report
	if err := json.Unmarshal(data, &rep); err != nil || rep.Total != 4 {
		t.Fatalf("report json: %v %+v", err, rep)
	}
	m, _ := parseRouteMatrix(fullRouteDoc())
	capData, _ := os.ReadFile(filepath.Join(e.capture, evqCaptureRecord))
	if want := routeBinding(commit, sha256Of(capData), routematrix.Digest(m)); rep.Binding != want {
		t.Fatalf("binding = %q, want %q", rep.Binding, want)
	}
	text := e.out.String()
	for _, want := range []string{"complete true", "matrix_digest " + routematrix.Digest(m),
		"route_report_sha256 " + sha256Of(data), "evq_capture " + sha256Of(capData)} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	// Deterministic, and an existing report is not overwritten.
	out2 := filepath.Join(e.dir, "again.json")
	if err := e.run(e.base(out2)...); err != nil {
		t.Fatal(err)
	}
	if d2, _ := os.ReadFile(out2); !bytes.Equal(data, d2) {
		t.Fatal("same inputs gave different bytes")
	}
	if err := e.run(e.base(out)...); err == nil {
		t.Fatal("an existing report was overwritten")
	}
}

func TestRouteReportWithoutBindingIsRefusedByTheRun(t *testing.T) {
	rep, err := json.Marshal(routematrix.Report{Total: 4, Complete: true, ByStatus: map[routematrix.Status]int{routematrix.StatusCovered: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseRouteReport(rep, commit); err == nil {
		t.Fatal("an unbound route report was accepted")
	}
}

func TestRouteReportCommandIncompleteIsStillAccepted(t *testing.T) {
	e := newRouteEnv(t, fullRouteDoc(), []pkgRun{passingPkg(pkgA, "TestOther")})
	out := filepath.Join(e.dir, "route_report.json")
	if err := e.run(e.base(out)...); err != nil {
		t.Fatal(err)
	}
	cov, err := loadRoute(out, commit)
	if err != nil || cov.Complete {
		t.Fatalf("coverage = %+v, %v", cov, err)
	}
	if !strings.Contains(e.out.String(), "complete false") {
		t.Fatalf("output = %s", e.out.String())
	}
}

func TestRouteReportCommandRefusals(t *testing.T) {
	e := newRouteEnv(t, fullRouteDoc(), passingRoutePkgs(refDR1))
	other := filepath.Join(e.dir, "other.json")
	if err := os.WriteFile(other, []byte(strings.Replace(string(fullRouteDoc()), `"c1"`, `"c2"`, 2)), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"no matrix":                       {"--config", e.config, "--capture", e.capture, "--out", filepath.Join(e.dir, "a")},
		"no config":                       {"--matrix", e.matrix, "--capture", e.capture, "--out", filepath.Join(e.dir, "b")},
		"no capture":                      {"--matrix", e.matrix, "--config", e.config, "--out", filepath.Join(e.dir, "c")},
		"no out":                          {"--matrix", e.matrix, "--config", e.config, "--capture", e.capture},
		"old test-json":                   append(e.base(filepath.Join(e.dir, "d")), "--test-json", filepath.Join(e.capture, evqCaptureTestJSON)),
		"bad matrix":                      {"--matrix", filepath.Join(e.capture, evqCaptureTestJSON), "--config", e.config, "--capture", e.capture, "--out", filepath.Join(e.dir, "e")},
		"matrix that is not the commit's": {"--matrix", other, "--config", e.config, "--capture", e.capture, "--out", filepath.Join(e.dir, "f")},
	} {
		if err := e.run(args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		if _, err := os.Stat(filepath.Join(e.dir, n)); err == nil {
			t.Errorf("a refused run wrote %s", n)
		}
	}
}

func TestPinnedRouteMatrix(t *testing.T) {
	m, err := parseRouteMatrix(readRepoFile(t, routeMatrixPath))
	if err != nil {
		t.Fatalf("pinned matrix: %v", err)
	}
	if err := routematrix.Validate(m); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := routematrix.RefsExist(m, func(ref string) bool {
		i := strings.LastIndex(ref, ".Test")
		return i > 0 && testExists(t, repoRoot(t), ref[:i], ref[i+1:])
	}); err != nil {
		t.Fatalf("pinned matrix: %v", err)
	}
	var refs []string
	guest := guestRouteArtifacts{Digests: map[string]string{}}
	for _, c := range m.Cells {
		switch {
		case c.Status == routematrix.StatusCovered && c.GuestArtifact == "":
		case c.Status == routematrix.StatusPendingGuest && c.GuestArtifact != "":
			guest.Digests[c.GuestArtifact] = sha256Of([]byte(c.GuestArtifact))
		default:
			t.Errorf("cell %s/%s is declared %s with guest artifact %q", c.Route, c.Class, c.Status, c.GuestArtifact)
		}
		for _, e := range c.Evidence {
			refs = append(refs, e.TestRef)
		}
	}
	// With all its tests passing but no guest artifact, the production cells are not covered.
	rep, err := buildRouteReport(m, outcomesOf(t, passAll(refs...)))
	if err != nil || rep.Complete || rep.ByStatus[routematrix.StatusPendingGuest] != len(guest.Digests) {
		t.Fatalf("without guest artifacts: %+v %v", rep, err)
	}
	// With all its tests passing and every guest artifact, every cell has its evidence and the report is complete.
	rep, err = buildRouteReportWithGuest(m, outcomesOf(t, passAll(refs...)), guest)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != len(m.Cells) || rep.Total != 4*len(m.Classes) || !rep.Complete || rep.ByStatus[routematrix.StatusCovered] != rep.Total {
		t.Fatalf("report = %+v", rep)
	}
	// Any one of its tests not passing, and the report is not complete.
	for _, ref := range refs {
		rep, err := buildRouteReportWithGuest(m, outcomesOf(t, routeTests(refs, map[string]string{ref: "skip"})), guest)
		if err != nil || rep.Complete {
			t.Errorf("%s skipped: %+v %v", ref, rep, err)
		}
	}
	// And with no test results, never complete.
	rep, err = buildRouteReport(m, newTestOutcomes())
	if err != nil || rep.Complete {
		t.Fatalf("empty evidence: %+v %v", rep, err)
	}
}
