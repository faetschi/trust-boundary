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
	return &cellview.View{EUID: testCellUID, EGID: testCellGID, LSM: "unconfined", OverflowRead: true, OverflowUID: 65534, OverflowGID: 65534, Facts: []cellview.Fact{
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
		"no view":                                            func(r *RouteDenied) { r.View = nil },
		"the view walk failed":                               func(r *RouteDenied) { r.View.Err = "/home/node: permission denied" },
		"the target is a directory":                          func(r *RouteDenied) { fact(r, "/home/node/.profile").Kind = cellview.KindDir },
		"the target is empty":                                func(r *RouteDenied) { fact(r, "/home/node/.profile").Size = 0 },
		"the target is not world-readable":                   func(r *RouteDenied) { fact(r, "/home/node/.profile").Mode = 0o600 },
		"the target is the cell's own, no owner read":        func(r *RouteDenied) { f := fact(r, "/home/node/.profile"); f.UID, f.Mode = testCellUID, 0o044 },
		"the parent is not listable":                         func(r *RouteDenied) { fact(r, "/home/node").Mode = 0o711 },
		"the parent cannot be searched":                      func(r *RouteDenied) { fact(r, "/home/node").Mode = 0o750 },
		"an ancestor cannot be searched":                     func(r *RouteDenied) { fact(r, "/home").Mode = 0o700 },
		"the root cannot be searched":                        func(r *RouteDenied) { fact(r, "/").Mode = 0o700 },
		"an ancestor owned by the cell, no owner x":          func(r *RouteDenied) { f := fact(r, "/home"); f.UID, f.Mode = testCellUID, 0o655 },
		"an ACL on the file":                                 func(r *RouteDenied) { fact(r, "/home/node/.profile").ACL = "access" },
		"an ACL on an ancestor":                              func(r *RouteDenied) { fact(r, "/home").ACL = "default" },
		"an ACL that could not be read":                      func(r *RouteDenied) { fact(r, "/home/node").ACL = cellview.ACLUnreadable },
		"an ancestor is missing":                             func(r *RouteDenied) { r.View.Facts = append(r.View.Facts[:1], r.View.Facts[2:]...) },
		"no facts":                                           func(r *RouteDenied) { r.View.Facts = nil },
		"a component stated twice":                           func(r *RouteDenied) { r.View.Facts = append(r.View.Facts, r.View.Facts[0]) },
		"an unclean component":                               func(r *RouteDenied) { fact(r, "/home").Path = "/home/" },
		"the probe is not the cell identity":                 func(r *RouteDenied) { r.View.EUID = 0 },
		"the probe has another group":                        func(r *RouteDenied) { r.View.EGID = 1 },
		"the overflow ids were not read":                     func(r *RouteDenied) { r.View.OverflowRead = false },
		"no overflow ids stated":                             func(r *RouteDenied) { r.View.OverflowRead, r.View.OverflowUID, r.View.OverflowGID = false, 0, 0 },
		"a file owner that is the overflow uid":              func(r *RouteDenied) { fact(r, "/home/node/.profile").UID = 65534 },
		"a directory group that is the overflow gid":         func(r *RouteDenied) { fact(r, "/home/node").GID = 65534 },
		"a supplementary group that is the overflow":         func(r *RouteDenied) { r.View.Groups = []uint32{65534} },
		"the artifact names no cell identity":                func(r *RouteDenied) { r.Cell.CellUID, r.Cell.CellGID = 0, 0 },
		"the cell identity is root":                          func(r *RouteDenied) { r.Cell.CellUID, r.View.EUID = 0, 0 },
		"limits not stated":                                  func(r *RouteDenied) { r.Limits = nil },
		"limits that claim Landlock alone":                   func(r *RouteDenied) { r.Limits = []string{"Landlock refused the access."} },
		"no admin key digest":                                func(r *RouteDenied) { r.Binding.AdminPubkeyDigest = "" },
		"another admin key than the run pins":                func(r *RouteDenied) { r.Binding.AdminPubkeyDigest = digestOf("another admin key") },
		"no probe profile digest":                            func(r *RouteDenied) { r.Binding.ProfileDigest = "" },
		"the journaled profile holds another source":         func(r *RouteDenied) { r.Binding.SourceDigest = digestOf("another source") },
		"no journaled profile source":                        func(r *RouteDenied) { r.Binding.SourceDigest = "" },
		"another base template than the run pins":            func(r *RouteDenied) { r.Binding.BaseTemplateDigest = digestOf("another base template") },
		"the binding is of another template than the cell's": func(r *RouteDenied) { r.Binding.TemplateDigest = digestOf("another template") },
		"no production template digest":                      func(r *RouteDenied) { r.Cell.ProductionTemplateDigest = "" },
		"the probe is the production template":               func(r *RouteDenied) { r.Cell.ProductionTemplateDigest = r.Cell.TemplateDigest },
		"the command cell kind":                              func(r *RouteDenied) { r.Cell.Kind = RouteCellCommand },
		"an attempt missing":                                 func(r *RouteDenied) { r.Attempts = r.Attempts[1:] },
		"attempts reordered":                                 func(r *RouteDenied) { r.Attempts[0], r.Attempts[3] = r.Attempts[3], r.Attempts[0] },
		"an operand other than the target":                   func(r *RouteDenied) { r.Attempts[0].Argv[1] = "/home/node/.bashrc" },
		"a tampered result digest":                           func(r *RouteDenied) { r.Attempts[1].ResultDigest = digestOf("tampered") },
		"an errno name that is not the code":                 func(r *RouteDenied) { r.Attempts[0].Errno = "EPERM" },
		"another proof for one attempt":                      func(r *RouteDenied) { r.Attempts[4].ReceiptDigest = digestOf("another proof") },
		"another cell for one attempt":                       func(r *RouteDenied) { r.Attempts[4].ContainerID = "tbound-d09-cell-2" },
		"another record for one attempt":                     func(r *RouteDenied) { r.Attempts[4].JournalSequence = 6 },
		"the control from another proof":                     func(r *RouteDenied) { r.Control.ReceiptDigest = digestOf("another proof") },
		"the control outside /work":                          func(r *RouteDenied) { r.Control.Argv[1] = "/home/node/.bashrc" },
		"the control is a stat":                              func(r *RouteDenied) { r.Control.Op = RouteOpStat },
		"sequence past the journal":                          func(r *RouteDenied) { r.Journal.Records = 4 },
	} {
		r := cloneDirect(goodDirectRuntime())
		mutate(&r)
		if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, r), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Reproductions of the review findings against the first direct-runtime baseline. The baseline judged the
// image, where the cell sees its mounts, and followed symbolic links, where the kernel's refusal to follow a link
// owned by another user in a sticky directory (fs.protected_symlinks) is not a file permission. Judged from
// what the cell measured on a path without links, both are refused, and so is a key the run does not pin.
func TestDirectRuntimeArtifactRefusesWhatTheFirstBaselineAccepted(t *testing.T) {
	e := expect()
	// The image holds a readable file at the target, but the cell's mounts replace /home/node with a link
	// into the workspace.
	mountReplaced := cloneDirect(goodDirectRuntime())
	mountReplaced.View.Facts = []cellview.Fact{{Path: "/", Kind: cellview.KindDir, Mode: 0o755}, {Path: "/home", Kind: cellview.KindDir, Mode: 0o755},
		{Path: "/home/node", Kind: cellview.KindLink, Mode: 0o777}, {Path: "/work", Kind: cellview.KindDir, Mode: 0o755},
		{Path: "/work/private", Kind: cellview.KindDir, Mode: 0o755}, {Path: "/work/private/.profile", Kind: cellview.KindFile, Mode: 0o644, Size: 807}}
	err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, mountReplaced), e)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a target the cell's mounts lead into the workspace: %v", err)
	}
	// The mount shows a directory the cell cannot search where the image has an open one.
	closed := cloneDirect(goodDirectRuntime())
	closed.View.Facts[2] = cellview.Fact{Path: "/home/node", Kind: cellview.KindDir, Mode: 0o700}
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, closed), e); err == nil {
		t.Error("a directory the mounts closed was judged by the image's modes")
	}
	// A link owned by another user in a sticky world-writable directory: the open fails with EACCES from
	// fs.protected_symlinks although the target's modes allow it.
	sticky := cloneDirect(goodDirectRuntime())
	sticky.View.Facts = []cellview.Fact{{Path: "/", Kind: cellview.KindDir, Mode: 0o755}, {Path: "/home", Kind: cellview.KindDir, Mode: 0o1777},
		{Path: "/home/node", Kind: cellview.KindDir, Mode: 0o755, UID: 1000, GID: 1000}, {Path: "/home/node/.profile", Kind: cellview.KindLink, Mode: 0o777, UID: 1000, GID: 1000}}
	err = Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, sticky), e)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a final symbolic link: %v", err)
	}
	// A profile verified under a key other than the pinned one is refused, whatever the facts say.
	e.AdminPubkeyDigest = digestOf("the pinned admin key")
	if err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, goodDirectRuntime()), e); err == nil || !strings.Contains(err.Error(), "admin_pubkey_digest") {
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

// The review finding against 0fd1834: the kernel shows an owner the cell's user namespace does not map as
// the overflow id (65534), and the cell template allows cell uid 65534. A file owned by host root then
// looks owned by the probe, so ordinary permissions looked like they allowed an open that file permissions
// alone refuse. The artifact is refused.
func TestDirectRuntimeArtifactRefusesAnOverflowOwnedTarget(t *testing.T) {
	r := cloneDirect(goodDirectRuntime())
	r.Cell.CellUID, r.Cell.CellGID = 65534, 65534
	r.View.EUID, r.View.EGID = 65534, 65534
	r.View.Facts[3] = cellview.Fact{Path: "/home/node/.profile", Kind: cellview.KindFile, Mode: 0o400, UID: 65534, GID: 65534, Size: 807}
	err := Validate(KindRouteDirectRuntime, encode(t, KindRouteDirectRuntime, r), expect())
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Errorf("an overflow-owned 0400 file read as the overflow identity: %v", err)
	}
}
