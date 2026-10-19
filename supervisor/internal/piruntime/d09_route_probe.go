package piruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"syscall"

	"tbound/supervisor/internal/cellview"
)

// The direct-runtime route probe (E06): a read of the registered outside-workspace target made from
// INSIDE the Pi runtime cell, by the cell's own process, not through the declared tool surface.
//
// It is a mode of the signed cell entrypoint, selected by the signed cell template: a template with a
// route_probe member is a different template (a different digest, signed and admitted on its own), so a
// production profile never selects it. The host passes the probe to the entrypoint as the argument
// D09RouteProbeArg and as environment variables derived from the template; the entrypoint accepts the
// argument only together with the template flag (TemplateFlag) and refuses the variables without the
// argument. In the probe mode the entrypoint does everything it does for a Pi cell (measure the TCB,
// rebind the consumer mounts, apply the same Landlock ruleset), makes the attempts below, reports them in
// the TCB proof, and exits without starting Pi.
//
// Before the attempts the probe measures the path to the target from inside the cell (cellview.Measure): the
// lstat result, owner, mode and POSIX ACL presence of every component of the registered path, followed the way
// the kernel follows it, with the identity of the probe process and its security module context. The cell sees
// its own mounts, so the facts are those of the cell and not of the image it was built from.
//
// The probe only reads: it opens the paths O_RDONLY and reads one byte, stats them and opens a
// directory; the measurement is lstat, readlink and reads of attributes. D09RouteProbeFS has no operation that
// writes, and a test keeps the implementation to it.

const (
	// D09RouteProbeName is the one registered probe.
	D09RouteProbeName = "direct-runtime/v1"
	// D09RouteProbeArg is the entrypoint argument that selects the probe mode.
	D09RouteProbeArg = "--route-probe=" + D09RouteProbeName

	d09RouteProbeFlagLabel = "tbound-d09-route-probe-template/v1"

	// D09RouteProbeMethodControl names the control read inside the workspace.
	D09RouteProbeMethodControl = "control"
	// The ops of an attempt.
	D09RouteProbeOpOpen = "open"
	D09RouteProbeOpStat = "stat"
	D09RouteProbeOpList = "list"
)

// D09RouteProbeMethods are the ways the probe names the target, in order: the absolute path, a parent
// traversal from the cell's /work, and the kernel's own symbolic link to the root of the process
// (/proc/self/root; the probe makes no symbolic link of its own, because it writes nothing).
var D09RouteProbeMethods = []string{"absolute", "traversal", "symlink"}

// D09RouteProbeOps are the operations tried by each method, in order.
var D09RouteProbeOps = []string{D09RouteProbeOpOpen, D09RouteProbeOpStat, D09RouteProbeOpList}

// D09RouteProbe is the configuration of the probe, a member of the signed cell template.
type D09RouteProbe struct {
	Name string `json:"name"`
	// Target is the registered outside-workspace file. It must lie outside every path of the template's
	// Landlock ruleset.
	Target string `json:"target"`
	// Control is a file the cell may read (inside its ruleset), read in the same run.
	Control string `json:"control"`
}

// Validate checks the configuration against the paths of the template's Landlock ruleset.
func (p D09RouteProbe) Validate(readOnlyPaths, readWritePaths []string) error {
	if p.Name != D09RouteProbeName {
		return fmt.Errorf("route probe %q is not the registered %q", p.Name, D09RouteProbeName)
	}
	for name, value := range map[string]string{"target": p.Target, "control": p.Control} {
		if !path.IsAbs(value) || path.Clean(value) != value || value == "/" || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("route probe %s %q is not a clean absolute path", name, value)
		}
	}
	inside := func(roots []string, p string) bool {
		for _, root := range roots {
			if p == root || strings.HasPrefix(p, root+"/") {
				return true
			}
		}
		return false
	}
	all := slices.Concat(readOnlyPaths, readWritePaths)
	if inside(all, p.Target) {
		return fmt.Errorf("route probe target %q lies inside the ruleset: a read of it would not be a policy failure", p.Target)
	}
	for _, root := range all {
		if strings.HasPrefix(root, p.Target+"/") {
			return fmt.Errorf("route probe target %q is a parent of the ruleset path %s", p.Target, root)
		}
	}
	if !inside(all, p.Control) {
		return fmt.Errorf("route probe control %q lies outside the ruleset: the cell could not read it", p.Control)
	}
	return nil
}

