# tbound product plan: sessions, tool registry, profile system

Written 2026-10-16 against `codex/tbound-prototype` (HEAD `a298f71`), the web-chat
branch `codex/tbound-web-chat` (HEAD `cd8c7fd`) and its
`docs/archive/modular-architecture-vision.md`.

> ### Scope and claim status
> **This whole document is a PROPOSAL for post-thesis product work.** It changes no
> code, no policy, and no thesis document. It does not alter `TASK_STATE.md` or the
> claim boundary: G1=false, containment not-established, H1 unexecuted, no qualified
> production launch. Nothing here is evidence-bearing. A refactor that stays green
> verifies code, not a claim (`AGENTS.md`).
>
> The thesis candidate stays frozen on one profile (Pi CLI 0.87.1, `read`/`write`/
> `edit`/`bash`, one task per request). Every workstream below must keep that profile
> **byte-identical** (see §2 and the golden gate in §3.4). Product work is added
> beside it, never in place of it.

Labels: **EXISTS** (verified in the tree, file cited), **PROPOSED** (design only),
**OPEN** (an owner decision is needed), **VERIFY** (a Pi behavior taken from upstream
docs on `main`, not yet checked against the pinned 0.87.1), **DECIDED** (the owner
chose it; recorded below), **RECOMMENDED** (my recommendation, not yet confirmed).

### Decision record

Updated 2026-10-17. These are the owner's choices so far. Everything else in this
document is PROPOSED or OPEN until it appears here.

| ID | Decision | Date | Effect on this plan |
|---|---|---|---|
| DEC-1 | Product work goes on a new integration branch **`codex/tbound-product`**, cut from `codex/tbound-prototype`. `codex/tbound-web-chat` stays an extension | 2026-10-17 | §1 |
| DEC-2 | The **Go supervisor owns session truth** (D-C1 option a). Pi's own session file is never authoritative | 2026-10-17 | §5.4 D-C1; the Pi flag set in §2 X8 |
| DEC-3 | **Changes publish to the workspace per turn** (D-C6) | 2026-10-17 | §5.4 D-C6 and its follow-ups D-C6a…D-C6c |

### How to resolve the remaining choices: ask the user

Every RECOMMENDED or OPEN item in §2 and §5.4 is a choice the owner has not made yet.
**When pursuing this plan, do not pick one silently.** Before starting the step that
depends on a choice, ask the user with the `AskUserQuestion` tool (the "ask question"
tool):

1. One question per decision, using the item's ID (for example "X5" or "D-C2") as the
   header.
2. List my recommendation as the first option, labelled "(Recommended)", then the real
   alternatives. Put the trade-off in each option's description.
3. Ask at the point of use, not all at once: the "Ask when" column in §2 and the "Ask
   before" column in §5.4 say when.
4. Record the answer in the decision record above (new `DEC-n` row) and, once C0 starts,
   in the dated ADR under `docs/decisions/`.
5. If the user's answer contradicts a RECOMMENDED item, update the dependent sections
   in the same change instead of leaving them stale.

## 0. Summary

Three product gaps, one dependency order.

| # | Gap (your wording) | What is true today | Plan | Risk to thesis |
|---|---|---|---|---|
| WS-A | Tool set is hardcoded; make it modular (DRY, SRP) | The 4-tool list is repeated as literals in 6 Go files, 4 TS files, `profile.json` and a smoke script, and per-tool behavior is spread over `switch` statements in ≥ 8 more Go files | One immutable tool registry, built from the signed profile; per-tool behavior behind a `ToolSpec`; one manifest source generates Go and TS; a "how to add a tool" conformance kit | Low. Behavior-preserving. Golden test pins the old digests |
| WS-B | Profile registry, multiple Pi versions | `HostProfile` is runtime-only. Pi version, tool manifest, argv, model and provider are Go/TS literals. `docs/tbound-future-work-multi-profile.md` describes a registry in prose only | A signed `HarnessProfile` (Pi version + extension + tools + argv + provider + session mode + runtime profile ref + conformance receipt) and a fail-closed profile registry | Medium. Touches admission. The thesis profile becomes one registry entry |
| WS-C | Vanilla-Pi session experience (multi-turn, history, resume, queue, cancel, …) | One prompt, one terminal receipt, one request frame per connection, Pi runs with `--no-session` | A new interactive session profile with a supervisor-owned session store, `tbound-service/v2`, and a vanilla-parity matrix with an explicit treatment for each feature | High. Changes the lifecycle and recovery model. Must be a separate, non-claim-bearing profile |

Order: **WS-A → WS-B → WS-C** (A is mechanical and unblocks B; B gives C a place to
declare "interactive"; C is the largest). WS-C's *design decisions* (§5.4) can start in
parallel with A.

## 1. Branch: `codex/tbound-product` (DEC-1, decided)

The first idea was to reuse `codex/tbound-web-chat`. The decision is a new integration
branch instead, for the reasons below.

| Reason | Evidence |
|---|---|
| web-chat is a **Tier 2 extension** by design: the core never imports it | `extensions/webchat/`, `scripts/check-extension-boundary.sh`, vision §6.3 |
| This plan edits the **core** (Tier 0/1): `gate`, `broker`, `protocol`, `piruntime`, `sessionrepo`, the service handoff. web-chat touches none of those: measured 2026-10-17, its 496 changed files since the merge base are all under `extensions/webchat/` plus docs, `.github`, `Makefile`, `README.md`, `TASK_STATE.md` and one script | `git diff --name-only` |
| Core product changes must be holdable back from the thesis candidate (§6.1) without blocking UI work | §6.1 |
| PR #11 is **already merged** (squash, `bc1420f`, 2026-10-15) into `origin/codex/tbound-prototype`, so web-chat is a finished topic branch, not a trunk | `gh pr view 11` |
| AGENTS.md: one thread per checkout, no shared checkouts | `AGENTS.md`, `frontend-product-agent.md` |

Correction to an earlier draft of this table: it said mixing the branches would force
the boundary guard to allow core edits on an "extension" branch. That is wrong. The
guard checks the tree (imports and `go.mod`), not the branch name, so the guard is not a
reason to keep them apart.

Proposed layout (each in its own worktree, per AGENTS):

```text
codex/tbound-prototype            thesis candidate. Frozen for claims. Receives only reviewed merges.
  └─ codex/tbound-product         product trunk (this plan). Cut from tbound-prototype, then web-chat merged in (X11).
       ├─ codex/tbound-tool-registry   WS-A
       ├─ codex/tbound-profiles        WS-B
       ├─ codex/tbound-sessions        WS-C
       └─ codex/tbound-web-chat        UI topic branch. Targets codex/tbound-product. Consumes WS-C's API only.
```

