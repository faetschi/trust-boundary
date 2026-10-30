---
status: prototype-increment
authority: implementation note for D06 staging
---

# Private Git staging (D06 integration status)

`supervisor/internal/privategit` is a Linux primitive used by the production
binder and `sessionlaunch.Prepare`. `Prepare` creates and verifies the private
repository and persists its provenance before D09 source exposure. This is
integrated prototype implementation evidence under the existing prototype
waiver. It is not production containment, a qualified D06 deployment, or G1
evidence.

## Trusted inputs

`Create` accepts an open source descriptor, a required `SourceBindingVerifier`
from the trusted session/source owner, a private 0700 parent descriptor, and a
`PinnedGit`. `PinGitExecutable` requires a trusted expected SHA-256 and verifier,
hashes the approved executable, and holds its descriptor; Git children run
`/proc/self/fd/3`. A held descriptor prevents pathname replacement but not
same-inode byte writes, so the verifier must establish the execution boundary
(for example a root-owned non-writable host binary).

`Options.CleanupBoundary` must attest exclusive ownership of the private parent
namespace and created child tree for the whole create/destroy operation. Linux
has no unlink-by-inode, so this is an integration precondition, not an atomic
claim. Without it the API rejects the operation or leaves a failed tree for
trusted quarantine via `ErrCleanupIncomplete`.

The source `Quiescent`, generation, and xattr fields are caller assertions
only. In `sessionlaunch.Prepare`, `verifiedSourceBinding` validates the held
descriptor against the retained Store generation, manifest, metadata/xattr
policy, and durable Store/journal evidence before `privategit.Create`.

The package never accepts a source pathname, Git directory, clone URL, Git
arguments, child environment, hook/template path, or worker command. No
model-visible Git tool or private-Git worker exists.

## Capture and Git boundary

Capture uses `workspace.Import` with `ReservedMetadataRejectLiveGit`: it
rejects `.git` directories and pointer files (including nested submodule and
linked-worktree pointers) and rechecks names and identity across the race
barrier. The new root is rescanned with `ReservedMetadataExcludeRootPrivateGit`,
which excludes only the root `.git`; the verifier checks that entry is the
recorded new inode, same mount, supervisor-owned 0700. Manifest, tree digest,
metadata-policy digest, object count, and byte capture must equal the imported
snapshot, and `Verify` repeats that check.

Git runs only for `--version` and `init --initial-branch=main`, with a minimal
environment that disables ambient config, prompts, locks, unsafe discovery,
network/file protocols, and templates. The generated `.git/config` must match an
exact allowlist of four `core.*` keys; includes, remotes, filters, fsmonitor,
credential helpers, SSH commands, and other keys fail closed. Admin
directories are same-mount, supervisor-owned 0700, files 0600; links, special
files, `commondir`, `alternates`, `gitdir`, and `worktrees` are rejected. No
commit, history, refs, or source `.git` metadata is created or imported.

## Cleanup and provenance

Failed creation and `Repository.Destroy` remove contents by descriptor-relative
traversal, checking every object against the root mount identity, with object
and depth limits. The held root identity and name are rechecked before the
final conditional unlink; a replacement barrier proves it fails closed.

Provenance (source root, private root, manifest/tree, binding and metadata
policy, xattr profile, capture limits, Git version and execution boundary,
filter, branch) goes into `sessionlaunch.Provenance` and the audit journal as
source-binding and private-Git-ready records. `PreparedSession` rechecks them
and the repository before D09 export. Worker APIs do not return the private
parent or `Repository.Root`.

## Limits

Tests (`privategit_linux_test.go`) use real Git and synthetic fixtures. Nested
mount traversal needs mount privileges and is not covered by the unprivileged
tests. The package makes no claim about process/mount settlement, read-only
consumer mounts, approved-delta attribution, publication safety, network
isolation, or a production host profile; consumer-side read-only mount
verification and production containment remain separate and unqualified.
