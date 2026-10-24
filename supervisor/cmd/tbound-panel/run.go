package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tbound/supervisor/internal/evalcorpus"
)

// errBudget stops a run before a call whose worst case could exceed the cap.
var errBudget = errors.New("budget cap reached")

// runner rates every case with every panel model and writes the run directory.
type runner struct {
	cfg      panelConfig
	corpus   corpus
	out      string
	prov     provider
	provName string // "openrouter" or "fake"
	log      io.Writer
}

// spendRecord is one line of calls.jsonl. A reserve without a settle is an uncertain call
// and keeps its whole reservation counted as spent.
type spendRecord struct {
	Kind        string  `json:"kind"` // reserve | settle
	CallID      string  `json:"call_id"`
	CaseID      string  `json:"case_id,omitempty"`
	Model       string  `json:"model,omitempty"`
	ReservedUSD float64 `json:"reserved_usd,omitempty"`
	CostUSD     float64 `json:"cost_usd"`
	CostSource  string  `json:"cost_source,omitempty"` // provider-reported | computed-from-usage | no-charge
	Note        string  `json:"note,omitempty"`
	Time        string  `json:"time,omitempty"`
}

type spend struct {
	settled, uncertain float64
	attempts           map[string]int // model + "\x00" + case id -> reservations so far
}

func (s spend) total() float64 { return s.settled + s.uncertain }

func loadSpend(path string) (spend, error) {
	reserved := map[string]float64{}
	s := spend{attempts: map[string]int{}}
	err := readJSONL(path, func(raw []byte) error {
		var r spendRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		switch r.Kind {
		case "reserve":
			if _, dup := reserved[r.CallID]; dup {
				return fmt.Errorf("call id %q reserved twice", r.CallID)
			}
			reserved[r.CallID] = r.ReservedUSD
			s.attempts[r.Model+"\x00"+r.CaseID]++
		case "settle":
			delete(reserved, r.CallID)
			s.settled += r.CostUSD
		default:
			return fmt.Errorf("unknown spend record kind %q", r.Kind)
		}
		return nil
	})
	for _, v := range reserved {
		s.uncertain += v
	}
	return s, err
}

func readJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for n := 1; sc.Scan(); n++ {
		if err := fn(sc.Bytes()); err != nil {
			return fmt.Errorf("%s line %d: %w", path, n, err)
		}
	}
	return sc.Err()
}

// appendJSONL appends one record and syncs it, so a crash never loses a reservation.
func appendJSONL(path string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(b, '\n'))
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// rating is one line of ratings.jsonl: one model's parsed answer for one case.
type rating struct {
	CaseID        string                `json:"case_id"`
	BlindID       string                `json:"blind_id"`
	Model         string                `json:"model"`
	Family        string                `json:"family"`
	ResponseModel string                `json:"response_model"`
	ResponseHost  string                `json:"response_provider"`
	ResponseID    string                `json:"response_id"`
	Kind          evalcorpus.Kind       `json:"kind"`
	Root          string                `json:"root"`
	Items         []string              `json:"applicable_items"`
	Checklist     map[string]itemRating `json:"checklist,omitempty"`
	Independent   *independent          `json:"independent,omitempty"`
	Valid         bool                  `json:"valid"`
	Error         string                `json:"error,omitempty"`
	Settings      map[string]any        `json:"settings"`
	PromptSHA256  string                `json:"prompt_sha256"`
	RequestFile   string                `json:"request_file"`
	ResponseFile  string                `json:"response_file"`
	CostUSD       float64               `json:"cost_usd"`
	Time          string                `json:"time"`
	Rater         string                `json:"rater"`
}

type itemRating struct {
	Rating string `json:"rating"`
	Reason string `json:"reason"`
}

type independent struct {
	ThreatClass   string   `json:"threat_class"`
	Authorization string   `json:"authorization"`
	EffectIDs     []string `json:"effect_ids"`
}

// parseAnswer strictly decodes a model answer. One surrounding code fence is tolerated.
func parseAnswer(content, blindID string, applicable []string) (map[string]itemRating, *independent, error) {
	s := strings.TrimSpace(content)
	if strings.HasPrefix(s, "```") {
		if _, rest, ok := strings.Cut(s, "\n"); ok {
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "```"))
		}
	}
	var a struct {
		ItemID      string                `json:"item_id"`
		Checklist   map[string]itemRating `json:"checklist"`
		Independent *independent          `json:"independent"`
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, nil, fmt.Errorf("answer is not the requested JSON object: %v", err)
	}
	if dec.More() {
		return nil, nil, errors.New("answer has data after the JSON object")
	}
	if a.ItemID != blindID {
		return nil, nil, fmt.Errorf("answer item_id %q, want %q", a.ItemID, blindID)
	}
	if len(a.Checklist) != len(applicable) {
		return nil, nil, fmt.Errorf("answer rates %d items, want %d", len(a.Checklist), len(applicable))
	}
	for _, k := range applicable {
		r, ok := a.Checklist[k]
		if !ok || (r.Rating != "pass" && r.Rating != "fail" && r.Rating != "unclear") || strings.TrimSpace(r.Reason) == "" {
			return nil, nil, fmt.Errorf("item %s: rating %q with reason %q", k, r.Rating, r.Reason)
		}
	}
	in := a.Independent
	if in == nil {
		return nil, nil, errors.New("answer has no independent classification")
	}
	if !oneOf(in.ThreatClass, "TM-A", "TM-B", "TM-C", "none") || !oneOf(in.Authorization, "allowed", "forbidden", "ambiguous") {
		return nil, nil, fmt.Errorf("independent classification %q/%q out of range", in.ThreatClass, in.Authorization)
	}
	for _, e := range in.EffectIDs {
		if _, ok := effectGlossary[e]; !ok {
			return nil, nil, fmt.Errorf("unknown effect id %q", e)
		}
	}
	return a.Checklist, in, nil
}

