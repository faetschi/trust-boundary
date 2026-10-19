//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
)

// guestEvidenceUsage is the part of the usage text for `tbound-profile guest-evidence`.
const guestEvidenceUsage = `  tbound-profile guest-evidence capability        --commit <40-hex> --seccomp-profile <abs file> --out <new file> [--service-cgroup </system.slice/..service>]
  tbound-profile guest-evidence netsink           --commit <40-hex> --out <new file>
  tbound-profile guest-evidence provider-exchange --commit <40-hex> --response <file> --profile-hash <sha256> --frozen-source-digest <sha256> --out <new file>
  tbound-profile guest-evidence route-denied      --commit <40-hex> --route bash-view --run-id <id> --response <file> --target <abs path> --control-path <rel path> --control-file <file> --profile-hash <sha256> --frozen-source-digest <sha256> [--audit-root <dir>] --out <new file>
  tbound-profile guest-evidence route-denied      --commit <40-hex> --route direct-runtime --run-id <id> --target <abs path> --control-path <rel path> --run-config <run.json> --probe-template <file> --production-template <file> [--host-profile <file> --host-profile-signature <file> --host-profile-key <file>] [--oci-layout <dir>] [--audit-root <dir>] --out <new file>
  tbound-profile guest-evidence tests             --commit <40-hex> --kind guest-tests-procoracle|guest-tests-landlock-connect --test-binary <abs file> --out <new file>
`

// The producers of the guest artifacts of internal/guestevidence. Each one observes the machine it runs
// on, builds the artifact in the envelope, and writes it only if the artifact passes the validator
// `tbound-run evq` applies. A guest that fails a requirement therefore leaves no artifact; the refusal
// names what failed.

const (
	netSinkProbeVerb  = "netsink-probe"
	nftBinary         = "/usr/sbin/nft"
	maxProbeOutput    = 64 << 10
	maxTestRunOutput  = 64 << 20
	maxGuestInputSize = 1 << 20
)

