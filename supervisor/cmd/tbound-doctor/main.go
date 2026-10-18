// Command tbound-doctor is a standalone preflight ("doctor") for a tbound user
// install. It reports whether the machine is ready to run the tbound Pi workflow
// and explains how to fix each problem. It never changes system state and
// never installs Pi.
//
// It is deliberately a separate binary from tbound, which stays focused on the
// supervisor runtime.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"tbound/supervisor/internal/piinstall"
)

type status string

const (
	pass status = "PASS"
	warn status = "WARN"
	fail status = "FAIL"
)

type check struct {
	Name        string `json:"name"`
	Status      status `json:"status"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

type report struct {
	Version  string  `json:"version"`
	GOOS     string  `json:"goos"`
	GOARCH   string  `json:"goarch"`
	Prefix   string  `json:"prefix"`
	Checks   []check `json:"checks"`
	DevReady bool    `json:"dev_ready"`
	GovReady bool    `json:"governed_ready"`
	// GovernedPrereqsAdvisory reports whether the Linux prerequisites looked
	// present. It is advisory only: it is never an admission result.
	GovernedPrereqsAdvisory bool `json:"governed_prereqs_advisory"`
	FailCount               int  `json:"fail_count"`
	WarnCount               int  `json:"warn_count"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tbound-doctor:", err)
		os.Exit(2)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("tbound-doctor", flag.ContinueOnError)
	version := fs.String("version", "dev", "version label to report")
	prefix := fs.String("prefix", defaultPrefix(), "tbound data prefix")
	pi := fs.String("pi", "", "explicit path to the installed official Pi CLI")
	jsonOut := fs.Bool("json", false, "emit JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	ctx := context.Background()
	opts := piinstall.Options{ExplicitPi: *pi, Run: realRun, LookPath: exec.LookPath}

	rep := buildReport(ctx, *version, *prefix, opts)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		printText(rep)
	}

	if !rep.DevReady {
		os.Exit(1)
	}
	return nil
}

func buildReport(ctx context.Context, version, prefix string, opts piinstall.Options) report {
	rep := report{Version: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Prefix: prefix}

	rep.Checks = append(rep.Checks, check{Name: "os", Status: pass, Detail: runtime.GOOS + "/" + runtime.GOARCH})

	if exe, err := os.Executable(); err == nil {
		rep.Checks = append(rep.Checks, check{Name: "self", Status: pass, Detail: exe})
	} else {
		rep.Checks = append(rep.Checks, check{Name: "self", Status: warn, Detail: "cannot resolve own executable"})
	}

	if info, err := os.Stat(prefix); err == nil {
		mode := info.Mode().Perm()
		st := pass
		detail := fmt.Sprintf("%s (mode %#o)", prefix, mode)
		if runtime.GOOS != "windows" && mode&0o077 != 0 {
			st = warn
			detail += " — should be private (0700)"
		}
		rep.Checks = append(rep.Checks, check{Name: "prefix", Status: st, Detail: detail, Remediation: "chmod 700 " + prefix})
	} else {
		rep.Checks = append(rep.Checks, check{
			Name: "prefix", Status: warn, Detail: prefix + " does not exist yet",
			Remediation: "run install/install.sh, or create it with mode 0700",
		})
	}

	piInfo, piErr := piinstall.DiscoverPi(ctx, opts)
	if piErr != nil {
		rep.Checks = append(rep.Checks, check{Name: "pi", Status: fail, Detail: piErr.Error(), Remediation: "install the official Pi CLI version 0.87.1 separately or pass --pi /path/to/pi"})
	} else {
		rep.Checks = append(rep.Checks, check{Name: "pi", Status: pass, Detail: fmt.Sprintf("%s (%s; sha256:%s)", piInfo.Version, piInfo.Path, piInfo.SHA256)})
	}

	if runtime.GOOS == "linux" {
		rep.Checks = append(rep.Checks, cgroupCheck())
		rep.Checks = append(rep.Checks, lookPathCheck("podman", "needed for governed containment (Phase 2)"))
		rep.Checks = append(rep.Checks, lookPathCheck("crun", "needed for governed containment (Phase 2)"))
		rep.Checks = append(rep.Checks, lookPathCheck("cosign", "needed to verify the signed cell image (Phase 2)"))
		rep.Checks = append(rep.Checks, signedProfileCheckAt("/etc/tbound"))
		rep.Checks = append(rep.Checks, landlockCheck())
	} else {
		rep.Checks = append(rep.Checks, check{Name: "containment", Status: warn, Detail: "containment checks are Linux-only", Remediation: "use the declared Linux evaluation host for governed runs"})
	}

	for _, c := range rep.Checks {
		switch c.Status {
		case fail:
			rep.FailCount++
		case warn:
			rep.WarnCount++
		}
	}
	// Dev-ready needs the pinned installed Pi CLI; governed additionally
	// needs the Linux containment stack and a signed profile.
	rep.DevReady = piErr == nil
	// governed_ready stays false until the production composition exists. The
	// Linux checks are advisory prerequisites, not an admission result, and this
	// tool must not become a parallel, weaker authority to the runtime verifier.
	rep.GovReady = false
	rep.GovernedPrereqsAdvisory = rep.DevReady && runtime.GOOS == "linux" && rep.WarnCount == 0
	return rep
}

