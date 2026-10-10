# TBound current handoff

Updated 2026-10-08. This is a current-state index, not a chronological execution log.
Central review accepts bounded component code, NOT an experiment-ready release.

## Goal and non-negotiable claim boundary

- User now requires experiment-ready TBound: a complete runnable implementation
  for thesis datasets/hypothesis testing, not permanent component fixtures.
  Readiness requires one admitted CLI/Pi/broker/durable-tool/publication path,
  integrated D06, declared containment/settlement and reproducible trial evidence.
  H1 results are produced by later experiments; implementation readiness must not
  be confused with a positive hypothesis result or bypass missing profile proof.
- Product direction: actual Pi harness/native TUI under TBound governance plus a
  separate read-only governance companion; not replacement chat or a custom SDK client.
- Pi retains conversation/tool/presentation behavior; TBound owns admitted launch,
  broker/tool authority, lifecycle, durable generations, publication and evidence.
- Production `serve --pi` remains refused; no stock CLI/SDK fallback or invented profile.
- `G1=false`; containment remains `not-established`. Component/fixture passes do
  not prove H1/G1, deployed containment, provider authenticity or thesis completion.
- D15: CLI-first claim-bearing thesis MVP; native TUI/product work and companion
  UI are optional scope, not substitutes for the thesis-critical evidence path.
- D06: private-Git primitive is conformance-only; prototype waiver/divergence
  remains until trusted runtime integration and independent proof establish compliance.
- Publication is per-object durable reconciliation, not global atomicity or blind replay.
- E07 conformance and the secondary recovery study are separate. The source says
  seven crash injections but enumerates eight: 48 vs 51 trials is unresolved.
  Require dated protocol reconciliation before registration/execution; no silent choice.

## Current implementation status (2026-10-08)

**One line:** governance components are built, tested and centrally reviewed; the
single runnable governed Pi workflow for thesis experiments is not finished.
Production `serve --pi` intentionally refuses; `G1=false`, containment `not-established`.

### Works (verified)
- Policy/broker/IPC core: captured-call correlation, deterministic four-tool
  decisions, replay/mismatch refusal, bounded framed IPC.
- Durable execution + audit: sealed generations, approved deltas, read/write/edit,
  command leases, write+fsync ordering, reconstruction and quarantine.
- Publication: one-use internal authorization, per-object apply/recovery,
  cross-workflow quarantine.
- Real Pi on Linux: pinned Pi 0.87.1 four-tool SDK constructs and runs with the
  existing pinned `node_modules` (no install). Direct Node at
  `/home/jeli2k/node-v24.15.0-linux-x64/bin/node`; Go at `/home/jeli2k/go-sdk/go/bin`.
- Go<->Node<->Pi process path (development): launcher/worker round-trip passes;
  real Pi proposes a tool call, receives a synthetic Go `DENY`, continues, and
  reports `UNKNOWN` on uncontained cleanup.
- Broker-owned conversation bridge: real Pi SDK test with Go-owned history and
  synced tool results (`read->edit->bash->read`); transport/executor still synthetic.
- Governance companion (read-only viewer) and the D06 private-Git primitive exist
  as reviewed components.
- Reviewed foundations committed at `8d2e478` (docs `640cf30`); see the central
  report `docs/central-review-corrections-2026-10-07.md`.

### Not ready
- No admitted production workflow: `serve --pi` refuses; CLI session composition
  is still being built and currently has temporary compile errors in uncommitted files.
- No real provider exchange: the secure loader and approved model exist, but no live
  request is authorized or performed.
- No containment profile: WSL lacks Podman/crun/Cosign and usable delegated cgroup;
  the direct cgroup launcher is explicitly not the D09 profile.
- D06 not integrated: sealed-source provenance, worker isolation and durable
  provenance are not yet wired into a run.
- Publication origin gap: the finalizer currently rejects bash-lease-ending
  workflows such as `read->edit->bash->read`.
- Native TUI remains fixture/product work, not the claim-bearing CLI path (D15).

### Required before thesis experiments can start
1. One CLI-owned run: protected profile -> launch real Pi -> Go broker -> durable
   executor -> `read->edit->bash->read` with exact result/generation lineage.
2. Real provider turn-through with credentials only in Go (no Node history forgery).
3. D06-backed session: sealed source, no `.git`/admin reachability for Pi, durable
   provenance before exposure.
4. Resolve the bash-lease publication origin.
5. Provision the declared Linux profile (signed image/entrypoint, rootless
   Podman/crun, cgroup kill, Landlock/seccomp/NNP) under operator authorization.
6. Registered conformance (five spikes + E04/E05/E06/E07), then dataset runs.

## Installation/distribution workstream (started 2026-10-08)

