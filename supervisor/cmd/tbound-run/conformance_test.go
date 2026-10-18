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
	"regexp"
	"slices"
	"strings"
	"testing"

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
)

func TestGoTestJSONOutcomes(t *testing.T) {
	data := goTestJSON(
		goTestLine("run", pkgA, "TestPass"), goTestLine("pass", pkgA, "TestPass"),
		goTestLine("run", pkgA, "TestFail"), goTestLine("fail", pkgA, "TestFail"),
		goTestLine("run", pkgA, "TestSkip"), goTestLine("skip", pkgA, "TestSkip"),
		goTestLine("run", pkgA, "TestSubSkip"), goTestLine("run", pkgA, "TestSubSkip/x"),
		goTestLine("skip", pkgA, "TestSubSkip/x"), goTestLine("pass", pkgA, "TestSubSkip"),
		goTestLine("pass", pkgA, "TestSubOK/y"), goTestLine("pass", pkgA, "TestSubOK"),
		goTestLine("pass", pkgA, "TestTwiceOneFails"), goTestLine("fail", pkgA, "TestTwiceOneFails"),
		goTestLine("pass", pkgA, ""),
		goTestLine("pass", pkgB, "TestPass"), goTestLine("fail", pkgB, ""),
	)
	o := outcomesOf(t, data)
	for _, c := range []struct {
		pkg, name string
		want      bool
	}{
		{pkgA, "TestPass", true},
		{pkgA, "TestFail", false},
		{pkgA, "TestSkip", false},
		{pkgA, "TestSubSkip", false},
		{pkgA, "TestSubOK", true},
		{pkgA, "TestTwiceOneFails", false},
		{pkgA, "TestMissing", false},
		{pkgB, "TestPass", false}, // the package failed as a whole
		{"tbound/supervisor/internal/unknown", "TestPass", false},
	} {
		if got := o.passed(c.pkg, c.name); got != c.want {
			t.Errorf("passed(%s, %s) = %t, want %t", c.pkg, c.name, got, c.want)
		}
	}
}

