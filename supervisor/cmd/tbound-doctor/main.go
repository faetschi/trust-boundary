// Command tbound-doctor is a standalone preflight ("doctor") for a tbound user
// install. It reports whether the machine is ready to run the tbound Pi workflow
// and explains how to fix each problem. It never changes system state, never
// requires sudo, and never installs anything unless --install-pi is passed.
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
	Version   string  `json:"version"`
	GOOS      string  `json:"goos"`
	GOARCH    string  `json:"goarch"`
	Prefix    string  `json:"prefix"`
	Checks    []check `json:"checks"`
	DevReady  bool    `json:"dev_ready"`
	GovReady  bool    `json:"governed_ready"`
	FailCount int     `json:"fail_count"`
	WarnCount int     `json:"warn_count"`
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
	node := fs.String("node", "", "explicit path to a Node binary")
	pi := fs.String("pi", "", "existing node_modules directory that contains Pi")
	jsonOut := fs.Bool("json", false, "emit JSON instead of text")
	installPi := fs.Bool("install-pi", false, "install the pinned Pi packages into the prefix (needs network)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	ctx := context.Background()
	opts := piinstall.Options{
		Prefix:       *prefix,
		ExplicitNode: *node,
		GOOS:         runtime.GOOS,
		Run:          realRun,
		LookPath:     exec.LookPath,
	}

	if *installPi {
		if err := piinstall.InstallPinnedPackages(ctx, opts, true); err != nil {
			return fmt.Errorf("install pinned Pi packages: %w", err)
		}
		if err := piinstall.LinkRuntimeNodeModules(*prefix); err != nil {
			return fmt.Errorf("link runtime node_modules: %w", err)
		}
	}

	rep := buildReport(ctx, *version, *prefix, *pi, opts)

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

func buildReport(ctx context.Context, version, prefix, explicitPi string, opts piinstall.Options) report {
	rep := report{Version: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Prefix: prefix}

	rep.Checks = append(rep.Checks, check{Name: "os", Status: pass, Detail: runtime.GOOS + "/" + runtime.GOARCH})

	if exe, err := os.Executable(); err == nil {
		rep.Checks = append(rep.Checks, check{Name: "self", Status: pass, Detail: exe})
	} else {
		rep.Checks = append(rep.Checks, check{Name: "self", Status: warn, Detail: "cannot resolve own executable"})
	}

	// Prefix.
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

	// Node.
	nodeInfo, nodeErr := piinstall.DiscoverNode(ctx, opts)
	switch {
	case nodeErr != nil:
		rep.Checks = append(rep.Checks, check{
			Name: "node", Status: fail, Detail: nodeErr.Error(),
			Remediation: fmt.Sprintf("install Node >= %d.%d, or pass --node /path/to/node", piinstall.MinNodeMajor, piinstall.MinNodeMinor),
		})
	case !nodeInfo.Satisfies():
		rep.Checks = append(rep.Checks, check{
			Name: "node", Status: fail,
			Detail:      fmt.Sprintf("%s (%s) is older than required %d.%d", nodeInfo.Version, nodeInfo.Path, piinstall.MinNodeMajor, piinstall.MinNodeMinor),
			Remediation: fmt.Sprintf("upgrade Node to >= %d.%d", piinstall.MinNodeMajor, piinstall.MinNodeMinor),
		})
	default:
		rep.Checks = append(rep.Checks, check{Name: "node", Status: pass, Detail: fmt.Sprintf("%s (%s, %s)", nodeInfo.Version, nodeInfo.Path, nodeInfo.Source)})
	}

	// Pi.
	piModules := explicitPi
	if piModules == "" {
		piModules = piinstall.PrefixPIModules(prefix)
	}
	if v, ok := piinstall.DetectNodeModulesPi(piModules); ok {
		st := pass
		detail := fmt.Sprintf("%s in %s", v, piModules)
		if v != piinstall.PinnedPiVersion {
			st = warn
			detail += fmt.Sprintf(" — pinned version is %s", piinstall.PinnedPiVersion)
		}
		rep.Checks = append(rep.Checks, check{Name: "pi", Status: st, Detail: detail})
	} else {
		rep.Checks = append(rep.Checks, check{
			Name: "pi", Status: warn, Detail: "no Pi packages found at " + piModules,
			Remediation: "run `tbound-doctor --install-pi` (side-by-side install, needs network) or point --pi at an existing node_modules",
		})
	}

	// Linux-only capability checks.
	if runtime.GOOS == "linux" {
		rep.Checks = append(rep.Checks, cgroupCheck())
		rep.Checks = append(rep.Checks, lookPathCheck("podman", "needed for governed containment (Phase 2)"))
		rep.Checks = append(rep.Checks, lookPathCheck("crun", "needed for governed containment (Phase 2)"))
		rep.Checks = append(rep.Checks, lookPathCheck("cosign", "needed to verify the signed cell image (Phase 2)"))
		rep.Checks = append(rep.Checks, signedProfileCheck())
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
	// Dev-ready needs a usable Node and a detectable Pi; governed additionally
	// needs the Linux containment stack and a signed profile.
	rep.DevReady = nodeErr == nil && nodeInfo.Satisfies()
	rep.GovReady = rep.DevReady && runtime.GOOS == "linux" && rep.WarnCount == 0
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

func lookPathCheck(name, reason string) check {
	if p, err := exec.LookPath(name); err == nil {
		return check{Name: name, Status: pass, Detail: p}
	}
	return check{Name: name, Status: warn, Detail: "not found — " + reason, Remediation: "install " + name + " for governed runs"}
}

func signedProfileCheck() check {
	candidates := []string{
		"/etc/tbound/pi-host-profile.json",
		"/etc/tbound/pi-host-profile.ed25519",
		"/etc/tbound/pi-host-profile.pub",
	}
	var missing []string
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, filepath.Base(p))
		}
	}
	if len(missing) == 0 {
		return check{Name: "signed-profile", Status: pass, Detail: "/etc/tbound profile present"}
	}
	return check{
		Name: "signed-profile", Status: warn,
		Detail:      "missing: " + strings.Join(missing, ", "),
		Remediation: "governed serve --pi is refused until a signed root-owned host profile exists (Phase 2); dev runs do not need it",
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
	fmt.Printf("\ndev-ready: %v   governed-ready: %v   (%d fail, %d warn)\n", rep.DevReady, rep.GovReady, rep.FailCount, rep.WarnCount)
	if !rep.GovReady {
		fmt.Println("note: governed `tbound serve --pi` also needs the signed profile + Podman/crun/cosign containment stack.")
	}
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
