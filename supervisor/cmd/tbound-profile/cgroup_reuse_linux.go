//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type cgroupReuseObservation struct {
	Name       string `json:"name"`
	Phase      string `json:"phase"`
	PID        int    `json:"pid"`
	Exited     bool   `json:"exited"`
	Signal     string `json:"signal,omitempty"`
	ExitStatus int    `json:"exit_status,omitempty"`
	WaitError  string `json:"wait_error,omitempty"`
}

type cgroupReuseReport struct {
	Result         string                   `json:"result"`
	Advisory       bool                     `json:"advisory"`
	Parent         string                   `json:"parent,omitempty"`
	Observations   []cgroupReuseObservation `json:"observations,omitempty"`
	ReusePopulated bool                     `json:"reuse_populated"`
	Error          string                   `json:"error,omitempty"`
}

func runCgroupReuseCheck(args []string, stdout, stderr io.Writer) error {
	return runCgroupReuseCheckWithProbe(args, stdout, stderr, probeCgroupReuse)
}

// The callback keeps confirmation parsing testable without touching cgroupfs.
func runCgroupReuseCheckWithProbe(args []string, stdout, stderr io.Writer, probe func() (cgroupReuseReport, error)) error {
	flags := flag.NewFlagSet("tbound-profile cgroup-reuse-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	confirmed := flags.Bool("confirm-disposable-cgroup", false, "authorize creating two disposable child cgroups and killing only their fixed sleep processes; requires an existing writable delegated scope")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !*confirmed {
		return errors.New("cgroup reuse is unverified: pass --confirm-disposable-cgroup inside an operator-authorized writable delegated user scope; no system configuration is changed")
	}
	report, probeErr := probe()
	report.Advisory = true
	if probeErr != nil {
		if report.Result != "incompatible" {
			report.Result = "unverified"
		}
		report.Error = probeErr.Error()
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return errors.Join(probeErr, err)
	}
	return probeErr
}

type cgroupReuseChild struct {
	name   string
	cmd    *exec.Cmd
	done   chan error
	waited bool
	err    error
}

func (c *cgroupReuseChild) poll() bool {
	if !c.waited {
		select {
		case c.err = <-c.done:
			c.waited = true
		default:
		}
	}
	return c.waited
}

func (c *cgroupReuseChild) observation(phase string) cgroupReuseObservation {
	o := cgroupReuseObservation{Name: c.name, Phase: phase, PID: c.cmd.Process.Pid, Exited: c.poll()}
	if c.waited {
		if c.err != nil {
			o.WaitError = c.err.Error()
		}
		if s, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			o.ExitStatus = s.ExitStatus()
			if s.Signaled() {
				o.Signal = s.Signal().String()
			}
		}
	}
	return o
}

func (c *cgroupReuseChild) waitForExit() error {
	if c.poll() {
		return nil
	}
	select {
	case c.err = <-c.done:
		c.waited = true
		return nil
	case <-time.After(time.Second):
		return fmt.Errorf("%s did not exit within 1s", c.name)
	}
}

func cleanupCgroupReuse(children []*cgroupReuseChild, fds []*os.File, dirs []string) error {
	var cleanupErr error
	for _, c := range children {
		if !c.poll() {
			if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("kill %s: %w", c.name, err))
			}
		}
		cleanupErr = errors.Join(cleanupErr, c.waitForExit())
	}
	for _, f := range fds {
		cleanupErr = errors.Join(cleanupErr, f.Close())
	}
	for _, d := range dirs {
		cleanupErr = errors.Join(cleanupErr, os.Remove(d))
	}
	return cleanupErr
}

// Reads and kill writes use the held cgroup descriptor, never a substituted path.
func cgroupReuseControl(dir *os.File, name, value string, write bool) ([]byte, error) {
	mode := unix.O_RDONLY
	if write {
		mode = unix.O_WRONLY
	}
	fd, err := unix.Openat(int(dir.Fd()), name, mode|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if write {
		_, err = f.WriteString(value)
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, 4096))
}

func cgroupReusePopulated(dir *os.File) (bool, error) {
	b, err := cgroupReuseControl(dir, "cgroup.events", "", false)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "populated 0" {
			return false, nil
		}
		if line == "populated 1" {
			return true, nil
		}
	}
	return false, fmt.Errorf("cgroup.events has no valid populated key: %q", b)
}

func cgroupReuseParent() (string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	var rel string
	for _, line := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			rel = p
		}
	}
	if rel == "" || rel == "/" || !filepath.IsAbs(rel) || filepath.Clean(rel) != rel {
		return "", errors.New("requires a non-root cgroup2 membership in an already delegated user scope")
	}
	b, err = os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		f := strings.Fields(before)
		if ok && strings.HasPrefix(after, "cgroup2 ") && len(f) > 4 && f[3] == "/" {
			return filepath.Join(unescapeMount(f[4]), rel), nil
		}
	}
	return "", errors.New("requires a visible cgroup2 mount rooted at /")
}

// No version allowlist: the result is only the observed behavior in this scope.
func cgroupReuseOutcome(reused, fresh, outside cgroupReuseObservation, populated bool) error {
	if fresh.Exited || outside.Exited {
		return errors.New("comparison unverified: a fresh or outside control exited; inspect child wait statuses")
	}
	if reused.Exited {
		if reused.Signal == syscall.SIGKILL.String() && !populated {
			return errors.New("incompatible cgroup reuse: the post-kill clone received SIGKILL while fresh and outside controls remained alive; use an operator-qualified kernel with the cgroup reuse fix, then rerun this check")
		}
		return errors.New("comparison unverified: reused-target child exited unexpectedly; inspect child wait status")
	}
	if !populated {
		return errors.New("comparison unverified: reused-target child appears alive but its cgroup is empty")
	}
	return nil
}