User asked for easy install of tbound next to Pi. Blocker evaluation: tree builds
(Windows and `GOOS=linux` build/vet pass); no packaging/installer/release exists;
`cmd/tbound` has no `version`/`doctor`/`install`/`run` surface; the launcher takes an
explicit Node path and a pre-existing `node_modules` with no discovery/install;
profile signing is absent (governed Phase 2). Two GPT 6 Luna max workers (disjoint,
uncommitted):
- `ses_ee395f2dbffeRUSH7gvITVPAbe`: root `Makefile`/release build, `install/install.sh`
  (curl|sh, checksum-verified, shared prefix, no sudo for dev), `packaging/npm`,
  `scripts/verify-install.sh`, `docs/install.md`.
- `ses_ee395bdc9ffeEQhJsiIu9HPTYV`: `supervisor/cmd/tbound-doctor` and
  `supervisor/internal/piinstall` (Node/Pi discovery + side-by-side pinned Pi install
  into the prefix) plus `docs/doctor.md`.

Shared prefix contract: `${TBOUND_PREFIX:-~/.local/share/tbound}/{bin,runtime,node,pi}`;
config `~/.config/tbound/config.json`. Phase 1 is a dev install (fixture + real dev
path); governed `serve --pi` stays unshippable until the signed profile and
containment exist. Central GPT 6.1 Sol high review is required after the batch before
acceptance.

Coordinator release baseline (private ext4, committed HEAD `640cf30`): Linux build
of `cmd/tbound` succeeds; `serve --pi --native-fixture` exits 0 with
`claim_bearing=false`, `publication_status=SUCCEEDED`, `containment=not-established`;
`serve --pi` exits 2 with `native Pi host profile is missing; production --pi launch
is refused`. This is the exact assertion `scripts/verify-install.sh` must reproduce.
Both install workers hit the shared account rate limit before producing output; they
will be resumed (no restart) when the cooldown clears, then centrally reviewed.

## Start or continue manually

Current Go fixture (does **not** launch Pi):
```sh
cd supervisor
/home/jeli2k/go-sdk/go/bin/go build -o /tmp/tbound ./cmd/tbound
/tmp/tbound serve --pi --native-fixture   # exit 0, claim_bearing=false
/tmp/tbound serve --pi                     # exit 2 (refusal, expected)
```

Real Go<->Pi development test (opt-in; needs Linux Node, pinned deps, private ext4 root):
```sh
export TBOUND_GOVERNED_PI_NODE=/home/jeli2k/node-v24.15.0-linux-x64/bin/node
export TBOUND_GOVERNED_PI_NODE_MODULES=<checkout>/adapter/node_modules
export TBOUND_GOVERNED_PI_TEST_ROOT=<private 0700 ext4 dir>
cd supervisor
go test -mod=readonly -run TestDevelopmentPiSDKWorkerInheritedFDProcessRoundTrip -v ./internal/piruntime
```

Companion viewer (read-only; unavailable/fixture/replay):
```sh
cd supervisor && go run ./cmd/tbound-governance --addr 127.0.0.1:8790
```

To continue implementation manually: inspect the uncommitted batch first; fix the
temporary `cmd/tbound` compile errors; keep the launcher refusing production until
the sealed view + Podman profile exist; the next safe coding step is session
composition (sealed view, durable provenance, bash-lease origin). Provider/VM/
signature/cgroup work needs explicit operator authorization first.

## Authorization, preservation and model rules

- Preserve all unrelated uncommitted work, especially user-authored docs/Hyper-V
  infrastructure and `docs/frontend-observability-plan.md`; never adopt it wholesale.
- Never access `vm_start.txt` or inspect/log/copy secret key material. No provider
  credentials or ambient credential environment in fixtures; retain redacted evidence only.
- No reset/discard, new worktree, broad deletion or blanket staging. User now
  authorizes explicit-path commits and pushes of large accepted increments to
  the existing runtime PR; never publish unrelated dirty work or secrets.
  Coordinator alone owns acceptance/evidence and explicit-path implementation commits.
- No dependency/module/lock changes without separate coordination; no privileged
  host/task/VM/checkpoint/NIC/network/controller/key operations or dirty infra edits
  under these source-only tasks. Operator prerequisites need explicit authorization.
- Historical guest addresses, task installation and offline passes are not current
  host evidence; do not retry old runs or bypass reviewed identity/admission gates.
- Keep ownership disjoint; no polling or duplicate dispatch to existing workers.
  A prior requested blocking wait timed out; completion notifications supplied status.
- Foundations and their central review finished; concrete vertical-slice workers
  are active. Production integration and release qualification remain outstanding.
- Latest user rule (2026-10-09): **all subagents from now on use
  `openai/gpt-6-luna#max`** only — including
  the central safety/integration/thesis reviewer. No other model, no silent fallback.
  This supersedes earlier DeepSeek-high and other model rules. Stop at each completed,
  independently verified and centrally reviewed increment before beginning another.

