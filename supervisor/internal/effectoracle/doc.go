// Package effectoracle is an independent, Linux-only transient-mutation
// observer for evidence contract E04 (MSE_MA_Thesis/tbound/AH-technical/
// 11-evidence-contract.md, "E04 — temporal workspace predicate").
//
// Start watches a directory tree with inotify and records each kernel event in
// an append-only log. Sequence numbers increase strictly across events, gaps,
// heartbeats, and marks. Mark records a named observation point together with a
// SHA-256 snapshot of the tree. Stop returns a Report with the events, the
// snapshots, the explicit gaps, and a verdict. Report.TransientMutations derives
// create-then-delete candidates and reverted candidates. An endpoint-only
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
// attempt is recorded as a heartbeat. The oracle's own canary traffic is
// filtered out of Events: the create, write, close, and removal it causes for
// each canary, in that order. The canary names do not appear in the report. Any
// other event for an issued canary name is recorded in Events and counted as a
// canary_anomaly gap, so a foreign process that learned a name leaves evidence.
// A canary that is not observed within the timeout, or that is attempted after
// the reader has stopped, is recorded as unobserved. Unless a gap is also
// recorded, the verdict is then instrument_unqualified (see Verdicts). inotify
// reports events in queue order, so an observed canary also shows that the
// kernel events of operations completed before the canary was written have been
// read. This holds unless the queue overflowed.
//
// Liveness is checked per Start, Mark, and Stop, and only on the root watch. A
// subdirectory watch that stopped delivering events without a recorded gap would
// not be detected by a canary. A kernel IN_IGNORED on a subdirectory watch drops
// that watch's mapping without a gap. This is not a silent loss. IN_IGNORED
// arrives when the directory is removed, which the parent's delete event
// records; when the directory is unmounted, which is a mount_boundary gap; or
// when the oracle removes the watch itself, which it does only after recording a
// gap (an overflow rebuild, or a directory replaced during watch installation) or
// a move.
//
// The window ends at the stop barrier. Once the stop canary is observed, or has
// timed out, later records are not applied to the log.
//
// # Loss and coverage
//
// Every loss or coverage gap is an explicit Gap record, and no gap is ever
// dropped. Gaps cover:
//   - kernel queue overflow (IN_Q_OVERFLOW). The tree is re-watched, and the
//     window is evidence_lost;
//   - a failed read or a malformed inotify record. The reader stops;
//   - the root deleted, moved, or unmounted;
//   - watch-limit exhaustion (ENOSPC) and other watch or enumeration failures,
//     including a directory replaced by a symlink during watch installation;
//   - a directory created or moved in with entries already in it (subdir_race).
//     Its subtree is watched and enumerated, but the entries that existed before
//     the watch are not observed. A directory created empty raises no gap. An
//     entry in it whose creation was not observed, because it existed before the
//     watch, does raise one (subdir_race) when its first event arrives;
//   - a watched directory moved away (dir_moved_out). Entries created in it
//     before the move are not paired with later events, so the window is
//     incomplete for that subtree;
//   - a path the snapshot could not read in full: a permission failure, a FIFO or
//     symlink swapped in mid-walk, or a directory swapped for a symlink
//     (snapshot_unreadable). The path is recorded as an entry of type
//     "unreadable". The walk goes on, so the other entries and events are kept;
//   - an event for an issued canary name that the oracle did not cause
//     (canary_anomaly);
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
//     Events carry the time the oracle read them, not the kernel's time (a later
//     reading says nothing about when the event happened, only that it did not
//     happen after it), and a file can change again before its digest is taken.
//   - Mutations that happen during Start, before the first barrier. Start must
//     run before the workload begins.
//   - Mutations between a directory's creation and the installation of its
//     watch. An entry that exists when the watch is installed is seen by the
//     enumeration, or, if it is removed before the enumeration, through its delete
//     event (subdir_race). An entry created and removed entirely before the watch
//     is installed is unobservable and leaves no trace. The verdict can be ok
//     despite such an entry, so the window's claim does not cover it.
//   - A swap of an ancestor directory between enumeration and watch installation,
//     for example by renaming an ancestor or by replacing one with a symlink.
//     O_NOFOLLOW and O_DIRECTORY protect only the final path component.
//   - Extended attributes, ACLs, and capabilities. IN_ATTRIB reports their
//     changes as events, but snapshot digests do not cover them.
//   - Snapshots are path based (Lstat, then open with O_NOFOLLOW) and are not
//     atomic. A concurrent change is detected only when a walk sees a path vanish,
//     when a path cannot be opened safely, or when workspace events overlap a
//     snapshot. A change whose event is queued after the post-snapshot barrier is
//     not detected.
//   - Anything after the post-Stop barrier. The window ends there, and records
//     read after it are not applied.
//   - The brief presence of canary files in the root. Concurrent processes can
//     observe them. A process that creates an issued canary name before the
//     oracle does makes that canary unobserved, so the verdict is not ok.
//   - Inode identity. Replacing a file with identical content is not a revert,
//     because equality compares type, mode, owner, link count, size, digest,
//     and link target.
//   - Identical rewrites and same-mode changes. A path whose start and stop
//     entries are equal is reported as reverted only when a mark saw a different
//     state. Otherwise it is touched_unchanged, which is not a claim of a
//     transient mutation. The fingerprint excludes mtime, so a same-content
//     rewrite between two marks cannot be told apart from no change.
//
// The at_ns fields are monotonic offsets from Start. Report.JSON includes them,
// and Report.Digest excludes them, so identical workloads produce identical
// digests. Report.StartMonoNS is CLOCK_MONOTONIC at Start, so an offset can be
// placed on the machine's monotonic clock (an event happened at StartMonoNS+AtNS
// or earlier, by an unbounded amount: AtNS is when the reader recorded it, so it
// bounds the time of the event from above and never from below); the digest excludes it too.
// A moved_from or moved_to event carries the kernel's rename cookie. The two
// halves of a rename between watched paths share one; a rename whose other side
// is outside the watched tree has a cookie of its own. The digest keeps which
// events share a cookie, not the cookie values. StartContext is Start that a
// done context calls off: it checks the context before every canary it writes,
// so a start that is called off writes nothing more into the root.
//
// On platforms other than Linux, Start returns ErrUnsupported.
package effectoracle
