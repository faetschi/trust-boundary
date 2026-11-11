//go:build linux

package procoracle

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/piruntime"
)

type heldCell struct{ dir *os.File }

// HeldCell reads a held cell cgroup descriptor through piruntime.ObserveCellCgroup.
// The reader is used by Capture and by a Monitor, possibly at the same time.
func HeldCell(dir *os.File) CellReader { return heldCell{dir: dir} }

func (h heldCell) Observe() (CellFacts, error) {
	f, err := piruntime.ObserveCellCgroup(h.dir)
	if err != nil {
		return CellFacts{}, err
	}
	// The inode of the held descriptor is the cell's identity (a cgroup2 id); fstat
	// works on a removed directory too.
	var st syscall.Stat_t
	if err := syscall.Fstat(int(h.dir.Fd()), &st); err != nil {
		return CellFacts{}, fmt.Errorf("stat the held cell cgroup: %w", err)
	}
	return CellFacts{HierarchyPath: f.HierarchyPath, Inode: st.Ino, Populated: f.Populated, PIDs: f.PIDs, Removed: f.Removed}, nil
}

// Why a pid is read.
const (
	candListed   uint8 = 1 << iota // named by the cell's cgroup.procs
	candRegistry                   // in the launcher's registry
	candIdentity                   // its user namespace is a cell's (cellUserNS)
)

// Capture records one snapshot at one sampling point. It refuses (a cell_identity_mismatch
// gap, no processes read) when the held cgroup is not the one named by
// Registration.CellCgroupPath and CellCgroupInode. It never fails: whatever
// it cannot read becomes a Gap in the snapshot, and Evaluate turns any gap into
// evidence_lost for the predicates it affects.
//
// The processes read are the union of three sources. (1) The members of the
// cell (cgroup.procs of the held descriptor and of its descendants). (2) The live
// registry entries. (3) The identity scan: every process of the whole /proc
// whose user namespace is a cell's (cellUserNS), wherever its cgroup is. The
// identity scan is what makes the capture independent of the registry's
// completeness: a double-fork descendant that left the cell before it was
// registered is still found by its user namespace, which it cannot leave for an
// ancestor.
//
// Completeness of the identity scan (see scanIdentity): it fails closed with a
// DomainIdentity gap when /proc cannot be listed, when pid 1 or the scanner's own
// pid is not visible, or when the status or the user namespace of any pid cannot
// be read, which is what hidepid=1/2/ptraceable, a restricted /proc and a scanner
// without ptrace-read access look like. A pid whose /proc entry vanishes while it
// is read has exited and is not a gap; every other read failure is.
//
// Sockets are those held (by descriptor) by the observed processes, resolved
// through the net tables of each process's own network namespace, plus every
// inet socket of a network namespace other than the scanner's that a live cell
// process is in (cellNetnsSockets).
func Capture(cell CellReader, view HostView, p CaptureParams) Snapshot {
	return capture(cell, view, p, true)
}

