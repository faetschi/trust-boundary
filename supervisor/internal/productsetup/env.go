package productsetup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Check statuses.
const (
	Pass = "PASS"
	Warn = "WARN"
	Fail = "FAIL"
)

// Check is one advisory or prerequisite result.
type Check struct {
	Name   string
	Status string
	Detail string
	Fix    string
}

// RegistrationSummary is what a verified registration shows. Text is for people, Profiles for the
// credential check.
type RegistrationSummary struct {
	ID       string
	Digest   string
	Projects []string
	Profiles []string
	// Principals are the registration's allowed principals: the UIDs it authorizes to use the service.
	Principals []int64
	// PolicyDigests maps each project to the gate-policy digest the registration commits to.
	PolicyDigests map[string]string
	Text          string
	// GitDigest and HostIdentityDigest are the host profile the registration binds: the service refuses to
	// start with another Git or another compiled identity.
	GitDigest, HostIdentityDigest string
}

// OpenTurn is a catalog turn that is not terminal.
type OpenTurn struct{ ID, State string }

// StateAwaitingReview is the one open state an update may leave: the next start discards it. StateUnknown
// is a turn whose outcome cannot be established: no start settles it, an update ignores it, and it still
// blocks any registration change.
const (
	StateAwaitingReview = "awaiting_review"
	StateUnknown        = "unknown"
)

// Env is the target and every seam. Root prefixes each host path: "" or "/" is the live host and any
// other value is a test root. Live applies owners and runs system commands for real; without it the
// commands are only recorded. DryRun writes nothing. Nil seams are documented at their field.
type Env struct {
	Root    string
	Live    bool
	DryRun  bool
	Replace bool
	// RequirePrereqs fails the run when a prerequisite check fails. Without it the results are reported
	// only, which a test root needs because it changes nothing on the host that runs the test.
	RequirePrereqs bool
	// Prereqs returns the host prerequisite results. Nil means no check.
	Prereqs func() []Check
	// VerifyRegistration verifies the signed registration and summarizes it. Required by Setup and Doctor.
	VerifyRegistration func(registration, signature, publicKey []byte) (RegistrationSummary, error)
	// ValidateServiceConfig is the service's own check of the config Setup writes. Nil skips it.
	ValidateServiceConfig func(data []byte) error
	// CheckHostInputs statically checks the installed inputs of the service's host profile the way the service
	// does at start, and returns one check per input. Nil skips it.
	CheckHostInputs func(HostInputs) []Check
	// Exec runs a system command and returns its stdout. Nil runs the real command.
	Exec func(name string, args ...string) ([]byte, error)
	// Chown changes an owner when Live, using the validated file's /proc/self/fd path. Nil uses fchown.
	Chown func(path string, uid, gid int) error
	// Turns lists the turns of the catalog that fence a registration change: the unresolved ones and the ones
	// with an unknown outcome. The catalog is read under the installed registration with the service stopped.
	// Required by Update, Rollback, Uninstall, and Setup when it replaces a registration.
	Turns func() ([]OpenTurn, error)
	// SelfTestHook runs the installed binary's self-test after a swap. Nil records it and treats it as
	// not run. ErrSelfTestNotConfigured is not a failure.
	SelfTestHook func() error
	// BinaryFormat returns the installation format a candidate binary (the bytes setup installs, or the open file update or rollback uses) writes and reads,
	// without running it. Setup compares it with InstallationFormat, update and rollback with the installation's, before they change anything.
	// Nil skips the check, which a test root needs because its fixture binaries carry no marker.
	BinaryFormat func(candidate io.ReadSeeker) (int, error)
	// RunSelfTest runs the synthetic turn for Doctor. Nil reports the self-test as unavailable.
	RunSelfTest func(SelfTest) (string, error)
	// CredentialStatus summarizes a provider profile's credential without its key. Nil skips it.
	CredentialStatus func(profile string) (string, error)
	// Confirm asks the administrator to confirm a purge. Nil refuses.
	Confirm func(prompt string) (bool, error)
}

// ErrSelfTestNotConfigured means the installation names no self-test project.
var ErrSelfTestNotConfigured = errors.New("self-test not configured")

// ErrSelfTest marks a failed self-test.
var ErrSelfTest = errors.New("self-test failed")

// Step is one planned or applied change.
type Step struct {
	Kind   string // account, dir, file, remove, stage, swap, ...
	Path   string
	Status string // created, updated, unchanged, refused, removed
	Detail string
}

// Report is the outcome of a run. Commands lists every system command the run issued, in order. They
// were executed only when the run was Live and not a dry run.
type Report struct {
	DryRun   bool
	Live     bool
	Checks   []Check
	Steps    []Step
	Commands []string
	Notes    []string
}

// Changed reports whether any step created, updated or removed something.
func (r Report) Changed() bool {
	for _, s := range r.Steps {
		if s.Status != "unchanged" && s.Status != "refused" {
			return true
		}
	}
	return false
}

