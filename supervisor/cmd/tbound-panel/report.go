package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"tbound/supervisor/internal/evalcorpus"
)

const humanNote = "AI panel ratings are not human reviews and never count as one; only the advisor's signed audit entries (audit.json) are human review."

// finish writes stats, audit list and receipt from whatever ratings exist, so an interrupted
// or budget-stopped run still reports its coverage honestly.
func (r *runner) finish(plan blindPlan) (spend, error) {
	spent, err := loadSpend(filepath.Join(r.out, "calls.jsonl"))
	if err != nil {
		return spent, err
	}
	byCase := map[string]map[string]rating{}
	calls := 0
	if err := readJSONL(filepath.Join(r.out, "ratings.jsonl"), func(raw []byte) error {
		var rt rating
		if err := json.Unmarshal(raw, &rt); err != nil {
			return err
		}
		calls++
		if rt.Valid {
			if byCase[rt.CaseID] == nil {
				byCase[rt.CaseID] = map[string]rating{}
			}
			byCase[rt.CaseID][rt.Model] = rt
		}
		return nil
	}); err != nil {
		return spent, err
	}
	cases := append([]evalcorpus.Case(nil), r.corpus.Manifest.Cases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].CaseID < cases[j].CaseID })
	complete := func(c evalcorpus.Case) bool { return len(byCase[c.CaseID]) == len(r.cfg.Models) }
	ratingsOf := func(c evalcorpus.Case, item string) []string {
		var out []string
		for _, m := range r.cfg.Models {
			out = append(out, byCase[c.CaseID][m.ID].Checklist[item].Rating)
		}
		return out
	}
	stats := map[string]ItemStats{}
	for _, it := range items {
		var subs []subject
		for _, c := range cases {
			if _, na := notApplicable[c.Kind][it.key]; !na && complete(c) {
				subs = append(subs, subject{root: root(c), ratings: ratingsOf(c, it.key)})
			}
		}
		stats[it.key] = itemStats(subs, r.cfg.Seed, r.cfg.BootstrapResamples)
	}
	var incomplete []string
	for _, c := range cases {
		if !complete(c) {
			incomplete = append(incomplete, c.CaseID)
		}
	}
	var models []string
	for _, m := range r.cfg.Models {
		models = append(models, m.ID)
	}
	statsDoc := map[string]any{
		"schema": "tbound-panel-stats/v1", "status": "pre-adjudication, rating round 1", "provider": r.provName,
		"manifest_digest": r.corpus.Digest, "models": models, "categories": categories,
		"raw_agreement_definition": agreementDefinition, "bootstrap": bootstrapMethod,
		"bootstrap_seed": r.cfg.Seed, "bootstrap_resamples": r.cfg.BootstrapResamples, "gate_rule": gateRule,
		"cases": len(cases), "complete_cases": len(cases) - len(incomplete), "incomplete_cases": incomplete,
		"not_applicable": notApplicable, "items": stats, "note": humanNote,
	}
	if err := writeJSON(filepath.Join(r.out, "stats.json"), statsDoc); err != nil {
		return spent, err
	}
	if err := writeFile(filepath.Join(r.out, "stats.md"), []byte(statsMarkdown(stats, len(cases), incomplete, r))); err != nil {
		return spent, err
	}
	if err := r.writeAudit(cases, byCase, complete, ratingsOf); err != nil {
		return spent, err
	}
	var modelReceipts []map[string]any
	for _, m := range r.cfg.Models {
		seen := map[string]bool{}
		for _, per := range byCase {
			if rt, ok := per[m.ID]; ok {
				seen[rt.ResponseModel+" via "+rt.ResponseHost] = true
			}
		}
		var observed []string
		for k := range seen {
			observed = append(observed, k)
		}
		sort.Strings(observed)
		modelReceipts = append(modelReceipts, map[string]any{"id": m.ID, "family": family(m.ID), "temperature": m.Temperature,
			"max_tokens": m.MaxTokens, "prompt_usd_per_mtok": m.PromptUSDPerMTok, "completion_usd_per_mtok": m.CompletionUSDPerMTok,
			"observed_response_models": observed})
	}
	receipt := map[string]any{
		"schema": "tbound-panel-receipt/v1", "tool": "supervisor/cmd/tbound-panel", "provider": r.provName,
		"dry_run": r.provName == "fake", "finished_at": time.Now().UTC().Format(time.RFC3339),
		"manifest_canonical_digest": r.corpus.Digest, "manifest_file_sha256": r.corpus.FileSHA256,
		"models": modelReceipts, "excluded_families": excludedTokens,
		"seeds":                map[string]any{"blind_ids_and_order": r.cfg.Seed, "bootstrap": r.cfg.Seed, "audit_sample": strconv.FormatUint(r.cfg.Seed, 10)},
		"system_prompt_sha256": sha(renderedSystemPrompt), "settings_file": "settings.json",
		"budget_cap_usd": r.cfg.BudgetUSD, "spent_usd": spent.total(), "spent_settled_usd": spent.settled, "spent_uncertain_usd": spent.uncertain,
		"calls_recorded": calls, "cases": len(cases), "complete_cases": len(cases) - len(incomplete),
		"key_handling": "OPENROUTER_API_KEY read from the environment at run time, sent only as the HTTP Authorization header, never written",
		"note":         humanNote,
	}
	return spent, writeJSON(filepath.Join(r.out, "receipt.json"), receipt)
}

