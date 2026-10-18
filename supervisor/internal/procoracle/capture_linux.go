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
func HeldCell(dir *os.File) CellReader { return heldCell{dir: dir} }

func (h heldCell) Observe() (CellFacts, error) {
	f, err := piruntime.ObserveCellCgroup(h.dir)
	if err != nil {
		return CellFacts{}, err
	}
	return CellFacts{HierarchyPath: f.HierarchyPath, Populated: f.Populated, PIDs: f.PIDs, Removed: f.Removed}, nil
}

// Capture records one snapshot of the cell and the registry at one sampling
// point. It never fails: whatever it cannot read becomes a Gap in the snapshot,
// and Evaluate turns any gap into evidence_lost for the predicates it affects.
//
// The processes read are the members of the cell (cgroup.procs of the held
// descriptor and of its descendants) and the live registry entries. A pid whose
// /proc entry vanishes while it is read has exited and is not a gap; every
// other read failure is.
func Capture(cell CellReader, view HostView, p CaptureParams) Snapshot {
	c := &capturer{root: view.ProcRoot, snap: Snapshot{
		Seq: p.Seq, Point: p.Point, Deadline: p.Deadline.UTC(), DrainProof: p.DrainProof,
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

	candidates := map[int]bool{} // pid -> listed by the cell
	for _, pid := range facts.PIDs {
		candidates[pid] = true
	}
	for _, id := range c.snap.Registry.Procs {
		if _, ok := candidates[id.PID]; !ok {
			candidates[id.PID] = false
		}
	}
	pids := make([]int, 0, len(candidates))
	for pid := range candidates {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	for _, pid := range pids {
		c.observe(pid, candidates[pid])
	}
	return c.snap
}

type capturer struct {
	root string
	snap Snapshot
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

// observe reads one candidate pid. listed is true when the cell's cgroup.procs
// named it; otherwise it is a registry entry, which only counts when the start
// time still matches (a different start time is pid reuse).
func (c *capturer) observe(pid int, listed bool) {
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
	if !listed && !registered {
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
	proc := Process{
		PID: pid, StartTicks: st.start, PPID: st.ppid, State: st.state, Comm: st.comm, CgroupPath: cg,
		InCell: underCell(cg, c.snap.CellPath), Registered: registered, OutThreads: []Thread{},
	}
	threads, err := c.outThreads(dir, cg)
	if failed("task_unreadable", err) {
		return
	}
	proc.OutThreads = threads

	var sockets []Socket
	if proc.Live() {
		inodes, err := socketInodes(filepath.Join(dir, "fd"))
		if failed("fd_unreadable", err) {
			return
		}
		sockets = c.resolve(dir, id, inodes, gone)
	}
	if gone() {
		return // exited, or the pid was reused, while it was read
	}

	if listed && !proc.InCell && !registered {
		c.gap(DomainRegistry, "unregistered_member_left_cell",
			fmt.Sprintf("pid %d was a member of the cell and is now in %s but was never registered", pid, cg))
	}
	c.snap.Processes = append(c.snap.Processes, proc)
	c.snap.Sockets = append(c.snap.Sockets, sockets...)
}

func underCell(path, cell string) bool {
	return cell != "" && (path == cell || strings.HasPrefix(path, cell+"/"))
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

// outThreads lists the threads of a process whose cgroup differs from the process's.
func (c *capturer) outThreads(dir, processCgroup string) ([]Thread, error) {
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
		if cg != processCgroup {
			out = append(out, Thread{TID: tid, CgroupPath: cg})
		}
	}
	slices.SortFunc(out, func(a, b Thread) int { return a.TID - b.TID })
	return out, nil
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
	st := strings.ToUpper(f[3])
	s := Socket{Proto: proto, Local: local, Remote: remote, State: tcpStates[st], Inode: inode}
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
