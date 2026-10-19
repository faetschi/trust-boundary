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
}

func newFS(facts ...Fact) *fakeFS {
	fs := &fakeFS{facts: map[string]Fact{}, acl: map[string]string{}, euid: cellUID, egid: cellGID}
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

func (f *fakeFS) lookup(p string) (Node, error) {
	fact, err := f.Lstat(p)
	return Node{Kind: fact.Kind, Link: fact.Link}, err
}

func dir(p string, mode, uid uint32) Fact {
	return Fact{Path: p, Kind: KindDir, Mode: mode, UID: uid, GID: uid}
}
func file(p string, mode, uid uint32, size int64) Fact {
	return Fact{Path: p, Kind: KindFile, Mode: mode, UID: uid, GID: uid, Size: size}
}
func link(p, to string) Fact { return Fact{Path: p, Kind: KindLink, Mode: 0o777, Link: to} }

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
	fs := newFS(dir("/", 0o755, 0), dir("/a", 0o755, 0), link("/a/l", "/b"), dir("/b", 0o755, 0), file("/b/f", 0o644, 0, 1))
	Measure(fs, "/a/l/f")
	slices.Sort(fs.acled)
	if want := []string{"/", "/a", "/b", "/b/f"}; !slices.Equal(fs.acled, want) {
		t.Errorf("ACLs read for %v, want %v", fs.acled, want)
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
	fs := newFS(dir("/", 0o755, 0), dir("/home", 0o755, 0), link("/home/node", "/work/private"),
		dir("/work", 0o755, 0), dir("/work/private", 0o755, 0), file("/work/private/.profile", 0o644, 0, 5))
	got := problemsOf(fs, "/home/node/.profile")
	if len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "admitted path /work") {
		t.Errorf("a target that the mounts lead into the workspace: %v", got)
	}
	fs = newFS(dir("/", 0o755, 0), dir("/home", 0o755, 0), dir("/home/node", 0o700, 0), file("/home/node/.profile", 0o644, 0, 5))
	if got := problemsOf(fs, "/home/node/.profile"); len(got) == 0 {
		t.Error("a mounted directory with mode 0700 owned by another user")
	}
}

// Leaving a directory by ".." needs search permission on it.
func TestLeavingADirectoryByDotDotNeedsSearchPermissionOnIt(t *testing.T) {
	facts := []Fact{dir("/", 0o755, 0), dir("/locked", 0o700, 0), dir("/public", 0o755, 0), file("/public/f", 0o644, 0, 3)}
	fs := newFS(append(facts, link("/start", "/locked/../public"))...)
	w, err := Resolve("/start/f", fs.lookup)
	if err != nil || w.Resolved != "/public/f" || !slices.Contains(w.Searched, "/locked") {
		t.Fatalf("walk = %+v, %v", w, err)
	}
	got := problemsOf(fs, "/start/f")
	if len(got) != 1 || !strings.Contains(got[0], "/locked") || !strings.Contains(got[0], "cannot search") {
		t.Errorf("problems = %v", got)
	}
	fs.facts["/locked"] = dir("/locked", 0o711, 0)
	if got := problemsOf(fs, "/start/f"); len(got) != 0 {
		t.Errorf("a directory that can be searched: %v", got)
	}
}

func TestResolveFollowsLinksDotsAndFailsOnLoops(t *testing.T) {
	fs := newFS(dir("/", 0o755, 0), dir("/srv", 0o755, 0), dir("/srv/home", 0o755, 0), file("/srv/home/f", 0o644, 0, 1), link("/home", "srv/home"),
		link("/loop", "/loop"), link("/dots", "srv/./home/../home"), link("/up", "srv/./home/.."), file("/file", 0o644, 0, 1))
	for name, c := range map[string]struct {
		target, resolved, listDir string
		searched                  []string
	}{
		"relative link to a directory": {"/home/f", "/srv/home/f", "/srv/home", []string{"/", "/srv"}},
		"dot and dot-dot":              {"/dots/f", "/srv/home/f", "/srv/home", []string{"/", "/srv", "/srv/home"}},
		"link ending in dot-dot":       {"/up/home/f", "/srv/home/f", "/srv/home", []string{"/", "/srv", "/srv/home"}},
	} {
		w, err := Resolve(c.target, fs.lookup)
		if err != nil || w.Resolved != c.resolved || w.ListDir != c.listDir {
			t.Errorf("%s: %+v %v", name, w, err)
			continue
		}
		for _, d := range c.searched {
			if !slices.Contains(w.Searched, d) {
				t.Errorf("%s: %s not searched (searched %v)", name, d, w.Searched)
			}
		}
	}
	for name, target := range map[string]string{
		"a loop":                  "/loop/f",
		"a name below a file":     "/file/f",
		"a missing name":          "/srv/absent",
		"a relative target":       "srv/home/f",
		"an unclean target":       "/srv//home/f",
		"the root":                "/",
		"dot-dot below a file":    "/file/../srv/home/f",
		"a parent that is a file": "/srv/home/f/x",
	} {
		if w, err := Resolve(target, fs.lookup); err == nil {
			t.Errorf("%s: resolved to %+v", name, w)
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
		"the target is inside the workspace":  func(fs *fakeFS) { fs.facts["/home/node"] = link("/home/node", "/work") },
		"the root is a link":                  func(fs *fakeFS) { fs.facts["/"] = link("/", "/x") },
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