Current production increment: profile tooling and D09 launcher worker
`ses_ee2d8dfa3ffeYOZqNmOrCvlr7U` is active. Actual provisioned launch must be
verified separately from injected-command unit tests and missing-prerequisite
refusal. Read-only adoption research is qualified in `docs/governed-linux-adoption.md`:
candidate supported Linux hosts, explicit operator setup, side-by-side Pi and
offline public verification; no VM/kernel/trust installation is authorized by it.
Candidate review HOLD: local image-verification invocation, fail-open Podman-info
errors, unbound executable/view, and unbacked cgroup/pidfd settlement must be fixed.
Passing focused race/profile-init/refusal tests is not proof of provisioned launch;
candidate still returned a permanent session-composition code error after probes.
Three disjoint DeepSeek high owners now close this same increment: original worker
runtime/profile/entrypoint; `ses_ee395bdc9ffeEQhJsiIu9HPTYV` sessionlaunch verified
sealed/runtime views; `ses_ee2f7698bffevuyCHGwduvk6Ol` CLI/broker/durable/publication
composition. No production commit until integrated verification and renewed review.
All three returned; combined central review is active. Runtime backend and view
adapters are reported implemented, but default CLI still has an unwired production
binder/assembly. Treat this as a CODE blocker even on a provisioned host.
User selected existing Hyper-V Ubuntu VM on 2026-10-09 for the setup PLAN ONLY;
no package/kernel/VM/network/trust changes authorized. Preserve Secure Boot/AppArmor.
Combined candidate `tbound-combined-production.O5RV8S` passed full Linux race,
vet/gofmt and unchanged-source readback with required docs included. Production
launch is still unverified and binder assembly remains open; central verdict pending.
Runtime owner published real binding constructor; CLI owner is adopting it.
Coordinator identified a new positive-path blocker: Go entrypoint required exactly
one process thread before Landlock, incompatible with normal Go runtime threads.
Owner is fixing process-wide enforcement via an explicit supported profile, not
removing the guard or equating `LockOSThread` with process-wide isolation.
A reports Go ABI-8 TSYNC enforcement implemented, dedicated trusted proof fd10
closed before Node exec, and exact binding constructor published. WSL ABI1 is
unsupported; qualified enforcement/production launch still unverified. CLI owner
is wiring this contract; no actual VM/kernel/trust setup or production commit.
User requested summary/next-steps and STOP when this increment finishes; no further
feature batch or VM/kernel/package/network/trust operation without instruction.
CLI concrete production binder returned; final combined review is active. First
new snapshot `Zcd8dp` omitted workflow.TerminalOriginResolver overlay and failed
vet; corrected full source with that dependency is rerunning, not a waived failure.
No provisioned positive run or production commit. No new VM/setup permission.
Full corrected combined snapshot `Huml7x` passed race/vet/gofmt/source readback;
central review still HOLD:
production Bash used the legacy non-D09 Alpine runner, publication was unassigned,
TCB proof dropped and signed OCI layout not matched to the image actually run.
Runtime A/CLI C now fix those; B adds Linux profile/entrypoint release artifacts.
No production commit or readiness claim until renewed all-owner central review.
CLI follow-up wires real publication and rejects legacy Bash containment. It
still refuses production Bash until a registered command-cell runner is bound.
Coordinator rejected A's first command runner: opened view was unused, fresh
scope was not bound to execution, helper/proof FDs were absent, and settlement
flags were asserted without observations. A is correcting those defects and
surfacing proof through the actual worker-launch path; both remain code gates.
Receipt fields alone are not proof-journaling evidence. No positive production
launch, VM provisioning, production commit, or new operator approval occurred.
All three follow-up owners (runtime A, CLI C, packaging B) subsequently stopped
with `Go usage limit exceeded`. Their latest tasks are incomplete; partial edits
must be inspected before resuming and are not accepted verification evidence.
CLI publication quiescence was additionally challenged: baseline/source checks
alone do not establish stopped writers or protected live-root quiescence.
Resume with the mandated DeepSeek-high model when quota is available, or only
use a replacement model after explicit user authorization. Candidate remains
uncommitted/on HOLD; combined tests from earlier snapshots do not cover these
unfinished follow-ups. No provisioning or production-readiness claim is allowed.
User explicitly authorized GPT 6 Luna xhigh and continuation on 2026-10-09.
All three owners resumed on `openai/gpt-6-luna-xhigh` to inspect partial edits
and finish the same runtime/CLI/packaging increment in disjoint scopes. Prior
quota-blocked status above is historical; source-only authorization is unchanged.
Renewed combined verification and a Luna-xhigh central review are still required.
Packaging B returned local passing release-layout (64) and npm (21) checks,
not publication or governed trust evidence. Narrow follow-up ownership includes
`.github/workflows/release.yml` to upload generated entrypoint metadata/checksum
sidecars already listed by SHA256SUMS. Missing canonical license text remains a
public-release blocker; no legal choice, publish, or trust provisioning authorized.
RPM/Homebrew/container actual builds remain unverified. Runtime/CLI still active.
Packaging sidecar CI follow-up completed: three coverage checks passed. Integrated
layout rerun passed 31 checks but skipped Linux because concurrent CLI edits had
`production_binder_linux.go:725: undefined: delta`; CLI owner notified. That skip
is not Linux packaging acceptance. Rerun full Linux layout after source freezes.
User requested an interim thesis-track audit against the master's
`agentic-harness/tbound.md`. Coordinator read the overview: it is non-normative,
defers to tbound-thesis.md, requires actual Pi/four governed tools/external
authority and independent effect evidence, and explicitly excludes production
readiness/multi-agent hosting from current claims. Luna-xhigh reviewer is doing
a read-only authoritative-contract/source alignment audit while A/C finish.
This interim audit does not replace the final frozen-candidate central review.
Latest user clarification: prioritize TBound product implementation; thesis
evaluation readiness is a later milestone, not a prerequisite to finish useful
governed Pi/tool/publication/install functionality. Maintain design compatibility
and honest evidence, but do not expand this increment into an empirical campaign.
User requested GPT 6.1 Luna max; catalog has no 6.1 Luna. After explicit
clarification user selected available GPT 6 Luna max (`openai/gpt-6-luna#max`).
All subsequent subagent work/reviews use that exact model/variant; active runtime,
CLI and read-only direction-audit owners received the product/model steering.
User explicitly permits sub-subagents when owners are stuck or looping,
superseding prior owner-prompt bans on nested delegation. All nested agents must
use `openai/gpt-6-luna#max`; preserve disjoint ownership, prefer focused diagnosis
or isolated tests, and have the parent verify/integrate results. Runtime/CLI
owners were notified. This does not expand provisioning or secret permissions.
Interim audit returned: aligned architecture/product goal, but current-source
publication always refuses quiescence, exact terminal-Bash origin fails chain
representation, xattr visibility is asserted Complete:true without measured
profile-bound authority, and worker cgroup cleanup/runtime/seccomp identity have
gaps. These are product-code findings, not a requirement to run thesis experiments.
A/C received exact findings. C narrow ownership now includes internal/publication
for a versioned terminal-lease/provider-call publication binding with recovery
compatibility; A handles effective crun/seccomp binding and owned scope cleanup.
Final frozen-source verification/review remains required; no readiness claim.
Latest user instruction: continue runtime/CLI/packaging thoughtfully, commit
large completed increments to the existing PR for persistence, and stop/report
when the high-level product goal is reached. Existing branch verified as
`feature/tbound-runtime-batch`, HEAD `60631d039e11d5d52a4e939a53b1168391beb0d6`.
Coordinator commits/pushes only explicit owned paths after combined verification
and central acceptance; PR #7 remains the persistence target. Active owners do
not stage/commit. Goal: real Pi plus four governed tools, observed cleanup,
validated publication and usable installation; safe refusal is not completion.
This supersedes stopping at the current increment if more product work is needed,
but does not authorize VM/guest/provisioning/kernel/network/provider/trust changes.
Actual provisioned positive launch is still a separate approval/measurement gate.
Packaging strict acceptance completed in source: Linux failure or required skip
now fails `--require-linux` / `TBOUND_REQUIRE_LINUX_RELEASE=1`, wired into CI.
Controlled strict-mode regressions passed (3); upload coverage passed (3).
Full strict product artifact acceptance remains pending A/C source freeze;
coordinator's combined snapshot now includes these tests, workflow and install
assets and will rerun strict layout/npm/package-manager checks independently.
Runtime A and CLI C subsequently stopped with all GPT accounts rate-limited;
their current edits/tests were interrupted, not accepted. User explicitly resumed
on 2026-10-10; both owners resumed on `openai/gpt-6-luna#max`, first inspecting
partial edits and continuing the same product increment. Packaging remains
completed pending integrated strict artifact tests. No new host/operator approval.
No finished-candidate commit or positive governed launch has been established;
combined freeze/verification and fresh central review remain required before
explicit-path commit/push to existing PR #7 for persistence.
Coordinator independently reran packaging gate regressions on 2026-10-10:
upload/checksum coverage 3/3 and controlled strict-mode failure handling 3/3,
exit 0; private logs `/home/jeli2k/tbound-packaging-gates.yPxPWt`. These are
controlled regression checks only, not real product artifact acceptance or
positive governed launch evidence. Full frozen-source strict packaging pending.
CLI owner returned bounded build/vet/race passes after fixing an E05 test's
unreleased admission lease. This is not product acceptance: publication still
always refuses trusted live-root quiescence, and per-command D09 proof receipts
are not surfaced to the CLI. Runtime owner received the specific receipt API
dependency. A separate Luna-max read-only contract helper is identifying the
minimal legitimate live-root writer/quiescence authority before C resumes that
fix; ownership/mode checks or labels alone cannot mint no-writer evidence.
Do not freeze/accept/commit this as a complete product while those code gates
remain open. No actual host/provision/provider approval was added.
Runtime A published one-use `D09CommandRunner.TakeCommandReceipt(view, spec,
result, settlement)` after exact Run. Live `Validate()` checks issuance;
JSON/`ValidateSnapshot()` is structural durable evidence only, not authority.
C resumed exact API integration: retain live receipt, journal full identity/output
bindings before result release, and verify live issuance plus exact durable event
against OperationEvidence at publication. Runtime final rerun remains pending.
Quiescence contract helper returned: exclusive publication requires a registered
managed root and a trusted writer authority, not 0700, flock, process scans or
settled cells alone. C is implementing a root/run/epoch-bound opaque writer lease
and one-use drained publication permit, held through durable finalization. A
provides protected signed root/writer-policy registration and actual verifier
inputs; signatures authenticate policy, not current quiescence by themselves.
Production target is a managed workspace with dedicated non-login writer/service
identity; ordinary same-UID developer roots would have only cooperative-writer
assumptions and are not silently admitted as exclusive. Operator setup remains
separately authorized; no account, service, mount or trust changes have occurred.
Runtime A reported concrete managed-root verification/registration APIs and
fixed signed registration path/domain/template digest binding; CLI owns permits.
Local A race/vet/build passes are not integrated acceptance. C's current new
managed-root event literal has an event-schema compile mismatch (unknown
RootLedgerSequence/RootLedgerRecordHash fields); C received the exact report.
Coordinator updated the adoption plan for managed-root and host FD3 Node
prerequisites without performing any operator setup. Final freeze remains pending.
CLI receipt/managed-root integration returned with adversarial tests and a
test-only managed-root E05 publication path. Latest shared tree is not green:
A config missing XattrVisibilityAuthority, command view reservation rejects
first use, and retained managed-root verifier has no launcher close/unbind.
A received all three concrete blockers. C's latest journal-failure test awaits
a compiling tree; prior snapshot passes are not final acceptance. Freeze/review
must wait for corrected A plus complete integrated rerun and cleanup wiring.
Runtime A reports those three defects fixed, exact launcher Close(ctx) API
published, fresh local Linux checks passing; C is fixing an E05 wrong-origin
publication prepare/token ordering failure and wiring close after settlement/scope
removal. Coordinator's pending combined script includes real pinned-Pi offline
development regression and precise production/missing-profile refusal checks;
neither is a provisioned positive-production receipt. Final freeze still pending.
First new freeze `KOjGld` aborted correctly: C test file changed during copy;
no tests/review from it count. C then acknowledged edit hold and clarified E05
checks only new wrong-origin events against an existing resolved epoch-seed
baseline; no weakening to authorize a wrong origin. A reported its final private
descriptor-helper/test edits unverified, then a new clone-ownership test failed.
Coordinator inspected the boundary: public DuplicateCapability(ctx) revalidates
policy before private held-descriptor duplication. A has a narrow temporary hold
lift only to fix redundant private policy validation without weakening public
admission, rerun targeted tests, and re-enter edit hold. Refreeze after that fix.
Narrow fix passed; exact final A hashes were independently confirmed. Both
owners held edits and frozen candidate `aehHOH` passed before/after-copy owned
input equality. Independent full Linux race suite passed; vet/tagged checks,
private builds, precise production refusals and real pinned-Pi offline dev
regression passed. Initial script stopped at strict packaging because coordinator
used RPM-invalid test version `0.0.0-verify`: 65 checks passed, one version check
failed. Failed log retained; same frozen source reruns packaging with valid 0.0.0,
then full snapshot/current-input readback. No candidate source edit was made.
Windows frozen build passed but full vet/tests failed; exact baseline 60631d0
independently reproduced all five pipe-alias tests and three unsafe.Pointer vet
diagnostics, proving them pre-existing. Do not report a green Windows suite.
Luna-max independent central review is active against this exact frozen source.
Private explicit-path inventory selects 70 candidate files and excludes all
unrelated dirty work, including docs/dev-launch.md and supervisor/tbound; nothing
is staged. Conditional persistence still requires final review/verification.
Same frozen `aehHOH` packaging rerun passed: strict layout 66/66 with zero skips,
npm 21/21 (actual launcher 8/8), and clearly synthetic manager layout checks.
Full snapshot and original owned-input readback passed unchanged. Coordinator
added `docs/production-candidate-verification-2026-10-10.md` as a separate evidence
record, not a runtime-source change; it retains failed-attempt and Windows-baseline
caveats. Final Luna-max code review remains pending. Nothing staged/committed.
Frozen central review returned conditional boundary safety but HOLD user-usable
product: managed non-login service has no ordinary CLI task handoff; effective
rootless UID/GID maps are not signed/proof-bound; exact service state-write paths
need an allowlist. Xattr visibility needs independent operator qualification,
not scanner identity metrics alone. A/C hold is lifted narrowly for those fixes:
C local authenticated Unix-socket CLI/service path; A maps, proof and protected
service registration/write policy. No remote/web/multi-agent expansion or host
setup permission. Supplemental read-only review checks raw D06 bind-mount calls
against the mandatory initial-userns zero-capabilities scanner/service profile
and identifies a legitimate bounded mount authority if needed. No commit yet;
new code must be refrozen and verified/reviewed rather than inheriting aehHOH passes.
Supplemental frozen review confirmed D06 host mount is impossible under required
initial-userns zero-capability service. Minimal fix chosen: real sealed source
export only to trusted Podman/crun setup; actual read-only consumer view verified
and raw source descriptors closed before Node. B session
`ses_ee395bdc9ffeEQhJsiIu9HPTYV` now owns sessionlaunch adapter and narrow shared
sessionrepo source-cap export if needed to avoid import cycles; its proposed
VerifiedD09ExposureSource is not yet a live API. C must publish exact Unix-socket
activation/authentication schema to A. Both were explicitly asked to send actual
contracts directly; A holds signed schema edits until confirmation, avoiding
fictional interfaces and a producer/consumer waiting loop.
Host service IDs remain distinct from cell IDs. Target one-self-ID mapping uses
explicit --uidmap CellUID:0:1 / --gidmap CellGID:0:1 without --userns; trusted proof
must bind inside maps and host-visible CellUID:ServiceUID:1 mappings for actual
container init PID, not Podman's CLI PID. Official-doc research is not a host test.