func runGuestEvidence(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("guest-evidence needs a kind\n" + guestEvidenceUsage)
	}
	kind, rest := args[0], args[1:]
	if kind == netSinkProbeVerb {
		return runNetSinkProbe(stdout)
	}
	flags := flag.NewFlagSet("tbound-profile guest-evidence "+kind, flag.ContinueOnError)
	flags.SetOutput(stderr)
	commit := flags.String("commit", "", "the freeze commit this binary was built from, 40 lowercase hex digits")
	out := flags.String("out", "", "the artifact file to create")
	seccomp := flags.String("seccomp-profile", "", "capability: the installed static seccomp profile file named by the cell template")
	serviceCgroup := flags.String("service-cgroup", guestevidence.ServiceCgroup, "capability: cgroup of the governed service")
	response := flags.String("response", "", "provider-exchange: the response line the governed client printed")
	profileHash := flags.String("profile-hash", "", "provider-exchange: run.json pins.profile_hash")
	frozenSource := flags.String("frozen-source-digest", "", "provider-exchange: run.json pins.frozen_source_digest")
	testKind := flags.String("kind", "", "tests: the artifact kind")
	testBinary := flags.String("test-binary", "", "tests: the test binary built by tbound-run evq-capture")
	route := flags.String("route", "", "route-denied: bash-view or direct-runtime")
	runID := flags.String("run-id", "", "route-denied: receipt.run_id of the route-probe run")
	auditRoot := flags.String("audit-root", "/var/lib/tbound/audit", "route-denied: directory of the service audit journals")
	target := flags.String("target", "", "route-denied: the registered outside-workspace file the probe commands request")
	controlPath := flags.String("control-path", "", "route-denied: the workspace file the control command reads, relative to /work")
	controlFile := flags.String("control-file", "", "route-denied: that workspace file as it is on the guest, for its bytes")
	probeTemplate := flags.String("probe-template", "", "route-denied direct-runtime: the route-probe cell template the probe cell was launched from")
	productionTemplate := flags.String("production-template", "", "route-denied direct-runtime: the production cell template the probe template derives from")
	runConfig := flags.String("run-config", "", "route-denied direct-runtime: run.json; its freeze_commit (must be --commit) and pins (profile_hash, containment_profile_digest, frozen_source_digest) are the expectations the journal and the probe profile are held to")
	hostProfile := flags.String("host-profile", "/etc/tbound/pi-host-profile.json", "route-denied direct-runtime: the signed host profile the probe run used (its digest must be the one the session journal records)")
	hostProfileSig := flags.String("host-profile-signature", "/etc/tbound/pi-host-profile.ed25519", "route-denied direct-runtime: the Ed25519 signature of that profile")
	hostProfileKey := flags.String("host-profile-key", "/etc/tbound/pi-host-profile.pub", "route-denied direct-runtime: the public key that verifies it")
	ociLayout := flags.String("oci-layout", "", "route-denied direct-runtime: the local OCI layout that holds the probed image (default: oci_layout_path of the probe template)")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !guestevidence.IsCommit(*commit) || *out == "" || !filepath.IsAbs(*out) {
		return errors.New("--commit (40 lowercase hex digits) and an absolute --out are required")
	}
	if _, err := os.Lstat(*out); err == nil {
		return fmt.Errorf("%s exists: remove it deliberately to produce the artifact again", *out)
	}
	self, err := selfDigest()
	if err != nil {
		return err
	}

	var artifactKind string
	var data any
	expect := guestevidence.Expect{Commit: *commit, BinarySHA256: self, ProfileHash: *profileHash, FrozenSourceDigest: *frozenSource}
	switch kind {
	case "capability":
		artifactKind = guestevidence.KindCapability
		data, err = probeCapability(capabilityEnv{mountinfo: "/proc/self/mountinfo", serviceCgroup: *serviceCgroup, seccompProfile: *seccomp})
	case "netsink":
		artifactKind = guestevidence.KindNetSink
		data, err = probeNetSink(context.Background(), productionNetSinkEnv(self))
	case "provider-exchange":
		artifactKind = guestevidence.KindProviderExchange
		data, err = providerExchangeFrom(*response)
	case "route-denied":
		opts := routeProbeOptions{route: *route, runID: *runID, auditRoot: *auditRoot, response: *response,
			target: *target, controlPath: *controlPath, controlFile: *controlFile,
			profileHash: *profileHash, frozenSource: *frozenSource, probeTemplate: *probeTemplate, productionTemplate: *productionTemplate,
			hostProfile: *hostProfile, hostProfileSig: *hostProfileSig, hostProfileKey: *hostProfileKey, ociLayout: *ociLayout}
		if *route == "direct-runtime" {
			// The expectations come from run.json, which also names the freeze commit, not from the caller.
			binding, pinErr := directRuntimePins(*runConfig, *commit, *profileHash, *frozenSource)
			if pinErr != nil {
				return pinErr
			}
			opts.profileHash, opts.frozenSource, opts.containmentDigest = binding.ProfileHash, binding.FrozenSourceDigest, binding.ContainmentProfileDigest
			expect.ProfileHash, expect.FrozenSourceDigest, expect.ContainmentProfileDigest = binding.ProfileHash, binding.FrozenSourceDigest, binding.ContainmentProfileDigest
		}
		data, artifactKind, err = probeRouteDenied(opts, productionRouteProbeEnv())
	case "tests":
		artifactKind = *testKind
		var run guestevidence.TestRun
		run, err = runGuestTests(context.Background(), *testKind, *testBinary)
		expect.TestBinaries = map[string]string{run.Package: run.TestBinarySHA256}
		data = run
	default:
		return fmt.Errorf("unknown guest-evidence kind %q\n%s", kind, guestEvidenceUsage)
	}
	if err != nil {
		return err
	}
	encoded, err := guestevidence.Encode(artifactKind, *commit, self, data)
	if err != nil {
		return err
	}
	if err := guestevidence.Validate(artifactKind, encoded, expect); err != nil {
		return fmt.Errorf("the observed facts do not meet the requirement, nothing is written: %w", err)
	}
	if err := writeNewFile(*out, encoded, false); err != nil {
		return err
	}
	sum := sha256.Sum256(encoded)
	_, err = fmt.Fprintf(stdout, "wrote %s artifact %s\nsha256 sha256:%s\nbinary_sha256 %s\ncommit %s\n", artifactKind, *out, hex.EncodeToString(sum[:]), self, *commit)
	return err
}

