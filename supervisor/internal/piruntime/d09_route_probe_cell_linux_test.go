//go:build linux

package piruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/cellview"
)

// measuredRouteProbeTemplate is a measured production template (no probe): Landlock reads /usr and
// reads and writes /work.
func measuredRouteProbeTemplate(t *testing.T) D09CellTemplate {
	t.Helper()
	landlockDigest, err := DigestD09LandlockRules([]string{"/usr"}, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	return D09CellTemplate{
		Image: "localhost/tbound/pi-cell:latest", ExpectedImageDigest: fakeD09Digest,
		CosignPublicKeyPath: "/etc/tbound/cosign.pub", CosignPublicKeyDigest: DigestBytes([]byte("cosign-pub")),
		OCILayoutPath: "/etc/tbound/oci", SeccompProfilePath: "/etc/tbound/seccomp.json", SeccompProfileDigest: DigestBytes([]byte("{}")),
		LandlockABI: 8, LandlockRulesetDigest: landlockDigest, LandlockROPath: "/usr", LandlockRWPath: "/work",
		FrozenCapabilities: d09EmptyCapabilitySet, EntrypointPath: "/usr/local/bin/tbound-pi-entrypoint", EntrypointDigest: DigestBytes([]byte("entrypoint")),
		Runtime: "crun", CgroupsPath: "/sys/fs/cgroup/tbound", CPUs: "1", PidsMax: 256, MemoryMB: 1024,
		PodmanPath: "/usr/bin/podman", PodmanDigest: DigestBytes([]byte("podman")), CrunPath: "/usr/bin/crun", CrunDigest: DigestBytes([]byte("crun")),
		CosignPath: "/usr/bin/cosign", CosignDigest: DigestBytes([]byte("cosign")),
		CommandExecutable: "/bin/bash", CommandExecutableDigest: DigestBytes([]byte("bash")),
		CellUID: testD09CellID(os.Geteuid(), 731), CellGID: testD09CellID(os.Getegid(), 732),
		NamespaceProfile: "user,mount,pid,ipc,uts,network-none/v1", CgroupVersion: "v2", CgroupControllers: "cpu,memory,pids",
		XattrFilesystemMagic: 0xef53, XattrMountOptionsDigest: DigestBytes([]byte("ro,relatime")),
		XattrScanUID: os.Geteuid(), XattrScanGID: os.Getegid(),
		XattrVisibilityProfileDigest: DigestD09XattrVisibilityProfile(0xef53, DigestBytes([]byte("ro,relatime")), os.Geteuid(), os.Getegid(), D09XattrScanPrivilegeProfile),
		XattrScanPrivilegeProfile:    D09XattrScanPrivilegeProfile, XattrVisibilityAuthority: D09XattrVisibilityAuthority,
		ManagedLiveRootRegistrationDigest: DigestBytes([]byte("managed-live-root-registration")),
		UserNamespaceMappingProfile:       D09UserNamespaceMappingProfile,
		UserNamespaceMappingDigest: DigestD09UserNamespaceMapping(testD09CellID(os.Geteuid(), 731), testD09CellID(os.Getegid(), 732),
			os.Geteuid(), os.Getegid()),
	}
}

func probeTemplate(t *testing.T) D09CellTemplate {
	template := measuredRouteProbeTemplate(t)
	template.RouteProbe = &D09RouteProbe{Name: D09RouteProbeName, Target: "/home/node/.profile", Control: "/work/src/release.txt"}
	return template
}

func TestRouteProbeIsASignedTemplateMemberThatLeavesTheProductionTemplateAlone(t *testing.T) {
	production := measuredRouteProbeTemplate(t)
	if err := validateD09CellTemplate(production); err != nil {
		t.Fatalf("production template: %v", err)
	}
	productionDigest, err := production.Digest()
	if err != nil {
		t.Fatal(err)
	}
	probe := probeTemplate(t)
	if err := validateD09CellTemplate(probe); err != nil {
		t.Fatalf("probe template: %v", err)
	}
	probeDigest, err := probe.Digest()
	if err != nil || probeDigest == productionDigest {
		t.Fatalf("the probe template has digest %s, production %s: a probe cell must be its own template (%v)", probeDigest, productionDigest, err)
	}
	// Without the member the template digests exactly as before the member existed.
	if raw, err := jsonOf(production); err != nil || strings.Contains(raw, "route_probe") {
		t.Fatalf("a production template carries a route_probe member: %s %v", raw, err)
	}
	// Stripping the member from the probe template gives the production template.
	stripped := probe
	stripped.RouteProbe = nil
	if d, _ := stripped.Digest(); d != productionDigest {
		t.Errorf("probe template minus the probe has digest %s, production %s", d, productionDigest)
	}
	raw, err := jsonOf(probe)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeD09CellTemplate([]byte(raw))
	if err != nil {
		t.Fatalf("a probe template does not decode: %v", err)
	}
	if d, _ := decoded.Digest(); d != probeDigest {
		t.Errorf("decoded digest %s, want %s", d, probeDigest)
	}
	for name, mutate := range map[string]func(*D09RouteProbe){
		"another probe":         func(p *D09RouteProbe) { p.Name = "direct-runtime/v2" },
		"target in the ruleset": func(p *D09RouteProbe) { p.Target = "/usr/bin/env" },
		"control outside":       func(p *D09RouteProbe) { p.Control = "/home/node/.bashrc" },
	} {
		bad := probeTemplate(t)
		p := *bad.RouteProbe
		mutate(&p)
		bad.RouteProbe = &p
		if err := validateD09CellTemplate(bad); !errors.Is(err, ErrD09TemplateInvalid) {
			t.Errorf("%s: template accepted (%v)", name, err)
		}
	}
}

func TestWorkerLaunchSelectsTheProbeOnlyFromTheSignedTemplate(t *testing.T) {
	piArgs := []string{"--mode", "rpc"}
	production := measuredRouteProbeTemplate(t).Config()
	args, env := d09WorkerProbeLaunch(production, piArgs)
	if !slices.Equal(args, piArgs) || len(env) != 0 {
		t.Fatalf("a production template launches args %q env %q, want the Pi arguments and no probe variable", args, env)
	}
	template := probeTemplate(t)
	config := template.Config()
	args, env = d09WorkerProbeLaunch(config, piArgs)
	digest, _ := template.Digest()
	if !slices.Equal(args, []string{D09RouteProbeArg}) {
		t.Errorf("probe args = %q, want only the probe argument (Pi is never started)", args)
	}
	want := []string{
		"TBOUND_TEMPLATE_DIGEST=" + digest,
		"TBOUND_ROUTE_PROBE_TARGET=/home/node/.profile",
		"TBOUND_ROUTE_PROBE_CONTROL=/work/src/release.txt",
		"TBOUND_ROUTE_PROBE_TEMPLATE_FLAG=" + template.RouteProbe.TemplateFlag(digest),
	}
	if !slices.Equal(env, want) {
		t.Errorf("probe env\n got %q\nwant %q", env, want)
	}
	// A launch request cannot select the probe: it has no member that names one.
	requestType := reflect.TypeOf(ExecutableLaunchRequest{})
	for i := 0; i < requestType.NumField(); i++ {
		if strings.Contains(strings.ToLower(requestType.Field(i).Name), "probe") {
			t.Errorf("ExecutableLaunchRequest.%s could select the probe", requestType.Field(i).Name)
		}
	}
}

func TestProbeProofMustMatchTheTemplateThatLaunchedIt(t *testing.T) {
	probeConfig := probeTemplate(t).Config()
	productionConfig := measuredRouteProbeTemplate(t).Config()
	result := RunD09RouteProbe(&recordingProbeFS{}, *probeTemplate(t).RouteProbe)
	with := func(mutate func(*D09RouteProbeResult)) D09TCBProof {
		r := result
		r.Attempts = slices.Clone(result.Attempts)
		mutate(&r)
		return D09TCBProof{RouteProbe: &r}
	}
	if err := validateD09RouteProbeProof(with(func(*D09RouteProbeResult) {}), probeConfig); err != nil {
		t.Fatalf("a matching probe proof is refused: %v", err)
	}
	if err := validateD09RouteProbeProof(D09TCBProof{}, probeConfig); err == nil {
		t.Error("a probe launch whose proof has no probe result was accepted")
	}
	if err := validateD09RouteProbeProof(with(func(*D09RouteProbeResult) {}), productionConfig); err == nil {
		t.Error("a production launch whose proof carries a probe result was accepted")
	}
	if err := validateD09RouteProbeProof(D09TCBProof{}, productionConfig); err != nil {
		t.Errorf("a production proof without a probe result is refused: %v", err)
	}
	for name, mutate := range map[string]func(*D09RouteProbeResult){
		"another target": func(r *D09RouteProbeResult) { r.Target = "/home/node/.bashrc" },
		"a missing step": func(r *D09RouteProbeResult) { r.Attempts = r.Attempts[:8] },
	} {
		if err := validateD09RouteProbeProof(with(mutate), probeConfig); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// ---- the real read-only surface ----

func treeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		line := p + " " + info.Mode().String() + " " + info.ModTime().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			line += " " + string(data)
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func TestOSRouteProbeFSReadsAndChangesNothing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "outside")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "secret")
	control := filepath.Join(root, "control")
	for _, f := range []string{target, control} {
		if err := os.WriteFile(f, []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := treeSnapshot(t, root)
	fsys := OSRouteProbeFS{}
	for _, p := range []string{target, control} {
		if err := fsys.OpenRead(p); err != nil {
			t.Errorf("OpenRead %s: %v", p, err)
		}
		if err := fsys.Stat(p); err != nil {
			t.Errorf("Stat %s: %v", p, err)
		}
	}
	if err := fsys.List(dir); err != nil {
		t.Errorf("List: %v", err)
	}
	if err := fsys.OpenRead(filepath.Join(dir, "absent")); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("an absent file gives %v, want ENOENT", err)
	}
	if err := fsys.List(target); !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("listing a file gives %v, want ENOTDIR", err)
	}
	if after := treeSnapshot(t, root); !slices.Equal(before, after) {
		t.Fatalf("the probe changed the tree:\nbefore %q\nafter  %q", before, after)
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(target, 0); err != nil {
			t.Fatal(err)
		}
		if err := fsys.OpenRead(target); !errors.Is(err, syscall.EACCES) {
			t.Errorf("an unreadable file gives %v, want EACCES", err)
		}
	}
}

