package main

import (
	"bytes"
	"strings"
	"testing"
)

// testRun is one test result of a package in a synthetic `go test -json` stream.
type testRun struct{ name, action string }

// pkgRun is one package of a synthetic stream: its tests and its package-level result.
type pkgRun struct {
	pkg    string
	tests  []testRun
	result string // "pass" when empty
}

// wellFormedLines renders the events a real `go test -json` run emits for the packages: package start,
// run and result of each test, then the package result.
func wellFormedLines(pkgs ...pkgRun) []string {
	var lines []string
	for _, p := range pkgs {
		lines = append(lines, goTestLine("start", p.pkg, ""))
		for _, tr := range p.tests {
			lines = append(lines, goTestLine("run", p.pkg, tr.name), goTestLine(tr.action, p.pkg, tr.name))
		}
		res := p.result
		if res == "" {
			res = "pass"
		}
		lines = append(lines, goTestLine(res, p.pkg, ""))
	}
	return lines
}

func wellFormed(pkgs ...pkgRun) []byte { return goTestJSON(wellFormedLines(pkgs...)...) }

// passingPkg is a package whose named tests all passed.
func passingPkg(pkg string, names ...string) pkgRun {
	p := pkgRun{pkg: pkg}
	for _, n := range names {
		p.tests = append(p.tests, testRun{n, "pass"})
	}
	return p
}

func TestGoTestJSONRequiresACompleteLifecycle(t *testing.T) {
	run := func(pkg, test string) string { return goTestLine("run", pkg, test) }
	pass := func(pkg, test string) string { return goTestLine("pass", pkg, test) }
	start := func(pkg string) string { return goTestLine("start", pkg, "") }
	for name, c := range map[string]struct {
		lines []string
		want  bool
	}{
		"a complete run passes": {wellFormedLines(passingPkg(pkgA, "TestX")), true},
		"result events only (synthetic)": {[]string{
			pass(pkgA, "TestX")}, false},
		"result events and a package pass but no run or start": {[]string{
			pass(pkgA, "TestX"), goTestLine("pass", pkgA, "")}, false},
		"tests passed but the stream was cut before the package result": {[]string{
			start(pkgA), run(pkgA, "TestX"), pass(pkgA, "TestX")}, false},
		"a pass followed by an unfinished repeat run": {[]string{
			start(pkgA), run(pkgA, "TestX"), pass(pkgA, "TestX"), run(pkgA, "TestX"), goTestLine("pass", pkgA, "")}, false},
		"a run with no result at all": {[]string{
			start(pkgA), run(pkgA, "TestX"), goTestLine("pass", pkgA, "")}, false},
		"a result with no run": {[]string{
			start(pkgA), pass(pkgA, "TestX"), goTestLine("pass", pkgA, "")}, false},
		"package pass without a start event": {[]string{
			run(pkgA, "TestX"), pass(pkgA, "TestX"), goTestLine("pass", pkgA, "")}, false},
		"a package started twice but finished once": {[]string{
			start(pkgA), start(pkgA), run(pkgA, "TestX"), pass(pkgA, "TestX"), goTestLine("pass", pkgA, "")}, false},
		"the package failed after the test passed": {wellFormedLines(
			pkgRun{pkg: pkgA, tests: []testRun{{"TestX", "pass"}}, result: "fail"}), false},
		"another test of the package failed": {wellFormedLines(
			pkgRun{pkg: pkgA, tests: []testRun{{"TestX", "pass"}, {"TestY", "fail"}}}), false},
		"the same test passed and failed": {wellFormedLines(
			pkgRun{pkg: pkgA, tests: []testRun{{"TestX", "pass"}, {"TestX", "fail"}}}), false},
		"a skipped package": {wellFormedLines(
			pkgRun{pkg: pkgA, tests: []testRun{{"TestX", "pass"}}, result: "skip"}), false},
		"a subtest that never finished": {[]string{
			start(pkgA), run(pkgA, "TestX"), run(pkgA, "TestX/sub"), pass(pkgA, "TestX"), goTestLine("pass", pkgA, "")}, false},
		"a repeated complete test run (count 2) passes": {[]string{
			start(pkgA), run(pkgA, "TestX"), pass(pkgA, "TestX"), run(pkgA, "TestX"), pass(pkgA, "TestX"), goTestLine("pass", pkgA, "")}, true},
	} {
		o := newTestOutcomes()
		if err := o.add(goTestJSON(c.lines...)); err != nil {
			t.Fatalf("%s: add: %v", name, err)
		}
		if got := o.passed(pkgA, "TestX"); got != c.want {
			t.Errorf("%s: passed = %t (%s), want %t", name, got, o.state(pkgA, "TestX"), c.want)
		}
	}
}

// -count=1 is the frozen invocation, so one run per test is the expected shape, but a repeated
// package run is consistent when every start has its pass.
func TestGoTestJSONAcceptsConsistentRepeatedRuns(t *testing.T) {
	two := wellFormed(passingPkg(pkgA, "TestX"))
	both := append(append([]byte{}, two...), two...)
	if o := outcomesOf(t, both); !o.passed(pkgA, "TestX") {
		t.Fatalf("two complete runs: %s", o.state(pkgA, "TestX"))
	}
	// Two files, one clean and one with the package failed: the failure wins.
	bad := wellFormed(pkgRun{pkg: pkgA, tests: []testRun{{"TestX", "pass"}}, result: "fail"})
	if o := outcomesOf(t, two, bad); o.passed(pkgA, "TestX") {
		t.Fatal("a package failure in any file must fail the test")
	}
}

// A package that has no test files reports only a skip; it passes no test.
func TestGoTestJSONNoTestFilesPackageIsNotAPass(t *testing.T) {
	data := goTestJSON(goTestLine("start", pkgB, ""), goTestLine("skip", pkgB, ""))
	o := outcomesOf(t, data)
	if o.passed(pkgB, "TestAnything") {
		t.Fatal("a skipped package passed a test")
	}
	if !strings.Contains(o.state(pkgB, "TestAnything"), "no result") && o.state(pkgB, "TestAnything") == "" {
		t.Fatal("state must name a reason")
	}
}

// add reads one `go test -json` document.
func (o *testOutcomes) add(data []byte) error { return o.addReader(bytes.NewReader(data)) }