func statsMarkdown(stats map[string]ItemStats, cases int, incomplete []string, r *runner) string {
	var b strings.Builder
	b.WriteString("# Construct panel agreement, before adjudication\n\n")
	fmt.Fprintf(&b, "Provider: `%s`. Manifest: `%s`. Complete cases: %d of %d.\n\n", r.provName, r.corpus.Digest, cases-len(incomplete), cases)
	if r.provName == "fake" {
		b.WriteString("**Dry run: these are canned fake ratings, not model judgments.**\n\n")
	}
	b.WriteString(kappaTable(stats))
	fmt.Fprintf(&b, "\n- Raw agreement: %s.\n- Bootstrap: %s; seed %d, %d resamples.\n- Gate: %s.\n- %s\n",
		agreementDefinition, bootstrapMethod, r.cfg.Seed, r.cfg.BootstrapResamples, gateRule, humanNote)
	if len(incomplete) > 0 {
		fmt.Fprintf(&b, "- Incomplete cases (not every model gave a valid rating): %s.\n", strings.Join(incomplete, ", "))
	}
	return b.String()
}

func kappaTable(stats map[string]ItemStats) string {
	var b strings.Builder
	b.WriteString("| Item | Subjects | Roots | Kappa | 95% CI | Raw agreement | Unanimous | Pass/Fail/Unclear | Gate |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, it := range items {
		s := stats[it.key]
		k, ci := "undefined", "n/a"
		if s.KappaDefined {
			k = fmt.Sprintf("%.3f", s.Kappa)
		}
		if s.CIDefined {
			ci = fmt.Sprintf("%.3f to %.3f", s.CILow, s.CIHigh)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %.3f | %.3f | %.2f/%.2f/%.2f | %s |\n", it.key, s.Subjects, s.Roots, k, ci,
			s.Agreement, s.Unanimous, s.Prevalence[0], s.Prevalence[1], s.Prevalence[2], s.Gate)
	}
	return b.String()
}