func capture(cell CellReader, view HostView, p CaptureParams, withSockets bool) Snapshot {
	c := &capturer{root: view.ProcRoot, reg: p.Registration, withSockets: withSockets, userNS: view.UserNS, nsCache: map[uint64]cellNS{}, snap: Snapshot{
		Seq: p.Seq, Point: p.Point, DrainProof: p.DrainProof, RegistrationDigest: p.Registration.Digest(),
		Registry:  Registry{Recorded: p.Registry.Recorded, Procs: slices.Clone(p.Registry.Procs)},
		Processes: []Process{}, Sockets: []Socket{}, Gaps: []Gap{},
	}}
	slices.SortFunc(c.snap.Registry.Procs, func(a, b ProcID) int {
		if a.PID != b.PID {
			return a.PID - b.PID
		}
		return int(a.StartTicks) - int(b.StartTicks)
	})
	c.snap.Registry.Procs = slices.Compact(c.snap.Registry.Procs)
	if c.userNS == nil {
		c.userNS = userNSAncestry
	}
	now := view.Now
	if now == nil {
		now = time.Now
	}
	c.snap.ObservedAt = now().UTC()
	if !p.Registry.Recorded {
		c.gap(DomainRegistry, "registry_missing", "no registry of cell pids and start times was recorded")
	}
	if c.root == "" {
		c.gap(DomainCell, "proc_root_missing", "no /proc view was supplied")
		return c.snap
	}
	ns, err := os.Readlink(filepath.Join(c.root, "self/ns/pid"))
	if err != nil {
		c.gap(DomainCell, "pidns_unreadable", err.Error())
	}
	c.snap.PIDNamespace = ns
	if cell == nil {
		c.gap(DomainCell, "cell_missing", "no held cell cgroup")
		return c.snap
	}
	facts, err := cell.Observe()
	if err != nil {
		c.gap(DomainCell, "cell_unreadable", err.Error())
		return c.snap
	}
	c.snap.CellPath, c.snap.Populated, c.snap.CellRemoved = facts.HierarchyPath, facts.Populated, facts.Removed
	if facts.Removed {
		c.snap.CellPath = ""
	}
	c.snap.CellInode = facts.Inode
	// The capture is bound to the registered cell: another cgroup (or an unidentified
	// one) is refused, whatever it holds.
	if !c.reg.cellMatches(c.snap.CellPath, facts.Inode, facts.Removed) {
		c.gap(DomainCell, "cell_identity_mismatch", fmt.Sprintf(
			"observed cgroup %q inode %d is not the registered cell %q inode %d",
			c.snap.CellPath, facts.Inode, c.reg.CellCgroupPath, c.reg.CellCgroupInode))
		return c.snap
	}

	candidates := map[int]uint8{}
	for _, pid := range facts.PIDs {
		candidates[pid] |= candListed
	}
	for _, id := range c.snap.Registry.Procs {
		candidates[id.PID] |= candRegistry
	}
	if c.reg.identityUsable() {
		if link, err := os.Readlink(filepath.Join(c.root, "self/ns/user")); err != nil {
			c.gap(DomainIdentity, "userns_unreadable", "the scanner's own user namespace: "+err.Error())
		} else if c.selfUserNS, err = nsInode(link, "user"); err != nil {
			c.gap(DomainIdentity, "userns_unreadable", "the scanner's own user namespace: "+err.Error())
		}
		for _, pid := range c.scanIdentity() {
			candidates[pid] |= candIdentity
		}
	} else {
		c.gap(DomainIdentity, "identity_scan_skipped", "the registration carries no usable cell identity")
	}
	pids := make([]int, 0, len(candidates))
	for pid := range candidates {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	for _, pid := range pids {
		c.observe(pid, candidates[pid])
	}
	if withSockets {
		c.cellNetnsSockets()
	}
	return c.snap
}

type capturer struct {
	root        string
	reg         Registration
	withSockets bool
	snap        Snapshot
	// userNS walks a user namespace's ancestry (HostView.UserNS); selfUserNS is the scanner's own user
	// namespace and nsCache the verdict for each user namespace already walked in this capture.
	userNS     func(nsFile string) ([]UserNSLevel, error)
	selfUserNS uint64
	nsCache    map[uint64]cellNS
}

// cellNS is the verdict of cellUserNS for one user namespace.
type cellNS struct {
	cell  bool
	chain []uint64
}

func (c *capturer) gap(domain, code, detail string) {
	c.snap.Gaps = append(c.snap.Gaps, Gap{Domain: domain, Code: code, Detail: detail})
}

func vanished(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func (c *capturer) registered(id ProcID) bool {
	return slices.Contains(c.snap.Registry.Procs, id)
}

// cellUserNS decides the cell identity of the process of dir from its user namespace. The cells are
// rootless Podman containers of the service: each is a user namespace below the service's rootless
// namespace (Podman's pause namespace), which is directly below the scanner's (the initial one) and owned
// by Registration.ServiceUID. So a process is the cell's when its user namespace is two or more levels
// below the scanner's and the topmost of those is owned by ServiceUID. The rootless namespace itself
// (Podman, its pause process, conmon, crun before it creates the cell) is not the cell's, and neither is
// any other account's container. A cell process cannot leave for an ancestor user namespace; a namespace it
// nests below its own is a deeper one of the same tree. chain lists the namespaces from the process's own
// up to the one directly below the scanner's. With no usable identity in the registration nothing is read.
func (c *capturer) cellUserNS(dir string) (bool, []uint64, error) {
	if !c.reg.identityUsable() {
		return false, nil, nil
	}
	file := filepath.Join(dir, "ns/user")
	link, err := os.Readlink(file)
	if err != nil {
		return false, nil, err
	}
	inode, err := nsInode(link, "user")
	if err != nil {
		return false, nil, err
	}
	if v, ok := c.nsCache[inode]; ok {
		return v.cell, v.chain, nil
	}
	levels, err := c.userNS(file)
	if err != nil {
		return false, nil, err
	}
	top := slices.IndexFunc(levels, func(l UserNSLevel) bool { return l.Inode == c.selfUserNS })
	if c.selfUserNS == 0 || top < 0 {
		return false, nil, fmt.Errorf("user namespace %s does not descend from the scanner's own (%d)", link, c.selfUserNS)
	}
	v := cellNS{cell: top >= 2 && levels[top-1].Owner == c.reg.ServiceUID, chain: make([]uint64, top)}
	for i := range v.chain {
		v.chain[i] = levels[i].Inode
	}
	// A process that changed its user namespace between the link and the walk (crun creating a cell) is
	// judged by what the walk found, but that verdict is not the namespace its link named.
	if levels[0].Inode == inode {
		c.nsCache[inode] = v
	}
	return v.cell, v.chain, nil
}

// maxUserNSLevels bounds the walk: the kernel nests user namespaces at most 32 deep below the initial one.
const maxUserNSLevels = 33

// userNSAncestry is HostView.UserNS of the live kernel: it opens the namespace link and walks NS_GET_PARENT
// up to the outermost user namespace the caller may see (EPERM), reading each one's inode and owner
// (NS_GET_OWNER_UID, as the caller's namespace sees it). Opening another process's link needs ptrace-read
// access to it.
func userNSAncestry(nsFile string) ([]UserNSLevel, error) {
	fd, err := unix.Open(nsFile, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: nsFile, Err: err}
	}
	defer func() { _ = unix.Close(fd) }()
	var out []UserNSLevel
	for range maxUserNSLevels {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return nil, fmt.Errorf("%s: fstat: %w", nsFile, err)
		}
		owner, err := unix.IoctlGetUint32(fd, unix.NS_GET_OWNER_UID)
		if err != nil {
			return nil, fmt.Errorf("%s: NS_GET_OWNER_UID: %w", nsFile, err)
		}
		out = append(out, UserNSLevel{Inode: st.Ino, Owner: owner})
		parent, err := unix.IoctlRetInt(fd, unix.NS_GET_PARENT)
		if errors.Is(err, unix.EPERM) {
			return out, nil // the outermost namespace the caller may see
		}
		if err != nil {
			return nil, fmt.Errorf("%s: NS_GET_PARENT: %w", nsFile, err)
		}
		_ = unix.Close(fd)
		fd = parent
	}
	return nil, fmt.Errorf("%s: user namespace ancestry deeper than %d levels", nsFile, maxUserNSLevels)
}

