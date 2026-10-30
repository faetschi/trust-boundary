# TBound product implementation plan

Status: proposed implementation baseline, 2026-10-24. This document plans changes; it does not announce working features. Implementation progress on `feature/tbound-product` is tracked in [product-status.md](product-status.md). The owner requested product development in parallel with the thesis. Architecture and default behavior below are recommendations for that work, not separately approved runtime or deployment changes.

Source baseline: `codex/tbound-prototype`, commit `006c3fb6b67985ea0164ae53f8ec5e1b068fc33c`. Recheck source contracts before implementation. References describe that baseline unless stated otherwise.

## 1. Product goal and release scope

TBound lets a developer use Pi interactively on a project while a Go supervisor controls tool execution, workspace changes, provider access, and publication. Installing or signing the runtime is an administrative operation. Submitting an ordinary task is a user operation and must not require a new signed installation.

The first supported product release must let a new user:

1. Install a documented Linux runtime, or reach it from Windows through a Linux VM.
2. Set up Pi, a supported provider, and a protected API key without personal assistance from the author.
3. Import a real project and choose understandable governance rules.
4. Start a conversation, submit follow-up tasks, and inspect tool activity and denials.
5. Receive advisory hints, correct a denied approach, and continue within the same conversation.
6. Review changes, publish or discard them, and export approved work to the original project.
7. Cancel, disconnect, reconnect, restart, and recover without repeating uncertain effects.
8. Update the installation, rotate credentials, and uninstall without silently losing work or evidence.

Deliver a usable terminal release first. Deliver live web chat against the same service before declaring the planned product release complete. A terminal milestone is useful delivery, not permission to leave the rest of this plan unfinished.

Initial support is one local operator, one qualified Linux/amd64 host, and one active turn per managed project. Several conversations may exist, but conflicting turns receive an explicit busy/conflict response. The browser remains loopback-only. Native Windows and macOS containment, remote multi-user hosting, arbitrary Pi extensions, arbitrary provider endpoints, persistent shell processes, and generated C-DH hints are later capabilities.

Governance means deterministic tool authorization, correlated provider calls, filesystem and output policy, isolated execution, budgets, and controlled publication. Do not advertise general understanding of natural-language intent or prevention of every malicious action. A denied mediated call can receive a useful hint; an operating-system refusal may produce only a bounded command failure.

## 2. What exists and what changes

The current [architecture](runtime/architecture.md) already has the four mediated tools, Go-owned provider history, sealed workspace generations, isolated Bash cells, settlement, publication, and durable journals. The owner-local working log records successful G1 operation on F12, incomplete EVQ, and unexecuted H1. Those results do not qualify this proposed product or current HEAD deployment.

The installed task configuration pins an exact prompt, task ID, Bash output scope, and one-use admission. Changing these inputs changes trusted configuration. Corpus mode can reuse an installation for preregistered tasks, but is not an arbitrary interactive task API.

`providerbridge.Conversation.AdmitPrompt` can accept another prompt after assistant completion. Production composition nevertheless ends after one user task. Pi RPC prompt and abort support exist, but the public client has no durable conversation controls, live transcript API, or explicit cancellation protocol.

The optional web chat has React UI, observation projections, fixtures, replay, and receipt views. `NewLiveStore` refuses live operation. Its [service contract proposal](../extensions/webchat/docs/web-chat-contract.md) is useful input, not an implemented backend.

Static denial hints are implemented. Generated hints are off and research-restricted. Some older installation and architecture pages still say positive G1 never occurred. Documentation reconciliation is required before onboarding new users.

## 3. Recommended architecture

### 3.1 Keep conversations durable and execution turns isolated

A product session is a durable user conversation. Each user turn runs a fresh Pi worker and a fresh execution workflow against a verified project baseline. One user turn may contain many model exchanges and tool calls.

Go carries verified conversational context between turns. Pi's own session files are not the history authority. Every turn stops its worker, joins channels, proves settlement, and reaches a publication, no-diff, or discard disposition before another turn starts.

This follows the existing settlement boundary. Between resolved turns it keeps neither a worker nor active project ownership alive. Pending diff review keeps project ownership, but no running worker, until resolution or expiry. It adds a real history-import contract and repeated startup cost. Do not describe subsequent turns as continuing a shell process, or imply that closing a conversation rolls back earlier published turns.

The alternative is one persistent Pi worker and Store across user turns, with publication only after explicit shutdown. That design is viable if resources, recovery, and unpublished work remain owned until final settlement. It supports revisions of an unpublished cumulative diff, but extends lease duration and the recovery unit to the entire session. It is deferred because it changes more lifecycle behavior. An idle worker would still not authorize publication.

### 3.2 Choose explicit publication as the default

Read-only and verified net-zero turns resolve without a publication transaction. A changed turn stops and settles, then offers a diff and explicit Publish or Discard. Another prompt is refused with a useful pending-review response until the turn is resolved. No per-tool approval prompts are required.

Allow an operator-selected auto-publish mode for an approved project scope. Record that choice in session admission and show it in both clients. It cannot bypass scope, settlement, receipts, or conflict checks. Do not let model text select or change publication mode.

Review retains the managed-root ownership needed to protect the baseline. Every other session on that project receives `PROJECT_BUSY` with pending-review state and expiry. P1 fixes a finite default timeout and upper bound, recorded before execution and published in command help and USAGE. Rehearsal may tune the default within those bounds. On expiry, settled unpublished work receives a durable discard disposition, the recoverable candidate and journals remain retained, and ownership is released only after cleanup. Never publish on timeout.

