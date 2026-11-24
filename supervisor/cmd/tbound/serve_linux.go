//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"tbound/supervisor/internal/sessionrepo"
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
	serviceMode := flags.Bool("service", false, "internal socket-activated Pi service mode")
	nativeFixture := flags.Bool("native-fixture", false, "run the explicit non-claim-bearing native lifecycle fixture")
	devFixture := flags.Bool("dev-fixture", false, "launch official Pi CLI 0.87.1 over RPC through the offline non-claim-bearing development composition")
	nativeHostProfile := flags.String("native-host-profile", "", "protected authorized signed host profile path; empty uses the root-owned /etc/tbound trust root")
	workspace := flags.String("workspace", "", "protected source workspace directory for the governed production session")
	source := flags.String("source", "", "protected source snapshot directory for the governed production session")
	taskID := flags.String("task-id", "", "registered, host-admitted production task identity (the case_id in corpus mode)")
	attempt := flags.Int("attempt", 0, "corpus-mode attempt, 1 or 2 (2 only as an outage rerun); corpus-mode services take the prompt from their frozen manifest and refuse --task")
	role := flags.String("role", "", "corpus-mode governed role of the trial: trajectory, static-replay, adaptive-replay, variance-1, variance-2, compliant, ambiguous, p-static-replay, p-adaptive-replay, b-compliant, b-ambiguous or v (the service checks it against the case kind)")
	taskRequestID := flags.String("task-request-id", "", "bounded production prompt request identity")
	task := flags.String("task", "", "bounded plain production task prompt (host-admitted control, never a model or key hint)")
	var rawBashOutputPaths []string
	flags.Func("bash-output", "operator-declared exact relative path Bash may change (repeatable)", func(path string) error {
		rawBashOutputPaths = append(rawBashOutputPaths, path)
		return nil
	})
	bashNoOutputs := flags.Bool("bash-no-outputs", false, "explicitly allow Bash only when it changes no paths")
	if err := flags.Parse(args); err != nil {
		return err
	}
	bashOutputPaths, err := canonicalServeBashOutputPaths(rawBashOutputPaths, *bashNoOutputs)
	if err != nil {
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
	if *serviceMode && (*devFixture || *nativeFixture) {
		return errors.New("--service is mutually exclusive with development/native fixtures")
	}
	if *devFixture || *nativeFixture {
		fixtureFlag := "--native-fixture"
		if *devFixture {
			fixtureFlag = "--dev-fixture"
		}
		if err := rejectFixtureProductionInputs(flags, fixtureFlag); err != nil {
			return err
		}
	}
	if (*devFixture || *nativeFixture) && bashOutputPaths != nil {
		return errors.New("Bash output scope is available only through the production Pi service client")
	}
	if *devFixture {
		return runDevelopmentPiFixture(context.Background(), transcript)
	}
	if *nativeFixture {
		return runNativeFixture(context.Background(), transcript)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *serviceMode {
		if *nativeHostProfile != "" || *workspace != "" || *source != "" || *taskID != "" || *taskRequestID != "" || *task != "" || *attempt != 0 || *role != "" || bashOutputPaths != nil {
			return errors.New("internal --service mode accepts no task, profile, workspace, source, or Bash output scope arguments")
		}
		return runProductionPiService(ctx, transcript)
	}
	if *nativeHostProfile != "" || *workspace != "" || *source != "" {
		return errors.New("ordinary `serve --pi` is a local service client; profile/source/workspace are service-owned and cannot be selected by the client")
	}
	var bashOutputScope *[]string
	if bashOutputPaths != nil {
		cloned := cloneDurableOutputScopePaths(bashOutputPaths)
		bashOutputScope = &cloned
	}
	request := piServiceRequest{
		RequestID: *taskRequestID, TaskID: *taskID, Prompt: *task, Attempt: *attempt, Role: *role, BashOutputPaths: bashOutputScope,
	}
	if request.Role == corpusRoleV {
		return runProductionPiServiceVClient(ctx, os.Stdin, transcript, request)
	}
	return runProductionPiServiceClient(ctx, transcript, request)
}

func rejectFixtureProductionInputs(flags *flag.FlagSet, fixtureFlag string) error {
	provided := make(map[string]bool, 3)
	flags.Visit(func(option *flag.Flag) {
		provided[option.Name] = true
	})
	for _, name := range []string{"native-host-profile", "workspace", "source"} {
		if provided[name] {
			return fmt.Errorf("%s does not accept --%s; omit production service inputs for this offline fixture", fixtureFlag, name)
		}
	}
	return nil
}

func canonicalServeBashOutputPaths(raw []string, allowNoOutputs bool) ([]string, error) {
	if allowNoOutputs {
		if len(raw) != 0 {
			return nil, errors.New("--bash-no-outputs cannot be combined with --bash-output")
		}
		return []string{}, nil
	}
	if len(raw) == 0 {
		return nil, nil
	}
	paths := append([]string(nil), raw...)
	sort.Strings(paths)
	if err := sessionrepo.ValidateOutputScopePaths(paths); err != nil {
		return nil, fmt.Errorf("invalid --bash-output scope: %w", err)
	}
	return paths, nil
}