// parseUIDs reads the Uid line of /proc/<pid>/status: real, effective and saved
// uid (the filesystem uid that follows is not needed).
func parseUIDs(data []byte) ([3]uint32, error) {
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "Uid:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 3 {
			return [3]uint32{}, errors.New("status Uid line is truncated")
		}
		var out [3]uint32
		for i := range out {
			v, err := strconv.ParseUint(f[i], 10, 32)
			if err != nil {
				return [3]uint32{}, errors.New("status Uid line is malformed")
			}
			out[i] = uint32(v)
		}
		return out, nil
	}
	return [3]uint32{}, errors.New("status has no Uid line")
}

// parseNSpid reads the NSpid line of /proc/<pid>/status: the pid in each PID namespace, outermost
// first. A status without the line (kernels before 4.1) gives nil: nothing is known of the process's own
// namespace, and the lingering-init exclusion then never applies to it.
func parseNSpid(data []byte) ([]int, error) {
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "NSpid:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return nil, errors.New("status NSpid line is empty")
		}
		out := make([]int, len(fields))
		for i, f := range fields {
			v, err := strconv.Atoi(f)
			if err != nil || v <= 0 {
				return nil, errors.New("status NSpid line is malformed")
			}
			out[i] = v
		}
		return out, nil
	}
	return nil, nil
}

