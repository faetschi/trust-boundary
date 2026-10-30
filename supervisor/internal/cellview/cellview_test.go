package cellview

import (
	"slices"
	"strings"
	"syscall"
	"testing"
)

const (
	cellUID = 100731
	cellGID = 100732
)

type fakeFS struct {
	facts  map[string]Fact
	acl    map[string]string
	euid   uint32
	egid   uint32
	groups []uint32
	lsm    string
	acled  []string
	// overflow ids the fake kernel reports; overflowBroken makes them unreadable.
	overflowUID, overflowGID uint32
	overflowBroken           bool
}

func newFS(facts ...Fact) *fakeFS {
	fs := &fakeFS{facts: map[string]Fact{}, acl: map[string]string{}, euid: cellUID, egid: cellGID, overflowUID: 65534, overflowGID: 65534}
	for _, f := range facts {
		fs.facts[f.Path] = f
	}
	return fs
}

func (f *fakeFS) Lstat(p string) (Fact, error) {
	fact, ok := f.facts[p]
	if !ok {
		return Fact{}, syscall.ENOENT
	}
	return fact, nil
}
func (f *fakeFS) ACL(p string) string {
	f.acled = append(f.acled, p)
	return f.acl[p]
}
func (f *fakeFS) Identity() (uint32, uint32, []uint32) { return f.euid, f.egid, f.groups }
func (f *fakeFS) LSM() string                          { return f.lsm }
func (f *fakeFS) Overflow() (uint32, uint32, bool) {
	return f.overflowUID, f.overflowGID, !f.overflowBroken
}

func dir(p string, mode, uid uint32) Fact {
	return Fact{Path: p, Kind: KindDir, Mode: mode, UID: uid, GID: uid}
}
func file(p string, mode, uid uint32, size int64) Fact {
	return Fact{Path: p, Kind: KindFile, Mode: mode, UID: uid, GID: uid, Size: size}
}
func link(p string) Fact { return Fact{Path: p, Kind: KindLink, Mode: 0o777} }

// openHome is a world-readable file below searchable directories.
func openHome() *fakeFS {
	return newFS(dir("/", 0o755, 0), dir("/home", 0o755, 0), dir("/home/node", 0o755, 1000), file("/home/node/.profile", 0o644, 1000, 807))
}

var admitted = []string{"/usr", "/etc", "/work", "/tmp"}

func problemsOf(fs *fakeFS, target string) []string {
	v := Measure(fs, target)
	return v.Problems(target, cellUID, cellGID, admitted)
}

func TestMeasureRecordsEveryComponentWithTheCellIdentity(t *testing.T) {
	fs := openHome()
	fs.groups = []uint32{4242}
	fs.lsm = "unconfined"
	v := Measure(fs, "/home/node/.profile")
	var paths []string
	for _, f := range v.Facts {
		paths = append(paths, f.Path)
	}
	if v.Err != "" || strings.Join(paths, " ") != "/ /home /home/node /home/node/.profile" {
		t.Fatalf("view = %+v", v)
	}
	if v.EUID != cellUID || v.EGID != cellGID || !slices.Equal(v.Groups, []uint32{4242}) || v.LSM != "unconfined" {
		t.Errorf("identity = %+v", v)
	}
	if got := problemsOf(openHome(), "/home/node/.profile"); len(got) != 0 {
		t.Errorf("a world-readable file below searchable directories: %v", got)
	}
}

func TestMeasureReadsTheACLOfDirectoriesAndFilesOnly(t *testing.T) {
	fs := newFS(dir("/", 0o755, 0), dir("/a", 0o755, 0), dir("/a/b", 0o755, 0), file("/a/b/f", 0o644, 0, 1))
	Measure(fs, "/a/b/f")
	slices.Sort(fs.acled)
	if want := []string{"/", "/a", "/a/b", "/a/b/f"}; !slices.Equal(fs.acled, want) {
		t.Errorf("ACLs read for %v, want %v", fs.acled, want)
	}
	fs = newFS(dir("/", 0o755, 0), link("/a"), dir("/b", 0o755, 0))
	if v := Measure(fs, "/a/f"); v.Err == "" || len(v.Facts) != 2 || v.Facts[1].Kind != KindLink {
		t.Errorf("the walk went past a link: %+v", v)
	}
}

