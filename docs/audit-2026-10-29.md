# Audit 2026-10-29: product findings and debt ledger

Scope: the clean export of `feature/tbound-product` at `c66599b`, with the branch diff `5f2d0a6..c66599b` reviewed against the code it touches. The thesis was audited at `d510813` in the same pass; its ledger is `docs/audit-2026-10-29.md` on `audit/thesis-fixes-1029`, merged into `codex/tbound-prototype`. Merged `audit/product-fixes-1029` at `8bf704f` into `feature/tbound-product` as `a7f3a60` on 2026-10-29 after independent scoped review. Read-only slices: service authentication and protocol, the catalog and turn driver, setup, update and credentials, project import and export, the web gateway and React live mode, and the code shared with the thesis. Each finding was checked against the code before it got a disposition.

Dispositions: **fixed** (P1-P3 on `audit/product-fixes-1029`, merged as `a7f3a60`; P4-P5 selectively ported below, all with red-before-green tests), **accepted** (decided, with the reason), **deferred** (owner and trigger named).

## Fixed

| # | Finding | Fix | Test |
|---|---|---|---|
| P1 | `setup` installed the service binary and the publisher key below directories that update and rollback refuse: group- or other-writable, or not root-owned on a live host (for example Debian's `/usr/local`). | `e9cb1df`: before the first write, including on a dry run, setup checks the existing ancestors of `BinaryDir` and of the key directory with the update rule. | `TestSetupRefusesAnUnprotectedBinaryAncestorBeforeAnyWrite` (ancestor and leaf rows), `TestSetupLiveRefusesAUserOwnedRootAndWritesNothing` |
| P2 | `project bundle` printed only a summary of what it included. The contract also promised that the registration's protected paths are excluded, but the registration does not exist at bundle time and the command has no input for it. | `d347cb3`: the bundle prints every included path. The contract and `--exclude` help say that the user passes protected and private paths with `--exclude`. | `TestProjectBundlePrintsEveryIncludedPath` |
| P3 | A model that writes 256 long `<`-heavy file names makes the review event about 1.5 MB. That is over the frame, so `observe` failed at that cursor for good. | `a315e22`: the review paths are bounded by count (256) and by worst-case encoded size (256 KiB). `6c7afa1`: the chat prints the full count from `Text`, which is now always set. So a zero-path candidate reads "0 changed paths" in the chat and in the web transcript ("Awaiting review. 0 changed paths. Candidate ..."), and one path reads "1 changed paths", the service's existing wording. A chat from this build talking to an older service would print an empty count for zero paths. | `TestProductServiceReviewEventFitsAFrame`, `TestProductServiceReviewEventCapsPathsAndCountsAll`, `TestChatReviewLineShowsTheFullCountNotTheBoundedPaths` |
| P4 | Workspace aliases ended a product turn as unknown. | Thesis `5d74c0b` ported as `b7eefe3`, with `abfb6ad` escape refusal coverage. Canonical paths reach preflight, execution and replay; provider arguments remain unchanged. | Product turns writing `./a.txt`, `src//a`, `src/./a` publish; protected alias denial preserves bytes, continuation and predecessor replay. |
| P5 | A second replay pass retained the promoted provider tip. | Thesis `f1ee21d` ported as `b824d0d`; `ReplayRestarter` resets each pass. | `TestReplayRetiresAnUnleasedCallAfterAPromotedWrite` and strict replay of the retained retirement. |

## Deferred or accepted

| # | Finding | Disposition | Reason, owner, trigger |
|---|---|---|---|
| P6 | An `unknown` turn blocks its project, and no operator path clears it. It is reached by shutdown past the grace period (the turn is not cancelled at a step boundary, contract line 373), a crash after a successful auto-publish and before settlement, and a crash between link and prompt admission. | deferred: product design; **blocks release** | Needs a contract change. Options: cancel running turns at shutdown, settle a proven auto-publish as published, and add an operator resolution verb. See [the P6 handoff](product-audit-handoff-2026-10-29.md#p6-unknown-turns-have-no-resolution-path) for the options and their consequences. |
| P7 | The signed release manifest has no version or serial, so an old signed release installs over a newer one. | deferred: P11 release tooling | The operator chooses the artifacts. Needs a signed serial and a stored installed serial. |
| P8 | A root `install.sh` takes its signature verifier from `PATH` or `TBOUND_VERIFIER` with no ownership check. | deferred: P11 | sudo's `secure_path` blocks it by default. Accept only `--verifier` under the trusted-path rule. |
| P9 | The turns listing in `update` re-executes the admin binary by path, as the service account, with root's environment. | deferred: P11 | It needs an attacker-writable admin binary location. Exec the started inode with a minimal environment. |
| P10 | There are 64 global connection slots, and an overflow connection is closed silently. | deferred | Only registered principals can do this. Add a per-UID cap and an explicit `SERVICE_UNAVAILABLE`. |
| P11 | On an unknown `create_session` outcome, the advice names `--session`, which the client never received. | deferred: P10 docs and CLI | Use `list_sessions`. |
| P12 | A failed catalog append returns a clean `SERVICE_UNAVAILABLE`, so the web client shows "not run". | deferred | The journal poisons itself, so a same-key retry cannot write twice, and restart reconciles the admission. Only the message is wrong. |
| P13 | `tbound-webchat` prints its pairing token on stdout. | deferred: P11 | The banner warns against redirecting it. Refuse a stdout that is not a TTY. |
| P14 | A receipt over 64 KiB, reachable with a very large operator turn limit, settles the turn `unknown`. | deferred | Bound the turn limit at registration after aligning receipt identifier validation. Takeover triage also found the provider/canonical-receipt identity bounds differ (256 versus 128 bytes); see [P14 engineering prerequisites](product-audit-handoff-2026-10-29.md#p14-derive-the-receipt-bound-before-any-cap). No cap or identity change is selected. |
| P15 | Apply cost grows with adversarial path depth and file count; the state-directory ancestor rule accepts a root-owned, world-writable directory without the sticky bit. | deferred | performance on adversarial input; a misconfigured host |
| P16 | HTML escaping inflates markup-heavy prompts in the gateway; sessionStorage holds unresolved prompts in plaintext; protocol keys are not refused under case folding; observe re-verifies the journal every 100 ms; the bundle prints names with bidi controls raw. | deferred or accepted | low impact. Plaintext sessionStorage is accepted: it is per tab and behind the CSP. |

## Shared-fix ledger entries

- Thesis to product, ported: `5d74c0b` and `abfb6ad` to `b7eefe3` (P4 alias normalization, escape refusals, protected alias scope/continuation/replay); `f1ee21d` to `b824d0d` (P5 retirement replay after promoted write).
- Thesis to product, test-only: `9759b42` and `aee0722` adapted as `df8b655` for mount-namespace scan observation. Stable PID start time and UID are required for an owned-process error; inconclusive observations fail after three attempts. Production scanning is unchanged and no skips were added.
- Thesis to product, still owed after F14 freeze: recovery identity helper `a11cee5` plus `fc117ef` tests (product-serve does not reach it), and guest-evidence fixes `5a3f4c5` and `44b6f7d` (thesis tooling). Carry these through the reviewed integration branch, not a pre-freeze product-to-thesis merge.
- Product to thesis: none from this audit. P1 to P3 are product-only code.

## Takeover validation

The reviewed audit merge baseline `43dc492` passed the clean-export full gate: 5745 pass / 31 skips / 0 fail, including subtests, and was pushed. P4/P5 passed two independent static review rounds, red-before-green regression checks, and focused suites: product 161 pass / 3 skips / 0 fail, recovery 6 pass / 0 fail, providerbridge 214 pass / 0 fail. Harness-only failed attempts and final delivery results are retained in `F:/TBoundAssets/takeover-20261029/RESULTS.md`. P6 and other recorded release qualification blockers remain open.

The subsequent gate exposed an overly narrow error assertion in the pre-existing mount-namespace scan test. An owned zombie reproduced the same error in five fresh processes; PID 12822's historical state is unproven. The reviewed test correction passed ten fresh race-enabled zombie checks, ownership mutation checks, 35 focused race checks, and the piruntime race suite (403 pass / 1 existing skip / 0 fail). A fresh full gate of the final delivery commit remains required before push.