// scanIdentity lists every pid of the whole /proc whose user namespace is a
// cell's (cellUserNS), wherever its cgroup is.
//
// Hidepid check. A /proc mounted with hidepid=1 (contents of other users' pids
// unreadable), hidepid=2 (other users' pids not listed) or hidepid=ptraceable
// hides exactly the processes that matter: those of another uid than the
// scanner. The scan therefore fails closed unless it can show that it sees them:
// (a) /proc lists, (b) pid 1 and the scanner's own pid (the /proc/self link) are
// listed, (c) pid 1's status is readable (pid 1 belongs to another user than any
// scanner but root, so hidepid=1/2/ptraceable denies it), and (d) the status and
// the user namespace of every listed pid are readable (vanished pids excepted;
// the namespace needs ptrace-read access, so a scanner that is not root fails
// here). Each failure is a DomainIdentity gap. The check is functional, not a
// parse of the mount options: a hidepid mount that exempts the scanner's group
// (gid=) is complete and passes.
func (c *capturer) scanIdentity() []int {
	c.snap.IdentityScanned = true
	entries, err := os.ReadDir(c.root)
	if err != nil {
		c.gap(DomainIdentity, "proc_unreadable", "cannot list /proc: "+err.Error())
		return nil
	}
	var pids []int
	listed := map[int]bool{}
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
			listed[pid] = true
		}
	}
	slices.Sort(pids)
	c.snap.ScannedPIDs = len(pids)
	if !listed[1] {
		c.gap(DomainIdentity, "proc_hidden", "pid 1 is not listed in /proc (hidepid=2 or another view)")
	}
	if target, err := os.Readlink(filepath.Join(c.root, "self")); err != nil {
		c.gap(DomainIdentity, "proc_hidden", "the scanner's own pid cannot be determined: "+err.Error())
	} else if self, err := strconv.Atoi(target); err != nil || !listed[self] {
		c.gap(DomainIdentity, "proc_hidden", fmt.Sprintf("the scanner's own pid %q is not listed in /proc", target))
	}

	var out []int
	var unread, nsUnread []string
	for _, pid := range pids {
		dir := filepath.Join(c.root, strconv.Itoa(pid))
		data, err := os.ReadFile(filepath.Join(dir, "status"))
		if vanished(err) {
			continue
		}
		if err == nil {
			_, err = parseUIDs(data)
		}
		if err != nil {
			if pid == 1 {
				c.gap(DomainIdentity, "proc_hidden", fmt.Sprintf("pid 1 status is unreadable (hidepid=1/ptraceable?): %v", err))
			}
			unread = append(unread, fmt.Sprintf("pid %d: %v", pid, err))
			continue
		}
		cell, _, err := c.cellUserNS(dir)
		if vanished(err) {
			if _, serr := os.Stat(dir); serr != nil {
				continue // exited; a live process always has its user namespace link
			}
		}
		if err != nil {
			nsUnread = append(nsUnread, fmt.Sprintf("pid %d: %v", pid, err))
			continue
		}
		if cell {
			out = append(out, pid)
		}
	}
	if len(unread) > 0 {
		c.gap(DomainIdentity, "status_unreadable",
			fmt.Sprintf("%d of %d pids cannot be checked for the cell identity, first %s", len(unread), len(pids), unread[0]))
	}
	if len(nsUnread) > 0 {
		c.gap(DomainIdentity, "userns_unreadable",
			fmt.Sprintf("the user namespace of %d of %d pids cannot be read, first %s", len(nsUnread), len(pids), nsUnread[0]))
	}
	return out
}

