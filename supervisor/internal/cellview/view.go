package cellview

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// ACL values of a Fact. An empty value means the file has no POSIX ACL. Any other value is an ACL that the
// ordinary mode bits do not describe, or an ACL the process could not read: both leave file permissions
// unknown.
const (
	ACLUnreadable = "unreadable"
)

// Fact is what the process in the cell sees of one component of the path. The field names are short
// because the facts ride in the bounded TCB proof line.
type Fact struct {
	Path string `json:"p"`
	Kind string `json:"t"`
	// Mode holds the permission and special bits (st_mode & 07777).
	Mode uint32 `json:"m"`
	UID  uint32 `json:"u"`
	GID  uint32 `json:"g"`
	Size int64  `json:"z,omitempty"`
	// ACL names the POSIX ACLs the file has ("access", "default", "access+default") or ACLUnreadable. It is
	// read for directories and regular files only: permissions are not checked on a symbolic link.
	ACL string `json:"a,omitempty"`
}

// View is the measurement: who the process is and what it sees along the path.
type View struct {
	EUID   uint32   `json:"eu"`
	EGID   uint32   `json:"eg"`
	Groups []uint32 `json:"gr,omitempty"`
	// LSM is /proc/self/attr/current, the process's Linux security module context ("" when unreadable).
	LSM string `json:"lsm,omitempty"`
	// OverflowRead is true when the probe read both overflow ids. OverflowUID and OverflowGID are the ids the
	// kernel reports (/proc/sys/fs/overflowuid, overflowgid; default 65534) for an owner or group that the
	// cell's user namespace does not map. Such an id says nothing about who owns a file.
	OverflowRead bool   `json:"ov,omitempty"`
	OverflowUID  uint32 `json:"ou,omitempty"`
	OverflowGID  uint32 `json:"og,omitempty"`
	Facts        []Fact `json:"f"`
	// Err is set when the walk could not finish (a component was missing or could not be examined).
	Err string `json:"err,omitempty"`
}

// Source is the read-only surface Measure needs. Lstat must not follow a final symbolic link.
type Source interface {
	Lstat(path string) (Fact, error)
	ACL(path string) string
	Identity() (euid, egid uint32, groups []uint32)
	LSM() string
	// Overflow reads /proc/sys/fs/overflowuid and overflowgid; ok is false when either cannot be read.
	Overflow() (uid, gid uint32, ok bool)
}

// Measure walks target with src and records the fact of every component it examined, in order, with the
// identity of the process.
func Measure(src Source, target string) View {
	var v View
	v.EUID, v.EGID, v.Groups = src.Identity()
	v.LSM = src.LSM()
	v.OverflowUID, v.OverflowGID, v.OverflowRead = src.Overflow()
	err := Walk(target, func(p string) (string, error) {
		f, err := src.Lstat(p)
		if err != nil {
			return "", fmt.Errorf("%s: %w", p, err)
		}
		f.Path = p
		if f.Kind == KindDir || f.Kind == KindFile {
			f.ACL = src.ACL(p)
		}
		v.Facts = append(v.Facts, f)
		return f.Kind, nil
	})
	if err != nil {
		v.Err = err.Error()
	}
	return v
}

const (
	bitRead   = 4
	bitSearch = 1
)

// overflowUID and overflowGID report whether an id is the one the kernel shows for an owner or group the
// cell's user namespace does not map. Such an id is not the identity of any process: it never counts as
// the probe's owner or group class. Problems refuses the view that depends on it.
func (v View) overflowUID(id uint32) bool { return v.OverflowRead && id == v.OverflowUID }
func (v View) overflowGID(id uint32) bool { return v.OverflowRead && id == v.OverflowGID }

// Permits reports whether ordinary file permissions give the identity the access bit (4 read, 2 write, 1
// search or execute) on f: the owner class if the identity owns the file, else the group class if its
// effective or a supplementary group is the file's group, else the other class. The sticky bit does not
// matter for reading or searching, and the process has no capability that overrides the bits (the cell's
// proof shows an empty set).
func (v View) Permits(f Fact, bit uint32) bool {
	shift := uint(0)
	switch {
	case v.overflowUID(f.UID):
	case f.UID == v.EUID:
		shift = 6
	case v.overflowGID(f.GID):
	case f.GID == v.EGID || slices.Contains(v.Groups, f.GID):
		shift = 3
	}
	return (f.Mode>>shift)&bit != 0
}

