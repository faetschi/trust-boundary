package guestevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

var (
	testCommit = strings.Repeat("a", 40)
	testBinary = digestOf("tbound-profile")
)

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func expect() Expect {
	tb := map[string]string{}
	for _, p := range TestPackages() {
		tb[p] = digestOf("test binary " + p)
	}
	return Expect{Commit: testCommit, BinarySHA256: testBinary, FrozenSourceDigest: digestOf("source"), CommitTree: adapterTree, TestBinaries: tb, BaseTemplateDigest: digestOf("base template"), ImageDigest: digestOf("image"), AdminPubkeyDigest: digestOf("admin key"), RuntimeBundleDigest: digestOf("runtime bundle"), RunnerDigest: digestOf("runner")}
}

// The profile and template of a run under its own config (G1, route probe): never the pilot's.
var (
	g1ProfileDigest  = digestOf("g1 profile")
	g1TemplateDigest = digestOf("g1 template")
)

// goodBinding is the binding a producer states for a run under its own signed profile.
func goodBinding(e Expect, profile, template string) ProfileBinding {
	return ProfileBinding{ProfileDigest: profile, TemplateDigest: template, BaseTemplateDigest: e.BaseTemplateDigest,
		RegistrationDigest: digestOf("registration of " + profile), ImageDigest: e.ImageDigest, SourceDigest: e.FrozenSourceDigest, AdminPubkeyDigest: e.AdminPubkeyDigest,
		RuntimeBundleDigest: e.RuntimeBundleDigest, RunnerDigest: e.RunnerDigest}
}

// The profile of a route-probe run under its own config.
var probeProfileDigest = digestOf("route probe profile")

func goodProviderExchange(t *testing.T, e Expect) ProviderExchange {
	t.Helper()
	p := NewProviderExchange(goodResponse(t, e, nil))
	p.Binding = goodBinding(e, g1ProfileDigest, g1TemplateDigest)
	return p
}

