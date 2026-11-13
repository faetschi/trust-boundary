package procoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Policy is the evaluator's authorization input for EFF-PROC-03.
type Policy struct {
	// AuthorizedSockets are "proto local" pairs (for example "tcp 127.0.0.1:8080")
	// that a survivor may hold without EFF-PROC-03.
	AuthorizedSockets []string
	// LingeringInit says the run is a no-reap/v1 one, whose command cells keep their init alive on purpose.
	// EFF-PROC-01 then does not count that init as a surviving descendant (cellInit).
	LingeringInit bool
	// D49. Unresolved lists missing or contradicting closure evidence: every predicate then has a gap, so none can be
	// no_effect (an observed effect still stands). CoverageDefects are defects of how the monitor record ended; they
	// are coverage gaps (EFF-PROC-02 only). Extra holds observations that are valid on their own but belong to no
	// numbered point (the closure scan): they can only add findings to EFF-PROC-01 and EFF-PROC-02, never gaps or clean
	// results.
	Unresolved      []string
	CoverageDefects []string
	Extra           []Snapshot
	// Tail are the polls observed after the closure scan, whenever a closure document was obtained: monitor records
	// already judged for EFF-PROC-02, whose live survivors are additionally EFF-PROC-01 findings (observed after the
	// deadline).
	Tail []Snapshot
}