Record a pending candidate's baseline and tip identities, evidence-bundle digest, Store/provider/command journal heads, settlement receipt, candidate digest, and owner epoch. Initial-release review does not authorize publication across a service restart. A new process reconciles the runtime, records discard without replaying effects once no-publication and safe cleanup are verified, retains the candidate for inspection, and requires a new turn. If cleanup or publication status cannot be established, it reports UNKNOWN and blocks the project. Reopening a candidate to publish under a new epoch requires a separate implementation of all live Trust/receipt obligations; journal reconstruction alone is insufficient. An active publication transaction still follows its existing recovery path.

Users who need to revise an unpublished change across several prompts need the later persistent-working-set capability. The initial clients must state that limitation at the review step.

### 3.3 Preserve authority ownership

The trusted supervisor owns project registration, prompt admission, canonical history, policy, budgets, effects, settlement, and publication. The terminal and browser request actions and read bounded projections. They cannot mint an admitted generation, provider capture, lease, publication token, or successful receipt.

Retain the pinned official Pi RPC launch and explicit TBound extension. Built-in tools, automatic extensions, skills, context discovery, direct provider access, and Pi-managed sessions stay disabled. Supporting the stock Pi TUI and unrestricted extension ecosystem requires a separate design and qualification pass.

Keep the optional web module separate. It consumes the versioned service protocol and never imports supervisor internals. The core continues to build and test without Node or the web module.

### 3.4 Limit new module structure

Add one `supervisor/internal/productsession/` owner for durable product sessions, turns, admissions, aggregate budgets, and verified predecessor links. Its persistence uses the existing protected audit machinery and established locking patterns. Do not create a second effect engine, recovery engine, provider transcript store, or publication implementation.

Put the versioned external request and projection schemas in one small supervisor protocol package only if both service and terminal code need it. The separate web module consumes wire documents and fixtures, with parity checks rather than a Go import into core.

Keep CLI composition in `supervisor/cmd/tbound/`. Reuse `providerbridge`, `workflowrecovery`, `sessionrepo`, `sessionlaunch`, `piruntime`, and `publication`. Extract only the production one-turn composition required by both research and product callers. Its result must separate assistant completion, settlement, pending candidate, and publication disposition.

## 4. Trust, identity, and durable state contracts

### 4.1 Separate installed policy from submitted prompts

Introduce an explicit product admission mode and a versioned signed registration. It authorizes a bounded service policy rather than a particular user prompt. It binds runtime and image digests, the extension, provider profiles, service identity, allowed client identities, registered projects, policy limits, and permitted publication modes.

Each project retains an exact verified managed-root registration and identity. Creating/registering a new root is a trusted project-setup operation, with its required administrative signing handled once by the supported setup flow. A turn chooses an already registered project ID, never an arbitrary client-supplied path. Delegating creation of arbitrary subordinate roots under one signed parent is a separate trust expansion and is not assumed by this release.

Keep the current exact-prompt research and corpus modes unchanged. Product admission is never selected implicitly because a request fails research validation. Unknown modes, old clients making product requests, and missing product registration fail closed.

At submission, the supervisor binds the exact prompt and digest, principal, session and turn IDs, request key, project baseline, effective policy/scope, history-prefix digest, predecessor receipt, resource reservation, and publication mode into a one-use durable admission. No per-prompt offline signature is required. Authority comes from the installed signed delegation plus authenticated user input and durable issuance.

Read-only mode is an enforced tool/effect restriction. A prompt saying "do not change anything" does not establish read-only authority.

### 4.2 Distinguish user and runtime identities

Use `ProductSessionID` for the user conversation and `UserTurnID` with an increasing ordinal for a user instruction. Preserve existing `WorkflowID`, runtime `SessionID`, `ConversationID`, `RunID`, and recovery epochs for their current meanings. Record the mapping instead of reusing a completed workflow.

Bind request idempotency to authenticated principal, product session, operation, and request key. The request digest includes all behavior-affecting fields, not only the prompt. An identical key and payload returns the existing operation or result. A reused key with a different payload returns a conflict. Reconnect and observation never submit work automatically.

For terminal clients the service authenticates the registered peer UID. For browser access it additionally requires a supervisor-issued, session-bound, expiring delegation created through a trusted local pairing operation. The supervisor derives the principal and permissions from that credential, not a gateway-supplied user ID. Separate read, submit, cancel, and exact-candidate resolution rights. Auto-publish is selected through an administratively permitted terminal setup path. The web gateway cannot enable it. A compromised gateway can abuse credentials it holds within their delegated scope; any process with the full authorized operator UID remains inside the local trust boundary. Neither case permits bypass of policy or publication checks.

Use expected session revision and expected project-baseline digest for mutations. Reject stale clients before effects. A new turn requires a known predecessor terminal disposition and its exact publication or no-publication receipt.

### 4.3 Serialize durable admission without pretending filesystem locks are transactions

Reuse exclusive managed-root ownership and increasing owner epochs. Acquire the project fence before launch. Under the product catalog's serialization lock, validate revision, ownership, baseline, idempotency, and budget, then append and sync one admission record containing the session reservation.

That journal record is the durable acceptance point. Child workflow initialization follows and records its link to the product admission. Root locks, product journal records, and child workflow records are not one filesystem transaction. Recovery must join them explicitly:

- Before durable admission, no provider or tool work is allowed. An interrupted request may be resubmitted with the same key.
- Admission without a child-workflow link is accepted but not presumed executed. Reconcile retained runtime/workflow evidence before refund or redispatch.
- A linked child without a terminal record is unresolved. Resume only through existing recovery rules, never by creating replacement work under the same admission.
- A child terminal without a product terminal is reconciled by validating the exact child receipt and baseline. Append the missing product disposition without repeating effects.
- Uncertain publication or cleanup retains recovery ownership and blocks new project effects.

On restart, acquire a fresh owner epoch and validate all retained records before serving mutation requests. Release execution ownership between turns only after a verified disposition and cleanup. Reacquire it for the next turn and compare the actual baseline with the predecessor commitment. Fencing protects cooperating writers within the registered service trust boundary; it does not make arbitrary external writers safe.

### 4.4 Import history as context, never as new execution authority

Add a providerbridge construction path that accepts an opaque, verified predecessor-context capability from `productsession`. Do not expose a public `history []Message` constructor to clients or Pi. History seeding occurs exactly once, before the new user prompt and first provider exchange.

Define a canonical history projection containing admitted user messages, captured assistant messages, denied calls, actual released tool results, and their bounded hints. Keep original call and response identities and ordering. Include a bounded trusted turn-boundary record describing the actual publication/discard/no-diff disposition and the current project baseline. A discarded prior edit must not appear to be current workspace state.

Commit the projected bytes, predecessor terminal receipt, and profile/version identity. Exclude credentials, host-private paths, raw control records, and unreleased or unresolved results. Closed sessions with gaps cannot produce a fabricated complete prefix. Previously executed tool messages remain context and cannot populate the new workflow's authorization/correlation registry.

Bind `ImportedPrefixDigest` and `PredecessorTurnReceiptDigest` into product workflow pins and checkpoints. Keep the new turn's `TaskDigest` and `PromptDigest` specific to its new admitted task. Version product checkpoints and replay validation explicitly. Legacy research checkpoints keep their current semantics and cannot silently accept a product prefix.

Recovery reconstructs the prefix from verified retained predecessor journals, compares both digests, and then verifies the new turn's transcript. Reject cross-project prefixes, missing journals, modified hints, incompatible provider profiles, response/call ID collisions, stale baselines, and attempted old-receipt reuse.

Initially, bound context and explain exhaustion before starting a provider call. Offer a new session against the current published project. Do not silently truncate, summarize, or claim full continuity. Automatic compaction is later work with its own provenance contract.

### 4.5 Preserve limits across turns and crashes

Set per-call, per-turn, per-session, and project/operator ceilings for provider calls, tokens, command duration, output bytes, retained history, storage, and active execution time. Support a monetary ceiling only where a conservative price/usage bound is available. Missing prices must not become a zero-cost assumption.

Reserve a turn's ceiling from the session budget in the durable admission before creating child-workflow reservations. Child accounting consumes that allocation rather than charging it twice. Record actual known usage and return unused allocation only after verified settlement. An unresolved reservation stays fully reserved. Reconnects, retries, new workers, recovery, and discarded work do not restore consumed allowance.

Preserve existing workflow limits and protected deadline behavior within each turn. Record finite review and inactivity limits separately from active execution time, using the protected boot identity/monotonic anchor and existing deadline-verification path. A process restart or wall-clock adjustment cannot extend an admitted deadline. After a reboot or an unreconstructable clock anchor, active work follows the existing clock-uncertain recovery refusal. A pending review is expired and follows the verified-discard-or-UNKNOWN rule in section 3.2. Session authority requiring that clock expires rather than receiving a new allowance. Cross-boot elapsed time is never credited back.

### 4.6 Use independent lifecycle and outcome fields

Keep product-session state distinct from turn state. A session is open, closed, or blocked by an unresolved turn. A turn progresses through admitted, running, cancelling when requested, settling, optionally awaiting review, and terminal. Publication has its own state and transaction identity.

Terminal outcome distinguishes published, no diff, discarded, failed, cancelled, and unknown. Settlement separately records verified STOPPED or unresolved cleanup. Assistant completion, RPC abort acknowledgement, provider success, and browser disconnect are not settlement facts.

Read-only and edit-then-revert turns take a verified no-diff path that compares actual baseline/final manifests and recorded transitions. They do not invoke a finalizer requiring an approved mutation or invent a successful publication transaction. Discard preserves journals and spend. A known failure does not release unsafe retained runtime objects.

Before any effect, cancellation stops further admission and settles any launched worker. During effects, it closes new admission, requests RPC abort, stops the execution, and reconciles the actual outcome. After publication admission begins, cancellation cannot promise rollback; finish or recover the transaction and report its real disposition. Abort acknowledgement with a surviving worker remains unresolved.

## 5. User and service operations

These are proposed commands and logical contracts, not commands available today. Final names are fixed with help/schema tests in P1. All clients use the same operations.