func TestMeasureFailsClosedOnAComponentItCannotExamine(t *testing.T) {
	fs := newFS(dir("/", 0o755, 0), dir("/home", 0o755, 0))
	v := Measure(fs, "/home/node/.profile")
	if v.Err == "" || !strings.Contains(v.Err, "/home/node") {
		t.Fatalf("a missing component: %+v", v)
	}
	if got := v.Problems("/home/node/.profile", cellUID, cellGID, admitted); len(got) == 0 {
		t.Error("an unfinished walk has no problem")
	}
}

// The mount-replaced path: the image shows a readable file at /home/node/.profile, but the cell's mounts
// replace /home/node with a link into the workspace, or with a directory the cell cannot search. The
// in-cell facts show the mount, not the image.
func TestAPathAMountReplacesIsJudgedByWhatTheCellSees(t *testing.T) {
	fs := newFS(dir("/", 0o755, 0), dir("/home", 0o755, 0), link("/home/node"),
		dir("/work", 0o755, 0), dir("/work/private", 0o755, 0), file("/work/private/.profile", 0o644, 0, 5))
	got := problemsOf(fs, "/home/node/.profile")
	if len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "symbolic link") {
		t.Errorf("a target that the mounts lead into the workspace: %v", got)
	}
	fs = newFS(dir("/", 0o755, 0), dir("/home", 0o755, 0), dir("/home/node", 0o700, 0), file("/home/node/.profile", 0o644, 0, 5))
	if got := problemsOf(fs, "/home/node/.profile"); len(got) == 0 {
		t.Error("a mounted directory with mode 0700 owned by another user")
	}
}

// The target inside the workspace is refused for what it is, with or without a mount that leads there.
func TestATargetInsideTheAdmittedPathsIsRefused(t *testing.T) {
	fs := newFS(dir("/", 0o755, 0), dir("/work", 0o755, 0), dir("/work/private", 0o755, 0), file("/work/private/.profile", 0o644, 0, 5))
	got := problemsOf(fs, "/work/private/.profile")
	if len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "admitted path /work") {
		t.Errorf("a target inside the workspace: %v", got)
	}
}

func TestWalkLooksUpEveryComponentAndRefusesLinksAndNonDirectories(t *testing.T) {
	fs := newFS(dir("/", 0o755, 0), dir("/srv", 0o755, 0), dir("/srv/home", 0o755, 0), file("/srv/home/f", 0o644, 0, 1), link("/home"),
		link("/srv/l"), file("/file", 0o644, 0, 1))
	lookup := func(p string) (string, error) {
		f, err := fs.Lstat(p)
		return f.Kind, err
	}
	if err := Walk("/srv/home/f", lookup); err != nil {
		t.Errorf("a path without links: %v", err)
	}
	if got, _ := Components("/srv/home/f"); !slices.Equal(got, []string{"/", "/srv", "/srv/home", "/srv/home/f"}) {
		t.Errorf("components = %v", got)
	}
	for name, target := range map[string]string{
		"a link directory":        "/home/f",
		"a link as the last name": "/srv/l",
		"a name below a file":     "/file/f",
		"a missing name":          "/srv/absent",
		"a relative target":       "srv/home/f",
		"an unclean target":       "/srv//home/f",
		"a dot-dot target":        "/srv/../srv/home/f",
		"the root":                "/",
		"a parent that is a file": "/srv/home/f/x",
	} {
		if err := Walk(target, lookup); err == nil {
			t.Errorf("%s: walked", name)
		}
	}
}

