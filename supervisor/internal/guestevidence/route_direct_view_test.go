package guestevidence

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/cellview"
)

const (
	testCellUID = 100731
	testCellGID = 100732
)

// goodView is what the probe in the cell measured for a target that file permissions alone would not
// refuse: a world-readable file below searchable directories, none owned by the cell identity.
func goodView() *cellview.View {
	return &cellview.View{EUID: testCellUID, EGID: testCellGID, LSM: "unconfined", Facts: []cellview.Fact{
		{Path: "/", Kind: cellview.KindDir, Mode: 0o755},
		{Path: "/home", Kind: cellview.KindDir, Mode: 0o755},
		{Path: "/home/node", Kind: cellview.KindDir, Mode: 0o755, UID: 1000, GID: 1000},
		{Path: "/home/node/.profile", Kind: cellview.KindFile, Mode: 0o644, UID: 1000, GID: 1000, Size: 807}}}
}

// cloneDirect copies the members of a good artifact that a test mutates.
func cloneDirect(r RouteDenied) RouteDenied {
	v := *r.View
	v.Facts = append([]cellview.Fact(nil), v.Facts...)
	v.Groups = append([]uint32(nil), v.Groups...)
	r.View = &v
	r.Limits = append([]string(nil), r.Limits...)
	r.Attempts = append([]RouteAttempt(nil), r.Attempts...)
	for i := range r.Attempts {
		r.Attempts[i].Argv = append([]string(nil), r.Attempts[i].Argv...)
	}
	r.Control.Argv = append([]string(nil), r.Control.Argv...)
	return r
}