// Finding is one observed effect with the matrix's required evidence fields.
type Finding struct {
	Point      Point     `json:"point"`
	Seq        int       `json:"seq"`
	Process    ProcID    `json:"process"`
	Comm       string    `json:"comm"`
	CellPath   string    `json:"cell_path"`
	CgroupPath string    `json:"cgroup_path"`
	ThreadID   int       `json:"thread_id,omitempty"`
	Socket     *Socket   `json:"socket,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	Deadline   time.Time `json:"deadline"`
}

// Result is the verdict for one predicate.
type Result struct {
	Predicate string    `json:"predicate"`
	Verdict   string    `json:"verdict"`
	Findings  []Finding `json:"findings"`
	Gaps      []Gap     `json:"gaps"`
}

// SnapshotRef binds a report to the snapshot it evaluated.
type SnapshotRef struct {
	Seq    int    `json:"seq"`
	Point  Point  `json:"point"`
	Digest string `json:"digest"`
}

// Report is the complete record of one evaluation. Its JSON encoding is
// deterministic: fields appear in declaration order, lists are never null, and
// no map is encoded.
type Report struct {
	OracleVersion string `json:"oracle_version"`
	Scope         string `json:"scope"`
	Instrument    string `json:"instrument"`
	CellPath      string `json:"cell_path"`
	// Registration is the digest of the registration the evaluation ran under.
	Registration string `json:"registration"`
	// LingeringInit is Policy.LingeringInit: the evaluation excluded the cell init from EFF-PROC-01.
	LingeringInit bool          `json:"lingering_init"`
	Snapshots     []SnapshotRef `json:"snapshots"`
	// Polls are the monitor records presented as the coverage witness.
	Polls   []SnapshotRef `json:"polls"`
	Results []Result      `json:"results"`
}

// Digest is the hex SHA-256 of the report's compact JSON.
func (r Report) Digest() (string, error) {
	n := r.normalized()
	b, err := json.Marshal(&n)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (r Report) normalized() Report {
	r.Snapshots = nonNil(r.Snapshots)
	r.Polls = nonNil(r.Polls)
	r.Results = append([]Result{}, r.Results...)
	for i := range r.Results {
		r.Results[i].Findings = append([]Finding{}, r.Results[i].Findings...)
		for j := range r.Results[i].Findings {
			f := &r.Results[i].Findings[j]
			f.ObservedAt, f.Deadline = f.ObservedAt.UTC(), f.Deadline.UTC()
		}
		r.Results[i].Gaps = nonNil(r.Results[i].Gaps)
	}
	return r
}

// Evaluate turns the snapshots and the monitor record of one interval into a
// Report with one Result for each of EFF-PROC-01..03, in that order.
//
// reg is the registration the executor took from the launcher or service
// receipt. It is an input of its own, never read from a snapshot: the deadline,
// the cell identity and the coverage bound come from it. A snapshot or poll whose
// RegistrationDigest is missing or differs from reg.Digest() is rejected: it is
// left out of the evaluation, and the registration gap makes the predicates
// evidence_lost unless an effect was observed elsewhere.
//
// Rules (13-oracle-coverage-matrix.md sections 2 and 3):
//
//   - Every snapshot and poll must have observed the registered cell: the cgroup
//     path and directory inode of reg (a removed cell: the inode alone).
//   - Snapshots must be numbered 1..n without a hole, name one cell and one PID
//     namespace, carry a recorded registry, a performed identity scan and no gap;
//     otherwise the predicates they affect are evidence_lost.
//   - A cell that no longer exists reads as empty only with a drain proof; a
//     populated cell with no visible member (or the reverse) is inconsistent.
//   - EFF-PROC-01 reads the post-teardown and final-cleanup snapshots: a live
//     process of the cell, a registered one, or any live process of the cell
//     identity (anywhere in /proc), observed at or after reg.Deadline is a
//     surviving descendant. Both points and the deadline are required. The
//     closure scan (Policy.Extra) and the polls after it (Policy.Tail), when
//     the early close supplied them, are read the same way but add findings
//     only, never gaps.
//   - EFF-PROC-02 reads every snapshot and every poll: a registered process or a
//     process of the cell identity outside the cell cgroup (or a thread of a cell
//     process outside it; a cgroup nested below the cell is inside) has crossed
//     the boundary. no_effect additionally needs the post-teardown point and
//     continuous coverage: polls numbered 1..n, no poll with a gap, the first poll
//     at most reg.MaxGap after IntervalStart, poll times and snapshot times
//     strictly increasing, a post-teardown snapshot strictly after IntervalStart,
//     and no stretch (negative or longer than reg.MaxGap) between IntervalStart,
//     the polls and the post-teardown snapshot. Without a monitor record EFF-PROC-02 is at best
//     evidence_lost. A crossing shorter than MaxGap can be missed (stated limit).
//   - EFF-PROC-03 reads the final-cleanup snapshots: a listener, bound socket or
//     connection held by a surviving process, or an inet socket of a cell's own
//     network namespace without a holder, not authorized by the policy.
//   - An observed effect stands even if other evidence is lost; no_effect never
//     does.
func Evaluate(reg Registration, snaps, polls []Snapshot, policy Policy) Report {
	digest := reg.Digest()
	rep := Report{OracleVersion: OracleVersion, Scope: ScopeUnitCheck, Instrument: Instrument, Registration: digest, LingeringInit: policy.LingeringInit,
		Snapshots: []SnapshotRef{}, Polls: []SnapshotRef{}}

	ordered, orderedPolls := bySeq(snaps), bySeq(polls)
	for _, s := range ordered {
		rep.Snapshots = append(rep.Snapshots, SnapshotRef{Seq: s.Seq, Point: s.Point, Digest: s.Digest()})
	}
	for _, s := range orderedPolls {
		rep.Polls = append(rep.Polls, SnapshotRef{Seq: s.Seq, Point: s.Point, Digest: s.Digest()})
	}
	ordered, rejected := accept(digest, ordered, "snapshot")
	orderedPolls, rejectedPolls := accept(digest, orderedPolls, "poll")
	rep.CellPath = firstCellPath(ordered)
	// A removed cell cannot name itself; it is the cell the other snapshots name.
	for i := range ordered {
		if ordered[i].CellRemoved && ordered[i].CellPath == "" {
			ordered[i].CellPath = rep.CellPath
		}
	}
	e := &evaluation{reg: reg, snaps: ordered, polls: orderedPolls, lingeringInit: policy.LingeringInit}
	for _, s := range policy.Tail {
		if s.RegistrationDigest == digest && reg.cellMatches(s.CellPath, s.CellInode, s.CellRemoved) && s.IdentityScanned {
			e.tail = append(e.tail, s)
		}
	}
	for _, s := range policy.Extra {
		if s.RegistrationDigest == digest && reg.cellMatches(s.CellPath, s.CellInode, s.CellRemoved) && s.IdentityScanned {
			e.extra = append(e.extra, s)
		}
	}
	e.common = append(commonGaps(reg, ordered), rejected...)
	for _, r := range policy.Unresolved {
		e.common = append(e.common, gapf(DomainProcess, "closure_unresolved", "%s", r))
	}
	e.coverage = append(rejectedPolls, e.coverageGaps(rep.CellPath)...)
	for _, d := range policy.CoverageDefects {
		e.coverage = append(e.coverage, gapf(DomainCoverage, "monitor_record_defect", "%s", d))
	}
	rep.Results = []Result{e.proc01(), e.proc02(), e.proc03(policy)}
	return rep
}

func firstCellPath(snaps []Snapshot) string {
	for _, s := range snaps {
		if s.CellPath != "" {
			return s.CellPath
		}
	}
	return ""
}

// CoverageDefects is the evaluator's own validation of the monitor record as the coverage witness of EFF-PROC-02:
// registration and cell identity of every poll, sequence numbering, scan state and gaps of each poll, the first poll's
// distance from the interval start and every stretch up to the post-teardown snapshot. The NET finalizer uses it so
// that a record the evaluator rejects cannot let a NET window close early.
func CoverageDefects(reg Registration, snaps, polls []Snapshot) []string {
	digest := reg.Digest()
	ordered, _ := accept(digest, bySeq(snaps), "snapshot")
	orderedPolls, rejectedPolls := accept(digest, bySeq(polls), "poll")
	e := &evaluation{reg: reg, snaps: ordered, polls: orderedPolls}
	var out []string
	for _, g := range append(rejectedPolls, e.coverageGaps(firstCellPath(ordered))...) {
		out = append(out, g.Code+": "+g.Detail)
	}
	return out
}

func bySeq(in []Snapshot) []Snapshot {
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b Snapshot) int { return a.Seq - b.Seq })
	return out
}

// accept splits off the snapshots that do not carry the registration digest.
// Their content is not evaluated; each is a registration gap.
func accept(digest string, in []Snapshot, what string) (kept []Snapshot, gaps []Gap) {
	for _, s := range in {
		switch {
		case s.RegistrationDigest == "":
			gaps = append(gaps, gapf(DomainRegistration, "registration_digest_missing", "%s %d carries no registration digest", what, s.Seq))
		case s.RegistrationDigest != digest:
			gaps = append(gaps, gapf(DomainRegistration, "registration_digest_mismatch",
				"%s %d was captured under another registration (another deadline, identity or cell)", what, s.Seq))
		default:
			kept = append(kept, s)
		}
	}
	return kept, gaps
}

type evaluation struct {
	reg           Registration
	lingeringInit bool
	snaps         []Snapshot
	polls         []Snapshot
	extra         []Snapshot
	tail          []Snapshot
	common        []Gap
	// coverage are the gaps of the monitor record (EFF-PROC-02 only).
	coverage []Gap
}

func gapf(domain, code, format string, args ...any) Gap {
	return Gap{Domain: domain, Code: code, Detail: fmt.Sprintf(format, args...)}
}

// commonGaps are the gaps found across and inside the snapshots and in the
// registration. Each predicate keeps only the domains that affect it.
func commonGaps(reg Registration, snaps []Snapshot) []Gap {
	gaps := reg.gaps()
	if len(snaps) == 0 {
		return append(gaps, gapf(DomainPoint, "no_snapshots", "no snapshot was taken"))
	}
	for i, s := range snaps {
		if s.Seq != i+1 {
			gaps = append(gaps, gapf(DomainSequence, "sequence_gap", "snapshot %d where %d was expected", s.Seq, i+1))
			break
		}
	}
	for _, s := range snaps {
		if s.CellPath != snaps[0].CellPath || s.PIDNamespace != snaps[0].PIDNamespace {
			gaps = append(gaps, gapf(DomainCell, "cell_mismatch", "snapshot %d names another cell or PID namespace", s.Seq))
		}
		if !reg.cellMatches(s.CellPath, s.CellInode, s.CellRemoved) {
			gaps = append(gaps, gapf(DomainCell, "cell_identity_mismatch",
				"snapshot %d observed cgroup %q inode %d, not the registered cell", s.Seq, s.CellPath, s.CellInode))
		}
		if !s.Registry.Recorded {
			gaps = append(gaps, gapf(DomainRegistry, "registry_missing", "snapshot %d has no recorded pid/start-time registry", s.Seq))
		}
		if !s.IdentityScanned || s.ScannedPIDs == 0 {
			gaps = append(gaps, gapf(DomainIdentity, "identity_scan_missing", "snapshot %d has no completed scan of /proc for the cell identity", s.Seq))
		}
		for _, g := range s.Gaps {
			g.Detail = fmt.Sprintf("snapshot %d: %s", s.Seq, g.Detail)
			gaps = append(gaps, g)
		}
		gaps = append(gaps, consistencyGaps(s)...)
	}
	return gaps
}

// coverageGaps decides whether the polls witness the interval continuously. A
// defect of the record is a coverage gap: EFF-PROC-02 only.
func (e *evaluation) coverageGaps(cellPath string) []Gap {
	var gaps []Gap
	lost := func(code, format string, args ...any) {
		gaps = append(gaps, gapf(DomainCoverage, code, format, args...))
	}
	if len(e.polls) == 0 {
		lost("no_monitor", "no monitor record: the snapshots alone are sparse samples, not continuous coverage of the interval")
		return gaps
	}
	for i, s := range e.polls {
		if s.Seq != i+1 {
			lost("poll_sequence_gap", "poll %d where %d was expected", s.Seq, i+1)
			break
		}
	}
	for _, s := range e.polls {
		switch {
		case !s.Registry.Recorded:
			lost("poll_registry_missing", "poll %d has no recorded registry", s.Seq)
		case !s.IdentityScanned || s.ScannedPIDs == 0:
			lost("poll_identity_scan_missing", "poll %d has no completed scan of /proc for the cell identity", s.Seq)
		case s.CellPath != cellPath && !(s.CellRemoved && s.CellPath == ""):
			lost("poll_cell_mismatch", "poll %d names another cell", s.Seq)
		case !e.reg.cellMatches(s.CellPath, s.CellInode, s.CellRemoved):
			lost("poll_cell_identity_mismatch", "poll %d observed cgroup %q inode %d, not the registered cell", s.Seq, s.CellPath, s.CellInode)
		}
		for _, g := range s.Gaps {
			lost("poll_"+g.Code, "poll %d: %s (%s)", s.Seq, g.Detail, g.Domain)
		}
	}
	if e.reg.MaxGap <= 0 || e.reg.IntervalStart.IsZero() {
		lost("coverage_unverifiable", "the registration has no MaxGap or interval start to bound the coverage")
		return gaps
	}
	var end time.Time
	for _, s := range atPoint(e.snaps, PointPostTeardown) {
		if s.ObservedAt.After(end) {
			end = s.ObservedAt
		}
	}
	// Snapshot times must be strictly increasing in sequence order.
	for i := 1; i < len(e.snaps); i++ {
		if !e.snaps[i].ObservedAt.After(e.snaps[i-1].ObservedAt) {
			lost("snapshot_time_regressed", "snapshot %d was not observed after snapshot %d", e.snaps[i].Seq, e.snaps[i-1].Seq)
		}
	}
	if end.IsZero() {
		return gaps // the missing post-teardown point is reported by needPoint
	}
	// The post-teardown snapshot ends the interval, so it must be after its start;
	// a negative stretch would otherwise pass the MaxGap comparison below.
	if !end.After(e.reg.IntervalStart) {
		lost("post_teardown_not_after_start", "the post-teardown snapshot (%s) is not after the interval start (%s)",
			end.Format(time.RFC3339Nano), e.reg.IntervalStart.UTC().Format(time.RFC3339Nano))
		return gaps
	}
	// The first poll must come within MaxGap of the interval start. A poll from
	// before the interval is allowed (it covers none of it).
	if first := e.polls[0].ObservedAt; first.After(e.reg.IntervalStart.Add(e.reg.MaxGap)) {
		lost("first_poll_late", "the first poll (%s) is more than MaxGap %s after the interval start",
			first.Format(time.RFC3339Nano), e.reg.MaxGap)
	}
	// stretch checks one uncovered stretch: never negative, never above MaxGap.
	stretch := func(d time.Duration, format string, args ...any) {
		switch {
		case d < 0:
			lost("coverage_negative_gap", format+": a negative stretch", args...)
		case d > e.reg.MaxGap:
			lost("coverage_gap", "%s "+format+" (MaxGap %s)", append(append([]any{d}, args...), e.reg.MaxGap)...)
		}
	}
	// Walk the timeline from the interval start through the polls to the
	// post-teardown snapshot; no stretch may exceed MaxGap. Polls from before the
	// interval cover none of it, and polls at or after the post-teardown snapshot
	// cover none of the stretch that ends there.
	prev := e.reg.IntervalStart
	for i, s := range e.polls {
		if i > 0 && !s.ObservedAt.After(e.polls[i-1].ObservedAt) {
			lost("poll_time_regressed", "poll %d was not observed after poll %d", s.Seq, s.Seq-1)
		}
		if s.ObservedAt.Before(prev) || !s.ObservedAt.Before(end) {
			continue
		}
		stretch(s.ObservedAt.Sub(prev), "without a poll before poll %d", s.Seq)
		prev = s.ObservedAt
	}
	stretch(end.Sub(prev), "between the last poll and the post-teardown snapshot")
	return gaps
}

func validProof(p string) bool {
	if len(p) != 64 {
		return false
	}
	for _, c := range p {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func consistencyGaps(s Snapshot) []Gap {
	var gaps []Gap
	if s.CellPath == "" && !s.CellRemoved {
		gaps = append(gaps, gapf(DomainCell, "cell_path_missing", "snapshot %d names no cell cgroup", s.Seq))
	}
	inCell := 0
	for _, p := range s.Processes {
		if p.InCell {
			inCell++
		}
	}
	switch {
	case s.CellRemoved:
		if !validProof(s.DrainProof) {
			gaps = append(gaps, gapf(DomainCell, "removed_without_drain_proof",
				"snapshot %d: the cell cgroup no longer exists and no drain proof licenses reading it as empty", s.Seq))
		}
		if s.Populated || inCell > 0 {
			gaps = append(gaps, gapf(DomainCell, "removed_cell_not_empty", "snapshot %d: a removed cell still shows processes", s.Seq))
		}
	case s.Populated && inCell == 0:
		gaps = append(gaps, gapf(DomainCell, "populated_without_visible_member",
			"snapshot %d: cgroup.events says populated but no member was observed", s.Seq))
	case !s.Populated && inCell > 0:
		gaps = append(gaps, gapf(DomainCell, "members_in_unpopulated_cell",
			"snapshot %d: members observed in a cell that cgroup.events says is empty", s.Seq))
	}
	return gaps
}

// applicable keeps the gaps of the given domains.
func applicable(gaps []Gap, domains ...string) []Gap {
	out := []Gap{}
	for _, g := range gaps {
		if slices.Contains(domains, g.Domain) {
			out = append(out, g)
		}
	}
	return out
}

func conclude(predicate string, findings []Finding, gaps []Gap) Result {
	r := Result{Predicate: predicate, Findings: nonNil(findings), Gaps: nonNil(gaps)}
	switch {
	case len(findings) > 0:
		r.Verdict = VerdictEffectPresent
	case len(gaps) > 0:
		r.Verdict = VerdictEvidenceLost
	default:
		r.Verdict = VerdictNoEffect
	}
	return r
}

func atPoint(snaps []Snapshot, points ...Point) []Snapshot {
	var out []Snapshot
	for _, s := range snaps {
		if slices.Contains(points, s.Point) {
			out = append(out, s)
		}
	}
	return out
}

func needPoint(snaps []Snapshot, p Point) []Gap {
	if len(atPoint(snaps, p)) == 0 {
		return []Gap{gapf(DomainPoint, "missing_"+string(p), "no %s snapshot", p)}
	}
	return nil
}

func (e *evaluation) processFinding(s Snapshot, p Process) Finding {
	return Finding{
		Point: s.Point, Seq: s.Seq, Process: p.ID(), Comm: p.Comm, CellPath: s.CellPath, CgroupPath: p.CgroupPath,
		ObservedAt: s.ObservedAt, Deadline: e.reg.Deadline,
	}
}

// everyDomain are the gap domains that affect every predicate.
var everyDomain = []string{DomainCell, DomainRegistry, DomainProcess, DomainSequence, DomainPoint, DomainIdentity, DomainRegistration}

func (e *evaluation) proc01() Result {
	gaps := applicable(e.common, append([]string{DomainDeadline}, everyDomain...)...)
	gaps = append(gaps, needPoint(e.snaps, PointPostTeardown)...)
	gaps = append(gaps, needPoint(e.snaps, PointFinalCleanup)...)
	var findings []Finding
	for _, s := range atPoint(e.snaps, PointPostTeardown, PointFinalCleanup) {
		if e.reg.Deadline.IsZero() {
			continue // already a deadline_missing gap
		}
		if s.ObservedAt.Before(e.reg.Deadline) {
			gaps = append(gaps, gapf(DomainDeadline, "observed_before_deadline", "snapshot %d was taken before the teardown deadline", s.Seq))
			continue
		}
		for _, p := range s.Processes {
			if p.Live() && (p.InCell || p.Registered || p.CellIdentity) && !(e.lingeringInit && cellInit(s.Processes, p)) {
				findings = append(findings, e.processFinding(s, p))
			}
		}
	}
	for _, s := range slices.Concat(e.extra, e.tail) {
		if e.reg.Deadline.IsZero() || s.ObservedAt.Before(e.reg.Deadline) {
			continue
		}
		for _, p := range s.Processes {
			if p.Live() && (p.InCell || p.Registered || p.CellIdentity) && !(e.lingeringInit && cellInit(s.Processes, p)) {
				findings = append(findings, e.processFinding(s, p))
			}
		}
	}
	return conclude(PredicateProc01, findings, gaps)
}

// cellInit reports whether p is the init of a cell's PID namespace, the one process no-reap/v1 keeps
// alive on purpose (cmd/tbound-cell-entrypoint lingerAsInit). The guest observer has no launcher registry
// and no recorded pid of the init, so the identity is rebuilt from facts a descendant cannot all
// forge: p is pid 1 of a PID namespace below the observer's (the last NSpid entry is 1, with at least
// two entries), it carries the entrypoint's comm, and its parent is not an observed process, so it is the
// root of the cell's process tree as the host sees it (its parent is the runtime monitor). A survivor
// that made a PID namespace of its own and renamed itself is the init of that namespace, but its parent
// is a process of the cell. A process outside every PID namespace has a single NSpid entry. The comm is
// the weakest of the three and is only a guard against another namespace init; a snapshot without an NSpid
// record never matches, so an old or incomplete snapshot counts the process.
func cellInit(procs []Process, p Process) bool {
	n := len(p.NSPid)
	if n < 2 || p.NSPid[n-1] != 1 || p.Comm != CellInitComm {
		return false
	}
	return !slices.ContainsFunc(procs, func(q Process) bool { return q.PID == p.PPID })
}

func (e *evaluation) proc02() Result {
	gaps := applicable(e.common, everyDomain...)
	gaps = append(gaps, needPoint(e.snaps, PointPostTeardown)...)
	gaps = append(gaps, e.coverage...)
	var findings []Finding
	for _, s := range slices.Concat(e.snaps, e.polls, e.extra) {
		for _, p := range s.Processes {
			if (p.Registered || p.CellIdentity) && !p.InCell {
				findings = append(findings, e.processFinding(s, p))
			}
			if p.InCell {
				for _, th := range p.OutThreads {
					if underCell(th.CgroupPath, s.CellPath) {
						continue // a cgroup nested below the cell is inside the cell
					}
					f := e.processFinding(s, p)
					f.ThreadID, f.CgroupPath = th.TID, th.CgroupPath
					findings = append(findings, f)
				}
			}
		}
	}
	return conclude(PredicateProc02, findings, gaps)
}

func (e *evaluation) proc03(policy Policy) Result {
	gaps := applicable(e.common, append([]string{DomainSocket}, everyDomain...)...)
	gaps = append(gaps, needPoint(e.snaps, PointFinalCleanup)...)
	var findings []Finding
	for _, s := range atPoint(e.snaps, PointFinalCleanup) {
		for _, so := range s.Sockets {
			if so.Role != RoleListener && so.Role != RoleConnected {
				continue
			}
			if slices.Contains(policy.AuthorizedSockets, so.Proto+" "+so.Local) {
				continue
			}
			owner := Process{PID: so.Owner.PID, StartTicks: so.Owner.StartTicks}
			for _, p := range s.Processes {
				if p.ID() == so.Owner {
					owner = p
				}
			}
			if !owner.Live() {
				continue
			}
			f := e.processFinding(s, owner)
			sock := so
			f.Socket = &sock
			findings = append(findings, f)
		}
	}
	return conclude(PredicateProc03, findings, gaps)
}
