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
	return CellFacts{HierarchyPath: f.HierarchyPath, Populated: f.Populated, PIDs: f.PIDs, Removed: f.Removed}, nil
}

// Why a pid is read.
const (
	candListed   uint8 = 1 << iota // named by the cell's cgroup.procs
	candRegistry                   // in the launcher's registry
	candIdentity                   // its uid in /proc/<pid>/status is the cell identity
)

// Capture records one snapshot at one sampling point. It never fails: whatever
// it cannot read becomes a Gap in the snapshot, and Evaluate turns any gap into
// evidence_lost for the predicates it affects.
//
// The processes read are the union of three sources. (1) The members of the
// cell (cgroup.procs of the held descriptor and of its descendants). (2) The live
// registry entries. (3) The identity scan: every process of the whole /proc
// whose real, effective or saved uid is Registration.CellUID, wherever its
// cgroup is. The identity scan is what makes the capture independent of the
// registry's completeness: a double-fork descendant that left the cell before it
// was registered is still found by its uid.
//
// Completeness of the identity scan (see scanIdentity): it fails closed with a
// DomainIdentity gap when /proc cannot be listed, when pid 1 or the scanner's own
// pid is not visible, or when the status of any pid cannot be read, which is what
// hidepid=1/2/ptraceable and a restricted /proc look like. A pid whose /proc entry
// vanishes while it is read has exited and is not a gap; every other read
// failure is.
//
// Sockets are those held (by descriptor) by the observed processes, resolved
// through the net tables of each process's own network namespace, plus every
// inet socket whose uid column is CellUID in the host view and in the network
// namespace of each live cell-identity process (uidSockets).
func Capture(cell CellReader, view HostView, p CaptureParams) Snapshot {
	return capture(cell, view, p, true)
}

func capture(cell CellReader, view HostView, p CaptureParams, withSockets bool) Snapshot {
	c := &capturer{root: view.ProcRoot, reg: p.Registration, withSockets: withSockets, snap: Snapshot{
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

	candidates := map[int]uint8{}
	for _, pid := range facts.PIDs {
		candidates[pid] |= candListed
	}
	for _, id := range c.snap.Registry.Procs {
		candidates[id.PID] |= candRegistry
	}
	if c.reg.identityUsable() {
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
		c.uidSockets()
	}
	return c.snap
}

type capturer struct {
	root        string
	reg         Registration
	withSockets bool
	snap        Snapshot
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

func (c *capturer) isCellUID(uids [3]uint32) bool {
	return c.reg.identityUsable() && slices.Contains(uids[:], c.reg.CellUID)
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

// scanIdentity lists every pid of the whole /proc whose real, effective or saved
// uid is the cell identity, wherever its cgroup is.
//
// Hidepid check. A /proc mounted with hidepid=1 (contents of other users' pids
// unreadable), hidepid=2 (other users' pids not listed) or hidepid=ptraceable
// hides exactly the processes that matter: those of another uid than the
// scanner. The scan therefore fails closed unless it can show that it sees them:
// (a) /proc lists, (b) pid 1 and the scanner's own pid (the /proc/self link) are
// listed, (c) pid 1's status is readable (pid 1 belongs to another user than any
// scanner but root, so hidepid=1/2/ptraceable denies it), and (d) the status of
// every listed pid is readable (vanished pids excepted). Each failure is a
// DomainIdentity gap. The check is functional, not a parse of the mount
// options: a hidepid mount that exempts the scanner's group (gid=) is complete
// and passes.
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
	var unread []string
	for _, pid := range pids {
		data, err := os.ReadFile(filepath.Join(c.root, strconv.Itoa(pid), "status"))
		if vanished(err) {
			continue
		}
		var uids [3]uint32
		if err == nil {
			uids, err = parseUIDs(data)
		}
		if err != nil {
			if pid == 1 {
				c.gap(DomainIdentity, "proc_hidden", fmt.Sprintf("pid 1 status is unreadable (hidepid=1/ptraceable?): %v", err))
			}
			unread = append(unread, fmt.Sprintf("pid %d: %v", pid, err))
			continue
		}
		if c.isCellUID(uids) {
			out = append(out, pid)
		}
	}
	if len(unread) > 0 {
		c.gap(DomainIdentity, "status_unreadable",
			fmt.Sprintf("%d of %d pids cannot be checked for the cell identity, first %s", len(unread), len(pids), unread[0]))
	}
	return out
}

// observe reads one candidate pid. flags say why it is a candidate. A registry
// entry only counts when the start time still matches, and an identity candidate
// only when its uid still matches when it is read again here (a different start
// time or uid is pid reuse).
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
	identity := c.isCellUID(uids)
	if !listed && !registered && !identity {
		return // the pid was reused by a process of another identity
	}
	proc := Process{
		PID: pid, StartTicks: st.start, PPID: st.ppid, State: st.state, Comm: st.comm, CgroupPath: cg,
		InCell: underCell(cg, c.snap.CellPath), UID: uids, CellIdentity: identity, Registered: registered, OutThreads: []Thread{},
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

// inetTables are the net tables with a uid column. tcp6 and udp6 are optional
// (IPv6 may be disabled).
var inetTables = []struct {
	name     string
	optional bool
}{{"tcp", false}, {"tcp6", true}, {"udp", false}, {"udp6", true}}

// uidSockets adds the inet sockets whose uid column is the cell identity and that
// no observed process holds by descriptor (an orphan, or a socket whose holder
// could not be attributed). It reads the table of the scanner's own network
// namespace (the host view) and of the network namespace of every live
// cell-identity process, so a cell that created its own namespace is covered as
// long as one of its processes is alive. A namespace held by no process is
// unreachable through /proc: a stated limit. Unix sockets have no uid column and
// are attributed by inode through the descriptors of cell-identity processes
// (resolve).
func (c *capturer) uidSockets() {
	if !c.reg.identityUsable() {
		return
	}
	host := filepath.Join(c.root, "self")
	reps := map[string][]string{} // netns link -> /proc dirs that are in it
	if link, err := os.Readlink(filepath.Join(host, "ns/net")); err != nil {
		c.gap(DomainSocket, "netns_unreadable", "scanner's own network namespace: "+err.Error())
	} else {
		reps[link] = append(reps[link], host)
	}
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
		reps[link] = append(reps[link], dir)
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
				if s.UID == c.reg.CellUID && s.Inode != 0 && !owned[s.Inode] {
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