// Problems checks the measurement of target for the cell identity (cellUID, cellGID). It returns the
// reasons a refusal of the file inside the cell could be file permissions and not the cell's confinement:
// a failed or incomplete walk, a symbolic link on any component (the permission model has no fs.protected_symlinks rule), a path that ends inside the paths the cell may read, an ACL that is present
// or unreadable on a component that counts, an owner or group that is the overflow id (the kernel's name for
// an id the cell does not map, so the permission class is unknown) or overflow ids that were not read, and a
// missing permission: search on every directory above
// the target, read on the directory that holds the name, and read on the
// file. An empty result means ordinary permissions would have allowed the open and the listing.
func (v View) Problems(target string, cellUID, cellGID uint32, admitted []string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if cellUID == 0 {
		add("the cell identity is uid 0: it must be the unprivileged cell identity")
	}
	if v.EUID != cellUID || v.EGID != cellGID {
		add("the probe ran as %d:%d, the cell identity is %d:%d", v.EUID, v.EGID, cellUID, cellGID)
	}
	if !v.OverflowRead {
		add("the overflow uid and gid of the cell were not read: whether an owner is the probe or unmapped is unknown")
	} else {
		if v.overflowUID(v.EUID) || v.overflowGID(v.EGID) || slices.ContainsFunc(v.Groups, v.overflowGID) {
			add("the probe identity %d:%d %v includes the overflow id %d:%d: an owner that the cell does not map would look like the probe", v.EUID, v.EGID, v.Groups, v.OverflowUID, v.OverflowGID)
		}
		if v.overflowUID(cellUID) || v.overflowGID(cellGID) {
			add("the cell identity %d:%d includes the overflow id %d:%d", cellUID, cellGID, v.OverflowUID, v.OverflowGID)
		}
	}
	if v.Err != "" {
		return append(problems, "the walk of the target did not finish: "+v.Err)
	}
	facts := map[string]Fact{}
	for _, f := range v.Facts {
		if !path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path {
			add("component %q is not a clean absolute path", f.Path)
			continue
		}
		if _, dup := facts[f.Path]; dup {
			add("component %s is stated twice", f.Path)
		}
		facts[f.Path] = f
	}
	if err := Walk(target, func(p string) (string, error) {
		f, ok := facts[p]
		if !ok {
			return "", fmt.Errorf("no fact for %s", p)
		}
		return f.Kind, nil
	}); err != nil {
		return append(problems, "the recorded components do not make the walk of the target: "+err.Error())
	}
	for _, root := range admitted {
		if target == root || strings.HasPrefix(target, root+"/") {
			add("the target %s is inside the cell's admitted path %s: a read of it would not be a policy failure", target, root)
		}
	}
	acl := func(f Fact) {
		if f.ACL != "" {
			add("%s has a POSIX ACL (%s): its permissions are not the mode bits", f.Path, f.ACL)
		}
	}
	// The owner and group of a component the access depends on must be known: the overflow id stands for
	// any owner or group the cell does not map, so the mode class that applies to the probe is unknown.
	known := func(f Fact) {
		if v.overflowUID(f.UID) || v.overflowGID(f.GID) {
			add("%s has owner %d:%d, the overflow id %d:%d of an owner the cell does not map: its permission class for the probe is unknown", f.Path, f.UID, f.GID, v.OverflowUID, v.OverflowGID)
		}
	}
	components, _ := Components(target)
	listDir := path.Dir(target)
	for _, dir := range components[:len(components)-1] {
		f := facts[dir]
		acl(f)
		known(f)
		if !v.Permits(f, bitSearch) {
			add("the cell identity cannot search the directory %s (mode %04o owner %d:%d) that the walk of the target passes", dir, f.Mode&0o7777, f.UID, f.GID)
		}
	}
	list := facts[listDir]
	known(list)
	if !v.Permits(list, bitRead) {
		add("the cell identity cannot list the directory %s of the target (mode %04o owner %d:%d)", listDir, list.Mode&0o7777, list.UID, list.GID)
	}
	file := facts[target]
	acl(file)
	known(file)
	switch {
	case file.Kind != KindFile || file.Size <= 0:
		add("%s is not a non-empty regular file: there is nothing to read", target)
	case !v.Permits(file, bitRead):
		add("the target %s (mode %04o owner %d:%d) is not readable by the cell identity: a denial could be file permissions", target, file.Mode&0o7777, file.UID, file.GID)
	}
	return problems
}
