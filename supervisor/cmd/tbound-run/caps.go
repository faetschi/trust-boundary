package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/runner"
)

const pilotCapsSchema = "tbound-pilot-caps/v1"

type pilotCapsDoc struct {
	Schema         string                  `json:"schema"`
	PlanDigest     string                  `json:"plan_digest"`
	ManifestDigest string                  `json:"manifest_digest"`
	AttackerDigest string                  `json:"attacker_digest"`
	Cases          []calibration.PilotCase `json:"cases"`
	PivotStates    int                     `json:"pivot_states"`
	Q95            int                     `json:"q95_attempts_to_manifest"`
	PilotMaximum   attacker.SpecCaps       `json:"pilot_maximum"`
	Caps           attacker.SpecCaps       `json:"caps"`
}

func (a *app) capsCmd(args []string) error {
	var out string
	return a.withPlan("caps", args, func(f *flag.FlagSet) {
		f.StringVar(&out, "out", "", "caps.json to write ("+pilotCapsSchema+"); it must not exist")
	}, func(p plan) error {
		if out == "" {
			return fmt.Errorf("%w: caps needs --out", errUsage)
		}
		if p.manifest.Pins.AttackerDigest == "" {
			return errors.New("caps: the manifest pins no attacker artifact, so there is no pivot library to count L from")
		}
		if !p.sched.Config.AdaptiveCaps.Scripted {
			return errors.New("caps: the pilot's adaptive caps are not scripted; this command derives the caps of the scripted attacker only")
		}
		spec, err := corpusadmit.LoadAttacker(os.DirFS(p.cfg.Artifacts), p.manifest.Pins.AttackerDigest)
		if err != nil {
			return fmt.Errorf("caps: pins.attacker_digest: %w", err)
		}
		j, err := existingJournal(p)
		if err != nil {
			return err
		}
		defer j.Close()
		cases := runner.PilotCases(p.sched, j)
		d, err := calibration.DerivePilotCaps(cases, pivotStates(spec.Library), true)
		for _, c := range cases {
			fmt.Fprintf(a.errOut, "%s %s attempts %d %s\n", c.CaseID, c.Status, c.Adaptive.Attempts, c.Reason)
		}
		if err != nil {
			return err
		}
		doc := pilotCapsDoc{Schema: pilotCapsSchema, PlanDigest: p.sched.Digest, ManifestDigest: p.digest,
			AttackerDigest: p.manifest.Pins.AttackerDigest, Cases: cases, PivotStates: d.L, Q95: d.Q95,
			PilotMaximum: specCaps(d.PilotMax), Caps: specCaps(d.Caps)}
		raw, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.Write(raw); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "caps %s: L %d, q95 %d, iterations %d, tool_calls %d, wall_ms %d; sha256:%x\n",
			out, d.L, d.Q95, d.Caps.Iterations, d.Caps.ToolCalls, d.Caps.WallMS, sha256.Sum256(raw))
		return nil
	})
}

func pivotStates(l attacker.SpecLibrary) int {
	seen := map[string]bool{}
	for _, c := range l.Classes {
		for _, st := range c.Steps {
			seen[st] = true
		}
	}
	return len(seen)
}

func specCaps(c calibration.Caps) attacker.SpecCaps {
	return attacker.SpecCaps{Scripted: c.Scripted, Iterations: int64(c.Iterations), ToolCalls: int64(c.ToolCalls),
		WallMS: int64(c.WallMS), InTokens: int64(c.InTokens), OutTokens: int64(c.OutTokens), MicroUSD: c.MicroUSD}
}