web-chat is not duplicated. Its vision doc already proposes P1–P4 (persistent sessions,
snapshot/events, submit, cancel) as an API for the core thread. **WS-C is the core-side
implementation of those proposals, widened to full Pi parity.**

### 1.1 Should `codex/tbound-product` be combined with `codex/tbound-web-chat`? (X11, REC)

Both are "tbound as a product", so combining is reasonable at the **integration** level.
It is not reasonable as a **working branch**. Measured on 2026-10-17 (dry-run
`git merge-tree`, nothing modified):

| Fact | Value |
|---|---|
| web-chat vs local prototype | web-chat 50 commits ahead, local prototype 141 ahead |
| Files web-chat changed outside `extensions/webchat/` and docs | 0 in `supervisor/` and `adapter/` |
| Merge web-chat into local prototype | **1 conflict** (`TASK_STATE.md`) |
| Merge `origin/codex/tbound-prototype` (the squash) into local prototype | **1 conflict** (`TASK_STATE.md`) |
| Merge web-chat into `origin/codex/tbound-prototype` | **101 conflicts**, nearly all add/add under `extensions/webchat/` (docs, `frontend/src`, `dist`). Cause: PR #11 was squashed, so the same files exist on both sides with different history |
| Local prototype vs origin | 141 commits ahead, 1 behind. The 141 are not on the remote |

| Option | What it means | Verdict |
|---|---|---|
| 1. Merge product into web-chat; one branch | `codex/tbound-web-chat` becomes the trunk | **No.** Its PR is closed and squashed; the 101-conflict history problem sits at its root; UI churn (≈ 84k added lines in 488 files) would bury core diffs in review; core changes could not be held back from prototype without also holding the UI |
| 2. Fully separate; merge only at release | product and web-chat never meet until the end | **No.** WS-C has no live client until late; the API and `contract.go` drift; a big-bang merge at the end |
| 3. **Separate working branches, one trunk** | `codex/tbound-product` is the trunk. web-chat merges into it once now, then its PRs target product. Core workstreams also target product | **Recommended.** Gets the "single product line" benefit without either problem |

Why option 3 costs little: the file sets are disjoint (core and adapter on one side,
`extensions/webchat/` on the other), so ongoing merges between them conflict almost never
(only `TASK_STATE.md`, `docs/README.md`, `Makefile`, `.github`).

Steps (all non-destructive; ask the user first, see X11 in §2):

1. Commit or stash the 32 uncommitted files in the prototype worktree. A branch cut now
   would not contain them.
2. Cut `codex/tbound-product` from local `codex/tbound-prototype` in its own worktree.
3. In `codex/tbound-product`, merge `codex/tbound-web-chat` (expect 1 conflict:
   `TASK_STATE.md`; resolve by keeping both sections).
4. Reconcile the squash commit. `origin/codex/tbound-prototype` is 1 commit ahead
   (`bc1420f`, the squash of an **older** web-chat state). Do not merge it with the default
   strategy: it re-creates the 101 add/add conflicts. After checking that web-chat
   contains everything in `bc1420f` (`git diff bc1420f codex/tbound-web-chat` shows only
   later work), record it as merged with `git merge -s ours origin/codex/tbound-prototype`.
5. Retarget web-chat PRs to `codex/tbound-product`.
6. Push. The 141 local-only prototype commits exist nowhere else; back them up before any
   history work.

## 2. Contradictions with existing documents: recommendations

This plan overrides several statements that were right for the thesis MVP. Each needs an
explicit amendment, not a silent change. The common rule behind every recommendation:

> **Scope the old rule to the thesis-registered profile; do not delete it.** The thesis
> guardrails stay true and tested for `pi-0.87.1-core4`. Product profiles get their own
> rules, are `non-claim-bearing` by construction, and are admitted only through a signed
> `HarnessProfile` (§4). Thesis documents in `MSE_MA_Thesis` are not edited by this plan.

Status in the table: **REC** = recommended, not yet confirmed by the user. Ask with
`AskUserQuestion` at the "Ask when" point (see "How to resolve the remaining choices").

