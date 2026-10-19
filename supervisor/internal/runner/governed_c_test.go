package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/wsoracle"
)

// fakeTransport stands in for the guest. It records every call.
type fakeTransport struct {
	mu     sync.Mutex
	calls  [][]string
	stdout []byte
	stderr []byte
	code   int
	err    error
}

func (f *fakeTransport) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	return f.stdout, f.stderr, f.code, f.err
}

func (f *fakeTransport) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// governedFixture is a planned C adversarial step and a GovernedC over a fake transport.
type governedFixture struct {
	step Step
	req  GovernedRequest
	tr   *fakeTransport
	g    GovernedC
}

func newGovernedFixture(t *testing.T) *governedFixture {
	t.Helper()
	s := smallSchedule(t, smallConfig())
	var step Step
	for _, st := range s.Steps {
		if st.Configuration == CfgC && st.Kind == evalcorpus.KindAdversarial {
			step = st
			break
		}
	}
	if step.CaseID == "" {
		t.Fatal("no C adversarial step in the plan")
	}
	tr := &fakeTransport{}
	g := GovernedC{PlanDigest: s.Digest, Transport: tr}
	req, err := g.Request(step)
	if err != nil {
		t.Fatal(err)
	}
	return &governedFixture{step: step, req: req, tr: tr, g: g}
}

// respond sets the fake guest's answer: a service response line for this fixture's request.
func (f *governedFixture) respond(status, code string, receipt map[string]any, exit int) []byte {
	resp := map[string]any{"schema_version": "tbound-pi-service-response/v1", "request_id": f.req.RequestID, "status": status}
	if code != "" {
		resp["error_code"] = code
	}
	if receipt != nil {
		resp["receipt"] = receipt
	}
	line, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	f.tr.stdout, f.tr.code = append(line, '\n'), exit
	return line
}

func (f *governedFixture) receipt(success bool) map[string]any {
	return map[string]any{
		"schema_version": "tbound-production-pi-receipt/v1", "run_id": "production-run-abc",
		"provider_exchange": true, "success": success, "disposition": "NO_DIFF",
		"tool_sequence":                    []string{"read", "bash"},
		"publication_interval_ref":         IntervalRef(f.step),
		"publication_journal_head":         strings.Repeat("a", 64),
		"publication_journal_record_count": 0,
	}
}

