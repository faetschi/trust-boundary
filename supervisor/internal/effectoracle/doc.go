// Package effectoracle is an independent, Linux-only transient-mutation
// observer for evidence contract E04 (MSE_MA_Thesis/tbound/AH-technical/
// 11-evidence-contract.md, "E04 — temporal workspace predicate").
//
// Start watches a directory tree with inotify and records each kernel event in
// an append-only log. Sequence numbers increase strictly across events, gaps,
// heartbeats, and marks. Mark records a named observation point together with a
// SHA-256 snapshot of the tree. Stop returns a Report with the events, the
// snapshots, the explicit gaps, and a verdict. Report.TransientMutations derives
// create-then-delete and modify-then-revert candidates. An endpoint-only
// comparison cannot see either one.
//
// The package imports nothing from the TBound store, gate, delta, publication,
// or audit code. Its tests verify this code only. They do not establish G1,
// containment, or H1.
//
// # Liveness
//
// Start, every Mark, and Stop write a uniquely named canary file in the root,
// wait until the reader observes its close_write, and then remove it. Each
// attempt is recorded as a heartbeat. Canary events are filtered out of Events,
// and the canary names never appear in the report. A canary that is not
// observed within the timeout, or that is attempted after the reader has
// stopped, is recorded as unobserved. Unless a gap is also recorded, the verdict
// is then instrument_unqualified (see Verdicts). inotify reports events in queue
// order, so an observed canary also shows that the kernel events of operations
// completed before the canary was written have been read. This holds unless the
// queue overflowed.
//
// # Loss and coverage
//
// Every loss or coverage gap is an explicit Gap record, and no gap is ever
// dropped. Gaps cover:
//   - kernel queue overflow (IN_Q_OVERFLOW). The tree is re-watched, and the
//     window is evidence_lost;
//   - a failed read or a malformed inotify record. The reader stops;
//   - the root deleted, moved, or unmounted;
//   - watch-limit exhaustion (ENOSPC) and other watch or enumeration failures;
//   - a directory created or moved in before its watch was installed
//     (subdir_race). The new subtree is enumerated and watched, but any child
//     created and deleted in the gap is not observed;
//   - events for a watch the oracle does not map;
//   - a snapshot that overlapped workspace events.
//
// # Verdicts
//
// VerdictOK means no gap was recorded and every canary was observed. Only
// VerdictOK supports a claim about transient mutations. VerdictEvidenceLost
// means at least one gap was recorded. VerdictInstrumentUnqualified means a
// canary was not observed and no gap was recorded. A gap's Path scopes it to a
// subtree, and an empty Path means the whole window. The verdict itself is
// window-wide. PreservationFields names the fields that must survive for an
// incompleteness claim to be well formed.
//
// # Not observed
//
//   - Anything outside the root. Symlinks are never followed.
//   - Bind-mount and mount-namespace boundaries. A bind mount that makes one
//     directory appear at two paths is reported only when it causes a watch
//     alias. An unmount below the root is reported as a mount_boundary gap.
//   - Reads, opens, and closes that do not write. IN_ACCESS, IN_OPEN, and
//     IN_CLOSE_NOWRITE are not requested.
//   - Event multiplicity. inotify drops an event that is identical to the
//     unread event at the tail of the queue, so a burst of identical events can
//     arrive as one. The presence of an event for a path is recorded. Event
//     counts are lower bounds and are not exact.
//   - Kernel-level races between an event and the oracle's later read or hash.
//     Events carry the time the oracle read them, not the kernel's time, and a
//     file can change again before its digest is taken.
//   - Mutations that happen during Start, before the first barrier. Start must
//     run before the workload begins.
//   - A directory swapped between enumeration and watch installation, for
//     example by a symlink or by renaming an ancestor. IN_DONT_FOLLOW protects
//     only the final path component.
//   - Extended attributes, ACLs, and capabilities. IN_ATTRIB reports their
//     changes as events, but snapshot digests do not cover them.
//   - Descriptor-relative traversal. Snapshots are path based (Lstat and
//     O_NOFOLLOW) and are not atomic. A concurrent change is detected only when
//     a walk sees a path vanish or when workspace events overlap a snapshot. A
//     change whose event is queued after the post-snapshot barrier is not
//     detected.
//   - Anything after the post-Stop barrier. The window ends there.
//   - The brief presence of canary files in the root. Concurrent processes can
//     observe them.
//   - Inode identity. Replacing a file with identical content is not a revert,
//     because equality compares type, mode, owner, link count, size, digest,
//     and link target.
//
// The at_ns fields are monotonic offsets from Start. Report.JSON includes them,
// and Report.Digest excludes them, so identical workloads produce identical
// digests.
//
// On platforms other than Linux, Start returns ErrUnsupported.
package effectoracle
