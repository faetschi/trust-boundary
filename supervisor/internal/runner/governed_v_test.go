package runner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/vrun"
)

const (
	vImage   = "sha256:" + "11111111111111111111111111111111111111111111111111111111111111aa"
	vFixture = "sha256:" + "22222222222222222222222222222222222222222222222222222222222222bb"
)

func vDoc(caseID string, passed bool) vrun.Doc {
	return vrun.Doc{Schema: vrun.Schema, CaseID: caseID, ImageDigest: vImage, FixtureDigest: vFixture, Exit: 0,
		TokensIn: 1200, TokensOut: 340, CostMicroUSD: 560, ToolCalls: 4, UsageEvents: 3,
		Accept: &taskaccept.AcceptDoc{Schema: taskaccept.ResultSchema, CaseID: caseID, SpecDigest: "sha256:" + strings.Repeat("c", 64), Passed: passed,
			Results: []taskaccept.Result{{Op: "contains", Path: "f", Passed: passed, Detail: "d"}}},
		StartedNS: 5_000_000_000, FinishedNS: 5_000_000_000 + 12_345_000_000}
}

func vReply(t *testing.T, d vrun.Doc) *scriptedTransport {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return &scriptedTransport{reply: func([]string) ([]byte, []byte, int, error) { return append(raw, '\n'), nil, 0, nil }}
}

func vStep() Step { return compliantStep(CfgV) }

func TestGovernedVMapsTheDocumentToTheOutcome(t *testing.T) {
	tr := vReply(t, vDoc("cmp-1", true))
	g := GovernedV{Transport: tr, ImageDigest: vImage, Route: RouteCoverage{Complete: true, Digest: "route"},
		FixtureDigest: func(id string) (string, bool) { return vFixture, id == "cmp-1" }}
	out, err := g.Execute(context.Background(), vStep())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tr.calls(), [][]string{{"v-run", "cmp-1"}}) {
		t.Fatalf("calls = %v", tr.calls())
	}
	if out.Task != TaskPass || !out.E01.Verified || !strings.HasPrefix(out.E01.Digest, "sha256:") || len(out.E01.Digest) != len("sha256:")+64 {
		t.Fatalf("outcome = %+v", out)
	}
	want := Metrics{LatencyMS: 12345, InTokens: 1200, OutTokens: 340, CostMicroUSD: 560, ToolCalls: 4}
	if out.Metrics != want {
		t.Fatalf("metrics = %+v, want %+v", out.Metrics, want)
	}
	if out.Route != (RouteCoverage{Complete: true, Digest: "route"}) || len(out.Oracles) != 0 || out.Receipt != nil || out.Channel != "" || out.ToolAttempt || out.MatchedDelivery {
		t.Fatalf("V claims more than it measures: %+v", out)
	}
}

func TestGovernedVTaskFollowsTheAcceptResultOnly(t *testing.T) {
	cases := map[string]func(d *vrun.Doc){
		"failed acceptance":          func(d *vrun.Doc) { d.Accept.Passed, d.Accept.Results[0].Passed = false, false },
		"no accept result":           func(d *vrun.Doc) { d.Accept, d.AcceptError = nil, "tree unreadable" },
		"accept of another case":     func(d *vrun.Doc) { d.Accept.CaseID = "cmp-2" },
		"accept of another schema":   func(d *vrun.Doc) { d.Accept.Schema = "x" },
		"passed without a result":    func(d *vrun.Doc) { d.Accept.Results = nil },
		"passed over a failed check": func(d *vrun.Doc) { d.Accept.Results[0].Passed = false },
	}
	want := map[string]TaskResult{"failed acceptance": TaskFail, "no accept result": TaskLost, "accept of another case": TaskLost,
		"accept of another schema": TaskLost, "passed without a result": TaskLost, "passed over a failed check": TaskLost}
	for name, mutate := range cases {
		d := vDoc("cmp-1", true)
		mutate(&d)
		out, err := GovernedV{Transport: vReply(t, d)}.Execute(context.Background(), vStep())
		if err != nil || out.Task != want[name] {
			t.Errorf("%s: task %q err %v, want %q", name, out.Task, err, want[name])
		}
	}
	// A timed-out or failed run is judged on what it left, and is still data.
	d := vDoc("cmp-1", false)
	d.TimedOut, d.Exit = true, 137
	if out, err := (GovernedV{Transport: vReply(t, d)}).Execute(context.Background(), vStep()); err != nil || out.Task != TaskFail {
		t.Errorf("timed out: task %q err %v", out.Task, err)
	}
	d = vDoc("cmp-1", false)
	d.Exit = 1
	if out, err := (GovernedV{Transport: vReply(t, d)}).Execute(context.Background(), vStep()); err != nil || out.Task != TaskFail {
		t.Errorf("exit 1 after model responses: task %q err %v", out.Task, err)
	}
}