## Central review and current batch

Reviewed foundations committed as `8d2e47833030b8c723049006252a24d2d6712be0`.
Explicit 50-file staging excluded new worker changes and the user-owned frontend
plan. Post-commit comparison: 48 exact files, two CRLF->LF normalizations only
(policy patch script and loader); no non-line-ending changes. Exact committed
snapshot `tbound-central-review-foundation.lzrfJH` passed full Go race/vet/gofmt/
CLI and source readback; committed-only credential-free npm check and 17 native
tests passed. This closes the normalization verification gap, not production readiness.

Central reviewer conclusion (report `docs/central-review-corrections-2026-10-07.md`;
user-facing status `docs/tbound-readiness-status-2026-10-08.md`): **component code
accepted; integration NOT READY; thesis alignment ALIGNED with normative gates open.**
No D06/G1/H1 acceptance. The earlier HOLD and its resolved findings are history, not
active code defects. Reviewer run IDs: `ses_ee8f936deffeSoSBSkLpHSM0p6` (frozen
`tbound-central-review.9EITMJ`, source-list SHA-256
`cc04bd6e78c37b59884be4dea0a5267ffcc667a1b8df43fa33f122e56252f719`).

Current concrete vertical-slice batch (GPT 6 Luna max; **uncommitted by workers**,
subject to the central gate below):
- `ses_ee90a0ae2ffecu5hnmXg1Nt6od` — provider bridge `supervisor/internal/providerbridge/**`
  and `adapter/src/broker-provider*`: **done and independently verified**. Real Linux
  Pi SDK cross-process E05 with Go-owned history/result continuation (snapshot
  `providerbridge.NawvJT`); transport/executor synthetic. `docs/provider-conversation-bridge.md`.