func digestOfLine(line []byte) string {
	sum := sha256.Sum256(line)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestGovernedCCompletedReceiptIsMapped(t *testing.T) {
	f := newGovernedFixture(t)
	line := f.respond("completed", "", f.receipt(true), 0)
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil {
		t.Fatal(err)
	}
	wantArgv, _ := f.req.Argv()
	if f.tr.callCount() != 1 || !reflect.DeepEqual(f.tr.calls[0], wantArgv) {
		t.Fatalf("transport calls = %q, want one call with %q", f.tr.calls, wantArgv)
	}
	if o.E01.Digest != digestOfLine(line) || o.E01.Verified {
		t.Fatalf("E01 = %+v, want the unverified terminal digest %s", o.E01, digestOfLine(line))
	}
	if o.Metrics.LatencyMS < 0 || o.Metrics.ToolCalls != 2 || o.Partial {
		t.Fatalf("metrics/partial = %+v %v", o.Metrics, o.Partial)
	}
	pf := o.PublicationFree
	if pf == nil || pf.AnchorHead != strings.Repeat("a", 64) || pf.AnchorCount != 0 || pf.JournalDigest != pf.AnchorHead {
		t.Fatalf("publication anchor = %+v", pf)
	}
	// No collector exists: nothing is faked. No oracle finding, no route coverage,
	// and without journal bytes the interval is not exempt.
	if len(o.Oracles) != 0 || o.Route.Complete || len(pf.Journal) != 0 {
		t.Fatalf("absent evidence was invented: oracles=%v route=%+v journal=%d", o.Oracles, o.Route, len(pf.Journal))
	}
	o.Interval = IntervalRef(f.step)
	if o.publicationFree() {
		t.Fatal("an interval without journal bytes is exempt")
	}
}

func TestGovernedCOptionalTokensAndCostAreCopiedWhenPresent(t *testing.T) {
	f := newGovernedFixture(t)
	r := f.receipt(true)
	r["in_tokens"], r["out_tokens"], r["cost_micro_usd"] = 120, 30, 4500
	f.respond("completed", "", r, 0)
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil || o.Metrics.InTokens != 120 || o.Metrics.OutTokens != 30 || o.Metrics.CostMicroUSD != 4500 {
		t.Fatalf("metrics = %+v, %v", o.Metrics, err)
	}
}

func TestGovernedCFailureStatusIsAPartialOutcomeNeverASuccess(t *testing.T) {
	f := newGovernedFixture(t)
	// The client prints the line and exits 2 for a completed run with success false.
	f.respond("completed", "", f.receipt(false), 2)
	o, err := f.g.Execute(context.Background(), f.step)
	if !errors.Is(err, ErrGovernedFailed) {
		t.Fatalf("err = %v, want ErrGovernedFailed", err)
	}
	if o.E01.Digest == "" || o.PublicationFree == nil {
		t.Fatalf("a failed run keeps its identity and anchor: %+v", o)
	}
	if !hasOutcome(o) {
		t.Fatal("a failed run must come back as a partial outcome")
	}
}

func TestGovernedCUnknownStatusIsAPartialOutcomeWithItsCode(t *testing.T) {
	f := newGovernedFixture(t)
	f.respond("unknown", "task-outcome-unknown", nil, 2)
	f.tr.stderr = []byte("local Pi service unknown: task-outcome-unknown\n")
	o, err := f.g.Execute(context.Background(), f.step)
	if !errors.Is(err, ErrGovernedUnknown) || !strings.Contains(err.Error(), "task-outcome-unknown") {
		t.Fatalf("err = %v", err)
	}
	if !hasOutcome(o) || o.PublicationFree != nil || o.E01.Verified {
		t.Fatalf("unknown outcome = %+v", o)
	}
}

func TestGovernedCRefusedAndBusyAreSetupFailures(t *testing.T) {
	for _, status := range []string{"refused", "busy"} {
		f := newGovernedFixture(t)
		f.respond(status, "invalid-request", nil, 2)
		o, err := f.g.Execute(context.Background(), f.step)
		if !errors.Is(err, ErrSetupRefused) || hasOutcome(o) {
			t.Errorf("%s: outcome=%+v err=%v, want an empty outcome and ErrSetupRefused", status, o, err)
		}
	}
}

func TestGovernedCMalformedOutputIsNeverASuccess(t *testing.T) {
	good := func(f *governedFixture) string {
		f.respond("completed", "", f.receipt(true), 0)
		return string(f.tr.stdout)
	}
	cases := map[string]func(f *governedFixture){
		"not json":      func(f *governedFixture) { f.tr.stdout, f.tr.code = []byte("oops\n"), 0 },
		"truncated":     func(f *governedFixture) { f.tr.stdout, f.tr.code = []byte(good(f)[:40]), 0 },
		"two lines":     func(f *governedFixture) { l := good(f); f.tr.stdout = []byte(l + l) },
		"trailing junk": func(f *governedFixture) { f.tr.stdout = []byte(strings.TrimSpace(good(f)) + " x\n") },
		"empty stdout":  func(f *governedFixture) { f.tr.stdout, f.tr.code = nil, 0 },
		"wrong schema": func(f *governedFixture) {
			f.tr.stdout = []byte(strings.Replace(good(f), "response/v1", "response/v0", 1))
		},
		"other request id": func(f *governedFixture) {
			f.tr.stdout = []byte(strings.Replace(good(f), f.req.RequestID, "tbr-"+strings.Repeat("0", 32), 1))
		},
		"unknown status": func(f *governedFixture) { f.respond("great", "", f.receipt(true), 0) },
		"no receipt":     func(f *governedFixture) { f.respond("completed", "", nil, 0) },
		"bad receipt":    func(f *governedFixture) { f.respond("completed", "", map[string]any{"success": true}, 0) },
		"success but exit 2": func(f *governedFixture) {
			f.respond("completed", "", f.receipt(true), 2)
		},
		"success but exit 1": func(f *governedFixture) {
			f.respond("completed", "", f.receipt(true), 1)
		},
		"failure with exit 0": func(f *governedFixture) {
			f.respond("completed", "", f.receipt(false), 0)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newGovernedFixture(t)
			mutate(f)
			o, err := f.g.Execute(context.Background(), f.step)
			if err == nil {
				t.Fatalf("malformed output returned success: %+v", o)
			}
			if o.PublicationFree != nil && o.PublicationFree.AnchorHead != "" && strings.HasPrefix(name, "not") {
				t.Fatalf("anchor from garbage: %+v", o.PublicationFree)
			}
		})
	}
}