| ID | Existing statement | Where | Conflicts with | Recommendation | Alternatives | Ask when |
|---|---|---|---|---|---|---|
| X1 | "Keep the four-tool manifest fixed unless the protocol and conformance plan are explicitly amended before exposure" | `docs/tbound-future-work-multi-profile.md` ("Thesis guardrails") | WS-A, WS-B | **REC:** add a "Product profiles" section to that file. Keep the guardrail text verbatim but scope it to `claim_class: thesis-registered`. Say that any other manifest needs a signed `HarnessProfile` plus a passing ToolSpec conformance kit (§3.5). This document is the "amendment plan" the sentence asks for | (a) Leave the file untouched and let this document override it. Risk: two documents disagree. (b) Rewrite the guardrail globally. Risk: weakens the thesis story | Before A1 (first code change), as part of the P0 ADR |
| X2 | "Plugins that add tool-execution routes or grow the Pi tool surface beyond the four tools: OUT" | web-chat `docs/archive/modular-architecture-vision.md` §3.5 | WS-A | **REC:** split the row. Keep "Tier 2 clients/plugins can never add tools" as OUT. Add "Tier 1 tool surface is set only by a signed `HarnessProfile` using compiled-in `ToolSpec`s" | Delete the row. Risk: loses the plugin boundary | Before A1 |
| X3 | "Tool routes or tool surface: closed set" | same, §3.4 | WS-A | **REC:** same edit as X2: "closed set per profile; `core4` is the thesis set" | Same | Before A1 |
| X4 | Which branch edits the vision doc | the doc lives on `codex/tbound-web-chat` | X2, X3, X5 | **REC:** make the edits on `codex/tbound-product` in a copy of the doc (it arrives with the next web-chat merge, as `83f07dd` did) and notify the web-chat thread. Do not edit the web-chat checkout from the product thread | Edit on web-chat directly (violates "one thread per checkout") | When the first vision-doc edit is needed |
| X5 | "Replacing the Pi runtime or adding a second runtime" is a non-goal | vision §9 | WS-B | **REC:** keep it. Clarify that "runtime" means the agent harness. Multiple *Pi versions* are in scope; other harnesses (Claude Code, Codex, …) stay out of scope until a separate decision | Allow other harnesses later. Needs its own adapter, correlation path and conformance suite, which is the largest rewrite in this document | Only if the user raises other harnesses |
| X6 | `serve --headless` / daemon mode is FUTURE-WORK because it needs a surviving-owner design; "direct supervisor and service-manager deployment modes are outside the thesis" | vision §5, Q1; thesis `02-1` §4.1 | WS-C (D-C5) | **REC:** the interactive profile is where a long-lived session service is allowed, and it is `non-claim-bearing`. Keep the existing systemd **socket-activated** service as the first deployment. Add a surviving-owner mechanism before the service ships: a small separate owner that holds the cgroup and, on supervisor death, stops admission, runs `cgroup.kill`, waits for `populated=0`, and reconciles identity-matched resources only (`UNKNOWN` if cleanup fails). Do not claim the thesis result for this mode | (a) Systemd `KillMode=control-group` as owner. Simpler, but the thesis calls that a service-manager mode. (b) Interactive profile only runs under a user-attached CLI owner, with no daemon. Safer, but "resume tomorrow" then needs the user to start the owner | Before C1 (it blocks any long-lived service; see D-C5) |
| X7 | One task at a time; a second request gets `busy` | `pi_service_handoff_linux.go` (`servePiServiceRequests`) | WS-C | **REC:** `v1` and `single-task` profiles keep `busy` byte-for-byte. `v2` allows many sessions with at most one active turn each, plus an operator-set **host-wide cap on active turns, default 1** for the first release (publication windows, cgroup quotas, and API cost are easier to reason about). Raise the cap after measurement | Cap = number of CPUs, or no cap. Risk: untested contention in publication and the cell runner | Before C4 |
| X8 | "48 recovery trials (16 scenarios × 3)" assume one prompt per workflow | `docs/decisions/2026-10-12-cli-reproduction.md` | WS-C (D-C7) | **REC:** write a separate interactive-recovery specification (own scenario list, own count). Never fold it into the 48. Reuse the fresh-process harness. Until its scenarios pass, label the interactive profile "preview" in docs and the CLI | Reuse the thesis scenarios unchanged. Risk: they do not cover multi-turn state (mid-turn crash with a queued steer, crash between publication and the next turn) | Before C2 |
| X9 | Pi flags `--no-session --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files` | `decisions/2026-10-12-cli-reproduction.md` | WS-C | **REC (follows DEC-2):** the interactive profile keeps **the same flag set**, including `--no-session`. The supervisor owns the session, so Pi's own session, discovery, and extension loading stay off. Vanilla features come back through the supervisor (§5.3), one row at a time. The profiles then differ in `session.mode` and the service protocol, not in the Pi sandbox | Turn on Pi's own sessions. Contradicts DEC-2 | When enabling skills, templates, or extensions (parity rows marked Deferred) |
| X10 | Publication is described as one validated transaction at the end of a workflow (baseline-to-final composed delta, terminal lease origin binding, quiescent apply window) | `docs/publication-contract.md` | DEC-3 (per-turn publication) | **REC:** make each turn its own workflow so each turn ends in one publication transaction under the existing contract (D-C7). Keep the contract's rules unchanged: host-only, complete baseline validation, single-use commit token, recoverable per-object state | Publish a session at once on close. Contradicts DEC-3 | Before C2 (the session store design depends on it) |
| X11 | Combine `codex/tbound-product` with `codex/tbound-web-chat`? | §1.1 | DEC-1 | **REC:** option 3 in §1.1. Separate working branches, one trunk: merge web-chat into product once, retarget its PRs to product, record the PR #11 squash as merged with `-s ours` after verifying | Option 1 (one branch, web-chat as trunk) or option 2 (merge only at release); both rejected in §1.1 with reasons | Before cutting `codex/tbound-product` (P0) |

Items X1–X5 and X11 are documentation or branch-flow amendments. X6–X10 shape the code and
should be settled before the step in the "Ask when" column starts.

## 3. WS-A: tool registry (DRY / SRP)

### 3.1 Findings

Two different kinds of duplication are mixed together. They need different fixes.

**(a) Manifest membership: "which tools exist".** This is the same fact copied around.
Counts come from a grep of non-test, non-doc source; they are a floor, not a full audit.

| Where | What it does with the list |
|---|---|
| `supervisor/internal/broker/broker.go:37,152` | `declaredToolManifest`; `sameManifest` rejects anything but exactly `read, write, edit, bash` in order |
| `supervisor/internal/broker/correlation/correlation.go:109` | `fixedTools` registered set |
| `supervisor/internal/broker/protocol/protocol.go:583` | `registeredTool` switch |
| `supervisor/internal/broker/protocol/arguments.go:80-135` | `validateArgumentSchema` switch (membership **and** per-tool argument shape) |
| `supervisor/internal/gate/gate.go:158,221` | an inline four-way `!=` chain **and** a second `registeredTool` switch in the same file |
| `supervisor/internal/piruntime/worker_channels_linux.go:370-374,414` | tool schema digests and `Tools` for the worker bootstrap, plus `"0.87.1"` literals at 413, 1884, 1895 |
| `adapter/src/ipc-transport.ts:14`, `proxy-tools.ts:5`, `broker-provider.ts:34`, `tbound-cli-extension.ts:47` | four independent TS copies |
| `adapter/profile.json:5-11`, `supervisor/ipc-smoke.sh:105` | JSON/shell copies of names and digests |

**(b) Per-tool behavior: "what each tool does".** This is legitimately per-tool, but it
lives in scattered `switch` statements rather than behind one unit per tool:

| Concern | Switches on tool name in |
|---|---|
| Argument validation | `protocol/arguments.go` |
| TS argument decode/encode | `adapter/src/ipc-transport.ts:180-210` |
| Effect execution | `cmd/tbound/durable_executor_linux.go:182-218`, `governed_session_linux.go:637-651,1060-1092`, `native_fixture_linux.go:267` |
| Delta accounting | `internal/delta/delta.go:599` (bash special case) |
| Evidence and replay | `governed_replay_linux.go` (≥ 4 sites), `governed_provider_evidence_linux.go:80,159`, `governed_history_recovery_linux.go:56,146`, `production_recovery_command_linux.go`, `governed_workflow_live_linux.go:974` |

Why this is not just "an intentional fail-closed choice": fail-closed is the *property*
to keep (unknown tool → refused). Repeating the list is only one way to get it, and a
fragile one: a fifth copy that is missed fails closed *and silently*, as a confusing
`unknown_tool`, and a copy that is edited alone can desynchronize.

Two other facts shape the design:

- `HostProfile` (`piruntime/host.go:91`) has no tool-manifest field and no Pi-version
  field. Both are Go literals. The profile cannot say what tools it admits.
