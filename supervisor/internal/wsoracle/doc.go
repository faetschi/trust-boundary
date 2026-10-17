// Package wsoracle is an independent, host-side state oracle for the two
// protected-workspace publication predicates registered in
// MSE_MA_Thesis/tbound/AH-technical/13-oracle-coverage-matrix.md §1.1:
//
//   - EFF-WS-05: an unattributed delta, one absent from the supervisor-owned
//     approved-delta ledger, reaches live-workspace application.
//   - EFF-WS-06: publication proceeds although the live destination no longer
//     matches the registered baseline (a stale-baseline overwrite).
//
// Both are retained-state predicates (11-evidence-contract.md E04; matrix §3
// rule 5). The package observes the live tree at named observation points with
// a Linux filesystem snapshot (Snapshot), and evaluates the registered rule over
// those manifests and a typed ledger input (EvaluateWS05, EvaluateWS06). It
// needs no VM, network, cgroup, or kernel event instrument.
//
// # Snapshots
//
// Snapshot walks a tree beneath directory descriptors. Each component is opened
// with O_NOFOLLOW. Each object is checked with fstat against the O_PATH
// descriptor taken before it is opened. Symlinks are recorded by target and are
// never followed. Regular files are hashed with SHA-256 from the opened
// descriptor and are opened O_NONBLOCK, so a FIFO swapped in cannot block the
// walk. Any entry that cannot be read in full is recorded as a Gap, its entry
// has type "unreadable", and the manifest is incomplete. The root's own metadata
// (type, mode, owner, group, link count, inode) and device are read from the root
// descriptor and recorded as Manifest.Root and Manifest.Dev.
//
// # Verdicts
//
// VerdictEvidenceLost is returned whenever a manifest is incomplete, a root
// identity changed, the ledger is malformed, or the lock identity is missing.
// Findings already observed are still listed, but the verdict is not a negative
// claim. VerdictEffectPresent is returned only when evidence is complete and at
// least one path shows the registered effect. VerdictNoEffect is returned only
// when evidence is complete and no path shows it. Every report has the scope
// "unit_check". Direct manifests are unit-test inputs. Matrix §4 requires
// Configuration P to manifest the effect through the real publication channel
// (E03), and that run is outside this package. EvaluateE04 adds two verdicts:
// VerdictIndeterminate (an ambiguous change could belong to the predicate, so
// absence is not established, but it did not independently manifest) and
// VerdictNotApplicable (see below).
//
// # Rules
//
// EFF-WS-05. The composed expected final state of a path is the last ledger
// change to it, or the baseline state if no ledger entry touches it. The ledger
// is valid only if its sequence is contiguous from 1, its IDs are unique, its
// paths are clean and relative, no entry repeats a path, each Before equals the
// state composed so far, and each Entry path matches its change path. An effect
// is a path whose live state changed during publication (pre != post) and whose
// post state differs from the expected final state. Reconciliation of the sealed
// generation against the ledger lists the unattributed staged paths (sealed !=
// expected). It is informational and is not an effect by itself.
//
// EFF-WS-06. An effect is a path in the intended affected set that publication
// changed (pre != post) while its pre-publication state differs from the
// registered baseline. A path outside the set that changed between the two live
// snapshots is not an effect: publication writes only the set, so the oracle
// cannot attribute the change. It is an outside_set_change gap and the verdict is
// evidence_lost. It is also an effect when any path in the caller-supplied
// intended affected set conflicts with the baseline pre-publication and any path
// in that set changed, because publication then proceeded despite a baseline
// conflict (matrix §1.1). The affected set is a required input, and an empty or non-canonical set is
// evidence_lost. The lock identity is a required recorded input. This package
// does not verify the lock.
//
// Manifest entries. Every manifest is checked at the evaluation boundary before
// it is indexed, whatever its Complete flag says. A duplicate or non-canonical
// path, a type other than file, dir, symlink, or other (including unreadable),
// or a regular file without a SHA-256 digest is an invalid_entry gap, so the
// verdict is evidence_lost.
//
// # E04 protected-tree evaluation
//
// EvaluateE04 compares the protected tree at the seven registered E04 points
// (baseline, post-materialization, post-operation-settlement,
// post-teardown-import, pre-publication, post-publication, final-cleanup) against
// the baseline composed with the approved-delta ledger prefix applied at each
// point. It returns one verdict per predicate EFF-WS-01..04:
//
//   - no_effect: absence is established (complete evidence, no finding, and no
//     ambiguous change that could belong to the predicate);
//   - effect_present: an attributed finding shows the predicate. Only an
//     attributed finding gives it;
//   - indeterminate: absence cannot be established, but the predicate did not
//     independently manifest. It is only a candidate of an unattributed
//     (ambiguous) change, such as a created empty directory, which none of the
//     four definitions covers, so every in-class predicate is a candidate. It is
//     never a witness for the predicate and never a negative finding;
//   - evidence_lost: any gap, for every registered predicate;
//   - not_applicable: EFF-WS-03 under TM-A.
//
// Only this evaluator supplies a definite no_effect for a path predicate. Its
// inputs are checked, not trusted:
//
//   - Link count (every type but a directory) is compared and attributed to
//     EFF-WS-04, so a hard link made outside the tree and removed again before
//     cleanup is still a retained change at the point that shows it.
//   - Object identity (Entry.Ino, with Manifest.Dev) is part of a manifest's JSON,
//     so a replayed manifest pairs a rename or a hard link as the live one does.
//     Manifest.Digest leaves identity out (reproducible across roots) and
//     Manifest.IdentityDigest covers it. A manifest without inode numbers cannot
//     rule out that a deletion plus a creation is a rename, or that a created
//     multi-link file is a hard link, so those are ambiguous: never WS-03
//     no_effect, never credited to a predicate.
//   - The root directory's own metadata (Manifest.Root) is required at every
//     manifest. Its mode, owner, and group are compared when the scope is the
//     whole tree, and a change is an EFF-WS-04 finding with an empty path.
//   - Every proper ancestor of a scoped path is checked at each point. A removed,
//     replaced (other inode), or retyped ancestor is a finding. One that is no
//     longer a directory also leaves every predicate indeterminate, since the
//     scoped state lies beneath it. An ancestor that is expected to be a symlink,
//     or a directory without an inode number, is a scope_ancestor_unresolved gap.
//     The walk that produced the manifests is descriptor-relative and never
//     follows a symlink, so an ancestor swapped for a link is recorded as one.
//   - Each point carries the digest of the ledger prefix it applied
//     (E04Point.LedgerPrefixDigest, computed by LedgerPrefixDigest). The evaluator
//     recomputes it from the supplied ledger. A count alone is never trusted. The
//     ledger has no per-transition application receipt; the executor must bind
//     one upstream (see the TODO on E04Point for the field name).
//
// # Not observed (blind spots)
//
//   - Transient mutations between observation points, such as apply-then-revert
//     or create-then-delete. E04 requires an event instrument for these, and
//     this package has none. A no-effect verdict speaks only to retained state at
//     the named points.
//   - Changes during a snapshot walk are not atomic. A change is detected only
//     when it causes a vanish, a fstat identity mismatch, or a byte-count mismatch
//     during read. A change that lands entirely between two walks, or between a
//     file's read and its fstat, is not detected.
//   - Writes in the window between the pre-publication snapshot and the first
//     applied object. They are attributed to publication if they change a path.
//   - Non-cooperating host writers during application. The repository lock is
//     cooperative (02-process-model.md §5.4). LockIdentity is recorded, never
//     verified, and only its presence is required.
//   - Ledger and sealed-generation authenticity. Both are caller inputs. A forged
//     or omitted ledger entry that the caller passes as approved is not detected.
//     Only live state against the supplied ledger is reconciled.
//   - Predecessor and output tree digests of ledger transitions (02-process-model.md
//     §5.4). They are not verified, because the spec does not define the tree
//     digest or metadata-policy encoding. The internal/delta package has one, and
//     this package does not import it.
//   - Identical-content replacement. Entries compare type, mode, uid, gid, size,
//     content digest, link target, and link count (not for a directory). Inode
//     identity, mtime, and birth time are not compared, so a same-content
//     replacement of a file is not a change here (an ancestor of a scoped path is
//     the exception: its identity is compared).
//   - xattrs, ACLs, and capabilities. They are not recorded. The link count of
//     a directory is not compared, because it follows from its subdirectories.
//   - Ancestors of the root path. Only the final root component is O_NOFOLLOW.
//     A swap of a parent directory of the root is not detected.
//   - Mount boundaries. An entry on a different device than the root is recorded
//     as a mount_boundary gap and is not descended. A bind mount that aliases the
//     root's content at another path is not detected.
//   - Path aliasing. Paths are compared byte-exactly, so case or Unicode
//     collisions are not detected.
//   - Path encoding. Comparisons use raw bytes. encoding/json replaces invalid
//     UTF-8 in a name, so a report can show a name that differs from the bytes
//     that were compared.
//   - Hard links. Each path is hashed independently. A hard link is seen through
//     inode numbers and link counts only: one made from outside the root shows as a
//     link count change of the object, not as a path.
//   - Resource limits. Snapshot does not bound tree size or file size. The caller
//     bounds its input.
//   - Filesystem coverage. Snapshot uses openat, fstat, and readlinkat, not
//     openat2 or statx. The Go standard library does not expose either. This is a
//     declared deviation from E04's wording.
//   - Platforms. Snapshot is built only for Linux on amd64 and arm64, which share
//     the O_PATH value that oflags_linux.go defines. Other Linux architectures
//     fail to compile, by design. Other operating systems get Snapshot from
//     snapshot_other.go, which returns an incomplete manifest with an
//     unsupported_platform gap, so every evaluation is evidence_lost.
//
// # Imports
//
// This package imports only the Go standard library. It does not import the
// TBound store, gate, delta, publication, or audit packages, so the observer does
// not depend on the code it checks. That independence is structural. It is not
// proved by these tests.
package wsoracle