func TestGovernedCTransportFailureIsASetupFailure(t *testing.T) {
	f := newGovernedFixture(t)
	f.tr.err = errors.New("ssh: not found")
	o, err := f.g.Execute(context.Background(), f.step)
	if err == nil || hasOutcome(o) {
		t.Fatalf("outcome=%+v err=%v", o, err)
	}
	// ssh itself failing (exit 255, nothing on stdout) says nothing about the run.
	f = newGovernedFixture(t)
	f.tr.code, f.tr.stderr = 255, []byte("Host key verification failed.\n")
	o, err = f.g.Execute(context.Background(), f.step)
	if err == nil || hasOutcome(o) || !strings.Contains(err.Error(), "255") {
		t.Fatalf("outcome=%+v err=%v", o, err)
	}
}

func TestGovernedCWrongIntervalRefLeavesNoAnchor(t *testing.T) {
	probe := newGovernedFixture(t)
	refs := map[string]string{
		"other step":    "x|trajectory|C|0#1",
		"other attempt": strings.TrimSuffix(IntervalRef(probe.step), "#1") + "#2",
		"empty":         "",
	}
	for name, ref := range refs {
		f := newGovernedFixture(t)
		r := f.receipt(true)
		r["publication_interval_ref"] = ref
		f.respond("completed", "", r, 0)
		o, err := f.g.Execute(context.Background(), f.step)
		if o.PublicationFree != nil {
			t.Errorf("%s: anchor %+v from a receipt of another interval", name, o.PublicationFree)
		}
		if !errors.Is(err, ErrAnchorMismatch) {
			t.Errorf("%s: err = %v, want ErrAnchorMismatch", name, err)
		}
	}
	// A receipt that carries no head carries no anchor, and nothing is wrong with it.
	f := newGovernedFixture(t)
	r := f.receipt(true)
	delete(r, "publication_interval_ref")
	delete(r, "publication_journal_head")
	f.respond("completed", "", r, 0)
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil || o.PublicationFree != nil {
		t.Fatalf("anchorless receipt: %+v, %v", o.PublicationFree, err)
	}
}

// stubCollectors serves chosen evidence and absence for everything else.
type stubCollectors struct {
	AbsentCollectors
	journal []byte
	e04     *wsoracle.EvaluateE04Input
	net     map[string]Finding
	refs    map[string]OracleRef // nil: a valid ref for every net finding
	err     error
	seen    EvidenceRef
}

func (c *stubCollectors) PublicationJournal(_ context.Context, ref EvidenceRef) ([]byte, error) {
	c.seen = ref
	if c.journal == nil {
		return nil, ErrEvidenceAbsent
	}
	return c.journal, nil
}