// String renders the report.
func (r Report) String() string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("dry run: nothing was changed\n")
	}
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "  [%s] %-18s %s\n", c.Status, c.Name, c.Detail)
		if c.Status != Pass && c.Fix != "" {
			fmt.Fprintf(&b, "         fix: %s\n", c.Fix)
		}
	}
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "  %-9s %-8s %s", s.Status, s.Kind, s.Path)
		if s.Detail != "" {
			fmt.Fprintf(&b, " (%s)", s.Detail)
		}
		b.WriteByte('\n')
	}
	mode := "recorded, not executed"
	if r.Live && !r.DryRun {
		mode = "executed"
	}
	if len(r.Commands) > 0 {
		fmt.Fprintf(&b, "commands (%s):\n", mode)
		for _, c := range r.Commands {
			fmt.Fprintf(&b, "  %s\n", c)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	return b.String()
}

// host carries one run's effects.
type host struct {
	env  Env
	root string
	dir  *os.File
	rep  *Report
	// ids of the service account, known once it exists.
	uid, gid int
	haveIDs  bool
}

func newHost(env Env) *host {
	root := env.Root
	if root == "" {
		root = "/"
	}
	return &host{env: env, root: filepath.Clean(root), rep: &Report{DryRun: env.DryRun, Live: env.Live}}
}

// ValidateRoot refuses a root inside a thesis installation before any host effect.
func ValidateRoot(root string) error {
	if !filepath.IsAbs(root) || IsThesisPath(filepath.ToSlash(filepath.Clean(root))) {
		return refuse("root %s must be absolute and outside the thesis paths", root)
	}
	return nil
}

// real maps a host path below the root.
func (h *host) real(logical string) string { return filepath.Join(h.root, filepath.FromSlash(logical)) }

// effective is true when the run changes files.
func (h *host) writes() bool { return !h.env.DryRun }

func (h *host) step(kind, path, status, detail string) {
	h.rep.Steps = append(h.rep.Steps, Step{Kind: kind, Path: path, Status: status, Detail: detail})
}

func (h *host) note(format string, args ...any) {
	h.rep.Notes = append(h.rep.Notes, fmt.Sprintf(format, args...))
}

func (h *host) exec(name string, args ...string) ([]byte, error) {
	if h.env.Exec != nil {
		return h.env.Exec(name, args...)
	}
	return exec.Command(name, args...).Output()
}

// command records a system command and runs it only for a live, non-dry run.
func (h *host) command(name string, args ...string) error {
	h.rep.Commands = append(h.rep.Commands, strings.Join(append([]string{name}, args...), " "))
	if !h.env.Live || h.env.DryRun {
		return nil
	}
	if out, err := h.exec(name, args...); err != nil {
		return fmt.Errorf("%s: %w %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// chown records the owner change and applies it only for a live, non-dry run.
func (h *host) chown(logical string, o owner) error {
	h.rep.Commands = append(h.rep.Commands, "chown "+o.String()+" "+logical)
	if !h.env.Live || h.env.DryRun {
		return nil
	}
	uid, gid := 0, 0
	if o == ownService {
		if !h.haveIDs {
			return fmt.Errorf("the %s account IDs are not known", ServiceUser)
		}
		uid, gid = h.uid, h.gid
	}
	f, err := h.openFile(logical, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if h.env.Chown != nil {
		return h.env.Chown(fmt.Sprintf("/proc/self/fd/%d", f.Fd()), uid, gid)
	}
	return f.Chown(uid, gid)
}

// wantIDs returns the numeric IDs an owner has on a live host, if known.
func (h *host) wantIDs(o owner) (uid, gid int, known bool) {
	if o == ownRoot {
		return 0, 0, true
	}
	return h.uid, h.gid, h.haveIDs
}

// writeFile replaces real atomically: a temporary file in the same directory, synced, then renamed.
func writeFile(real string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(real), ".tbound-setup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(mode); err == nil {
		if _, err = tmp.Write(data); err == nil {
			err = tmp.Sync()
		}
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), real)
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0
}

// HostInputs is what the static host-input check needs. Paths are real paths below the root. Owners are the
// UIDs that may own a protected input: root, the service account when it exists, and on a test root the
// test process, whose files setup cannot chown.
type HostInputs struct {
	// PolicyDigests is the registered gate-policy digest per project, GatePolicyPaths the installed file.
	PolicyDigests, GatePolicyPaths map[string]string
	GitPath, GitDigest             string
	XattrCalibrationPath           string
	// PublicKey is the registration public key as file content, PublicKeyPath where it was read from.
	PublicKey     []byte
	PublicKeyPath string
	Owners        []uint32
	// Registration is the verified registration the inputs must match.
	Registration RegistrationSummary
}
