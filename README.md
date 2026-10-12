# `tbound` prototype

**Status (2026-10-12):** reviewed governance components and a frozen runtime
integration increment are implemented and tested, but the prototype is **not an
experiment-ready, governed Pi deployment**. On Linux, `tbound serve --pi` is now a
managed-service client; it fails when the registered service is absent. The
service source-capture code path exists but remains unqualified on Linux with a
provisioned service. No positive governed run has occurred; `G1=false`,
containment remains `not-established`, and no real provider exchange, WP1/G1
conformance, or thesis result is claimed. Current status and next steps: [`docs/tbound-readiness-status-2026-10-08.md`](docs/tbound-readiness-status-2026-10-08.md),
[`docs/central-review-corrections-2026-10-07.md`](docs/central-review-corrections-2026-10-07.md),
[`TASK_STATE.md`](TASK_STATE.md).

**Run guide:** [Current native Linux and existing VM launch steps](docs/launch.md).

**Setup guides:** [Researcher quickstart and VM resume](docs/reproducible-setup.md)
and [fresh-host setup and offline verification](docs/setup.md).

`tbound` is a host-side supervisor for one Pi coding workflow. The first slice is
deliberately narrow: one Pi configuration, one agent, the proxy-backed `read`,
`write`, `edit`, and `bash` tools, one compliant task, and one declared Linux
VM/host profile. This README does not claim that its security properties have been
demonstrated; it records what exists and what remains.

## Design boundary

- A TypeScript Pi SDK adapter installs a **complete custom resource loader** that
  exposes only `read`, `write`, `edit`, and `bash`. Each tool sends a proposal over
  session-bound IPC and returns the supervisor's correlated result. The adapter does
  not independently access files, spawn commands, or call a provider. Pi and adapter
  inputs remain untrusted.
- A Go supervisor owns session lifecycle, IPC admission, deterministic policy
  decisions, durable workflow state, the isolated session repository, command-cell
  execution, delta validation, and evidence collection.
- A provider broker owns credentials and provider access. It constructs requests
  from a registered profile, enforces the fixed four-tool manifest and conversation
  lineage, captures the exact tool call before forwarding, and records the exact
  result before it reaches Pi. Credentials and HTTP stay in Go; Node/ Pi never receive
  the key or control the endpoint/model.
- The Linux runtime, immutable policy, evidence store, and registered external
  effect observers belong to the declared trusted computing base. Pi, model output,
  IPC payloads, repository contents, and commands are untrusted. This boundary is a
  design target, not yet a tested result.

```text
provider broker (Go: credentials, fixed profile, captures)
              │ registered exchange
              ▼
TypeScript Pi adapter ── session IPC ── Go supervisor
   four proxy tools                         ├─ gate + workflow + durable audit
                                            ├─ sealed session generations
                                            └─ isolated per-bash command cell
```

Provider, workflow, proposal, tool-call, and execution identifiers retain their own
issuer/namespace. A provider tool-call ID is correlation data; it is not
authorization or an idempotency key.

## Current implementation status

**Works (verified as components).** Captured-call broker correlation and
deterministic four-tool policy; bounded framed IPC; durable execution, sealed
generations, approved deltas, read/write/edit and command leases; write+fsync
ordering; audit reconstruction and quarantine; per-object publication with recovery
and cross-workflow quarantine. Real pinned Pi 0.87.1 (`read/write/edit/bash` proxy
tools) constructs and runs on Linux with the existing pinned `node_modules` (no
install). A development Go↔Node↔Pi process path and a Go-owned provider-conversation
bridge (real Pi SDK `read→edit→bash→read`, synthetic transport/executor) pass. A
read-only governance companion and a D06 private-Git primitive exist as reviewed
components. Reviewed foundations are committed at `8d2e478`.

**Not ready.** The Linux CLI is now a managed-service client, and the
systemd-activated service endpoint, authenticated handoff, journaling, and
idempotency code exist. Service-mode source qualification code also exists and
refuses source mismatches before worker exposure. These Linux paths, host
provisioning, and the real signed-profile route remain unqualified. No positive
governed Pi workflow, real provider exchange, or D09 runtime has been exercised.
D06 sealed-source export and read-only consumer-view code, managed-root
publication permits, command receipt validation, and Bash-origin resolution are
implemented but only fixture-tested; per-command receipts are not surfaced in
the CLI, and no live publication or independent effect-oracle run has qualified
them. Native TUI remains fixture/product work, not the claim-bearing CLI path
(D15).

**Required before thesis experiments can start.** (1) qualify the implemented
service-mode source-capture path on Linux with the real signed-profile flow, then
run one CLI-owned real-Pi workflow through the broker and durable executor: `read→edit→bash→read` with exact lineage and an authorized provider turn;
(2) qualify D06 source provenance and the read-only consumer view in the real
Podman/crun boundary; (3) surface per-command receipt evidence and complete live
publication with independent workspace/network/process oracles; (4) freeze a
declared Linux profile with verified Landlock ABI 8, rootless UID/GID mapping,
cgroup cleanup, seccomp and NNP; (5) pass the five WP1 spikes plus E04/E05/E06/E07
conformance before dataset runs.

