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
	"os"
	"os/signal"
	"runtime"

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

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		os.Exit(2)
	}
}

func run(args []string, transcript io.Writer, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "provider" {
		return runProvider(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "serve" {
		return runServe(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "abandon-unknown" {
		return runAbandonUnknown(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "verify-retained" {
		return runVerifyRetained(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "workflow-root-init" {
		return runWorkflowRootInit(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "product-serve" {
		return runProductServe(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "project" {
		return runProject(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "chat" {
		return runChat(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "product" {
		return runProductAdmin(args[1:], transcript, stderr)
	}
	if len(args) > 0 && args[0] == "release" {
		return runRelease(args[1:], transcript, stderr)
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
