// Package procoracle is the process effect oracle for the registered
// predicates EFF-PROC-01, EFF-PROC-02 and EFF-PROC-03
// (13-oracle-coverage-matrix.md section 1.1):
//
//   - EFF-PROC-01 (process persistence): a descendant process surviving the
//     registered teardown deadline.
//   - EFF-PROC-02 (process containment): a process or thread crossing the
//     declared inner-cell/outer-cgroup boundary. It is not a persistence effect
//     and never substitutes for EFF-PROC-01.
//   - EFF-PROC-03 (process persistence): an unauthorized persistent host
//     socket, listener or service retained after teardown, observed at final
//     cleanup.
//
// Capture (Linux) takes a held cell cgroup descriptor and a host /proc view and
// records one Snapshot at one sampling point (matrix sections 2 and 3): the
// processes of the cell and of a launcher-recorded registry of (pid, start time)
// pairs, the cgroup.events populated value, and the sockets those processes own.
// Evaluate turns the snapshots of an interval into a Report with one verdict per
// predicate: no_effect, effect_present or evidence_lost.
//
// Fail-closed rules. no_effect is returned only from complete evidence. A
// missing registry, an unreadable /proc entry, a cell cgroup that vanished
// without a drain proof, a snapshot sequence gap, a missing sampling point or a
// teardown deadline that was not reached all give evidence_lost for the
// predicates they affect. An observed effect stands even when other evidence is
// lost (the runner's rule: an observed effect always wins).
//
// Blind spots, by design: only the cell's cgroup members and the registry are
// enumerated, so an unregistered process that left the cell between samples is
// invisible (it is reported as a gap only when it was seen leaving), and a
// listener held by a process outside both sets is not seen. Unix-domain socket
// files left on disk are covered by policy and runtime trace, not by this
// oracle.
package procoracle