## How to run the current prototype

The built-in Go fixture exercises the real durable Go pipeline but **does not launch
Pi** and is non-claim-bearing:

```sh
cd supervisor
go build -o /tmp/tbound ./cmd/tbound
/tmp/tbound serve --pi --native-fixture   # exit 0, claim_bearing=false
/tmp/tbound serve --pi                     # calls managed service; unavailable until provisioned
```

The real Go↔Pi development test is opt-in and needs a Linux Node, the pinned
dependency tree, and a private ext4 root:

```sh
export TBOUND_GOVERNED_PI_NODE=/home/jeli2k/node-v24.15.0-linux-x64/bin/node
export TBOUND_GOVERNED_PI_NODE_MODULES=<checkout>/adapter/node_modules
export TBOUND_GOVERNED_PI_TEST_ROOT=<private 0700 ext4 dir>
cd supervisor
go test -mod=readonly -run TestDevelopmentPiSDKWorkerInheritedFDProcessRoundTrip -v ./internal/piruntime
```

Read-only governance companion (unavailable/fixture/replay modes):

```sh
cd supervisor && go run ./cmd/tbound-governance --addr 127.0.0.1:8790
```

## Reproducing the evaluation environment

Use [`docs/reproducible-setup.md`](docs/reproducible-setup.md) as the guided
from-scratch setup, and [`infra/repro/README.md`](infra/repro/README.md) for
the scripts and their options. There are two paths: native Ubuntu 24.04 Linux
or an Ubuntu Hyper-V guest on Windows. Both use the same guest capability probe.
The scripts are new reproducibility tooling and have not yet been qualified by
an end-to-end run on a clean host; review their proposed changes before using
them on a valuable machine.

**Security note:** use a dedicated disposable VM for the thesis profile where
possible. The Ubuntu 24.04 user-namespace workaround changes a system-wide
AppArmor-related sysctl and weakens a default restriction for unprivileged user
namespaces. The bootstrap asks before applying it; do not use unattended
confirmation on a daily-driver workstation. Review the scoped AppArmor-profile
alternative if provisioning native Linux.

### Existing Windows Hyper-V VM: start, connect, resume

The reference guest is named `TBound-Ubuntu-2404`. After a Windows restart, open
**elevated PowerShell** in the checkout and run:

```powershell
Get-VM -Name 'TBound-Ubuntu-2404' | Select-Object Name, State
Start-VM -Name 'TBound-Ubuntu-2404'   # only if State is Off
vmconnect.exe localhost 'TBound-Ubuntu-2404'
```

Log in at the VM console as the configured guest user. In Ubuntu, get the
current DHCP address (it can change after a reboot):

```sh
ip -brief address show eth0
```

From Windows, connect with the SSH key registered for this guest. On first
connection, use `StrictHostKeyChecking=ask` and compare the displayed host-key
fingerprint against the guest console before accepting it:

```powershell
ssh -o StrictHostKeyChecking=ask -o HostKeyAlgorithms=ssh-ed25519 `
  -i "$env:USERPROFILE\.ssh\id_ed25519" tboundadmin@<guest-ip>
