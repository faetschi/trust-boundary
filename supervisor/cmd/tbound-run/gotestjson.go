package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// maxTestEventLine bounds one line of `go test -json` output (an output event can be long).
const maxTestEventLine = 16 << 20

// testOutcomes are the results of named Go tests read from `go test -json` output. It is the one
// reader of test evidence for the EVQ results and the route report, so both fail closed the same
// way. It follows the lifecycle of every package and test: a package counts only if it started and
// finished with a package-level pass (a cut stream, a package run that never finished and a skipped
// package do not), a test counts as passed only when each of its run events has exactly one pass,
// it has no failure and no skip, and a failure of any test or of the package fails every test of
// that package.
type testOutcomes struct {
	pkgs  map[string]*pkgTally
	tests map[testKey]*testTally
}

type testKey struct{ pkg, name string }

// pkgTally counts the package-level events of one package and notes a failed test anywhere in it.
type pkgTally struct {
	start, pass, fail, skip int
	testFailed              bool
}

// testTally counts the lifecycle events of one top-level test. subs counts run and result events
// per subtest: an unfinished, unmatched, failed or skipped subtest makes the parent not pass,
// because the parent can report pass over a skipped subtest.
type testTally struct {
	runs, pass, fail, skip int
	subs                   map[string]*subTally
}

type subTally struct {
	runs, results int
	notPassed     bool
}

func newTestOutcomes() *testOutcomes {
	return &testOutcomes{pkgs: map[string]*pkgTally{}, tests: map[testKey]*testTally{}}
}

// testEvent is the part of a test2json event this package reads. Other members are ignored.
type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

// addReader reads one `go test -json` stream. A line that is not a JSON object with an Action and a
// Package is refused, and so is a stream without any event: nothing is guessed from text.
func (o *testOutcomes) addReader(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxTestEventLine)
	events := 0
	for line := 1; sc.Scan(); line++ {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return fmt.Errorf("go test json: line %d is not a test event: %w", line, err)
		}
		if ev.Action == "" || ev.Package == "" {
			return fmt.Errorf("go test json: line %d has no Action or Package", line)
		}
		events++
		o.record(ev)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("go test json: %w", err)
	}
	if events == 0 {
		return errors.New("go test json: no test events")
	}
	return nil
}

func (o *testOutcomes) record(ev testEvent) {
	p := o.pkgs[ev.Package]
	if p == nil {
		p = &pkgTally{}
		o.pkgs[ev.Package] = p
	}
	if ev.Test == "" {
		switch ev.Action {
		case "start":
			p.start++
		case "pass":
			p.pass++
		case "fail":
			p.fail++
		case "skip":
			p.skip++
		}
		return
	}
	if ev.Action != "run" && ev.Action != "pass" && ev.Action != "fail" && ev.Action != "skip" {
		return
	}
	top, _, isSub := strings.Cut(ev.Test, "/")
	k := testKey{ev.Package, top}
	t := o.tests[k]
	if t == nil {
		t = &testTally{subs: map[string]*subTally{}}
		o.tests[k] = t
	}
	if ev.Action == "fail" {
		p.testFailed = true
	}
	if isSub {
		s := t.subs[ev.Test]
		if s == nil {
			s = &subTally{}
			t.subs[ev.Test] = s
		}
		if ev.Action == "run" {
			s.runs++
			return
		}
		s.results++
		if ev.Action != "pass" {
			s.notPassed = true
		}
		return
	}
	switch ev.Action {
	case "run":
		t.runs++
	case "pass":
		t.pass++
	case "fail":
		t.fail++
	default:
		t.skip++
	}
}

// passed reports whether the top-level test name of package pkg passed.
func (o *testOutcomes) passed(pkg, name string) bool { return o.state(pkg, name) == "" }

// state is empty for a passed test and otherwise names why it did not pass.
func (o *testOutcomes) state(pkg, name string) string {
	p := o.pkgs[pkg]
	t := o.tests[testKey{pkg, name}]
	switch {
	case p == nil:
		return "no result in the test JSON"
	case p.fail > 0 || p.testFailed:
		return "its package failed"
	case p.start == 0:
		return "its package has no start event"
	case p.skip > 0:
		return "its package was skipped"
	case p.pass != p.start:
		return "its package did not finish with a package-level pass"
	case t == nil:
		return "no result in the test JSON"
	case t.fail > 0:
		return "failed"
	case t.skip > 0:
		return "skipped"
	case t.runs == 0 || t.runs != t.pass:
		return "run and pass events do not match (unfinished, or a result without a run)"
	}
	for _, s := range t.subs {
		switch {
		case s.notPassed:
			return "a subtest failed or was skipped"
		case s.runs == 0 || s.runs != s.results:
			return "a subtest did not finish"
		}
	}
	return ""
}

// packageNames lists every package that has an event, sorted.
func (o *testOutcomes) packageNames() []string {
	names := make([]string, 0, len(o.pkgs))
	for n := range o.pkgs {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// packageFinished reports whether the package started and every start has exactly one package-level
// result (pass, fail or skip). A cut stream leaves a package unfinished.
func (o *testOutcomes) packageFinished(pkg string) bool {
	p := o.pkgs[pkg]
	return p != nil && p.start > 0 && p.start == p.pass+p.fail+p.skip
}

// anyFailed reports whether any package or test failed in the stream.
func (o *testOutcomes) anyFailed() bool {
	for _, p := range o.pkgs {
		if p.fail > 0 || p.testFailed {
			return true
		}
	}
	return false
}
