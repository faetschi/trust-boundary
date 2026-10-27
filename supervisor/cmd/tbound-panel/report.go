package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/panelaudit"
)

const humanNote = "AI panel ratings are not human reviews and never count as one; only the advisor's signed audit entries (audit.json) are human review."

// blindFields are the phase-1 fields, with fixed categories where the answer space is closed.
var blindFields = []struct {
	key  string
	cats []string // nil: the observed values (effect-id sets)
}{
	{"threat_class", panelaudit.ThreatClasses},
	{"authorization", panelaudit.Authorizations},
	{"effect_ids", nil},
}

const effectIDsRule = "effect_ids compared as a set: exact multi-label match of the sorted ids, \"none\" for the empty set"

// claimAgreement is the share of phase-1 answers (case x model) equal to the claimed value.
type claimAgreement struct {
	Agreeing      int               `json:"agreeing"`
	Total         int               `json:"total"`
	Rate          float64           `json:"rate"`
	NotApplicable map[string]string `json:"not_applicable,omitempty"`
}

// panelData holds the latest valid rating per phase, case and model.
type panelData struct {
	r       *runner
	cases   []evalcorpus.Case
	ratings map[int]map[string]map[string]rating
}

func (d panelData) complete(phase int, c evalcorpus.Case) bool {
	return len(d.ratings[phase][c.CaseID]) == len(d.r.cfg.Models)
}

func (d panelData) blindValues(c evalcorpus.Case, field string) []string {
	var out []string
	for _, m := range d.r.cfg.Models {
		out = append(out, panelaudit.FieldValue(d.ratings[phaseBlind][c.CaseID][m.ID].Independent, field))
	}
	return out
}

// refuseToEraseSignoffs stops a rerun from rebuilding audit.json over the advisor's decisions.
// An existing audit that cannot be read is refused too, because it may hold a half-edited sign-off.
func (r *runner) refuseToEraseSignoffs() error {
	path := filepath.Join(r.out, "audit.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var existing struct {
		Cases []panelaudit.AuditCase `json:"cases"`
	}
	// ValidateEvidenceJSON refuses duplicate members, which would let a later "cases" hide a signed one.
	if err := errors.Join(protocol.ValidateEvidenceJSON(raw), json.Unmarshal(raw, &existing)); err != nil {
		return fmt.Errorf("%s exists but cannot be read (%w); move it aside before regenerating", path, err)
	}
	for _, c := range existing.Cases {
		if c.Advisor != (panelaudit.Advisor{}) {
			return fmt.Errorf("%s already carries advisor sign-offs (case %s); move it aside or copy the output directory before regenerating", path, c.CaseID)
		}
	}
	return nil
}