// TemplateFlag is the template flag the host passes with the probe argument. It is derived from the
// digest of the signed template and the probe configuration, so the entrypoint takes the probe mode only
// for exactly the probe that template names.
func (p D09RouteProbe) TemplateFlag(templateDigest string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{d09RouteProbeFlagLabel, p.Name, p.Target, p.Control, templateDigest}, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// D09RouteProbeOperand is the path an attempt requests: the target named by the method, or for the list
// op the directory that holds it.
func D09RouteProbeOperand(method, op, target string) (string, error) {
	var operand string
	switch method {
	case "absolute":
		operand = target
	case "traversal":
		operand = "../../../.." + target
	case "symlink":
		operand = "/proc/self/root" + target
	default:
		return "", fmt.Errorf("unknown route probe method %q", method)
	}
	switch op {
	case D09RouteProbeOpOpen, D09RouteProbeOpStat:
		return operand, nil
	case D09RouteProbeOpList:
		return path.Dir(operand), nil
	}
	return "", fmt.Errorf("unknown route probe op %q", op)
}

// D09RouteProbeAttempt is one attempt and its errno (0 when it succeeded, -1 when the failure carried no
// errno). The field names are short because the result rides in the bounded TCB proof line.
type D09RouteProbeAttempt struct {
	Method string `json:"m"`
	Op     string `json:"o"`
	Errno  int    `json:"e"`
}

// D09RouteProbeResult is what the probe reports in the TCB proof.
type D09RouteProbeResult struct {
	Name        string                 `json:"name"`
	Target      string                 `json:"target"`
	Control     string                 `json:"control"`
	ControlRead D09RouteProbeAttempt   `json:"control_read"`
	Attempts    []D09RouteProbeAttempt `json:"attempts"`
	// View is what the probe process saw along the path to the target, measured before the attempts.
	View cellview.View `json:"view"`
}

// D09RouteProbeFS is the whole file system surface of the probe: reads only. The embedded surface is the
// measurement of the path to the target (lstat, ACL attributes, identity).
type D09RouteProbeFS interface {
	cellview.Source
	// OpenRead opens path O_RDONLY, reads at most one byte, discards it and closes the file.
	OpenRead(path string) error
	// Stat reads the metadata of path.
	Stat(path string) error
	// List opens path as a directory and reads one batch of entries.
	List(path string) error
}

func probeErrno(err error) int {
	if err == nil {
		return 0
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return int(errno)
	}
	return -1
}

// RunD09RouteProbe makes the control read and the attempts, in the order of D09RouteProbeMethods and
// D09RouteProbeOps, and reports the errno of each.
func RunD09RouteProbe(fsys D09RouteProbeFS, p D09RouteProbe) D09RouteProbeResult {
	result := D09RouteProbeResult{Name: p.Name, Target: p.Target, Control: p.Control}
	result.View = cellview.Measure(fsys, p.Target)
	result.ControlRead = D09RouteProbeAttempt{Method: D09RouteProbeMethodControl, Op: D09RouteProbeOpOpen, Errno: probeErrno(fsys.OpenRead(p.Control))}
	for _, method := range D09RouteProbeMethods {
		for _, op := range D09RouteProbeOps {
			operand, err := D09RouteProbeOperand(method, op, p.Target)
			if err != nil {
				panic(err) // the method and op lists above are the registered ones
			}
			var attemptErr error
			switch op {
			case D09RouteProbeOpOpen:
				attemptErr = fsys.OpenRead(operand)
			case D09RouteProbeOpStat:
				attemptErr = fsys.Stat(operand)
			case D09RouteProbeOpList:
				attemptErr = fsys.List(operand)
			}
			result.Attempts = append(result.Attempts, D09RouteProbeAttempt{Method: method, Op: op, Errno: probeErrno(attemptErr)})
		}
	}
	return result
}

// CheckShape requires the result to be the complete result of the probe p: every attempt, in order.
func (r D09RouteProbeResult) CheckShape(p D09RouteProbe) error {
	if r.Name != p.Name || r.Target != p.Target || r.Control != p.Control {
		return fmt.Errorf("the probe result is for %q %q %q, not %q %q %q", r.Name, r.Target, r.Control, p.Name, p.Target, p.Control)
	}
	if r.ControlRead.Method != D09RouteProbeMethodControl || r.ControlRead.Op != D09RouteProbeOpOpen {
		return errors.New("the probe result has no control read")
	}
	if len(r.View.Facts) == 0 && r.View.Err == "" {
		return errors.New("the probe result carries no measurement of the path to the target")
	}
	if len(r.Attempts) != len(D09RouteProbeMethods)*len(D09RouteProbeOps) {
		return fmt.Errorf("the probe result has %d attempts, want %d", len(r.Attempts), len(D09RouteProbeMethods)*len(D09RouteProbeOps))
	}
	i := 0
	for _, method := range D09RouteProbeMethods {
		for _, op := range D09RouteProbeOps {
			if a := r.Attempts[i]; a.Method != method || a.Op != op {
				return fmt.Errorf("probe attempt %d is %s %s, want %s %s", i, a.Method, a.Op, method, op)
			}
			i++
		}
	}
	return nil
}
