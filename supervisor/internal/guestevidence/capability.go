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

// The governed service identity (tbound-pi): the owner of the cells cgroup and the identity the cell
// cgroups are made by. The probe of the cells subtree runs as this uid and gid.
const (
	ServiceUID = 995
	ServiceGID = 986
)

// CapabilityChain is the delegation chain the producer reads, from the hierarchy root to the cells
// cgroup, as paths below the cgroup2 mount.
var CapabilityChain = []string{"/", "/system.slice", ServiceCgroup, CellsCgroup}

// CellsProbeFiles are the files of a probe child of the cells cgroup that the service identity must be
// able to open for writing: the kill and migration interfaces and the three limit files the cell
// template sets (cpus, memory, pids).
var CellsProbeFiles = []string{"cgroup.kill", "cgroup.procs", "cpu.max", "memory.max", "pids.max"}

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
	// Chain lists, for every cgroup from the root to the cells cgroup, the controllers it has and the
	// ones it enables for its children. CellsProbe is what the service identity could do in a child of
	// the cells cgroup.
	Chain      []CgroupLevel `json:"chain"`
	CellsProbe CellsProbe    `json:"cells_probe"`
}

// CgroupLevel is one cgroup of the delegation chain: its type, the controllers available to it
// (cgroup.controllers) and the ones it enables for its children (cgroup.subtree_control).
type CgroupLevel struct {
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	Controllers    []string `json:"controllers"`
	SubtreeControl []string `json:"subtree_control"`
}

// CellsProbe is the result of making, using and removing a child of the cells cgroup as the service
// identity: Files states, in CellsProbeFiles order, whether each file exists in the child and whether
// the identity could open it for writing. Nothing is written, so nothing is killed or moved.
type CellsProbe struct {
	UID          int              `json:"uid"`
	GID          int              `json:"gid"`
	ChildCreated bool             `json:"child_created"`
	ChildRemoved bool             `json:"child_removed"`
	Files        []CellsProbeFile `json:"files"`
}

// CellsProbeFile is one probed file of the child cgroup.
type CellsProbeFile struct {
	Name     string `json:"name"`
	Exists   bool   `json:"exists"`
	Writable bool   `json:"writable"`
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
	validateChain(c.Cgroup.Chain, add)
	validateCellsProbe(c.Cgroup.CellsProbe, add)
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

// validateChain requires the chain to be exactly CapabilityChain, every level to have the required
// controllers and to enable them for its children (so the cells cgroup has the controllers and its
// children get the limit files), and the cells cgroup to be a domain.
func validateChain(chain []CgroupLevel, add func(string, ...any)) {
	if len(chain) != len(CapabilityChain) {
		add("the delegation chain has %d levels, want the %d of %v", len(chain), len(CapabilityChain), CapabilityChain)
		return
	}
	for i, level := range chain {
		if level.Path != CapabilityChain[i] {
			add("chain level %d is %q, want %q", i, level.Path, CapabilityChain[i])
			continue
		}
		for _, want := range requiredControllers {
			if !slices.Contains(level.Controllers, want) {
				add("cgroup %s has no %s controller (have %v)", level.Path, want, level.Controllers)
			}
			if !slices.Contains(level.SubtreeControl, want) {
				add("cgroup %s does not enable %s for its children (subtree_control %v)", level.Path, want, level.SubtreeControl)
			}
		}
	}
	if cells := chain[len(chain)-1]; cells.Path == CellsCgroup && cells.Type != "domain" {
		add("cells cgroup type %q, want domain", cells.Type)
	}
}

func validateCellsProbe(p CellsProbe, add func(string, ...any)) {
	if p.UID != ServiceUID || p.GID != ServiceGID {
		add("the cells probe ran as %d:%d, want the service identity %d:%d", p.UID, p.GID, ServiceUID, ServiceGID)
	}
	if !p.ChildCreated || !p.ChildRemoved {
		add("the service identity could not make and remove a child of the cells cgroup (created %t, removed %t)", p.ChildCreated, p.ChildRemoved)
	}
	if len(p.Files) != len(CellsProbeFiles) {
		add("the cells probe reports %d files, want %v", len(p.Files), CellsProbeFiles)
		return
	}
	for i, f := range p.Files {
		switch {
		case f.Name != CellsProbeFiles[i]:
			add("cells probe file %d is %q, want %q", i, f.Name, CellsProbeFiles[i])
		case !f.Exists || !f.Writable:
			add("in a child of the cells cgroup %s is not writable by the service identity (exists %t, writable %t)", f.Name, f.Exists, f.Writable)
		}
	}
}
