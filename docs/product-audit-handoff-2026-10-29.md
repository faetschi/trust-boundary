# Product audit handoff, 2026-10-29

This handoff sums up the owner-independent work left after the [2026-10-29 audit](audit-2026-10-29.md). It draws on that ledger and on [product contracts](product-contracts.md). It does not audit the code again. It changes no code, contract, or disposition.

## Current state

- Audit P1 to P3 are merged. `8bf704f` merged into `feature/tbound-product` as `a7f3a60`, and the merge ledger is `43dc492`.
- Selective ports of P4 and P5 from the thesis are in progress: `5d74c0b` and `f1ee21d`. A code worker is active on them, and the ledger still lists both as deferred. The ledger's shared-fix list also names `a11cee5`, `5a3f4c5`, and `44b6f7d`, which the next sync carries.
- P4 and P6 block the release.
- No product-to-thesis merge happens before the F14 freeze. [TASK_STATE.md](../TASK_STATE.md) lists owner approval for signing and the guest chain as part of that freeze, and it gives no date.
- After the freeze, propose `integration/f14-product`, based on the frozen thesis SHA, and merge the then-gated product branch there. Preserve the thesis evidence semantics and product soft refusals. Run the combined gate and an independent review before merging either target.

## P6: unknown turns have no resolution path

Only `MarkTurnUnknown` writes `unknown`, per contract line 110. An `unknown` turn blocks its session with `PREDECESSOR_UNRESOLVED`, per line 101, and its project with `PROJECT_BLOCKED`, per lines 103 and 115. Until a path exists, one unknown turn blocks its project for every session. Reopening refuses a changed registration digest while any turn is unresolved, per line 66. `ResolveTurn` accepts only `awaiting_review` turns, per lines 199 and 200, so no verb clears `unknown`.

The restart row for `linked`, no `terminal`, per line 227, is the only restart path for a linked turn. It settles `failed` only if the Store has no quarantine or unknown effect, the project equals the admitted baseline, the publication journal has no transaction for the workflow, and "no reservation open" holds. Admission reserves budget, per line 108, and for a linked turn only `CompleteTurn` releases that reservation. Read as the turn's own budget reservation, the clause never holds for a linked turn. The contract must say which reservation the row means before any option relies on it.

The ledger names three causes. Each meets that row differently.

- Shutdown past the grace period leaves a turn linked and running. The contract names no grace period. `Shutdown` in `supervisor/cmd/tbound/product_service_linux.go` reads one, and its error text says restart reconciliation settles such turns. Confirm which row check fails before choosing option 1.
- A crash after a successful auto-publish leaves a publication transaction, so the row cannot settle `failed`.
- A crash between link and prompt admission leaves no proof that no prompt was sent. Line 69 writes `product_turn_linked` before any provider or tool work, but the contract names no record that proves the absence. Confirm which row check fails before choosing option 3.

### Options, none selected

Each option needs a contract change before any code. This handoff does not authorize any of them.

1. Shutdown step-boundary cancellation. Shutdown requests `CancelTurn` for each linked, running turn. Each turn stops at its next step boundary and settles `cancelled` with usage charged, per line 216. The option adds no rollback and interrupts no call in flight. A turn whose auto-mode finalizer has started cannot promise rollback, per line 218. A call in flight still finishes first, so the grace period still bounds shutdown. The shutdown rule in line 373 changes.
2. Proven auto-publication recovery. A new restart row settles `published` when the publication journal shows the workflow's transaction as `SUCCEEDED` and the project equals the sealed candidate. The contract records a candidate before publication only in review mode, per `product_turn_awaiting_review` in line 71. This option needs a durable candidate record, written before the finalizer starts, and the new row.
3. Proven no-prompt, no-effect link recovery. A new restart row settles `failed`, with usage from the retained ledger counters, when the provider and Store journals prove that no prompt was sent and no effect happened. It needs that proof record and the row 227 clarification first.
4. Operator resolution verb. A new verb settles an `unknown` turn, because `ResolveTurn` cannot. Four points need an owner decision. Authority covers who may call it. Usage and refund cover whether unproven spend comes back, since the reservation stays consumed today, per line 74. Request idempotency covers how a retry with the same key behaves. Audit and fence rules cover which record it writes and when the project block lifts.

## P14: derive the receipt bound before any cap