// observe reads one candidate pid. flags say why it is a candidate. A registry
// entry only counts when the start time still matches, and an identity candidate
// only when its user namespace is still a cell's when it is read again here (a
// different start time or namespace is pid reuse).
func (c *capturer) observe(pid int, flags uint8) {
	dir := filepath.Join(c.root, strconv.Itoa(pid))
	fail := func(code string, err error) {
		c.gap(DomainProcess, code, fmt.Sprintf("pid %d: %v", pid, err))
	}
	data, err := os.ReadFile(filepath.Join(dir, "stat"))
	if vanished(err) {
		return
	}
	if err != nil {
		fail("proc_unreadable", err)
		return
	}
	st, err := parseStat(data)
	if err != nil {
		fail("stat_malformed", err)
		return
	}
	id := ProcID{PID: pid, StartTicks: st.start}
	registered := c.registered(id)
	listed := flags&candListed != 0
	if !listed && !registered && flags&candIdentity == 0 {
		return // pid reuse: not the registered incarnation
	}

	// gone is true when the process exited (or its pid was reused) since stat
	// was read: then a missing /proc entry is an exit, not an unreadable one.
	gone := func() bool {
		again, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			return vanished(err)
		}
		st2, perr := parseStat(again)
		return perr == nil && st2.start != st.start
	}
	// failed reports whether a read error ends the observation of this pid.
	failed := func(code string, err error) bool {
		if err == nil {
			return false
		}
		if vanished(err) && gone() {
			return true
		}
		fail(code, err)
		return true
	}

	cg, err := readCgroupPath(filepath.Join(dir, "cgroup"))
	if failed("cgroup_unreadable", err) {
		return
	}
	statusData, err := os.ReadFile(filepath.Join(dir, "status"))
	var uids [3]uint32
	if err == nil {
		uids, err = parseUIDs(statusData)
	}
	if failed("status_unreadable", err) {
		return
	}
	identity, userNS, err := c.cellUserNS(dir)
	if failed("userns_unreadable", err) {
		return
	}
	if !listed && !registered && !identity {
		return // the pid was reused by a process outside the cell's user namespaces
	}
	nspid, err := parseNSpid(statusData)
	if err != nil {
		fail("status_malformed", err)
		return
	}
	proc := Process{
		PID: pid, StartTicks: st.start, PPID: st.ppid, State: st.state, Comm: st.comm, CgroupPath: cg,
		InCell: underCell(cg, c.snap.CellPath), UID: uids, CellIdentity: identity, UserNS: userNS, Registered: registered, OutThreads: []Thread{},
		NSPid: nspid,
	}
	if proc.InCell {
		threads, err := c.outThreads(dir)
		if failed("task_unreadable", err) {
			return
		}
		proc.OutThreads = threads
	}

	var sockets []Socket
	if c.withSockets && proc.Live() {
		inodes, err := socketInodes(filepath.Join(dir, "fd"))
		if failed("fd_unreadable", err) {
			return
		}
		sockets = c.resolve(dir, id, inodes, gone)
	}
	if gone() {
		return // exited, or the pid was reused, while it was read
	}

	if listed && !proc.InCell && !registered && !identity {
		c.gap(DomainRegistry, "unregistered_member_left_cell",
			fmt.Sprintf("pid %d was a member of the cell and is now in %s but was never registered", pid, cg))
	}
	c.snap.Processes = append(c.snap.Processes, proc)
	c.snap.Sockets = append(c.snap.Sockets, sockets...)
}

// nsInode parses a namespace link ("user:[4026531837]") of the given kind.
func nsInode(link, kind string) (uint64, error) {
	v, ok := strings.CutPrefix(link, kind+":[")
	if ok {
		v, ok = strings.CutSuffix(v, "]")
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if !ok || err != nil || n == 0 {
		return 0, fmt.Errorf("%q is not a %s namespace link", link, kind)
	}
	return n, nil
}

func readCgroupPath(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok && strings.HasPrefix(p, "/") {
			return p, nil
		}
	}
	return "", errors.New("no cgroup2 membership line")
}

