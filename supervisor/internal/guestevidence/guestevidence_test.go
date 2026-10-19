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
	return Expect{Commit: testCommit, BinarySHA256: testBinary, ProfileHash: digestOf("profile"), FrozenSourceDigest: digestOf("source"), TestBinaries: tb}
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
			ServiceCgroup: ServiceCgroup, ServiceCgroupType: "domain", KillInterface: true},
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
	return map[string]any{"schema_version": receiptSchema, "profile_hash": e.ProfileHash, "frozen_source_digest": e.FrozenSourceDigest,
		"provider_exchange": true, "claim_bearing": false, "task_admitted": true, "turn_completed": true, "success": true,
		"tool_sequence": []string{"read", "edit", "bash", "read"}, "provider_turns": 5,
		"source_generation": "g0", "final_generation": "g2", "generation_count": 3,
		"settlement": "STOPPED", "exit_observed": true, "exit_code": 0, "descendants_settled": true, "some_future_member": 1}
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
	r := TestRun{Package: spec.Package, TestBinarySHA256: e.TestBinaries[spec.Package], Args: slices.Clone(TestRunArgs), KernelRelease: "6.8.0-synthetic",
		OutputSHA256: digestOf("output")}
	for _, name := range spec.Required {
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
		{KindProviderExchange, NewProviderExchange(goodResponse(t, e, nil))},
		{KindProcOracleTests, goodTests(KindProcOracleTests, e)},
		{KindLandlockConnectTests, goodTests(KindLandlockConnectTests, e)},
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
		"nonzero exit":         func(_, f map[string]any) { f["exit_code"] = 1 },
		"descendants left":     func(_, f map[string]any) { f["descendants_settled"] = false },
	} {
		p := NewProviderExchange(goodResponse(t, e, mutate))
		if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, p), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The wrapper must show the response it names.
	p := NewProviderExchange(goodResponse(t, e, nil))
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
		if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, NewProviderExchange(raw)), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The run must pin the profile and the source, or the receipt is not bound at all.
	noPins := e
	noPins.ProfileHash, noPins.FrozenSourceDigest = "", ""
	if err := Validate(KindProviderExchange, encode(t, KindProviderExchange, NewProviderExchange(goodResponse(t, noPins, nil))), noPins); err == nil {
		t.Error("a receipt was accepted although the run pins nothing to bind it to")
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
