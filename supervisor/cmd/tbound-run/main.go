// Command tbound-run is the host-side runner CLI of the TBound evaluation. It plans,
// runs, resumes, exports and reports the registered experiment from the Windows host
// (and anywhere else Go builds): it reaches the guest only through the Git Bash `ssh`
// binary, with host key checking fixed to StrictHostKeyChecking=yes against one pinned
// known_hosts file. There is no option to relax it.
//
// Every subcommand takes --config run.json (see runConfig). The journal is
// <run_dir>/journal.jsonl: `run` is resumable from it, and a step that was begun in an
// earlier process and never finished is closed as abandoned, never run again.
//
// Configurations P, B and V have no executor yet. A plan that contains them fails closed
// with runner.ErrNotWired in `selfcheck` and `run`; nothing is filtered or skipped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
)

const usageText = `tbound-run - host-side runner of the TBound evaluation

usage:
  tbound-run plan      --config run.json [--registration record.json]
  tbound-run run       --config run.json [--max-steps N]
  tbound-run rerun     --config run.json --step <step id>
  tbound-run export    --config run.json --out trials.json
  tbound-run report    --config run.json --out <dir> [--ledger-v3 d48-ledger-v3.jsonl [--certification certify.json]] [--registration record.json]
  tbound-run selfcheck --config run.json
  tbound-run fixture digest <spec.json>
  tbound-run source-digest --repo <git repo> --commit <40-hex>     (the frozen source digest: host profile SourceProvenanceDigest, run.json pins.frozen_source_digest)
  tbound-run evq-capture  --repo <git repo> --commit <40-hex> --out <new dir>     (Linux or WSL)
  tbound-run evq-adapter-capture --repo <git repo> --commit <40-hex> --node <abs> --npm-cli <abs npm-cli.js> --npm-cache <dir> --out <new file>   (Linux or WSL; offline)
  tbound-run evq          --mapping evq-mapping.json --config run.json --capture <dir> [--guest-binary-sha256 sha256:.. (needed for guest kinds) --artifact kind=file ...] --out evq_results.json
  tbound-run route-report --matrix route-matrix.json --config run.json --capture <dir> --out route_report.json
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	a := &app{out: os.Stdout, errOut: os.Stderr, wire: wireSSH, ctx: ctx}
	err := a.run(os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tbound-run:", err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

// app holds what the subcommands share. wire and ctx are test seams.
type app struct {
	out, errOut io.Writer
	wire        wireFunc
	ctx         context.Context
}

func (a *app) run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.errOut, usageText)
		return fmt.Errorf("%w: no subcommand", errUsage)
	}
	switch name, rest := args[0], args[1:]; name {
	case "plan":
		return a.planCmd(rest)
	case "run":
		return a.runCmd(rest)
	case "rerun":
		return a.rerunCmd(rest)
	case "export":
		return a.exportCmd(rest)
	case "report":
		return a.reportCmd(rest)
	case "selfcheck":
		return a.withPlan(name, rest, nil, a.selfcheck)
	case "fixture":
		return a.fixtureCmd(rest)
	case "evq":
		return a.evqCmd(rest)
	case "source-digest":
		return a.sourceDigestCmd(rest)
	case "evq-capture":
		return a.evqCaptureCmd(rest)
	case "evq-adapter-capture":
		return a.evqAdapterCaptureCmd(rest)
	case "route-report":
		return a.routeReportCmd(rest)
	case "help", "-h", "--help":
		fmt.Fprint(a.out, usageText)
		return nil
	}
	fmt.Fprint(a.errOut, usageText)
	return fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
}

// withPlan parses the flags of a subcommand (plus --config), loads the plan and calls fn.
// extra registers the subcommand's own flags.
func (a *app) withPlan(name string, args []string, extra func(*flag.FlagSet), fn func(plan) error) error {
	flags := flag.NewFlagSet("tbound-run "+name, flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	config := flags.String("config", "", "run.json")
	if extra != nil {
		extra(flags)
	}
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 || *config == "" {
		return fmt.Errorf("%w: %s takes --config and no positional arguments", errUsage, name)
	}
	cfg, err := loadConfig(*config)
	if err != nil {
		return err
	}
	p, err := loadPlan(cfg)
	if err != nil {
		return err
	}
	return fn(p)
}