// selfDigest is the sha256 of the running executable.
func selfDigest() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func fileDigest(path string, normalizeLF bool) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("%s is empty", path)
	}
	if normalizeLF {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ---- capability ----

type capabilityEnv struct {
	mountinfo      string
	serviceCgroup  string
	seccompProfile string
	// cellsProbe probes a child of the cells cgroup at the given directory as the service identity;
	// nil is the production probe (probeCellsAsService). Tests replace it.
	cellsProbe func(cellsDir string) (guestevidence.CellsProbe, error)
}

// cgroup2Mount returns the mount point of the cgroup2 hierarchy from mountinfo text.
func cgroup2Mount(mountinfo string) (string, error) {
	for _, line := range strings.Split(mountinfo, "\n") {
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		fields := strings.Fields(left)
		if r := strings.Fields(right); len(r) > 0 && r[0] == "cgroup2" && len(fields) >= 5 {
			return unescapeMount(fields[4]), nil
		}
	}
	return "", errors.New("no cgroup2 mount is visible")
}

var octalEscape = regexp.MustCompile(`\\([0-7]{3})`)

func unescapeMount(s string) string {
	return octalEscape.ReplaceAllStringFunc(s, func(m string) string {
		var v byte
		for _, c := range m[1:] {
			v = v<<3 | byte(c-'0')
		}
		return string([]byte{v})
	})
}

func probeCapability(env capabilityEnv) (guestevidence.Capability, error) {
	var c guestevidence.Capability
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return c, err
	}
	c.KernelRelease, c.Arch = unix.ByteSliceToString(uts.Release[:]), runtime.GOARCH
	if !filepath.IsAbs(env.seccompProfile) {
		return c, errors.New("--seccomp-profile must be the absolute path of the installed seccomp profile")
	}
	var err error
	if c.Seccomp.ProfileSHA256, err = fileDigest(env.seccompProfile, false); err != nil {
		return c, fmt.Errorf("seccomp profile: %w", err)
	}
	info, err := os.ReadFile(env.mountinfo)
	if err != nil {
		return c, err
	}
	mount, err := cgroup2Mount(string(info))
	if err != nil {
		return c, err
	}
	c.Cgroup.Mount, c.Cgroup.FSType, c.Cgroup.ServiceCgroup = mount, "cgroup2", env.serviceCgroup
	controllers, err := os.ReadFile(filepath.Join(mount, "cgroup.controllers"))
	if err != nil {
		return c, fmt.Errorf("cgroup controllers: %w", err)
	}
	c.Cgroup.Controllers = strings.Fields(string(controllers))
	if !strings.HasPrefix(env.serviceCgroup, "/") || strings.Contains(env.serviceCgroup, "..") {
		return c, fmt.Errorf("--service-cgroup %q is not a clean absolute cgroup path", env.serviceCgroup)
	}
	dir := filepath.Join(mount, env.serviceCgroup)
	if kind, err := os.ReadFile(filepath.Join(dir, "cgroup.type")); err == nil {
		c.Cgroup.ServiceCgroupType = strings.TrimSpace(string(kind))
	}
	if st, err := os.Stat(filepath.Join(dir, "cgroup.kill")); err == nil && st.Mode().IsRegular() {
		c.Cgroup.KillInterface = true
	}
	if c.Cgroup.Chain, err = readDelegationChain(mount); err != nil {
		return c, err
	}
	probe := env.cellsProbe
	if probe == nil {
		probe = probeCellsAsService
	}
	if c.Cgroup.CellsProbe, err = probe(filepath.Join(mount, guestevidence.CellsCgroup)); err != nil {
		return c, fmt.Errorf("cells cgroup probe: %w", err)
	}
	c.Landlock = probeLandlock()
	c.Seccomp.FilterInstalled, c.Seccomp.Mode = probeSeccompFilter()
	return c, nil
}