func TestGovernedVRunWithoutAModelResponseIsPartial(t *testing.T) {
	d := vDoc("cmp-1", false)
	d.Exit, d.UsageEvents, d.TokensIn, d.TokensOut = 1, 0, 0, 0
	out, err := GovernedV{Transport: vReply(t, d)}.Execute(context.Background(), vStep())
	if !errors.Is(err, ErrVNoModelResponse) || out.Task == TaskPass || out.E01.Digest == "" {
		t.Fatalf("task %q err %v", out.Task, err)
	}
}

func TestGovernedVVerificationNeedsThePin(t *testing.T) {
	good := vDoc("cmp-1", true)
	fixture := func(string) (string, bool) { return vFixture, true }
	for name, g := range map[string]GovernedV{
		"no pin":             {ImageDigest: ""},
		"other image":        {ImageDigest: "sha256:" + strings.Repeat("0", 64)},
		"other fixture":      {ImageDigest: vImage, FixtureDigest: func(string) (string, bool) { return "sha256:" + strings.Repeat("0", 64), true }},
		"fixture of no case": {ImageDigest: vImage, FixtureDigest: func(string) (string, bool) { return "", false }},
	} {
		g.Transport = vReply(t, good)
		if out, err := g.Execute(context.Background(), vStep()); err != nil || out.E01.Verified {
			t.Errorf("%s: verified %v err %v", name, out.E01.Verified, err)
		}
	}
	g := GovernedV{Transport: vReply(t, good), ImageDigest: vImage, FixtureDigest: fixture}
	if out, err := g.Execute(context.Background(), vStep()); err != nil || !out.E01.Verified {
		t.Errorf("pinned: verified %v err %v", out.E01.Verified, err)
	}
	// The identity covers the document and the image: another run, another image, another digest.
	a, _ := g.Execute(context.Background(), vStep())
	other := good
	other.TokensOut++
	b, _ := GovernedV{Transport: vReply(t, other)}.Execute(context.Background(), vStep())
	image := good
	image.ImageDigest = "sha256:" + strings.Repeat("9", 64)
	c, _ := GovernedV{Transport: vReply(t, image)}.Execute(context.Background(), vStep())
	if a.E01.Digest == b.E01.Digest || a.E01.Digest == c.E01.Digest || b.E01.Digest == c.E01.Digest {
		t.Fatalf("digests collide: %s %s %s", a.E01.Digest, b.E01.Digest, c.E01.Digest)
	}
	again, _ := g.Execute(context.Background(), vStep())
	if again.E01.Digest != a.E01.Digest {
		t.Fatal("the identity is not deterministic")
	}
}

func TestGovernedVSetupFailures(t *testing.T) {
	good, _ := json.Marshal(vDoc("cmp-1", true))
	for name, reply := range map[string]func([]string) ([]byte, []byte, int, error){
		"transport error": func([]string) ([]byte, []byte, int, error) { return nil, nil, 0, errors.New("ssh: reset") },
		"guest refusal": func([]string) ([]byte, []byte, int, error) {
			return nil, []byte("v-run applies to compliant cases only"), 2, nil
		},
		"not json": func([]string) ([]byte, []byte, int, error) { return []byte("ok"), nil, 0, nil },
		"other case": func([]string) ([]byte, []byte, int, error) {
			return []byte(strings.Replace(string(good), `"cmp-1"`, `"cmp-2"`, 1)), nil, 0, nil
		},
		"exit with a document": func([]string) ([]byte, []byte, int, error) { return good, nil, 1, nil },
	} {
		out, err := GovernedV{Transport: &scriptedTransport{reply: reply}}.Execute(context.Background(), vStep())
		if err == nil || !reflect.DeepEqual(out, Outcome{}) {
			t.Errorf("%s: out %+v err %v, want an empty outcome and an error", name, out, err)
		}
	}
}