func TestPermitsIsTheOrdinaryOwnerGroupOtherRule(t *testing.T) {
	v := View{EUID: cellUID, EGID: cellGID, Groups: []uint32{777}}
	f := func(mode, uid, gid uint32) Fact { return Fact{Mode: mode, UID: uid, GID: gid} }
	for name, c := range map[string]struct {
		fact Fact
		bit  uint32
		want bool
	}{
		"other class reads":                  {f(0o004, 1, 1), 4, true},
		"other class has no read":            {f(0o770, 1, 1), 4, false},
		"owner class applies":                {f(0o400, cellUID, 1), 4, true},
		"owner class hides other bits":       {f(0o077, cellUID, 1), 4, false},
		"owner class denies search":          {f(0o655, cellUID, 1), 1, false},
		"group class applies":                {f(0o040, 1, cellGID), 4, true},
		"group class hides other bits":       {f(0o007, 1, cellGID), 4, false},
		"owner wins over group":              {f(0o070, cellUID, cellGID), 4, false},
		"supplementary group applies":        {f(0o010, 1, 777), 1, true},
		"supplementary group hides other":    {f(0o001, 1, 777), 1, false},
		"search is not read":                 {f(0o711, 1, 1), 4, false},
		"sticky does not give search":        {f(0o1700, 1, 1), 1, false},
		"sticky does not take search":        {f(0o1755, 1, 1), 1, true},
		"setuid and setgid are no access":    {f(0o6000, 1, 1), 4, false},
		"sticky directory keeps read of 777": {f(0o1777, 1, 1), 4, true},
	} {
		if got := v.Permits(c.fact, c.bit); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestAnACLOrAnUnreadableACLRefusesTheView(t *testing.T) {
	for name, set := range map[string]func(*fakeFS){
		"an access ACL on the file":       func(fs *fakeFS) { fs.acl["/home/node/.profile"] = "access" },
		"an access ACL on a directory":    func(fs *fakeFS) { fs.acl["/home"] = "access" },
		"a default ACL on the parent":     func(fs *fakeFS) { fs.acl["/home/node"] = "default" },
		"an ACL on the root":              func(fs *fakeFS) { fs.acl["/"] = "access+default" },
		"an ACL read that was denied":     func(fs *fakeFS) { fs.acl["/home/node"] = ACLUnreadable },
		"an unreadable ACL on the target": func(fs *fakeFS) { fs.acl["/home/node/.profile"] = ACLUnreadable },
	} {
		fs := openHome()
		set(fs)
		got := Measure(fs, "/home/node/.profile").Problems("/home/node/.profile", cellUID, cellGID, admitted)
		if len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "ACL") {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestProblemsNameWhatPermissionsWouldAllowOrRefuse(t *testing.T) {
	for name, mutate := range map[string]func(*fakeFS){
		"the file is owner-only":              func(fs *fakeFS) { fs.facts["/home/node/.profile"] = file("/home/node/.profile", 0o600, 1000, 807) },
		"the file is the cell's, no owner r":  func(fs *fakeFS) { fs.facts["/home/node/.profile"] = file("/home/node/.profile", 0o044, cellUID, 807) },
		"the file is empty":                   func(fs *fakeFS) { fs.facts["/home/node/.profile"] = file("/home/node/.profile", 0o644, 1000, 0) },
		"the file is a directory":             func(fs *fakeFS) { fs.facts["/home/node/.profile"] = dir("/home/node/.profile", 0o755, 1000) },
		"the parent cannot be searched":       func(fs *fakeFS) { fs.facts["/home/node"] = dir("/home/node", 0o750, 1000) },
		"the parent cannot be listed":         func(fs *fakeFS) { fs.facts["/home/node"] = dir("/home/node", 0o711, 1000) },
		"an ancestor cannot be searched":      func(fs *fakeFS) { fs.facts["/home"] = dir("/home", 0o700, 0) },
		"the root cannot be searched":         func(fs *fakeFS) { fs.facts["/"] = dir("/", 0o600, 0) },
		"the cell owns a directory, no x":     func(fs *fakeFS) { fs.facts["/home"] = dir("/home", 0o655, cellUID) },
		"the cell's group has no x":           func(fs *fakeFS) { fs.facts["/home"] = Fact{Path: "/home", Kind: KindDir, Mode: 0o705, GID: cellGID} },
		"the probe is not the cell identity":  func(fs *fakeFS) { fs.euid = 0 },
		"the probe has another group":         func(fs *fakeFS) { fs.egid = 5 },
		"the target is inside the workspace":  func(fs *fakeFS) { fs.facts["/home/node"] = link("/home/node") },
		"the root is a link":                  func(fs *fakeFS) { fs.facts["/"] = link("/") },
		"a directory is missing from the run": func(fs *fakeFS) { delete(fs.facts, "/home") },
	} {
		fs := openHome()
		mutate(fs)
		if got := problemsOf(fs, "/home/node/.profile"); len(got) == 0 {
			t.Errorf("%s: no problem", name)
		}
	}
	// A supplementary group that gives the missing search permission is honoured.
	fs := openHome()
	fs.facts["/home"] = Fact{Path: "/home", Kind: KindDir, Mode: 0o710, GID: 4242}
	if got := problemsOf(fs, "/home/node/.profile"); len(got) == 0 {
		t.Error("search denied by the other class was granted")
	}
	fs.groups = []uint32{4242}
	if got := problemsOf(fs, "/home/node/.profile"); len(got) != 0 {
		t.Errorf("search granted by a supplementary group is refused: %v", got)
	}
	// A view that states a component twice or with an unclean path is refused.
	v := Measure(openHome(), "/home/node/.profile")
	v.Facts = append(v.Facts, v.Facts[1])
	if got := v.Problems("/home/node/.profile", cellUID, cellGID, admitted); len(got) == 0 {
		t.Error("a component stated twice")
	}
	v = Measure(openHome(), "/home/node/.profile")
	v.Facts[1].Path = "/home/"
	if got := v.Problems("/home/node/.profile", cellUID, cellGID, admitted); len(got) == 0 {
		t.Error("an unclean component")
	}
}

// The reviewer's scenario: Linux shows an owner that the cell's user namespace does not map as the
// overflow id (default 65534), and the cell template allows cell uid 65534. A host-root-owned 0400 file
// then looks owned by the probe, its owner class reads it, yet open fails with EACCES from file
// permissions alone. The measurement must not call that evidence of confinement.
func TestAnOverflowIdIsNeverTakenForOwnershipOfTheProbe(t *testing.T) {
	const overflow = 65534
	hostRootFile := func() *fakeFS {
		fs := newFS(dir("/", 0o755, 0), dir("/home", 0o755, 0), dir("/home/node", 0o755, 1000),
			file("/home/node/.profile", 0o400, overflow, 807))
		fs.euid, fs.egid = overflow, overflow
		return fs
	}
	target := "/home/node/.profile"
	got := hostRootFile()
	v := Measure(got, target)
	if p := v.Problems(target, overflow, overflow, admitted); !strings.Contains(strings.Join(p, ";"), "overflow") {
		t.Errorf("an overflow-owned 0400 file read by the overflow identity: %v", p)
	}
	if v.Permits(v.Facts[3], bitRead) {
		t.Error("the owner class applied to an overflow owner")
	}

	for name, set := range map[string]func(*fakeFS){
		"the probe identity is the overflow uid": func(fs *fakeFS) { fs.egid = cellGID; fs.euid = overflow },
		"the probe group is the overflow gid":    func(fs *fakeFS) { fs.euid = cellUID; fs.egid = overflow },
		"a supplementary group is the overflow":  func(fs *fakeFS) { fs.euid, fs.egid, fs.groups = cellUID, cellGID, []uint32{4242, overflow} },
		"the file has an overflow owner":         func(fs *fakeFS) { fs.euid, fs.egid = cellUID, cellGID },
		"a directory has an overflow owner": func(fs *fakeFS) {
			fs.euid, fs.egid = cellUID, cellGID
			fs.facts["/home"] = dir("/home", 0o755, overflow)
			fs.facts[target] = file(target, 0o644, 1000, 807)
		},
		"a directory has an overflow group": func(fs *fakeFS) {
			fs.euid, fs.egid = cellUID, cellGID
			fs.facts["/home/node"] = Fact{Path: "/home/node", Kind: KindDir, Mode: 0o755, UID: 1000, GID: overflow}
			fs.facts[target] = file(target, 0o644, 1000, 807)
		},
		"the file has an overflow group the probe would match": func(fs *fakeFS) {
			fs.euid, fs.egid, fs.groups = cellUID, cellGID, []uint32{overflow}
			fs.facts[target] = Fact{Path: target, Kind: KindFile, Mode: 0o040, UID: 1000, GID: overflow, Size: 807}
		},
		"the file has an overflow group and open for other": func(fs *fakeFS) {
			fs.euid, fs.egid = cellUID, cellGID
			fs.facts[target] = Fact{Path: target, Kind: KindFile, Mode: 0o004, UID: 1000, GID: overflow, Size: 807}
		},
	} {
		fs := hostRootFile()
		set(fs)
		p := Measure(fs, target).Problems(target, fs.euid, fs.egid, admitted)
		if !strings.Contains(strings.Join(p, ";"), "overflow") {
			t.Errorf("%s: %v", name, p)
		}
	}

	// An overflow id that differs from the default is honoured too.
	fs := openHome()
	fs.overflowUID, fs.overflowGID = 1000, 1000
	if p := problemsOf(fs, target); !strings.Contains(strings.Join(p, ";"), "overflow") {
		t.Errorf("a custom overflow uid equal to the owner: %v", p)
	}
}

func TestUnreadableOverflowIdsRefuseTheView(t *testing.T) {
	fs := openHome()
	fs.overflowBroken = true
	v := Measure(fs, "/home/node/.profile")
	if v.OverflowRead {
		t.Error("unreadable overflow ids recorded as read")
	}
	if p := problemsOf(fs, "/home/node/.profile"); !strings.Contains(strings.Join(p, ";"), "overflow") {
		t.Errorf("unreadable overflow ids: %v", p)
	}
	// A view that carries no overflow statement at all (an older or forged artifact) is refused.
	v = Measure(openHome(), "/home/node/.profile")
	v.OverflowRead, v.OverflowUID, v.OverflowGID = false, 0, 0
	if p := v.Problems("/home/node/.profile", cellUID, cellGID, admitted); !strings.Contains(strings.Join(p, ";"), "overflow") {
		t.Errorf("a view without overflow ids: %v", p)
	}
}

func TestMappedIdsStillPassWithTheOverflowIdsRecorded(t *testing.T) {
	v := Measure(openHome(), "/home/node/.profile")
	if !v.OverflowRead || v.OverflowUID != 65534 || v.OverflowGID != 65534 {
		t.Errorf("overflow ids = %+v", v)
	}
	if p := v.Problems("/home/node/.profile", cellUID, cellGID, admitted); len(p) != 0 {
		t.Errorf("mapped owners: %v", p)
	}
}

// The reviewer's scenario for fs.protected_symlinks: the registered name is a symbolic link owned by
// another user in a sticky world-writable directory. The target's own permissions allow the read, yet with
// protected_symlinks=1 the open fails with EACCES. The permission model has no such rule, so it refuses any
// walk that passes a symbolic link, whichever component it is.
func TestAWalkThatPassesASymbolicLinkIsNeverTheDACBaseline(t *testing.T) {
	sticky := dir("/srv/drop", 0o1777, 0)
	for name, fs := range map[string]*fakeFS{
		"the final component is a link in a sticky world-writable directory": newFS(dir("/", 0o755, 0), dir("/srv", 0o755, 0), sticky,
			dir("/real", 0o755, 0), file("/real/token", 0o644, 0, 9), link("/srv/drop/token")),
		"an intermediate component is a link": newFS(dir("/", 0o755, 0), dir("/srv", 0o755, 0), link("/srv/drop"),
			dir("/real", 0o755, 0), file("/real/token", 0o644, 0, 9)),
	} {
		got := problemsOf(fs, "/srv/drop/token")
		if len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "symbolic link") {
			t.Errorf("%s: %v", name, got)
		}
	}
	fs := newFS(dir("/", 0o755, 0), dir("/srv", 0o755, 0), sticky, file("/srv/drop/token", 0o644, 0, 9))
	if got := problemsOf(fs, "/srv/drop/token"); len(got) != 0 {
		t.Errorf("the same name without a link: %v", got)
	}
}