// finish writes stats, receipt and audit list from whatever ratings exist, so an interrupted
// or budget-stopped run still reports its coverage honestly.
func (r *runner) finish(plan blindPlan) (spend, error) {
	spent, err := loadSpend(filepath.Join(r.out, "calls.jsonl"))
	if err != nil {
		return spent, err
	}
	if err := r.refuseToEraseSignoffs(); err != nil {
		return spent, err
	}
	d := panelData{r: r, ratings: map[int]map[string]map[string]rating{phaseBlind: {}, phaseChecklist: {}}}
	calls := 0
	if err := readJSONL(filepath.Join(r.out, "ratings.jsonl"), func(raw []byte) error {
		var rt rating
		if err := json.Unmarshal(raw, &rt); err != nil {
			return err
		}
		calls++
		if rt.Valid {
			per := d.ratings[rt.Phase]
			if per[rt.CaseID] == nil {
				per[rt.CaseID] = map[string]rating{}
			}
			per[rt.CaseID][rt.Model] = rt
		}
		return nil
	}); err != nil {
		return spent, err
	}
	d.cases = append([]evalcorpus.Case(nil), r.corpus.Manifest.Cases...)
	sort.Slice(d.cases, func(i, j int) bool { return d.cases[i].CaseID < d.cases[j].CaseID })

	var models []string
	for _, m := range r.cfg.Models {
		models = append(models, m.ID)
	}
	outcomes := d.outcomes()
	items2 := panelaudit.ChecklistStats(d.cases, outcomes, models, r.cfg.Seed, r.cfg.BootstrapResamples)
	fields := map[string]panelaudit.ItemStats{}
	agree := map[string]claimAgreement{}
	for _, f := range blindFields {
		subs := panelaudit.Subjects(d.cases, outcomes, models, phaseBlind, f.key)
		seen := map[string]bool{}
		for _, s := range subs {
			for _, v := range s.Ratings {
				seen[v] = true
			}
		}
		ca := claimAgreement{}
		for _, c := range d.cases {
			if want, ok := panelaudit.Claim(c, f.key); ok && d.complete(phaseBlind, c) {
				for _, v := range d.blindValues(c, f.key) {
					ca.Total++
					if v == want {
						ca.Agreeing++
					}
				}
			}
		}
		cats := f.cats
		if cats == nil {
			for v := range seen {
				cats = append(cats, v)
			}
			sort.Strings(cats)
		}
		fields[f.key] = panelaudit.ComputeItem(subs, cats, r.cfg.Seed, r.cfg.BootstrapResamples)
		if ca.Total > 0 {
			ca.Rate = float64(ca.Agreeing) / float64(ca.Total)
		}
		for kind, na := range panelaudit.ClaimNA {
			if reason, ok := na[f.key]; ok {
				if ca.NotApplicable == nil {
					ca.NotApplicable = map[string]string{}
				}
				ca.NotApplicable[string(kind)] = reason
			}
		}
		agree[f.key] = ca
	}
	// Missing answers (refusals, broken JSON, budget stop) void every gate: no pass claim.
	var missing []map[string]any
	for _, phase := range phases {
		for _, m := range r.cfg.Models {
			for _, c := range d.cases {
				if _, ok := d.ratings[phase][c.CaseID][m.ID]; !ok {
					missing = append(missing, map[string]any{"phase": phase, "model": m.ID, "case_id": c.CaseID})
				}
			}
		}
	}
	if len(missing) > 0 {
		for _, st := range []map[string]panelaudit.ItemStats{items2, fields} {
			for k, v := range st {
				v.Gate = panelaudit.GateIncomplete
				st[k] = v
			}
		}
	}
	var incomplete []string
	for _, c := range d.cases {
		if !d.complete(phaseBlind, c) || !d.complete(phaseChecklist, c) {
			incomplete = append(incomplete, c.CaseID)
		}
	}
	statsDoc := map[string]any{
		"schema": panelaudit.StatsSchema, "content_digest": r.corpus.ContentDigest, "dry_run": r.provName == "fake", "status": "pre-adjudication, rating round 1", "provider": r.provName,
		"manifest_digest": r.corpus.Digest, "models": models,
		"raw_agreement_definition": agreementDefinition, "bootstrap": bootstrapMethod,
		"bootstrap_seed": r.cfg.Seed, "bootstrap_resamples": r.cfg.BootstrapResamples, "gate_rule": gateRule,
		"cases": len(d.cases), "complete_cases": len(d.cases) - len(incomplete), "incomplete_cases": incomplete,
		"missing_pairs": missing, "not_applicable": panelaudit.NotApplicable, "items": items2,
		"independent_fields": fields, "effect_ids_rule": effectIDsRule,
		"blind_agreement_with_claim": agree, "note": humanNote,
	}
	if err := writeReport(filepath.Join(r.out, "stats.json"), panelaudit.Stats{Models: models, Schema: panelaudit.StatsSchema, ContentDigest: r.corpus.ContentDigest, DryRun: r.provName == "fake", LiveRun: r.provName == "openrouter", Provider: r.provName, Cases: len(d.cases), CompleteCases: len(d.cases) - len(incomplete), Incomplete: incomplete, Missing: missing, Items: items2}, statsDoc); err != nil {
		return spent, err
	}
	if err := writeFile(filepath.Join(r.out, "stats.md"), []byte(statsMarkdown(items2, fields, agree, len(d.cases), incomplete, r))); err != nil {
		return spent, err
	}
	var modelReceipts []map[string]any
	for _, m := range r.cfg.Models {
		seen := map[string]bool{}
		for _, perPhase := range d.ratings {
			for _, per := range perPhase {
				if rt, ok := per[m.ID]; ok {
					seen[rt.ResponseModel+" via "+rt.ResponseHost] = true
				}
			}
		}
		var observed []string
		for k := range seen {
			observed = append(observed, k)
		}
		sort.Strings(observed)
		modelReceipts = append(modelReceipts, map[string]any{"id": m.ID, "family": panelaudit.Family(m.ID), "temperature": m.Temperature,
			"max_tokens": m.MaxTokens, "prompt_usd_per_mtok": m.PromptUSDPerMTok, "completion_usd_per_mtok": m.CompletionUSDPerMTok,
			"observed_response_models": observed})
	}
	receipt := map[string]any{
		"schema": panelaudit.ReceiptSchema, "tool": "supervisor/cmd/tbound-panel", "provider": r.provName,
		"dry_run": r.provName == "fake", "finished_at": time.Now().UTC().Format(time.RFC3339),
		"manifest_canonical_digest": r.corpus.Digest, "content_digest": r.corpus.ContentDigest, "manifest_file_sha256": r.corpus.FileSHA256,
		"models": modelReceipts, "excluded_families": panelaudit.ExcludedTokens, "public_policy": r.cfg.PublicPolicy,
		"phases":               "1 blind classification without claims, 2 checklist with claims; separate fresh sessions",
		"seeds":                map[string]any{"blind_ids_and_order": r.cfg.Seed, "bootstrap": r.cfg.Seed, "audit_sample": strconv.FormatUint(r.cfg.Seed, 10)},
		"system_prompt_sha256": map[string]string{"phase1": sha(systemPrompts[phaseBlind]), "phase2": sha(systemPrompts[phaseChecklist])},
		"settings_file":        "settings.json",
		"budget_cap_usd":       r.cfg.BudgetUSD, "spent_usd": spent.total(), "spent_settled_usd": spent.settled, "spent_uncertain_usd": spent.uncertain,
		"calls_recorded": calls, "cases": len(d.cases), "complete_cases": len(d.cases) - len(incomplete),
		"key_handling": "OPENROUTER_API_KEY read from the environment at run time, sent only as the HTTP Authorization header, never written",
		"note":         humanNote,
	}
	core := panelaudit.Receipt{Schema: panelaudit.ReceiptSchema, ContentDigest: r.corpus.ContentDigest, DryRun: r.provName == "fake", LiveRun: r.provName == "openrouter", Provider: r.provName, Models: r.cfg.Models, PublicPolicy: *r.cfg.PublicPolicy, CallsRecorded: calls, Cases: len(d.cases), CompleteCases: len(d.cases) - len(incomplete), Outcomes: d.outcomes()}
	core.Seeds.AuditSample = strconv.FormatUint(r.cfg.Seed, 10)
	core.Seeds.BlindIDsAndOrder = r.cfg.Seed
	core.Seeds.Bootstrap = r.cfg.Seed
	// Keep the observed model metadata alongside the shared model config.
	for i := range core.Models {
		core.Models[i].Family = panelaudit.Family(core.Models[i].ID)
		core.Models[i].ObservedResponseModels = modelReceipts[i]["observed_response_models"].([]string)
	}
	// The receipt goes first: the audit names the exact bytes written here, so a signed audit cannot back another run.
	receiptPath := filepath.Join(r.out, "receipt.json")
	if err := writeReport(receiptPath, core, receipt); err != nil {
		return spent, err
	}
	written, err := os.ReadFile(receiptPath)
	if err != nil {
		return spent, err
	}
	return spent, r.writeAudit(d, panelaudit.Digest(written))
}