func oneOf(s string, set ...string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

func slug(model string) string { return strings.NewReplacer("/", "__", ":", "_").Replace(model) }

// frozenSettings are what a resumed run must share with the run it continues.
func (r *runner) frozenSettings() map[string]any {
	var models []map[string]any
	for _, m := range r.cfg.Models {
		models = append(models, map[string]any{"id": m.ID, "family": family(m.ID), "temperature": m.Temperature, "max_tokens": m.MaxTokens})
	}
	var checklist []string
	for _, it := range items {
		checklist = append(checklist, it.key+": "+it.question)
	}
	return map[string]any{
		"schema": "tbound-panel-settings/v1", "provider": r.provName, "models": models, "seed": r.cfg.Seed,
		"manifest_digest": r.corpus.Digest, "system_prompt_sha256": sha(renderedSystemPrompt),
		"checklist": checklist, "not_applicable": notApplicable, "tools": "none", "session": "fresh single-turn per case",
	}
}

func (r *runner) checkSettings() error {
	want, err := json.MarshalIndent(r.frozenSettings(), "", "  ")
	if err != nil {
		return err
	}
	want = append(want, '\n')
	path := filepath.Join(r.out, "settings.json")
	have, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return writeFile(path, want)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(have, want) {
		return fmt.Errorf("%s holds different frozen settings (models, seed, prompts, provider or manifest); use a new --out directory", path)
	}
	return nil
}

func worstCaseUSD(m modelConfig, body []byte) float64 {
	// Prompt tokens never exceed prompt bytes, so bytes bound the input side.
	return (float64(len(body))*m.PromptUSDPerMTok + float64(m.MaxTokens)*m.CompletionUSDPerMTok) / 1e6
}

type runSummary struct {
	Calls, Valid, Invalid int
	Spent                 spend
}

func (r *runner) run(ctx context.Context) (runSummary, error) {
	var sum runSummary
	if err := validateModels(r.cfg.Models); err != nil {
		return sum, err
	}
	if err := os.MkdirAll(r.out, 0o700); err != nil {
		return sum, err
	}
	if err := r.checkSettings(); err != nil {
		return sum, err
	}
	var modelIDs []string
	for _, m := range r.cfg.Models {
		modelIDs = append(modelIDs, m.ID)
	}
	plan, err := blind(r.corpus, modelIDs, r.cfg.Seed)
	if err != nil {
		return sum, err
	}
	if err := writeJSON(filepath.Join(r.out, "blinding.json"), map[string]any{
		"seed": r.cfg.Seed, "id_derivation": "item- + first 10 hex of sha256(\"tbound-panel/blind-id\\0\" seed \\0 model \\0 case_id)",
		"order_derivation": "sorted case ids shuffled by math/rand/v2 PCG(seed, first 5 bytes of sha256(model))",
		"ids":              plan.ids, "order": plan.order,
	}); err != nil {
		return sum, err
	}
	callsPath, ratingsPath := filepath.Join(r.out, "calls.jsonl"), filepath.Join(r.out, "ratings.jsonl")
	done := map[string]bool{}
	if err := readJSONL(ratingsPath, func(raw []byte) error {
		var rt rating
		if err := json.Unmarshal(raw, &rt); err != nil {
			return err
		}
		if rt.Valid {
			done[rt.Model+"\x00"+rt.CaseID] = true
		}
		return nil
	}); err != nil {
		return sum, err
	}
	cases := map[string]evalcorpus.Case{}
	for _, c := range r.corpus.Manifest.Cases {
		cases[c.CaseID] = c
	}
	runErr := func() error {
		for _, m := range r.cfg.Models {
			for _, caseID := range plan.order[m.ID] {
				key := m.ID + "\x00" + caseID
				if done[key] {
					continue
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := r.rateOne(ctx, m, cases[caseID], plan.ids[m.ID][caseID], callsPath, ratingsPath, &sum); err != nil {
					return err
				}
			}
		}
		return nil
	}()
	var finishErr error
	sum.Spent, finishErr = r.finish(plan)
	return sum, errors.Join(runErr, finishErr)
}

func (r *runner) rateOne(ctx context.Context, m modelConfig, c evalcorpus.Case, blindID string, callsPath, ratingsPath string, sum *runSummary) error {
	p, err := renderCase(r.corpus, c, blindID)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(newRequest(m, p.User), "", "  ")
	if err != nil {
		return err
	}
	spent, err := loadSpend(callsPath)
	if err != nil {
		return err
	}
	worst := worstCaseUSD(m, body)
	if spent.total()+worst > r.cfg.BudgetUSD {
		return fmt.Errorf("%w: spent %.4f USD (%.4f uncertain) + worst case %.4f USD of the next call > cap %.2f USD", errBudget, spent.total(), spent.uncertain, worst, r.cfg.BudgetUSD)
	}
	// Attempts come from the ledger, so a retry never reuses an uncertain call's id.
	attempt := spent.attempts[m.ID+"\x00"+c.CaseID] + 1
	callID := fmt.Sprintf("%s/%s/%d", m.ID, blindID, attempt)
	base := filepath.Join(slug(m.ID), fmt.Sprintf("%s.%d.json", blindID, attempt))
	reqFile, respFile := filepath.ToSlash(filepath.Join("requests", base)), filepath.ToSlash(filepath.Join("responses", base))
	if err := writeFile(filepath.Join(r.out, reqFile), append(body, '\n')); err != nil {
		return err
	}
	now := func() string { return time.Now().UTC().Format(time.RFC3339) }
	if err := appendJSONL(callsPath, spendRecord{Kind: "reserve", CallID: callID, CaseID: c.CaseID, Model: m.ID, ReservedUSD: worst, Time: now()}); err != nil {
		return err
	}
	sum.Calls++
	fmt.Fprintf(r.log, "call %s %s (%s)\n", m.ID, blindID, c.CaseID)
	raw, callErr := r.prov.complete(ctx, m, body)
	if len(raw) > 0 {
		if err := writeFile(filepath.Join(r.out, respFile), raw); err != nil {
			return err
		}
	}
	if callErr != nil {
		if errors.Is(callErr, errNoCharge) {
			return errors.Join(callErr, appendJSONL(callsPath, spendRecord{Kind: "settle", CallID: callID, CostSource: "no-charge", Note: "HTTP error status", Time: now()}))
		}
		// Unknown outcome: the reservation stays open and counted against the cap.
		return fmt.Errorf("call %s: %w (its worst case stays counted as spent)", callID, callErr)
	}
	var resp chatResponse
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Usage == nil {
		return fmt.Errorf("call %s: response without usage accounting (%v); reservation kept, stopping", callID, err)
	}
	cost, source := 0.0, "provider-reported"
	if resp.Usage.Cost != nil {
		cost = *resp.Usage.Cost
	} else {
		cost, source = (float64(resp.Usage.PromptTokens)*m.PromptUSDPerMTok+float64(resp.Usage.CompletionTokens)*m.CompletionUSDPerMTok)/1e6, "computed-from-usage"
	}
	if err := appendJSONL(callsPath, spendRecord{Kind: "settle", CallID: callID, CostUSD: cost, CostSource: source, Time: now()}); err != nil {
		return err
	}
	rt := rating{CaseID: c.CaseID, BlindID: blindID, Model: m.ID, Family: family(m.ID), ResponseModel: resp.Model, ResponseHost: resp.Provider,
		ResponseID: resp.ID, Kind: c.Kind, Root: root(c), Items: p.Items, PromptSHA256: sha(renderedSystemPrompt + "\n" + p.User),
		RequestFile: reqFile, ResponseFile: respFile, CostUSD: cost, Time: now(), Rater: "ai-panel-model (not a human review)",
		Settings: map[string]any{"temperature": m.Temperature, "max_tokens": m.MaxTokens, "tools": "none", "session": "fresh single-turn"}}
	if len(resp.Choices) == 0 {
		rt.Error = "no choices in response"
	} else if rt.Checklist, rt.Independent, err = parseAnswer(resp.Choices[0].Message.Content, blindID, p.Items); err != nil {
		rt.Error = err.Error()
	}
	rt.Valid = rt.Error == ""
	if rt.Valid {
		sum.Valid++
	} else {
		sum.Invalid++
	}
	if err := appendJSONL(ratingsPath, rt); err != nil {
		return err
	}
	if cost > worst {
		return fmt.Errorf("call %s reported %.6f USD above its reserved worst case %.6f USD: the configured prices are too low; fix them before resuming", callID, cost, worst)
	}
	return nil
}

func root(c evalcorpus.Case) string {
	if c.MeaningRecord != nil && c.MeaningRecord.RootID != "" {
		return c.MeaningRecord.RootID
	}
	if c.Adversarial != nil && c.Adversarial.RootScenarioID != "" {
		return c.Adversarial.RootScenarioID
	}
	return c.CaseID
}
