package guestevidence

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The facts a guest must show for EVQ-LINUX-CAPABILITY-GUEST (Spike A).
const (
	// ServiceCgroup is the cgroup of the governed service; its cgroup.kill is the interface the
	// reset and the cancellation use.
	ServiceCgroup = "/system.slice/tbound-pi.service"
	// RequiredLandlockABI is the exact Landlock ABI the D09 profile requires.
	RequiredLandlockABI = 8
)

// requiredControllers are the cgroup controllers the cells cgroup delegates.
var requiredControllers = []string{"cpu", "memory", "pids"}

// Capability is the data of a guest-capability artifact.
type Capability struct {
	KernelRelease string             `json:"kernel_release"`
	Arch          string             `json:"arch"`
	Cgroup        CapabilityCgroup   `json:"cgroup"`
	Landlock      CapabilityLandlock `json:"landlock"`
	Seccomp       CapabilitySeccomp  `json:"seccomp"`
}

// CapabilityCgroup is what the producer read from the cgroup2 hierarchy.
type CapabilityCgroup struct {
	Mount             string   `json:"mount"`
	FSType            string   `json:"fstype"`
	Controllers       []string `json:"controllers"`
	ServiceCgroup     string   `json:"service_cgroup"`
	ServiceCgroupType string   `json:"service_cgroup_type"`
	KillInterface     bool     `json:"kill_interface"`
}

// CapabilityLandlock is the kernel's Landlock ABI and, where the kernel reports them, its errata.
type CapabilityLandlock struct {
	ABI             int    `json:"abi"`
	ErrataSupported bool   `json:"errata_supported"`
	Errata          uint32 `json:"errata"`
}

// CapabilitySeccomp is the result of installing a filter in a disposable thread and the digest of
// the static seccomp profile file the cell template names.
type CapabilitySeccomp struct {
	FilterInstalled bool   `json:"filter_installed"`
	Mode            int    `json:"mode"`
	ProfileSHA256   string `json:"profile_sha256"`
}

func validateCapability(raw []byte) error {
	var c Capability
	if err := DecodeStrict(raw, &c); err != nil {
		return err
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if strings.TrimSpace(c.KernelRelease) == "" {
		add("kernel_release is empty")
	}
	if c.Arch != "amd64" {
		add("arch %q, want amd64", c.Arch)
	}
	if c.Cgroup.FSType != "cgroup2" || !strings.HasPrefix(c.Cgroup.Mount, "/") {
		add("the cgroup hierarchy is %q at %q, want cgroup2 at an absolute mount", c.Cgroup.FSType, c.Cgroup.Mount)
	}
	for _, want := range requiredControllers {
		if !slices.Contains(c.Cgroup.Controllers, want) {
			add("cgroup controller %s is not available (have %v)", want, c.Cgroup.Controllers)
		}
	}
	if c.Cgroup.ServiceCgroup != ServiceCgroup {
		add("service cgroup %q, want %q", c.Cgroup.ServiceCgroup, ServiceCgroup)
	}
	if c.Cgroup.ServiceCgroupType != "domain" {
		add("service cgroup type %q, want domain", c.Cgroup.ServiceCgroupType)
	}
	if !c.Cgroup.KillInterface {
		add("the service cgroup has no cgroup.kill interface")
	}
	if c.Landlock.ABI != RequiredLandlockABI {
		add("Landlock ABI %d, want exactly %d", c.Landlock.ABI, RequiredLandlockABI)
	}
	if !c.Landlock.ErrataSupported && c.Landlock.Errata != 0 {
		add("errata %d is reported although the kernel does not report errata", c.Landlock.Errata)
	}
	if !c.Seccomp.FilterInstalled || c.Seccomp.Mode != 2 {
		add("a seccomp filter was not installed in filter mode (installed %t, mode %d)", c.Seccomp.FilterInstalled, c.Seccomp.Mode)
	}
	if !IsDigest(c.Seccomp.ProfileSHA256) {
		add("seccomp profile digest %q is not a sha256 digest", c.Seccomp.ProfileSHA256)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