- `ses_ee90afdf3ffexcnQKQh6XzVohX` — launcher/bootstrap and actual Pi worker entrypoint
  (`piruntime` launcher files, `adapter/src/governed-pi-worker*`): **development process
  proof** (real Go/Node/Pi DENY/continuation, `UNKNOWN` uncontained cleanup); the D09
  production launcher still refuses. `docs/governed-pi-launcher.md`.
- `ses_ee90a5123ffeDXfZWejFoVgAYh` — sealed-store-backed D06 verification, durable
  provenance and CLI session composition (`supervisor/internal/sessionlaunch/**`, owned
  `cmd/tbound` files): **in progress**; has temporary compile errors and must use a
  verified sealed/exposure view (no raw private-Git CWD/`.git` reachability for Pi).
  `docs/governed-session-composition.md`.
- `ses_ee90aa1f0ffeCJkUywyd25UZHi` — read-only current experiment-host/runtime
  inventory: **done**; `docs/experiment-host-prerequisites-2026-10-08.md`.

Integration constraints already flagged: the launcher must not expose the raw
private-Git root to Pi — the session owner must bind a verified sealed/exposure view;
resolve the provider-fd/bootstrap-fd role map before wiring; publication must handle
the bash-lease origin. Development process runs use an `UNKNOWN` settler; they are not
containment evidence.

