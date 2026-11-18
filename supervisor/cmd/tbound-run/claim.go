package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
)

const maxLedgerV3Bytes = 64 << 20

// The reasons a report is not claim-bearing, in the order claimReasons checks them.
const (
	reasonLedgerSynthetic = "ledger_synthetic"              // no verified USD ledger file: the spend section says nothing
	reasonNotThesis       = "not_thesis_profile"            // H1 is the thesis corpus; a pilot run bears no H1 claim
	reasonRunIncomplete   = "run_incomplete"                // a registered case has no record or an unfinished step (D36: unfinished evidence)
	reasonNoGroups        = "no_scenario_mechanism_results" // D47/D48: the case bound is never reported alone
	reasonNotCertified    = "bound_not_certified"           // D48: the case bound stands only after the zero-event certification
	reasonNotRegistered   = "not_registered"                // D31/D36: no pre-registration record to check the run against
	reasonMismatchPrefix  = "registration_mismatch:"        // the run is not the registered one; the suffix names the field
)

// claimReasons lists why a report is not claim-bearing. A report is claim-bearing exactly when the list is empty:
// it is then the registered confirmatory analysis of the registered run, and stands as the result whatever its H1
// verdict says. A non-claim-bearing report is still a correct report of its inputs; it is not the H1 result.
func claimReasons(p plan, realLedger bool, rep evalreport.Report, incomplete int, reg *evalreport.Registration) []string {
	var reasons []string
	add := func(cond bool, reason string) {
		if cond {
			reasons = append(reasons, reason)
		}
	}
	add(!realLedger, reasonLedgerSynthetic)
	add(p.cfg.QuotaProfile != quotaThesis, reasonNotThesis)
	add(!rep.Coverage.Exact || incomplete > 0, reasonRunIncomplete)
	add(rep.Groups == nil, reasonNoGroups)
	add(rep.Groups != nil && !rep.Groups.Certification.Certified, reasonNotCertified)
	add(reg == nil, reasonNotRegistered)
	if reg != nil {
		for _, m := range registrationMismatches(*reg, p) {
			reasons = append(reasons, reasonMismatchPrefix+m.field)
		}
	}
	return reasons
}

type mismatch struct{ field, registered, run string }

// registrationMismatches compares the registered record with this run: the freeze, the manifest, the quota
// profile, the plan (runner config and every step), the run binding (receipt pins, admission policy, V image,
// route and EVQ results, PROC settings with the D49 drain grace) and, when the run has an attribution file, the
// rubric that file names.
func registrationMismatches(reg evalreport.Registration, p plan) []mismatch {
	var out []mismatch
	check := func(field, registered, run string) {
		if registered != run {
			out = append(out, mismatch{field, registered, run})
		}
	}
	check("freeze_commit", reg.FreezeCommit, p.cfg.FreezeCommit)
	check("corpus_manifest_digest", reg.CorpusManifestDigest, p.digest)
	check("quota_profile", reg.QuotaProfile, p.cfg.QuotaProfile)
	check("runner_plan_digest", reg.RunnerPlanDigest, p.sched.Digest)
	check("run_binding", reg.RunBinding, p.sched.RunBinding)
	if p.attributions.rubricDigest != "" {
		check("attribution_rubric_digest", reg.AttributionRubricDigest, p.attributions.rubricDigest)
	}
	return out
}

// readInput reads a regular file of at most limit bytes.
func readInput(what, path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %s is not a regular file", what, path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %s is unreadable or larger than %d bytes", what, path, limit)
	}
	return data, nil
}

func loadRegistration(path string) (evalreport.Registration, error) {
	data, err := readInput("registration record", path, maxConfigBytes)
	if err != nil {
		return evalreport.Registration{}, err
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return evalreport.Registration{}, fmt.Errorf("registration record: %w", err)
	}
	return evalreport.ParseRegistration(data)
}

// d48Inputs reads the files behind the scenario and mechanism results: the main draw the manifest pins, from the
// artifact directory, ledger v3 and, when given, the select.py certify output. evalreport checks all three.
func d48Inputs(p plan, ledgerPath, certificationPath string) (*evalreport.D48Inputs, error) {
	if ledgerPath == "" {
		if certificationPath != "" {
			return nil, fmt.Errorf("%w: --certification needs --ledger-v3", errUsage)
		}
		return nil, nil
	}
	name, ok := corpusadmit.DrawArtifactName(p.manifest.Pins.DrawDigest)
	if !ok || p.cfg.Artifacts == "" {
		return nil, errors.New("--ledger-v3: the manifest pins no D48 draw in artifacts_dir")
	}
	in := &evalreport.D48Inputs{}
	var err error
	if in.Draw, err = readInput("draw", filepath.Join(p.cfg.Artifacts, name), evalcorpus.MaxDrawBytes); err != nil {
		return nil, err
	}
	if in.Ledger, err = readInput("ledger v3", ledgerPath, maxLedgerV3Bytes); err != nil {
		return nil, err
	}
	if certificationPath != "" {
		if in.Certification, err = readInput("certification", certificationPath, maxConfigBytes); err != nil {
			return nil, err
		}
	}
	return in, nil
}

// planCmd prints the plan and, with --registration, refuses a run that is not the registered one before any unit
// is spent.
func (a *app) planCmd(args []string) error {
	var registration string
	return a.withPlan("plan", args, func(f *flag.FlagSet) {
		f.StringVar(&registration, "registration", "", "pre-registration record (tbound-profile preregistration-record) this run must match")
	}, func(p plan) error {
		if err := a.plan(p); err != nil || registration == "" {
			return err
		}
		reg, err := loadRegistration(registration)
		if err != nil {
			return err
		}
		if bad := registrationMismatches(reg, p); len(bad) > 0 {
			lines := make([]string, len(bad))
			for i, m := range bad {
				lines[i] = fmt.Sprintf("%s registered %q, run %q", m.field, m.registered, m.run)
			}
			return fmt.Errorf("the run is not the one registered in %s: %s", reg.RecordDigest, strings.Join(lines, "; "))
		}
		fmt.Fprintf(a.out, "registration %s matches\n", reg.RecordDigest)
		return nil
	})
}
