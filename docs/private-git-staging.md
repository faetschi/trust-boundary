---
status: prototype-increment
authority: implementation note for D06 staging
---

# Private Git staging (D06 integration status)

`supervisor/internal/privategit` is a Linux-focused primitive used by the
committed production-binder and `sessionlaunch` preparation path. The binder
pins an approved Git executable with `PinGitExecutable`; `sessionlaunch.Prepare`
creates and verifies the private repository and persists its provenance before
D09 source exposure. The existing prototype waiver remains in force: this
implementation is not production containment or G1/private-Git compliance
evidence.

The committed path supplies Store-backed source binding, an approved Git
executable and execution boundary, an exclusive private-admin cleanup
boundary, and durable provenance records. This is integrated prototype
implementation evidence; it does not claim a qualified D06 deployment or close
the remaining production-containment and G1 questions.

## Contract and required trusted inputs

`Create` accepts:

- an already-open source descriptor;
- a required `SourceBindingVerifier` supplied by the trusted session/source
  owner;
- a private 0700 parent descriptor; and
- a `PinnedGit` created with a trusted expected digest, verifier, and execution
  boundary.

`Options.CleanupBoundary` must explicitly attest exclusive ownership of the
private parent namespace and the created child tree for the complete
create/destroy operation. Linux has
no unlink-by-inode operation. This boundary is therefore a real integration
precondition, not an atomic-inode-unlink claim; without it the API rejects the
operation or leaves a failed tree for trusted quarantine via
`ErrCleanupIncomplete`.

The source `Quiescent`, generation, and xattr fields are shape checks and caller
assertions only. They are not authentic sealed-source evidence. In committed
`sessionlaunch.Prepare`, `verifiedSourceBinding` validates the held descriptor
against the retained Store generation, manifest, metadata/xattr policy, and
durable Store/journal evidence before it is passed to `privategit.Create`.
The binding digest and private-Git provenance are appended to session audit
records before `Prepare` returns. This is implementation evidence, not proof
of qualified deployment or G1.

The package never accepts a source pathname, Git directory, clone URL,
arbitrary Git arguments, arbitrary child environment, hook/template path, or
worker command. No model-visible Git tool or private-Git worker is created.

## Capture and Git boundary

The Linux path uses `workspace.Import` with the fixed
`ReservedMetadataRejectLiveGit` policy. The policy admits each recursive entry
before opening it, rejects `.git` directories and pointer files (including
nested submodule/linked-worktree pointers), and rechecks directory names and
entry identity across the deterministic race barrier. Default zero-value
workspace imports retain their historical behavior.

After the copy, the new root is scanned again with
`ReservedMetadataExcludeRootPrivateGit`: it excludes only the root `.git` entry
from the logical worktree manifest, while nested `.git` components remain
rejected. The private-Git verifier independently checks that excluded entry is
the recorded newly-created inode on the same mount and is a supervisor-owned
0700 admin directory. The final manifest, tree digest, metadata-policy digest,
object count, and byte
capture are compared exactly with the imported snapshot. `Verify` repeats this
same exact worktree invariant before checking Git admin metadata.

`PinGitExecutable` requires a trusted expected SHA-256 identity and an explicit
trusted verifier. It hashes and holds the approved executable descriptor; Git
children execute `/proc/self/fd/3` through `ExtraFiles`, with the held root and
template descriptors as child fd 4/5. `Cmd.Dir`, `HOME`, and template/config
paths do not reuse a mutable destination pathname. A held descriptor prevents
pathname replacement, but it does **not** by itself prevent same-inode byte
writes. The trusted verifier must establish the approved execution boundary,
such as a root-owned non-writable host binary in the registered host namespace
or an approved immutable boundary. The package makes no stronger claim.

Git is invoked only for `--version` and fixed `init --initial-branch=main`.
The environment is minimal and disables ambient system/global config, prompts,
optional locks, unsafe discovery, network/file protocol use, and ambient
templates. The generated `.git/config` is parsed against an exact allowlist of
the four expected `core.*` initialization keys and values; includes, remotes,
filters, fsmonitor, credential helpers, SSH commands, external drivers, and
other keys fail closed. Admin directories are same-mount, supervisor-owned,
0700; regular admin files are 0600; links, special files, `commondir`,
`alternates`, `gitdir`, and `worktrees` metadata are rejected.

No initial commit is created. No live history, object storage, refs, or source
`.git` metadata is imported. Safe worktree files such as `.gitattributes` are
ordinary snapshot content.

## Cleanup and provenance boundary

Failed creation and `Repository.Destroy` retain the created root descriptor and
remove contents by descriptor-relative traversal. Every opened cleanup object
and every recursive directory is checked against the root mount identity;
limits bound object count and depth. Before the final parent/name unlink, the
held root identity and namespace name are rechecked. A deterministic replacement
barrier proves the second check fails closed and preserves the replacement.
The final unlink remains conditional on the explicit exclusive-parent boundary.
No broad path cleanup is attempted.

`privategit` returns provenance for the source root, private root, source
manifest/tree, binding and metadata policy, xattr profile, capture limits, Git
executable/version and execution boundary, filter identity, and branch. The
committed `sessionlaunch.Prepare` path incorporates it in `sessionlaunch.Provenance`
and persists source-binding and private-Git-ready records in the audit journal.
`PreparedSession` rechecks those records and the repository before D09 source
export. This is durable implementation evidence, not production qualification
or G1 compliance evidence.

## Evidence boundary and tests

Focused Linux tests use real installed Git and synthetic private fixtures. They
cover:

- trusted expected Git identity rejection of a same-UID Git-like fake;
- execution through the held approved descriptor after replacing the Git
  pathname, proving the swapped fake is not executed;
- source `.git` directories, pointer files, nested submodule/linked-worktree
  pointers, safe `.gitattributes`, and no imported history/ref;
- deterministic workspace `.git` insertion with zero inserted Git bytes copied;
- exact post-init and `Verify` manifest rescans;
- parsed config tampering for include/remotes/filters/fsmonitor/credentials/SSH;
- descriptor-relative cleanup, mount-identity comparison, and deterministic
  root-name replacement fail-closed behavior.

Actual nested-mount traversal requires mount privileges and is not claimed by
the unprivileged fixture tests. The applicable-kernel comparator is tested; a
privileged host integration fixture is still required for end-to-end mount
insertion/replacement coverage. Tests should run with a private ext4 `TMPDIR`
mode 0700, for example `/home/jeli2k/tbound-privategit-test`.

The package itself makes no claim about process/mount settlement, read-only
consumer mounts, approved-delta attribution, publication safety, network
isolation, or a production host profile. The committed production binder keeps
the private repository in `PreparedSession`; its worker APIs do not return the
private parent or `Repository.Root`. The D09 path can issue a verified Store
source capability, but consumer-side read-only mount verification and
production containment remain separate and unqualified. No worker-facing Git
tool is created here, and this note does not establish G1 or a production
no-`.git`-exposure guarantee.
