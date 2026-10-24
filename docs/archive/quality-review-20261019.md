# PR13 webchat cleanup

PR13 is rebased onto the merged prototype `2c318812aa4101145b6dcad6aa2e9befbc852def`. It contains only `extensions/webchat/` changes and these review docs. Supervisor, adapter, scripts, packaging, infrastructure, and root task-state files match that base. The main checkout and its uncommitted docs were not touched. Do not merge; the owner decides.

## Retained changes

- Presentation actions reuse immutable record collections through `publish(model)`; stream/snapshot events still copy. Tests cover action notifications, eviction, equal-sized replacement, and prior snapshot isolation.
- Rail memoization depends on records, source, and verification. Tests preserve scope changes and accessible outcome wording.
- A 512-record component test covers selection, matching inspector evidence, exact search order, End-key focus, one roving tab stop, and one snapshot fetch.

The path-filtered replay combines the webchat parts of `0dc8525`, `50eed96`, and `fb72ff3`. Its complete module tree is identical to the old PR head: `81ec345c8d5a60fea2749cb0575cfa62d00c7412`. Commit `92bd1de` records the replay on the new base. The old 12-commit history is preserved locally on `codex/archive/cleanup-before-split-20261019` at `fb72ff3` and in the [historical review](https://github.com/faetschi/trust-boundary/tree/60456aa80a94245105b429328edcf9dfd1f8fcf2/docs/quality-review-20261019).

## Deferred core branch

All original supervisor/adapter changes are held on [codex/cleanup-core-deferred](https://github.com/faetschi/trust-boundary/tree/codex/cleanup-core-deferred), based on the same `2c31881` prototype. Commit `eaa6074` adds an **unapplied patch queue**, not active source changes or qualified implementations. The queue retains the core portions of `bc18e5d`, `ec7f816`, `bcd91fe`, `97402dc`, and mixed commit `0dc8525`, with provenance and hashes.

The old core changes conflict with newer service/containment code in `pi_service_handoff_linux.go`, `containment_linux.go`, and `managed_live_root_linux.go`. Resolving their semantics is deferred until after the freeze/pilot, with the full pinned gate and an independent review. No obsolete implementation was copied over the new EVQ/route-evidence, corpus, or oracle work.

F0 in `infra/guest/PROVISIONING-SIGNING.md` still governs signing inputs. Historical `ec7f816` concerns the old offline development fixture only; it is not permission to change the pilot's deliberate `workflow_bash_output_paths: []`. `infra/guest/serve-pi-service.corpus-pilot.json` and all other pinned configs remain byte-for-byte equal to the new base.

| Deferred finding | Conditions for resuming |
| --- | --- |
| Unused core helpers, duplicate validation/copying, manifest comparison, and provider buffering | Review the preserved patches against the new architecture after the freeze/pilot; full gate and independent review. Historical proof is in the [core audit](cleanup-audit-20261018.md), not current PR validation. |
| Generation-ID index, journal/tree verification profiling, and generation copying | Post-freeze/post-pilot. Preserve tamper checks, recovery, synchronization, duplicate/missing IDs, and error precedence; profile realistic sizes, full gate, independent review. |
| Large lifecycle/publication file splits and Go SSE/history work | Post-freeze/post-pilot. Keep transaction sequencing, capture-before-admission, bounds, and ownership; full gate and independent review. |
| Further UI rendering reduction | Measure first. Removing `RecordView.detail` broke helper shape/parity and was withdrawn. Preserve focus, search/order, and trust labels; frontend/webchat gates and independent review. |
| Protocol/legacy consolidation | Retain distinct receiver checks and React/legacy LOST/DENY contracts. Require exact equivalence or an explicit product decision. |

## Rebase verification

Verification uses isolated Linux snapshots, Go 1.27.1, Node 24.21.0, `umask 077`, and a private short TMPDIR. Exact commands, exits, and rechecked source manifests are under `F:/TBoundAssets/cleanup-rebased-20261019-final/`. The snapshot identifies source commit `92bd1de` and base `2c31881`; later commits only update docs.

All gates passed with exit 0: frontend `npm run verify` (77 files, 1,281 tests, build and sink/dependency checks), `npm run repro` against committed dist before verification rebuilds, webchat Go build/vet/format/race (174 test/subtest records), and legacy Node tests (262). No skips or failures. Formatting output was empty. Frozen source is checked against the new base; no supervisor/adapter gate or production qualification is claimed for this webchat-only PR. Historical full-core results do not validate the deferred queue on the new architecture.

Source-manifest SHA256: `b3e9e1d5a201169b779207d352b914158b241a7b0b321be791f64411f3d43149`. Dist digest: `a8cc3e80285d82b4f5f0da61824421aa4a3371560b9dc8d8df7650fa2d1ad9e8`; both rebuilds match the committed artifact. The first Windows archive introduced CRLF formatting noise; the final gate exports with `core.autocrlf=false`, without modifying any source file. Independent review confirmed scope, replay identity, queue hashes, and pinned-config preservation.