func TestGovernedVPrepare(t *testing.T) {
	ok := &scriptedTransport{reply: func([]string) ([]byte, []byte, int, error) { return nil, nil, 0, nil }}
	if err := (GovernedV{Transport: ok}).Prepare(context.Background(), vStep()); err != nil || len(ok.calls()) != 0 {
		t.Fatalf("Prepare = %v, calls %v", err, ok.calls())
	}
	if err := (GovernedV{}).Prepare(context.Background(), vStep()); !errors.Is(err, ErrNotWired) {
		t.Fatalf("no transport: %v", err)
	}
	if _, err := (GovernedV{}).Execute(context.Background(), vStep()); !errors.Is(err, ErrNotWired) {
		t.Fatalf("no transport (Execute): %v", err)
	}
	for name, s := range map[string]Step{
		"C step":    compliantStep(CfgC),
		"ambiguous": {CaseID: "a", Kind: evalcorpus.KindAmbiguous, Configuration: CfgV, Role: RoleAmbiguous, Attempt: 1},
	} {
		if err := (GovernedV{Transport: ok}).Prepare(context.Background(), s); !errors.Is(err, ErrSetupRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := SSHTransport{SSHPath: "ssh", IdentityFile: "k", User: "root", Host: "h", HostKeyAlias: "a", KnownHostsFile: "/kh"}
	if err := (GovernedV{Transport: bad}).Prepare(context.Background(), vStep()); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("a client-command transport was accepted: %v", err)
	}
}

func TestExecutorsRouteVToGovernedV(t *testing.T) {
	tr := vReply(t, vDoc("cmp-1", true))
	e := Executors{V: GovernedV{Transport: tr}}
	out, err := e.Execute(context.Background(), vStep())
	if err != nil || out.Task != TaskPass {
		t.Fatalf("task %q err %v", out.Task, err)
	}
}

// The governed V executor reports no EVQ obligations, so V must not decide
// evq_passed (D44): neither a passing V nor a V without a model response.
func TestExportEVQIgnoresGovernedVObligations(t *testing.T) {
	for _, noResponse := range []bool{false, true} {
		cfg := smallConfig()
		s := smallSchedule(t, cfg)
		fake := NewFakeExecutor(s.Config)
		v := GovernedV{Transport: &scriptedTransport{reply: func(argv []string) ([]byte, []byte, int, error) {
			d := vDoc(argv[len(argv)-1], true)
			if noResponse {
				d.Exit, d.UsageEvents, d.TokensIn, d.TokensOut = 1, 0, 0, 0
			}
			raw, err := json.Marshal(d)
			return append(raw, '\n'), nil, 0, err
		}}, ImageDigest: vImage, Route: RouteCoverage{Complete: true, Digest: "route"}}
		j, _ := runAll(t, s, t.TempDir(), Executors{B: fake, C: fake, P: fake, V: v})
		res, err := Export(s, j, ExportOptions{})
		j.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !res.Input.EVQPassed {
			t.Fatalf("noResponse=%v: governed V outcomes failed EVQ", noResponse)
		}
	}
}

// An obligation a V step reports as failed still fails EVQ: it is evidence about
// the infrastructure, not about V's result.
func TestExportEVQKeepsAFailedVObligation(t *testing.T) {
	cfg := smallConfig()
	s := smallSchedule(t, cfg)
	s, _ = withV(t, s, firstCase(s, "compliant"))
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		if st.Configuration == CfgV {
			o.EVQ = []Obligation{{ID: "EVQ-1", Passed: false}}
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Input.EVQPassed {
		t.Fatal("a failed obligation reported by V passed EVQ")
	}
}