// probeLandlock asks the kernel for its Landlock ABI version and, if it reports them, its errata. No
// ruleset is created.
func probeLandlock() guestevidence.CapabilityLandlock {
	const (
		createRulesetVersion = 1 << 0
		createRulesetErrata  = 1 << 1
	)
	var l guestevidence.CapabilityLandlock
	if abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, createRulesetVersion); errno == 0 {
		l.ABI = int(abi)
	}
	if errata, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, createRulesetErrata); errno == 0 {
		l.ErrataSupported, l.Errata = true, uint32(errata)
	}
	return l
}

// probeSeccompFilter installs an allow-everything filter on a thread of its own and reads the thread's
// seccomp mode back. The thread is locked and never unlocked, so the runtime ends it with the goroutine
// (or, if it is the main thread, wedges it) and the filter is never inherited by anything else of this
// process: threads the runtime starts from a locked thread come from its template thread.
func probeSeccompFilter() (installed bool, mode int) {
	type result struct {
		installed bool
		mode      int
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			done <- result{}
			return
		}
		allow := []unix.SockFilter{{Code: 0x06, K: 0x7fff0000}} // BPF_RET | SECCOMP_RET_ALLOW
		prog := unix.SockFprog{Len: uint16(len(allow)), Filter: &allow[0]}
		if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, 1 /* SECCOMP_SET_MODE_FILTER */, 0, uintptr(unsafe.Pointer(&prog))); errno != 0 {
			done <- result{}
			return
		}
		status, err := os.ReadFile("/proc/thread-self/status")
		if err != nil {
			done <- result{true, 0}
			return
		}
		for _, line := range strings.Split(string(status), "\n") {
			if v, ok := strings.CutPrefix(line, "Seccomp:"); ok {
				m := 0
				fmt.Sscanf(strings.TrimSpace(v), "%d", &m)
				done <- result{true, m}
				return
			}
		}
		done <- result{true, 0}
	}()
	r := <-done
	return r.installed, r.mode
}

// ---- provider exchange ----

