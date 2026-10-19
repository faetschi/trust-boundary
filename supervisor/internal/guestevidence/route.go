package guestevidence

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// The E06 exposure matrix (route-matrix.json) has two cells that the prototype sandbox tests cannot
// establish for production: the bash view (a command in the Podman command cell) and direct runtime
// access (a read from the Pi runtime cell). Each needs a denied-read artifact from the guest, bound to
// the frozen commit and to the profile and source of the governed run that made the attempts.
const (
	KindRouteBashView      = "guest-route-bash-view"
	KindRouteDirectRuntime = "guest-route-direct-runtime"
)

// Cell kinds of a route artifact.
const (
	RouteCellCommand   = "podman-command-cell"
	RouteCellPiRuntime = "pi-runtime-cell"
)

// The methods by which the registered target is requested, and the outcomes of an attempt.
const (
	RouteMethodAbsolute  = "absolute"
	RouteMethodTraversal = "traversal"
	RouteMethodSymlink   = "symlink"
	RouteMethodControl   = "control"

	RouteOutcomeDenied = "denied"
	RouteOutcomeRead   = "read"
	// RouteOutcomeMetadata is a stat that returned the metadata of the target: no content was read.
	// Landlock does not mediate stat(2), so a Pi runtime cell shows the metadata of a target it cannot open.
	RouteOutcomeMetadata = "metadata"
)

// RouteMethods are the denied attempts every route artifact carries, in order.
var RouteMethods = []string{RouteMethodAbsolute, RouteMethodTraversal, RouteMethodSymlink}

// RouteRulesetRoots are the paths the cell's Landlock ruleset admits (the D09 template). The registered
// target must lie outside all of them, or a successful read would not be a policy failure.
var RouteRulesetRoots = []string{"/usr", "/lib", "/lib64", "/etc", "/runtime", "/var/lib/tbound/pi-worker", "/proc", "/dev", "/work", "/tmp"}

// RouteDenied is the data of a guest-route-* artifact.
type RouteDenied struct {
	// RunID, ProfileHash and FrozenSourceDigest identify the governed run that made the attempts. The
	// last two must equal the run.json pins.
	RunID              string `json:"run_id"`
	ProfileHash        string `json:"profile_hash"`
	FrozenSourceDigest string `json:"frozen_source_digest"`
	// Target is the registered outside-workspace file the attempts request.
	Target string `json:"target"`
	// Cell identifies the cell the attempts ran in, from the cell's own proof in the receipts.
	Cell RouteCell `json:"cell"`
	// Journal is the head of the verified hash chain the attempts were read from.
	Journal RouteJournal `json:"journal"`
	// TargetFact (Pi runtime cell only) is what root saw of the target from outside the cell: it exists,
	// is a regular file, is readable by other users, and its directory can be listed by them, so only the
	// cell's confinement stands between the cell and the bytes.
	TargetFact *RouteTarget `json:"target_fact,omitempty"`
	// Control reads a file inside the workspace in the same cell: the detector sees a successful read.
	Control RouteAttempt `json:"control"`
	// Attempts are the denied requests of the target, one per method of RouteMethods.
	Attempts []RouteAttempt `json:"attempts"`
}

// RouteCell is the cell identity common to the attempts of one artifact.
type RouteCell struct {
	Kind                  string `json:"kind"`
	ImageDigest           string `json:"image_digest"`
	TemplateDigest        string `json:"template_digest"`
	LandlockRulesetDigest string `json:"landlock_ruleset_digest"`
	// ProductionTemplateDigest (Pi runtime cell only) is the digest of the production cell template: the
	// probe cell's template is that template plus the route_probe member, so the probe ran in the production
	// cell's image, mounts, Landlock ruleset, seccomp profile and user mapping.
	ProductionTemplateDigest string `json:"production_template_digest,omitempty"`
}

// RouteJournal names the audit journal the evidence was read from: the verified record count and the
// hash of its last record.
type RouteJournal struct {
	Records  uint64 `json:"records"`
	HeadHash string `json:"head_hash"`
}

// RouteAttempt is one command and what the cell returned. ResultDigest is the digest of the command
// result (exit code, stdout, stderr) as the receipt states it, and Outcome is the classification the
// producer derived by recomputing that digest for the expected result.
type RouteAttempt struct {
	Method          string   `json:"method"`
	Argv            []string `json:"argv"`
	ExitCode        int      `json:"exit_code"`
	Outcome         string   `json:"outcome"`
	ResultDigest    string   `json:"result_digest"`
	ReceiptDigest   string   `json:"receipt_digest"`
	ContainerID     string   `json:"container_id"`
	JournalSequence uint64   `json:"journal_sequence"`
	// Op and Errno (Pi runtime cell only) name the operation (open, stat or list) and the errno it got.
	Op    string `json:"op,omitempty"`
	Errno string `json:"errno,omitempty"`
}

func routeCellKindFor(kind string) (string, bool) {
	switch kind {
	case KindRouteBashView:
		return RouteCellCommand, true
	case KindRouteDirectRuntime:
		return RouteCellPiRuntime, true
	}
	return "", false
}

// IsRouteKind reports whether kind is one of the route artifact kinds.
func IsRouteKind(kind string) bool {
	_, ok := routeCellKindFor(kind)
	return ok
}

