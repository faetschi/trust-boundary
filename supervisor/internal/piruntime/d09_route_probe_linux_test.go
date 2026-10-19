//go:build linux

package piruntime

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// recordingProbeFS is a fake root for the route probe: it implements exactly the read operations of
// D09RouteProbeFS (the interface has no write operation, so the probe cannot make one), records every call
// and answers from a table.
type recordingProbeFS struct {
	calls  []string
	answer map[string]error
}

func (f *recordingProbeFS) do(op, path string) error {
	f.calls = append(f.calls, op+" "+path)
	return f.answer[op+" "+path]
}
func (f *recordingProbeFS) OpenRead(path string) error { return f.do("open", path) }
func (f *recordingProbeFS) Stat(path string) error     { return f.do("stat", path) }
func (f *recordingProbeFS) List(path string) error     { return f.do("list", path) }

var testRouteProbe = D09RouteProbe{Name: D09RouteProbeName, Target: "/home/node/.profile", Control: "/work/src/release.txt"}

func TestRouteProbeMakesOnlyTheReadAttemptsOfTheThreeRoutesAndTheControl(t *testing.T) {
	fsys := &recordingProbeFS{answer: map[string]error{}}
	for _, p := range []string{"/home/node/.profile", "../../../../home/node/.profile", "/proc/self/root/home/node/.profile",
		"/home/node", "../../../../home/node", "/proc/self/root/home/node"} {
		fsys.answer["open "+p] = syscall.EACCES
		fsys.answer["list "+p] = syscall.EACCES
	}
	result := RunD09RouteProbe(fsys, testRouteProbe)
	want := []string{
		"open /work/src/release.txt",
		"open /home/node/.profile", "stat /home/node/.profile", "list /home/node",
		"open ../../../../home/node/.profile", "stat ../../../../home/node/.profile", "list ../../../../home/node",
		"open /proc/self/root/home/node/.profile", "stat /proc/self/root/home/node/.profile", "list /proc/self/root/home/node",
	}
	if !slices.Equal(fsys.calls, want) {
		t.Fatalf("probe calls\n got %q\nwant %q", fsys.calls, want)
	}
	if result.Name != D09RouteProbeName || result.Target != testRouteProbe.Target || result.Control != testRouteProbe.Control {
		t.Fatalf("result does not state the probe it ran: %+v", result)
	}
	if result.ControlRead.Errno != 0 || len(result.Attempts) != 9 {
		t.Fatalf("control errno %d, %d attempts", result.ControlRead.Errno, len(result.Attempts))
	}
	for i, a := range result.Attempts {
		wantErrno := int(syscall.EACCES)
		if a.Op == "stat" {
			wantErrno = 0
		}
		if a.Errno != wantErrno {
			t.Errorf("attempt %d %s %s: errno %d, want %d", i, a.Method, a.Op, a.Errno, wantErrno)
		}
	}
	if err := result.CheckShape(testRouteProbe); err != nil {
		t.Fatalf("a complete result is refused: %v", err)
	}
}

func TestRouteProbeRecordsASucceededReadAndAnUnknownFailure(t *testing.T) {
	fsys := &recordingProbeFS{answer: map[string]error{"open /home/node/.profile": nil, "list /home/node": errors.New("not an errno")}}
	result := RunD09RouteProbe(fsys, testRouteProbe)
	for _, a := range result.Attempts {
		if a.Method == "absolute" && a.Op == "open" && a.Errno != 0 {
			t.Errorf("a successful open is recorded as errno %d", a.Errno)
		}
		if a.Method == "absolute" && a.Op == "list" && a.Errno != -1 {
			t.Errorf("a failure that is no errno is recorded as %d, want -1", a.Errno)
		}
	}
}

