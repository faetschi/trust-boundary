# `tbound-doctor`

`tbound-doctor` checks local prerequisites. It does not install Pi, change
system state, or make a runtime admission decision.

```sh
tbound-doctor
tbound-doctor --pi /absolute/path/to/pi
tbound-doctor --json
```

Without `--pi`, it resolves `pi` through `PATH`. It checks that the
executable reports the pinned version 0.87.1 and prints the path and SHA-256
fingerprint. Doctor does not verify that the executable or future TBound
extension is signed or admitted by a production profile.

On Linux it also reports the live Landlock ABI, the current cgroup-v2 scope, Podman, crun,
Cosign, and signed-profile prerequisites. D09 requires exactly Landlock ABI 8;
a mismatch reports the observed ABI and the next host/profile step. These checks
are advisory. A matching ABI or present binary/profile file does not qualify a
host or prove containment. The signed-profile check confirms path presence
only; it does not verify file type, ownership, signature, or runtime admission.
For the operator-owned profile paths, see [governed host setup, section 4](governed-setup.md#4-signed-host-profile).

The cgroup inventory distinguishes the current scope from controllers available
at the hierarchy root. It reports the scope's type, available controllers,
controllers enabled for children, and limit/control files. File presence does
not prove writable delegation or working teardown. Doctor therefore leaves
delegation and cgroup reuse unverified, without creating cgroups or writing
their controls. These warnings do not prevent installing the application or
running the Go-only native fixture.

## Optional cgroup compatibility check

The installed `tbound-profile` command includes a functional check; no Go
compiler or source checkout is needed. As a nonroot user in an existing,
operator-authorized writable delegated domain scope, run:

```sh
tbound-profile cgroup-reuse-check --confirm-disposable-cgroup
```

On a host already configured for delegated systemd user scopes, the explicit
invocation can be:

```sh
systemd-run --user --scope -p Delegate=yes -- \
  tbound-profile cgroup-reuse-check --confirm-disposable-cgroup
```

The confirmation flag authorizes two disposable child cgroups and four fixed
`/bin/sleep` processes. The check kills only its own target group, waits for it
to empty, then launches into the same group again using `CLONE_INTO_CGROUP`.
A fresh sibling and an outside process must remain alive. It records child
exit signals and cleans up its children and cgroups. It does not install a
kernel, invoke sudo, enable a service, or change host policy. Without the flag,
it refuses before creating anything.

JSON `result` is `pass`, `incompatible`, or `unverified`; all results are
advisory. Exit 0 means the single comparison passed; exit 2 means incompatibility,
unavailable prerequisites, an invalid comparison, or cleanup failure. Inspect
the JSON error and observations for the reason. A missing user bus or writable
delegation needs an operator-provided scope; rerunning this command as root
does not establish that prerequisite.

The audit reproduced a kernel defect on Ubuntu `7.0.0-38-generic`: a new clone
into a previously killed cgroup immediately received SIGKILL while both controls
stayed alive. An incompatible result requires an operator-qualified kernel with
the [upstream cgroup reuse fix](https://github.com/torvalds/linux/commit/8e359920216689b3b79e0fe8961a77fe312a511f),
followed by another explicit check after boot. Do not infer compatibility from
the version string or Landlock ABI alone. A pass covers this bounded behavior
in this scope; signed profile admission, controller limits, and the rest of the
governed runtime still require their own verification.

The doctor JSON report includes `dev_ready`, `governed_ready`,
`governed_prereqs_advisory`, and individual checks. `governed_ready` is
always false: only the runtime admission path can decide whether a governed
session may start. See [current status](status.md).