func TestDirectRuntimeArtifactMutations(t *testing.T) {
	e := expect()
	fact := func(r *RouteDenied, path string) *cellview.Fact {
		for i := range r.View.Facts {
			if r.View.Facts[i].Path == path {
				return &r.View.Facts[i]
			}
		}
		t.Fatalf("no fact %s", path)
		return nil
	}
	for name, mutate := range map[string]func(*RouteDenied){
		"no view":                                     func(r *RouteDenied) { r.View = nil },
		"the view walk failed":                        func(r *RouteDenied) { r.View.Err = "/home/node: permission denied" },
		"the target is a directory":                   func(r *RouteDenied) { fact(r, "/home/node/.profile").Kind = cellview.KindDir },
		"the target is empty":                         func(r *RouteDenied) { fact(r, "/home/node/.profile").Size = 0 },
		"the target is not world-readable":            func(r *RouteDenied) { fact(r, "/home/node/.profile").Mode = 0o600 },
		"the target is the cell's own, no owner read": func(r *RouteDenied) { f := fact(r, "/home/node/.profile"); f.UID, f.Mode = testCellUID, 0o044 },
		"the parent is not listable":                  func(r *RouteDenied) { fact(r, "/home/node").Mode = 0o711 },
		"the parent cannot be searched":               func(r *RouteDenied) { fact(r, "/home/node").Mode = 0o750 },
		"an ancestor cannot be searched":              func(r *RouteDenied) { fact(r, "/home").Mode = 0o700 },
		"the root cannot be searched":                 func(r *RouteDenied) { fact(r, "/").Mode = 0o700 },
		"an ancestor owned by the cell, no owner x":   func(r *RouteDenied) { f := fact(r, "/home"); f.UID, f.Mode = testCellUID, 0o655 },
		"an ACL on the file":                          func(r *RouteDenied) { fact(r, "/home/node/.profile").ACL = "access" },
		"an ACL on an ancestor":                       func(r *RouteDenied) { fact(r, "/home").ACL = "default" },
		"an ACL that could not be read":               func(r *RouteDenied) { fact(r, "/home/node").ACL = cellview.ACLUnreadable },
		"an ancestor is missing":                      func(r *RouteDenied) { r.View.Facts = append(r.View.Facts[:1], r.View.Facts[2:]...) },
		"no facts":                                    func(r *RouteDenied) { r.View.Facts = nil },
		"a component stated twice":                    func(r *RouteDenied) { r.View.Facts = append(r.View.Facts, r.View.Facts[0]) },
		"an unclean component":                        func(r *RouteDenied) { fact(r, "/home").Path = "/home/" },
		"the probe is not the cell identity":          func(r *RouteDenied) { r.View.EUID = 0 },
		"the probe has another group":                 func(r *RouteDenied) { r.View.EGID = 1 },
		"the artifact names no cell identity":         func(r *RouteDenied) { r.Cell.CellUID, r.Cell.CellGID = 0, 0 },
		"the cell identity is root":                   func(r *RouteDenied) { r.Cell.CellUID, r.View.EUID = 0, 0 },
		"limits not stated":                           func(r *RouteDenied) { r.Limits = nil },
		"limits that claim Landlock alone":            func(r *RouteDenied) { r.Limits = []string{"Landlock refused the access."} },
		"no admin key digest":                         func(r *RouteDenied) { r.AdminPubkeyDigest = "" },
		"another admin key than the run pins":         func(r *RouteDenied) { r.AdminPubkeyDigest = digestOf("another admin key") },
		"no probe profile digest":                     func(r *RouteDenied) { r.ProbeProfileDigest = "" },
		"the probe profile is the production profile": func(r *RouteDenied) { r.ProbeProfileDigest = r.ProfileHash },
		"the journaled profile holds another source":  func(r *RouteDenied) { r.ProfileSourceDigest = digestOf("another source") },
		"no journaled profile source":                 func(r *RouteDenied) { r.ProfileSourceDigest = "" },
		"another production template than the run pins": func(r *RouteDenied) {
			r.Cell.ProductionTemplateDigest = digestOf("another production template")
		},
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
		r := cloneDirect(goodDirectRuntime())
		mutate(&r)
		if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, r), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Reproductions of the review findings against the first direct-runtime baseline. The baseline judged the
// image, where the cell sees its mounts, and its walk left a directory by ".." without the search permission
// the kernel checks. Judged from what the cell measured, both are refused, and so is a key the run does not pin.
func TestDirectRuntimeArtifactRefusesWhatTheFirstBaselineAccepted(t *testing.T) {
	e := expect()
	// The image holds a readable file at the target, but the cell's mounts replace /home/node with a link
	// into the workspace.
	mountReplaced := cloneDirect(goodDirectRuntime())
	mountReplaced.View.Facts = []cellview.Fact{{Path: "/", Kind: cellview.KindDir, Mode: 0o755}, {Path: "/home", Kind: cellview.KindDir, Mode: 0o755},
		{Path: "/home/node", Kind: cellview.KindLink, Mode: 0o777, Link: "/work/private"}, {Path: "/work", Kind: cellview.KindDir, Mode: 0o755},
		{Path: "/work/private", Kind: cellview.KindDir, Mode: 0o755}, {Path: "/work/private/.profile", Kind: cellview.KindFile, Mode: 0o644, Size: 807}}
	err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, mountReplaced), e)
	if err == nil || !strings.Contains(err.Error(), "admitted path /work") {
		t.Errorf("a target the cell's mounts lead into the workspace: %v", err)
	}
	// The mount shows a directory the cell cannot search where the image has an open one.
	closed := cloneDirect(goodDirectRuntime())
	closed.View.Facts[2] = cellview.Fact{Path: "/home/node", Kind: cellview.KindDir, Mode: 0o700}
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, closed), e); err == nil {
		t.Error("a directory the mounts closed was judged by the image's modes")
	}
	// A link whose text goes through a 0700 directory and back by "..": the kernel needs search on it.
	dotdot := cloneDirect(goodDirectRuntime())
	dotdot.View.Facts = []cellview.Fact{{Path: "/", Kind: cellview.KindDir, Mode: 0o755}, {Path: "/home", Kind: cellview.KindDir, Mode: 0o755},
		{Path: "/home/node", Kind: cellview.KindLink, Mode: 0o777, Link: "/locked/../public"}, {Path: "/locked", Kind: cellview.KindDir, Mode: 0o700},
		{Path: "/public", Kind: cellview.KindDir, Mode: 0o755}, {Path: "/public/.profile", Kind: cellview.KindFile, Mode: 0o644, Size: 807}}
	err = Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, dotdot), e)
	if err == nil || !strings.Contains(err.Error(), "cannot search the directory /locked") {
		t.Errorf("leaving a 0700 directory by '..': %v", err)
	}
	// With the directory searchable the same artifact is valid: the refusal is about permission, not the link.
	dotdot.View.Facts[3].Mode = 0o711
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, dotdot), e); err != nil {
		t.Errorf("a walk over a searchable directory: %v", err)
	}
	// A profile verified under a key other than the pinned one is refused, whatever the facts say.
	e.AdminPubkeyDigest = digestOf("the pinned admin key")
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, goodDirectRuntime()), e); err == nil || !strings.Contains(err.Error(), "admin key") {
		t.Errorf("a profile verified under a key the run does not pin: %v", err)
	}
	e.AdminPubkeyDigest = ""
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, goodDirectRuntime()), e); err == nil {
		t.Error("accepted although the run pins no admin key")
	}
}

func TestDirectRuntimeLimitsStateThatLandlockAloneIsNotShown(t *testing.T) {
	all := strings.Join(DirectRuntimeLimits, " ")
	for _, want := range []string{"AppArmor", "Landlock alone", "by exclusion", "view.lsm"} {
		if !strings.Contains(all, want) {
			t.Errorf("the limits do not mention %q", want)
		}
	}
}