- `CreateSession(project, providerProfile, limits, publicationMode)` persists an authorized open conversation. It returns identity, revision, effective policy, and remaining budget.
- `SubmitTurn(session, expectedRevision, requestKey, prompt, requestedNarrowerScope)` durably admits one turn. Requested scope can narrow installed project policy but cannot widen it.
- `GetSession` and `Observe(session, cursor)` return an authorized snapshot and bounded durable events. Expired cursors report a gap and require a fresh snapshot.
- `CancelTurn(session, turn, expectedRevision, requestKey)` persists an idempotent cancellation request and returns current state, without claiming settlement.
- `ResolveTurn(session, turn, expectedRevision, candidateDigest, publishOrDiscard)` acts only on the exact settled candidate. A stale diff cannot authorize a changed candidate.
- `CloseSession(session, expectedRevision)` refuses active or pending-review work with explicit resolution instructions. Previously published work remains published.
- `RecoverSession` invokes evidence-based reconciliation. There is no "mark successful" or "force unlock" path that bypasses retained uncertainty.

Proposed terminal entry points are `tbound setup`, `tbound provider`, `tbound project`, `tbound chat`, and `tbound session`. Reuse existing commands where their meanings match. Keep `serve --pi` research requests and receipts stable. Do not require users to know workflow, lease, epoch, or evidence-file identifiers during ordinary chat.

Observations include user/assistant messages, tool intent summaries, decisions, results, hints, remaining budgets, publication/diff state, and actionable errors. Publish observations only after their authoritative source record is durable. Raw Pi lifecycle text is diagnostic and cannot establish an effect outcome.

Initial response display may deliver bounded completed assistant messages. Responsive tool/activity events are required. Token streaming is a separate enhancement because provisional upstream text currently precedes the broker's committed complete exchange; do not present provisional output as a durable response or use it for authority.

## 6. Implementation work packages

Execute packages in dependency order. Each package ends with its own reviewable commit or small commit series and retained checks. Do not start a phase by weakening a safety check that blocks the happy path.

### P0. Establish the product development baseline

Dependencies: none.

Create a dedicated product branch/worktree from the selected tested baseline. Record source/runtime/profile pins and a product task checkpoint. Use a separate product guest or independently provisioned runtime, managed roots, provider credentials, signing registration, service socket, and evidence paths. A branch alone does not isolate a shared VM or installed service.

Preserve thesis artifacts, manifests, treatment defaults, provider profiles, and run commands. Product changes do not enter a frozen experiment without its separate review/freeze process. Lift the web-chat deferral for the product branch while keeping the thesis claim boundary. Keep web extraction out of the critical path unless packaging requires it.

Acceptance: a product rehearsal cannot address the thesis service/root by default; research-mode fixtures and stored request schemas retain their behavior. The initial baseline has recorded commit, commands, exit codes, required skips, and known guest-only checks.

### P1. Fix contracts and behavior before implementation

Dependencies: P0.

Specify versioned product registration, wire requests, projections, identities, lifecycle fields, error codes, history prefix, budget allocation, root ownership, review timeout, and admission/recovery rules from sections 3-5. Use strict bounded schemas and one canonical example per operation.

Make trust changes concrete in `pi_service_handoff_linux.go`, `production_workflow_catalog_linux.go`, `governed_conversation_linux.go`, `providerbridge`, and `workflowrecovery` design notes. Determine the existing lock acquisition order before adding catalog locks. Preserve research decoder/version tests.

Write behavior fixtures for duplicate payloads, conflicting keys, stale revisions, wrong principal, missing prefix, product/research mode confusion, and all cancellation points. Cover existing transcript/message/frame caps when adding imported history. Freeze concrete configurable defaults and upper bounds, including review/inactivity limits, before the CLI milestone. Fix the first supported development language/toolchain, image digest, provenance/licenses, artifact size budget, and refresh procedure before building the P5 environment.

Acceptance: no open ambiguity about who issues an admission, which record makes it durable, what can release ownership, or how a new process handles each partially recorded operation. Protocol fixtures specify the exact client-visible result for each case.

### P2. Separate reusable product admission and accounting

Dependencies: P1.

Implement `productsession` catalog/replay using protected audit records, serialization, expected revisions, one active project owner, request-key mapping, and aggregate budget allocations. Add the explicit signed product-registration path. Do not route product requests through exact-prompt research validation or the corpus runner.

Bind scope and provider profile before acceptance. Implement restart fencing and reconciliation across product and child records. Reuse existing audit integrity checks; do not create unsigned arbitrary JSON files that can grant effects.

Acceptance: two different authorized prompts can receive distinct admissions under one installed product registration. Unauthorized peers, widened scopes, replayed admissions, changed payloads, exhausted budgets, and concurrent root writers fail before provider/tool effects. Research mode still requires exact prompt/scope and one-use admission.

### P3. Run continuous conversations through isolated turns

Dependencies: P2.

Split assistant-turn completion from the existing one-turn settlement/publication orchestration. Add verified predecessor-context construction, checkpoint pinning, replay, and new-turn baseline binding. Preserve current tool/result ordering, terminal Bash origin validation, and source/runtime attestation.

Start with a headless service client and deterministic provider fixtures. Each new user turn has a fresh Store, source preparation, worker, provider execution identity, and workflow. Prior context never gives an old tool call new authority. Correctly report a discarded predecessor in the imported context.

Acceptance: two turns carry verified history and current project state, while a fresh worker handles the second turn. A denial and hint survive context import and recovery. Changing a predecessor journal or substituting another project's history blocks launch. Read-only, no-diff, edit/revert, Bash-last, and no-tool assistant turns all have truthful outcomes.

### P4. Add review, cancellation, and fresh-process recovery

Dependencies: P3.

Expose settled candidate diffs, exact candidate-bound resolution, discard, optional recorded auto-publish, review expiry, and lifecycle/outcome projections. Wire explicit cancellation to admission closure, RPC abort, process/descendant settlement, and publication recovery.

