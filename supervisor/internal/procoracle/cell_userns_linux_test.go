//go:build linux

package procoracle

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The M3 rehearsal: the guest admin's systemd user manager (host uid 1000, the in-cell uid, in the host
// user namespace) outside the cell is not the cell's and is no PROC-02.
func TestCaptureHostUID1000OutsideTheCellIsNotProc02(t *testing.T) {
	f := newFakeProc(t)
	f.addProcUID(1000, 700, 7000, "systemd", "S", 1, "/user.slice/user-1000.slice/user@1000.service/init.scope")
	snaps := threePoints(f, cellOf())
	for _, s := range snaps {
		if len(s.Processes) != 0 {
			t.Fatalf("a host uid-1000 process was recorded: %+v", s.Processes)
		}
	}
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

// An escaped cell process runs as the service uid (995) on the host. In the cell user namespace, or in a
// namespace it nested below it, outside the cell cgroup, it is cell identity and a PROC-02 finding.
func TestCaptureCellUserNSProcessOutsideTheCellIsProc02(t *testing.T) {
	const nested uint64 = 4026532300
	f := newFakeProc(t)
	f.addUserNS(nested, cellUserNS, fakeUID)
	f.addProc(500, 7000, "escaped", "S", 1, "/system.slice/elsewhere")
	f.addProc(501, 7100, "nested", "S", 500, "/system.slice/elsewhere")
	f.setUserNS(501, nested)
	snaps := threePoints(f, cellOf())
	got := map[int][]uint64{}
	for _, p := range snaps[0].Processes {
		if p.CellIdentity && p.UID == [3]uint32{fakeUID, fakeUID, fakeUID} {
			got[p.PID] = p.UserNS
		}
	}
	if !slices.Equal(got[500], []uint64{cellUserNS, rootlessUserNS}) || !slices.Equal(got[501], []uint64{nested, cellUserNS, rootlessUserNS}) {
		t.Fatalf("cell identity: %v (processes %+v gaps %v)", got, snaps[0].Processes, gapDomains(snaps[0]))
	}
	r := evalT(snaps, Policy{})
	pids := map[int]bool{}
	for _, fnd := range result(t, r, PredicateProc02).Findings {
		pids[fnd.Process.PID] = true
	}
	if result(t, r, PredicateProc02).Verdict != VerdictEffectPresent || !pids[500] || !pids[501] {
		t.Errorf("PROC-02: %+v", result(t, r, PredicateProc02))
	}
}

// The service's rootless namespace holds Podman, its pause process and conmon (uid 995): they are not the
// cell's. Nor is a container of another account's rootless namespace (the admin's, owned by uid 1000).
func TestCaptureRootlessNamespaceAndForeignContainersAreNotCellIdentity(t *testing.T) {
	const adminRootless, adminContainer uint64 = 4026532400, 4026532401
	f := newFakeProc(t)
	f.addUserNS(adminRootless, hostUserNS, 1000)
	f.addUserNS(adminContainer, adminRootless, 1000)
	for i, comm := range []string{"catatonit", "podman", "conmon", "crun"} {
		f.addProcUID(fakeUID, 600+i, uint64(7000+i), comm, "S", 1, "/system.slice/tbound-pi.service/runner")
		f.setUserNS(600+i, rootlessUserNS)
	}
	f.addProcUID(1000, 610, 7100, "admin-ctr", "S", 1, "/user.slice")
	f.setUserNS(610, adminContainer)
	snaps := threePoints(f, cellOf())
	if len(snaps[0].Processes) != 0 || len(snaps[0].Gaps) != 0 {
		t.Fatalf("processes %+v gaps %v", snaps[0].Processes, gapDomains(snaps[0]))
	}
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

// A process that changes its user namespace during the read (crun creating a cell's namespace from the
// rootless one) is judged by its own walk, and that verdict does not reach another process of the
// namespace its link named: conmon, still in the rootless namespace, stays outside the cell identity.
func TestCaptureUserNSThatMovedDuringTheReadStaysWithThatProcess(t *testing.T) {
	f := newFakeProc(t)
	f.addProcUID(fakeUID, 600, 7000, "crun", "S", 1, "/system.slice/tbound-pi.service/runner")
	f.setUserNS(600, rootlessUserNS)
	f.walkAs[600] = cellUserNS
	f.addProcUID(fakeUID, 601, 7001, "conmon", "S", 1, "/system.slice/tbound-pi.service/runner")
	f.setUserNS(601, rootlessUserNS)
	s := Capture(cellOf(), f.view(), params(PointInterval))
	for _, p := range s.Processes {
		if p.PID == 601 {
			t.Fatalf("conmon became cell identity: %+v", p)
		}
	}
}

// The user namespace scan fails closed: a namespace link the scanner may not read (no ptrace-read access),
// one that is missing for a live pid, or an ancestry that does not reach the scanner's own namespace.
func TestCaptureUnreadableUserNamespaceIsAnIdentityGap(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, f *fakeProc){
		"no ptrace-read access": func(t *testing.T, f *fakeProc) {
			skipIfRoot(t)
			dir := filepath.Join(f.root, "300/ns")
			if err := os.Chmod(dir, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		},
		"link missing for a live pid": func(t *testing.T, f *fakeProc) {
			if err := os.Remove(filepath.Join(f.root, "300/ns/user")); err != nil {
				t.Fatal(err)
			}
		},
		"ancestry outside the scanner's scope": func(t *testing.T, f *fakeProc) {
			f.addUserNS(4026532900, 0, fakeUID)
			f.setUserNS(300, 4026532900)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeProc(t)
			f.addProcUID(2000, 300, 8000, "other", "S", 1, "/user.slice")
			mutate(t, f)
			var snaps []Snapshot
			for i, pt := range []Point{PointInterval, PointPostTeardown, PointFinalCleanup} {
				snaps = append(snaps, Capture(cellOf(), f.view(), withSeq(params(pt), i+1)))
			}
			wantGapCode(t, snaps[2], DomainIdentity, "userns_unreadable")
			wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
		})
	}
}

func TestRegistrationWithoutAServiceUIDIsInvalidAndDoesNotScan(t *testing.T) {
	f := newFakeProc(t)
	p := params(PointFinalCleanup)
	p.Registration.ServiceUID = 0
	s := Capture(cellOf(), f.view(), p)
	wantGapCode(t, s, DomainIdentity, "identity_scan_skipped")
	reg := testReg
	reg.ServiceUID = 0
	if gaps := reg.gaps(); len(gaps) == 0 || gaps[0].Code != "registration_invalid" {
		t.Errorf("gaps: %+v", gaps)
	}
}

// The D49 early close reads the same identity: a live cell-namespace process outside the cgroup refuses
// the proof, and a host uid-1000 process does not.
func TestClosureScanUsesTheUserNamespaceIdentity(t *testing.T) {
	cell := fakeCellReader{facts: CellFacts{HierarchyPath: sshCell.CellCgroupPath, Inode: sshCell.CellCgroupInode}}
	scan := func(f *fakeProc) Snapshot {
		v := f.view()
		v.Now = func() time.Time { return sshT0.Add(5 * time.Second) }
		return Capture(cell, v, CaptureParams{Point: PointInterval, Seq: 1, Registration: closureReg, Registry: Registry{Recorded: true}})
	}
	closure := func(s Snapshot) Closure {
		d := cleanClosureDoc()
		d.Scan = s
		return EvaluateClosure(closureReg, d, sshRef.Interval, "run-1")
	}

	f := newFakeProc(t)
	f.addProcUID(1000, 700, 7000, "systemd", "S", 1, "/user.slice/user-1000.slice/user@1000.service/init.scope")
	if c := closure(scan(f)); !c.Proven {
		t.Errorf("a host uid-1000 process blocked the proof: %+v", c.Reasons)
	}

	f.addProc(500, 7100, "escaped", "S", 1, "/system.slice/elsewhere")
	c := closure(scan(f))
	if c.Proven || !slices.ContainsFunc(c.Unresolved, func(r string) bool { return strings.Contains(r, "process 500") }) {
		t.Errorf("a live cell-namespace process did not refuse the proof: %+v", c)
	}
}

// The NS_GET_PARENT walk on the live kernel, unprivileged: this process (the scanner's own user
// namespace) is not the cell's; a child one user namespace below (where Podman's rootless namespace
// sits) is not; a process two below, under a namespace this uid owns, is, and only for this uid as the
// service.
func TestCellUserNSOnTheLiveKernel(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		t.Skip("running as root: the namespaces this test makes would be owned by root, which is no service identity")
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("no unshare to nest a user namespace")
	}
	spawn := func(mapped bool, argv ...string) int {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER, Pdeathsig: syscall.SIGKILL}
		if mapped { // the namespace root must be mapped to create a namespace below it
			cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: uid, Size: 1}}
			cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: gid, Size: 1}}
		}
		if err := cmd.Start(); err != nil {
			t.Skipf("unprivileged user namespaces are not available: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd.Process.Pid
	}
	rootless := spawn(false, "sleep", "60")
	cell := spawn(true, unshare, "--user", "sleep", "60")

	selfLink, err := os.Readlink("/proc/self/ns/user")
	if err != nil {
		t.Fatal(err)
	}
	selfNS, err := nsInode(selfLink, "user")
	if err != nil {
		t.Fatal(err)
	}
	judge := func(serviceUID uint32, pid int) (bool, []uint64, error) {
		reg := testReg
		reg.ServiceUID = serviceUID
		c := &capturer{root: "/proc", reg: reg, userNS: userNSAncestry, selfUserNS: selfNS}
		return c.cellUserNS(filepath.Join("/proc", strconv.Itoa(pid)))
	}
	// unshare execs sleep once it made the inner namespace.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ok, _, err := judge(uint32(uid), cell); err == nil && ok {
			break
		}
		if time.Now().After(deadline) {
			ok, chain, err := judge(uint32(uid), cell)
			t.Fatalf("the nested process never became cell identity: %v %v %v", ok, chain, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ok, chain, err := judge(uint32(uid), os.Getpid()); err != nil || ok || len(chain) != 0 {
		t.Errorf("this process: %v %v %v", ok, chain, err)
	}
	if ok, chain, err := judge(uint32(uid), rootless); err != nil || ok || len(chain) != 1 {
		t.Errorf("one level below: %v %v %v", ok, chain, err)
	}
	if ok, chain, err := judge(uint32(uid), cell); err != nil || !ok || len(chain) != 2 {
		t.Errorf("two levels below: %v %v %v", ok, chain, err)
	}
	if ok, _, err := judge(uint32(uid)+1, cell); err != nil || ok {
		t.Errorf("two levels below another uid's namespace: %v %v", ok, err)
	}
}

// A namespace inode number is reused as soon as the namespace is freed. A process whose link names the
// number of a cell namespace seen earlier in the same capture, but whose own walk finds a namespace of
// another owner, is judged by its walk: no verdict is shared between processes by inode number.
func TestCaptureReusedUserNSInodeIsJudgedByItsOwnWalk(t *testing.T) {
	const adminRootless, adminContainer uint64 = 4026532400, 4026532401
	f := newFakeProc(t)
	f.addUserNS(adminRootless, hostUserNS, 1000)
	f.addUserNS(adminContainer, adminRootless, 1000)
	f.addProc(500, 7000, "cell", "S", 1, fakeCell)
	f.addProcUID(1000, 610, 7100, "admin-ctr", "S", 1, "/user.slice")
	f.setUserNS(610, cellUserNS) // the number the freed cell namespace had
	f.walkAs[610] = adminContainer
	s := Capture(cellOf(500), f.view(), params(PointInterval))
	for _, p := range s.Processes {
		if p.PID == 610 {
			t.Fatalf("a process of another namespace with a reused inode number became cell identity: %+v", p)
		}
	}
}

// The identity rule is checked against the cell itself: a live cell member two or more user namespaces
// below the scanner's that the rule does not call cell identity (the service's topology or ServiceUID
// drifted, e.g. PrivateUsers= puts a root-owned namespace on top) is an identity gap, never a silent miss.
func TestCaptureCellMemberOutsideTheIdentityIsAnIdentityGap(t *testing.T) {
	const privateUsers, rootless, cell uint64 = 4026532500, 4026532501, 4026532502
	f := newFakeProc(t)
	f.addUserNS(privateUsers, hostUserNS, 0)
	f.addUserNS(rootless, privateUsers, fakeUID)
	f.addUserNS(cell, rootless, fakeUID)
	f.addProc(500, 7000, "cell", "S", 1, fakeCell)
	f.setUserNS(500, cell)
	var snaps []Snapshot
	for i, pt := range []Point{PointInterval, PointPostTeardown, PointFinalCleanup} {
		snaps = append(snaps, Capture(cellOf(500), f.view(), withSeq(params(pt), i+1)))
	}
	wantGapCode(t, snaps[0], DomainIdentity, "cell_member_not_identity")
	r := evalT(snaps, Policy{})
	if v := result(t, r, PredicateProc02).Verdict; v != VerdictEvidenceLost {
		t.Errorf("PROC-02 = %s, want evidence_lost", v)
	}
}

// The scanner's own user namespace is the anchor of the rule: unreadable, the scan has an identity gap.
func TestCaptureUnreadableScannerUserNSIsAnIdentityGap(t *testing.T) {
	f := newFakeProc(t)
	if err := os.Remove(filepath.Join(f.root, "999/ns/user")); err != nil {
		t.Fatal(err)
	}
	var snaps []Snapshot
	for i, pt := range []Point{PointInterval, PointPostTeardown, PointFinalCleanup} {
		snaps = append(snaps, Capture(cellOf(), f.view(), withSeq(params(pt), i+1)))
	}
	wantGapCode(t, snaps[2], DomainIdentity, "userns_unreadable")
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}
