package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/usdledger"
)

// plan prints the plan digest and the unit counts.
func (a *app) plan(p plan) error {
	fmt.Fprintf(a.out, "plan_digest %s\n", p.sched.Digest)
	fmt.Fprintf(a.out, "manifest_digest %s\n", p.sched.ManifestDigest)
	fmt.Fprintf(a.out, "run_binding %s\n", p.sched.RunBinding)
	fmt.Fprintf(a.out, "quota_profile %s\n", p.cfg.QuotaProfile)
	fmt.Fprintf(a.out, "units %d\n", p.sched.Units())
	fmt.Fprintf(a.out, "subset %t\n", p.sched.Subset)
	fmt.Fprintf(a.out, "route_complete %t\n", p.route.Complete)
	byCfg, byRole := map[string]int{}, map[string]int{}
	for _, st := range p.sched.Steps {
		byCfg[string(st.Configuration)]++
		byRole[string(st.Role)]++
	}
	for _, k := range sortedKeys(byCfg) {
		fmt.Fprintf(a.out, "configuration %s %d\n", k, byCfg[k])
	}
	for _, k := range sortedKeys(byRole) {
		fmt.Fprintf(a.out, "role %s %d\n", k, byRole[k])
	}
	return nil
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// errMaxSteps is what limitExecutor.Prepare returns once --max-steps units are spent. A
// Prepare refusal voids the begin, so the next step is neither spent nor started.
var errMaxSteps = errors.New("stopped at --max-steps")

// limitExecutor stops the run cleanly after max executed steps.
type limitExecutor struct {
	runner.Executor
	max, done int
}

func (l *limitExecutor) Prepare(ctx context.Context, s runner.Step) error {
	if l.done >= l.max {
		return errMaxSteps
	}
	return l.Executor.Prepare(ctx, s)
}

func (l *limitExecutor) Execute(ctx context.Context, s runner.Step) (runner.Outcome, error) {
	l.done++
	return l.Executor.Execute(ctx, s)
}

// Check forwards the wiring check, which the runner runs before any unit is spent.
func (l *limitExecutor) Check(s runner.Schedule) error {
	if c, ok := l.Executor.(interface{ Check(runner.Schedule) error }); ok {
		return c.Check(s)
	}
	return nil
}

func (a *app) runCmd(args []string) error {
	var maxSteps int
	return a.withPlan("run", args, func(f *flag.FlagSet) {
		f.IntVar(&maxSteps, "max-steps", 0, "stop after this many executed steps (0 = no limit)")
	}, func(p plan) error {
		if maxSteps < 0 {
			return fmt.Errorf("%w: --max-steps must not be negative", errUsage)
		}
		ex, err := a.wire(p)
		if err != nil {
			return err
		}
		j, err := runner.OpenJournal(p.cfg.journalPath(), p.sched)
		if err != nil {
			return err
		}
		defer j.Close()
		var x runner.Executor = ex
		if maxSteps > 0 {
			x = &limitExecutor{Executor: ex, max: maxSteps}
		}
		r, err := runner.NewRunner(p.sched, j, x, runner.Options{})
		if err != nil {
			return err
		}
		rep, err := r.Run(a.ctx)
		a.printRun(rep)
		if errors.Is(err, errMaxSteps) {
			fmt.Fprintf(a.out, "stopped at --max-steps %d\n", maxSteps)
			return nil
		}
		return err
	})
}

func (a *app) printRun(rep runner.RunReport) {
	fmt.Fprintf(a.out, "plan_digest %s\nplanned_units %d\nunits_used %d\nexecuted %d\nalready_done %d\nabandoned %d\n"+
		"setup_failures %d\nexec_failures %d\nremaining %d\nfinished %t\nstop %q\nspent_micro_usd %d\n",
		rep.PlanDigest, rep.PlannedUnits, rep.UnitsUsed, rep.Executed, rep.AlreadyDone, rep.Abandoned,
		rep.SetupFailures, rep.ExecFailures, rep.Remaining, rep.Finished, rep.Stop, rep.SpentMicroUSD)
}

// existingJournal opens the journal of a run that has started; export, report and rerun
// never create one.
func existingJournal(p plan) (*runner.Journal, error) {
	if _, err := os.Stat(p.cfg.journalPath()); err != nil {
		return nil, fmt.Errorf("no journal: %w", err)
	}
	return runner.OpenJournal(p.cfg.journalPath(), p.sched)
}

func (a *app) rerunCmd(args []string) error {
	var step string
	return a.withPlan("rerun", args, func(f *flag.FlagSet) {
		f.StringVar(&step, "step", "", "step id (case|role|configuration|repeat)")
	}, func(p plan) error {
		if step == "" {
			return fmt.Errorf("%w: rerun needs --step", errUsage)
		}
		j, err := existingJournal(p)
		if err != nil {
			return err
		}
		defer j.Close()
		ex, err := a.wire(p)
		if err != nil {
			return err
		}
		r, err := runner.NewRunner(p.sched, j, ex, runner.Options{})
		if err != nil {
			return err
		}
		if err := r.Rerun(a.ctx, step); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "rerun of %s done; units_used %d\n", step, j.UnitsUsed())
		return nil
	})
}