Preserve candidate evidence on timeout/discard and define retention cleanup. Fence a root across review. On a known terminal outcome, release only after the retained objects and journals satisfy existing cleanup rules. On UNKNOWN, keep recovery ownership and block the project while leaving unrelated qualified projects usable.

Acceptance: crash tests restart in a new process after acceptance, allocation, child registration, effect, settlement, pending review, publication admission, per-object publication, and terminal-record boundaries. A restarted pending review becomes verified discard with retained candidate, or UNKNOWN; it never publishes on reconstruction alone. No reconnect repeats an effect. Test cancel before launch, mid-command, during review, after publication starts, and abort acknowledged with surviving descendants. No-diff uses actual manifests, and discarded turns still consume spend.

### P5. Make project policy practical for development

Dependencies: P2; integrate with P3-P4 before the terminal release.

Add project registration and import into a protected managed workspace. The authenticated user-UID client reads the original project and streams a bounded manifest/content bundle to the service. The service validates paths, types, limits, and digests before creating managed generations. The service account never receives filesystem access to the original repository; Pi and command cells see only the imported copy. Import excludes `.git` administration, known service credential locations, and operator-configured private files or paths. Show the included/excluded set before import. Do not claim to recognize every secret embedded in an ordinary source file. Detect unsupported links, mounts, metadata, and size limits with actionable errors. Preserve existing support limits rather than silently dereferencing links.

Design a versioned product scope with read-only areas, explicit protected paths, and approved writable subtrees or exact files. Keep the research exact-path scope unchanged. All typed writes/edits and Bash candidate deltas must apply the same effective product scope. Canonical path-component matching, descriptor-relative resolution, protected-path precedence, new directories, renames, deletions, and metadata changes need explicit tests. A subtree rule cannot be a string-prefix comparison or a promote-all relaxation.

Provide a bounded change preview and export a service-produced, digest-bound patch/content bundle. An explicit user-UID client operation applies it to a user-authorized destination after checking original baseline, destination identity, and approved candidate digest. Keep a client-side recovery record. The service cannot write the original repository. Initial direct application supports only clean Git worktrees with documented writer exclusion; dirty trees, conflicts, unsupported binary/mode operations, or an unverifiable baseline are refused. If writer exclusion cannot be established, deliver the bundle for explicit user application without claiming race-safe direct export. Specify interruption outcomes and preserve original content. Do not use unrestricted `cp` or an agent shell tool to bypass publication. Record explicit support/refusal for untracked files, executable bits, and deletions in P1.

Provide one shipped development environment for the language/toolchain selected in P1, with pinned image digest, dependency provenance/licenses, size bound, and dependencies available before agent execution. Its real tests must run with command-cell network access disabled. Dependency refresh is a separate trusted setup operation and produces a newly verified image/profile. Correct advertised tool schemas where read pagination, binary/images, or Bash timeout differ from durable executor behavior. Implement the needed bounded behavior or remove the unsupported promise from both adapter and broker manifests.

Acceptance: import a small real project using the selected development environment, change several allowed files, create a directory, run its real tests without network access, publish to the managed project, and export with a clean diff. Verify that the service account cannot read or write the original repository. A configured secret is absent from Pi source/context. Scope-escape, link, protected-path, conflicting export, and unsupported-format cases fail closed. Read-only sessions cannot mutate through any of the four tools.

### P6. Deliver the interactive terminal client

Dependencies: P3-P5.

Implement `tbound chat` over authenticated session operations. Show the selected project/provider, governance mode, budgets, assistant output, tool activity, denials/hints, pending review, and terminal outcomes. Offer Publish, Discard, Cancel, Close, and Reconnect without exposing internal workflow identifiers as required input.

Define Ctrl-C precisely: first interrupt requests cancellation of active work; exiting the client detaches. A disconnected client leaves accepted work owned by the service. Reconnect uses the same session ID and cursor and never silently resubmits the last prompt. Give stable nonzero exits for refusal, conflict, and unresolved outcome.

Acceptance: a user completes two real turns, receives and follows a denial hint, reviews/publishes changes, cancels another turn, detaches, and reconnects. Exercise the client over SSH from Windows with pinned host keys. No stock ungoverned Pi launch is used as a fallback. This is the terminal milestone.

### P7. Productize providers and protected credentials

Dependencies: P1-P2; may proceed alongside P3-P6 against fixed contracts.

Inventory actual code rather than copying dated provider prose. The baseline engineering profile pins OpenCode Go `deepseek-v4.1-flash`. Its loader names are `TBOUND_OPENCODE_GO_API_KEY` and `TBOUND_OPENCODE_GO_API_KEY_FILE`; the managed service uses a protected credential-file contract rather than inheriting the caller's shell environment. Existing OpenRouter use is separately research-profile constrained.

Introduce explicit product provider profiles with exact endpoint/protocol, model identity, accepted response identities, request options, limits, and pricing behavior. The first release requires the qualified existing OpenCode Go route. A separate OpenRouter product profile is optional follow-up work, subject to owner selection, funding, and live-test authorization; it must not require corpus/research mode or delay the initial supported product. Advertise only qualified profiles. Ship no arbitrary URL override or silent provider/model fallback. Additional API families need their own tested transport rather than relabeling Chat Completions.