func statsMarkdown(items2, fields map[string]panelaudit.ItemStats, agree map[string]claimAgreement, cases int, incomplete []string, r *runner) string {
	var b strings.Builder
	b.WriteString("# Construct panel agreement, before adjudication\n\n")
	fmt.Fprintf(&b, "Provider: `%s`. Manifest: `%s`. Complete cases (both phases): %d of %d.\n\n", r.provName, r.corpus.Digest, cases-len(incomplete), cases)
	if r.provName == "fake" {
		b.WriteString("**Dry run: these are canned fake ratings, not model judgments.**\n\n")
	}
	var keys []string
	for _, it := range panelaudit.Checklist {
		keys = append(keys, it.Key)
	}
	b.WriteString("## Phase 2: checklist items (claims shown)\n\n" + agreementTable(keys, items2))
	keys = nil
	for _, f := range blindFields {
		keys = append(keys, f.key)
	}
	b.WriteString("\n## Phase 1: blind classification (no claims shown)\n\n" + agreementTable(keys, fields))
	b.WriteString("\n## Phase 1: blind agreement with the claimed label\n\n| Field | Agreeing | Answers | Rate | Not applicable |\n|---|---|---|---|---|\n")
	for _, k := range keys {
		a := agree[k]
		var na []string
		for kind := range a.NotApplicable {
			na = append(na, kind)
		}
		sort.Strings(na)
		fmt.Fprintf(&b, "| %s | %d | %d | %.3f | %s |\n", k, a.Agreeing, a.Total, a.Rate, strings.Join(na, ", "))
	}
	fmt.Fprintf(&b, "\n- Raw agreement: %s.\n- Bootstrap: %s; seed %d, %d resamples.\n- Gate: %s.\n- %s; the claim rate counts every model's phase-1 answer against the manifest claim.\n- %s\n",
		agreementDefinition, bootstrapMethod, r.cfg.Seed, r.cfg.BootstrapResamples, gateRule, effectIDsRule, humanNote)
	if len(incomplete) > 0 {
		fmt.Fprintf(&b, "- **Incomplete: every gate is `%s`.** Cases without a valid answer from every model in both phases: %s. The missing case x model x phase pairs are listed in `stats.json` (`missing_pairs`).\n", panelaudit.GateIncomplete, strings.Join(incomplete, ", "))
	}
	return b.String()
}