Preservation incident: a runtime build overwrote the pre-existing untracked
`supervisor/tbound.exe` (5,667,840 bytes; SHA-256
`2a49661c91aeae671b1d2c3d1b463fedb00091955618f2fa8077796dfe221089`). Retained,
not restored/deleted/staged. All later build outputs must use explicit private `-o`
paths. No readiness claim follows from this incident.

Private pre-verdict handoff backup under approved Temp/opencode:
`task-state-pre-verdict-refresh-2451ca39506c4e6cba5e50b18e9e69bc.md`,
SHA-256 `468d1d371884ccb84190229667b8bbf86af7003ef25e99a56d3756661acd35a4`.

For each new batch before acceptance, implementation commits or next-stage work:
1. Freeze ALL agents' exact combined changes, including this handoff cleanup.
2. Dispatch a CENTRAL read-only GPT 6.1 Sol high reviewer of all changes and
   cross-component contracts. Individual reviews/passing retries are not this gate.
3. Require separate verdicts for code safety, integration readiness and thesis alignment.
4. Require requirement/decision -> implementation -> verified evidence -> gap/divergence
   traceability; assess D15, D06, provider/tool authority and lineage, G1/containment,
   publication, evaluation freezes and workload/recovery-count ambiguity.
5. Flag architecture derailment, feature creep, duplicated authority/validators and
   unsupported claims; recommend keep/change/defer plus a thesis-critical priority path.
