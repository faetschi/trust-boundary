package guestevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
)

// The direct-runtime artifact: the attempts of one probe run inside the Pi runtime cell. Unlike the
// command cell's attempts (one container and one receipt each), they are all stated against the one TCB
// proof the cell gave: its container, the digest of the proof and the journal record that holds it.

// The operations of a direct-runtime attempt, tried by each method in this order.
const (
	RouteOpOpen = "open"
	RouteOpStat = "stat"
	RouteOpList = "list"

	// RouteDirectWorkspace is where the Pi cell mounts the workspace (piruntime.PiWorkerCellDefaultWorkingDirectory);
	// /work is only the command cell's mount.
	RouteDirectWorkspace = "/var/lib/tbound/pi-worker"
)

// RouteDirectOps are the operations the probe tries with each method.
var RouteDirectOps = []string{RouteOpOpen, RouteOpStat, RouteOpList}

// DirectRuntimeLimits are what a direct-runtime artifact does not establish. The artifact states them, and
// the validator requires them to be stated, so that a reader of the artifact alone does not take it for more.
var DirectRuntimeLimits = []string{
	"A denial is attributed to confinement by exclusion: ordinary file permissions, computed from what the probe measured inside the cell " +
		"(owner, mode and absence of POSIX ACLs along the walked absolute name, which holds no symbolic link: the kernel's own refusal to follow a link, " +
		"fs.protected_symlinks, is not a file permission), would have allowed the open and the listing. The traversal name " +
		"(from the working directory of the probe) and the /proc/self/root name (a kernel link, not part of the measured walk) are only attempts that " +
		"are denied or not; they are not walked separately and give no baseline.",
	"The confinement that refused the access is not shown to be Landlock alone: the security module context of the probe (view.lsm; AppArmor or " +
		"another module) and the seccomp filter can refuse with the same EACCES or EPERM. The artifact does not tell these apart.",
	"The path is measured once, before the attempts; a change of the cell's file system between the measurement and the attempts is not excluded.",
}

// RouteDirectObservation is one attempt of the probe as the cell reported it: the errno of the operation,
// 0 when it succeeded and -1 when the failure carried no errno.
type RouteDirectObservation struct {
	Method string
	Op     string
	Errno  int
}

const (
	errnoEPERM  = 1
	errnoENOENT = 2
	errnoEACCES = 13
)

// RouteErrnoName names the Linux errno codes an attempt can end with. Other codes are named by number.
func RouteErrnoName(errno int) string {
	switch errno {
	case errnoEPERM:
		return "EPERM"
	case errnoENOENT:
		return "ENOENT"
	case errnoEACCES:
		return "EACCES"
	case 20:
		return "ENOTDIR"
	case 21:
		return "EISDIR"
	}
	return fmt.Sprintf("errno-%d", errno)
}

func errnoText(errno int) string {
	if errno == 0 {
		return ""
	}
	return RouteErrnoName(errno)
}

// RouteDirectOperand is the path an attempt requests: the target as the method names it, or for the list
// operation the directory that holds it. internal/piruntime holds the same rule for the probe itself; a
// test keeps the two equal.
func RouteDirectOperand(method, op, target string) (string, error) {
	var operand string
	switch method {
	case RouteMethodAbsolute:
		operand = target
	case RouteMethodTraversal:
		operand = RouteTraversalOperand(target)
	case RouteMethodSymlink:
		operand = "/proc/self/root" + target
	default:
		return "", fmt.Errorf("unknown method %q", method)
	}
	switch op {
	case RouteOpOpen, RouteOpStat:
		return operand, nil
	case RouteOpList:
		return path.Dir(operand), nil
	}
	return "", fmt.Errorf("unknown op %q", op)
}