func TestGoTestJSONMergesFilesFailClosed(t *testing.T) {
	linux := goTestJSON(goTestLine("pass", pkgA, "TestX"))
	other := goTestJSON(goTestLine("skip", pkgA, "TestX"))
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
	good := mappingDoc(oblTests + `,{"id":"EVQ-B","title":"b","tests":{},"artifacts":["probe"]},{"id":"EVQ-C","title":"c"}`)
	m, err := parseEVQMapping(good)
	if err != nil {
		t.Fatalf("good mapping refused: %v", err)
	}
	if len(m.Obligations) != 3 || m.Obligations[0].ID != "EVQ-A" || m.Digest != sha256Of(good) {
		t.Fatalf("mapping = %+v", m)
	}
	for name, doc := range map[string][]byte{
		"empty":              nil,
		"no obligations":     mappingDoc(""),
		"wrong schema":       []byte(`{"schema":"x","obligations":[{"id":"EVQ-A","title":"a"}]}`),
		"unknown member":     mappingDoc(`{"id":"EVQ-A","title":"a","extra":1}`),
		"unknown top member": []byte(`{"schema":"tbound-evq-mapping/v1","obligations":[{"id":"EVQ-A","title":"a"}],"x":1}`),
		"duplicate key":      mappingDoc(`{"id":"EVQ-A","id":"EVQ-B","title":"a"}`),
		"trailing data":      append(mappingDoc(`{"id":"EVQ-A","title":"a"}`), []byte(`{}`)...),
		"repeated id":        mappingDoc(`{"id":"EVQ-A","title":"a"},{"id":"EVQ-A","title":"b"}`),
		"bad id":             mappingDoc(`{"id":"evq a","title":"a"}`),
		"empty id":           mappingDoc(`{"id":"","title":"a"}`),
		"no title":           mappingDoc(`{"id":"EVQ-A"}`),
		"subtest name":       mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":["TestX/sub"]}}`),
		"not a test":         mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":["Helper"]}}`),
		"bad package":        mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"../x":["TestX"]}}`),
		"empty test list":    mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":[]}}`),
		"repeated test":      mappingDoc(`{"id":"EVQ-A","title":"a","tests":{"` + pkgA + `":["TestX","TestX"]}}`),
		"bad artifact kind":  mappingDoc(`{"id":"EVQ-A","title":"a","artifacts":["Bad Kind"]}`),
		"repeated artifact":  mappingDoc(`{"id":"EVQ-A","title":"a","artifacts":["k","k"]}`),
		"null obligations":   []byte(`{"schema":"tbound-evq-mapping/v1","obligations":null}`),
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
			`{"id":"EVQ-FAIL","title":"f","tests":{"` + pkgA + `":["TestFail"]}},` +
			`{"id":"EVQ-MISSING","title":"m","tests":{"` + pkgA + `":["TestPass","TestGone"]}},` +
			`{"id":"EVQ-ART","title":"a","artifacts":["probe"]},` +
			`{"id":"EVQ-BOTH","title":"b","tests":{"` + pkgA + `":["TestPass"]},"artifacts":["probe","other"]},` +
			`{"id":"EVQ-UNMAPPED","title":"u"}`))
	if err != nil {
		t.Fatal(err)
	}
	tj := goTestJSON(
		goTestLine("pass", pkgA, "TestPass"), goTestLine("skip", pkgA, "TestSkip"), goTestLine("fail", pkgA, "TestFail"))
	in := evqInputs{
		Outcomes:  outcomesOf(t, tj),
		TestJSON:  []string{sha256Of(tj)},
		Artifacts: map[string]string{"probe": sha256Of([]byte("probe"))},
	}
	results, statuses := buildEVQResults(m, in)
	if err := results.Validate(); err != nil {
		t.Fatalf("results invalid: %v", err)
	}
	want := map[string]bool{"EVQ-PASS": true, "EVQ-SKIP": false, "EVQ-FAIL": false, "EVQ-MISSING": false,
		"EVQ-ART": true, "EVQ-BOTH": false, "EVQ-UNMAPPED": false}
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
		if r.Passed && r.EvidenceRef == "" {
			t.Errorf("%s passed without evidence_ref", r.ID)
		}
		if !r.Passed && r.EvidenceRef != "" {
			t.Errorf("%s not passed but carries evidence_ref %q", r.ID, r.EvidenceRef)
		}
		if statuses[i].ID != r.ID || statuses[i].Passed != r.Passed {
			t.Errorf("%s status = %+v", r.ID, statuses[i])
		}
	}
	// The evidence reference binds the mapping, the test JSON and the artifact bytes.
	pass := results[0].EvidenceRef
	for _, part := range []string{"evq-mapping=" + m.Digest, "go-test-json=" + sha256Of(tj)} {
		if !strings.Contains(pass, part) {
			t.Errorf("EVQ-PASS evidence_ref %q lacks %q", pass, part)
		}
	}
	art := results[4].EvidenceRef
	if !strings.Contains(art, "artifact:probe="+sha256Of([]byte("probe"))) {
		t.Errorf("EVQ-ART evidence_ref %q lacks the artifact digest", art)
	}
	if strings.Contains(art, "go-test-json") {
		t.Errorf("EVQ-ART needs no test JSON, evidence_ref %q", art)
	}
	for _, id := range []string{"EVQ-MISSING", "EVQ-BOTH", "EVQ-UNMAPPED"} {
		for _, s := range statuses {
			if s.ID == id && len(s.Reasons) == 0 {
				t.Errorf("%s not passed without a reason", id)
			}
		}
	}
	if !statuses[6].Unmapped || statuses[0].Unmapped {
		t.Errorf("unmapped flags wrong: %+v %+v", statuses[0], statuses[6])
	}
}

func TestBuildEVQResultsChangedArtifactChangesEvidence(t *testing.T) {
	m, _ := parseEVQMapping(mappingDoc(`{"id":"EVQ-ART","title":"a","artifacts":["probe"]}`))
	ref := func(content string) string {
		r, _ := buildEVQResults(m, evqInputs{Outcomes: newTestOutcomes(), Artifacts: map[string]string{"probe": sha256Of([]byte(content))}})
		return r[0].EvidenceRef
	}
	if ref("a") == ref("b") || ref("a") != ref("a") {
		t.Fatal("evidence_ref must follow the artifact digest")
	}
}

type evqEnv struct {
	dir, mapping, tj string
	tjData           []byte
	out              bytes.Buffer
}