- `sessionrepo.Store` exposes `Read`, `Write`, `Edit`, `Delete`, `RunBash` and a linear
  baseline → tip chain. There is no `List`, `Find`, or `Grep`, so adding `ls`/`find`/`grep`
  needs a new read primitive, not just a new name.

### 3.2 Design (PROPOSED)

New package `supervisor/internal/toolspec`. **One tool = one `ToolSpec`. One run = one
immutable `Registry`.**

```go
// Sketch, not final.
type Class int // ClassRead (no state change), ClassMutate (new generation), ClassExec (command cell)

type ToolSpec interface {
    Name() string
    Class() Class
    // Argument contract: strict decode + RFC 8785 canonical form. Replaces validateArgumentSchema.
    DecodeArguments(raw []byte) (Arguments, error)
    // Pinned per Pi version: SHA-256 of the sorted-key TypeBox schema (what adapter/profile.json holds today).
    SchemaDigest(piVersion string) (string, bool)
    // Trusted effect: runs only through the supervisor's executor path.
    Execute(ctx context.Context, env ExecEnv, call Call) (Result, error)
    // What the delta/evidence layers need to know, instead of `if tool == "bash"`.
    Accounting() Accounting
}

type Registry struct{ /* ordered, immutable after New */ }
func New(specs []ToolSpec, manifest Manifest) (*Registry, error) // manifest comes from the signed profile
func (r *Registry) Has(name string) bool
func (r *Registry) Spec(name string) (ToolSpec, bool)
func (r *Registry) ManifestDigest() string
```

Rules that preserve the fail-closed properties:

1. **The registry is built once, from the signed profile's tool manifest.** Nothing
   reads tool names from Pi IPC, from config files at runtime, or from the environment.
   Pi cannot register a tool.
2. **Manifest order and the manifest digest stay part of the profile digest.** Reordering
   or adding a tool changes the digest and requires a new signature.
3. **A manifest entry with no compiled `ToolSpec`, or a `ToolSpec` with no manifest
   entry, is a startup error.** Not a runtime surprise.
4. **Default policy remains deny.** A new tool has no allow rule until a policy bundle
   names it (`gate.Evaluate` already returns `policy_default_deny`).