// outThreads lists the threads of a process of the cell that sit outside the
// cell cgroup. A thread in a cgroup nested below the cell is inside the cell.
func (c *capturer) outThreads(dir string) ([]Thread, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "task"))
	if err != nil {
		return nil, err
	}
	out := []Thread{}
	for _, e := range entries {
		tid, err := strconv.Atoi(e.Name())
		if err != nil {
			return nil, fmt.Errorf("task entry %q", e.Name())
		}
		cg, err := readCgroupPath(filepath.Join(dir, "task", e.Name(), "cgroup"))
		if vanished(err) {
			continue // the thread exited
		}
		if err != nil {
			return nil, err
		}
		if !underCell(cg, c.snap.CellPath) {
			out = append(out, Thread{TID: tid, CgroupPath: cg})
		}
	}
	slices.SortFunc(out, func(a, b Thread) int { return a.TID - b.TID })
	return out, nil
}

// inetTables are the inet net tables (cellNetnsSockets). tcp6 and udp6 are optional
// (IPv6 may be disabled).
var inetTables = []struct {
	name     string
	optional bool
}{{"tcp", false}, {"tcp6", true}, {"udp", false}, {"udp6", true}}

// cellNetnsSockets adds the inet sockets of the cell's own network namespaces
// that no observed process holds by descriptor (an orphan, or a socket whose
// holder could not be attributed). A network namespace other than the scanner's
// that a live cell process is in is the cell's (a network-none cell gets one of
// its own), so every row of it is. The scanner's own (host) namespace is not read
// this way: there a cell socket shows the service uid in the uid column, as the
// service's own sockets do, so a host socket is the cell's only through the
// descriptor of a cell process. A namespace held by no process is unreachable
// through /proc: a stated limit. Unix sockets are attributed by inode through the
// descriptors of cell processes (resolve).
func (c *capturer) cellNetnsSockets() {
	if !c.reg.identityUsable() {
		return
	}
	host, err := os.Readlink(filepath.Join(c.root, "self/ns/net"))
	if err != nil {
		c.gap(DomainSocket, "netns_unreadable", "scanner's own network namespace: "+err.Error())
		return
	}
	reps := map[string][]string{} // netns link -> /proc dirs that are in it
	for _, p := range c.snap.Processes {
		if !p.CellIdentity || !p.Live() {
			continue
		}
		dir := filepath.Join(c.root, strconv.Itoa(p.PID))
		link, err := os.Readlink(filepath.Join(dir, "ns/net"))
		if vanished(err) {
			continue
		}
		if err != nil {
			c.gap(DomainSocket, "netns_unreadable", fmt.Sprintf("pid %d: %v", p.PID, err))
			continue
		}
		if link != host {
			reps[link] = append(reps[link], dir)
		}
	}
	owned := map[uint64]bool{}
	for _, s := range c.snap.Sockets {
		owned[s.Inode] = true
	}
	links := make([]string, 0, len(reps))
	for l := range reps {
		links = append(links, l)
	}
	slices.Sort(links)
	for _, link := range links {
		for _, tbl := range inetTables {
			for _, s := range c.readInet(reps[link], tbl.name, tbl.optional) {
				if s.Inode != 0 && !owned[s.Inode] {
					owned[s.Inode] = true
					c.snap.Sockets = append(c.snap.Sockets, s)
				}
			}
		}
	}
}

// readInet reads one net table from the first directory of a namespace that still
// exists (its holder may have exited). A table that cannot be read or parsed is a
// socket gap.
func (c *capturer) readInet(dirs []string, name string, optional bool) []Socket {
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, "net", name))
		if err != nil {
			if vanished(err) {
				if _, serr := os.Stat(dir); serr != nil {
					continue // the holder exited: try another process of the namespace
				}
				if optional && errors.Is(err, fs.ErrNotExist) {
					return nil
				}
			}
			c.gap(DomainSocket, "net_table_unreadable", fmt.Sprintf("%s net/%s: %v", dir, name, err))
			return nil
		}
		rows, err := parseNetTable(name, data)
		if err != nil {
			c.gap(DomainSocket, "net_table_malformed", fmt.Sprintf("%s net/%s: %v", dir, name, err))
			return nil
		}
		return rows
	}
	return nil
}

