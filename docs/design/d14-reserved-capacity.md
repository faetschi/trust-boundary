# D14 bounded-capacity deployment design

**Status:** thesis-completion scope, 2026-10-15. This is a bounded design and open-obligation record, not implementation or qualification evidence. The detailed global-ticket proposal is retained historically at commit `9fb5dd6` (`docs/design/d14-reserved-capacity.md`); this file replaces that proposed mechanism.

## Adopted obligation and claim boundary

D14 retains SQLite WAL with `synchronous=FULL`, preallocated or reserved audit capacity, fault injection for write/fsync/disk-full/corruption, recorded filesystem assumptions, and restart-persistent `UNKNOWN`/`INVALID` outcomes ([decision register D14](../../../MSE_MA_Thesis/tbound/AH-technical/10-decision-register.md); [recovery contract](../../../MSE_MA_Thesis/tbound/AH-technical/02-1-recovery-and-continuation.md)). Storage must cover protected journal/WAL and emergency records, and first-write ENOSPC/EDQUOT must be tested.

This finish model covers one local claim-bearing service behind the CLI, one active workflow, and serialized effects. Remote/multi-user operation and malicious host administrator, kernel, or hypervisor behavior remain outside the thesis boundary. The service must not share its protected allocation budget with task output/workspace writes. If the thesis retains a claim against non-cooperating privileged host writers, this bounded model is insufficient and requires an explicit scope decision before implementation.

## Bounded deployment model

- Put audit, owner, and protected-root state on a dedicated service-owned filesystem or a strict service quota. A `StateDirectory` on an ordinary root filesystem alone does not reserve capacity. Bind and verify the deployed mount/quota identity and filesystem behavior at startup; ensure host backing storage cannot run out before the guest reserve is usable.
- Physically allocate a fixed reserve before each governed effect. The bound must include the maximum terminal audit outcome, owner checkpoint/proof transaction and WAL, protected-root terminal anchor and WAL, plus filesystem allocation/journal overhead. Include the pre-effect intent path in the capacity analysis. Derive the size from enforced serialized limits, SQLite page/schema behavior, and the selected filesystem; do not use the prior 64 MiB experiment size as a production bound.
- Keep a durable unresolved-workflow admission rule. At startup and before any new request ID, independently reconcile prior requests. A missing terminal record or any `unknown` terminal response remains unresolved and blocks new workflow IDs. Allow only exact-ID recovery under D20's original authority and budget rules; never replay a consumed effect or treat transport completion as proof of settlement.
- Reuse the existing authenticated local service request/effect records and cross-process singleton lock if they prove these properties end to end. Do not add a `ProtectedTrustedRoot` global ticket, new schema, generalized private capacity profile, or all-system writer mutex unless a failing test shows the bounded service model can be bypassed or the reserve lifecycle cannot be recovered from existing records.

## Required terminal-settlement evidence

Before admitting a new workflow, fresh-process verification must bind the same effect identity across: (1) the durable audit intent and known terminal outcome; (2) the owner terminal checkpoint, event, and proof; (3) the protected-root head anchoring that owner state; and (4) independent evidence for the controlled effect and its completion or absence. `UNKNOWN`, missing records, mismatched identities, and unverified reserve state block admission. They are not successful completion.

Use a physically full protected filesystem or quota to induce real ENOSPC after a controlled effect, release and sync the reserve, and prove the maximum-bound audit, owner, and root terminal writes complete. Inject crashes before and after reserve release and at terminal-write boundaries. A fresh process must either verify all linked terminal records and permit settlement, or retain the unresolved workflow and refuse new IDs. Failure to allocate the reserve must refuse before callback entry. Keep syscall EIO, corruption, and declared-platform power-loss assumptions as separately stated fault cases; a successful `fsync` return alone is not power-loss evidence.

## Existing partial evidence and open obligations

The ext4 experiment receipt at [reserved-capacity-ext4-experiment-20261014.json](../evidence/reserved-capacity-ext4-experiment-20261014.json) records real guest-filesystem ENOSPC, reserve release, audit/owner/root terminal writes, and fresh-process refusal after a crash following release. Its small synthetic checkpoint does not exercise maximum enforced checkpoint/proof sizes, qualify the production volume or host backing storage, or prove global unresolved-request admission. It is feasibility evidence only; it does not complete D14.

Open work is limited to: establish the dedicated filesystem/quota and backing-space assumption; derive and enforce a defensible reserve bound; verify the authenticated service singleton; add or prove the durable unresolved-request admission rule, including `unknown` terminal responses; exercise maximum-bound terminalization and crash points through the retained production path; and preserve fresh-process receipts. Expand to a global ticket only if one of these tests demonstrates the simpler model is inadequate.
