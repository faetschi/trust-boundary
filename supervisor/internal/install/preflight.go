package install

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// CgroupInfo describes the cgroup v2 state relevant to isolation.
type CgroupInfo struct {
	Unified                bool
	Controllers            string
	SubtreeControlWritable bool
	CanKill                bool
}

// Probes are the host facts preflight depends on. They are injectable so the
// checks can be unit-tested without a Linux host, privileges, or side effects.
type Probes struct {
	GOOS           string
	ReadFile       func(string) ([]byte, error)
	Stat           func(string) (os.FileInfo, error)
	LookPath       func(string) (string, error)
	Run            func(ctx context.Context, name string, args ...string) (string, error)
	KernelRelease  func() (string, error)
	LandlockABI    func() (int, error)
	PidfdSupported func() (bool, error)
	UserName       func() (string, error)
	Cgroup2        func() (CgroupInfo, error)
	OwnerUID       func(path string) (uid int, ok bool)
}

// Options configures preflight. Zero values fall back to DefaultOptions.
type Options struct {
	Probes                Probes
	RequiredDistroID      string
	RequiredDistroVersion string
	MinKernel             string
	MinLandlockABI        int
	User                  string
	EtcTbound             string
}

// DefaultOptions returns the pinned Ubuntu 24.04 target and platform probes.
func DefaultOptions() Options {
	return Options{
		Probes:                DefaultProbes(),
		RequiredDistroID:      "ubuntu",
		RequiredDistroVersion: "24.04",
		MinKernel:             "5.15",
		MinLandlockABI:        3,
		User:                  currentUser(),
		EtcTbound:             "/etc/tbound",
	}
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	return ""
}

// Preflight evaluates the declared host requirements and returns a fail-closed
// report. On non-Linux platforms every check is reported unsupported.
func Preflight(ctx context.Context, opts Options) Report {
	if opts.Probes.GOOS == "" {
		opts.Probes.GOOS = DefaultProbes().GOOS
	}
	var report Report
	if opts.Probes.GOOS != "linux" {
		for _, name := range []string{
			"distro", "kernel", "landlock-abi", "seccomp", "pidfd",
			"user-namespaces", "cgroup-v2", "podman-rootless", "crun", "cosign", "subid-mapping",
		} {
			report.Unsupported(name, "host is not Linux", "run tbound install on the target Linux machine")
		}
		report.Finalize()
		return report
	}

	checkDistro(&report, opts)
	checkKernel(&report, opts)
	checkLandlock(&report, opts)
	checkSeccomp(&report, opts)
	checkPidfd(&report, opts)
	checkUserns(&report, opts)
	checkCgroup2(&report, opts)
	checkRootless(ctx, &report, opts)
	checkBinary(&report, opts, "crun", "install crun (rootless OCI runtime)")
	checkBinary(&report, opts, "cosign", "install cosign for offline cell-image verification")
	checkSubIDs(&report, opts)
	report.Finalize()
	return report
}

func checkDistro(r *Report, opts Options) {
	data, err := opts.Probes.ReadFile("/etc/os-release")
	if err != nil {
		r.Unknown("distro", "cannot read /etc/os-release: "+err.Error(), "verify the host distribution manually")
		return
	}
	fields := parseOSRelease(string(data))
	id := strings.ToLower(fields["ID"])
	version := fields["VERSION_ID"]
	if opts.RequiredDistroID != "" && id != strings.ToLower(opts.RequiredDistroID) {
		r.Fail("distro", "id="+id+" version="+version,
			fmt.Sprintf("TBound's declared profile is %s %s", opts.RequiredDistroID, opts.RequiredDistroVersion))
		return
	}
	if opts.RequiredDistroVersion != "" && version != opts.RequiredDistroVersion {
		r.Fail("distro", "id="+id+" version="+version,
			fmt.Sprintf("declared version is %s", opts.RequiredDistroVersion))
		return
	}
	r.Pass("distro", id+" "+version)
}

func checkKernel(r *Report, opts Options) {
	release, err := opts.Probes.KernelRelease()
	if err != nil {
		r.Unknown("kernel", "cannot read kernel release: "+err.Error(), "run uname -r")
		return
	}
	if opts.MinKernel != "" && compareKernel(release, opts.MinKernel) < 0 {
		r.Fail("kernel", release, "require >= "+opts.MinKernel+" for Landlock/seccomp/pidfd")
		return
	}
	r.Pass("kernel", release)
}

func checkLandlock(r *Report, opts Options) {
	abi, err := opts.Probes.LandlockABI()
	if err != nil {
		r.Unknown("landlock-abi", err.Error(), "verify CONFIG_SECURITY_LANDLOCK and a recent kernel")
		return
	}
	if abi < opts.MinLandlockABI {
		r.Fail("landlock-abi", strconv.Itoa(abi), fmt.Sprintf("require Landlock ABI >= %d", opts.MinLandlockABI))
		return
	}
	r.Pass("landlock-abi", strconv.Itoa(abi))
}

