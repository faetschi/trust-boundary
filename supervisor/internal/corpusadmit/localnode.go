package corpusadmit

import (
	"bytes"
	"context"
	"errors"
	"os/exec"

	"tbound/supervisor/internal/taskaccept"
)

// LocalNodeRunner runs a node_test program with a local node binary, reading the program from
// stdin, under taskaccept.NodeTestTimeout. It is for admission only: CheckManifestArtifactsWith
// runs nothing but the corpus's own fixtures and reference solutions with it. An agent's output is
// never run this way; trial acceptance uses the isolated cell runner of cmd/tbound.
type LocalNodeRunner struct{ Path string }

func (r LocalNodeRunner) RunNode(program []byte) (taskaccept.NodeRun, error) {
	if r.Path == "" {
		return taskaccept.NodeRun{}, taskaccept.ErrNoNodeRunner
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskaccept.NodeTestTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Path, "-")
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout = bytes.NewReader(program), &out
	err := cmd.Run()
	run := taskaccept.NodeRun{TimedOut: ctx.Err() != nil, Stdout: out.Bytes()}
	if n := len(run.Stdout); n > taskaccept.MaxNodeStdout {
		run.Stdout = run.Stdout[n-taskaccept.MaxNodeStdout:]
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		run.Exit = exit.ExitCode()
	case err != nil && !run.TimedOut:
		return taskaccept.NodeRun{}, err
	}
	return run, nil
}
