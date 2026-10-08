// Command tbound is the supervisor process entry point. Production provider
// registration, durable session admission, audit wiring, and a contained
// executor are not configured by default, so only explicitly selected routes
// may start a runtime.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/piruntime"
)

var ErrRuntimeNotConfigured = errors.New("provider broker, durable audit, and contained executor are not configured")

// Keep the original cmd/tbound construction names source-compatible while the
// implementation is importable by the native host composition root.
type Broker = piruntime.Broker
type Executor = piruntime.Executor
type DurableDecisionRecorder = piruntime.DurableDecisionRecorder
type DecisionRecorder = DurableDecisionRecorder
type Supervisor = piruntime.Supervisor

// transcriptEntry remains a small command-package decoding type used by the
// existing smoke/integration tests. The shared runtime owns its writer type.
type transcriptEntry struct {
	Proposal    protocol.Proposal    `json:"proposal"`
	Correlation correlation.Decision `json:"correlation"`
	Decision    gate.Decision        `json:"decision"`
	Result      protocol.Result      `json:"result"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string, transcript io.Writer, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "serve" {
		return runServe(args[1:], transcript, stderr)
	}
	flags := flag.NewFlagSet("tbound", flag.ContinueOnError)
	flags.SetOutput(stderr)
	smokeListen := flags.Bool("smoke-listen", false, "run the synthetic, no-effect Unix-socket integration listener")
	socketDir := flags.String("socket-dir", "", "caller-created private mode-0700 directory for the smoke socket")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !*smokeListen {
		return ErrRuntimeNotConfigured
	}
	if runtime.GOOS != "linux" {
		return errors.New("the synthetic Unix-socket smoke listener is available only on Linux")
	}
	if *socketDir == "" {
		return errors.New("--socket-dir is required for --smoke-listen")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runSyntheticListener(ctx, *socketDir, transcript)
}

// serveConn is the small composition point used by a host-owned runtime after
// it has created and bound a session connection.
func serveConn(ctx context.Context, conn net.Conn, bindingToken string, broker Broker, policy gate.Policy, decisions DecisionRecorder, executor Executor) error {
	server, err := ipc.NewServer(conn, bindingToken)
	if err != nil {
		return err
	}
	return (&Supervisor{IPC: server, Broker: broker, Policy: policy, Decisions: decisions, Executor: executor}).Serve(ctx)
}