func checkSeccomp(r *Report, opts Options) {
	data, err := opts.Probes.ReadFile("/proc/sys/kernel/seccomp/actions_avail")
	if err != nil {
		r.Unknown("seccomp", "cannot read actions_avail: "+err.Error(), "verify CONFIG_SECCOMP_FILTER")
		return
	}
	if !strings.Contains(string(data), "allow") {
		r.Fail("seccomp", strings.TrimSpace(string(data)), "seccomp filter mode unavailable")
		return
	}
	r.Pass("seccomp", strings.TrimSpace(string(data)))
}

func checkPidfd(r *Report, opts Options) {
	ok, err := opts.Probes.PidfdSupported()
	if err != nil {
		r.Unknown("pidfd", err.Error(), "verify kernel pidfd_open support")
		return
	}
	if !ok {
		r.Fail("pidfd", "pidfd_open unavailable", "require a kernel with pidfd_open(2)")
		return
	}
	r.Pass("pidfd", "pidfd_open available")
}

func checkUserns(r *Report, opts Options) {
	if data, err := opts.Probes.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil {
		if strings.TrimSpace(string(data)) == "1" {
			r.Fail("user-namespaces", "apparmor_restrict_unprivileged_userns=1",
				"grant an AppArmor profile for the runtime, or use a host where unprivileged userns is permitted")
			return
		}
	}
	data, err := opts.Probes.ReadFile("/proc/sys/user/max_user_namespaces")
	if err != nil {
		r.Unknown("user-namespaces", "cannot read max_user_namespaces: "+err.Error(), "verify unprivileged user namespaces")
		return
	}
	max, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if convErr != nil || max <= 0 {
		r.Fail("user-namespaces", "max_user_namespaces="+strings.TrimSpace(string(data)),
			"enable unprivileged user namespaces")
		return
	}
	r.Pass("user-namespaces", "max_user_namespaces="+strconv.Itoa(max))
}

func checkCgroup2(r *Report, opts Options) {
	info, err := opts.Probes.Cgroup2()
	if err != nil {
		r.Unknown("cgroup-v2", err.Error(), "verify cgroup v2 unified mount at /sys/fs/cgroup")
		return
	}
	if !info.Unified {
		r.Fail("cgroup-v2", "no cgroup2 unified mount", "boot with cgroup_no_v1=all / unified hierarchy")
		return
	}
	if !info.SubtreeControlWritable {
		r.Fail("cgroup-v2", "controllers="+info.Controllers,
			"delegate a writable non-threaded cgroup v2 subtree (systemd Delegate=yes)")
		return
	}
	r.Pass("cgroup-v2", "controllers="+info.Controllers+" kill="+strconv.FormatBool(info.CanKill))
}

func checkRootless(ctx context.Context, r *Report, opts Options) {
	if _, err := opts.Probes.LookPath("podman"); err != nil {
		r.Fail("podman-rootless", "podman not found", "install rootless Podman")
		return
	}
	out, err := opts.Probes.Run(ctx, "podman", "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		r.Fail("podman-rootless", err.Error(), "run podman info and verify rootless=true")
		return
	}
	if strings.TrimSpace(out) != "true" {
		r.Fail("podman-rootless", "rootless="+strings.TrimSpace(out), "configure rootless Podman for the trial user")
		return
	}
	r.Pass("podman-rootless", "rootless=true")
}

func checkBinary(r *Report, opts Options, name, remediation string) {
	if _, err := opts.Probes.LookPath(name); err != nil {
		r.Fail(name, name+" not found", remediation)
		return
	}
	r.Pass(name, name+" present")
}

func checkSubIDs(r *Report, opts Options) {
	user := opts.User
	if user == "" {
		r.Unknown("subid-mapping", "current user unknown", "run as the trial user")
		return
	}
	ok := false
	for _, path := range []string{"/etc/subuid", "/etc/subgid"} {
		data, err := opts.Probes.ReadFile(path)
		if err != nil {
			continue
		}
		if subjectHasRange(string(data), user) {
			ok = true
		}
	}
	if !ok {
		r.Fail("subid-mapping", "no subordinate UID/GID range for "+user,
			"add a 65536-ID subuid/subgid range for the trial user")
		return
	}
	r.Pass("subid-mapping", "subordinate range present for "+user)
}

func parseOSRelease(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), "\"")
		out[k] = v
	}
	return out
}

func subjectHasRange(body, user string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, found := strings.Cut(line, ":")
		if !found || name != user {
			continue
		}
		parts := strings.Split(rest, ":")
		if len(parts) != 2 {
			continue
		}
		if _, err := strconv.Atoi(parts[1]); err == nil {
			return true
		}
	}
	return false
}

func compareKernel(got, want string) int {
	g := kernelParts(got)
	w := kernelParts(want)
	for i := 0; i < 3; i++ {
		if g[i] != w[i] {
			if g[i] < w[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func kernelParts(release string) [3]int {
	var out [3]int
	release = strings.SplitN(release, "-", 2)[0]
	segs := strings.Split(release, ".")
	for i := 0; i < 3 && i < len(segs); i++ {
		n, err := strconv.Atoi(segs[i])
		if err != nil {
			return out
		}
		out[i] = n
	}
	return out
}