func providerExchangeFrom(path string) (guestevidence.ProviderExchange, error) {
	if path == "" {
		return guestevidence.ProviderExchange{}, errors.New("--response is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return guestevidence.ProviderExchange{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxGuestInputSize+1))
	if err != nil || len(data) > maxGuestInputSize {
		return guestevidence.ProviderExchange{}, errors.New("the response file is unreadable or too large")
	}
	line := strings.TrimSuffix(string(data), "\n")
	if line == "" || strings.Contains(line, "\n") {
		return guestevidence.ProviderExchange{}, errors.New("the response file must hold exactly the one response line of the governed client")
	}
	return guestevidence.NewProviderExchange(line), nil
}

// ---- guest test runs ----

var (
	testRunLineRE    = regexp.MustCompile(`^=== RUN +(Test[^\s/]*)$`)
	testResultLineRE = regexp.MustCompile(`^--- (PASS|FAIL|SKIP): (Test[^\s/]*) \(`)
)

// parseTestOutput reads the verbose output of a test binary. Only top-level tests count: a test that
// started and has no result line is reported as failed.
func parseTestOutput(out []byte) []guestevidence.TestResult {
	started := map[string]bool{}
	results := map[string]string{}
	var order []string
	note := func(name string) {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := testRunLineRE.FindStringSubmatch(line); m != nil {
			started[m[1]] = true
			note(m[1])
			continue
		}
		if m := testResultLineRE.FindStringSubmatch(line); m != nil {
			action := map[string]string{"PASS": guestevidence.TestPass, "FAIL": guestevidence.TestFail, "SKIP": guestevidence.TestSkip}[m[1]]
			if prev, dup := results[m[2]]; dup && prev != action {
				action = guestevidence.TestFail
			}
			results[m[2]] = action
			note(m[2])
		}
	}
	var res []guestevidence.TestResult
	for _, name := range order {
		action, ok := results[name]
		if !ok {
			action = guestevidence.TestFail
		}
		res = append(res, guestevidence.TestResult{Test: name, Action: action})
	}
	return res
}

// runGuestTests runs the test binary the capture of the frozen commit built, with the fixed arguments, and
// records what happened. The digest of the binary is the digest of the file that ran.
func runGuestTests(ctx context.Context, kind, binary string) (guestevidence.TestRun, error) {
	spec, ok := guestevidence.SpecFor(kind)
	if !ok {
		return guestevidence.TestRun{}, fmt.Errorf("--kind must be one of guest-tests-procoracle, guest-tests-landlock-connect, not %q", kind)
	}
	if !filepath.IsAbs(binary) {
		return guestevidence.TestRun{}, errors.New("--test-binary must be an absolute path")
	}
	digest, err := fileDigest(binary, false)
	if err != nil {
		return guestevidence.TestRun{}, err
	}
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return guestevidence.TestRun{}, err
	}
	work, err := os.MkdirTemp("", "tbound-guest-tests-")
	if err != nil {
		return guestevidence.TestRun{}, err
	}
	defer os.RemoveAll(work)
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, guestevidence.TestRunArgs...)
	cmd.Dir, cmd.Env = work, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + work, "TMPDIR=" + work, "LANG=C", "LC_ALL=C"}
	cmd.Stdout, cmd.Stderr = &limitedWriter{w: &out, left: maxTestRunOutput}, &limitedWriter{w: &out, left: maxTestRunOutput}
	run := guestevidence.TestRun{Package: spec.Package, TestBinarySHA256: digest, Args: slices.Clone(guestevidence.TestRunArgs),
		KernelRelease: unix.ByteSliceToString(uts.Release[:])}
	var exitErr *exec.ExitError
	switch err := cmd.Run(); {
	case err == nil:
	case errors.As(err, &exitErr) && exitErr.ExitCode() > 0:
		run.ExitCode = exitErr.ExitCode()
	default:
		return run, fmt.Errorf("the test binary did not run to an exit status: %w", err)
	}
	sum := sha256.Sum256(out.Bytes())
	run.OutputSHA256 = "sha256:" + hex.EncodeToString(sum[:])
	run.Results = parseTestOutput(out.Bytes())
	return run, nil
}

type limitedWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > l.left {
		p = p[:l.left]
	}
	l.left -= len(p)
	l.w.Write(p)
	return len(p), nil
}

// ---- net sink ----

type netSinkEnv struct {
	self       string
	bootID     func() (string, error)
	readFile   func(path string) ([]byte, error)
	mountinfo  string
	nftList    func(ctx context.Context) ([]byte, error)
	probe      func(ctx context.Context, cgroupDir string) (netSinkProbeResult, error)
	cellsPath  string
	attestPath string
	rulePath   string
}