func newEVQEnv(t *testing.T) *evqEnv {
	t.Helper()
	e := &evqEnv{dir: t.TempDir()}
	e.mapping = filepath.Join(e.dir, "mapping.json")
	mp := mappingDoc(`{"id":"EVQ-T","title":"t","tests":{"` + pkgA + `":["TestPass"]}},` +
		`{"id":"EVQ-G","title":"g","artifacts":["probe"]},{"id":"EVQ-U","title":"u"}`)
	if err := os.WriteFile(e.mapping, mp, 0o600); err != nil {
		t.Fatal(err)
	}
	e.tj = filepath.Join(e.dir, "test.json")
	e.tjData = goTestJSON(goTestLine("pass", pkgA, "TestPass"))
	if err := os.WriteFile(e.tj, e.tjData, 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *evqEnv) run(t *testing.T, args ...string) error {
	t.Helper()
	a := &app{out: &e.out, errOut: &bytes.Buffer{}}
	return a.run(append([]string{"evq"}, args...))
}

func TestEVQCommandWritesResultsRunJSONAccepts(t *testing.T) {
	e := newEVQEnv(t)
	probe := filepath.Join(e.dir, "probe.txt")
	if err := os.WriteFile(probe, []byte("landlock abi 8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(e.dir, "evq_results.json")
	if err := e.run(t, "--mapping", e.mapping, "--test-json", e.tj, "--artifact", "probe="+probe, "--out", out); err != nil {
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
	}
	if !got["EVQ-T"] || !got["EVQ-G"] || got["EVQ-U"] {
		t.Fatalf("results = %v", got)
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
	for _, want := range []string{"EVQ-T passed", "EVQ-G passed", "EVQ-U not_passed unmapped",
		`evq_obligations ["EVQ-T","EVQ-G","EVQ-U"]`, "results_sha256 " + sha256Of(data), "passed 2", "not_passed 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestEVQCommandDeterministicAndNeverOverwrites(t *testing.T) {
	e := newEVQEnv(t)
	o1, o2 := filepath.Join(e.dir, "o1.json"), filepath.Join(e.dir, "o2.json")
	for _, o := range []string{o1, o2} {
		if err := e.run(t, "--mapping", e.mapping, "--test-json", e.tj, "--out", o); err != nil {
			t.Fatal(err)
		}
	}
	d1, _ := os.ReadFile(o1)
	d2, _ := os.ReadFile(o2)
	if !bytes.Equal(d1, d2) {
		t.Fatal("same inputs gave different bytes")
	}
	if err := e.run(t, "--mapping", e.mapping, "--test-json", e.tj, "--out", o1); err == nil {
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

func TestEVQCommandRefusals(t *testing.T) {
	e := newEVQEnv(t)
	empty := filepath.Join(e.dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := func(n string) string { return filepath.Join(e.dir, n) }
	for name, args := range map[string][]string{
		"no mapping":          {"--test-json", e.tj, "--out", out("a")},
		"no test json":        {"--mapping", e.mapping, "--out", out("b")},
		"no out":              {"--mapping", e.mapping, "--test-json", e.tj},
		"missing mapping":     {"--mapping", filepath.Join(e.dir, "none"), "--test-json", e.tj, "--out", out("c")},
		"positional":          {"--mapping", e.mapping, "--test-json", e.tj, "--out", out("d"), "extra"},
		"unknown kind":        {"--mapping", e.mapping, "--test-json", e.tj, "--artifact", "typo=" + e.tj, "--out", out("e")},
		"repeated kind":       {"--mapping", e.mapping, "--test-json", e.tj, "--artifact", "probe=" + e.tj, "--artifact", "probe=" + e.tj, "--out", out("f")},
		"artifact no equals":  {"--mapping", e.mapping, "--test-json", e.tj, "--artifact", "probe", "--out", out("g")},
		"missing artifact":    {"--mapping", e.mapping, "--test-json", e.tj, "--artifact", "probe=" + filepath.Join(e.dir, "none"), "--out", out("h")},
		"empty artifact":      {"--mapping", e.mapping, "--test-json", e.tj, "--artifact", "probe=" + empty, "--out", out("i")},
		"directory artifact":  {"--mapping", e.mapping, "--test-json", e.tj, "--artifact", "probe=" + e.dir, "--out", out("j")},
		"malformed test json": {"--mapping", e.mapping, "--test-json", empty, "--out", out("k")},
	} {
		err := e.run(t, args...)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if name == "no mapping" || name == "no test json" || name == "no out" || name == "positional" {
			if !errors.Is(err, errUsage) {
				t.Errorf("%s: %v is not a usage error", name, err)
			}
		}
	}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		if _, err := os.Stat(out(n)); err == nil {
			t.Errorf("a refused run wrote %s", n)
		}
	}
}

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

// The obligations that have no implementable evidence yet. Mapping one of them is a deliberate
// change that edits this list.
var wantUnmappedEVQ = []string{
	"EVQ-COMMAND-VIEW-FAULTS",
	"EVQ-LINUX-CAPABILITY-GUEST",
	"EVQ-NETWORK-SINK-GUEST",
	"EVQ-PROVIDER-REAL-EXCHANGE",
	"EVQ-RECOVERY-LIFECYCLE-CUTPOINTS",
	"EVQ-SURFACE-ADAPTER-EQUIVALENCE",
}

func TestPinnedEVQMapping(t *testing.T) {
	m, err := parseEVQMapping(readRepoFile(t, evqMappingPath))
	if err != nil {
		t.Fatalf("pinned mapping: %v", err)
	}
	var unmapped []string
	for _, ob := range m.Obligations {
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
	if len(m.Obligations)-len(unmapped) < 10 {
		t.Errorf("only %d obligations are mapped", len(m.Obligations)-len(unmapped))
	}
}

// With no test results at all the pinned mapping passes nothing: the generator fails closed.
func TestPinnedEVQMappingFailsClosedWithoutEvidence(t *testing.T) {
	m, err := parseEVQMapping(readRepoFile(t, evqMappingPath))
	if err != nil {
		t.Fatal(err)
	}
	results, _ := buildEVQResults(m, evqInputs{Outcomes: newTestOutcomes()})
	for _, r := range results {
		if r.Passed {
			t.Errorf("%s passed without evidence", r.ID)
		}
	}
}

// With every mapped test passing, exactly the unmapped obligations stay not passed.
func TestPinnedEVQMappingPassesOnlyMappedObligations(t *testing.T) {
	m, err := parseEVQMapping(readRepoFile(t, evqMappingPath))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, ob := range m.Obligations {
		for pkg, names := range ob.Tests {
			for _, n := range names {
				lines = append(lines, goTestLine("pass", pkg, n))
			}
		}
	}
	tj := goTestJSON(lines...)
	results, _ := buildEVQResults(m, evqInputs{Outcomes: outcomesOf(t, tj), TestJSON: []string{sha256Of(tj)}})
	var notPassed []string
	for _, r := range results {
		if !r.Passed {
			notPassed = append(notPassed, r.ID)
		}
	}
	slices.Sort(notPassed)
	if !slices.Equal(notPassed, wantUnmappedEVQ) {
		t.Errorf("not passed = %v, want %v", notPassed, wantUnmappedEVQ)
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

const (
	refDR1 = pkgA + ".TestDeclaredAttempt"
	refDR2 = pkgA + ".TestDeclaredSuccess"
	refBV1 = pkgA + ".TestBashAttempt"
	refBV2 = pkgA + ".TestBashSuccess"
	refRT1 = pkgA + ".TestRuntimeAttempt"
	refRT2 = pkgA + ".TestRuntimeSuccess"
	refPB  = pkgA + ".TestBrokerDisclosure"
)

// fullRouteDoc covers the one class on all four routes with every required evidence kind.
func fullRouteDoc() []byte {
	return routeDoc(`"c1"`,
		routeCell("declared-read", "c1", "unreachable", "Covered", "", "AttemptedRead", refDR1, "SuccessfulRead", refDR2),
		routeCell("bash-view", "c1", "unreachable", "Covered", "", "AttemptedRead", refBV1, "SuccessfulRead", refBV2),
		routeCell("direct-runtime", "c1", "unreachable", "Covered", "", "AttemptedRead", refRT1, "SuccessfulRead", refRT2),
		routeCell("provider-broker", "c1", "proposal-denied", "Covered", "", "ProviderChannelDisclosure", refPB))
}

func passAll(refs ...string) []byte {
	var lines []string
	for _, r := range refs {
		i := strings.LastIndex(r, ".Test")
		lines = append(lines, goTestLine("pass", r[:i], r[i+1:]))
	}
	return goTestJSON(lines...)
}

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

func TestBuildRouteReportComplete(t *testing.T) {
	m, err := parseRouteMatrix(fullRouteDoc())
	if err != nil {
		t.Fatal(err)
	}
	o := outcomesOf(t, passAll(refDR1, refDR2, refBV1, refBV2, refRT1, refRT2, refPB))
	rep, err := buildRouteReport(m, o)
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
	all := []string{refDR1, refDR2, refBV1, refBV2, refRT1, refRT2, refPB}
	// Each ref in turn failing, skipped or absent: the report is incomplete and the cell is not Covered.
	for _, ref := range all {
		i := strings.LastIndex(ref, ".Test")
		for _, action := range []string{"fail", "skip", ""} {
			var lines []string
			for _, r := range all {
				if r == ref {
					if action != "" {
						lines = append(lines, goTestLine(action, r[:i], r[i+1:]))
					}
					continue
				}
				j := strings.LastIndex(r, ".Test")
				lines = append(lines, goTestLine("pass", r[:j], r[j+1:]))
			}
			o := outcomesOf(t, goTestJSON(lines...))
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
	dir, matrix, tj string
	out             bytes.Buffer
}

func newRouteEnv(t *testing.T, matrix []byte, tj []byte) *routeEnv {
	t.Helper()
	e := &routeEnv{dir: t.TempDir()}
	e.matrix, e.tj = filepath.Join(e.dir, "matrix.json"), filepath.Join(e.dir, "test.json")
	if err := os.WriteFile(e.matrix, matrix, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.tj, tj, 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *routeEnv) run(args ...string) error {
	a := &app{out: &e.out, errOut: &bytes.Buffer{}}
	return a.run(append([]string{"route-report"}, args...))
}

func TestRouteReportCommandIsAcceptedByRunConfig(t *testing.T) {
	e := newRouteEnv(t, fullRouteDoc(), passAll(refDR1, refDR2, refBV1, refBV2, refRT1, refRT2, refPB))
	out := filepath.Join(e.dir, "route_report.json")
	if err := e.run("--matrix", e.matrix, "--test-json", e.tj, "--out", out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	cov, err := loadRoute(out)
	if err != nil {
		t.Fatalf("loadRoute: %v", err)
	}
	if !cov.Complete || cov.Digest != sha256Of(data) {
		t.Fatalf("coverage = %+v", cov)
	}
	var rep routematrix.Report
	if err := json.Unmarshal(data, &rep); err != nil || rep.Total != 4 {
		t.Fatalf("report json: %v %+v", err, rep)
	}
	m, _ := parseRouteMatrix(fullRouteDoc())
	text := e.out.String()
	for _, want := range []string{"complete true", "matrix_digest " + routematrix.Digest(m),
		"route_report_sha256 " + sha256Of(data), "test_json " + sha256Of(e.tjBytes(t))} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	// Deterministic, and an existing report is not overwritten.
	out2 := filepath.Join(e.dir, "again.json")
	if err := e.run("--matrix", e.matrix, "--test-json", e.tj, "--out", out2); err != nil {
		t.Fatal(err)
	}
	if d2, _ := os.ReadFile(out2); !bytes.Equal(data, d2) {
		t.Fatal("same inputs gave different bytes")
	}
	if err := e.run("--matrix", e.matrix, "--test-json", e.tj, "--out", out); err == nil {
		t.Fatal("an existing report was overwritten")
	}
}

func (e *routeEnv) tjBytes(t *testing.T) []byte {
	t.Helper()
	d, err := os.ReadFile(e.tj)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRouteReportCommandIncompleteIsStillAccepted(t *testing.T) {
	e := newRouteEnv(t, fullRouteDoc(), goTestJSON(goTestLine("pass", pkgA, "TestOther")))
	out := filepath.Join(e.dir, "route_report.json")
	if err := e.run("--matrix", e.matrix, "--test-json", e.tj, "--out", out); err != nil {
		t.Fatal(err)
	}
	cov, err := loadRoute(out)
	if err != nil || cov.Complete {
		t.Fatalf("coverage = %+v, %v", cov, err)
	}
	if !strings.Contains(e.out.String(), "complete false") {
		t.Fatalf("output = %s", e.out.String())
	}
}

func TestRouteReportCommandRefusals(t *testing.T) {
	e := newRouteEnv(t, fullRouteDoc(), passAll(refDR1))
	for name, args := range map[string][]string{
		"no matrix":    {"--test-json", e.tj, "--out", filepath.Join(e.dir, "a")},
		"no test json": {"--matrix", e.matrix, "--out", filepath.Join(e.dir, "b")},
		"no out":       {"--matrix", e.matrix, "--test-json", e.tj},
		"bad matrix":   {"--matrix", e.tj, "--test-json", e.tj, "--out", filepath.Join(e.dir, "c")},
	} {
		if err := e.run(args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, n := range []string{"a", "b", "c"} {
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
	// With all its tests passing the matrix reports what is declared: it is honest about what has no evidence yet.
	var refs []string
	for _, c := range m.Cells {
		for _, e := range c.Evidence {
			refs = append(refs, e.TestRef)
		}
	}
	rep, err := buildRouteReport(m, outcomesOf(t, passAll(refs...)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != len(m.Cells) || rep.Total != 4*len(m.Classes) {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Complete && rep.ByStatus[routematrix.StatusCovered] != rep.Total {
		t.Fatalf("complete report with uncovered cells: %+v", rep)
	}
	// And with no test results, never complete.
	rep, err = buildRouteReport(m, newTestOutcomes())
	if err != nil || rep.Complete {
		t.Fatalf("empty evidence: %+v %v", rep, err)
	}
}
