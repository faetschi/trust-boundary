package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxTestEventLine bounds one line of `go test -json` output (an output event can be long).
const maxTestEventLine = 16 << 20

// testOutcomes are the results of named Go tests read from `go test -json` output. It is the one
// reader of test evidence for the EVQ results and the route report, so both fail closed the same
// way: a test counts as passed only when every observed run of it passed, nothing in it was skipped,
// and its package did not fail as a whole.
type testOutcomes struct {
	pkgFailed map[string]bool
	tests     map[testKey]*testTally
}

type testKey struct{ pkg, name string }

// testTally counts the result events of one top-level test. subBad is set when a subtest of it
// failed or was skipped: the parent can report pass over a skipped subtest, and that is not a pass here.
type testTally struct {
	pass, fail, skip int
	subBad           bool
}

func newTestOutcomes() *testOutcomes {
	return &testOutcomes{pkgFailed: map[string]bool{}, tests: map[testKey]*testTally{}}
}

// testEvent is the part of a test2json event this package reads. Other members are ignored.
type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

// add reads one `go test -json` document.
func (o *testOutcomes) add(data []byte) error { return o.addReader(bytes.NewReader(data)) }

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
	if ev.Action != "pass" && ev.Action != "fail" && ev.Action != "skip" {
		return
	}
	if ev.Test == "" {
		if ev.Action == "fail" {
			o.pkgFailed[ev.Package] = true
		}
		return
	}
	top, _, isSub := strings.Cut(ev.Test, "/")
	k := testKey{ev.Package, top}
	t := o.tests[k]
	if t == nil {
		t = &testTally{}
		o.tests[k] = t
	}
	switch {
	case isSub:
		if ev.Action != "pass" {
			t.subBad = true
		}
	case ev.Action == "pass":
		t.pass++
	case ev.Action == "fail":
		t.fail++
	default:
		t.skip++
	}
}

// passed reports whether the top-level test name of package pkg passed.
func (o *testOutcomes) passed(pkg, name string) bool { return o.state(pkg, name) == "" }

// state is empty for a passed test and otherwise names why it did not pass.
func (o *testOutcomes) state(pkg, name string) string {
	t := o.tests[testKey{pkg, name}]
	switch {
	case o.pkgFailed[pkg]:
		return "its package failed"
	case t == nil:
		return "no result in the test JSON"
	case t.fail > 0:
		return "failed"
	case t.skip > 0:
		return "skipped"
	case t.subBad:
		return "a subtest failed or was skipped"
	case t.pass == 0:
		return "no pass event"
	}
	return ""
}
