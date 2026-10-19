package guestevidence

import (
	"strings"
	"syscall"
	"testing"
)

const goodControlPath = "/work/src/release.txt"

// goodObservations is what the probe in the Pi runtime cell reports for a cell that denies the target:
// open and list are refused, stat (which Landlock does not mediate) shows the metadata.
func goodObservations() []RouteDirectObservation {
	var obs []RouteDirectObservation
	for _, method := range RouteMethods {
		for _, op := range RouteDirectOps {
			errno := int(syscall.EACCES)
			if op == RouteOpStat {
				errno = 0
			}
			obs = append(obs, RouteDirectObservation{Method: method, Op: op, Errno: errno})
		}
	}
	return obs
}

func goodDirectRuntimeWith(obs []RouteDirectObservation, controlErrno int) RouteDenied {
	e := expect()
	control, attempts, err := DirectRuntimeAttempts("/home/node/.profile", goodControlPath, "tbound-d09-cell-1", digestOf("proof"), 5, controlErrno, obs)
	if err != nil {
		panic(err)
	}
	return RouteDenied{RunID: "run-1", ProfileHash: e.ProfileHash, FrozenSourceDigest: e.FrozenSourceDigest, Target: "/home/node/.profile",
		Cell: RouteCell{Kind: RouteCellPiRuntime, ImageDigest: digestOf("image"), TemplateDigest: digestOf("probe template"),
			LandlockRulesetDigest: digestOf("ruleset"), ProductionTemplateDigest: digestOf("production template")},
		Journal:    RouteJournal{Records: 9, HeadHash: strings.Repeat("a", 64)},
		TargetFact: &RouteTarget{Regular: true, Mode: 0o644, UID: 1000, GID: 1000, Size: 807, ParentMode: 0o755},
		Control:    control, Attempts: attempts}
}

func goodDirectRuntime() RouteDenied { return goodDirectRuntimeWith(goodObservations(), 0) }

func TestDirectRuntimeArtifactStatesTheDeniedReadsOfOneCell(t *testing.T) {
	e := expect()
	d := goodDirectRuntime()
	if len(d.Attempts) != 9 {
		t.Fatalf("%d attempts", len(d.Attempts))
	}
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, d), e); err != nil {
		t.Fatalf("a good direct-runtime artifact is refused: %v", err)
	}
	// The cell, the proof and the journal record are one for every attempt.
	for _, a := range append([]RouteAttempt{d.Control}, d.Attempts...) {
		if a.ContainerID != "tbound-d09-cell-1" || a.ReceiptDigest != digestOf("proof") || a.JournalSequence != 5 {
			t.Errorf("attempt %s %s is not from the one proof: %+v", a.Method, a.Op, a)
		}
	}
	if d.Attempts[0].Argv[0] != "open" || d.Attempts[2].Argv[1] != "/home/node" || d.Attempts[5].Argv[1] != "../../../../home/node" ||
		d.Attempts[7].Argv[1] != "/proc/self/root/home/node/.profile" {
		t.Errorf("operands: %v %v %v %v", d.Attempts[0].Argv, d.Attempts[2].Argv, d.Attempts[5].Argv, d.Attempts[7].Argv)
	}
}

func TestDirectRuntimeArtifactRefusesAReadAndAnythingThatIsNotAPolicyDenial(t *testing.T) {
	e := expect()
	withErrno := func(method, op string, errno int) []RouteDirectObservation {
		obs := goodObservations()
		for i := range obs {
			if obs[i].Method == method && obs[i].Op == op {
				obs[i].Errno = errno
			}
		}
		return obs
	}
	// Produced from observations in which one attempt got through: the artifact is made but not valid.
	for name, obs := range map[string][]RouteDirectObservation{
		"the absolute open succeeded":    withErrno("absolute", "open", 0),
		"the traversal open succeeded":   withErrno("traversal", "open", 0),
		"the symlink open succeeded":     withErrno("symlink", "open", 0),
		"the listing succeeded":          withErrno("absolute", "list", 0),
		"the target is absent (ENOENT)":  withErrno("absolute", "open", int(syscall.ENOENT)),
		"the listing is not a directory": withErrno("symlink", "list", int(syscall.ENOTDIR)),
		"an unknown failure":             withErrno("absolute", "open", -1),
	} {
		d := goodDirectRuntimeWith(obs, 0)
		if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, d), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if d := goodDirectRuntimeWith(goodObservations(), int(syscall.EACCES)); Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, d), e) == nil {
		t.Error("a control read that was denied was accepted: the detector would see no successful read")
	}
	// The stat of a denied cell may be denied as well.
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime,
		goodDirectRuntimeWith(withErrno("absolute", "stat", int(syscall.EACCES)), 0)), e); err != nil {
		t.Errorf("a denied stat is refused: %v", err)
	}
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime,
		goodDirectRuntimeWith(withErrno("absolute", "stat", int(syscall.ENOENT)), 0)), e); err == nil {
		t.Error("a stat that found nothing was accepted: the target must exist")
	}
}