func encode(t *testing.T, kind string, data any) []byte {
	t.Helper()
	b, err := Encode(kind, testCommit, testBinary, data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func goodCapability() Capability {
	return Capability{KernelRelease: "6.8.0-synthetic", Arch: "amd64",
		Cgroup: CapabilityCgroup{Mount: "/sys/fs/cgroup", FSType: "cgroup2", Controllers: []string{"cpuset", "cpu", "io", "memory", "pids"},
			ServiceCgroup: ServiceCgroup, ServiceCgroupType: "domain", KillInterface: true,
			Chain: goodChain(), CellsProbe: goodCellsProbe()},
		Landlock: CapabilityLandlock{ABI: 8, ErrataSupported: true, Errata: 3},
		Seccomp:  CapabilitySeccomp{FilterInstalled: true, Mode: 2, ProfileSHA256: digestOf("seccomp")}}
}

func goodNetSink() NetSink {
	return NetSink{BootID: "boot-1",
		Attestation:    NetSinkAttestation{Schema: NetSinkAttestSchema, BootID: "boot-1", ParentDev: 5, ParentIno: 77, RuleDigest: NetSinkRuleDigest},
		RuleFileSHA256: NetSinkRuleDigest, CellsCgroup: CellsCgroup, NftListingSHA256: digestOf("listing"), RulesetVerified: true,
		Probes: NetSinkProbes{AllowedConnect: ProbeConnected, WrongPort: ProbeTimeout, OtherLoopback: ProbeTimeout, UDP: ProbeSent,
			DropCounterBefore: 10, DropCounterAfter: 16}}
}

func goodReceipt(e Expect) map[string]any {
	return map[string]any{"schema_version": receiptSchema, "profile_hash": g1ProfileDigest, "containment_profile_digest": g1TemplateDigest, "frozen_source_digest": e.FrozenSourceDigest,
		"provider_exchange": true, "claim_bearing": false, "task_admitted": true, "turn_completed": true, "success": true,
		"tool_sequence": []string{"read", "edit", "bash", "read"}, "provider_turns": 5,
		"source_generation": "g0", "final_generation": "g2", "generation_count": 3,
		"settlement": "STOPPED", "exit_observed": true, "exit_code": -1, "descendants_settled": true, "some_future_member": 1} // G1 on F10: the supervisor stops the worker after the turn
}

func goodResponse(t *testing.T, e Expect, mutate func(response, receipt map[string]any)) string {
	t.Helper()
	receipt := goodReceipt(e)
	response := map[string]any{"schema_version": responseSchema, "request_id": "req-1", "status": "completed", "receipt": receipt}
	if mutate != nil {
		mutate(response, receipt)
	}
	b, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func goodTests(kind string, e Expect) TestRun {
	spec, _ := SpecFor(kind)
	required := spec.Required
	if kind == KindLandlockConnectTests {
		required = []string{"TestLandlockConnectRightIsPort8080OnlyAndOnlyForNetSink", "TestLandlockTSYNCReachesEnforcement"}
	}
	r := TestRun{Package: spec.Package, TestBinarySHA256: e.TestBinaries[spec.Package], Args: slices.Clone(TestRunArgs), KernelRelease: "6.8.0-synthetic",
		OutputSHA256: digestOf("output")}
	for _, name := range required {
		r.Results = append(r.Results, TestResult{Test: name, Action: TestPass})
	}
	r.Results = append(r.Results, TestResult{Test: "TestSomethingElseSkips", Action: TestSkip})
	return r
}

type artifactCase struct {
	kind string
	good any
}

func goodArtifacts(t *testing.T, e Expect) []artifactCase {
	t.Helper()
	return []artifactCase{
		{KindCapability, goodCapability()},
		{KindNetSink, goodNetSink()},
		{KindProviderExchange, goodProviderExchange(t, e)},
		{KindProcOracleTests, goodTests(KindProcOracleTests, e)},
		{KindLandlockConnectTests, goodTests(KindLandlockConnectTests, e)},
		{KindAdapterTests, goodAdapter(t)},
		{KindRouteBashView, goodRouteDenied(KindRouteBashView)},
		{KindRouteDirectRuntime, goodRouteDenied(KindRouteDirectRuntime)},
	}
}

func TestEveryKindHasAGoodArtifactAndAValidator(t *testing.T) {
	e := expect()
	var kinds []string
	for _, c := range goodArtifacts(t, e) {
		kinds = append(kinds, c.kind)
		if err := Validate(c.kind, encode(t, c.kind, c.good), e); err != nil {
			t.Errorf("%s: a good artifact was refused: %v", c.kind, err)
		}
	}
	slices.Sort(kinds)
	if !slices.Equal(kinds, Kinds()) {
		t.Fatalf("good artifacts exist for %v, kinds are %v", kinds, Kinds())
	}
}

// The envelope binds every artifact to the frozen commit and to the binary that wrote it.
func TestEnvelopeIsBoundToTheCommitAndTheBinary(t *testing.T) {
	e := expect()
	for _, c := range goodArtifacts(t, e) {
		good := encode(t, c.kind, c.good)
		other := strings.Repeat("b", 40)
		for name, mutate := range map[string]func(Expect) Expect{
			"another commit":         func(x Expect) Expect { x.Commit = other; return x },
			"another binary":         func(x Expect) Expect { x.BinarySHA256 = digestOf("another"); return x },
			"no expected commit":     func(x Expect) Expect { x.Commit = ""; return x },
			"no expected binary":     func(x Expect) Expect { x.BinarySHA256 = ""; return x },
			"malformed binary pin":   func(x Expect) Expect { x.BinarySHA256 = "abc"; return x },
			"uppercase commit pin":   func(x Expect) Expect { x.Commit = strings.ToUpper(x.Commit); return x },
			"short commit pin":       func(x Expect) Expect { x.Commit = x.Commit[:39]; return x },
			"no digest after sha256": func(x Expect) Expect { x.BinarySHA256 = "sha256:"; return x },
		} {
			if !IsGuestKind(c.kind) && (strings.Contains(name, "binary") || strings.Contains(name, "sha256")) {
				continue // an adapter run is bound to the commit tree, not to the guest binary
			}
			if err := Validate(c.kind, good, mutate(e)); err == nil {
				t.Errorf("%s with %s: accepted", c.kind, name)
			}
		}
		// The same document under another kind's name.
		for _, k := range Kinds() {
			if k != c.kind {
				if err := Validate(k, good, e); err == nil {
					t.Errorf("a %s document was accepted as %s", c.kind, k)
				}
			}
		}
		if err := Validate("guest-unknown", good, e); err == nil {
			t.Error("an unknown kind was accepted")
		}
	}
}

func TestEnvelopeIsStrict(t *testing.T) {
	e := expect()
	for _, c := range goodArtifacts(t, e) {
		good := string(encode(t, c.kind, c.good))
		for name, doc := range map[string]string{
			"empty":                 "",
			"not json":              "PASS cgroup-v2\n",
			"array":                 "[]",
			"wrong schema":          strings.Replace(good, Schema, "tbound-guest-evidence/v0", 1),
			"unknown member":        strings.Replace(good, `"kind"`, `"extra": 1, "kind"`, 1),
			"duplicate key":         strings.Replace(good, `"kind"`, `"kind": "`+c.kind+`", "kind"`, 1),
			"trailing data":         good + "{}",
			"no data":               strings.Replace(good, `"data"`, `"dat"`, 1),
			"unknown data member":   strings.Replace(good, `"data": {`, `"data": {"extra": 1,`, 1),
			"duplicate data member": duplicateFirstDataMember(good),
			"data is null":          nullData(t, good),
			"too big":               good + strings.Repeat(" ", MaxBytes),
			"truncated":             good[:len(good)/2],
		} {
			if err := Validate(c.kind, []byte(doc), e); err == nil {
				t.Errorf("%s / %s: accepted", c.kind, name)
			}
		}
	}
}

func nullData(t *testing.T, doc string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	m["data"] = json.RawMessage("null")
	b, _ := json.Marshal(m)
	return string(b)
}

// duplicateFirstDataMember repeats the first member of data under the same name.
func duplicateFirstDataMember(doc string) string {
	i := strings.Index(doc, `"data": {`) + len(`"data": {`)
	rest := strings.TrimLeft(doc[i:], " \n")
	end := strings.IndexAny(rest, ",}")
	return doc[:i] + rest[:end] + "," + rest
}

func TestCapabilityRequiredValues(t *testing.T) {
	e := expect()
	for name, mutate := range map[string]func(*Capability){
		"landlock abi 1":            func(c *Capability) { c.Landlock.ABI = 1 },
		"landlock abi 7":            func(c *Capability) { c.Landlock.ABI = 7 },
		"landlock abi 9":            func(c *Capability) { c.Landlock.ABI = 9 },
		"no landlock":               func(c *Capability) { c.Landlock = CapabilityLandlock{} },
		"errata without support":    func(c *Capability) { c.Landlock.ErrataSupported = false; c.Landlock.Errata = 1 },
		"cgroup v1":                 func(c *Capability) { c.Cgroup.FSType = "cgroup" },
		"relative mount":            func(c *Capability) { c.Cgroup.Mount = "sys/fs/cgroup" },
		"no cpu":                    func(c *Capability) { c.Cgroup.Controllers = []string{"memory", "pids"} },
		"no memory":                 func(c *Capability) { c.Cgroup.Controllers = []string{"cpu", "pids"} },
		"no pids":                   func(c *Capability) { c.Cgroup.Controllers = []string{"cpu", "memory"} },
		"no controllers":            func(c *Capability) { c.Cgroup.Controllers = nil },
		"another service cgroup":    func(c *Capability) { c.Cgroup.ServiceCgroup = "/system.slice/other.service" },
		"threaded cgroup":           func(c *Capability) { c.Cgroup.ServiceCgroupType = "threaded" },
		"no cgroup.kill":            func(c *Capability) { c.Cgroup.KillInterface = false },
		"no seccomp filter":         func(c *Capability) { c.Seccomp.FilterInstalled = false },
		"seccomp strict mode":       func(c *Capability) { c.Seccomp.Mode = 1 },
		"no seccomp mode":           func(c *Capability) { c.Seccomp.Mode = 0 },
		"no seccomp profile digest": func(c *Capability) { c.Seccomp.ProfileSHA256 = "" },
		"bad seccomp profile":       func(c *Capability) { c.Seccomp.ProfileSHA256 = "sha256:zz" },
		"no kernel":                 func(c *Capability) { c.KernelRelease = " " },
		"arm64":                     func(c *Capability) { c.Arch = "arm64" },
	} {
		c := goodCapability()
		mutate(&c)
		if err := Validate(KindCapability, encode(t, KindCapability, c), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNetSinkRequiredValues(t *testing.T) {
	e := expect()
	for name, mutate := range map[string]func(*NetSink){
		"empty boot id":               func(n *NetSink) { n.BootID = "" },
		"attestation of another boot": func(n *NetSink) { n.Attestation.BootID = "boot-0" },
		"attestation schema":          func(n *NetSink) { n.Attestation.Schema = "x" },
		"attestation of another rule": func(n *NetSink) { n.Attestation.RuleDigest = digestOf("other rule") },
		"attestation without parent":  func(n *NetSink) { n.Attestation.ParentIno = 0 },
		"another rule file":           func(n *NetSink) { n.RuleFileSHA256 = digestOf("other rule") },
		"another cells cgroup":        func(n *NetSink) { n.CellsCgroup = "/system.slice/other/cells" },
		"no nft listing":              func(n *NetSink) { n.NftListingSHA256 = "" },
		"ruleset not verified":        func(n *NetSink) { n.RulesetVerified = false },
		"the sink was not reachable":  func(n *NetSink) { n.Probes.AllowedConnect = "timeout" },
		"the sink refused":            func(n *NetSink) { n.Probes.AllowedConnect = "refused" },
		"wrong port connected":        func(n *NetSink) { n.Probes.WrongPort = ProbeConnected },
		"wrong port was refused":      func(n *NetSink) { n.Probes.WrongPort = "refused" },
		"other loopback connected":    func(n *NetSink) { n.Probes.OtherLoopback = ProbeConnected },
		"other loopback refused":      func(n *NetSink) { n.Probes.OtherLoopback = "refused" },
		"udp not tried":               func(n *NetSink) { n.Probes.UDP = "" },
		"counter did not move":        func(n *NetSink) { n.Probes.DropCounterAfter = n.Probes.DropCounterBefore },
		"counter moved too little":    func(n *NetSink) { n.Probes.DropCounterAfter = n.Probes.DropCounterBefore + MinDroppedPackets - 1 },
		"counter went backwards":      func(n *NetSink) { n.Probes.DropCounterAfter = n.Probes.DropCounterBefore - 1 },
	} {
		n := goodNetSink()
		mutate(&n)
		if err := Validate(KindNetSink, encode(t, KindNetSink, n), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestProviderExchangeRequiredValues(t *testing.T) {
	e := expect()
	for name, mutate := range map[string]func(response, receipt map[string]any){
		"status refused":       func(r, _ map[string]any) { r["status"] = "refused" },
		"status unknown":       func(r, _ map[string]any) { r["status"] = "unknown" },
		"error code":           func(r, _ map[string]any) { r["error_code"] = "x" },
		"response schema":      func(r, _ map[string]any) { r["schema_version"] = "v0" },
		"no request id":        func(r, _ map[string]any) { r["request_id"] = "" },
		"no receipt":           func(r, _ map[string]any) { delete(r, "receipt") },
		"receipt schema":       func(_, f map[string]any) { f["schema_version"] = "v0" },
		"another profile":      func(_, f map[string]any) { f["profile_hash"] = digestOf("other") },
		"another source":       func(_, f map[string]any) { f["frozen_source_digest"] = digestOf("other") },
		"no provider exchange": func(_, f map[string]any) { f["provider_exchange"] = false },
		"not successful":       func(_, f map[string]any) { f["success"] = false },
		"not admitted":         func(_, f map[string]any) { f["task_admitted"] = false },
		"turn not completed":   func(_, f map[string]any) { f["turn_completed"] = false },
		"claim bearing":        func(_, f map[string]any) { f["claim_bearing"] = true },
		"read only":            func(_, f map[string]any) { f["tool_sequence"] = []string{"read"} },
		"wrong order":          func(_, f map[string]any) { f["tool_sequence"] = []string{"read", "bash", "edit", "read"} },
		"extra call":           func(_, f map[string]any) { f["tool_sequence"] = []string{"read", "edit", "bash", "read", "read"} },
		"too few turns":        func(_, f map[string]any) { f["provider_turns"] = 3 },
		"one generation":       func(_, f map[string]any) { f["generation_count"] = 1; f["final_generation"] = "g0" },
		"another final":        func(_, f map[string]any) { f["final_generation"] = "g3" },
		"unsettled":            func(_, f map[string]any) { f["settlement"] = "UNKNOWN" },
		"exit not observed":    func(_, f map[string]any) { f["exit_observed"] = false },
		"descendants left":     func(_, f map[string]any) { f["descendants_settled"] = false },
	} {
		p := NewProviderExchange(goodResponse(t, e, mutate))
		p.Binding = goodBinding(e, g1ProfileDigest, g1TemplateDigest)
		if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, p), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The wrapper must show the response it names.
	p := goodProviderExchange(t, e)
	p.ResponseSHA256 = digestOf("other response")
	if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, p), e); err == nil {
		t.Error("a digest that is not the response's was accepted")
	}
	// A response with a duplicate key or trailing data is not a response.
	for name, raw := range map[string]string{
		"duplicate status": strings.Replace(goodResponse(t, e, nil), `"status":"completed"`, `"status":"unknown","status":"completed"`, 1),
		"trailing data":    goodResponse(t, e, nil) + "{}",
		"not json":         "local Pi service completed",
	} {
		if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, ProviderExchange{Response: raw, ResponseSHA256: NewProviderExchange(raw).ResponseSHA256, Binding: goodBinding(e, g1ProfileDigest, g1TemplateDigest)}), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The run must pin what the run shares with the pilot, or the receipt is bound to nothing.
	for name, mutate := range map[string]func(*Expect){
		"no source pin":         func(x *Expect) { x.FrozenSourceDigest = "" },
		"no base template pin":  func(x *Expect) { x.BaseTemplateDigest = "" },
		"no admin key pin":      func(x *Expect) { x.AdminPubkeyDigest = "" },
		"no image pin":          func(x *Expect) { x.ImageDigest = "" },
		"no runtime bundle pin": func(x *Expect) { x.RuntimeBundleDigest = "" },
		"no runner pin":         func(x *Expect) { x.RunnerDigest = "" },
	} {
		x := e
		mutate(&x)
		if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, goodProviderExchange(t, e)), x); err == nil {
			t.Errorf("%s: a receipt was accepted although the run pins nothing to bind it to", name)
		}
	}
}

// A G1 run has its own config, registration, template and signed profile, so its receipt can never carry the
// pilot's profile_hash. The artifact is bound to what the runs share instead: the base cell template, the
// image, the source and the admin key.
func TestProviderExchangeIsBoundToTheBaseTemplateNotToThePilotProfile(t *testing.T) {
	e := expect()
	good := goodProviderExchange(t, e)
	if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, good), e); err != nil {
		t.Fatalf("a run under its own profile and template was refused: %v", err)
	}
	for name, mutate := range map[string]func(*ProviderExchange){
		"no binding":                       func(p *ProviderExchange) { p.Binding = ProfileBinding{} },
		"another base template":            func(p *ProviderExchange) { p.Binding.BaseTemplateDigest = digestOf("another base") },
		"another source":                   func(p *ProviderExchange) { p.Binding.SourceDigest = digestOf("another source") },
		"another admin key":                func(p *ProviderExchange) { p.Binding.AdminPubkeyDigest = digestOf("another key") },
		"another image":                    func(p *ProviderExchange) { p.Binding.ImageDigest = digestOf("another image") },
		"another runtime bundle":           func(p *ProviderExchange) { p.Binding.RuntimeBundleDigest = digestOf("another bundle") },
		"another runner":                   func(p *ProviderExchange) { p.Binding.RunnerDigest = digestOf("another runner") },
		"no runtime bundle":                func(p *ProviderExchange) { p.Binding.RuntimeBundleDigest = "" },
		"no runner":                        func(p *ProviderExchange) { p.Binding.RunnerDigest = "" },
		"another profile than the receipt": func(p *ProviderExchange) { p.Binding.ProfileDigest = digestOf("another profile") },
		"another template than the receipt": func(p *ProviderExchange) {
			p.Binding.TemplateDigest = digestOf("another template")
		},
		"no registration":                   func(p *ProviderExchange) { p.Binding.RegistrationDigest = "" },
		"the template is the base template": func(p *ProviderExchange) { p.Binding.TemplateDigest = p.Binding.BaseTemplateDigest },
	} {
		p := good
		mutate(&p)
		if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, p), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTestRunRequiredValues(t *testing.T) {
	e := expect()
	for _, kind := range []string{KindProcOracleTests, KindLandlockConnectTests} {
		spec, _ := SpecFor(kind)
		for name, mutate := range map[string]func(*TestRun){
			"another package":          func(r *TestRun) { r.Package = "tbound/supervisor/internal/sandbox" },
			"another test binary":      func(r *TestRun) { r.TestBinarySHA256 = digestOf("a binary built elsewhere") },
			"no test binary":           func(r *TestRun) { r.TestBinarySHA256 = "" },
			"filtered by -test.run":    func(r *TestRun) { r.Args = append(r.Args, "-test.run=TestX") },
			"cached count":             func(r *TestRun) { r.Args = []string{"-test.v"} },
			"no arguments":             func(r *TestRun) { r.Args = nil },
			"non-zero exit":            func(r *TestRun) { r.ExitCode = 1 },
			"no kernel":                func(r *TestRun) { r.KernelRelease = "" },
			"no output digest":         func(r *TestRun) { r.OutputSHA256 = "" },
			"required test skipped":    func(r *TestRun) { r.Results[0].Action = TestSkip },
			"required test failed":     func(r *TestRun) { r.Results[0].Action = TestFail },
			"required test missing":    func(r *TestRun) { r.Results = r.Results[1:] },
			"no results":               func(r *TestRun) { r.Results = nil },
			"another test failed":      func(r *TestRun) { r.Results = append(r.Results, TestResult{Test: "TestOther", Action: TestFail}) },
			"a result is repeated":     func(r *TestRun) { r.Results = append(r.Results, r.Results[0]) },
			"a result has no name":     func(r *TestRun) { r.Results = append(r.Results, TestResult{Action: TestPass}) },
			"a result has an odd verb": func(r *TestRun) { r.Results = append(r.Results, TestResult{Test: "TestOdd", Action: "ok"}) },
		} {
			r := goodTests(kind, e)
			mutate(&r)
			if err := Validate(kind, encode(t, kind, r), e); err == nil {
				t.Errorf("%s / %s: accepted", kind, name)
			}
		}
		// A capture that recorded no binary for the package cannot bind the run.
		noBinary := e
		noBinary.TestBinaries = nil
		if err := Validate(kind, encode(t, kind, goodTests(kind, e)), noBinary); err == nil {
			t.Errorf("%s: accepted although the capture recorded no test binary", kind)
		}
		_ = spec
	}
}

func TestLandlockArtifactRequiresPassingSecondThreadEnforcement(t *testing.T) {
	e := expect()
	for _, action := range []string{"absent", TestSkip} {
		t.Run(action, func(t *testing.T) {
			r := goodTests(KindLandlockConnectTests, e)
			r.Results = []TestResult{{Test: "TestLandlockConnectRightIsPort8080OnlyAndOnlyForNetSink", Action: TestPass}}
			if action != "absent" {
				r.Results = append(r.Results, TestResult{Test: "TestLandlockTSYNCReachesEnforcement", Action: action})
			}
			if err := Validate(KindLandlockConnectTests, encode(t, KindLandlockConnectTests, r), e); err == nil ||
				!strings.Contains(err.Error(), "TestLandlockTSYNCReachesEnforcement") {
				t.Fatalf("artifact with %s second-thread result was not refused: %v", action, err)
			}
		})
	}
}

func TestTestPackagesAreTheSpecs(t *testing.T) {
	got := TestPackages()
	if !slices.IsSorted(got) || len(got) != 2 {
		t.Fatalf("packages = %v", got)
	}
	for _, k := range []string{KindProcOracleTests, KindLandlockConnectTests} {
		if s, ok := SpecFor(k); !ok || len(s.Required) == 0 || !slices.Contains(got, s.Package) {
			t.Errorf("%s: %+v %v", k, s, ok)
		}
	}
	if _, ok := SpecFor(KindCapability); ok {
		t.Error("capability is not a test run")
	}
}

func TestEncodeIsDeterministicAndNewlineTerminated(t *testing.T) {
	a := encode(t, KindCapability, goodCapability())
	b := encode(t, KindCapability, goodCapability())
	if !bytes.Equal(a, b) || a[len(a)-1] != '\n' {
		t.Fatal("encoding is not deterministic")
	}
}

func goodChain() []CgroupLevel {
	var chain []CgroupLevel
	for _, p := range CapabilityChain {
		chain = append(chain, CgroupLevel{Path: p, Type: "domain", Controllers: []string{"cpuset", "cpu", "io", "memory", "pids"}, SubtreeControl: []string{"cpu", "memory", "pids"}})
	}
	return chain
}

func goodCellsProbe() CellsProbe {
	p := CellsProbe{UID: ServiceUID, GID: ServiceGID, ChildCreated: true, ChildRemoved: true}
	for _, name := range CellsProbeFiles {
		p.Files = append(p.Files, CellsProbeFile{Name: name, Exists: true, Writable: true})
	}
	return p
}

// The delegation chain from the hierarchy root to the cells cgroup (thesis E01) must enable cpu, memory
// and pids at every level, and the governed service identity must be able to make, use and remove a
// child cgroup of the cells cgroup.
func TestCapabilityDelegationChainAndCellsProbe(t *testing.T) {
	e := expect()
	without := func(list []string, drop string) []string {
		var out []string
		for _, s := range list {
			if s != drop {
				out = append(out, s)
			}
		}
		return out
	}
	for name, mutate := range map[string]func(*Capability){
		"no chain":                    func(c *Capability) { c.Cgroup.Chain = nil },
		"chain stops at the service":  func(c *Capability) { c.Cgroup.Chain = c.Cgroup.Chain[:3] },
		"chain skips system.slice":    func(c *Capability) { c.Cgroup.Chain = append(c.Cgroup.Chain[:1:1], c.Cgroup.Chain[2:]...) },
		"chain reordered":             func(c *Capability) { c.Cgroup.Chain[1], c.Cgroup.Chain[2] = c.Cgroup.Chain[2], c.Cgroup.Chain[1] },
		"chain of another cells path": func(c *Capability) { c.Cgroup.Chain[3].Path = "/system.slice/tbound-pi.service/other" },
		"root lacks pids":             func(c *Capability) { c.Cgroup.Chain[0].Controllers = without(c.Cgroup.Chain[0].Controllers, "pids") },
		"root does not enable memory": func(c *Capability) {
			c.Cgroup.Chain[0].SubtreeControl = without(c.Cgroup.Chain[0].SubtreeControl, "memory")
		},
		"slice does not enable cpu": func(c *Capability) {
			c.Cgroup.Chain[1].SubtreeControl = without(c.Cgroup.Chain[1].SubtreeControl, "cpu")
		},
		"service does not enable pids": func(c *Capability) {
			c.Cgroup.Chain[2].SubtreeControl = without(c.Cgroup.Chain[2].SubtreeControl, "pids")
		},
		"service lacks memory": func(c *Capability) { c.Cgroup.Chain[2].Controllers = without(c.Cgroup.Chain[2].Controllers, "memory") },
		"cells does not enable cpu": func(c *Capability) {
			c.Cgroup.Chain[3].SubtreeControl = without(c.Cgroup.Chain[3].SubtreeControl, "cpu")
		},
		"cells lacks cpu":          func(c *Capability) { c.Cgroup.Chain[3].Controllers = without(c.Cgroup.Chain[3].Controllers, "cpu") },
		"cells is threaded":        func(c *Capability) { c.Cgroup.Chain[3].Type = "threaded" },
		"cells type unknown":       func(c *Capability) { c.Cgroup.Chain[3].Type = "" },
		"no probe":                 func(c *Capability) { c.Cgroup.CellsProbe = CellsProbe{} },
		"probe as root":            func(c *Capability) { c.Cgroup.CellsProbe.UID = 0 },
		"probe as another user":    func(c *Capability) { c.Cgroup.CellsProbe.UID = 1000 },
		"probe with another group": func(c *Capability) { c.Cgroup.CellsProbe.GID = 0 },
		"child not created":        func(c *Capability) { c.Cgroup.CellsProbe.ChildCreated = false },
		"child not removed":        func(c *Capability) { c.Cgroup.CellsProbe.ChildRemoved = false },
		"no probe files":           func(c *Capability) { c.Cgroup.CellsProbe.Files = nil },
		"a probe file is missing":  func(c *Capability) { c.Cgroup.CellsProbe.Files = c.Cgroup.CellsProbe.Files[1:] },
		"an unknown probe file": func(c *Capability) {
			c.Cgroup.CellsProbe.Files = append(c.Cgroup.CellsProbe.Files, CellsProbeFile{Name: "io.max", Exists: true, Writable: true})
		},
		"probe files out of order": func(c *Capability) { f := c.Cgroup.CellsProbe.Files; f[0], f[1] = f[1], f[0] },
		"cgroup.kill absent": func(c *Capability) {
			c.Cgroup.CellsProbe.Files[0].Exists = false
			c.Cgroup.CellsProbe.Files[0].Writable = false
		},
		"cgroup.kill not writable":      func(c *Capability) { c.Cgroup.CellsProbe.Files[0].Writable = false },
		"cgroup.procs not writable":     func(c *Capability) { c.Cgroup.CellsProbe.Files[1].Writable = false },
		"pids.max not writable":         func(c *Capability) { c.Cgroup.CellsProbe.Files[4].Writable = false },
		"writable but absent is absurd": func(c *Capability) { c.Cgroup.CellsProbe.Files[2].Exists = false },
	} {
		c := goodCapability()
		mutate(&c)
		if err := Validate(KindCapability, encode(t, KindCapability, c), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func goodRouteDenied(kind string) RouteDenied {
	cell, _ := routeCellKindFor(kind)
	e := expect()
	if kind == KindRouteDirectRuntime {
		return goodDirectRuntime()
	}
	attempt := func(method, outcome string, exit int, seq uint64) RouteAttempt {
		return RouteAttempt{Method: method, Argv: []string{"/bin/bash", "-lc", "cat /home/node/.profile"}, ExitCode: exit, Outcome: outcome,
			ResultDigest: digestOf("result " + method), ReceiptDigest: digestOf("receipt " + method), ContainerID: "container-" + method, JournalSequence: seq}
	}
	return RouteDenied{RunID: "run-1", ProfileHash: probeProfileDigest, FrozenSourceDigest: e.FrozenSourceDigest, Target: "/home/node/.profile",
		Binding: goodBinding(e, probeProfileDigest, digestOf("template")),
		Cell:    RouteCell{Kind: cell, ImageDigest: digestOf("image"), TemplateDigest: digestOf("template"), LandlockRulesetDigest: digestOf("ruleset")},
		Journal: RouteJournal{Records: 9, HeadHash: strings.Repeat("a", 64)},
		Control: attempt(RouteMethodControl, RouteOutcomeRead, 0, 2),
		Attempts: []RouteAttempt{attempt(RouteMethodAbsolute, RouteOutcomeDenied, 1, 4), attempt(RouteMethodTraversal, RouteOutcomeDenied, 1, 6),
			attempt(RouteMethodSymlink, RouteOutcomeDenied, 1, 8)}}
}

func TestRouteArtifactRequiredValues(t *testing.T) {
	e := expect()
	// The attempt-level mutations are the command cell's (one command, one container, one receipt each); the Pi runtime cell's are in route_direct_test.go.
	for _, kind := range []string{KindRouteBashView} {
		if err := Validate(kind, encode(t, kind, goodRouteDenied(kind)), e); err != nil {
			t.Fatalf("%s: good artifact refused: %v", kind, err)
		}
		for name, mutate := range map[string]func(*RouteDenied){
			"no run id":                        func(r *RouteDenied) { r.RunID = "" },
			"another profile":                  func(r *RouteDenied) { r.ProfileHash = digestOf("other") },
			"another source":                   func(r *RouteDenied) { r.FrozenSourceDigest = digestOf("other") },
			"no binding":                       func(r *RouteDenied) { r.Binding = ProfileBinding{} },
			"another base template":            func(r *RouteDenied) { r.Binding.BaseTemplateDigest = digestOf("other") },
			"binding of another source":        func(r *RouteDenied) { r.Binding.SourceDigest = digestOf("other") },
			"another admin key":                func(r *RouteDenied) { r.Binding.AdminPubkeyDigest = digestOf("other") },
			"another runtime bundle":           func(r *RouteDenied) { r.Binding.RuntimeBundleDigest = digestOf("other") },
			"another runner":                   func(r *RouteDenied) { r.Binding.RunnerDigest = digestOf("other") },
			"another image than the cell's":    func(r *RouteDenied) { r.Binding.ImageDigest = digestOf("other") },
			"another template than the cell's": func(r *RouteDenied) { r.Binding.TemplateDigest = digestOf("other") },
			"relative target":                  func(r *RouteDenied) { r.Target = "home/node/.profile" },
			"unclean target":                   func(r *RouteDenied) { r.Target = "/home/node/../node/.profile" },
			"target inside /work":              func(r *RouteDenied) { r.Target = "/work/src/release.txt" },
			"target inside /etc":               func(r *RouteDenied) { r.Target = "/etc/hostname" },
			"target is a ruleset root":         func(r *RouteDenied) { r.Target = "/tmp" },
			"the other cell kind": func(r *RouteDenied) {
				if r.Cell.Kind == RouteCellCommand {
					r.Cell.Kind = RouteCellPiRuntime
				} else {
					r.Cell.Kind = RouteCellCommand
				}
			},
			"no image digest":      func(r *RouteDenied) { r.Cell.ImageDigest = "" },
			"no template digest":   func(r *RouteDenied) { r.Cell.TemplateDigest = "" },
			"no ruleset digest":    func(r *RouteDenied) { r.Cell.LandlockRulesetDigest = "" },
			"no journal":           func(r *RouteDenied) { r.Journal = RouteJournal{} },
			"bad journal hash":     func(r *RouteDenied) { r.Journal.HeadHash = "xyz" },
			"no control":           func(r *RouteDenied) { r.Control = RouteAttempt{} },
			"control denied":       func(r *RouteDenied) { r.Control.Outcome, r.Control.ExitCode = RouteOutcomeDenied, 1 },
			"control exit nonzero": func(r *RouteDenied) { r.Control.ExitCode = 1 },
			"control as an attempt": func(r *RouteDenied) {
				r.Attempts[0] = r.Control
			},
			"no attempts":           func(r *RouteDenied) { r.Attempts = nil },
			"a method is missing":   func(r *RouteDenied) { r.Attempts = r.Attempts[:2] },
			"methods out of order":  func(r *RouteDenied) { r.Attempts[0], r.Attempts[1] = r.Attempts[1], r.Attempts[0] },
			"an extra attempt":      func(r *RouteDenied) { r.Attempts = append(r.Attempts, r.Attempts[0]) },
			"a read, not a denial":  func(r *RouteDenied) { r.Attempts[1].Outcome = RouteOutcomeRead },
			"an unclassified read":  func(r *RouteDenied) { r.Attempts[1].Outcome = "other" },
			"exit zero for denial":  func(r *RouteDenied) { r.Attempts[2].ExitCode = 0 },
			"no command":            func(r *RouteDenied) { r.Attempts[0].Argv = nil },
			"command not on target": func(r *RouteDenied) { r.Attempts[0].Argv = []string{"/bin/bash", "-lc", "cat /home/node/.bashrc"} },
			"no result digest":      func(r *RouteDenied) { r.Attempts[0].ResultDigest = "" },
			"no receipt digest":     func(r *RouteDenied) { r.Attempts[0].ReceiptDigest = "" },
			"no container":          func(r *RouteDenied) { r.Attempts[0].ContainerID = "" },
			"no journal sequence":   func(r *RouteDenied) { r.Attempts[0].JournalSequence = 0 },
			"sequence past journal": func(r *RouteDenied) { r.Attempts[0].JournalSequence = 10 },
			"one container twice":   func(r *RouteDenied) { r.Attempts[1].ContainerID = r.Attempts[0].ContainerID },
			"one receipt twice":     func(r *RouteDenied) { r.Attempts[1].ReceiptDigest = r.Attempts[0].ReceiptDigest },
		} {
			r := goodRouteDenied(kind)
			mutate(&r)
			if err := Validate(kind, encode(t, kind, r), e); err == nil {
				t.Errorf("%s / %s: accepted", kind, name)
			}
		}
		// The run must pin what the run shares with the pilot, or the artifact is bound to nothing.
		for name, mutate := range map[string]func(*Expect){
			"no source pin": func(x *Expect) { x.FrozenSourceDigest = "" }, "no base template pin": func(x *Expect) { x.BaseTemplateDigest = "" },
			"no admin key pin": func(x *Expect) { x.AdminPubkeyDigest = "" }, "no image pin": func(x *Expect) { x.ImageDigest = "" },
			"no runtime bundle pin": func(x *Expect) { x.RuntimeBundleDigest = "" }, "no runner pin": func(x *Expect) { x.RunnerDigest = "" },
		} {
			x := e
			mutate(&x)
			if err := Validate(kind, encode(t, kind, goodRouteDenied(kind)), x); err == nil {
				t.Errorf("%s / %s: accepted although the run pins nothing", kind, name)
			}
		}
	}
	// One route's artifact is not the other's.
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, goodRouteDenied(KindRouteBashView)), e); err == nil {
		t.Error("a command-cell artifact was accepted as a Pi-runtime-cell one")
	}
}