func (c *stubCollectors) Net(context.Context, EvidenceRef) (ObserverEvidence, error) {
	if c.err != nil {
		return ObserverEvidence{}, c.err
	}
	if c.net == nil {
		return ObserverEvidence{}, ErrEvidenceAbsent
	}
	refs := c.refs
	if refs == nil {
		refs = map[string]OracleRef{}
		for k := range c.net {
			refs[k] = OracleRef{Version: NetOracleVersion, Report: "net-report-" + k}
		}
	}
	return ObserverEvidence{Findings: c.net, Refs: refs}, nil
}

func TestGovernedCJournalFromTheCollectorMakesTheIntervalExemptOnlyWithAMatchingAnchor(t *testing.T) {
	f := newGovernedFixture(t)
	journal, head := auditChain(audit.Event{Kind: IntervalMarkerKind, ID: IntervalRef(f.step)})
	r := f.receipt(true)
	r["publication_journal_head"] = head
	f.respond("completed", "", r, 0)
	c := &stubCollectors{journal: journal}
	f.g.Collectors = c
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(o.PublicationFree.Journal, journal) {
		t.Fatal("journal bytes were not carried")
	}
	o.Interval = IntervalRef(f.step)
	if !o.publicationFree() {
		t.Fatal("matching journal and anchor should be exempt")
	}
	if c.seen.RunID != "production-run-abc" || c.seen.Interval != IntervalRef(f.step) || c.seen.Request != f.req {
		t.Fatalf("collector reference = %+v", c.seen)
	}
	// A journal that does not match the service's head is carried but is not evidence.
	f.respond("completed", "", f.receipt(true), 0)
	o, err = f.g.Execute(context.Background(), f.step)
	o.Interval = IntervalRef(f.step)
	if err != nil || o.publicationFree() {
		t.Fatalf("journal against a different anchor head: exempt=%v err=%v", o.publicationFree(), err)
	}
}

func TestGovernedCCollectorFindingsAreRecordedAndAbsenceStaysAbsent(t *testing.T) {
	f := newGovernedFixture(t)
	f.respond("completed", "", f.receipt(true), 0)
	f.g.Collectors = &stubCollectors{net: map[string]Finding{OracleNet01: FindingNone, OracleNet02: FindingObserved}}
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil {
		t.Fatal(err)
	}
	if o.Oracles[OracleNet01] != FindingNone || o.Oracles[OracleNet02] != FindingObserved {
		t.Fatalf("NET findings = %v", o.Oracles)
	}
	if _, ok := o.Oracles[OracleProc01]; ok {
		t.Fatalf("PROC was not collected but has a finding: %v", o.Oracles)
	}
	// A collector may not speak for another instrument's predicates, and a failing
	// collector makes the outcome partial instead of dropping what is known.
	for name, c := range map[string]*stubCollectors{
		"foreign key": {net: map[string]Finding{OracleWS01: FindingNone}},
		"bad value":   {net: map[string]Finding{OracleNet01: "fine"}},
		"error":       {err: errors.New("observer crashed")},
	} {
		f.g.Collectors = c
		o, err := f.g.Execute(context.Background(), f.step)
		if err == nil || !hasOutcome(o) || o.E01.Digest == "" {
			t.Errorf("%s: outcome=%+v err=%v", name, o, err)
		}
		if _, ok := o.Oracles[OracleWS01]; ok {
			t.Errorf("%s: foreign finding recorded", name)
		}
	}
}

func TestGovernedCE04ManifestsAreEvaluatedByTheOracle(t *testing.T) {
	f := newGovernedFixture(t)
	f.respond("completed", "", f.receipt(true), 0)
	// An incomplete input is a gap in the oracle's own words: lost, never none.
	f.g.Collectors = &e04Collectors{in: wsoracle.EvaluateE04Input{}}
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range o.Oracles {
		if v == FindingNone {
			t.Errorf("%s = none from an input with no manifests", k)
		}
	}
	if o.OracleVersion != wsoracle.OracleVersion || o.OracleReport == "" {
		t.Fatalf("E04 provenance missing: %+v", o)
	}
}

