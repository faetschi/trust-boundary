# Disposable cgroup teardown witness

`cgroup_teardown_witness.py` is a trusted guest-host component probe for the
destructive `cgroup.kill` path. It runs one fixed local Python fixture, moves
the blocked leader into a newly created direct child of an operator-selected
delegated cgroup, then records independent host `/proc`, `pidfd`,
`cgroup.procs`, and `cgroup.events` observations. It accepts no provider,
client, OCI, or arbitrary worker command.

This does not establish production qualification. The receipt labels itself
`component-only`, sets `g1` false, and leaves EFF-PROC-02/03 untested. It also
does not establish the full EFF-PROC-01 oracle, which still needs the registered
runtime fixture and OCI/cgroup event stream. It cannot replace network,
workspace, recovery, signature, or protocol-freeze evidence.

## Preconditions

Run only on the trusted Linux guest host as UID 0 in the guest init PID, user,
mount, and cgroup namespaces. Profile schema v4 requires the current boot ID
as a canonical UUID plus the device/inode identity of each namespace, captured
by trusted guest-init provisioning for that same boot. At preflight and again
at the final kill gate, the helper compares `/proc/sys/kernel/random/boot_id`
with the pinned boot ID, then compares both `/proc/self/ns/*` and
`/proc/1/ns/*` against the pinned namespace identities. The receipt records the
observed boot ID. Agreement between the observer and `/proc/1` alone is
insufficient: both can refer to a nested PID namespace. Namespace identity
comes from `stat(2)` device/inode values, as documented by Linux
[namespaces(7)](https://man7.org/linux/man-pages/man7/namespaces.7.html).
`NS_GET_PARENT` is not used as a root test because `EPERM` can mean either that
the namespace has no parent or that its parent is outside the caller's
namespace scope, per [NS_GET_PARENT(2const)](https://man7.org/linux/man-pages/man2/NS_GET_PARENT.2const.html).

The trusted provisioning/registration process must capture these values from
the actual guest-init context: read the boot UUID from
`/proc/sys/kernel/random/boot_id`, and measure `st_dev`/`st_ino` for
`/proc/self/ns/{pid,user,mnt,cgroup}` and `/proc/1/ns/{pid,user,mnt,cgroup}`;
require each pair to match before recording the profile. Capture the boot ID
and namespace values together, then bind the resulting manifest bytes to the
trusted profile record. Regenerate and re-register the baseline after every
guest reboot. Do not create it from the same potentially nested shell that
will run the probe. The helper detects changes during a run but does not
authenticate the manifest or registration; a caller able to replace the
manifest and its trusted pin can supply a false baseline. This is an explicit
guest provisioning TCB assumption.

The operator must pre-provision a dedicated
cgroup-v2 domain below the hierarchy root, owned by UID 0, with no group/other
write permission on any path component or cgroup control. The helper derives
the non-root-writer exclusion from those live ownership and mode bits at
preflight and again at the destructive gate; the profile has no caller-supplied
writer assertion. UID 0 is part of the trusted host TCB. The selected root
must match the pinned profile by absolute path, device, inode, mount ID, and
owner UID 0, and it must be empty before the run.

The helper creates one random direct child, captures its device/inode
immediately after `mkdir`, and opens it only if the entry and held descriptor
match that captured identity. The fixture runs as UID/GID 65534. Immediately
before `cgroup.kill`, the helper rechecks the protected ancestry and control
permissions, exact root child set, exact child PID set, and absence of nested
cgroups. This keeps recursive `cgroup.kill` scoped to the newly created
descendant-free child. Every name-based removal revalidates the original child
identity first. If a value is unknown or the name was substituted, it leaves
the path for explicit inspection and never sweeps the root or removes a
replacement.

The candidate profile manifest must pin all of the following measured values
(this is a schema sketch, not valid JSON and contains no registration values):

```text
{
  "schema": "tbound-host-proc-cgroup-profile/v4",
  "profile_id": "<registered candidate profile identity>",
  "kernel_release": "<exact running guest kernel release>",
  "landlock_abi": 8,
  "runtime_profile_id": "<runtime profile identity>",
  "runtime_profile_path": "<absolute path to runtime profile bytes>",
  "runtime_profile_sha256": "<sha256 of those exact bytes>",
  "teardown_deadline_ms": <registered integer from 1 to 60000>,
  "guest_boot_id": "<canonical UUID from the trusted guest-init boot>",
  "guest_init_namespaces": {
    "pid": {"device": <measured st_dev>, "inode": <measured st_ino>},
    "user": {"device": <measured st_dev>, "inode": <measured st_ino>},
    "mnt": {"device": <measured st_dev>, "inode": <measured st_ino>},
    "cgroup": {"device": <measured st_dev>, "inode": <measured st_ino>}
  },
  "delegated_root": {
    "path": "<exact absolute delegated cgroup path>",
    "device": <measured device number>,
    "inode": <measured inode number>,
    "mount_id": <measured cgroup2 mount ID>,
    "owner_uid": 0
  }
}
```

Replace each placeholder with an actual measurement. The manifest and
runtime-profile digest are identity checks only. The helper does not verify
signatures or registration. It never changes permissions on a pre-existing
root or ancestor; it refuses an unsafe root.
Its live Landlock syscall query must return exactly ABI 8; a higher ABI does
not pass this gate. Python must expose `os.pidfd_open` and the guest `/proc`
view must make every fixture process observable.

## Revalidate the existing guest

To inventory the existing Hyper-V VM, run the fixed-purpose launcher from a
normal PowerShell session. It verifies literal source hashes, stages those
exact byte snapshots, holds read-only handles that deny write/delete sharing,
and requests one hidden UAC-elevated child with `Start-Process -Verb RunAs`.
The child validates both snapshot hashes before creating or invoking script
blocks, checks its elevated token, uses canonical System32 PowerShell and an
exact Hyper-V module manifest with a cleared ambient `PSModulePath`, and reports
the module binary identity. The launcher accepts success only after validating
bootstrap and worker markers, process exit code, and report hash. It has no
VM-name, command, or output-path arguments, does not alter the VM, and does not
connect to the guest. It writes the launch/bootstrap/completion/result markers,
inventory report, and verified source snapshots in a fresh per-run directory
under the current user's Desktop. The launcher prints that exact directory
and returns nonzero if UAC is denied, the worker times out, or any check fails.

```powershell
& 'C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary\infra\repro\launch-reviewed-hyperv-inventory-elevated.ps1'
```

The fixed host script and elevated worker are pinned by literal SHA-256 values
inside the launcher. The per-run output records their exact snapshot hashes,
the bootstrap-command hash, token/module identities, and process exit markers.
The host report includes VM state, processor/memory/firmware settings, virtual
switch names/types, disk paths and readable VHD metadata, DVD attachment, and
checkpoints. Its guest-kernel field is deliberately `NOT RUN`. After placing
the exact tracked guest probe in the guest through an already approved
provisioning path, run it from the actual VMConnect guest console and retain
the displayed values with the inventory:

```sh
bash /absolute/guest/path/probe-landlock-abi8.sh
```

The exact source is [probe-landlock-abi8.sh](probe-landlock-abi8.sh). It queries
the live Landlock ABI and returns success only for exactly 8; it does not
modify kernel, cgroup, VM, or network state. Do not use shared folders,
clipboard integration, or an unauthenticated SSH route to transfer it. If no
approved guest path exists, the guest probe remains unrun until the trusted
provisioning path is available. Do not treat the WSL kernel or this read-only
identity probe as clean-guest qualification.

## Invocation

After preparing and reviewing the candidate manifest and the delegated root,
run this explicit destructive command on the guest host:

```sh
python3 infra/repro/cgroup_teardown_witness.py \
  --profile-manifest /absolute/path/guest-profile.json \
  --delegated-root /absolute/path/delegated-cgroup \
  --confirm-disposable-cgroup-kill \
  > /absolute/path/cgroup-teardown-evidence.json
```

The helper writes `cgroup.kill` only after observing the unprivileged worker
and its two children in the exact fresh scope, opening pidfds, pinning `/proc`
start times and cgroup memberships, rechecking the protected writer boundary,
and proving that the scope has no nested descendants. It then requires every
pidfd to become readable,
all original `/proc` identities to be absent or reused, and the scope to report
`populated 0` before the registered deadline. Any missing, stale, mismatched,
unknown, escaped, or surviving observation fails closed. The helper removes
only its own child after observing it empty. An incomplete run never kills or
sweeps the delegated root; if the child remains populated, its exact path and
device/inode are included in the error for explicit operator inspection and
cleanup.

Do not run the destructive command against WSL, a shared development cgroup,
or an unregistered guest. The local Windows/WSL environment is only suitable
for the unprivileged parser and rejection tests. A successful component receipt
still leaves clean-guest qualification, OCI-integrated EFF-PROC-01,
EFF-PROC-02 crossing, EFF-PROC-03 listener inspection, and G1 open.
