# Product owner proposals, 2026-10-29

Prepared 2026-10-29. These are owner proposed positions, not finalized or signed contracts. Every position below is pending.

The positions are not advisor confirmations, implementations, or validation evidence. This document changes no contract or status, implements nothing, authorizes no product implementation, and does not unblock any release.

This backlog stays separate from the thesis closeout. Read the [product audit handoff, 2026-10-29](product-audit-handoff-2026-10-29.md) first. Priorities and dependencies come from that handoff and the owner. P numbers here are audit finding IDs. Some contract and status text uses P4 for a product package, so check which list a P number comes from.

## P6: give unknown turns a contract before code

An `unknown` turn blocks its session and its project. Today no verb clears it. `ResolveTurn` accepts only `awaiting_review` turns. The handoff names four recovery paths. This proposal takes them as one contract, in the handoff's order.

### Owner proposed position

1. Graceful cancellation. Shutdown requests `CancelTurn` for each linked, running turn. `product-contracts.md` line 216 already stops each such turn at its next step boundary and settles it `cancelled` with usage charged. A call in flight finishes first, so the grace period still bounds shutdown. The shutdown rule at line 373 changes. Line 218 applies once an auto-mode finalizer has started: cancellation cannot promise rollback.
2. Evidence-based completed publication. A restart settles `published` only when the publication journal shows the workflow's transaction as `SUCCEEDED` and the project equals the sealed candidate.
3. Provably unstarted recovery. Recover work only when retained evidence proves that it did not start and had no effect. The proof criterion, terminal state, and usage rule still need specification; the owner has not selected `failed` versus `cancelled` here.
4. Operator recovery. An authenticated, audited, idempotent operator verb settles an `unknown` turn. It records explicit usage accounting and names the conditions that lift the project block. It never marks a turn successful to unblock its project.

Insufficient proof keeps the turn `unknown`. The contract does not promise that every `unknown` turn resolves. This draft chooses no default for the three items below.

### Owner confirmation before implementation

- Authority. Who may call the operator verb, and how the caller authenticates.
- Refund and usage. Whether unproven spend returns. Line 74 keeps the reservation consumed today, and the review-mode event already records a refund at line 71, so the new rule must match it.
- Evidence criteria. What proves a turn never started, and what proves a publication completed.

### Specification and validation still open

- Reservation ambiguity. The restart row for linked, no terminal requires "no reservation open" at line 227, while admission reserves budget at line 108. Read as the turn's own reservation, that clause never holds for a linked turn. Resolve this before implementation by naming the reservation the row means.
- Causes. Confirm which row check fails after a shutdown past the grace period, before choosing option 1. Confirm which fails after a crash between link and prompt admission, before choosing option 3. The contract names no grace period, but `Shutdown` in `supervisor/cmd/tbound/product_service_linux.go` reads one.
- Proof records. Option 2 needs a durable candidate record, written before the finalizer starts. The contract records a candidate only in review mode, at line 71. Option 3 needs its proof record and the row 227 clarification first. Line 69 writes `product_turn_linked` before any provider or tool work, but the contract names no record that proves that no prompt was sent.
- Retry and lift rules. Define how a retry with the same request key behaves. Define which record the operator verb writes, and the exact conditions that lift the project block.

## P7: reject lower release serials

### Owner proposed position

1. A signed release carries a monotonically increasing serial.
2. The installed state persists the serial of the installed release.
3. An ordinary install with a lower serial is rejected.
4. An install with the same serial is accepted only as a retry of identical artifacts.
5. Rollback is outside the initial serial change. This does not authorize removing or changing existing rollback behavior.

### Specification still open

The owner has not chosen these items. This draft invents no choice for them, and each one gets specified later:

- The exact serial representation.
- The signature binding, meaning what the signature covers.
- Atomic crash persistence of the installed serial.
- Migration of existing unversioned installed state.

The handoff puts a decision on the serial scheme first. The operator chooses the artifacts. Engineering has nothing to start until the scheme exists.

## P13: print the pairing token only to a terminal

### Owner proposed position

1. Write the pairing token to stdout only when stdout is an interactive TTY.
2. When stdout is redirected, refuse and write no token.

### Specification and validation still open

- Inventory. List each supported caller that reads the token from stdout. Identify each caller that the refusal breaks. No inventory has been performed.
- Redirected use. If the inventory finds a supported use that redirects stdout, the owner decides how that use changes. The handoff lists this decision as open.
- Detection and refusal. Nothing yet defines how the command detects an interactive TTY, or what the refusal prints and which exit status it returns.

## Prompt and evidence versioning: pin each turn to immutable IDs

### Owner proposed position

1. Each turn records immutable prompt and tool-schema IDs and an evidence-schema version.
2. A changed prompt or tool schema gets a new ID. Older turns keep the material that their replay needs.
3. The system rejects an unsupported version explicitly. It does not guess a fallback.
4. Migration of existing turns remains unspecified. Do not infer version IDs for old records or claim they remain replayable without a compatibility check.

### Specification and validation still open

- Migration and support policy. Pending. This draft chooses none.
- ID format, and the record that stores the IDs and the evidence-schema version.
- Whether any catalog is already live. The handoff did not check. The answer shows whether the policy must cover deployed catalogs.

The product-status prompt versioning risk says that changing compiled-in prompts breaks replay of earlier turns. It also says versioning comes before the first catalog deploys. That ordering is the handoff's priority for this item.

## Scope and release status

- No product-to-thesis merge happens before the F14 freeze. The freeze has no date. This draft does not change the post-freeze proposal in the handoff, `integration/f14-product`.
- P6 still blocks release, alongside the retained qualification work. Nothing in this document unblocks it.
- Handoff order: owner-independent engineering for P8 to P12, P14, P15, and P16 comes next. P7 and P13 need a decision first.