// socketInodes lists the inodes of the socket descriptors of a process.
func socketInodes(fdDir string) ([]uint64, error) {
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, err
	}
	seen := map[uint64]struct{}{}
	var out []uint64
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if vanished(err) {
			continue // the descriptor was closed
		}
		if err != nil {
			return nil, err
		}
		v, ok := strings.CutPrefix(target, "socket:[")
		if !ok || !strings.HasSuffix(v, "]") {
			continue
		}
		inode, err := strconv.ParseUint(strings.TrimSuffix(v, "]"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("socket link %q", target)
		}
		if _, dup := seen[inode]; !dup {
			seen[inode] = struct{}{}
			out = append(out, inode)
		}
	}
	slices.Sort(out)
	return out, nil
}

// netTables are read from /proc/<pid>/net, which is the network namespace of that
// process. tcp6 and udp6 are optional (IPv6 may be disabled); the others are not.
var netTables = []struct {
	name     string
	optional bool
}{{"tcp", false}, {"tcp6", true}, {"udp", false}, {"udp6", true}, {"unix", false}}

// resolve maps the socket inodes of one process to rows of its net tables. An
// inode that no table accounts for is a Socket of role unknown and a gap.
func (c *capturer) resolve(dir string, owner ProcID, inodes []uint64, gone func() bool) []Socket {
	if len(inodes) == 0 {
		return nil
	}
	rows := map[uint64]Socket{}
	for _, tbl := range netTables {
		data, err := os.ReadFile(filepath.Join(dir, "net", tbl.name))
		if err != nil {
			if vanished(err) && gone() {
				return nil
			}
			if tbl.optional && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			c.gap(DomainSocket, "net_table_unreadable", fmt.Sprintf("pid %d net/%s: %v", owner.PID, tbl.name, err))
			return nil
		}
		parsed, err := parseNetTable(tbl.name, data)
		if err != nil {
			c.gap(DomainSocket, "net_table_malformed", fmt.Sprintf("pid %d net/%s: %v", owner.PID, tbl.name, err))
			return nil
		}
		for _, s := range parsed {
			rows[s.Inode] = s
		}
	}
	out := make([]Socket, 0, len(inodes))
	for _, inode := range inodes {
		s, ok := rows[inode]
		if !ok {
			s = Socket{Proto: "unknown", Inode: inode, Role: RoleUnknown}
			c.gap(DomainSocket, "socket_unresolved", fmt.Sprintf("pid %d socket inode %d is in no /proc/net table", owner.PID, inode))
		}
		s.Owner = owner
		out = append(out, s)
	}
	return out
}

type procStat struct {
	comm, state string
	ppid        int
	start       uint64
}

// parseStat reads /proc/<pid>/stat. The comm field may hold spaces and
// parentheses, so it is delimited by the first "(" and the last ")".
func parseStat(data []byte) (procStat, error) {
	s := string(data)
	open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || closing < open {
		return procStat{}, errors.New("stat has no comm field")
	}
	rest := strings.Fields(s[closing+1:])
	if len(rest) < 20 { // state is field 3, starttime is field 22
		return procStat{}, errors.New("stat is truncated")
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return procStat{}, errors.New("stat ppid is malformed")
	}
	start, err := strconv.ParseUint(rest[19], 10, 64)
	if err != nil || start == 0 {
		return procStat{}, errors.New("stat starttime is malformed")
	}
	if len(rest[0]) != 1 {
		return procStat{}, errors.New("stat state is malformed")
	}
	return procStat{comm: s[open+1 : closing], state: rest[0], ppid: ppid, start: start}, nil
}

var tcpStates = map[string]string{
	"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV", "04": "FIN_WAIT1", "05": "FIN_WAIT2",
	"06": "TIME_WAIT", "07": "CLOSE", "08": "CLOSE_WAIT", "09": "LAST_ACK", "0A": "LISTEN", "0B": "CLOSING",
}