func productionNetSinkEnv(self string) netSinkEnv {
	return netSinkEnv{
		self: self, bootID: piruntime.ReadD09BootID, readFile: os.ReadFile, mountinfo: "/proc/self/mountinfo",
		cellsPath: guestevidence.CellsCgroup, attestPath: piruntime.D09NetSinkAttestPath, rulePath: piruntime.D09NetSinkRulePath,
		nftList: func(ctx context.Context) ([]byte, error) {
			cmd := exec.CommandContext(ctx, nftBinary, "-j", "list", "table", "inet", piruntime.D09NetSinkTable)
			cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("nft list: %w: %s", err, strings.TrimSpace(stderr.String()))
			}
			return out, nil
		},
		probe: runProbeInCgroup,
	}
}

// netSinkProbeResult is what the probe child reports from inside the net-sink cgroup.
type netSinkProbeResult struct {
	Cgroup         string `json:"cgroup"`
	AllowedConnect string `json:"allowed_connect"`
	WrongPort      string `json:"wrong_port"`
	OtherLoopback  string `json:"other_loopback"`
	UDP            string `json:"udp"`
}

func probeNetSink(ctx context.Context, env netSinkEnv) (guestevidence.NetSink, error) {
	var n guestevidence.NetSink
	var err error
	if n.BootID, err = env.bootID(); err != nil {
		return n, fmt.Errorf("boot id: %w", err)
	}
	raw, err := env.readFile(env.attestPath)
	if err != nil {
		return n, fmt.Errorf("net-sink attestation (run tbound trial reset first): %w", err)
	}
	var a piruntime.D09NetSinkAttestation
	if err := guestevidence.DecodeStrict(raw, &a); err != nil {
		return n, fmt.Errorf("net-sink attestation: %w", err)
	}
	n.Attestation = guestevidence.NetSinkAttestation{Schema: a.Schema, BootID: a.BootID, ParentDev: a.ParentDev, ParentIno: a.ParentIno, RuleDigest: a.RuleDigest}
	rule, err := env.readFile(env.rulePath)
	if err != nil {
		return n, fmt.Errorf("net-sink rule file: %w", err)
	}
	ruleSum := sha256.Sum256(bytes.ReplaceAll(rule, []byte("\r\n"), []byte("\n")))
	n.RuleFileSHA256 = "sha256:" + hex.EncodeToString(ruleSum[:])
	n.CellsCgroup = env.cellsPath
	info, err := env.readFile(env.mountinfo)
	if err != nil {
		return n, err
	}
	mount, err := cgroup2Mount(string(info))
	if err != nil {
		return n, err
	}
	cgroupPath, level, err := piruntime.D09NetSinkNftMatch(filepath.Join(mount, env.cellsPath))
	if err != nil {
		return n, err
	}

	before, err := env.nftList(ctx)
	if err != nil {
		return n, err
	}
	sum := sha256.Sum256(before)
	n.NftListingSHA256 = "sha256:" + hex.EncodeToString(sum[:])
	if err := piruntime.VerifyNetSinkRuleset(before, cgroupPath, level); err != nil {
		return n, fmt.Errorf("the loaded table is not the pinned rule: %w", err)
	}
	n.RulesetVerified = true
	if n.Probes.DropCounterBefore, err = dropCounter(before); err != nil {
		return n, err
	}
	res, err := env.probe(ctx, filepath.Join(mount, env.cellsPath, piruntime.D09NetSinkCgroupName))
	if err != nil {
		return n, err
	}
	if want := filepath.Join(env.cellsPath, piruntime.D09NetSinkCgroupName); res.Cgroup != want {
		return n, fmt.Errorf("the probe ran in cgroup %q, not in %q", res.Cgroup, want)
	}
	n.Probes.AllowedConnect, n.Probes.WrongPort, n.Probes.OtherLoopback, n.Probes.UDP = res.AllowedConnect, res.WrongPort, res.OtherLoopback, res.UDP
	after, err := env.nftList(ctx)
	if err != nil {
		return n, err
	}
	if err := piruntime.VerifyNetSinkRuleset(after, cgroupPath, level); err != nil {
		return n, fmt.Errorf("the table changed during the probe: %w", err)
	}
	if n.Probes.DropCounterAfter, err = dropCounter(after); err != nil {
		return n, err
	}
	return n, nil
}