func (r *runner) writeAudit(cases []evalcorpus.Case, byCase map[string]map[string]rating, complete func(evalcorpus.Case) bool, ratingsOf func(evalcorpus.Case, string) []string) error {
	reasons := map[string][]string{}
	unanimousRoots := map[string]bool{}
	for _, c := range cases {
		var why []string
		if c.Kind == evalcorpus.KindAmbiguous {
			why = append(why, "ambiguous case")
		}
		if !complete(c) {
			why = append(why, "incomplete panel ratings")
		} else {
			for _, it := range items {
				if _, na := notApplicable[c.Kind][it.key]; na {
					continue
				}
				rs := ratingsOf(c, it.key)
				for _, x := range rs {
					if x != rs[0] {
						why = append(why, "disagreement on "+it.key)
						break
					}
				}
				for _, x := range rs {
					if x == "unclear" {
						why = append(why, "unclear on "+it.key)
						break
					}
				}
			}
			var classes, auths []string
			for _, m := range r.cfg.Models {
				in := byCase[c.CaseID][m.ID].Independent
				classes, auths = append(classes, in.ThreatClass), append(auths, in.Authorization)
			}
			if !same(classes) || !same(auths) {
				why = append(why, "independent classification disagreement")
			}
		}
		reasons[c.CaseID] = why
		rt := root(c)
		if _, seen := unanimousRoots[rt]; !seen {
			unanimousRoots[rt] = true
		}
		unanimousRoots[rt] = unanimousRoots[rt] && len(why) == 0
	}
	var eligible []string
	for rt, ok := range unanimousRoots {
		if ok {
			eligible = append(eligible, rt)
		}
	}
	sort.Strings(eligible)
	k := min(r.cfg.AuditSample, len(eligible))
	seed := strconv.FormatUint(r.cfg.Seed, 10)
	selected, err := evalcorpus.SelectSeeded(seed, "tbound-panel/unanimous-audit", eligible, k)
	if err != nil && len(eligible) > 0 {
		return err
	}
	sel := map[string]bool{}
	for _, s := range selected {
		sel[s] = true
	}
	type auditCase struct {
		CaseID  string                           `json:"case_id"`
		Kind    evalcorpus.Kind                  `json:"kind"`
		Root    string                           `json:"root"`
		Reasons []string                         `json:"reasons"`
		Packet  string                           `json:"packet"`
		Panel   map[string]map[string]itemRating `json:"ai_panel_ratings"`
		Indep   map[string]*independent          `json:"ai_panel_independent"`
		Advisor map[string]string                `json:"advisor_signoff"`
	}
	var list []auditCase
	for _, c := range cases {
		why := reasons[c.CaseID]
		if len(why) == 0 && sel[root(c)] {
			why = []string{"random unanimous sample"}
		}
		if len(why) == 0 {
			continue
		}
		p, err := renderCase(r.corpus, c, "advisor-review")
		if err != nil {
			return err
		}
		pkt := filepath.ToSlash(filepath.Join("audit-packets", c.CaseID+".md"))
		if err := writeFile(filepath.Join(r.out, pkt), []byte("# "+c.CaseID+"\n\n"+p.User)); err != nil {
			return err
		}
		ac := auditCase{CaseID: c.CaseID, Kind: c.Kind, Root: root(c), Reasons: why, Packet: pkt,
			Panel: map[string]map[string]itemRating{}, Indep: map[string]*independent{},
			Advisor: map[string]string{"decision (accept|reject|revise)": "", "resolution and reason": "", "advisor identity": "", "signed at (with timezone)": ""}}
		for m, rt := range byCase[c.CaseID] {
			ac.Panel[m], ac.Indep[m] = rt.Checklist, rt.Independent
		}
		list = append(list, ac)
	}
	doc := map[string]any{
		"schema": "tbound-panel-audit/v1", "manifest_digest": r.corpus.Digest, "provider": r.provName,
		"selection": map[string]any{"seed": seed, "purpose": "tbound-panel/unanimous-audit", "method": "evalcorpus.SelectSeeded",
			"sample_size": r.cfg.AuditSample, "eligible_roots": eligible, "selected_roots": selected},
		"cases": list, "human_reviews_recorded": 0, "note": humanNote,
	}
	if err := writeJSON(filepath.Join(r.out, "audit.json"), doc); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Advisor audit list, construct panel\n\n")
	if r.provName == "fake" {
		b.WriteString("**Dry run: the panel ratings are canned fake data. Do not sign.**\n\n")
	}
	fmt.Fprintf(&b, "%s\n\nManifest `%s`. This list holds every disagreement, every `unclear`, every ambiguous case, incomplete ratings, and %d of %d eligible unanimous roots drawn with seed `%s` (evalcorpus.SelectSeeded, purpose `tbound-panel/unanimous-audit`). Statistics before adjudication are in `stats.md`.\n\n",
		humanNote, r.corpus.Digest, len(selected), len(eligible), seed)
	b.WriteString("For each case: read its packet, the three AI ratings in `audit.json`, decide, and fill the sign-off fields in `audit.json`. Any audit error expands review across the case's source/tool stratum.\n\n| Case | Kind | Reasons | Packet |\n|---|---|---|---|\n")
	for _, ac := range list {
		fmt.Fprintf(&b, "| %s | %s | %s | `%s` |\n", ac.CaseID, ac.Kind, strings.Join(ac.Reasons, "; "), ac.Packet)
	}
	b.WriteString("\nAdvisor signature (name, date, timezone): ______________________\n")
	return writeFile(filepath.Join(r.out, "audit.md"), []byte(b.String()))
}

func same(xs []string) bool {
	for _, x := range xs {
		if x != xs[0] {
			return false
		}
	}
	return true
}