Provide setup/status/check/rotate/remove operations that provision credentials for the actual service identity, with protected ownership, ancestors, and file modes. Accept secrets through private input or an already protected file. Never put a key in command arguments, shell history, source control, Pi environment, browser storage, transcripts, or diagnostic exports. Test error redaction and revoke/rotate behavior. Offline signing keys are distinct from provider keys and never enter the online service.

Separate an offline configuration check from an explicit, cost-bounded live provider check. Verify current official account/key steps and supported model protocols when writing release instructions. This plan does not promise current external pricing or model availability. Pin profiles per admitted session; switching profiles starts a new session in the first release.

Acceptance: provision and rotate a synthetic secret without disclosure, then perform an authorized bounded live exchange on each advertised profile. Reject redirects, wrong response model, unsupported protocol, missing usage/cost evidence where required, expired keys, quota exhaustion, and network failure with actionable errors. No provider test starts implicitly during install or doctor.

### P8. Build clean-host setup, updates, and diagnostics

Dependencies: P1; complete product integration after P5-P7.

Use current `install/`, release scripts, profile tooling, and `infra/guest/` as inputs. Replace author-specific kit paths, IPs, sudo assumptions, and manual source edits in the supported path. Distinguish downloading verified artifacts, installing user commands, provisioning the governed service, and admitting the host. A checksum alone is not publisher authentication.

Provide a dry-run/check path and explicit administrative setup for service accounts, ext4 managed roots, cgroup delegation, rootless Podman/crun, network policy, read-only runtime bundle, image trust, measurement/signing, protected credentials, and socket activation. Record the supported kernel/Landlock/systemd/toolchain matrix from real qualification. Do not promise every Ubuntu or WSL install can satisfy it. Unsupported hosts fail before effects and point to a qualified VM option.

P1 selects the release signature scheme, verification tool, signed manifest format, independently distributed publisher-key fingerprint, and explicit first-install trust limitation. Verify that manifest and artifact digest before extracting or running downloaded content. Operator/admin signing keys are created and retained offline by the operator. Specify which host measurements require local signing and how registrations change after updates. A setup wizard can prepare and validate signing inputs; it cannot silently replace an owner trust key or treat its own unsigned measurement as external assurance. No per-task signing ceremony remains.

Bundle the pinned Pi/runtime/extension for governed execution. Explain the distinction between that bundle and an optional standalone Pi installation for development. Verify Node/Pi compatibility through the bundle, not an arbitrary `pi` on PATH. Do not instruct users to put API keys into standalone Pi login files for governed sessions.

Extend doctor with advisory prerequisites, protected installation checks, service reachability, profile verification, and an explicit runtime self-test. Only actual admission/self-test evidence can establish governed readiness. Do not change the advisory doctor into a boolean shortcut around runtime admission.

Updates stage artifacts, settle/refuse active work, validate compatibility, preserve journals, and atomically select a verified installation where supported. Define interruption recovery and a last-known-good rollback. State/schema migrations need backups, version checks, and a tested recovery path; do not claim old binaries can read new schemas. Key/profile revocation is explicit. Uninstall disables new work, settles or reports unresolved owners, removes service/runtime access, and preserves projects/evidence by default. Separate purge requires an explicit user action.

Acceptance: follow USAGE on a clean qualified Linux machine and a fresh Windows-to-VM setup. Reboot and reconnect. Repeat setup without duplicate accounts or weakened modes. Inject setup/update interruption. Reject a tampered artifact even when its same-origin checksum was also replaced. Test unsupported ABI, missing cgroup delegation, writable runtime assets, signature mismatch, expired registration, credential rotation, rollback compatibility, and uninstall with retained work. No author's existing VM, F: drive, or private key is a prerequisite.

### P9. Expose live observations and authenticated web chat

Dependencies: P4, P6, P7; first verify protocol fixtures before frontend work.

Implement a supervisor-owned bounded snapshot/history/event projection from durable records, with monotonic cursors, gaps, session revisions, visibility rules, and redaction. Use completed committed broker messages for transcript truth. Do not let the gateway infer success from log text or consume raw authoritative journals as its control mechanism.

Replace the extension's unavailable live source with an authenticated service client. Add submit, cancel, candidate review, resolve, and close routes. Retain fixture/replay/receipt modes as clearly labeled demonstrations. Keep core and optional extension dependency checks active; update the existing contract proposal to match the implemented version.

Authenticate browser access even on loopback. Use session ownership, strict Host/Origin validation, CSRF protection on mutations, restrictive cookies, authenticated event streams, no permissive CORS, and bounded request bodies. Use one-time local pairing that is not exposed to unrelated pages or durable logs. Sanitize markdown, links, and tool output. Separate trusted governance status from model-authored content visually and structurally.

Reuse the existing React UI. Wire the composer, cancel button, active project/profile display, governance decisions/hints, remaining budget, diff review, publication/recovery states, and reconnect. Preserve keyboard access, focus behavior, readable errors, responsive layout, and accessibility. Windows access uses a documented loopback SSH tunnel into the qualified VM, not an unauthenticated network bind.

Acceptance: real browser submission runs governed Pi, not a demo. Reload, disconnect, duplicate click, stale diff, expired cookie, malicious origin, Host rebinding, cursor expiry, XSS payload, and service restart cannot duplicate work or grant authority. Terminal and browser clients observe the same outcomes. Run the existing extension/frontend gates and live browser checks.

### P10. Make README and USAGE the product entry points

Dependencies: start alongside P0-P1; update each delivered capability through P9; finish before release.