// RouteDirectResultDigest is the digest of one attempt's result: the operation, what it requested and the
// errno it got. The validator recomputes it, so an attempt cannot state an errno its digest does not.
func RouteDirectResultDigest(op, operand string, errno int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("tbound-route-direct-attempt/v1\x00%s\x00%s\x00%d", op, operand, errno)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// directOutcome classifies the errno of an operation: success is a read for open and list and metadata
// for stat, EACCES and EPERM are a denial, anything else is not a policy denial ("other").
func directOutcome(op string, errno int) string {
	switch {
	case errno == 0 && op == RouteOpStat:
		return RouteOutcomeMetadata
	case errno == 0:
		return RouteOutcomeRead
	case errno == errnoEACCES || errno == errnoEPERM:
		return RouteOutcomeDenied
	}
	return "other"
}

// DirectRuntimeAttempts builds the control and the attempts of a direct-runtime artifact from the
// observations the probe in the cell reported. The observations must be exactly one for each method and
// operation, in order. An attempt that got through (errno 0 for open or list) is built as a "read" and the
// validator refuses the artifact.
func DirectRuntimeAttempts(target, controlPath, containerID, proofDigest string, sequence uint64, controlErrno int, observed []RouteDirectObservation) (RouteAttempt, []RouteAttempt, error) {
	attempt := func(method, op, operand string, errno int) RouteAttempt {
		return RouteAttempt{Method: method, Op: op, Argv: []string{op, operand}, ExitCode: errno, Outcome: directOutcome(op, errno), Errno: errnoText(errno),
			ResultDigest: RouteDirectResultDigest(op, operand, errno), ReceiptDigest: proofDigest, ContainerID: containerID, JournalSequence: sequence}
	}
	control := attempt(RouteMethodControl, RouteOpOpen, controlPath, controlErrno)
	if len(observed) != len(RouteMethods)*len(RouteDirectOps) {
		return control, nil, fmt.Errorf("%d observations, want %d", len(observed), len(RouteMethods)*len(RouteDirectOps))
	}
	var attempts []RouteAttempt
	i := 0
	for _, method := range RouteMethods {
		for _, op := range RouteDirectOps {
			o := observed[i]
			if o.Method != method || o.Op != op {
				return control, nil, fmt.Errorf("observation %d is %s %s, want %s %s", i, o.Method, o.Op, method, op)
			}
			operand, err := RouteDirectOperand(method, op, target)
			if err != nil {
				return control, nil, err
			}
			attempts = append(attempts, attempt(method, op, operand, o.Errno))
			i++
		}
	}
	return control, attempts, nil
}

// directRuntimeProblems checks what is specific to the Pi runtime cell: the production template the probe
// template derives from, what the probe measured inside the cell, and every attempt (open, stat, list by each method)
// of the one proof the cell gave.
func directRuntimeProblems(r RouteDenied, add func(format string, args ...any)) {
	if !IsDigest(r.Cell.ProductionTemplateDigest) || r.Cell.ProductionTemplateDigest == r.Cell.TemplateDigest {
		add("the production template digest is missing or equals the probe template's: the probe cell is not shown to be the production cell plus the probe")
	}
	viewProblems(r, add)

	shared := func(a RouteAttempt, what string) {
		if !IsDigest(a.ReceiptDigest) || strings.TrimSpace(a.ContainerID) == "" || a.JournalSequence == 0 {
			add("%s: proof, container or journal sequence is missing", what)
		}
		if a.ReceiptDigest != r.Control.ReceiptDigest || a.ContainerID != r.Control.ContainerID || a.JournalSequence != r.Control.JournalSequence {
			add("%s: not from the one proof the control read is from", what)
		}
		if r.Journal.Records != 0 && a.JournalSequence > r.Journal.Records {
			add("%s: journal sequence %d is beyond the %d verified records", what, a.JournalSequence, r.Journal.Records)
		}
	}
	c := r.Control
	switch {
	case c.Method != RouteMethodControl || c.Op != RouteOpOpen || c.Outcome != RouteOutcomeRead || c.ExitCode != 0 || c.Errno != "":
		add("the control read (method %q op %q outcome %q errno %d %q) is not a successful open: the detector would see no read", c.Method, c.Op, c.Outcome, c.ExitCode, c.Errno)
	case len(c.Argv) != 2 || c.Argv[0] != RouteOpOpen || !strings.HasPrefix(c.Argv[1], RouteDirectWorkspace+"/"):
		add("the control read does not open a file inside the Pi cell's workspace %s: %q", RouteDirectWorkspace, c.Argv)
	case c.ResultDigest != RouteDirectResultDigest(RouteOpOpen, c.Argv[1], 0):
		add("the control read's result digest is not that of its open and errno")
	}
	shared(c, "control")

	if len(r.Attempts) != len(RouteMethods)*len(RouteDirectOps) {
		add("%d attempts, want one for each operation %v of each method %v", len(r.Attempts), RouteDirectOps, RouteMethods)
		return
	}
	i := 0
	for _, method := range RouteMethods {
		for _, op := range RouteDirectOps {
			a := r.Attempts[i]
			i++
			what := method + " " + op
			if a.Method != method || a.Op != op {
				add("attempt %d is %s %s, want %s", i-1, a.Method, a.Op, what)
				continue
			}
			operand, err := RouteDirectOperand(method, op, r.Target)
			if err != nil || len(a.Argv) != 2 || a.Argv[0] != op || a.Argv[1] != operand {
				add("attempt %s: requests %q, want %s %q", what, a.Argv, op, operand)
				continue
			}
			if a.ResultDigest != RouteDirectResultDigest(op, operand, a.ExitCode) || a.Errno != errnoText(a.ExitCode) {
				add("attempt %s: the result digest or the errno name %q is not that of errno %d", what, a.Errno, a.ExitCode)
			}
			switch want := directOutcome(op, a.ExitCode); {
			case a.Outcome != want:
				add("attempt %s: outcome %q, errno %d gives %q", what, a.Outcome, a.ExitCode, want)
			case want == RouteOutcomeDenied, want == RouteOutcomeMetadata:
			case want == RouteOutcomeRead:
				add("attempt %s succeeded: the cell read the outside target", what)
			default:
				add("attempt %s ended with %s, not a policy denial", what, RouteErrnoName(a.ExitCode))
			}
			shared(a, what)
		}
	}
}

// viewProblems requires what the probe measured inside the cell to show that ordinary file permissions
// would have allowed the access, so that the refusals are attributable to the cell's confinement: the walk
// of the target, the cell identity, the permission on every directory the walk searched and on the file,
// and no POSIX ACL. It also requires the artifact to state its limits.
func viewProblems(r RouteDenied, add func(format string, args ...any)) {
	if r.View == nil {
		add("what the probe saw of the target inside the cell is not stated")
	} else {
		for _, p := range r.View.Problems(r.Target, r.Cell.CellUID, r.Cell.CellGID, RouteRulesetRoots) {
			add("%s", p)
		}
	}
	if !slices.Equal(r.Limits, DirectRuntimeLimits) {
		add("the artifact does not state the limits of the direct-runtime evidence")
	}
}
