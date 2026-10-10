//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// runServe implements the native Pi routes. The default production route now
// composes the protected, root-owned /etc/tbound host profile, the registered
// D09 cell template, and the sealed runtime bindings supplied by the trusted
// runtime/session composition owners; it launches only when the approved
// profile, trust root, and sealed bindings are all present, and otherwise
// refuses safely with the exact missing prerequisites. The explicit fixtures
// remain separate and mutually exclusive with each other and with a production
// host profile.
func runServe(args []string, transcript io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pi := flags.Bool("pi", false, "run the native Pi harness")
	nativeFixture := flags.Bool("native-fixture", false, "run the explicit non-claim-bearing native lifecycle fixture")
	devFixture := flags.Bool("dev-fixture", false, "launch the actual pinned Pi SDK worker through the offline non-claim-bearing development composition")
	nativeHostProfile := flags.String("native-host-profile", "", "protected authorized signed host profile path; empty uses the root-owned /etc/tbound trust root")
	workspace := flags.String("workspace", "", "protected source workspace directory for the governed production session")
	source := flags.String("source", "", "protected source snapshot directory for the governed production session")
	taskID := flags.String("task-id", "", "registered, host-admitted production task identity")
	taskRequestID := flags.String("task-request-id", "", "bounded production prompt request identity")
	task := flags.String("task", "", "bounded plain production task prompt (host-admitted control, never a model or key hint)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !*pi {
		return errors.New("tbound serve requires --pi")
	}
	if *devFixture && *nativeFixture {
		return errors.New("--dev-fixture and --native-fixture are mutually exclusive")
	}
	if *devFixture && *nativeHostProfile != "" {
		return errors.New("--dev-fixture and --native-host-profile are mutually exclusive")
	}
	if *devFixture {
		return runDevelopmentPiFixture(context.Background(), transcript)
	}
	if *nativeFixture {
		return runNativeFixture(context.Background(), transcript)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runGovernedPiServe(ctx, transcript, productionServeConfig{
		ProfilePath:   *nativeHostProfile,
		Workspace:     *workspace,
		Source:        *source,
		TaskID:        *taskID,
		TaskRequestID: *taskRequestID,
		TaskPrompt:    *task,
	})
}

// runGovernedPiServe is the CLI production entry point. It performs the CLI-side
// trust decision and drives the composed session through the concrete
// productionBinder. The binder assembles the protected profile/template, durable
// D06 session, sealed exposure/assets, and the D09 launcher; there is no nil
// placeholder. See docs/production-pi-session.md for the exact contract.
func runGovernedPiServe(ctx context.Context, output io.Writer, config productionServeConfig) error {
	return runProductionPiServe(ctx, output, config, newProductionBinder())
}