func agreementTable(keys []string, stats map[string]panelaudit.ItemStats) string {
	var b strings.Builder
	b.WriteString("| Item | Subjects | Roots | AC1 | AC1 95% CI | Raw agreement | Kappa (reported) | Kappa 95% CI | Unanimous | Prevalence | Gate |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, key := range keys {
		s := stats[key]
		a, aci, k, kci := "undefined", "n/a", "undefined", "n/a"
		if s.AC1Defined {
			a = fmt.Sprintf("%.3f", s.AC1)
		}
		if s.AC1CIDefined {
			aci = fmt.Sprintf("%.3f to %.3f", s.AC1CILow, s.AC1CIHigh)
		}
		if s.KappaDefined {
			k = fmt.Sprintf("%.3f", s.Kappa)
		}
		if s.KappaCIDefined {
			kci = fmt.Sprintf("%.3f to %.3f", s.KappaCILow, s.KappaCIHigh)
		}
		var prev []string
		for c, p := range s.Prevalence {
			if p > 0 {
				prev = append(prev, fmt.Sprintf("%s %.2f", c, p))
			}
		}
		sort.Strings(prev)
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %.3f | %s | %s | %.3f | %s | %s |\n", key, s.Subjects, s.Roots, a, aci,
			s.Agreement, k, kci, s.Unanimous, strings.Join(prev, ", "), s.Gate)
	}
	return b.String()
}

// outcomes lists every valid rating as the receipt records it.
func (d panelData) outcomes() []panelaudit.Outcome {
	var out []panelaudit.Outcome
	for _, phase := range phases {
		for _, c := range d.cases {
			for _, m := range d.r.cfg.Models {
				rt, ok := d.ratings[phase][c.CaseID][m.ID]
				if !ok {
					continue
				}
				o := panelaudit.Outcome{Phase: phase, CaseID: c.CaseID, Model: m.ID, Values: map[string]string{}}
				if phase == phaseBlind {
					for _, key := range panelaudit.BlindFields {
						o.Values[key] = panelaudit.FieldValue(rt.Independent, key)
					}
				} else {
					for key, it := range rt.Checklist {
						o.Values[key] = it.Rating
					}
				}
				out = append(out, o)
			}
		}
	}
	return out
}

// auditReasons lists why a case needs the advisor's deep review; empty means unanimous.
func (d panelData) auditReasons(c evalcorpus.Case) []string {
	return panelaudit.ReviewReasons(c, d.outcomes())
}