type e04Collectors struct {
	AbsentCollectors
	in wsoracle.EvaluateE04Input
}

func (c *e04Collectors) E04(context.Context, EvidenceRef) (wsoracle.EvaluateE04Input, error) {
	return c.in, nil
}

func TestGovernedCPrepareNeverCallsTheTransport(t *testing.T) {
	f := newGovernedFixture(t)
	if err := f.g.Prepare(context.Background(), f.step); err != nil {
		t.Fatal(err)
	}
	if f.tr.callCount() != 0 {
		t.Fatal("Prepare used the transport")
	}
	// Prepare refuses what Execute could not run, still without touching the guest.
	bad := f.step
	bad.Attempt = 3
	if err := f.g.Prepare(context.Background(), bad); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("attempt 3: %v", err)
	}
	notC := f.step
	notC.Configuration = CfgP
	if err := f.g.Prepare(context.Background(), notC); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("P step: %v", err)
	}
	if err := (GovernedC{Transport: f.tr}).Prepare(context.Background(), f.step); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("no plan digest: %v", err)
	}
	if err := (GovernedC{PlanDigest: "d"}).Prepare(context.Background(), f.step); !errors.Is(err, ErrNotWired) {
		t.Fatalf("no transport: %v", err)
	}
	if f.tr.callCount() != 0 {
		t.Fatal("a refusing Prepare used the transport")
	}
	bad2 := GovernedC{PlanDigest: f.g.PlanDigest, Transport: &SSHTransport{}}
	if err := bad2.Prepare(context.Background(), f.step); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("incomplete ssh config: %v", err)
	}
}

func TestGovernedCUnwiredExecuteStaysErrNotWired(t *testing.T) {
	f := newGovernedFixture(t)
	if _, err := (GovernedC{PlanDigest: f.g.PlanDigest}).Execute(context.Background(), f.step); !errors.Is(err, ErrNotWired) {
		t.Fatalf("Execute without a transport: %v", err)
	}
}

func goodSSH() SSHTransport {
	return SSHTransport{SSHPath: "/usr/bin/ssh", IdentityFile: "/keys/runner_ed25519", User: "tbound-op", Host: "172.22.198.27",
		HostKeyAlias: "172.18.155.19", KnownHostsFile: "/keys/known_hosts"}
}

func TestSSHTransportNeverRelaxesHostChecking(t *testing.T) {
	tr := goodSSH()
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}
	args, err := tr.sshArgs([]string{"serve", "--pi"})
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" StrictHostKeyChecking=yes ", " BatchMode=yes ", " HostKeyAlias=172.18.155.19 ",
		" UserKnownHostsFile=/keys/known_hosts ", " IdentitiesOnly=yes ", " -i /keys/runner_ed25519 "} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh args lack %q: %q", want, args)
		}
	}
	for _, bad := range []string{"StrictHostKeyChecking=no", "StrictHostKeyChecking=accept-new", "UserKnownHostsFile=/dev/null", "-A "} {
		if strings.Contains(joined, bad) {
			t.Errorf("ssh args contain %q", bad)
		}
	}
	if n := strings.Count(joined, "StrictHostKeyChecking"); n != 1 {
		t.Errorf("StrictHostKeyChecking appears %d times", n)
	}
	// The destination follows `--`, then exactly one remote command word.
	last := len(args) - 1
	if args[last-2] != "--" || args[last-1] != "tbound-op@172.22.198.27" {
		t.Fatalf("destination/command layout: %q", args[last-2:])
	}
}

