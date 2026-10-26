// Command tbound-panel runs the blinded three-model AI construct-review panel of the D35
// amendment (2026-10-24) over a TBound corpus manifest. Every model rates every case in two
// separate fresh sessions: phase 1 classifies the case without any claimed label, phase 2
// rates the checklist with the claims shown. It then reports Gwet's AC1 (the gate statistic), raw
// agreement and Fleiss' kappa per checklist item and per phase-1 field, the phase-1 agreement with
// the claimed labels, all before adjudication, and the advisor's audit list. See docs/guides/panel-run.md.
//
// Exactly one mode is required, so nothing spends money by accident:
//
//	--estimate  print expected calls, tokens and cost; no call, no output directory
//	--dry-run   full pipeline with a deterministic fake provider; no network
//	--live      OpenRouter; the key is read from OPENROUTER_API_KEY and never written
//
// A run is resumable: rerunning with the same --out skips every case x model pair that
// already has a valid rating. The hard budget cap is checked before each call against the
// recorded provider-reported cost plus the worst case of the next call.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"tbound/supervisor/internal/panelaudit"
	"time"
)

const usageText = `tbound-panel - blinded three-model construct-review panel (D35 amendment)

usage:
  tbound-panel --config panel.json --manifest corpus/pilot/manifest.draft.json --estimate
  tbound-panel --config panel.json --manifest corpus/pilot/manifest.draft.json --out <run dir> --dry-run
  tbound-panel --config panel.json --manifest corpus/pilot/manifest.draft.json --out <run dir> --live

--live reads the OpenRouter key from OPENROUTER_API_KEY. Rerun the same command to resume.
`

var errUsage = errors.New("usage")

type app struct {
	out, errOut io.Writer
	getenv      func(string) string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	a := &app{out: os.Stdout, errOut: os.Stderr, getenv: os.Getenv}
	err := a.runCtx(ctx, os.Args[1:])
	stop()
	switch {
	case err == nil:
	case errors.Is(err, errUsage):
		fmt.Fprintln(os.Stderr, "tbound-panel:", err)
		os.Exit(2)
	case errors.Is(err, errBudget):
		fmt.Fprintln(os.Stderr, "tbound-panel:", err)
		os.Exit(3)
	default:
		fmt.Fprintln(os.Stderr, "tbound-panel:", err)
		os.Exit(1)
	}
}

func (a *app) run(args []string) error { return a.runCtx(context.Background(), args) }

func (a *app) runCtx(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tbound-panel", flag.ContinueOnError)
	fs.SetOutput(a.errOut)
	fs.Usage = func() { fmt.Fprint(a.errOut, usageText) }
	cfgPath := fs.String("config", "", "panel config JSON")
	manifest := fs.String("manifest", "", "corpus manifest (artifacts/ next to it)")
	out := fs.String("out", "", "run directory (created; reused to resume)")
	estimate := fs.Bool("estimate", false, "print expected calls, tokens and cost; call nothing")
	dry := fs.Bool("dry-run", false, "run end to end with the fake provider")
	live := fs.Bool("live", false, "run against OpenRouter (spends money)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	modes := 0
	for _, b := range []bool{*estimate, *dry, *live} {
		if b {
			modes++
		}
	}
	if fs.NArg() != 0 || modes != 1 || *cfgPath == "" || *manifest == "" || (!*estimate && *out == "") {
		fmt.Fprint(a.errOut, usageText)
		return fmt.Errorf("%w: need --config, --manifest, exactly one of --estimate/--dry-run/--live, and --out unless --estimate", errUsage)
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if err := panelaudit.ValidateModels(cfg.Models); err != nil {
		return err
	}
	c, err := loadCorpus(*manifest)
	if err != nil {
		return err
	}
	c.Policy = *cfg.PublicPolicy
	if *estimate {
		return a.estimate(cfg, c)
	}
	r := &runner{cfg: cfg, corpus: c, out: *out, prov: fakeProvider{}, provName: "fake", log: a.errOut}
	if *live {
		key := a.getenv("OPENROUTER_API_KEY")
		if key == "" {
			return errors.New("--live needs the OpenRouter key in the environment variable OPENROUTER_API_KEY")
		}
		r.prov = &openRouter{endpoint: openRouterEndpoint, key: key, client: &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}}
		r.provName = "openrouter"
	}
	sum, err := r.run(ctx)
	fmt.Fprintf(a.out, "calls this run: %d (valid %d, invalid %d); spent %.4f USD of cap %.2f USD (%.4f uncertain)\n",
		sum.Calls, sum.Valid, sum.Invalid, sum.Spent.total(), cfg.BudgetUSD, sum.Spent.uncertain)
	if md, rerr := os.ReadFile(filepath.Join(*out, "stats.md")); rerr == nil {
		a.out.Write(md)
	}
	fmt.Fprintf(a.out, "\noutputs: %s (receipt.json, ratings.jsonl, stats.md, audit.md)\n", *out)
	return err
}

// expectedCompletionTokens is the typical answer size used for the expected (not worst) cost.
// shortcut: a guess for six short reasons or one classification; replace with the dry-run mean once a live pilot exists.
const expectedCompletionTokens = 800

func (a *app) estimate(cfg panelConfig, c corpus) error {
	var ids []string
	for _, m := range cfg.Models {
		ids = append(ids, m.ID)
	}
	plan, err := blind(c, ids, cfg.Seed)
	if err != nil {
		return err
	}
	n := len(c.Manifest.Cases) * len(phases)
	fmt.Fprintf(a.out, "%d cases x %d models x %d phases = %d calls (one fresh request each; retries of invalid answers are extra)\n\n",
		len(c.Manifest.Cases), len(cfg.Models), len(phases), n*len(cfg.Models))
	fmt.Fprintln(a.out, "| Model | Calls | Prompt bytes | Est. prompt tokens (bytes/4) | Est. completion tokens | Expected USD | Worst-case USD |")
	fmt.Fprintln(a.out, "|---|---|---|---|---|---|---|")
	var expTotal, worstTotal float64
	for _, m := range cfg.Models {
		bytesTotal, worst := 0, 0.0
		for _, phase := range phases {
			for _, cs := range c.Manifest.Cases {
				p, err := renderCase(c, cs, plan.ids[raterKey(phase, m.ID)][cs.CaseID], phase)
				if err != nil {
					return err
				}
				body, _ := json.MarshalIndent(newRequest(m, phase, p.User), "", "  ")
				bytesTotal += len(body)
				worst += worstCaseUSD(m, body)
			}
		}
		promptTok, complTok := bytesTotal/4, n*expectedCompletionTokens
		exp := (float64(promptTok)*m.PromptUSDPerMTok + float64(complTok)*m.CompletionUSDPerMTok) / 1e6
		expTotal += exp
		worstTotal += worst
		fmt.Fprintf(a.out, "| %s | %d | %d | %d | %d | %.4f | %.4f |\n", m.ID, n, bytesTotal, promptTok, complTok, exp, worst)
	}
	fmt.Fprintf(a.out, "\nexpected total %.4f USD; worst-case total %.4f USD (every prompt byte a token, every answer at max_tokens); cap %.2f USD\n",
		expTotal, worstTotal, cfg.BudgetUSD)
	return nil
}