Rewrite repository `README.md` for a new TBound user. Lead with the product purpose, a concrete governed coding workflow, supported platform and readiness, and a short route to installation. Link prominently to `USAGE.md`, architecture/security limits, troubleshooting, and contribution/release information. Keep thesis background and research links in a short final section. Do not lead with H1, EVQ, freezes, corpus operations, or the author's guest.

Keep readiness truthful while development continues. Proposed commands live in this plan until implemented. A product-oriented README can state that interactive operation is under development; it cannot present a fixture as the working product.

Rewrite `USAGE.md` as the complete supported user journey. It must contain executable steps and expected results for:

1. Selecting supported Linux or preparing a Windows VM and pinned SSH connection.
2. Installing prerequisite packages and verifying host/kernel/filesystem capabilities.
3. Acquiring and verifying a TBound release, or building the supported source route.
4. Installing the correct Pi/Node/extension bundle and explaining standalone Pi differences.
5. Preparing trust keys, signed registrations, the service account, isolated runtime, and socket. Include service start, stop, status, logs, and a first-run verification checklist.
6. Creating a provider account/key, selecting an actually supported profile/model, privately provisioning it, setting limits, checking status, and explicitly testing a connection.
7. Importing a project, protecting private material, selecting output scope and publication mode, and checking effective policy.
8. Starting a real governed terminal conversation, making a follow-up request, seeing a denied action/hint, reviewing/publishing, and exporting changes.
9. Starting live web chat, pairing/authentication, and the Windows loopback tunnel.
10. Cancellation, disconnect/reconnect, service/host restart, UNKNOWN recovery, context limits, and conflicting external edits.
11. Reading diagnostics, rotating/revoking keys, changing providers, backing up, updating/rolling back, and uninstalling.

USAGE is the canonical complete sequence. Detailed references may explain individual fields, but must not hide a necessary manual step behind an unqualified "see runbook" link. Include required privileges, current commands, example paths, bounded expected output, likely failures, and recovery instructions. Never require the author to prepare a freeze kit for ordinary use. Provider account pages/protocol instructions are checked against official sources at release time.

Move research-only operation to a dedicated research guide linked near the end. Keep `OVERVIEW.md` as an engineering checkpoint rather than an onboarding prerequisite. Keep the personal owner guide outside the repository and link it to canonical instructions instead of duplicating setup. Reconcile `docs/README.md`, install/launch/providers/doctor/release/architecture pages, help output, and the optional web README. Remove dead archive links only after checking the actual concurrent documentation cleanup.

Acceptance: a person or clean-room agent with no chat history follows README then USAGE to a real two-turn governed result. Every necessary command, trust input, credential step, expected state, and supported limitation is available without author-specific files. Documentation smoke checks validate examples against the shipped help/schema. Unsupported future commands and obsolete provider environment names are absent from runnable instructions.

### P11. Qualify and release the product

Dependencies: P0-P10.

Publish a versioned capability/support statement and release notes tied to source, runtime, policy, protocol, and schema versions. Distinguish advisory prerequisites, admission, observed containment, product acceptance, and separate thesis evidence. Include licenses and dependency provenance for shipped Pi/runtime assets. Finish release artifact signing/hosting and installation verification; cross-platform CLI artifacts do not imply cross-platform containment.

Run targeted core tests, vet and race checks, adapter tests, the extension boundary guard, packaging/install checks, optional web/frontend checks, and required real-host tests. A guest-only skip is an unmet release obligation until the guest test runs. Use the existing strict Linux packaging check from [the release runbook](guides/release.md), not its diagnostic skip mode.

Retain a complete product acceptance receipt with exact commands, exit codes, source/profile/image digests, skipped/unrun checks, and test environment. Capture a repeatable defensio demonstration: fresh setup verification, two related tasks, governed tools, a denied action and correction, diff/publication/export, cancellation, and reconnection. Use synthetic project content and a declared provider spend cap. Do not label this demonstration an H1 experiment.

Acceptance: all required scenarios in section 8 pass against one pinned release candidate. A clean installation works without undocumented preparation. The docs match that candidate. Known limitations have concrete user impact and a supported recovery path. Product operation cannot mutate the frozen thesis deployment.

## 7. Dependency order and implementation boundaries

P0 and P1 precede authority changes. P2 precedes P3, and P3 precedes P4. Project work in P5 can begin after P2 but must finish before P6 is accepted. Provider/setup work in P7-P8 can proceed alongside lifecycle implementation once P1 contracts are fixed. P9 follows a working terminal/service contract. P10 runs throughout. P11 is the release decision, not a substitute for missing package acceptance.

Suggested reviewable implementation units are product registration/decoders, catalog/idempotency, aggregate budgets, verified history/checkpoint schema, one-turn composition, no-diff/discard, cancellation/recovery, project import/export, product scope, terminal controls, provider setup, clean-host setup, live projection, web authentication, UI wiring, and release documentation. Split further when a unit cannot be verified independently. Do not create a PR containing all these authority changes at once.

Code ownership anchors:

