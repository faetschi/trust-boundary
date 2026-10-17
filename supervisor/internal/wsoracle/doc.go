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
// has type "unreadable", and the manifest is incomplete.
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
// (E03), and that run is outside this package.
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
//     content digest, and link target. Inode identity, mtime, and birth time are
//     not compared, so a same-content replacement is not a change here.
//   - Link count, xattrs, ACLs, and capabilities. Link count is recorded but not
//     compared (EFF-WS-04 owns it). Extended attributes are not recorded.
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
//   - Hard links. Each path is hashed independently, and hard-link identity is
//     not compared.
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