```

For a normal shutdown, power off Ubuntu cleanly, then confirm the Hyper-V state:

```sh
sudo poweroff
```

```powershell
Get-VM -Name 'TBound-Ubuntu-2404' | Select-Object Name, State
```

On the next Windows login, repeat `Get-VM` / `Start-VM` / `vmconnect.exe`,
re-discover the IP, and run the capability probe before experiments. Avoid
`Stop-VM -TurnOff` except as an explicitly accepted recovery action: it is an
unclean power loss and can leave filesystems or experiments in an uncertain
state. Do not edit checkpoint disks manually as a routine start/resume method.

**Current VM access and probe (2026-10-12):** `CodexSandboxUsers` established
strict SSH as `tboundadmin` to `192.168.251.23/20` after operator console login;
the ED25519 host fingerprint matched the pin in
[`infra/hyperv/INSTALLATION-RECORD.md`](infra/hyperv/INSTALLATION-RECORD.md).
The DHCP address is dynamic. Hyper-V `Get-VM` remains denied to this execution
context, and the host alias does not resolve. The fresh guest probe reported
13 PASS, 0 FAIL, and 2 UNVERIFIED; it reported kernel 6.8.0-146-generic with
Landlock ABI 4. HWE 7.0.0-38 reached userspace on repeated boots and ran for
minutes; inspected journal tails showed no panic/oops, but the reboot cause is
unknown and HWE is not qualified. The help-sheet IP `172.19.193.190` was
unreachable and remains historical. G1 remains false; this VM is not a clean G1
baseline. See [the run guide](docs/launch.md) and [TASK_STATE.md](TASK_STATE.md).
### Native Linux: start from scratch and resume

On a dedicated Ubuntu 24.04 amd64 host, follow the guided setup:

```sh
sudo bash infra/repro/bootstrap-linux.sh <trial-user>
```

Read and approve each proposed host change. Reboot if the guide says the kernel
changed, then rerun the probe. To resume after shutdown, sign in as the trial
user, enter the checkout, and verify the live profile before experiments:

```sh
uname -r
bash infra/guest/probe-capabilities.sh
```

For either environment, a passing capability probe alone does not qualify
containment. The remaining G1 gate is the five-spike conformance set plus E04,
E05, E06, and E07 fixtures on the declared host (see "Gate to G1 / experiments"
below). WSL is development-only, not the evaluation profile.

## Source layout

```text
adapter/src/            TypeScript Pi worker, four proxy tools, IPC client, broker-provider
supervisor/cmd/tbound/          Go supervisor entry point (fixture; production refused)
supervisor/cmd/tbound-governance/  read-only governance companion server
supervisor/internal/broker/     provider profile/request construction and correlation (broker)
supervisor/internal/providerbridge/ Go-owned provider conversation + inherited-channel bridge
supervisor/internal/ipc/        bounded, session-bound proposal/result transport
supervisor/internal/gate/       canonicalization and deterministic policy decision
supervisor/internal/sessionrepo/ independent, supervisor-owned session generations
supervisor/internal/delta/      approved-delta attribution, sealing, validation
supervisor/internal/workspace/  descriptor-relative scan/import with metadata policy
supervisor/internal/privategit/ filtered byte snapshot + new private Git (conformance)
supervisor/internal/publication/ per-object durable application and recovery
supervisor/internal/workflow/   publication lifecycle, recovery and quarantine
supervisor/internal/piruntime/  supervisor loop, child lifecycle, launcher (Linux)
supervisor/internal/sessionlaunch/ sealed-source session composition (in progress)
supervisor/internal/audit/      durable claim-critical records and bounded enrichment
supervisor/internal/governanceview/ bounded observation contract + read-only viewer
supervisor/internal/evaluationindex/ structural obligation ledger validator
```

## Effect and generation invariants

1. Correlate a proposal with the registered provider exchange before policy
   evaluation; reject unknown, duplicate, stale, reordered, or digest-mismatched calls.
2. Durably record claim-critical intent, decision, and single-use execution authority
   before releasing an allowed effect. Durably record a terminal result, or explicit
   `UNKNOWN`, before completing the proposal. Audit failure blocks effect/result release.
   Recovery never blindly replays an unresolved effect.
3. Read only from the governed session repository. Supervisor `write` and `edit`
   operations produce new supervisor-owned generations. Pi sees a read-only sealed
   generation; each `bash` call receives a disposable writable view of a sealed generation.
4. After `bash`, terminate writers, detach the command view, validate its complete
   delta, and import only attributable output into a new sealed generation. No Pi or
   command cell receives the live workspace or live `.git` administration.
5. Apply to the live workspace only through the separately validated publication path
   after the workflow is complete. A matching final tree alone does not establish that
   no transient mutation occurred.

These are intended contracts. Denying a proxy proposal alone does not demonstrate that
an undeclared Pi/runtime path was contained; the configured surface and the Linux
boundary need separate evidence.

## Gate to G1 / experiments

WP1/G1 requires more than a tool smoke test. On the declared candidate VM the plan
requires: (1) prove the Pi surface is closed to exactly the four proxy tools;
(2) capture one real provider-specific exchange and the E05 two-generation
`read → edit → bash → read` fixture; (3) prove independent repository isolation and
delta provenance (ordinary byte-copy views first); (4) exercise the selected rootless
Podman/crun containment profile and entrypoint; (5) fault-inject audit write, sync, and
disk-full failures; and (6) prepare the E04 transient-mutation and E06 denied-read
fixtures. The tested host and runtime identities must be frozen; a failed capability
probe requires redesign or a narrower claim, not an unrecorded runtime fallback.

The tracked starting point remains [`todo-implementation-tbound.md`](../MSE_MA_Thesis/agentic-harness/todo-implementation-tbound.md).
The intended order is Pi closure, broker correlation, generation/repository staging,
containment, durable authority, and a reconstructable evidence package. Regardless of
order, each governed effect follows the pre-effect durable-recording rule above.

## Open decisions

- Provisioning the declared production Linux evaluation host/profile (signed image,
  signed entrypoint, rootless Podman/crun, delegated cgroup-v2, Landlock/seccomp/NNP)
  — requires explicit operator authorization; WSL2 is development only.
- Provider/model live verification with the approved `nvidia/nemotron-3.5-lightning:free`
  profile and the Go secure credential loader (no live request yet).
- D06 sealed-source provenance, worker isolation, and durable provenance integration.
- Publication origin binding for workflows that end in a bash lease.
- Recovery protocol amendment: 48 trials (16 scenarios × 3); the eighth crash
  point is E07 publication conformance, outside that denominator. Record the
  dated amendment before registration or execution.

## Thesis design references

The normative thesis documents remain in the original thesis repository under
`agentic-harness/` (one folder up in the MA thesis root) and are not part of this
standalone repository. Key references: DOCUMENT-GOVERNANCE.md,
AH-technical/02-process-model.md, AH-technical/02-1-recovery-and-continuation.md,
AH-technical/03-architecture.md, AH-technical/06-4-sandboxing-decision.md,
AH-technical/10-decision-register.md, and AH-technical/11-evidence-contract.md.
