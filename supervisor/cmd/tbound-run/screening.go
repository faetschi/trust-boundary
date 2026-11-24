package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/runner"
)

const screeningReportSchema = "tbound-screening-report/v1"

var screeningSuffix = regexp.MustCompile(`\.s[0-9]+$`)

type screeningReportRow struct {
	ScreeningID string              `json:"screening_id"`
	BaseID      string              `json:"base_id"`
	Tool        evalcorpus.Tool     `json:"tool"`
	End         runner.ScreeningEnd `json:"end"`
	Reason      string              `json:"reason,omitempty"`
}

type screeningReportDoc struct {
	Schema         string                      `json:"schema"`
	PlanDigest     string                      `json:"plan_digest"`
	ManifestDigest string                      `json:"manifest_digest"`
	Rows           []screeningReportRow        `json:"rows"`
	Ends           map[runner.ScreeningEnd]int `json:"ends"`
}

func (a *app) screeningReportCmd(args []string) error {
	var out string
	return a.withPlan("screening-report", args, func(f *flag.FlagSet) {
		f.StringVar(&out, "out", "", "screening-report.json to write ("+screeningReportSchema+"); it must not exist")
	}, func(p plan) error {
		if out == "" {
			return fmt.Errorf("%w: screening-report needs --out", errUsage)
		}
		j, err := existingJournal(p)
		if err != nil {
			return err
		}
		defer j.Close()
		rows, err := runner.ScreeningRows(p.sched, j)
		if err != nil {
			return err
		}
		tools := map[string]evalcorpus.Tool{}
		for _, c := range p.manifest.Cases {
			if c.Compliant != nil {
				tools[c.CaseID] = c.Compliant.PrimaryTool
			}
		}
		doc := screeningReportDoc{Schema: screeningReportSchema, PlanDigest: p.sched.Digest, ManifestDigest: p.digest,
			Ends: map[runner.ScreeningEnd]int{}}
		for _, r := range rows {
			doc.Rows = append(doc.Rows, screeningReportRow{ScreeningID: r.CaseID, BaseID: screeningSuffix.ReplaceAllString(r.CaseID, ""),
				Tool: tools[r.CaseID], End: r.End, Reason: r.Reason})
			doc.Ends[r.End]++
		}
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
		fmt.Fprintf(a.out, "screening-report %s: %d tasks, %d admitted; sha256:%x\n", out, len(rows), doc.Ends[runner.ScreeningAdmitted], sha256.Sum256(raw))
		return nil
	})
}