func probeCgroupReuse() (report cgroupReuseReport, err error) {
	report.Result, report.Advisory = "unverified", true
	if os.Geteuid() <= 0 || os.Getegid() <= 0 {
		return report, errors.New("run cgroup-reuse-check as a nonroot user with positive uid/gid in an existing authorized delegated scope; this command never invokes sudo or creates a systemd scope")
	}
	parent, err := cgroupReuseParent()
	if err != nil {
		return report, err
	}
	report.Parent = parent
	p, err := os.Open(parent)
	if err != nil {
		return report, err
	}
	defer p.Close()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(p.Fd()), &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return report, fmt.Errorf("not a verified cgroup2 parent: %v", err)
	}
	kind, err := cgroupReuseControl(p, "cgroup.type", "", false)
	if err != nil || strings.TrimSpace(string(kind)) != "domain" {
		return report, fmt.Errorf("requires a delegated domain cgroup: type=%q err=%v", kind, err)
	}
	var children []*cgroupReuseChild
	var dirs []string
	var fds []*os.File
	defer func() {
		if cleanupErr := cleanupCgroupReuse(children, fds, dirs); cleanupErr != nil {
			report.Result = "unverified"
			err = errors.Join(err, fmt.Errorf("disposable cgroup cleanup failed: %w", cleanupErr))
		}
	}()
	makeGroup := func(label string) (*os.File, error) {
		path, e := os.MkdirTemp(parent, "tbound-reuse-"+label+"-")
		if e != nil {
			return nil, fmt.Errorf("cgroup delegation unverified: cannot create a disposable child: %w; run within an operator-authorized writable delegated scope", e)
		}
		dirs = append(dirs, path)
		fd, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if e != nil {
			return nil, e
		}
		f := os.NewFile(uintptr(fd), path)
		fds = append(fds, f)
		return f, nil
	}
	reusedFD, err := makeGroup("target")
	if err != nil {
		return report, err
	}
	freshFD, err := makeGroup("control")
	if err != nil {
		return report, err
	}
	start := func(name string, cg *os.File) (*cgroupReuseChild, error) {
		cmd := exec.Command("/bin/sleep", "30")
		cmd.Env = []string{"LC_ALL=C"}
		if cg != nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(cg.Fd())}
		}
		if e := cmd.Start(); e != nil {
			return nil, fmt.Errorf("start %s: %w; requires /bin/sleep and clone3 into the delegated cgroup", name, e)
		}
		c := &cgroupReuseChild{name: name, cmd: cmd, done: make(chan error, 1)}
		children = append(children, c)
		go func() { c.done <- cmd.Wait() }()
		return c, nil
	}
	outside, err := start("outside-control", nil)
	if err != nil {
		return report, err
	}
	fresh, err := start("fresh-control", freshFD)
	if err != nil {
		return report, err
	}
	first, err := start("before-first-kill", reusedFD)
	if err != nil {
		return report, err
	}
	time.Sleep(time.Second) // One baseline window; no re-spawn or retry.
	for _, c := range children {
		o := c.observation("baseline")
		report.Observations = append(report.Observations, o)
		if o.Exited {
			return report, fmt.Errorf("baseline unverified: %s exited before the first cgroup kill", c.name)
		}
	}
	for _, f := range fds {
		if live, e := cgroupReusePopulated(f); e != nil || !live {
			return report, fmt.Errorf("baseline cgroup membership unverified: populated=%v err=%v", live, e)
		}
	}
	if _, err := cgroupReuseControl(reusedFD, "cgroup.kill", "1", true); err != nil {
		return report, fmt.Errorf("disposable cgroup.kill unavailable: %w", err)
	}
	if err := first.waitForExit(); err != nil {
		return report, err
	}
	o := first.observation("first-kill")
	report.Observations = append(report.Observations, o)
	if o.Signal != syscall.SIGKILL.String() {
		return report, errors.New("first cgroup kill did not produce SIGKILL; comparison unverified")
	}
	for deadline := time.Now().Add(time.Second); ; {
		live, e := cgroupReusePopulated(reusedFD)
		if e != nil {
			return report, e
		}
		if !live {
			break
		}
		if !time.Now().Before(deadline) {
			return report, errors.New("disposable target remained populated 1s after its first kill")
		}
		time.Sleep(5 * time.Millisecond)
	}
	second, err := start("after-first-kill", reusedFD)
	if err != nil {
		return report, err
	}
	time.Sleep(time.Second) // Single matched reuse window; never retry to pass.
	reusedObs, freshObs, outsideObs := second.observation("after-reuse"), fresh.observation("after-reuse"), outside.observation("after-reuse")
	report.Observations = append(report.Observations, reusedObs, freshObs, outsideObs)
	report.ReusePopulated, err = cgroupReusePopulated(reusedFD)
	if err != nil {
		return report, err
	}
	if err := cgroupReuseOutcome(reusedObs, freshObs, outsideObs, report.ReusePopulated); err != nil {
		if !freshObs.Exited && !outsideObs.Exited && reusedObs.Signal == syscall.SIGKILL.String() && !report.ReusePopulated {
			report.Result = "incompatible"
		}
		return report, err
	}
	report.Result = "pass"
	return report, nil
}