func TestRouteProbeResultShapeIsExact(t *testing.T) {
	good := RunD09RouteProbe(&recordingProbeFS{}, testRouteProbe)
	cases := map[string]func(*D09RouteProbeResult){
		"another target":     func(r *D09RouteProbeResult) { r.Target = "/etc/passwd" },
		"another control":    func(r *D09RouteProbeResult) { r.Control = "/work/other" },
		"another name":       func(r *D09RouteProbeResult) { r.Name = "x" },
		"an attempt missing": func(r *D09RouteProbeResult) { r.Attempts = r.Attempts[1:] },
		"an attempt added":   func(r *D09RouteProbeResult) { r.Attempts = append(r.Attempts, r.Attempts[0]) },
		"attempts reordered": func(r *D09RouteProbeResult) { r.Attempts[0], r.Attempts[1] = r.Attempts[1], r.Attempts[0] },
		"another method":     func(r *D09RouteProbeResult) { r.Attempts[0].Method = "control" },
		"no control":         func(r *D09RouteProbeResult) { r.ControlRead = D09RouteProbeAttempt{} },
	}
	for name, mutate := range cases {
		r := good
		r.Attempts = slices.Clone(good.Attempts)
		mutate(&r)
		if err := r.CheckShape(testRouteProbe); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRouteProbeConfigurationIsValidatedAgainstTheRuleset(t *testing.T) {
	ro, rw := []string{"/usr", "/lib", "/etc"}, []string{"/work", "/tmp"}
	if err := testRouteProbe.Validate(ro, rw); err != nil {
		t.Fatalf("the probe is refused: %v", err)
	}
	bad := map[string]D09RouteProbe{
		"unknown name":                {Name: "direct-runtime/v2", Target: testRouteProbe.Target, Control: testRouteProbe.Control},
		"target in the ruleset":       {Name: D09RouteProbeName, Target: "/etc/passwd", Control: testRouteProbe.Control},
		"target is a ruleset root":    {Name: D09RouteProbeName, Target: "/work", Control: testRouteProbe.Control},
		"relative target":             {Name: D09RouteProbeName, Target: "home/node/.profile", Control: testRouteProbe.Control},
		"unclean target":              {Name: D09RouteProbeName, Target: "/home/node/../node/.profile", Control: testRouteProbe.Control},
		"target is the root":          {Name: D09RouteProbeName, Target: "/", Control: testRouteProbe.Control},
		"control outside the ruleset": {Name: D09RouteProbeName, Target: testRouteProbe.Target, Control: "/home/node/.bashrc"},
		"control relative":            {Name: D09RouteProbeName, Target: testRouteProbe.Target, Control: "src/release.txt"},
		"newline in target":           {Name: D09RouteProbeName, Target: "/home/node/a\nb", Control: testRouteProbe.Control},
	}
	for name, p := range bad {
		if err := p.Validate(ro, rw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRouteProbeTemplateFlagBindsTheProbeToTheTemplate(t *testing.T) {
	flag := testRouteProbe.TemplateFlag(DigestBytes([]byte("template")))
	if !validDigest(flag) {
		t.Fatalf("flag %q is not a digest", flag)
	}
	for name, other := range map[string]string{
		"another template": testRouteProbe.TemplateFlag(DigestBytes([]byte("other"))),
		"another target":   D09RouteProbe{Name: D09RouteProbeName, Target: "/home/node/.bashrc", Control: testRouteProbe.Control}.TemplateFlag(DigestBytes([]byte("template"))),
		"another control":  D09RouteProbe{Name: D09RouteProbeName, Target: testRouteProbe.Target, Control: "/work/x"}.TemplateFlag(DigestBytes([]byte("template"))),
	} {
		if other == flag {
			t.Errorf("%s gives the same flag", name)
		}
	}
	if !strings.HasPrefix(D09RouteProbeArg, "--route-probe=") {
		t.Errorf("probe argument %q", D09RouteProbeArg)
	}
}

// The entrypoint writes the whole proof on one bounded line (readD09TCBProof refuses 4096 bytes). The
// probe result rides in it, so a full proof with a full result must still fit.
func TestProofWithProbeResultFitsTheProofLine(t *testing.T) {
	long := strings.Repeat("a", 64)
	proof := D09TCBProof{Schema: "tbound-pi-entrypoint/v1", UID: 1000, GID: 1000, CapEff: "0000000000000000", NoNewPrivs: true, Seccomp: 2,
		LandlockABI: 8, LandlockRulesetDigest: "sha256:" + long, LandlockTSYNC: true, CgroupV2: true,
		UIDMap: "         0     100000      65536", GIDMap: "         0     100000      65536", UserNamespaceMapDigest: "sha256:" + long,
		ContainerName: "tbound-d09-cell-" + long[:32], ContainerLeaderPID: 1, ImageRef: "localhost/tbound/pi-cell@sha256:" + long,
		ImageDigest: "sha256:" + long, TemplateDigest: "sha256:" + long, ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: 1234, RuntimeMountID: 1235, WorkerDevice: 4294967295, WorkerInode: 18446744073709551615,
		RuntimeDevice: 4294967295, RuntimeInode: 18446744073709551615, UserNSInode: 4026532999, MountNSInode: 4026532999,
		PIDNSInode: 4026532999, IPCNSInode: 4026532999, UTSNSInode: 4026532999, NetworkNSInode: 4026532999, NoExternalRoutes: true}
	result := RunD09RouteProbe(&recordingProbeFS{}, D09RouteProbe{Name: D09RouteProbeName, Target: "/" + strings.Repeat("d", 100), Control: "/work/" + strings.Repeat("c", 100)})
	proof.RouteProbe = &result
	line, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if len(line) > 4095 {
		t.Fatalf("a full proof with a probe result is %d bytes; the proof line holds 4095", len(line))
	}
}