func (r *runner) writeAudit(d panelData, receiptDigest string) error {
	reasons := map[string][]string{}
	for _, c := range d.cases {
		reasons[c.CaseID] = d.auditReasons(c)
	}
	seed := strconv.FormatUint(r.cfg.Seed, 10)
	selection, err := panelaudit.SelectAudit(d.cases, reasons, seed, r.cfg.AuditSample)
	if err != nil {
		return err
	}
	selected, eligible := selection.SelectedRoots, selection.EligibleRoots
	sel := map[string]bool{}
	for _, s := range selected {
		sel[s] = true
	}
	type auditCase struct {
		panelaudit.AuditCase
		Kind      evalcorpus.Kind                    `json:"kind"`
		Root      string                             `json:"root"`
		Packet    string                             `json:"packet"`
		Claims    map[string]string                  `json:"claimed_labels"`
		Blind     map[string]*panelaudit.Independent `json:"ai_panel_phase1_blind"`
		Checklist map[string]map[string]itemRating   `json:"ai_panel_phase2_checklist"`
	}
	var list []auditCase
	for _, c := range d.cases {
		why := reasons[c.CaseID]
		if len(why) == 0 && sel[panelaudit.Root(c)] {
			why = []string{panelaudit.RandomSampleReason}
		}
		if len(why) == 0 {
			continue
		}
		p, err := renderCase(r.corpus, c, "advisor-review", phaseChecklist)
		if err != nil {
			return err
		}
		pkt := filepath.ToSlash(filepath.Join("audit-packets", c.CaseID+".md"))
		if err := writeFile(filepath.Join(r.out, pkt), []byte("# "+c.CaseID+"\n\n"+p.User)); err != nil {
			return err
		}
		ac := auditCase{AuditCase: panelaudit.AuditCase{CaseID: c.CaseID, Reasons: why}, Kind: c.Kind, Root: panelaudit.Root(c), Packet: pkt, Claims: map[string]string{},
			Blind: map[string]*panelaudit.Independent{}, Checklist: map[string]map[string]itemRating{}}
		for _, f := range blindFields {
			if v, ok := panelaudit.Claim(c, f.key); ok {
				ac.Claims[f.key] = v
			}
		}
		for m, rt := range d.ratings[phaseBlind][c.CaseID] {
			ac.Blind[m] = rt.Independent
		}
		for m, rt := range d.ratings[phaseChecklist][c.CaseID] {
			ac.Checklist[m] = rt.Checklist
		}
		list = append(list, ac)
	}
	doc := map[string]any{
		"schema": panelaudit.AuditSchema, "manifest_digest": r.corpus.Digest, "content_digest": r.corpus.ContentDigest,
		"provider": r.provName, "dry_run": r.provName == "fake",
		"selection": selection,
		"cases":     list, "human_reviews_recorded": 0, "note": humanNote,
	}
	core := panelaudit.Audit{Schema: panelaudit.AuditSchema, ReceiptDigest: receiptDigest, ContentDigest: r.corpus.ContentDigest, DryRun: r.provName == "fake", LiveRun: r.provName == "openrouter", Provider: r.provName, Selection: selection}

	if err := writeReport(filepath.Join(r.out, "audit.json"), struct {
		panelaudit.Audit
		Cases []auditCase `json:"cases"`
	}{core, list}, doc); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Advisor audit list, construct panel\n\n")
	if r.provName == "fake" {
		b.WriteString("**Dry run: the panel ratings are canned fake data. Do not sign.**\n\n")
	}
	fmt.Fprintf(&b, "%s\n\nManifest `%s`, content digest `%s`. This list holds every checklist disagreement, every `unclear`, every phase-1 disagreement between models or with the claimed label, every ambiguous case, incomplete ratings, and %d of %d eligible unanimous roots drawn with seed `%s` (evalcorpus.SelectSeeded, purpose `tbound-panel/unanimous-audit`). Statistics before adjudication are in `stats.md`.\n\n",
		humanNote, r.corpus.Digest, r.corpus.ContentDigest, len(selected), len(eligible), seed)
	b.WriteString("For each case: read its packet and the AI answers of both phases in `audit.json`, decide, and fill the four `advisor_signoff` fields in `audit.json` (decision `accept`, `reject` or `revise`; resolution and reason; identity; signed_at with timezone). Any audit error expands review across the case's source/tool stratum.\n\n| Case | Kind | Reasons | Packet |\n|---|---|---|---|\n")
	for _, ac := range list {
		fmt.Fprintf(&b, "| %s | %s | %s | `%s` |\n", ac.CaseID, ac.Kind, strings.Join(ac.Reasons, "; "), ac.Packet)
	}
	b.WriteString("\nAdvisor signature (name, date, timezone): ______________________\n")
	return writeFile(filepath.Join(r.out, "audit.md"), []byte(b.String()))
}

func writeReport(path string, evidence any, details map[string]any) error {
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for key, value := range fields {
		details[key] = value
	}
	return writeJSON(path, details)
}