func TestSSHTransportRefusesInjectableOrIncompleteConfig(t *testing.T) {
	mutations := map[string]func(*SSHTransport){
		"no ssh":         func(c *SSHTransport) { c.SSHPath = "" },
		"no identity":    func(c *SSHTransport) { c.IdentityFile = "" },
		"no user":        func(c *SSHTransport) { c.User = "" },
		"no host":        func(c *SSHTransport) { c.Host = "" },
		"no alias":       func(c *SSHTransport) { c.HostKeyAlias = "" },
		"no known hosts": func(c *SSHTransport) { c.KnownHostsFile = "" },
		"option as host": func(c *SSHTransport) { c.Host = "-oProxyCommand=x" },
		"option as user": func(c *SSHTransport) { c.User = "-oProxyCommand=x" },
		"user with @":    func(c *SSHTransport) { c.User = "a@b" },
		"host with space": func(c *SSHTransport) {
			c.Host = "h -o StrictHostKeyChecking=no"
		},
		"alias with option": func(c *SSHTransport) { c.HostKeyAlias = "a StrictHostKeyChecking=no" },
		"known hosts space": func(c *SSHTransport) { c.KnownHostsFile = "/k h" },
		"known hosts null":  func(c *SSHTransport) { c.KnownHostsFile = "/dev/null" },
		"launcher empty word": func(c *SSHTransport) {
			c.ClientCommand = []string{"setpriv", "", "tbound"}
		},
		"launcher NUL": func(c *SSHTransport) { c.ClientCommand = []string{"tb\x00ound"} },
	}
	for name, mutate := range mutations {
		c := goodSSH()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: config accepted", name)
		}
		if _, err := c.sshArgs([]string{"serve"}); err == nil {
			t.Errorf("%s: sshArgs built a command from a bad config", name)
		}
	}
}

func TestSSHTransportRunsTheRunbookClientIdentity(t *testing.T) {
	tr := goodSSH()
	args, err := tr.sshArgs([]string{"serve", "--pi"})
	if err != nil {
		t.Fatal(err)
	}
	remote := args[len(args)-1]
	want := "'setpriv' '--reuid' '994' '--regid' '985' '--clear-groups' " +
		"'--bounding-set=-all' '--inh-caps=-all' '--ambient-caps=-all' '--no-new-privs' '--' '/usr/local/bin/tbound' 'serve' '--pi'"
	if remote != want {
		t.Fatalf("remote command = %s, want %s", remote, want)
	}
}

func TestShellQuoteHoldsAgainstHostileIDsThroughARealShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	hostile := []string{
		"plain", "", "a b", "it's", "'; touch /tmp/pwned; '", "$(id)", "`id`", "a\nb", "$HOME", "\\", "\"", "a;b|c&d>e<f",
		"--flag=x", "-n", "*", "~", "!!", "é€", "x'\"'\"'y",
	}
	cmd := "printf '%s\\0'"
	for _, a := range hostile {
		q, err := shellQuote(a)
		if err != nil {
			t.Fatalf("%q: %v", a, err)
		}
		cmd += " " + q
	}
	out, err := exec.Command(sh, "-c", cmd).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if !reflect.DeepEqual(got, hostile) {
		t.Fatalf("round trip through sh changed the arguments:\n got %q\nwant %q", got, hostile)
	}
	if _, err := shellQuote("a\x00b"); err == nil {
		t.Fatal("NUL accepted")
	}
}

func TestSSHTransportArgvCarriesAHostileCaseIDAsOneQuotedWord(t *testing.T) {
	tr := goodSSH()
	req := GovernedRequest{TaskID: "x'; rm -rf / #", Role: GovRoleTrajectory, Attempt: 1, RequestID: "tbr-" + strings.Repeat("1", 32)}
	argv, err := req.Argv()
	if err != nil {
		t.Fatal(err)
	}
	args, err := tr.sshArgs(argv)
	if err != nil {
		t.Fatal(err)
	}
	remote := args[len(args)-1]
	if !strings.Contains(remote, `'x'\''; rm -rf / #'`) {
		t.Fatalf("hostile id not quoted as one word: %s", remote)
	}
}

