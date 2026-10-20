package guestevidence

import (
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/cellview"
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
	return RouteDenied{RunID: "run-1", ProfileHash: digestOf("probe profile"), FrozenSourceDigest: e.FrozenSourceDigest, Target: "/home/node/.profile",
		Binding: goodBinding(e, digestOf("probe profile"), digestOf("probe template")),
		Cell: RouteCell{Kind: RouteCellPiRuntime, ImageDigest: digestOf("image"), TemplateDigest: digestOf("probe template"),
			LandlockRulesetDigest: digestOf("ruleset"), ProductionTemplateDigest: digestOf("production template"), CellUID: testCellUID, CellGID: testCellGID},
		Journal: RouteJournal{Records: 9, HeadHash: strings.Repeat("a", 64)},
		View:    goodView(), Limits: DirectRuntimeLimits,
		Control: control, Attempts: attempts}
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

func TestBashViewArtifactCarriesNoDirectRuntimeMembers(t *testing.T) {
	e := expect()
	for name, mutate := range map[string]func(*RouteDenied){
		"a production template digest": func(r *RouteDenied) { r.Cell.ProductionTemplateDigest = digestOf("x") },
		"a cell identity":              func(r *RouteDenied) { r.Cell.CellUID = 5 },
		"a view":                       func(r *RouteDenied) { r.View = &cellview.View{} },
		"limits":                       func(r *RouteDenied) { r.Limits = DirectRuntimeLimits },
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
		r := cloneDirect(goodDirectRuntime())
		mutate(&r)
		if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, r), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, mutate := range map[string]func(*Expect){
		"no source pin": func(x *Expect) { x.FrozenSourceDigest = "" }, "no base template pin": func(x *Expect) { x.BaseTemplateDigest = "" },
		"no admin key pin": func(x *Expect) { x.AdminPubkeyDigest = "" }, "no image pin": func(x *Expect) { x.ImageDigest = "" },
		"no runtime bundle pin": func(x *Expect) { x.RuntimeBundleDigest = "" }, "no runner pin": func(x *Expect) { x.RunnerDigest = "" },
	} {
		x := e
		mutate(&x)
		if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, goodDirectRuntime()), x); err == nil {
			t.Errorf("%s: accepted although the run pins nothing", name)
		}
	}
}

// The probe runs under its own config and registration, so its production template and its profile are not the
// pilot's. What it shares with the pilot is the base template (the template without its registration), the image,
// the source and the admin key.
func TestDirectRuntimeArtifactIsBoundToTheBaseTemplateNotToThePilotTemplate(t *testing.T) {
	e := expect()
	d := goodDirectRuntime()
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, d), e); err != nil {
		t.Fatalf("a probe under its own registration is refused: %v", err)
	}
	d.Cell.ProductionTemplateDigest = digestOf("the template of another registration")
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, d), e); err != nil {
		t.Fatalf("the production template of the probe run is not compared with a pilot pin: %v", err)
	}
	d.Binding.BaseTemplateDigest = digestOf("another base template")
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, d), e); err == nil {
		t.Error("an artifact of another base template than the run pins was accepted")
	}
}
