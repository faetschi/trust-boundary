package taskaccept

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// OpNodeTest is the behavioural check: the file at Path, followed by the pinned dataset test
// program Text (node:assert, MultiPL-E style: the tests name the function directly), must run to
// completion under node. It uses Path and Text only.
//
// This package never runs it. Evaluation hands the program to a NodeRunner; without one, or when
// the runner fails, the check is refused with an error and never passes. The production runner
// executes it in an isolated, network-none cell (cmd/tbound); admission may use a local runner,
// because there it only ever runs the corpus's own fixture and reference solution.
const OpNodeTest = "node_test"

// NodeTestTimeout is the fixed wall-clock limit of one behavioural check.
const NodeTestTimeout = 30 * time.Second

// MaxNodeStdout bounds the stdout a runner returns.
const MaxNodeStdout = 64 << 10

// NodeRun is what a runner observed: node's exit code, whether the timeout killed it, and its
// stdout (at most MaxNodeStdout bytes, the tail).
type NodeRun struct {
	Exit     int
	TimedOut bool
	Stdout   []byte
}

// NodeRunner runs one node program in isolation with NodeTestTimeout. It returns an error when the
// program could not be run or observed; the check is then refused, not failed or passed.
type NodeRunner interface {
	RunNode(program []byte) (NodeRun, error)
}

// ErrNoNodeRunner refuses a behavioural check when no isolated runner is available.
var ErrNoNodeRunner = errors.New("a node_test check needs an isolated node runner; none is available, so acceptance is refused")

// checkNodeTest returns the result, or an error that refuses the whole evaluation. A missing or
// unreadable file fails without running anything. The program ends with a marker that only the
// evaluator knows, so a file that exits early (process.exit(0)) before the tests ran does not pass.
func checkNodeTest(after tree, c Check, runner NodeRunner) (bool, string, error) {
	n, ok := after[c.Path]
	if !ok {
		return false, "the file is missing", nil
	}
	data, err := n.read(MaxReadBytes)
	if err != nil {
		return false, err.Error(), nil
	}
	if runner == nil {
		return false, "", ErrNoNodeRunner
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false, "", fmt.Errorf("node_test marker: %w", err)
	}
	marker := "tbound-node-test-complete-" + hex.EncodeToString(nonce[:])
	program := make([]byte, 0, len(data)+len(c.Text)+len(marker)+32)
	program = append(append(append(program, data...), '\n'), c.Text...)
	program = append(program, "\nconsole.log('"+marker+"');\n"...)
	run, err := runner.RunNode(program)
	if err != nil {
		return false, "", fmt.Errorf("node_test %s: the isolated runner failed, so acceptance is refused: %w", c.Path, err)
	}
	switch {
	case run.TimedOut:
		return false, fmt.Sprintf("the tests did not finish within %s", NodeTestTimeout), nil
	case run.Exit != 0:
		return false, fmt.Sprintf("the tests failed (node exit %d)", run.Exit), nil
	case !endsWithLine(run.Stdout, marker):
		return false, "the tests did not run to completion", nil
	}
	return true, "the dataset tests pass", nil
}

func endsWithLine(out []byte, line string) bool {
	lines := bytes.Split(bytes.TrimRight(out, "\r\n"), []byte("\n"))
	return string(bytes.TrimRight(lines[len(lines)-1], "\r")) == line
}
