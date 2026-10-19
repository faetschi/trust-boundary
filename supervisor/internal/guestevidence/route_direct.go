package guestevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
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
)

// RouteDirectOps are the operations the probe tries with each method.
var RouteDirectOps = []string{RouteOpOpen, RouteOpStat, RouteOpList}

// RouteTarget is what the verified image of the probe shows of the registered target: the facts that
// decide whether a refusal inside the cell can be file permissions instead of the cell's confinement. They
// are read from the layers of the OCI image whose manifest digest is ImageDigest, which must be the image
// the probe cell ran.
type RouteTarget struct {
	Regular    bool   `json:"regular"`
	Mode       uint32 `json:"mode"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
	Size       int64  `json:"size"`
	ParentMode uint32 `json:"parent_mode"`
	// ImageDigest is the manifest digest of the image the facts were read from.
	ImageDigest string `json:"image_digest"`
	// CellUID and CellGID are the identity the permissions are judged for (the template's cell identity).
	CellUID uint32 `json:"cell_uid"`
	CellGID uint32 `json:"cell_gid"`
	// ResolvedPath is the target after the symlinks of the image are followed; Ancestors are the directories
	// the resolution passed through, the root and every directory above ResolvedPath included.
	ResolvedPath string          `json:"resolved_path"`
	Ancestors    []RouteAncestor `json:"ancestors"`
}

// RouteAncestor is a directory in the image tree above the target.
type RouteAncestor struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
}

// ImageAccess reports the read, write and search bits that ordinary file permissions give the identity
// (cellUID, cellGID, no supplementary groups) on an object with the given mode and owner: the owner class if
// the identity owns it, else the group class if it is in the group, else the other class.
func ImageAccess(mode, uid, gid, cellUID, cellGID uint32) (read, write, search bool) {
	shift := uint(0)
	switch {
	case uid == cellUID:
		shift = 6
	case gid == cellGID:
		shift = 3
	}
	bits := (mode >> shift) & 7
	return bits&4 != 0, bits&2 != 0, bits&1 != 0
}

// targetFactProblems checks the baseline against the probed image and the cell identity.
func targetFactProblems(r RouteDenied, add func(format string, args ...any)) {
	t := r.TargetFact
	switch {
	case t == nil:
		add("what the verified image shows of the target is not stated")
		return
	case !t.Regular || t.Size <= 0:
		add("the target is not a non-empty regular file (size %d): there is nothing to read", t.Size)
	}
	if !IsDigest(t.ImageDigest) || t.ImageDigest != r.Cell.ImageDigest {
		add("the baseline is of image %q, the probe cell ran image %q", t.ImageDigest, r.Cell.ImageDigest)
	}
	if t.CellUID == 0 {
		add("the baseline is judged for uid 0: it must be judged for the unprivileged cell identity")
	}
	if read, _, _ := ImageAccess(t.Mode, t.UID, t.GID, t.CellUID, t.CellGID); !read {
		add("the target (mode %04o owner %d:%d) is not readable by the cell identity %d:%d: a denial could be file permissions, not the cell", t.Mode, t.UID, t.GID, t.CellUID, t.CellGID)
	}
	resolved := t.ResolvedPath
	if !path.IsAbs(resolved) || path.Clean(resolved) != resolved || resolved == "/" {
		add("the resolved target %q is not a clean absolute path", resolved)
		return
	}
	seen := map[string]RouteAncestor{}
	for _, a := range t.Ancestors {
		if !path.IsAbs(a.Path) || path.Clean(a.Path) != a.Path {
			add("ancestor %q is not a clean absolute path", a.Path)
			continue
		}
		if _, dup := seen[a.Path]; dup {
			add("ancestor %q is stated twice", a.Path)
		}
		seen[a.Path] = a
		if _, _, search := ImageAccess(a.Mode, a.UID, a.GID, t.CellUID, t.CellGID); !search {
			add("the cell identity cannot search the directory %s (mode %04o owner %d:%d) above the target", a.Path, a.Mode, a.UID, a.GID)
		}
	}
	parent := path.Dir(resolved)
	for dir := parent; ; dir = path.Dir(dir) {
		if _, ok := seen[dir]; !ok {
			add("the directory %s above the target is not in the baseline", dir)
		}
		if dir == "/" {
			break
		}
	}
	if p, ok := seen[parent]; ok {
		if read, _, _ := ImageAccess(p.Mode, p.UID, p.GID, t.CellUID, t.CellGID); !read {
			add("the cell identity cannot list the directory of the target %s (mode %04o)", parent, p.Mode)
		}
		if p.Mode != t.ParentMode {
			add("the directory of the target is stated with mode %04o and %04o", p.Mode, t.ParentMode)
		}
	}
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
// template derives from, what root saw of the target, and every attempt (open, stat, list by each method)
// of the one proof the cell gave.
func directRuntimeProblems(r RouteDenied, e Expect, add func(format string, args ...any)) {
	if !IsDigest(r.Cell.ProductionTemplateDigest) || r.Cell.ProductionTemplateDigest == r.Cell.TemplateDigest {
		add("the production template digest is missing or equals the probe template's: the probe cell is not shown to be the production cell plus the probe")
	}
	if e.ContainmentProfileDigest == "" || r.Cell.ProductionTemplateDigest != e.ContainmentProfileDigest {
		add("the production template %q is not the one the run pins (containment_profile_digest %q)", r.Cell.ProductionTemplateDigest, e.ContainmentProfileDigest)
	}
	if !IsDigest(r.ProbeProfileDigest) || r.ProbeProfileDigest == r.ProfileHash {
		add("the probe profile digest is missing or equals the run's profile: the probe ran under the production profile plus the probe template")
	}
	if e.FrozenSourceDigest == "" || r.ProfileSourceDigest != e.FrozenSourceDigest || r.ProfileSourceDigest != r.FrozenSourceDigest {
		add("the source of the journaled probe profile %q is not the run's frozen source %q", r.ProfileSourceDigest, e.FrozenSourceDigest)
	}
	targetFactProblems(r, add)

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
	case len(c.Argv) != 2 || c.Argv[0] != RouteOpOpen || !strings.HasPrefix(c.Argv[1], "/work/"):
		add("the control read does not open a file inside /work: %q", c.Argv)
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