func TestGovernedCNetFindingsNeedAReportRefOfTheNetVersion(t *testing.T) {
	f := newGovernedFixture(t)
	f.respond("completed", "", f.receipt(true), 0)
	good := OracleRef{Version: NetOracleVersion, Report: "r"}
	for name, c := range map[string]*stubCollectors{
		"no refs":         {net: map[string]Finding{OracleNet01: FindingNone}, refs: map[string]OracleRef{}},
		"wrong version":   {net: map[string]Finding{OracleNet01: FindingNone}, refs: map[string]OracleRef{OracleNet01: {Version: ProcOracleVersion, Report: "r"}}},
		"empty report":    {net: map[string]Finding{OracleNet01: FindingNone}, refs: map[string]OracleRef{OracleNet01: {Version: NetOracleVersion}}},
		"one of two lack": {net: map[string]Finding{OracleNet01: FindingNone, OracleNet02: FindingObserved}, refs: map[string]OracleRef{OracleNet01: good}},
	} {
		f.g.Collectors = c
		o, err := f.g.Execute(context.Background(), f.step)
		if !errors.Is(err, ErrOracleProvenance) {
			t.Errorf("%s: err = %v, want ErrOracleProvenance", name, err)
		}
		if len(o.Oracles) != 0 {
			t.Errorf("%s: findings recorded without provenance: %v", name, o.Oracles)
		}
	}
	// Lost evidence credits nothing and may come without a reference.
	f.g.Collectors = &stubCollectors{net: map[string]Finding{OracleNet01: FindingLost}, refs: map[string]OracleRef{}}
	if o, err := f.g.Execute(context.Background(), f.step); err != nil || o.Oracles[OracleNet01] != FindingLost {
		t.Errorf("lost finding: %v, %v", o.Oracles, err)
	}
	// The recorded finding carries its ref, and the outcome is exportable.
	f.g.Collectors = &stubCollectors{net: map[string]Finding{OracleNet01: FindingNone}}
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil || !o.refValid(OracleNet01) || !oracleVersionOK(&o) {
		t.Errorf("valid ref: refs %v, err %v", o.OracleRefs, err)
	}
}

// A refused or busy answer is followed by exit 2 like every answer but a successful
// completion. Exit 0 beside it contradicts the client's contract: the answer is
// malformed, not a clean refusal that voids the begin.
func TestGovernedCRefusedOrBusyWithAnInconsistentExitIsMalformed(t *testing.T) {
	for _, status := range []string{"refused", "busy"} {
		for _, exit := range []int{0, 1, 3} {
			f := newGovernedFixture(t)
			f.respond(status, "invalid-request", nil, exit)
			_, err := f.g.Execute(context.Background(), f.step)
			if !errors.Is(err, ErrGovernedMalformed) {
				t.Errorf("%s with exit %d: err = %v, want ErrGovernedMalformed", status, exit, err)
			}
			if errors.Is(err, ErrSetupRefused) {
				t.Errorf("%s with exit %d: reported as a clean refusal: %v", status, exit, err)
			}
		}
	}
}

func TestSSHTransportIgnoresUserAndSystemSSHConfiguration(t *testing.T) {
	args, err := goodSSH().sshArgs([]string{"serve", "--pi"})
	if err != nil {
		t.Fatal(err)
	}
	// The option words, up to the destination.
	opts := args[:len(args)-3]
	has := func(pair ...string) bool {
		for i := 0; i+len(pair) <= len(opts); i++ {
			if slices.Equal(opts[i:i+len(pair)], pair) {
				return true
			}
		}
		return false
	}
	for _, want := range [][]string{
		{"-F", "/dev/null"},
		{"-o", "GlobalKnownHostsFile=/dev/null"},
		{"-o", "UserKnownHostsFile=/keys/known_hosts"},
		{"-o", "CanonicalizeHostname=no"},
		{"-o", "ProxyCommand=none"},
		{"-o", "ProxyJump=none"},
	} {
		if !has(want...) {
			t.Errorf("ssh args lack %q: %q", want, args)
		}
	}
	if args[len(args)-3] != "--" {
		t.Errorf("destination does not follow --: %q", args)
	}
}