P14 comes first in engineering order. It is the only P7 to P16 finding where a turn limit in the signed registration sends a turn to `unknown`. The step bound equals `host_calls`, and one step is one provider exchange, per line 395. The receipt carries `tool_sequence` and `denials`, per line 157, and refusals are bounded by the host-call limit, per line 185. So the receipt grows with `host_calls`.

The contract explicitly bounds the v2 receipt at 64 KiB in the Product turn receipt section. The producer enforces this in `supervisor/cmd/tbound/product_turn_receipt_linux.go:38-50`; the catalog repeats the bound in `internal/productsession/records.go:38` and `state.go:571-576`. The producer maps encoding failure to `unknown` in `product_turn_linux.go:387-389`.

First align the product tool-call identity rule across worker and catalog. `providerbridge.MaxIdentityBytes` is 256, while `productsession.validIdentity` accepts at most 128 printable ASCII bytes without spaces; the receipt catalog applies it to denial IDs. A provider-valid ID can therefore fail settlement independently of the byte cap. Then derive a call ceiling from the actual encoder, including escaped identifiers, the fixed envelope, and the 4096-entry list limit. Check it at registration (`registration.go:323-325`), where `Budget.valid` currently permits up to 2^40 calls. Tests must cover ceiling and ceiling+1, a worst-case encoded receipt, and an incompatible ID before a step is recorded; the refusal settlement must remain truthful. No ceiling is selected here. Product-status line 513 places versioning before the first catalog deployment. Whether any catalog is already live was not checked; confirm compatibility before shipping the check.

## P7 to P16 triage

Owner-independent engineering comes next for P8 through P12, P14, P15, and P16. A decision comes first for P7 and P13.

| Finding | Engineering next | Decision or qualification |
| --- | --- | --- |
| P7 signed serial | None until the scheme exists. | A decision on the serial scheme. The operator chooses the artifacts. |
| P8 verifier from `PATH` | Remove the `PATH` and `TBOUND_VERIFIER` lookup. Accept only `--verifier`, under the trusted-path rule. | Qualification needs the clean-host run that product-status lists for P8. |
| P9 admin binary by path | Exec the started inode with a minimal environment. | None. |
| P10 connection slots | Add a per-UID cap and an explicit `SERVICE_UNAVAILABLE`. Give the cap a stated basis. | None. |
| P11 `list_sessions` advice | Point the advice at `list_sessions`. | None. |
| P12 outcome message | Say that the outcome is unknown, and retry with the same key. | None. |
| P13 token on stdout | List the callers that read the token from stdout. | A decision on whether any supported use redirects stdout. |
| P14 receipt bound | Derive the bound, then check it at registration. | None. |
| P15 cost and ancestors | Refuse a world-writable ancestor without the sticky bit. Measure apply cost on adversarial input before choosing a bound. | None. |
| P16 low issues | Fix each one separately. The items are bidi controls in the bundle, protocol keys that collide under case folding, HTML escape growth, and the 100 ms journal re-verify. | Plaintext sessionStorage stays accepted, not reopened. |

P10 and P12 both need `SERVICE_UNAVAILABLE` in the contract. The code defines it in `supervisor/internal/productprotocol/protocol.go`, but the error list at line 123 omits it.

## Retained blockers

- Real Pi worker per turn. `product-serve` refuses `worker_mode: pi`, per product-status line 507. It needs the qualified host, the guest, and a provider key, all on FABIAN, per product-status line 453. Engineering alone cannot clear it.
- Host qualification. Nothing is qualified on a real host, per product-status line 19. The calibration measurement, the privilege drop, `systemctl enable`, `RENAME_EXCHANGE` on the qualified filesystem, a real browser run, and a real provider have not run, per product-status line 509.
- Publication mismatch behavior. After `RENAME_EXCHANGE`, a detected mismatch does not swap the old object back, per product-status line 508. Product limits writers to root and the service account for now. The fix is shared code in `supervisor/internal/publication`, and the thesis line owns it. Take it after the F14 freeze.
- Prompt and evidence versioning. Changing a compiled prompt breaks replay of earlier turns, per product-status line 436. Define the versioning before the first catalog deploys, per product-status line 513. This needs a decision.

## Name collisions

Finding IDs P1 to P16 come from the ledger. Package IDs P0 to P11 come from product-status. The numbers P1 to P11 mean both, so check which list a reference comes from. In the ledger, "deferred: P11" names the qualification and release package.
