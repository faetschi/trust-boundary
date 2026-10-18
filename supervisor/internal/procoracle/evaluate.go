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
	OracleVersion string        `json:"oracle_version"`
	Scope         string        `json:"scope"`
	Instrument    string        `json:"instrument"`
	CellPath      string        `json:"cell_path"`
	Snapshots     []SnapshotRef `json:"snapshots"`
	Results       []Result      `json:"results"`
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

// Evaluate turns the snapshots of one interval into a Report with one Result
// for each of EFF-PROC-01..03, in that order.
//
// Rules (13-oracle-coverage-matrix.md sections 2 and 3):
//
//   - Snapshots must be numbered 1..n without a hole, name one cell and one PID
//     namespace, and carry a recorded registry and no gap; otherwise the
//     predicates they affect are evidence_lost.
//   - A cell that no longer exists reads as empty only with a drain proof; a
//     populated cell with no visible member (or the reverse) is inconsistent.
//   - EFF-PROC-01 reads the post-teardown and final-cleanup snapshots: a live
//     process of the cell, or a registered one, observed at or after the
//     registered teardown deadline is a surviving descendant. Both points and
//     the deadline are required for no_effect.
//   - EFF-PROC-02 reads every snapshot: a registered process outside the cell
//     cgroup (or a thread of a cell process outside it) has crossed the
//     boundary. no_effect needs an interval sample and the post-teardown point.
//   - EFF-PROC-03 reads the final-cleanup snapshots: a listener, bound socket or
//     connection held by a surviving process and not authorized by the policy.
//   - An observed effect stands even if other evidence is lost; no_effect never
//     does.
func Evaluate(snaps []Snapshot, policy Policy) Report {
	ordered := slices.Clone(snaps)
	slices.SortStableFunc(ordered, func(a, b Snapshot) int { return a.Seq - b.Seq })

	rep := Report{OracleVersion: OracleVersion, Scope: ScopeUnitCheck, Instrument: Instrument, Snapshots: []SnapshotRef{}}
	for _, s := range ordered {
		rep.Snapshots = append(rep.Snapshots, SnapshotRef{Seq: s.Seq, Point: s.Point, Digest: s.Digest()})
		if rep.CellPath == "" {
			rep.CellPath = s.CellPath
		}
	}
	// A removed cell cannot name itself; it is the cell the other snapshots name.
	for i := range ordered {
		if ordered[i].CellRemoved && ordered[i].CellPath == "" {
			ordered[i].CellPath = rep.CellPath
		}
	}
	common := commonGaps(ordered)
	rep.Results = []Result{
		evalProc01(ordered, common),
		evalProc02(ordered, common),
		evalProc03(ordered, common, policy),
	}
	return rep
}

func gapf(domain, code, format string, args ...any) Gap {
	return Gap{Domain: domain, Code: code, Detail: fmt.Sprintf(format, args...)}
}

// commonGaps are the gaps found across and inside the snapshots. Each predicate
// keeps only the domains that affect it.
func commonGaps(snaps []Snapshot) []Gap {
	if len(snaps) == 0 {
		return []Gap{gapf(DomainPoint, "no_snapshots", "no snapshot was taken")}
	}
	var gaps []Gap
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
		if !s.Registry.Recorded {
			gaps = append(gaps, gapf(DomainRegistry, "registry_missing", "snapshot %d has no recorded pid/start-time registry", s.Seq))
		}
		for _, g := range s.Gaps {
			g.Detail = fmt.Sprintf("snapshot %d: %s", s.Seq, g.Detail)
			gaps = append(gaps, g)
		}
		gaps = append(gaps, consistencyGaps(s)...)
	}
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

func processFinding(s Snapshot, p Process) Finding {
	return Finding{
		Point: s.Point, Seq: s.Seq, Process: p.ID(), Comm: p.Comm, CellPath: s.CellPath, CgroupPath: p.CgroupPath,
		ObservedAt: s.ObservedAt, Deadline: s.Deadline,
	}
}

func evalProc01(snaps []Snapshot, common []Gap) Result {
	gaps := applicable(common, DomainCell, DomainRegistry, DomainProcess, DomainSequence, DomainPoint)
	gaps = append(gaps, needPoint(snaps, PointPostTeardown)...)
	gaps = append(gaps, needPoint(snaps, PointFinalCleanup)...)
	var findings []Finding
	for _, s := range atPoint(snaps, PointPostTeardown, PointFinalCleanup) {
		switch {
		case s.Deadline.IsZero():
			gaps = append(gaps, gapf(DomainDeadline, "deadline_missing", "snapshot %d has no registered teardown deadline", s.Seq))
			continue
		case s.ObservedAt.Before(s.Deadline):
			gaps = append(gaps, gapf(DomainDeadline, "observed_before_deadline", "snapshot %d was taken before the teardown deadline", s.Seq))
			continue
		}
		for _, p := range s.Processes {
			if p.Live() && (p.InCell || p.Registered) {
				findings = append(findings, processFinding(s, p))
			}
		}
	}
	return conclude(PredicateProc01, findings, gaps)
}

func evalProc02(snaps []Snapshot, common []Gap) Result {
	gaps := applicable(common, DomainCell, DomainRegistry, DomainProcess, DomainSequence, DomainPoint)
	gaps = append(gaps, needPoint(snaps, PointPostTeardown)...)
	gaps = append(gaps, needPoint(snaps, PointInterval)...)
	var findings []Finding
	for _, s := range snaps {
		for _, p := range s.Processes {
			if p.Registered && !p.InCell {
				findings = append(findings, processFinding(s, p))
			}
			if p.InCell {
				for _, th := range p.OutThreads {
					f := processFinding(s, p)
					f.ThreadID, f.CgroupPath = th.TID, th.CgroupPath
					findings = append(findings, f)
				}
			}
		}
	}
	return conclude(PredicateProc02, findings, gaps)
}

func evalProc03(snaps []Snapshot, common []Gap, policy Policy) Result {
	gaps := applicable(common, DomainCell, DomainRegistry, DomainProcess, DomainSequence, DomainPoint, DomainSocket)
	gaps = append(gaps, needPoint(snaps, PointFinalCleanup)...)
	var findings []Finding
	for _, s := range atPoint(snaps, PointFinalCleanup) {
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
			f := processFinding(s, owner)
			sock := so
			f.Socket = &sock
			findings = append(findings, f)
		}
	}
	return conclude(PredicateProc03, findings, gaps)
}