// dropCounter returns the packet count of the counter in the table's drop rule.
func dropCounter(listing []byte) (uint64, error) {
	var doc struct {
		Items []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(listing, &doc); err != nil {
		return 0, err
	}
	for _, item := range doc.Items {
		raw, ok := item["rule"]
		if !ok {
			continue
		}
		var rule struct {
			Expr []map[string]json.RawMessage `json:"expr"`
		}
		if err := json.Unmarshal(raw, &rule); err != nil {
			return 0, err
		}
		hasDrop, packets, hasCounter := false, uint64(0), false
		for _, e := range rule.Expr {
			if _, ok := e["drop"]; ok {
				hasDrop = true
			}
			if c, ok := e["counter"]; ok {
				var counter struct {
					Packets uint64 `json:"packets"`
				}
				if err := json.Unmarshal(c, &counter); err != nil {
					return 0, err
				}
				packets, hasCounter = counter.Packets, true
			}
		}
		if hasDrop && hasCounter {
			return packets, nil
		}
	}
	return 0, errors.New("the table has no counted drop rule")
}

// runProbeInCgroup runs this executable's probe verb as a child that the kernel starts inside the given
// cgroup (clone3 with CLONE_INTO_CGROUP), so that the child's sockets match the rule from their first
// packet.
func runProbeInCgroup(ctx context.Context, cgroupDir string) (netSinkProbeResult, error) {
	var res netSinkProbeResult
	dir, err := os.Open(cgroupDir)
	if err != nil {
		return res, fmt.Errorf("net-sink cgroup: %w", err)
	}
	defer dir.Close()
	self, err := os.Executable()
	if err != nil {
		return res, err
	}
	cmd := exec.CommandContext(ctx, self, "guest-evidence", netSinkProbeVerb)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(dir.Fd())}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return res, fmt.Errorf("probe child: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if len(out) > maxProbeOutput {
		return res, errors.New("probe child output is too large")
	}
	if err := guestevidence.DecodeStrict(out, &res); err != nil {
		return res, fmt.Errorf("probe child output: %w", err)
	}
	return res, nil
}

// runNetSinkProbe is the child: it makes the probe connections and reports what happened to each.
func runNetSinkProbe(stdout io.Writer) error {
	cgroup := ""
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "0::"); ok {
				cgroup = v
			}
		}
	}
	sink := net.JoinHostPort(piruntime.D09NetSinkAddress, fmt.Sprint(piruntime.D09NetSinkPort))
	res := netSinkProbeResult{
		Cgroup:         cgroup,
		AllowedConnect: dialOutcome(sink),
		WrongPort:      dialOutcome(net.JoinHostPort(piruntime.D09NetSinkAddress, fmt.Sprint(piruntime.D09NetSinkPort+1))),
		OtherLoopback:  dialOutcome(net.JoinHostPort("127.0.0.1", fmt.Sprint(piruntime.D09NetSinkPort))),
		UDP:            udpOutcome(sink),
	}
	return json.NewEncoder(stdout).Encode(res)
}

// dialOutcome is "connected", "timeout" (nothing answered: a dropped SYN), "refused" (the host answered
// with a reset: nothing dropped the packet) or the error text.
func dialOutcome(addr string) string {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		conn.Close()
		return guestevidence.ProbeConnected
	}
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.As(err, &ne) && ne.Timeout():
		return guestevidence.ProbeTimeout
	}
	return "error: " + err.Error()
}

func udpOutcome(addr string) string {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return "error: " + err.Error()
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("tbound-netsink-probe")); err != nil {
		return "error: " + err.Error()
	}
	return guestevidence.ProbeSent
}

// ---- delegation chain and the cells cgroup ----