func validateRoute(kind string, raw []byte, e Expect) error {
	wantCell, ok := routeCellKindFor(kind)
	if !ok {
		return fmt.Errorf("kind %s is not a route artifact", kind)
	}
	var r RouteDenied
	if err := DecodeStrict(raw, &r); err != nil {
		return err
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if strings.TrimSpace(r.RunID) == "" {
		add("run_id is empty")
	}
	if e.ProfileHash == "" || e.FrozenSourceDigest == "" {
		add("run.json pins.profile_hash and pins.frozen_source_digest are required to bind the run")
	} else if r.ProfileHash != e.ProfileHash || r.FrozenSourceDigest != e.FrozenSourceDigest {
		add("the run is for profile %q and source %q, the run pins %q and %q", r.ProfileHash, r.FrozenSourceDigest, e.ProfileHash, e.FrozenSourceDigest)
	}
	switch {
	case !path.IsAbs(r.Target) || path.Clean(r.Target) != r.Target:
		add("target %q is not a clean absolute path", r.Target)
	default:
		for _, root := range RouteRulesetRoots {
			if r.Target == root || strings.HasPrefix(r.Target, root+"/") {
				add("target %q is inside the cell's admitted path %s", r.Target, root)
			}
		}
	}
	if r.Cell.Kind != wantCell {
		add("cell kind %q, want %q for %s", r.Cell.Kind, wantCell, kind)
	}
	if !IsDigest(r.Cell.ImageDigest) || !IsDigest(r.Cell.TemplateDigest) || !IsDigest(r.Cell.LandlockRulesetDigest) {
		add("the cell is not identified by image, template and ruleset digests")
	}
	if r.Journal.Records == 0 || !IsHex64(r.Journal.HeadHash) {
		add("the journal head is not stated (%d records, hash %q)", r.Journal.Records, r.Journal.HeadHash)
	}

	if kind == KindRouteDirectRuntime {
		directRuntimeProblems(r, add)
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "; "))
		}
		return nil
	}
	if r.Cell.ProductionTemplateDigest != "" || r.TargetFact != nil || r.Control.Op != "" || r.Control.Errno != "" {
		add("a command cell artifact carries members of the Pi runtime cell artifact")
	}
	seenContainers := map[string]bool{}
	seenReceipts := map[string]bool{}
	checkAttempt := func(a RouteAttempt, wantMethod, wantOutcome string) {
		if a.Method != wantMethod || a.Outcome != wantOutcome {
			add("attempt %s: method %q outcome %q, want %q %q", wantMethod, a.Method, a.Outcome, wantMethod, wantOutcome)
		}
		if len(a.Argv) == 0 || slices.Contains(a.Argv, "") {
			add("attempt %s: no command", wantMethod)
		}
		if wantOutcome == RouteOutcomeDenied && a.ExitCode == 0 {
			add("attempt %s: exit code 0 for a denied read", wantMethod)
		}
		if wantOutcome == RouteOutcomeRead && a.ExitCode != 0 {
			add("attempt %s: the control exited %d", wantMethod, a.ExitCode)
		}
		if !IsDigest(a.ResultDigest) || !IsDigest(a.ReceiptDigest) || strings.TrimSpace(a.ContainerID) == "" || a.JournalSequence == 0 {
			add("attempt %s: result, receipt, container or journal sequence is missing", wantMethod)
		}
		if seenContainers[a.ContainerID] || seenReceipts[a.ReceiptDigest] {
			add("attempt %s repeats the container or receipt of another attempt", wantMethod)
		}
		seenContainers[a.ContainerID], seenReceipts[a.ReceiptDigest] = true, true
		if r.Journal.Records != 0 && a.JournalSequence > r.Journal.Records {
			add("attempt %s: journal sequence %d is beyond the %d verified records", wantMethod, a.JournalSequence, r.Journal.Records)
		}
		if a.Op != "" || a.Errno != "" {
			add("attempt %s: op and errno belong to the Pi runtime cell artifact", wantMethod)
		}
		if wantOutcome == RouteOutcomeDenied && !argvNamesTarget(a, r.Target) {
			add("attempt %s: the command does not request the target %s", wantMethod, r.Target)
		}
	}
	checkAttempt(r.Control, RouteMethodControl, RouteOutcomeRead)
	if len(r.Attempts) != len(RouteMethods) {
		add("%d denied attempts, want one for each of %v", len(r.Attempts), RouteMethods)
	} else {
		for i, a := range r.Attempts {
			checkAttempt(a, RouteMethods[i], RouteOutcomeDenied)
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// argvNamesTarget reports whether the command reaches the target: its text holds the target path, or,
// for a traversal, the target path preceded by parent steps.
func argvNamesTarget(a RouteAttempt, target string) bool {
	for _, arg := range a.Argv {
		if strings.Contains(arg, target) {
			return true
		}
	}
	return false
}

// IsHex64 reports whether s is 64 lowercase hex digits.
func IsHex64(s string) bool { return IsDigest("sha256:" + s) }

// RouteProbeLink is the symlink the symlink attempt makes in the cell's private /tmp.
const RouteProbeLink = "/tmp/route-link"

// RouteProbeCommands returns the shell command of each method (RouteMethodControl included) that a
// route-probe run issues, one `bash -lc` call each. Three of them request the target; the control reads
// a file inside the workspace. The producer recomputes the expected result of each command (a `cat`
// refused with "Permission denied", or the control file's bytes) and accepts an attempt only if the
// receipt's result digest is that result.
func RouteProbeCommands(target, controlPath string) map[string]string {
	return map[string]string{
		RouteMethodControl:   "cat " + controlPath,
		RouteMethodAbsolute:  "cat " + target,
		RouteMethodTraversal: "cat " + RouteTraversalOperand(target),
		RouteMethodSymlink:   "ln -s " + target + " " + RouteProbeLink + "; cat " + RouteProbeLink,
	}
}

// RouteTraversalOperand reaches target from the cell's /work by parent steps.
func RouteTraversalOperand(target string) string { return "../../../.." + target }
