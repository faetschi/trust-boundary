# Qualification and research procedure — 2026-10-14

Status: adopted for the implementation work by the user's explicit instructions
on 2026-10-14. Branch: `codex/tbound-prototype`.

## Host qualification

Adjust the existing Hyper-V setup to provide the implementation's exact Landlock
ABI 8 profile. The user authorizes disposal of the existing development VM and
replacement with a fresh disposable qualification guest with similar attributes.
The observed reference resources are 8 vCPUs, 16 GiB fixed RAM and a 40 GiB disk;
see [the inventory receipt](../evidence/host-inventory-20261014.json).
Inspect and preserve required artifacts before disposal. Verify the replacement's
live ABI, filesystem, runtime closure and service profile before qualification.
Use distinct private provisioning paths and keep credentials out of source control.

## Final Research Claims milestone

Research Claims remains last, after engineering, deployment and product operations.
The user, guided by the thesis advisor, will perform construct review. OSF/Zenodo
accounts and multiple independent human construct reviewers are not required for
this implementation completion procedure. The former external-registration and
role-separated human-review requirements are superseded here; the thesis sources
have not been rewritten by this decision.

Freeze the executed protocol, corpus, analysis and artifact identities locally in
version control. Retain review records and raw results. Describe the actual review
and registration procedure in reports; do not label self-review independent or
claim external registration. Record the resulting validity limitations.

Independent runtime/effect oracles, the engineering gates, D14/D20 constraints,
and the security/calibration/friction acceptance criteria remain requirements.
G1 and H1 stay unestablished until their relevant evidence has been executed and
reviewed. OpenRouter is the intended provider route; define and record the model,
request/token limits and monetary budget before provider evaluation runs.

The user subsequently fixed the total OpenRouter spend at **US$5 maximum** and
selected **DeepSeek V4.1 Flash**, model ID `deepseek/deepseek-v4.1-flash`.
The [OpenRouter model page](https://openrouter.ai/deepseek/deepseek-v4.1-flash)
confirmed that ID on 2026-10-14. Freeze provider routing, current price ceilings,
request/token limits and the complete trial cost estimate before paid execution.
Reserve each request's worst-case cost against the shared durable budget before
sending it; retain uncertain charges across restart. Include pilot, retry,
calibration and evaluation calls in the US$5 total. If the complete protocol
cannot fit, report the unfinished evidence rather than reduce its denominator
or exceed the cap. No paid runs have been executed under this decision.

## Delegation

All subsequent delegated work uses GPT 6 Luna with reasoning `max` as subagents.


## Scope amendment — 2026-10-15

**Adopted by the user's explicit priority:** complete the thesis first and defer broader product features. This amends the 2026-10-14 ordering so general product operations are not a prerequisite to the thesis. Research Claims remains last after thesis-critical engineering and qualification. This scope change does not establish or weaken G1, containment, H1, D14, or D20; the actual production path and profiles remain unqualified.

### Claim-bearing implementation boundary

The selected study may use one authenticated, local, systemd-managed UDS service as an internal supervisor boundary behind the CLI, with one active claim-bearing workflow. Remote and multi-user service behavior remain excluded. This permits an internal implementation component; it does not add a web/service product mode or qualify the route. D15 in the thesis decision register still says there is no service mode in the MVP. Reconcile that wording explicitly in the thesis before protocol freeze; the sibling thesis files are not changed by this amendment.

Exact ABI-8 guest qualification, signed-profile and Pi-closure admission, a real end-to-end production run with independent effect/publication oracles, and the retained D20 production recovery binder and actual owner/cell drain remain required. Passing component tests or fail-closed refusal does not establish successful recovery or task completion.

### Bounded D14 path

Retain D14's reserved-capacity, write/fsync/disk-full/corruption fault, filesystem-assumption, and restart-persistent UNKNOWN/INVALID obligations. First qualify a dedicated service-owned filesystem or strict service quota, with task output on separate storage and a derived physical reserve sufficient for maximum audit, owner, protected-root, WAL, and filesystem-metadata terminal writes. Use the existing local service's durable request/effect records and singleton lock only after proving one unresolved workflow blocks every new request ID across restart. A missing or `unknown` terminal result is unresolved; exact-ID recovery may reconcile under D20 rules but may not replay consumed authority or reset budgets.

A global capacity ticket, new protected-root schema, generalized capacity profile, or product-wide all-writer gate is deferred unless the bounded model fails a concrete admission, reserve, or crash test. The host administrator/kernel/hypervisor remain within the thesis trusted boundary; any broader writer threat requires a separate explicit decision. See the replacement [D14 bounded-capacity design](../design/d14-reserved-capacity.md). The earlier detailed candidate remains at commit `9fb5dd6` for historical review.

### Deferred product and research work

Defer broad history/cancel/doctor UX, frontend/web chat, generalized installer/update/rollback, remote or multi-user service, and the optional 51-trial recovery expansion. Safe shutdown and verified process drain remain engineering obligations even when a user-facing cancel command is deferred.

Research Claims remains the final milestone. The adopted user/advisor construct-review procedure does not require OSF/Zenodo or multiple independent construct reviewers; accurately report the actual review and its validity limits. Independent runtime/effect oracles and the adopted security, calibration, and friction criteria remain in force. Use DeepSeek V4.1 Flash through OpenRouter with the user's US$5 total cap; freeze current prices, routing, request/token limits, and complete worst-case trial cost before any paid call. If the full protocol does not fit, amend the thesis claim before evaluation rather than reduce the denominator silently.