// parseNetTable parses /proc/net/{tcp,tcp6,udp,udp6,unix}. The header line is
// skipped; any other row that does not parse is an error.
func parseNetTable(name string, data []byte) ([]Socket, error) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var out []Socket
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		var (
			s   Socket
			err error
		)
		if name == "unix" {
			s, err = parseUnixRow(f)
		} else {
			s, err = parseInetRow(name, f)
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func parseInetRow(proto string, f []string) (Socket, error) {
	if len(f) < 10 {
		return Socket{}, errors.New("too few fields")
	}
	local, lport, err := parseInetAddr(f[1])
	if err != nil {
		return Socket{}, err
	}
	remote, _, err := parseInetAddr(f[2])
	if err != nil {
		return Socket{}, err
	}
	inode, err := strconv.ParseUint(f[9], 10, 64)
	if err != nil {
		return Socket{}, errors.New("inode is malformed")
	}
	uid, err := strconv.ParseUint(f[7], 10, 32)
	if err != nil {
		return Socket{}, errors.New("uid is malformed")
	}
	st := strings.ToUpper(f[3])
	s := Socket{Proto: proto, Local: local, Remote: remote, State: tcpStates[st], Inode: inode, UID: uint32(uid)}
	if s.State == "" {
		return Socket{}, errors.New("state is malformed")
	}
	switch {
	case strings.HasPrefix(proto, "tcp") && st == "0A":
		s.Role = RoleListener
	case strings.HasPrefix(proto, "udp") && st == "07":
		s.State = "UNCONN"
		s.Role = RoleListener
		if lport == 0 {
			s.Role = RoleInternal
		}
	default:
		s.Role = RoleConnected
	}
	return s, nil
}

// parseInetAddr decodes "<hex ip>:<hex port>" with the kernel's per-word byte
// order. It returns the printable address and the port.
func parseInetAddr(v string) (string, int, error) {
	host, port, ok := strings.Cut(v, ":")
	if !ok || len(port) != 4 {
		return "", 0, errors.New("address is malformed")
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return "", 0, errors.New("port is malformed")
	}
	if len(host) != 8 && len(host) != 32 {
		return "", 0, errors.New("ip is malformed")
	}
	ip := make(net.IP, len(host)/2)
	for w := 0; w < len(host); w += 8 {
		for b := 0; b < 4; b++ {
			x, err := strconv.ParseUint(host[w+2*b:w+2*b+2], 16, 8)
			if err != nil {
				return "", 0, errors.New("ip is malformed")
			}
			ip[w/2+3-b] = byte(x) // each 32-bit word is printed in host (little-endian) order
		}
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(p))), int(p), nil
}

func parseUnixRow(f []string) (Socket, error) {
	if len(f) < 7 {
		return Socket{}, errors.New("too few fields")
	}
	flags, err := strconv.ParseUint(f[3], 16, 32)
	if err != nil {
		return Socket{}, errors.New("flags are malformed")
	}
	typ, err := strconv.ParseUint(f[4], 16, 16)
	if err != nil {
		return Socket{}, errors.New("type is malformed")
	}
	inode, err := strconv.ParseUint(f[6], 10, 64)
	if err != nil {
		return Socket{}, errors.New("inode is malformed")
	}
	path := ""
	if len(f) > 7 {
		path = strings.Join(f[7:], " ")
	}
	const acceptCon = 0x10000 // __SO_ACCEPTCON
	s := Socket{Proto: "unix", Local: path, Inode: inode}
	switch {
	case flags&acceptCon != 0:
		s.State, s.Role = "LISTEN", RoleListener
	case path == "":
		s.State, s.Role = "UNNAMED", RoleInternal
	case typ == 2: // SOCK_DGRAM bound to a name receives datagrams
		s.State, s.Role = "BOUND", RoleListener
	default:
		s.State, s.Role = "CONNECTED", RoleConnected
	}
	return s, nil
}
