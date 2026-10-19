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
// # Registration
//
// Registration is the one immutable record (cell id, interval start, teardown
// deadline, the dedicated cell identity CellUID/CellGID, the cell cgroup identity
// CellCgroupPath/CellCgroupInode, and MaxGap) that the
// oracle works from. The executor MUST take it from the launcher or service
// receipt, the party that created the cell and set the deadline, and hand the
// same value to Capture, StartMonitor and Evaluate. Every Snapshot carries
// Registration.Digest(); Evaluate rejects a snapshot or poll whose digest is
// missing or different (evidence_lost), so a deadline, an identity or a bound
// cannot be supplied from the capture side. The cgroup identity binds the registration
// to the observed cell: Capture refuses (cell_identity_mismatch) when the held
// cgroup has another path or directory inode, and Evaluate rejects any snapshot
// or poll that observed another.
//
// # Capture
//
// Capture (Linux) takes a held cell cgroup descriptor, a host /proc view and the
// Registration and records one Snapshot at one sampling point (matrix sections 2
// and 3). The processes it records are the union of the cell members, the
// launcher's (pid, start time) registry, and the identity scan: every process of
// the whole /proc whose real, effective or saved uid is CellUID, wherever its
// cgroup is. The registry is an additional source; completeness does not depend
// on it, so a double-fork descendant that left the cell before registration is
// still found by its uid. Sockets are those the observed processes hold, plus
// every inet socket whose /proc/net uid column is CellUID in the host view and in
// the network namespace of each live cell-identity process; unix sockets are
// matched by inode through the descriptors of cell-identity processes.
//
// The identity scan fails closed. It is only complete if /proc lists, pid 1 and
// the scanner's own pid (/proc/self) are listed, pid 1's status is readable (so
// hidepid=1/2/ptraceable, which hide other users' processes, is detected
// functionally; a hidepid mount that exempts the scanner passes) and the status
// of every listed pid is readable. Anything else is a DomainIdentity gap and the
// verdicts are evidence_lost. Reading the descriptors and namespaces of
// cell-identity processes needs ptrace-read access to them (root or
// CAP_SYS_PTRACE when the scanner is another uid); without it the gaps make the
// verdicts evidence_lost.
//
// # Monitor
//
// Monitor (Linux) is the continuous-coverage witness of EFF-PROC-02: a
// goroutine that runs the identity scan every Interval and appends each poll to
// an append-only record (sequence, time). Evaluate accepts no_effect for
// EFF-PROC-02 only if the record has no sequence hole, no poll has a gap, poll
// and snapshot times are strictly increasing, the post-teardown snapshot is
// after the interval start, the first poll is at most Registration.MaxGap after
// it, and no stretch (negative or longer than Registration.MaxGap) lies between
// the interval start, the polls and the post-teardown snapshot. Otherwise EFF-PROC-02 is evidence_lost.
//
// # Evaluate
//
// Evaluate turns the registration, the snapshots and the monitor record into a
// Report with one verdict per predicate: no_effect, effect_present or
// evidence_lost.
//
// Fail-closed rules. no_effect is returned only from complete evidence. A
// missing registry, an incomplete identity scan, an unreadable /proc entry, a
// cell cgroup that vanished without a drain proof, a snapshot sequence gap, a
// missing sampling point, a missing or foreign registration digest, or a
// teardown deadline that was not reached all give evidence_lost for the
// predicates they affect. An observed effect stands even when other evidence is
// lost (the runner's rule: an observed effect always wins).
//
// # The lingering cell init (no-reap/v1)
//
// A no-reap/v1 command cell keeps its init alive after the command exits, so the
// init is not a surviving descendant. Policy.LingeringInit (set by the collector
// for the P half of a no-reap/v1 case only) makes EFF-PROC-01 skip it. The guest
// observer has no launcher registry, so the init is recognised by what /proc says
// (cellInit): pid 1 of a PID namespace below the observer's (Process.NSPid), the
// entrypoint's comm, and no observed parent. A survivor that made a PID namespace
// of its own has an observed parent and still counts, as does everything that
// descends from the init. The report records the policy (Report.LingeringInit).
//
// # Stated limits
//
//   - A boundary crossing that begins and ends between two polls (shorter than
//     the poll spacing, at most MaxGap) is not seen by EFF-PROC-02.
//   - A cell-identity process that is outside the cell cgroup only transiently at
//     startup (forked as the cell uid before it is placed in the cell cgroup) is
//     an EFF-PROC-02 finding; the launcher must place cell processes in the cell
//     cgroup at creation.
//   - The identity is assumed to be used by one cell at a time (the dedicated
//     cell identity of the registration); a second concurrent cell with the same
//     uid would be reported as an escape.
//   - A network namespace that no process of the cell identity holds, and a
//     socket that no /proc table or descriptor accounts for, are not visible.
//     Closing orphan sockets (FIN_WAIT) of a dead cell process may be reported by
//     EFF-PROC-03 until the kernel drops them.
//   - Unix-domain socket files left on disk are covered by policy and runtime
//     trace, not by this oracle.
package procoracle
