//go:build linux

package piruntime

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/cellview"
)

// recordingProbeFS is a fake root for the route probe: it implements exactly the read operations of
// D09RouteProbeFS (the interface has no write operation, so the probe cannot make one), records every call
// and answers from a table. The measurement of the path (Lstat, ACL, Identity, LSM) answers from facts, and
// every call is also in order, so a test can see that the measurement comes before the attempts.
type recordingProbeFS struct {
	calls  []string
	order  []string
	answer map[string]error
	facts  map[string]cellview.Fact
}

func (f *recordingProbeFS) do(op, path string) error {
	f.calls = append(f.calls, op+" "+path)
	f.order = append(f.order, op+" "+path)
	return f.answer[op+" "+path]
}
func (f *recordingProbeFS) OpenRead(path string) error { return f.do("open", path) }
func (f *recordingProbeFS) Stat(path string) error     { return f.do("stat", path) }
func (f *recordingProbeFS) List(path string) error     { return f.do("list", path) }
func (f *recordingProbeFS) Lstat(path string) (cellview.Fact, error) {
	f.order = append(f.order, "lstat "+path)
	fact, ok := f.facts[path]
	if !ok {
		return cellview.Fact{}, syscall.ENOENT
	}
	return fact, nil
}
func (f *recordingProbeFS) ACL(path string) string {
	f.order = append(f.order, "acl "+path)
	return ""
}
func (f *recordingProbeFS) Identity() (uint32, uint32, []uint32) {
	f.order = append(f.order, "identity")
	return 100731, 100732, nil
}
func (f *recordingProbeFS) LSM() string {
	f.order = append(f.order, "lsm")
	return "unconfined"
}
func (f *recordingProbeFS) Overflow() (uint32, uint32, bool) {
	f.order = append(f.order, "overflow")
	return 65534, 65534, true
}

// homeFacts is a view of /home/node/.profile that file permissions alone would not refuse.
func homeFacts() map[string]cellview.Fact {
	d := func(p string, uid uint32) cellview.Fact {
		return cellview.Fact{Path: p, Kind: cellview.KindDir, Mode: 0o755, UID: uid, GID: uid}
	}
	return map[string]cellview.Fact{"/": d("/", 0), "/home": d("/home", 0), "/home/node": d("/home/node", 1000),
		"/home/node/.profile": {Path: "/home/node/.profile", Kind: cellview.KindFile, Mode: 0o644, UID: 1000, GID: 1000, Size: 807}}
}

var testRouteProbe = D09RouteProbe{Name: D09RouteProbeName, Target: "/home/node/.profile", Control: "/work/src/release.txt"}

func TestRouteProbeMakesOnlyTheReadAttemptsOfTheThreeRoutesAndTheControl(t *testing.T) {
	fsys := &recordingProbeFS{answer: map[string]error{}, facts: homeFacts()}
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

// The path is measured from inside the cell before any attempt, component by component, with lstat and
// the attribute reads only, and the identity of the probe and its security module context are recorded.
func TestRouteProbeMeasuresThePathBeforeTheAttempts(t *testing.T) {
	fsys := &recordingProbeFS{answer: map[string]error{}, facts: homeFacts()}
	result := RunD09RouteProbe(fsys, testRouteProbe)
	firstAttempt := slices.IndexFunc(fsys.order, func(c string) bool { return strings.HasPrefix(c, "open ") })
	if firstAttempt < 0 {
		t.Fatalf("no attempt in %q", fsys.order)
	}
	var measured []string
	for i, c := range fsys.order {
		if !strings.HasPrefix(c, "open ") && !strings.HasPrefix(c, "stat ") && !strings.HasPrefix(c, "list ") {
			measured = append(measured, c)
			if i > firstAttempt {
				t.Errorf("%q is a measurement after the first attempt", c)
			}
		}
	}
	for _, want := range []string{"lstat /", "lstat /home", "lstat /home/node", "lstat /home/node/.profile", "acl /home/node/.profile", "identity", "lsm", "overflow"} {
		if !slices.Contains(measured, want) {
			t.Errorf("no %q in the measurement %q", want, measured)
		}
	}
	v := result.View
	if v.Err != "" || len(v.Facts) != 4 || v.EUID != 100731 || v.EGID != 100732 || v.LSM != "unconfined" {
		t.Fatalf("view = %+v", v)
	}
	if got := v.Problems(testRouteProbe.Target, 100731, 100732, []string{"/work", "/tmp"}); len(got) != 0 {
		t.Errorf("the view of a readable file is refused: %v", got)
	}
	// A path the cell cannot examine is recorded as such and not guessed.
	gone := RunD09RouteProbe(&recordingProbeFS{}, testRouteProbe)
	if gone.View.Err == "" {
		t.Error("a path that cannot be examined has no error in the view")
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
		"no measurement":     func(r *D09RouteProbeResult) { r.View = cellview.View{} },
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
	// A target of about 100 characters in five components, every one measured with owner and mode.
	parts := []string{strings.Repeat("a", 19), strings.Repeat("b", 19), strings.Repeat("c", 19), strings.Repeat("d", 19), strings.Repeat("e", 19)}
	target := "/" + strings.Join(parts, "/")
	fsys := &recordingProbeFS{facts: map[string]cellview.Fact{"/": {Path: "/", Kind: cellview.KindDir, Mode: 0o755}}}
	for i := range parts {
		p := "/" + strings.Join(parts[:i+1], "/")
		fsys.facts[p] = cellview.Fact{Path: p, Kind: cellview.KindDir, Mode: 0o755, UID: 4294967295, GID: 4294967295}
	}
	fsys.facts[target] = cellview.Fact{Path: target, Kind: cellview.KindFile, Mode: 0o644, UID: 4294967295, GID: 4294967295, Size: 9007199254740991}
	result := RunD09RouteProbe(fsys, D09RouteProbe{Name: D09RouteProbeName, Target: target, Control: "/work/" + strings.Repeat("c", 100)})
	result.View.Groups = []uint32{4294967295, 4294967294, 4294967293, 4294967292}
	result.View.LSM = strings.Repeat("l", 128)
	proof.RouteProbe = &result
	line, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if len(line) > 4095 {
		t.Fatalf("a full proof with a probe result and a measured view is %d bytes; the proof line holds 4095", len(line))
	}
}