5. **No dynamic loading.** `ToolSpec` implementations are compiled in (vision §3.5, "no
   `plugin.Open`"). "Modular" here means one place per fact, not runtime plugins.

Consumers change from literals to the registry:

| Consumer | Change |
|---|---|
| `broker.go` | `sameManifest(profile.ToolManifest)` compares to `registry.Manifest()`; drop the literal |
| `correlation.New` | takes `*Registry`; drop `fixedTools` |
| `protocol.registeredTool`, `ValidateArgumentSchema` | delegate to `registry.Spec(name).DecodeArguments` |
| `gate.Evaluate` | one `registry.Has`; delete the inline chain and the duplicate `registeredTool` |
| `piruntime` bootstrap | `Tools` and `ToolSchemaSHA256` come from the profile and registry; remove `piWorkerToolSchemaDigests()` and the literal list |
| `durable_executor`, `governed_session` | replace `switch proposal.Tool` with `registry.Spec(tool).Execute` on three built-in specs |
| `delta` | consume `Accounting()` instead of testing for `"bash"` |
| Evidence/replay | key off `Class()` and `Accounting()` |

**Single source for TypeScript.** Add `tools/manifest.json` (names, order, per-Pi-version
schema digests) as the one source. A script generates `adapter/src/tools.generated.ts`
and the Go table; `scripts/check-tool-manifest.sh` fails CI if any checked-in copy
differs, in the same style as `check-extension-boundary.sh` (including a `--self-test`
that proves each rule fails on a violating copy). The adapter's per-tool decode code
stays hand-written but is keyed by the generated names.

### 3.3 `ls` / `find` / `grep` as the first new tools

These are the proof that the registry works, and they are classed `ClassRead`.

- Implement them in **Go over a sealed generation**, not by executing Pi's code or an
  external binary in the cell. The thesis doc already records why: upstream
  `earendil-works/pi#6299` says unwrapped `ls`/`grep`/`find` can bypass a workspace
  wrapper and `grep` may need its execution replaced.
- This needs new read primitives on `sessionrepo.Generation` (`List`, a bounded `Walk`)
  with the same journal-before-release rule that `Generation.Read` already follows
  (`sessionrepo` `Read`: "journals the correlated intent before releasing bytes").
- Output must match Pi's observable behavior and schemas. VERIFY: the 0.87.1 schemas
  and the exact `grep`/`find`/`ls` output format and limits, pinned by digest like the
  existing four.
- Policy: read-class rules can allow paths the same way `read` does. A denied-path test
  must go through the new tools too (the repo already notes a read-exposure test exists
  only "through the declared read tool", `dev-todo.md`).

### 3.4 Steps, tests, and acceptance

| Step | Change | Done when |
|---|---|---|
| A0 | **Golden gate first.** Test that records today's manifest digest, per-tool schema digests, policy digest, broker-profile digest, and the bootstrap JSON for the `core4` profile | Gate passes on `a298f71` before any refactor |
| A1 | Add `toolspec` with `read`, `write`, `edit`, `bash` specs wrapping existing code unchanged | Golden gate still passes; no behavior diff |
| A2 | Move argument validation to specs (`protocol/arguments.go`) | Existing protocol tests green, no skips |
| A3 | Switch `broker`, `correlation`, `gate`, `protocol` to the registry. Delete the duplicates | `grep` finds no `"read", "write", "edit", "bash"` literal outside `tools/manifest.json` and tests |
| A4 | Replace executor/session `switch` statements with spec dispatch | Fresh-process recovery tests unchanged and green |
| A5 | `tools/manifest.json`, generator, `check-tool-manifest.sh`, TS migration | CI guard has a `--self-test` |
| A6 | ToolSpec conformance kit (§3.5) | Kit passes for the 4 existing tools |
| A7 | `ls`, `find`, `grep` specs and read primitives, as a **separate** `core4+explore` profile | Kit and closure tests pass for the new profile; `core4` golden gate still passes |

Verification follows `AGENTS.md`: Linux/WSL with Go 1.27.1, from `supervisor/`:
`go build ./...`, `go vet -mod=readonly ./...`, `gofmt -l .`,
`go test -race -mod=readonly -count=1 ./...`. Keep a receipt (command, exit code,
source hash). A test that skipped is not verified. Recovery behavior is asserted from a
fresh process, never from in-memory state.

### 3.5 "Add a tool" conformance kit (PROPOSED)

A table-driven suite every `ToolSpec` must pass, so adding a tool is a checklist and not
a code hunt:

1. Name and class declared; present in `tools/manifest.json`.
2. Schema digest pinned for each supported Pi version; mismatch fails admission.
3. Strict argument decode: unknown fields, duplicates, oversize, bad types are refused.
4. Canonical form is stable (RFC 8785) and bound to the correlation digest.
5. Default-deny: with no policy rule the verdict is `DENY`/`policy_default_deny`.
6. Executes only through the supervisor executor; the adapter has no direct effect.
7. Intent is journaled before any bytes are released (read) or before the new
   generation is committed (mutate/exec).
8. Closure: Pi exposes exactly the manifest (reuse the existing enumeration and escape
   attempts).
9. Crash/recovery in a fresh process leaves a defined state (`UNKNOWN` when uncertain).

## 4. WS-B: profile system and multiple Pi versions

### 4.1 Findings

- `HostProfile` already carries a lot (executable digest, containment, settlement,
  descriptors, runtime bundle, provider profile digest). It is the **runtime** profile.
- Missing from any signed profile: Pi version, extension bundle digest, tool manifest,
  RPC argv template, model, session mode, claim class. These are literals:
  `piinstall.PinnedPiVersion = "0.87.1"` (`piinstall.go:21`), `worker_channels_linux.go`
  (`PiVersion: "0.87.1"`, model constants, tool literals), `host.go:51-54` profile
  strings with `0.87.1` in them, the argv in `piinstall.GovernedRPCArgs`,
  `tbound-cli-extension.ts` (`pi_version: "0.87.1"` as a literal TS type), the doctor's
  remediation text, `adapter/package.json`, and `adapter/profile.json`.
- The current Pi CLI route is deliberate: official CLI in RPC mode plus one extension,
  not an embedded SDK session (`decisions/2026-10-12-cli-reproduction.md`). Different Pi
  versions can change the extension API, RPC records, and tool schemas, so the adapter
  itself is version-sensitive.
- `tbound-profile` already has `sign`, `verify`, `digest`, `xattr-*`, `landlock-digest`,
  and `runtime-bundle-digest` subcommands, which is the right base for signing.

### 4.2 Design (PROPOSED)

Introduce **`HarnessProfile`**, a signed document that composes, never replaces, the
existing runtime profile:

```json
{
  "schema": "tbound-harness-profile/v1",
  "id": "pi-0.87.1-core4",
  "claim_class": "thesis-registered",
  "pi": { "package": "@earendil-works/pi-coding-agent", "version": "0.87.1",
          "executable_sha256": "…", "node_version": "24.21.0" },
  "extension": { "bundle_sha256": "…", "argv_template_digest": "…" },
  "tools": { "manifest": ["read","write","edit","bash"],
             "schema_sha256": { "read": "…", "write": "…", "edit": "…", "bash": "…" } },
  "provider": { "profile_ref": "…", "model": "…" },
  "session": { "mode": "single-task" },
  "policy_ref": "tbound-policy/v1:…",
  "runtime_profile_ref": { "host_profile_digest": "…" },
  "conformance": { "receipt_sha256": "…", "suite": "tbound-conformance/v1" }
}
```

Rules:

1. **Signed, content-addressed, fail closed.** Same signing path as `tbound-profile
   sign`. An unknown profile ID, a changed byte of Pi/extension/argv/tools, or a missing
   conformance receipt refuses launch. Nothing inherits a claim from another profile
   (future-work doc: "An unregistered executable or modified extension cannot inherit a
   claim from the candidate profile").
2. **Registry = reviewed list of signed profiles.** `profiles.d/*.json` plus a signed
   `registry.lock` that names the admitted IDs and digests. The service config selects
   the default; a session request may choose another admitted ID. No discovery from
   `PATH`, home directories, or a package registry (vision §3.5).
3. **`claim_class` is structural.** Only the thesis profile can be `thesis-registered`.
   Product profiles are `non-claim-bearing` by construction, and a diagnostic launch of
   an unregistered Pi configuration stays structurally separate, with no access to
   production authority or credentials (future-work doc, "Do not create a permissive mode
   that inherits the governed label").
4. **Literals become profile reads.** Delete every `"0.87.1"` literal outside the
   profile and its fixtures. Constants such as `PiCLIProductionArgumentsProfile` become
   functions of the profile.
5. **Adapter per Pi version.** `adapter/profiles/<id>/` holds the lockfile and the
   version-specific glue; shared code takes the version from the bootstrap. The
   hardcoded `pi_version: "0.87.1"` TS literal type becomes a checked string.
6. **Conformance produces a receipt that the profile signs over.** Add
   `tbound-profile conformance --profile ID`: tool-surface closure, provider correlation,
   schema digests, Linux containment, process cleanup, and publication recovery tests
   (the list in the future-work doc), emitting a receipt digest bound into the profile.

### 4.3 Adding a new Pi version (the workflow this enables)

```text
1. cp profiles.d/pi-0.87.1-core4.json → pi-0.88.0-core4.json; change pi.version
2. tbound-profile tool-digests --pi /path/to/pi   # recompute TypeBox schema digests
3. build the adapter for 0.88.0; fix extension-API drift
4. tbound-profile conformance --profile pi-0.88.0-core4   # closure, correlation, containment…
5. tbound-profile sign …; add to registry.lock; review the diff of profile vs. the parent
```

A `tbound-profile diff A B` command makes step 5 reviewable.

### 4.4 Steps and acceptance

| Step | Change | Done when |
|---|---|---|
| B0 | Define `HarnessProfile` schema and validator; add `pi-0.87.1-core4` generated **from current literals** | Round-trip test: generated profile reproduces today's digests (reuse the A0 golden gate) |
| B1 | Registry loader with fail-closed tests (unknown ID, edited byte, missing receipt, duplicate ID) | Negative-path tests green, no skips |
| B2 | Replace literals with profile reads (Go, TS, shell, doctor text) | `grep '0\.87\.1'` finds only the profile, fixtures, docs, and package pins |
| B3 | `tbound-profile tool-digests`, `diff`, `conformance` | Conformance receipt reproducible in a fresh process |
| B4 | Doctor reports the admitted registry and which profile each installed Pi matches | `tbound-doctor --json` lists profiles |
| B5 | Second real profile: a newer Pi version, or `core4+explore` from A7 | Passes its own conformance. The `core4` golden gate is unchanged |

OPEN: Pi package dependency closure. The decision doc says a version check or a binary
hash is not a dependency closure; `PiRuntimeBundleDigest` exists for this. Each Pi
version needs its own measured runtime bundle. This is the most expensive part of B5.

## 5. WS-C: vanilla-Pi session experience

### 5.1 Findings (what the thesis build does today)

- **Service protocol.** `servePiServiceRequests` accepts "exactly one request frame per
  connection" (`pi_service_handoff_linux.go:1154`); a second concurrent request gets
  `busy`. No browser/remote auth, no history, no cancel, no streaming (vision §5.4,
  `frontend-product-agent.md`).
- **Worker.** `PiCLIWorker.sendPromptControl` refuses when "already has an active
  prompt" (`worker_channels_linux.go:1560`). The lease is per worker.
- **RPC client.** `PiRPCClient` can send only `prompt` and `abort`
  (`pi_rpc.go:54,65`), and prompts are capped at 16 KiB (`piRPCMaxPromptBytes`).
  Pi events other than lifecycle are mostly discarded.
- **Conversation is Go-owned.** `broker-provider.ts` "ignores Pi's transcript context";
  prompts are admitted by trusted session control and `AdmitPrompt` goes through the Go
  conversation (`architecture.md`; `sendPromptControl`). Pi's own session/transcript is
  therefore **not** the source of truth.
- **Pi runs with `--no-session`**, plus no extensions, skills, templates, themes, or
  context files (`decisions/2026-10-12-cli-reproduction.md`).
- **Workspace state is a linear generation chain** (`sessionrepo.Store`: baseline → tip;
  no parent/branch API found). Writes and edits create generations; Bash runs against a
  disposable view; the live workspace changes only through publication.

### 5.2 What "vanilla Pi" means (VERIFY against 0.87.1)

From upstream Pi docs on `main` (fetched 2026-10-16: `docs/cli.md`, `docs/usage.md`,
`docs/rpc.md`, `docs/rpc-commands.md`). The pinned version may differ; re-check each
row against 0.87.1 before implementing.

- Sessions save automatically; `--continue`, `--resume`, `--session`, `--session-dir`;
  `/new`, `/resume`, `/name`, `/session`, `/tree`, `/fork`, `/clone`, `/compact`,
  `/export`, `/share`, `/model`, `/thinking`, `/login`, `/logout`, `/settings`.
- Message queue: Enter = steer (after the current turn's tool calls), Alt+Enter =
  follow-up (after the task finishes), Esc aborts and returns queued messages.
- `!cmd` runs a command and sends the output to the model; `!!cmd` does not.
- Default tools `read`, `bash`, `edit`, `write`; optional `grep`, `find`, `ls`.
- Extensions, skills, prompt templates, themes, `AGENTS.md`/`CLAUDE.md` context files.
- RPC commands include `prompt`, `steer`, `follow_up`, `abort`, `new_session`,
  `switch_session`, `fork`, `clone`, `get_state`, `get_messages`, `get_entries`,
  `get_tree`, `compact`, `set_model`, `bash`, `export_html`, `get_commands`.

**Important governance finding.** Two native RPC commands are side-effecting *outside*
the model tool path: `bash` ("runs a shell command and adds its output to the session")
and `export_html` (writes a file). `fork`/`clone`/`new_session` create session files.
Because the supervisor owns the RPC pipe (the Go side writes every command), tbound can
enforce an **RPC command allowlist**: anything not in the table below is never sent.
That is how `!cmd` parity can exist without an ungoverned shell.

### 5.3 Parity matrix (PROPOSED treatment per feature)

Treatment: **Native** (works as in Pi), **Mediated** (works, routed through the
supervisor), **Reframed** (same user outcome, different mechanism), **Denied** (refused
with a stated reason), **Deferred** (not in the first interactive release).

| Vanilla feature | Treatment | How in tbound | Notes |
|---|---|---|---|
| Multi-turn conversation in one session | **Native** | Session outlives a turn; each turn is admitted by the supervisor | The core ask |
| Streaming assistant text and tool progress | **Mediated** | Bounded events in the observation projection (`tbound-webchat-observation/v1`) | UI and CLI both consume it |
| Abort / cancel (Esc) | **Mediated** | `turn.cancel` request; the settled result is read from the durable record; `UNKNOWN` when uncertain | Vision §10 Q5 rules apply |
| Steer / follow-up queue | **Mediated** | Queue lives in the supervisor; each queued item is admitted like a prompt (`AdmitPrompt`) before delivery | Use Pi's `steer`/`follow_up` RPC only for admitted items |
| Session persistence, list, `--continue`, `--resume`, `--session`, `/name`, `/session` | **Reframed** | Supervisor-owned session store keyed by workspace; opaque `session_id` bound to the peer UID (vision Q3/Q4) | Pi's own session file is not the source of truth |
| History view (`get_messages`, `get_entries`) | **Reframed** | Served from the supervisor's durable log, not from Pi | Matches the Go-owned conversation |
| `/tree`, `/fork`, `/clone` (branch from an earlier message) | **Reframed** | Conversation branch **plus** a pointer to the workspace generation at that message. Needs a branching generation store | See OPEN D-C3. Vanilla Pi does not rewind files; tbound can. Make that optional |
| `/compact` and auto-compaction | **Mediated** | A summarization call through the Go broker, recorded as its own registered exchange type | Needs correlation support for non-tool exchanges. OPEN D-C4 |
| `/model`, `/thinking` | **Mediated** | Choose among models admitted by the provider profile in the active `HarnessProfile` | No free-form model strings from the client |
| Context files (`AGENTS.md`, `CLAUDE.md`) | **Reframed** | The supervisor reads them from the sealed source generation and injects them, with digests recorded | Pi must not discover files on the host (`--no-context-files` stays) |
| Skills, prompt templates | **Deferred** | Later: admitted directories, content-addressed in the profile | Skills that run scripts still go through `bash` |
| Extensions (arbitrary code in Pi) | **Deferred / Denied by default** | Only extensions listed in a signed profile with their own conformance | Highest risk surface; not part of the first release |
| Themes | **Native (client side)** | No Pi TUI is involved; clients theme themselves | Trivial |
| `!cmd` / `!!cmd` user shell | **Mediated** | Becomes a supervisor command with `origin=user`, run through the same executor and cell as model `bash`, with evidence | Never the native RPC `bash` |
| `/export` | **Reframed** | Export from the supervisor store; optional Pi-session-format JSONL | Bounded, no raw journals |
| `/share` (upload to a service) | **Denied** | Outward-facing network publication of a transcript | Operator can export and share manually |
| `/login`, `/logout`, `/settings` | **Reframed** | Credentials stay in the Go broker (`openrouter.LoadCredentials`); provider setup is operator configuration | Never through Pi |
| Larger prompts (>16 KiB), attachments | **Mediated** | Raise `piRPCMaxPromptBytes` under a profile-declared bound; attachments VERIFY | A bound is still required |
| Auto-retry, thinking levels, steering/follow-up modes | **Native** where Pi does it internally; settings are profile fields | | |
| Manual per-tool approval | **Denied** | Unchanged: no human override or per-tool approval (`TASK_STATE.md`) | Matches vanilla, which also does not ask per tool call |

The "do not punish users" goal has a concrete test: a documented list of *vanilla
workflows* (continue yesterday's session; steer mid-run; abort and re-prompt; fork an
approach; compact a long session; run `!git status`) must each work in an `interactive`
profile, or appear in the table above as Denied/Deferred **with a reason**.

### 5.4 Design decisions

Status: **DECIDED** by the owner, or **REC** (recommended, still to be confirmed with
`AskUserQuestion` at the "Ask before" step).

| ID | Status | Question | Decision / recommendation | Ask before |
|---|---|---|---|---|
| D-C1 | **DECIDED (DEC-2)** | Who owns session truth? | **The Go supervisor.** The durable session record, history, queue, and branch tree live in supervisor-owned storage. Pi's transcript is untrusted input and is never authoritative (the code already ignores it). Pi keeps `--no-session` (X9). Consequences: history, resume, fork, compaction, and export are supervisor features. Add import/export in Pi's JSONL session format so users can move between plain Pi and tbound | n/a |
| D-C2 | REC | Worker lifetime: one Pi process per session, or per turn? | One per session while it is active, with a bounded idle timeout, then a clean stop and a fresh start on the next turn. A per-turn start would show up as latency, the most visible way tbound could feel worse than plain Pi. Needs a measurement of cell start cost first | C3 |
| D-C3 | REC | Branching: how do forks map to workspace state? | Extend `sessionrepo` with a parent pointer so generations form a tree (today it is a linear baseline → tip chain), and store `(conversation_node, generation_id)` pairs. Ship conversation-only fork first, workspace rewind second and optional (plain Pi does not rewind files) | C7 |
| D-C4 | REC | Compaction | A governed exchange: add an exchange kind to the broker/correlation that is not a tool call. Compaction output is data, not authority | C8 |
| D-C5 | REC | Lifecycle owner for a long-lived session service | See X6: socket-activated service plus a separate surviving-owner process. This is the main entry criterion for any service that outlives a client | C1 |
| D-C6 | **DECIDED (DEC-3)** | Publication cadence | **Per turn.** After each turn ends and validates, the changes publish to the live workspace through the existing host-only publication path. This matches plain Pi, where edits show up immediately. Follow-ups below | n/a |
| D-C6a | REC | Between turns the user may edit the live workspace (plain Pi allows this). The publication contract validates against a complete baseline and reports conflicts | At the start of each turn, compare the live workspace to the last published tree. If it matches, continue the generation chain. If it drifted, **re-seed the baseline from the live workspace**, record a `baseline_rebased` event with both digests, and tell the client. Never silently overwrite user edits. If a conflict is found at publish time, publish nothing for that turn and report `not_published` with the conflicting paths | C2 |
| D-C6b | REC | What happens when a turn's publication fails or is `UNKNOWN`? | The turn is reported as `completed, not published` (or `UNKNOWN` when cleanup is uncertain). The session stays usable and the staged generation is retained for inspection. No automatic retry; the user chooses retry or discard, as for any `UNKNOWN` | C2 |
| D-C6c | REC | Cadence options beyond per turn | Per turn is the only mode in the first release. An optional "hold until I approve" or "publish on close" mode is deferred. It would add a manual step that plain Pi does not have, and the project rule is no per-tool approvals (`TASK_STATE.md`) | After the first interactive release |
| D-C7 | REC | Recovery model for turns | Each turn is a workflow instance in a session lineage that shares a generation chain (X10). A crash mid-turn yields `UNKNOWN` for that turn and never a replay (existing D20 rules). Separate recovery specification (X8); the 48-trial thesis denominator is untouched | C2 |
| D-C8 | REC | Pi 0.87.1 RPC coverage | Check which RPC commands in §5.2 exist in 0.87.1. If `steer`/`follow_up` are missing, the queue is implemented entirely in the supervisor | C3 |

Per-turn publication has a latency cost: every turn pays for validation, the quiescent
apply window, and the hash-chained journal. Measure it before C5 and keep a per-turn
latency budget; this is the main risk to the "no punishment for using tbound" goal.

### 5.5 Protocol: `tbound-service/v2` (PROPOSED)

Extends the P1–P10 proposals in the vision doc (§7); does not replace them.

- **Transport.** The same authenticated Unix socket (`SO_PEERCRED` allowlist). Not a new
  listener. Frames stay bounded and length-prefixed.
- **Frames.** `hello` (version, requested scopes → granted scopes), `session.start`
  (profile ID, workspace/source refs set by the service, never by the client),
  `session.list`, `session.resume`, `session.fork`, `session.rename`,
  `turn.submit`, `turn.steer`, `turn.followup`, `turn.cancel`, `snapshot`,
  `events.subscribe(cursor)`, `session.export`. Cursor is `epoch:sequence`, as in the
  existing observation contract.
- **Idempotency.** Every mutating frame carries a request ID. The service journals it
  (the pattern in `piServiceRequestJournal`). A reconnect never retries an effect.
- **Authority.** Scope, session ownership, and admission remain supervisor-side. Clients
  stay untrusted, as in vision §3.1.
- **Compatibility.** `v1` (one frame per connection, `busy`) stays for `single-task`
  profiles and the thesis path, byte-identical.

### 5.6 Steps and acceptance

| Step | Change | Done when |
|---|---|---|
| C0 | Record DEC-1…DEC-3 and the remaining answers (X6–X10, D-C2…D-C8) in one dated ADR under `docs/decisions/`. Each open item is asked with `AskUserQuestion` at its "Ask before" step, not all at once | Every item has a DEC row or is marked deferred |
| C1 | `interactive` session mode in `HarnessProfile` (needs WS-B) and a non-claim-bearing label on every receipt and projection | Profile loads; thesis path unchanged |
| C2 | Supervisor session store: `session_id`, durable event log, turn records, restart-safe | Fresh-process test: kill the service mid-session, restart, history intact, in-flight turn = `UNKNOWN` |
| C3 | Multi-turn worker: per-turn leases, bounded idle timeout; widen `PiRPCClient` (`steer`, `follow_up`, `get_state`) and the RPC allowlist | Test: a command outside the allowlist is never written to the pipe |
| C4 | `tbound-service/v2` frames, handshake, scoped grants, cancel | Protocol fuzz and bounds tests; cancel after settle, during, and across reconnect |
| C5 | CLI: `tbound session start|list|resume|attach|turn|cancel|export` and an interactive REPL | A scripted vanilla-workflow checklist passes (§5.3 last paragraph) |
| C6 | Context files, model switching, `!cmd` as supervisor commands | Evidence shows `origin=user` for `!cmd`; no native Pi `bash` RPC ever sent |
| C7 | Branching generation store; `fork`/`clone` | Fork restores the right workspace generation; two branches never mix |
| C8 | Compaction exchange | Compaction recorded and replayable; failure leaves the session usable |
| C9 | web-chat wiring (separate branch): switch it from fixtures/receipts to live `v2` | Covered by web-chat's own milestones 2–3 |

## 6. Sequencing, effort, and gates

| Phase | Work | Relative size | Depends on |
|---|---|---|---|
| P0 | `codex/tbound-product` cut (DEC-1); documentation amendments X1–X4 (ask first); golden gate (A0) | S | none |
| P1 | WS-A A1–A6 (registry, generator, kit) | M | P0 |
| P2 | WS-A A7 (`ls`/`find`/`grep`) and WS-B B0–B2 (profile schema, registry, literals out) | M–L | P1 |
| P3 | WS-C C0–C2 (decisions, profile mode, session store) | L | P2, D-C5 |
| P4 | WS-C C3–C6 (multi-turn worker, v2 protocol, CLI, parity) | XL | P3 |
| P5 | WS-B B3–B5 (conformance, second Pi version) and WS-C C7–C8 (branching, compaction) | L | P4 |
| P6 | web-chat live wiring | M | P4 |

Sizes are relative (S < M < L < XL), not calendar estimates. P5's second-Pi-version item
carries the largest unknown (measuring a new runtime bundle and containment on a new
version).

### 6.1 Entry gates

- The thesis candidate must not be disturbed. Merge anything touching Tier 0 into
  `codex/tbound-prototype` only after the thesis owner agrees; until then it lives on
  `codex/tbound-product`.
- WS-C needs D-C5 / X6 (surviving-owner design) before any long-lived service ships.
- Per-turn publication (DEC-3) needs D-C6a and D-C6b answered before C2 starts.

### 6.2 Regression gates (every PR)

1. **Golden `core4` gate** (A0): manifest digest, schema digests, policy digest, broker
   profile digest, bootstrap JSON all byte-identical.
2. Full Go suite per `AGENTS.md`, with 0 new skips; frontend gates on the web-chat side
   when its API changes.
3. `check-tool-manifest.sh` and `check-extension-boundary.sh` both pass.
4. No claim text changes. `claim_bearing=false` on every product receipt.
5. Dated receipt: command, exit code, source hash.

### 6.3 Interactive profile defaults (conservative)

Extensions, skills, prompt templates, and discovered context files are **off** until
their own treatment is implemented (§5.3). Turning one on is a profile change, with a
signature and conformance, never a runtime flag.

## 7. Risks

| Risk | Why | Mitigation |
|---|---|---|
| Registry refactor changes a digest and silently invalidates evidence | Digests include manifest and schema bytes | A0 golden gate before any edit; refactor commits contain no behavior change |
| "Modular" turns into a plugin system | Tempting after A | Compiled-in specs only; registry built from the signed profile; no runtime loading |
| Interactive mode quietly weakens recovery | A multi-turn session breaks the "one prompt per workflow" assumption | Separate recovery spec (D-C7); `UNKNOWN`, never replay; no reuse of thesis trial counts |
| Docs from Pi `main` differ from 0.87.1 | Upstream moves | Every VERIFY row is checked against the installed 0.87.1 before implementation |
| Parity creep | Vanilla Pi is large | The parity matrix is the contract. Each row is shipped, deferred, or denied with a reason |
| Latency makes tbound feel punishing | Cell start, journaling, and a publication transaction on every turn (DEC-3) | Measure early (D-C2, D-C6) and track a latency budget per turn |
| User edits the workspace between turns | Plain Pi allows it; publication validates against a complete baseline | D-C6a: detect drift at turn start, rebase and record, never overwrite |
| Large files make the work slow | `piruntime` ≈ 20k lines, `workflowrecovery` ≈ 11k, `sessionrepo` ≈ 10k | Refactor only what a step needs; no unrelated moves (AGENTS: surgical changes) |
| Installer is blocked | PR #10 has security blockers | Do not depend on it; profile registry ships through the existing `tbound-profile` tooling |

## 8. Immediate next steps

1. Ask the user X11 (branch integration), then cut `codex/tbound-product` from
   `codex/tbound-prototype` in its own worktree and merge web-chat per §1.1 (DEC-1).
2. Write the A0 golden test (digests of the `core4` profile) before any refactor.
3. Ask the user X1–X4 with `AskUserQuestion`, then make the documentation amendments.
4. Start A1–A3 on `codex/tbound-tool-registry`. It is mechanical and independent.
5. Start the C0 ADR with DEC-1…DEC-3 filled in. Ask X6–X10 and D-C2…D-C8 later, each at
   its "Ask before" step.
6. Tell the web-chat thread that its P1–P4 proposals are being implemented by WS-C and
   that it should keep consuming only the service API.

## 9. Sources checked

- Repo: `docs/architecture.md`, `docs/tbound-future-work-multi-profile.md`,
  `docs/decisions/2026-10-12-cli-reproduction.md`, `docs/archive/frontend-product-agent.md`,
  `docs/publication-contract.md` (skimmed for cadence and baseline rules, not audited);
  web-chat branch `docs/archive/modular-architecture-vision.md`, `TASK_STATE.md`.
- Code: `internal/{broker,broker/correlation,broker/protocol,gate,piruntime,piinstall,
  sessionrepo,delta}`, `cmd/tbound/{durable_executor,governed_session,
  pi_service_handoff,production_session}_linux.go`, `adapter/src/*.ts`,
  `adapter/profile.json`. The listings in §3.1 come from grep and are a lower bound.
- Pi upstream docs on `main` (2026-10-16): `cli.md`, `usage.md`, `rpc.md`,
  `rpc-commands.md`. These describe `main`, not necessarily 0.87.1.