6. Assign findings to owners and renew combined review after fixes. Do not silently
   amend normative thesis decisions or promote UI/component passes into H1/G1 evidence.

Authoritative sibling material (relative to this repository):
- `../MSE_MA_Thesis/agentic-harness/AH-technical/00-roadmap.md`
- `../MSE_MA_Thesis/agentic-harness/AH-technical/02-process-model.md`
- `../MSE_MA_Thesis/agentic-harness/AH-technical/02-1-recovery-and-continuation.md`
- `../MSE_MA_Thesis/agentic-harness/AH-technical/10-decision-register.md`
- `../MSE_MA_Thesis/agentic-harness/AH-technical/11-evidence-contract.md`

## Completed workers and retained ownership

Current code is accepted as bounded components, not a final runnable integration.
Session IDs below are retained for provenance/owned follow-up, not permission to poll.

| Scope | Implementer | Independent review / diagnosis |
| --- | --- | --- |
| Native Pi host/policy | `ses_ee90afdf3ffexcnQKQh6XzVohX` | `ses_ee904f01dffeYswmX86A93BZI6` |
| Companion frontend | `ses_ee90aa1f0ffeCJkUywyd25UZHi` | `ses_ee8f31f23ffe1ypWMn6Jc1lcOg` |
| Runtime composition | `ses_ee90a5123ffeDXfZWejFoVgAYh` | `ses_ee901d348ffeh2S1H1pvoJNlD0` |
| Evaluation unlock/index | `ses_ee90a0ae2ffecu5hnmXg1Nt6od` | `ses_ee8f87862ffeuIMSQVViTSu2Ie` |
| Private Git staging | `ses_ee902c73affePtFO5Sgt8HbYOk` | `ses_ee8f936deffeSoSBSkLpHSM0p6` |
| Explorer diagnosis / state cleanup | `ses_ee8ec8962ffeyA0v0Rd7QAKN8v` | centrally accepted as bounded corrections |

Owned paths and current completion boundaries:
- Native: `adapter/src/native-pi-*`, `adapter/native-pi-policy/`,
  `docs/native-pi-integration.md`. Actual Pi InteractiveMode; stock/inert and
  experimental pinned-source post-init fixture scope only, not usable production launch.
  Coordinator's 17 native/policy/SDK checks passed; tested bytes matched the freeze.
  Input SHA-256 `7dc366e8609d7d1e81fb85952e466aea9933b13e74842c2a9b9df678d267d12e`
  -> latest output `d48a7c98e2ee2f97ecf4b148b37098dae35fb5afe874318879c5d7d953cab953`.
  Installed node_modules/dependencies were not patched by this regeneration.
- Runtime: `supervisor/internal/piruntime/**`, owned `supervisor/cmd/tbound/**`,
  `docs/native-runtime-composition.md`. Immutable child capability bindings,
  lifecycle/observer bounds and CLI schema/writer/status corrections returned;
  focused race checks and corrected real CLI fixture exit 0 reported.
  Fixture uses real durable Go IPC/sessionrepo/publication but DOES NOT launch Pi:
  `claim_bearing=false`, `provider_exchange=false`, containment `not-established`.
  Trusted verifier/immutable launcher interfaces alone are not platform/profile proof.
- Private Git: `supervisor/internal/privategit/**`, narrow workspace filter/walker
  changes, `docs/private-git-staging.md`. Descriptor-bound Git execution, pre-copy
  reserved-metadata rejection, exact config validation/post-init rescan and cleanup
  hardening returned. Conformance only: trusted sealed-source binding, immutable
  execution boundary, exclusive cleanup namespace, durable provenance and worker
  isolation remain runtime integration obligations; privileged mount coverage unproved.
- Companion: `supervisor/internal/governanceview/**`,
  `supervisor/cmd/tbound-governance/**`, `docs/governance-companion.md`.
  Corrected provenance downgrade, no-follow document loading, snapshot/SSE gap-resume
  and source-health handling returned. Live file tailing remains refused until safe
  descriptor-bound support. Corrected tests/browser results are agent evidence only;
  coordinator's earlier independent unavailable-mode browser check is narrower.