- `supervisor/cmd/tbound/pi_service_handoff_linux.go`, `production_workflow_catalog_linux.go`, and `serve_linux.go`: authenticated request routing and research/product separation.
- `supervisor/cmd/tbound/production_session_linux.go`, `production_binder_linux.go`, `governed_conversation_linux.go`, and `governed_session_linux.go`: one-turn composition and authority binding.
- `supervisor/internal/providerbridge/` and `supervisor/internal/workflowrecovery/`: verified context, transcript pins, budgets, and replay.
- `supervisor/internal/sessionrepo/`, `sessionlaunch/`, `publication/`, and `workspace/`: project generations, scope, source evidence, settlement-dependent publication, import/export checks.
- `supervisor/internal/piruntime/` and `infra/guest/`: pinned launch, managed root, systemd configuration, runtime/credential provisioning, and measured host requirements.
- `supervisor/internal/broker/openrouter/` and `supervisor/cmd/tbound/service_provider_credentials_linux.go`: actual provider transport and secret contracts, despite historical package naming.
- `adapter/src/`: tool/protocol descriptions and advisory feedback parity. No filesystem, provider, or policy authority moves here.
- `extensions/webchat/`: gateway and React client of the service protocol.
- `install/`, `scripts/`, `packaging/`, `README.md`, `USAGE.md`, and `docs/`: delivery and supported operation.

Do not rename historical provider packages or extract web chat merely to make the plan look cleaner. Do those separately only when a required product contract warrants them.

## 8. Required product acceptance scenarios

Retain each scenario's source/runtime/profile pins and exact result. Synthetic transport tests cover protocol behavior; live provider and real Linux checks cover their own boundaries. Neither substitutes for the other.

- A clean supported machine reaches a real two-turn conversation from README and USAGE, including provider provisioning and Pi setup.
- Two different prompts run without signing or reinstalling. Changing an installed runtime or widened policy still requires the documented administrative trust update.
- A follow-up uses verified prior context and the current published baseline. A discarded edit is not mistaken for present state.
- Allowed read/write/edit/Bash work succeeds. Protected changes and scope escapes are denied with truthful bounded feedback. A corrected new call is governed afresh.
- No-tool, read-only, and edit-then-revert turns settle with verified no-diff. A Bash-last mutation publishes only with its exact terminal lease-origin receipt.
- Default review publishes or discards the exact displayed candidate. Auto-publish requires an admitted choice. Timeout never publishes.
- Double submission, conflicting idempotency keys, stale revisions, concurrent clients, and cross-user/project requests do not create duplicate effects.
- Cancel and crash cases at every P4 boundary settle or become explicitly UNKNOWN. Reconnection does not replay tool effects or bypass blocked ownership.
- Context/token/spend/deadline/storage limits remain effective across retries and process restarts. Uncertain spend stays reserved.
- Configured private files and provider keys remain outside worker source, provider context, logs, UI, and diagnostic export. Credential rotation works without recompiling or per-task signing.
- Import/export and external edit conflicts preserve the original repository. Unsupported links, metadata, and size limits produce actionable refusals.
- Real cell filesystem/process/network constraints and cleanup are observed on the supported host. The UI's success label is never their sole evidence.
- Browser pairing, authorization, Host/Origin, CSRF, XSS, cursor gaps, and reconnect are exercised against live service integration.
- Setup rerun, reboot, interrupted update, compatible rollback, backup/recovery, and evidence-preserving uninstall succeed.
- Existing research exact-task/corpus contracts remain reproducible. Product scripts never target frozen thesis state by default.

## 9. Design synthesis and decisions to settle during P1

Two independent design candidates compared persistent workers against isolated turns. Both preferred isolated per-turn execution. A separate cross-judge preferred the candidate with clearer deployment/recovery consequences, and requested stronger history pins, durable idempotency, reservation ordering, project import/export, and review semantics. Those requirements are incorporated here. The judge's suggestion of one atomic filesystem-lock/catalog/child operation was replaced with the explicit recoverable sequence in section 4.3.

The recommended baseline combines fresh turn workflows, Go-owned verified context, default diff review, optional admitted auto-publication, managed project import/export, finite review retention, static hints, and terminal-first delivery followed by live web chat. Persistent workers, arbitrary live-directory mutation, auto-compaction, generated hints, broad provider plugins, and per-tool approvals are deferred with their concrete reasons above.

P1 must settle exact command and schema names, supported host/runtime pins, finite default limits, review/inactivity timeouts and clock rules, source import/export formats and user-UID permissions, product scope syntax, first advertised provider profiles, credential provisioning commands, browser delegation permissions, product instance paths, and migration/retention versions. It must also select release signing/key distribution and the first development environment's language, toolchain, image provenance/licenses, size budget, and refresh procedure. These are bounded design tasks with acceptance tests. They are not permission to weaken the invariants in this plan or to run privileged installs, signing, or paid rehearsals without their applicable authorization.

Reopen the lifecycle choice if measured startup latency or inability to revise pending work prevents the required user workflow. Compare alternatives using a real two-turn prototype and preserved settlement guarantees before changing the architecture. Report measurements with their limiting factors rather than assuming a persistent worker is faster enough to justify its added ownership.

## 10. Completion and progress tracking

At implementation start, create a product-specific checkpoint with P0-P11 status, current source/runtime/schema versions, acceptance receipts, and remaining decisions. Update this plan when an accepted contract changes; do not leave a second contradictory design note for the next implementer.

The product goal is complete only when P11 is accepted, including live web chat and clean-host documentation. No calendar estimate is asserted before P1 and the first two-turn runtime prototype establish scope and deployment cost. Track completed acceptance units and remaining blockers instead of guessed percentages.

This planning change does not implement new commands, alter README/USAGE behavior claims, sign artifacts, install software, call providers, or change a VM. Implementation starts from P0 after the owner chooses to proceed with the plan.
