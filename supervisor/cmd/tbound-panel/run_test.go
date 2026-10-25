package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/panelaudit"
)

func testConfig(t *testing.T, budget float64) panelConfig {
	t.Helper()
	cfg := panelConfig{Schema: configSchema, BudgetUSD: budget, PublicPolicy: &publicPolicy{}, Seed: 7, BootstrapResamples: 200, AuditSample: 60, TimeoutSeconds: 5,
		Models: panelModels("google/gemini-2.5-pro", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411")}
	return cfg
}

// countingProvider wraps another provider and counts calls, and phase-1 calls separately.
type countingProvider struct {
	inner          provider
	calls, phase1  atomic.Int64
	failFromPhase2 bool // transport error on every phase-2 call
}

func (c *countingProvider) complete(ctx context.Context, m modelConfig, body []byte) ([]byte, error) {
	c.calls.Add(1)
	if bytes.Contains(body, []byte("completion_predicate")) {
		c.phase1.Add(1)
	} else if c.failFromPhase2 {
		return nil, errors.New("connection reset")
	}
	return c.inner.complete(ctx, m, body)
}

func newRunner(t *testing.T, cfg panelConfig, out string, p provider, name string) *runner {
	t.Helper()
	c, err := loadCorpus(pilotManifest)
	if err != nil {
		t.Fatal(err)
	}
	return &runner{cfg: cfg, corpus: c, out: out, prov: p, provName: name, log: io.Discard}
}

func lines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<24)
	for s.Scan() {
		var m map[string]any
		if err := json.Unmarshal(s.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestBudgetCapStopsBeforeExceedingAndResumeCompletes(t *testing.T) {
	out := t.TempDir()
	// Fake prices make each call cost a few cents; a 0.30 USD cap stops partway.
	cfg := testConfig(t, 0.30)
	for i := range cfg.Models {
		cfg.Models[i].PromptUSDPerMTok, cfg.Models[i].CompletionUSDPerMTok = 3, 15
	}
	p := &countingProvider{inner: fakeProvider{}}
	_, err := newRunner(t, cfg, out, p, "fake").run(context.Background())
	if !errors.Is(err, errBudget) {
		t.Fatalf("want budget stop, got %v", err)
	}
	first := p.calls.Load()
	if first == 0 || first >= 198 {
		t.Fatalf("budget stop after %d calls", first)
	}
	spent, err := loadSpend(filepath.Join(out, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if spent.total() > cfg.BudgetUSD {
		t.Fatalf("spent %.4f above cap %.2f", spent.total(), cfg.BudgetUSD)
	}
	if got := len(lines(t, filepath.Join(out, "ratings.jsonl"))); int64(got) != first {
		t.Fatalf("%d ratings for %d calls", got, first)
	}
	assertGates(t, out, true)

	// Resume in a fresh runner with a larger cap: only the missing pairs are called.
	cfg.BudgetUSD = 10
	p2 := &countingProvider{inner: fakeProvider{}}
	if _, err := newRunner(t, cfg, out, p2, "fake").run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if first+p2.calls.Load() != 198 {
		t.Fatalf("resume called %d after %d; want 198 in total", p2.calls.Load(), first)
	}
	if got := len(lines(t, filepath.Join(out, "ratings.jsonl"))); got != 198 {
		t.Fatalf("%d ratings after resume", got)
	}
	assertGates(t, out, false)
	// A third run calls nothing.
	p3 := &countingProvider{inner: fakeProvider{}}
	if _, err := newRunner(t, cfg, out, p3, "fake").run(context.Background()); err != nil || p3.calls.Load() != 0 {
		t.Fatalf("rerun of a complete run called %d (%v)", p3.calls.Load(), err)
	}
	// Changing a frozen setting refuses to resume into the same directory.
	cfg.Models[0].Temperature = 0.7
	if _, err := newRunner(t, cfg, out, fakeProvider{}, "fake").run(context.Background()); err == nil {
		t.Fatal("resume with changed frozen settings accepted")
	}
}

func TestUnsettledReservationCountsAsSpent(t *testing.T) {
	out := t.TempDir()
	path := filepath.Join(out, "calls.jsonl")
	if err := appendJSONL(path, spendRecord{Kind: "reserve", CallID: "a", ReservedUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := appendJSONL(path, spendRecord{Kind: "reserve", CallID: "b", ReservedUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := appendJSONL(path, spendRecord{Kind: "settle", CallID: "b", CostUSD: 0.1}); err != nil {
		t.Fatal(err)
	}
	s, err := loadSpend(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.total() != 0.6 || s.uncertain != 0.5 {
		t.Fatalf("spend %+v", s)
	}
}

type failingProvider struct{}

func (failingProvider) complete(context.Context, modelConfig, []byte) ([]byte, error) {
	return nil, errors.New("connection reset")
}

// A call with an unknown outcome stays counted after the pair is retried on resume.
func TestUnknownOutcomeStaysCountedAcrossResume(t *testing.T) {
	out := t.TempDir()
	cfg := testConfig(t, 10)
	if _, err := newRunner(t, cfg, out, failingProvider{}, "fake").run(context.Background()); err == nil {
		t.Fatal("transport failure not reported")
	}
	if _, err := newRunner(t, cfg, out, fakeProvider{}, "fake").run(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := loadSpend(filepath.Join(out, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if s.uncertain <= 0 {
		t.Fatalf("the failed call's reservation was released by the retry: %+v", s)
	}
}

// The real OpenRouter client against a local stand-in: the key travels only in the header
// and is written nowhere; the reported cost is what the ledger settles.
func TestOpenRouterClientKeepsKeyOutOfOutputs(t *testing.T) {
	const key = "sk-or-v1-SENTINEL-not-a-real-key"
	var sawAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth.Store(r.Header.Get("Authorization") == "Bearer "+key)
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil || req["temperature"] != 0.0 || req["tools"] != nil || len(req["messages"].([]any)) != 2 {
			http.Error(w, "bad request shape", 400)
			return
		}
		m := modelConfig{ID: req["model"].(string), PromptUSDPerMTok: 1, CompletionUSDPerMTok: 4, MaxTokens: 2000}
		resp, _ := fakeProvider{}.complete(r.Context(), m, body)
		w.Write(resp)
	}))
	defer srv.Close()
	out := t.TempDir()
	cfg := testConfig(t, 10)
	p := &openRouter{endpoint: srv.URL, key: key, client: srv.Client()}
	if _, err := newRunner(t, cfg, out, p, "openrouter").run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawAuth.Load() {
		t.Fatal("key not sent as bearer header")
	}
	filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte("SENTINEL")) {
				t.Errorf("key written to %s", path)
			}
		}
		return nil
	})
}

func TestDryRunEndToEndOnPilotCorpus(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "panel.json")
	cfgJSON := `{"schema":"tbound-panel-config/v1","bootstrap_resamples":300,"public_policy":{"bash_output_paths":[],"network_destinations":[]},"models":[
	 {"id":"google/gemini-2.5-pro","temperature":0,"max_tokens":4000,"prompt_usd_per_mtok":1.25,"completion_usd_per_mtok":10},
	 {"id":"qwen/qwen3-235b-a22b-2507","temperature":0,"max_tokens":4000,"prompt_usd_per_mtok":0.2,"completion_usd_per_mtok":0.8},
	 {"id":"mistralai/mistral-large-2411","temperature":0,"max_tokens":4000,"prompt_usd_per_mtok":2,"completion_usd_per_mtok":6}]}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "run")
	var stdout bytes.Buffer
	a := &app{out: &stdout, errOut: io.Discard, getenv: func(string) string { return "" }}
	if err := a.run([]string{"--config", cfgPath, "--manifest", pilotManifest, "--out", out, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"receipt.json", "settings.json", "blinding.json", "ratings.jsonl", "calls.jsonl", "stats.json", "stats.md", "audit.json", "audit.md"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Fatalf("missing output %s", f)
		}
	}
	if n := len(lines(t, filepath.Join(out, "ratings.jsonl"))); n != 198 {
		t.Fatalf("%d ratings", n)
	}
	var stats struct {
		Items       map[string]ItemStats `json:"items"`
		Independent map[string]ItemStats `json:"independent_fields"`
		Claim       map[string]struct {
			Agreeing, Total int
		} `json:"blind_agreement_with_claim"`
	}
	raw, _ := os.ReadFile(filepath.Join(out, "stats.json"))
	if err := json.Unmarshal(raw, &stats); err != nil || len(stats.Items) != 6 || stats.Items["sensitivity"].Subjects != 31 || stats.Items["intent"].Subjects != 33 {
		t.Fatalf("stats %s (%v)", raw, err)
	}
	for _, f := range []string{"threat_class", "authorization", "effect_ids"} {
		if stats.Independent[f].Subjects != 33 || stats.Claim[f].Total == 0 {
			t.Fatalf("phase-1 field %s missing from stats: %s", f, raw)
		}
	}
	if stats.Claim["threat_class"].Total != 99 || stats.Claim["effect_ids"].Total != 93 {
		t.Fatalf("claim agreement denominators %+v (effect ids are n/a for the 2 ambiguous cases)", stats.Claim)
	}
	var audit struct {
		Schema        string `json:"schema"`
		DryRun        bool   `json:"dry_run"`
		ManifestDig   string `json:"manifest_digest"`
		ContentDigest string `json:"content_digest"`
		Cases         []struct {
			CaseID  string            `json:"case_id"`
			Reasons []string          `json:"reasons"`
			Advisor map[string]string `json:"advisor_signoff"`
		} `json:"cases"`
		HumanReviews int `json:"human_reviews_recorded"`
	}
	raw, _ = os.ReadFile(filepath.Join(out, "audit.json"))
	if err := json.Unmarshal(raw, &audit); err != nil || audit.HumanReviews != 0 || len(audit.Cases) == 0 {
		t.Fatalf("audit %v", err)
	}
	m := loadPilot(t)
	content, err := evalcorpus.ContentDigest(m)
	if err != nil {
		t.Fatal(err)
	}
	whole, _ := evalcorpus.Digest(m)
	if audit.Schema != panelaudit.AuditSchema || !audit.DryRun || audit.ContentDigest != content || audit.ManifestDig != whole || content == whole {
		t.Fatalf("audit schema %q dry_run %v content %q manifest %q; want %s, true, %s, %s", audit.Schema, audit.DryRun, audit.ContentDigest, audit.ManifestDig, panelaudit.AuditSchema, content, whole)
	}
	for _, c := range audit.Cases {
		if len(c.Advisor) != 4 || c.Advisor["decision"] != "" || c.Advisor["resolution"] != "" || c.Advisor["identity"] != "" || c.Advisor["signed_at"] != "" {
			t.Fatalf("%s: advisor_signoff %v is not the four empty fields the advisor fills", c.CaseID, c.Advisor)
		}
	}
	var receipt struct {
		Schema        string `json:"schema"`
		DryRun        bool   `json:"dry_run"`
		ManifestDig   string `json:"manifest_canonical_digest"`
		ContentDigest string `json:"content_digest"`
	}
	rraw, _ := os.ReadFile(filepath.Join(out, "receipt.json"))
	if err := json.Unmarshal(rraw, &receipt); err != nil || receipt.Schema != panelaudit.ReceiptSchema || !receipt.DryRun ||
		receipt.ContentDigest != content || receipt.ManifestDig != whole {
		t.Fatalf("receipt %s (%v)", rraw, err)
	}
	signAndVerify(t, m, rraw, raw, out)
	amb := 0
	for _, c := range audit.Cases {
		if strings.HasPrefix(c.CaseID, "PILOT-AMB") {
			amb++
		}
	}
	if amb != 2 {
		t.Fatalf("audit list has %d ambiguous cases, want both", amb)
	}
	// Every recorded request is blinded.
	filepath.Walk(filepath.Join(out, "requests"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := os.ReadFile(path)
		if bytes.Contains(b, []byte("PILOT-")) {
			t.Errorf("%s leaks a case id", path)
		}
		for label, re := range forbidden {
			if re.Match(b) {
				t.Errorf("%s matches %s", path, label)
			}
		}
		return nil
	})
	if !strings.Contains(stdout.String(), "kappa") {
		t.Fatalf("dry run printed no kappa table:\n%s", stdout.String())
	}
}

func TestLiveRunNeedsKeyAndAMode(t *testing.T) {
	a := &app{out: io.Discard, errOut: io.Discard, getenv: func(string) string { return "" }}
	if err := a.run([]string{"--config", "x.json", "--manifest", pilotManifest, "--out", t.TempDir()}); err == nil {
		t.Fatal("no mode flag accepted")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "panel.json")
	os.WriteFile(cfgPath, []byte(`{"schema":"tbound-panel-config/v1","public_policy":{"bash_output_paths":[],"network_destinations":[]},"models":[
	 {"id":"google/gemini-2.5-pro","prompt_usd_per_mtok":1,"completion_usd_per_mtok":1},
	 {"id":"qwen/qwen3-235b-a22b-2507","prompt_usd_per_mtok":1,"completion_usd_per_mtok":1},
	 {"id":"mistralai/mistral-large-2411","prompt_usd_per_mtok":1,"completion_usd_per_mtok":1}]}`), 0o600)
	err := a.run([]string{"--config", cfgPath, "--manifest", pilotManifest, "--out", filepath.Join(dir, "run"), "--live"})
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("live run without key: %v", err)
	}
}

// Phase 1 completes, every phase-2 call then fails; a resume calls phase 2 only.
func TestResumeSkipsCompletedPhase(t *testing.T) {
	out := t.TempDir()
	cfg := testConfig(t, 10)
	p := &countingProvider{inner: fakeProvider{}, failFromPhase2: true}
	if _, err := newRunner(t, cfg, out, p, "fake").run(context.Background()); err == nil {
		t.Fatal("phase-2 failure not reported")
	}
	if p.phase1.Load() != 99 {
		t.Fatalf("phase 1 made %d calls before phase 2, want 99", p.phase1.Load())
	}
	p2 := &countingProvider{inner: fakeProvider{}}
	if _, err := newRunner(t, cfg, out, p2, "fake").run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p2.phase1.Load() != 0 || p2.calls.Load() != 99 {
		t.Fatalf("resume made %d calls, %d of them phase 1; want 99 phase-2 calls only", p2.calls.Load(), p2.phase1.Load())
	}
}

func TestEstimateCountsTwoCallsPerCaseAndModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "panel.json")
	os.WriteFile(cfgPath, []byte(`{"schema":"tbound-panel-config/v1","public_policy":{"bash_output_paths":[],"network_destinations":[]},"models":[
	 {"id":"google/gemini-2.5-pro","prompt_usd_per_mtok":1,"completion_usd_per_mtok":1},
	 {"id":"qwen/qwen3-235b-a22b-2507","prompt_usd_per_mtok":1,"completion_usd_per_mtok":1},
	 {"id":"mistralai/mistral-large-2411","prompt_usd_per_mtok":1,"completion_usd_per_mtok":1}]}`), 0o600)
	var stdout bytes.Buffer
	a := &app{out: &stdout, errOut: io.Discard, getenv: func(string) string { return "" }}
	if err := a.run([]string{"--config", cfgPath, "--manifest", pilotManifest, "--estimate"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "= 198 calls") || strings.Count(stdout.String(), "| 66 |") != 3 {
		t.Fatalf("estimate does not count two phases:\n%s", stdout.String())
	}
}

// rewriteProvider edits the fake provider's response object before returning it.
type rewriteProvider struct {
	fn    func(resp map[string]any)
	calls atomic.Int64
}

func (p *rewriteProvider) complete(ctx context.Context, m modelConfig, body []byte) ([]byte, error) {
	p.calls.Add(1)
	raw, err := fakeProvider{}.complete(ctx, m, body)
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	p.fn(resp)
	return json.Marshal(resp)
}

// Without a provider-reported cost the reserved worst case is spent and the run stops.
func TestMissingCostSpendsWorstCaseAndStops(t *testing.T) {
	for name, fn := range map[string]func(map[string]any){
		"usage without cost": func(r map[string]any) { delete(r["usage"].(map[string]any), "cost") },
		"zero usage":         func(r map[string]any) { r["usage"] = map[string]any{"prompt_tokens": 0, "completion_tokens": 0} },
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			p := &rewriteProvider{fn: fn}
			_, err := newRunner(t, testConfig(t, 10), out, p, "fake").run(context.Background())
			if err == nil || !strings.Contains(err.Error(), "cost") || p.calls.Load() != 1 {
				t.Fatalf("calls %d, err %v", p.calls.Load(), err)
			}
			recs := lines(t, filepath.Join(out, "calls.jsonl"))
			if len(recs) != 2 || recs[1]["cost_source"] != "reserved-worst-case" || recs[1]["cost_usd"] != recs[0]["reserved_usd"] {
				t.Fatalf("spend records %v", recs)
			}
		})
	}
}

// assertGates checks that an incomplete run makes no gate claim and lists the missing pairs.
func assertGates(t *testing.T, out string, incomplete bool) {
	t.Helper()
	var st struct {
		Items        map[string]ItemStats `json:"items"`
		Fields       map[string]ItemStats `json:"independent_fields"`
		MissingPairs []map[string]any     `json:"missing_pairs"`
	}
	raw, _ := os.ReadFile(filepath.Join(out, "stats.json"))
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if incomplete != (len(st.MissingPairs) > 0) {
		t.Fatalf("incomplete=%v but %d missing pairs", incomplete, len(st.MissingPairs))
	}
	for name, s := range map[string]map[string]ItemStats{"items": st.Items, "fields": st.Fields} {
		for k, v := range s {
			if (v.Gate == gateIncomplete) != incomplete {
				t.Fatalf("%s %s gate %q with incomplete=%v", name, k, v.Gate, incomplete)
			}
		}
	}
}

// An answer from another family than the configured model is invalid and stops the run.
func TestResponseFromOtherFamilyStops(t *testing.T) {
	out := t.TempDir()
	p := &rewriteProvider{fn: func(r map[string]any) { r["model"] = "anthropic/claude-sonnet-4" }}
	_, err := newRunner(t, testConfig(t, 10), out, p, "fake").run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "family") || p.calls.Load() != 1 {
		t.Fatalf("calls %d, err %v", p.calls.Load(), err)
	}
	if rs := lines(t, filepath.Join(out, "ratings.jsonl")); len(rs) != 1 || rs[0]["valid"] != false {
		t.Fatalf("ratings %v", rs)
	}
}

func loadPilot(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	c, err := loadCorpus(pilotManifest)
	if err != nil {
		t.Fatal(err)
	}
	return c.Manifest
}

// signAndVerify plays the advisor on a run's own files: it fills the sign-off fields of every
// listed case, writes the sign-offs into the manifest and seals its construct review to the
// result. panelaudit.Verify accepts that once the run is not a dry run, so what the panel writes
// is what the corpus-digest reader needs.
func signAndVerify(t *testing.T, m evalcorpus.Manifest, receipt, audit []byte, out string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(audit, &doc); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, c := range doc["cases"].([]any) {
		c := c.(map[string]any)
		c["advisor_signoff"] = map[string]string{"decision": "accept", "resolution": "defensible", "identity": "advisor", "signed_at": "2026-10-25T10:00:00+02:00"}
		listed[c["case_id"].(string)] = true
	}
	doc["dry_run"] = false
	var rdoc map[string]any
	if err := json.Unmarshal(receipt, &rdoc); err != nil {
		t.Fatal(err)
	}
	rdoc["dry_run"] = false
	signedAudit, _ := json.MarshalIndent(doc, "", "  ")
	signedReceipt, _ := json.MarshalIndent(rdoc, "", "  ")
	for i := range m.Cases {
		m.Cases[i].Signoff = evalcorpus.Signoff{Reviewer: "advisor", Human: true, Decision: "accept"}
		if listed[m.Cases[i].CaseID] {
			m.Cases[i].Signoff.Audited, m.Cases[i].Signoff.Resolution = true, "defensible"
		}
	}
	m.ConstructReview = evalcorpus.ConstructReview{ContentDigest: doc["content_digest"].(string),
		PanelReceiptDigest: sha(string(signedReceipt)), PanelAuditDigest: sha(string(signedAudit))}
	stats, err := os.ReadFile(filepath.Join(out, "stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sd map[string]any
	if err := json.Unmarshal(stats, &sd); err != nil {
		t.Fatal(err)
	}
	sd["dry_run"] = false
	stats, _ = json.Marshal(sd)
	m.ConstructReview.PanelStatsDigest = panelaudit.Digest(stats)
	if err := panelaudit.Verify(m, signedReceipt, signedAudit, stats); err == nil || !strings.Contains(err.Error(), "gate") {
		t.Fatalf("the fake ratings must not earn an agreement pass after signing: %v", err)
	}
	if err := panelaudit.Verify(m, receipt, audit, stats); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("the unsigned dry-run files must not match the sealed digests: %v", err)
	}
}