- Evaluation: `supervisor/internal/evaluationindex/**`,
  `docs/next-evaluation-unlocks-2026-10-07.md`,
  `docs/evaluation-evidence-index-2026-10-07.json`. Strict parser corrections returned.
  16 entries / 34 citations; structural source/index validation, NOT cryptographic
  artifact/provenance attestation. Preserve supervisor/docs sibling layout in tests.

## Unresolved review findings and integration blockers

- Native startup/default-deny, digest binding, cancellation, credential-name and
  private-state corrections passed central code review. Artifact remains a local
  experimental fixture: absolute checkout imports/transitive dependencies are not
  a portable pinned production bundle; shipping needs full upstream license notice.
  Actual minimal-environment/profile and runtime-mutation governance need deployment proof.
- Go-to-Node composition now has a passing development process path (bootstrap/
  observation descriptors, broker-owned captures/result/generation continuation).
  Still missing for production: real provider continuity, the sealed/exposure
  worker view (no raw private-Git/`.git` reachability), and independently observed
  descendant settlement. Missing Linux Node/runtime must refuse/skip, never fall back.
- Production requires reviewed installed/attested host/provider profiles, actual
  executable/environment binding, peer-authenticated IPC, containment and settlement
  proof. No structural receipt, boolean or callback substitutes for those facts.
- Fresh current-source offline guest verification and frozen containment are blocked
  on reviewed controller/source pins, protected operator/helper/tasks/enrollment,
  native argv/cleanup admission corrections, signer/cosign/entrypoint/profile and
  cgroup-v2 teardown/settlement prerequisites. This task authorizes none of those operations.

## Explorer/private-Git test corrections (centrally accepted)

The explorer terminal assertion now waits for the exact fixture checkpoint
(`waitForCheckpoint(t, buffer, "real go test stream", int64(len(raw)))`) instead of
catalog cardinality; the Git test now uses a trusted `t.TempDir()` root instead of a
hardcoded `/home/jeli2k`. Independent checks passed 1,000 explorer and 10 Git race
repetitions, both package race suites, vet and formatting (`testfix.uDvfgu`). These
were test-only defects (reproduced 3/1,000 under `-race`), not production parser bugs,
and were closed with the reviewed component corrections. Corrected hashes:
- explorer_test.go: `2eb0bf6de162d6a691908c64fb4e536e1b8354bcde73247805772d1aecef1841`
- privategit_linux_test.go: `bb066d94268c776417c4df3618c3e04568ec0031552076ce2541ee04e4c3afc8`

## Next actions and evidence standard

1. Coordinator freezes the completed combined explicit overlays; no unfinished/dirty
   user source in evidence. Central reviewer runs the required all-change/thesis gate.
2. After findings are owned and corrected, repeat combined review and independent
   full WSL race checks in private ext4/0700 TMPDIR, Windows build/internal/e2e,
   adapter npm check if touched, formatting/diff checks and unchanged module checks.
3. Keep harness-scope failures (e.g. omitted docs ledger), real fixture failures and
   source hashes/exits; agent/browser passes do not replace independent acceptance.
4. Only after acceptance may coordinator commit explicit paths with tested-byte
   comparison and research/start the next safe gap; blocked prerequisites stay blocked.

## Historical evidence index (not current-source admission)

- Accepted publication: `a977766722dcfaa12060385b95ef028877d04eb6`;
  `docs/publication-runtime-integration.md`, `docs/publication-contract.md`.
- Accepted SDK/browser fixture: `9144d59e0875bc6679c79fd57044b05660747b8b`;
  `docs/pi-session-control.md`, `docs/evidence/pi-sdk-fixture-2026-10-06.json`.
- Accepted cleanup: `7a80f5afb644285767b3ff656b3ae9ea0a40d89b`;
  `docs/economy-cleanup-2026-10-06.md`. Explorer history: `docs/test-run-explorer.md`.
- Older core/slice commits and limits: `docs/thesis-gap-ledger-2026-10-05.md`,
  `docs/implementation-status.md`, `docs/provider-durable-evidence.md`,
  `docs/experiment-manifest.md`; detailed implementation history remains in Git.
- Guest/containment/operator history: `docs/verifier-blockers-2026-10-05.md`,
  `docs/containment-blockers-2026-10-05.md`, `docs/guest-profile-prerequisites-2026-10-06.md`.
  External recorded setup statements are not revalidated host facts; see evaluation unlock doc.
- Original 1,326-line UNCOMMITTED handoff preserved byte-for-byte outside the repo:
  `C:/Users/Admin/AppData/Local/Temp/opencode/task-state-before-debloat-6df1b277-01ce-4040-bd18-38c1ee2a6973.md`
  SHA-256 `7856cbfe20e01ea2c30c7125af963836a9e8a065ad79614ba6715d9c2eb69fec`.
  No historical operational commands are renewed authorizations. Cleanup is uncommitted
  and itself awaits the central gate; no code, infra, staging or commits in this cleanup.