func (a *app) exportCmd(args []string) error {
	var out string
	return a.withPlan("export", args, func(f *flag.FlagSet) {
		f.StringVar(&out, "out", "", "trials.json to write (evalscore-trials/v1)")
	}, func(p plan) error {
		if out == "" {
			return fmt.Errorf("%w: export needs --out", errUsage)
		}
		res, err := a.exportJournal(p)
		if err != nil {
			return err
		}
		if err := runner.WriteTrialsFile(out, res.Input); err != nil {
			return err
		}
		if _, err := syncFlagsFile(filepath.Dir(out), res.AdaptiveBudgetFlags); err != nil {
			return err
		}
		a.printExport(res)
		return nil
	})
}

func (a *app) exportJournal(p plan) (runner.ExportResult, error) {
	j, err := existingJournal(p)
	if err != nil {
		return runner.ExportResult{}, err
	}
	defer j.Close()
	opt := runner.ExportOptions{}
	if p.cfg.CDH != nil {
		opt.CDHBaseline, err = loadCDHBaseline(p.cfg.CDHBaseline)
		if err != nil {
			return runner.ExportResult{}, err
		}
	}
	return runner.Export(p.sched, j, opt)
}

func (a *app) printExport(res runner.ExportResult) {
	fmt.Fprintf(a.out, "adversarial_records %d\ncompliant_records %d\nincomplete_cases %d\npartial_cases %d\nevq_passed %t\n",
		len(res.Input.Adversarial), len(res.Input.Compliant), len(res.Incomplete), len(res.Partial), res.Input.EVQPassed)
	if len(res.AdaptiveBudgetFlags) > 0 {
		fmt.Fprintf(a.out, "adaptive_budget_flags %d\n", len(res.AdaptiveBudgetFlags))
		for _, l := range flagLines(res.AdaptiveBudgetFlags) {
			fmt.Fprintf(a.out, "adaptive_budget_flag %s\n", l)
		}
	}
}

// syntheticLedger is the empty USD ledger of a pilot report: nothing spent, the
// registered cap, no records. It binds nothing to a real ledger file.
func syntheticLedger() usdledger.Snapshot {
	return usdledger.Snapshot{
		LedgerID:                    ledgerSyntheticEmpty,
		CapMicroUSD:                 evalreport.RegisteredCapMicroUSD,
		MinPerUnitWorstCaseMicroUSD: 1,
		RemainingMicroUSD:           evalreport.RegisteredCapMicroUSD,
		HeadSHA256:                  strings.Repeat("0", 64),
	}
}

const nonClaimNote = "NON-CLAIM-BEARING: the USD ledger of this report is synthetic and empty, so its spend section " +
	"says nothing about the research budget. It is a pilot or dry-run report, not a result for G1, H1 or any thesis claim."