func cgroupCheck() check {
	b, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil {
		return check{Name: "cgroup-v2", Status: warn, Detail: "cgroup v2 not detected", Remediation: "governed runs need delegated non-threaded cgroup v2 with cgroup.kill"}
	}
	controllers := strings.TrimSpace(string(b))
	if controllers == "" {
		return check{Name: "cgroup-v2", Status: warn, Detail: "cgroup v2 present but no controllers delegated", Remediation: "delegate a cgroup v2 subtree with memory/pids and writable cgroup.kill"}
	}
	return check{Name: "cgroup-v2", Status: pass, Detail: "controllers: " + controllers}
}

func landlockCheckForABI(abi int, err error, requiredABI int) check {
	remediation := fmt.Sprintf("Use the declared D09 host/profile with exactly ABI %d; see infra/repro/CLEAN-GUEST-QUALIFICATION.md and re-run tbound-doctor after boot.", requiredABI)
	if err != nil {
		return check{Name: "landlock-abi", Status: warn, Detail: fmt.Sprintf("Landlock ABI query failed: %v", err), Remediation: remediation}
	}
	if abi != requiredABI {
		return check{Name: "landlock-abi", Status: warn, Detail: fmt.Sprintf("kernel reports ABI %d; D09 requires exactly ABI %d", abi, requiredABI), Remediation: remediation}
	}
	return check{Name: "landlock-abi", Status: pass, Detail: fmt.Sprintf("kernel reports ABI %d (matches the exact D09 requirement)", abi)}
}

func lookPathCheck(name, reason string) check {
	if p, err := exec.LookPath(name); err == nil {
		return check{Name: name, Status: pass, Detail: p}
	}
	return check{Name: name, Status: warn, Detail: "not found — " + reason, Remediation: "install " + name + " for governed runs"}
}

func signedProfileCheckAt(directory string) check {
	candidates := []string{
		filepath.Join(directory, "pi-host-profile.json"),
		filepath.Join(directory, "pi-host-profile.ed25519"),
		filepath.Join(directory, "pi-host-profile.pub"),
	}
	var missing []string
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return check{Name: "signed-profile", Status: pass,
			Detail: fmt.Sprintf("required profile paths exist under %s; tbound-doctor does not verify file type, ownership, signature, or runtime admission", directory)}
	}
	return check{
		Name: "signed-profile", Status: warn,
		Detail:      "missing: " + strings.Join(missing, ", "),
		Remediation: fmt.Sprintf("A host operator must provision the root-owned signed profile and fixed public key under %s; see docs/governed-setup.md section 4. Re-run tbound-doctor. Development runs do not need these files.", directory),
	}
}

func printText(rep report) {
	fmt.Printf("tbound-doctor %s (%s/%s)\n", rep.Version, rep.GOOS, rep.GOARCH)
	fmt.Printf("prefix: %s\n\n", rep.Prefix)
	for _, c := range rep.Checks {
		fmt.Printf("  [%s] %-16s %s\n", c.Status, c.Name, c.Detail)
		if c.Status != pass && c.Remediation != "" {
			fmt.Printf("         fix: %s\n", c.Remediation)
		}
	}
	fmt.Printf("\ndev-ready: %v   governed-ready: false (advisory prereqs met: %v)   (%d fail, %d warn)\n", rep.DevReady, rep.GovernedPrereqsAdvisory, rep.FailCount, rep.WarnCount)
	fmt.Println("note: these checks are advisory. Governed `tbound serve --pi` is refused until the signed host profile and the Podman/crun containment stack exist and are verified by the runtime; no probe result is an admission.")
}

func defaultPrefix() string {
	if v := os.Getenv("TBOUND_PREFIX"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "tbound")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "tbound")
	}
	return "tbound"
}

func realRun(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}