func TestDirectRuntimeArtifactMutations(t *testing.T) {
	e := expect()
	for name, mutate := range map[string]func(*RouteDenied){
		"no target fact":                       func(r *RouteDenied) { r.TargetFact = nil },
		"the target does not exist":            func(r *RouteDenied) { r.TargetFact.Regular = false },
		"the target is empty":                  func(r *RouteDenied) { r.TargetFact.Size = 0 },
		"the target is not world-readable":     func(r *RouteDenied) { r.TargetFact.Mode = 0o600 },
		"the parent is not listable":           func(r *RouteDenied) { r.TargetFact.ParentMode = 0o700 },
		"no production template digest":        func(r *RouteDenied) { r.Cell.ProductionTemplateDigest = "" },
		"the probe is the production template": func(r *RouteDenied) { r.Cell.ProductionTemplateDigest = r.Cell.TemplateDigest },
		"the command cell kind":                func(r *RouteDenied) { r.Cell.Kind = RouteCellCommand },
		"an attempt missing":                   func(r *RouteDenied) { r.Attempts = r.Attempts[1:] },
		"attempts reordered":                   func(r *RouteDenied) { r.Attempts[0], r.Attempts[3] = r.Attempts[3], r.Attempts[0] },
		"an operand other than the target":     func(r *RouteDenied) { r.Attempts[0].Argv[1] = "/home/node/.bashrc" },
		"a tampered result digest":             func(r *RouteDenied) { r.Attempts[1].ResultDigest = digestOf("tampered") },
		"an errno name that is not the code":   func(r *RouteDenied) { r.Attempts[0].Errno = "EPERM" },
		"another proof for one attempt":        func(r *RouteDenied) { r.Attempts[4].ReceiptDigest = digestOf("another proof") },
		"another cell for one attempt":         func(r *RouteDenied) { r.Attempts[4].ContainerID = "tbound-d09-cell-2" },
		"another record for one attempt":       func(r *RouteDenied) { r.Attempts[4].JournalSequence = 6 },
		"the control from another proof":       func(r *RouteDenied) { r.Control.ReceiptDigest = digestOf("another proof") },
		"the control outside /work":            func(r *RouteDenied) { r.Control.Argv[1] = "/home/node/.bashrc" },
		"the control is a stat":                func(r *RouteDenied) { r.Control.Op = RouteOpStat },
		"sequence past the journal":            func(r *RouteDenied) { r.Journal.Records = 4 },
	} {
		r := goodDirectRuntime()
		tf := *r.TargetFact
		r.TargetFact = &tf
		r.Attempts = append([]RouteAttempt(nil), r.Attempts...)
		for i := range r.Attempts {
			r.Attempts[i].Argv = append([]string(nil), r.Attempts[i].Argv...)
		}
		r.Control.Argv = append([]string(nil), r.Control.Argv...)
		mutate(&r)
		if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, r), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBashViewArtifactCarriesNoDirectRuntimeMembers(t *testing.T) {
	e := expect()
	for name, mutate := range map[string]func(*RouteDenied){
		"a production template digest": func(r *RouteDenied) { r.Cell.ProductionTemplateDigest = digestOf("x") },
		"a target fact":                func(r *RouteDenied) { r.TargetFact = &RouteTarget{Regular: true} },
		"an op":                        func(r *RouteDenied) { r.Attempts[0].Op = RouteOpOpen },
		"an errno":                     func(r *RouteDenied) { r.Attempts[0].Errno = "EACCES" },
	} {
		r := goodRouteDenied(KindRouteBashView)
		r.Attempts = append([]RouteAttempt(nil), r.Attempts...)
		mutate(&r)
		if err := Validate(KindRouteBashView, encode(t, KindRouteBashView, r), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRouteErrnoNamesAreTheLinuxCodes(t *testing.T) {
	for name, errno := range map[string]syscall.Errno{"EACCES": syscall.EACCES, "EPERM": syscall.EPERM, "ENOENT": syscall.ENOENT} {
		if got := RouteErrnoName(int(errno)); got != name {
			t.Errorf("errno %d is named %q, want %q", errno, got, name)
		}
	}
}

func TestDirectRuntimeArtifactIsBoundLikeEveryRouteArtifact(t *testing.T) {
	e := expect()
	for name, mutate := range map[string]func(*RouteDenied){
		"no run id":                func(r *RouteDenied) { r.RunID = "" },
		"another profile":          func(r *RouteDenied) { r.ProfileHash = digestOf("other") },
		"another source":           func(r *RouteDenied) { r.FrozenSourceDigest = digestOf("other") },
		"relative target":          func(r *RouteDenied) { r.Target = "home/node/.profile" },
		"unclean target":           func(r *RouteDenied) { r.Target = "/home/node/../node/.profile" },
		"target inside /work":      func(r *RouteDenied) { r.Target = "/work/src/release.txt" },
		"target inside /etc":       func(r *RouteDenied) { r.Target = "/etc/hostname" },
		"target is a ruleset root": func(r *RouteDenied) { r.Target = "/tmp" },
		"no image digest":          func(r *RouteDenied) { r.Cell.ImageDigest = "" },
		"no template digest":       func(r *RouteDenied) { r.Cell.TemplateDigest = "" },
		"no ruleset digest":        func(r *RouteDenied) { r.Cell.LandlockRulesetDigest = "" },
		"no journal":               func(r *RouteDenied) { r.Journal = RouteJournal{} },
		"bad journal hash":         func(r *RouteDenied) { r.Journal.HeadHash = "xyz" },
	} {
		r := goodDirectRuntime()
		tf := *r.TargetFact
		r.TargetFact = &tf
		mutate(&r)
		if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, r), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	noPins := e
	noPins.ProfileHash, noPins.FrozenSourceDigest = "", ""
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, goodDirectRuntime()), noPins); err == nil {
		t.Error("accepted although the run pins nothing")
	}
}