func (a *app) reportCmd(args []string) error {
	var dir string
	return a.withPlan("report", args, func(f *flag.FlagSet) {
		f.StringVar(&dir, "out", "", "directory for report.json, report.md and report.meta.json")
	}, func(p plan) error {
		if dir == "" {
			return fmt.Errorf("%w: report needs --out", errUsage)
		}
		res, err := a.exportJournal(p)
		if err != nil {
			return err
		}
		rep, err := evalreport.Build(evalreport.Inputs{Manifest: p.manifest, Trials: res.Input, Ledger: syntheticLedger()})
		if err != nil {
			return err
		}
		canonical, err := rep.CanonicalJSON()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		flagsDigest, err := syncFlagsFile(dir, res.AdaptiveBudgetFlags)
		if err != nil {
			return err
		}
		meta, err := json.Marshal(struct {
			ClaimBearing bool     `json:"claim_bearing"`
			Ledger       string   `json:"ledger"`
			ReportDigest string   `json:"report_digest"`
			PlanDigest   string   `json:"plan_digest"`
			Incomplete   int      `json:"incomplete_cases"`
			Flags        []string `json:"adaptive_budget_flags,omitempty"`
			FlagsDigest  string   `json:"adaptive_budget_flags_sha256,omitempty"`
			Note         string   `json:"note"`
		}{false, ledgerSyntheticEmpty, rep.Digest, p.sched.Digest, len(res.Incomplete), flagLines(res.AdaptiveBudgetFlags), flagsDigest, nonClaimNote})
		if err != nil {
			return err
		}
		for _, f := range []struct {
			name string
			data []byte
		}{
			{"report.json", append(canonical, '\n')},
			{"report.md", []byte("> " + nonClaimNote + "\n\n" + flagsMarkdown(res.AdaptiveBudgetFlags) + rep.Markdown())},
			{"report.meta.json", append(meta, '\n')},
		} {
			if err := writeFileAtomic(filepath.Join(dir, f.name), f.data); err != nil {
				return err
			}
		}
		fmt.Fprintf(a.out, "report_digest %s\nclaim_bearing false\nledger %s\n", rep.Digest, ledgerSyntheticEmpty)
		a.printExport(res)
		return nil
	})
}

// writeFileAtomic writes through a synced temporary file in the same directory and a rename.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tbound-run-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// selfcheck validates the configuration, the SSH settings and the executor wiring without
// any network or key access. It fails if any configuration of the plan has no executor.
func (a *app) selfcheck(p plan) error {
	failed := 0
	check := func(name string, err error) {
		if err != nil {
			failed++
			fmt.Fprintf(a.out, "FAIL %s: %v\n", name, err)
			return
		}
		fmt.Fprintf(a.out, "ok   %s\n", name)
	}
	check(fmt.Sprintf("plan %s (%d units)", p.sched.Digest, p.sched.Units()), nil)
	ssh := p.cfg.SSH
	if strings.ContainsAny(ssh.Path, `/\`) {
		check("ssh binary "+ssh.Path, statRegular(ssh.Path))
	} else {
		_, err := exec.LookPath(ssh.Path)
		check("ssh binary "+ssh.Path, err)
	}
	check("known_hosts "+ssh.KnownHosts, statRegular(ssh.KnownHosts))
	for _, k := range []struct{ name, path string }{{"client", ssh.Client.Key}, {"evidence", ssh.Evidence.Key}, {"trial", ssh.Trial.Key}} {
		check(k.name+" key file exists", statRegular(k.path)) // only stat: a key is never opened here
	}
	check("attest settings", runner.Attest{EVQ: p.cfg.EVQObligations, EVQResults: p.evq, Pins: p.cfg.Pins.ReceiptPins}.Validate())
	check("collectors", p.collectors().Validate())
	check("trial transport", runner.ResetExecutor{Transport: p.cfg.transports().trial}.Validate())

	ex, err := a.wire(p)
	if err != nil {
		check("executors", err)
	} else {
		configurations := []runner.Configuration{runner.CfgB, runner.CfgC, runner.CfgP, runner.CfgV}
		if p.cfg.CDH != nil {
			for _, model := range p.cfg.CDH.Manifest.Models {
				configurations = append(configurations, runner.CDHConfiguration(model))
			}
		}
		for _, cfg := range configurations {
			var steps []runner.Step
			for _, st := range p.sched.Steps {
				if st.Configuration == cfg {
					steps = append(steps, st)
				}
			}
			if len(steps) > 0 {
				check(fmt.Sprintf("configuration %s wired (%d steps)", cfg, len(steps)), ex.Check(runner.Schedule{Steps: steps}))
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("selfcheck: %d check(s) failed", failed)
	}
	return nil
}

func statRegular(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

// fixtureCmd serves `fixture digest <spec>`.
func (a *app) fixtureCmd(args []string) error {
	if len(args) != 2 || args[0] != "digest" {
		return fmt.Errorf("%w: usage: fixture digest <spec.json>", errUsage)
	}
	return a.fixtureDigest(args[1])
}