// The implementation of the probe is held to reads: none of the calls that create, change, remove, link,
// rename or write may appear in it.
func TestRouteProbeSourceHoldsNoWritingCall(t *testing.T) {
	forbidden := []string{"O_WRONLY", "O_RDWR", "O_CREAT", "O_TRUNC", "O_APPEND", "O_EXCL", "syscall.Write", "Unlink", "Rmdir", "Mkdir", "Symlink", "Link(",
		"Rename", "Chmod", "Chown", "Truncate", "Mknod", "Setxattr", "Removexattr", "Utimes", "Mount", "os.Create", "os.OpenFile", "os.WriteFile", "os.Remove", "os.Mkdir", "ioutil"}
	for _, name := range []string{"d09_route_probe.go", "d09_route_probe_linux.go", "../cellview/resolve.go", "../cellview/view.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range forbidden {
			if strings.Contains(string(src), token) {
				t.Errorf("%s contains %q: the route probe performs reads only", name, token)
			}
		}
	}
}

// The real surface measures a real path: a symbolic link in the middle, the owner and mode of each
// component, the ACL attribute reads, and the identity of the process. Nothing in the tree changes.
func TestOSRouteProbeFSMeasuresARealPathAndChangesNothing(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(real, "secret")
	if err := os.WriteFile(file, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, root)
	target := filepath.Join(root, "alias", "secret")
	v := cellview.Measure(OSRouteProbeFS{}, target)
	if v.Err != "" {
		t.Fatalf("the walk of %s: %s", target, v.Err)
	}
	kinds := map[string]string{}
	for _, f := range v.Facts {
		kinds[f.Path] = f.Kind
		if f.Kind == cellview.KindLink && f.Link != "real" {
			t.Errorf("link text %q", f.Link)
		}
	}
	if kinds[filepath.Join(root, "alias")] != cellview.KindLink || kinds[file] != cellview.KindFile || kinds[real] != cellview.KindDir || kinds["/"] != cellview.KindDir {
		t.Errorf("kinds = %v", kinds)
	}
	if v.EUID != uint32(os.Geteuid()) || v.EGID != uint32(os.Getegid()) {
		t.Errorf("identity %d:%d", v.EUID, v.EGID)
	}
	for path, got := range map[string]uint32{"/proc/sys/fs/overflowuid": v.OverflowUID, "/proc/sys/fs/overflowgid": v.OverflowGID} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32); err != nil || uint32(want) != got || !v.OverflowRead {
			t.Errorf("%s: recorded %d (read %v), the kernel says %q", path, got, v.OverflowRead, raw)
		}
	}
	for _, f := range v.Facts {
		if f.Path == file && (f.Mode != 0o644 || f.Size != 5 || f.UID != uint32(os.Geteuid())) {
			t.Errorf("fact of the file = %+v", f)
		}
		if f.ACL == cellview.ACLUnreadable && f.Kind != cellview.KindLink {
			t.Logf("ACL of %s is unreadable on this file system", f.Path)
		}
	}
	if _, err := (OSRouteProbeFS{}).Lstat(filepath.Join(root, "absent")); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("an absent component gives %v, want ENOENT", err)
	}
	if after := treeSnapshot(t, root); !slices.Equal(before, after) {
		t.Fatalf("the measurement changed the tree:\nbefore %q\nafter  %q", before, after)
	}
}