// readDelegationChain reads, for every cgroup from the root to the cells cgroup, its type, the
// controllers it has and the controllers it enables for its children. A file that is absent gives an
// empty value, which the validator refuses; the cells cgroup itself must exist.
func readDelegationChain(mount string) ([]guestevidence.CgroupLevel, error) {
	var chain []guestevidence.CgroupLevel
	for _, p := range guestevidence.CapabilityChain {
		dir := filepath.Join(mount, p)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("cgroup %s is not present below %s: %v", p, mount, err)
		}
		read := func(name string) string {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(data))
		}
		chain = append(chain, guestevidence.CgroupLevel{Path: p, Type: read("cgroup.type"),
			Controllers: strings.Fields(read("cgroup.controllers")), SubtreeControl: strings.Fields(read("cgroup.subtree_control"))})
	}
	return chain, nil
}

// probeCellsAsService makes, inspects and removes a child of the cells cgroup with the identity of the
// governed service (uid 995, gid 986). The identity is switched on one locked OS thread with raw system
// calls, which change the credentials of the calling thread only; the goroutine ends without unlocking,
// so the runtime ends the thread and nothing else of this process runs as the service. It needs root.
func probeCellsAsService(cellsDir string) (guestevidence.CellsProbe, error) {
	type result struct {
		probe guestevidence.CellsProbe
		err   error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		uid, gid := uintptr(guestevidence.ServiceUID), uintptr(guestevidence.ServiceGID)
		if _, _, errno := unix.RawSyscall(unix.SYS_SETGROUPS, 0, 0, 0); errno != 0 {
			done <- result{err: fmt.Errorf("drop supplementary groups: %w", errno)}
			return
		}
		if _, _, errno := unix.RawSyscall(unix.SYS_SETRESGID, gid, gid, gid); errno != 0 {
			done <- result{err: fmt.Errorf("switch to gid %d: %w", gid, errno)}
			return
		}
		if _, _, errno := unix.RawSyscall(unix.SYS_SETRESUID, uid, uid, uid); errno != 0 {
			done <- result{err: fmt.Errorf("switch to uid %d: %w", uid, errno)}
			return
		}
		p, err := probeCellsChild(cellsDir, nil)
		done <- result{p, err}
	}()
	r := <-done
	return r.probe, r.err
}

// probeCellsChild runs as the calling identity: it creates a child cgroup of cellsDir, opens each of
// guestevidence.CellsProbeFiles for writing without writing anything (so nothing is killed, moved or
// limited), and removes the child. populate, when not nil, runs after the child exists and returns what
// to undo before the removal (a test makes the files a real cgroup2 filesystem would have).
func probeCellsChild(cellsDir string, populate func(child string) (undo func())) (guestevidence.CellsProbe, error) {
	p := guestevidence.CellsProbe{UID: os.Geteuid(), GID: os.Getegid()}
	child := filepath.Join(cellsDir, fmt.Sprintf("tbound-evq-probe-%d", os.Getpid()))
	if err := os.Mkdir(child, 0o755); err != nil {
		return p, fmt.Errorf("make a child of the cells cgroup: %w", err)
	}
	p.ChildCreated = true
	undo := func() {}
	if populate != nil {
		undo = populate(child)
	}
	for _, name := range guestevidence.CellsProbeFiles {
		f := guestevidence.CellsProbeFile{Name: name}
		if _, err := os.Lstat(filepath.Join(child, name)); err == nil {
			f.Exists = true
		}
		if file, err := os.OpenFile(filepath.Join(child, name), os.O_WRONLY, 0); err == nil {
			f.Writable = true
			file.Close()
		}
		p.Files = append(p.Files, f)
	}
	undo()
	if err := os.Remove(child); err != nil {
		return p, fmt.Errorf("remove the probe child %s (an empty cgroup stays; remove it by hand): %w", child, err)
	}
	p.ChildRemoved = true
	return p, nil
}
