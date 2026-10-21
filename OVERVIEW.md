# TBound — current state

Updated 2026-10-21. Frozen commit **F′ = b42b920** (codex/tbound-prototype). Engineering route OpenCode Go only. Green results verify code only: G1 false, H1 unexecuted.

**Headline: the first real governed smoke on the frozen build succeeded** on the ABI 8 guest (signed F′ image, real keys, sudo narrowed): `status completed`, `success true`, one real OpenCode Go exchange, `read` allowed, disposition `NO_DIFF`, worker settled.

| Milestone | Current state |
| --- | --- |
| System | 30 real-host defects fixed (Codex-reviewed, gate 4194/0/24), frozen as F′. New: defect #31 — a completed fresh workflow leaves no `pi_workflow_terminal` in the host catalog, so the next run is refused `prior-workflow-unsettled`; fix in progress (needs a new freeze F″). |
| Measurement | All observers have production sources. EVQ capture of F′ done (archive digest = frozen source). |
| Research enablement | C, P, B, V, adaptive attacker, per-trial reset, task oracle, `tbound-run`. |
| Evidence (EVQ / routes) | 18 of 22 EVQ offline; guest producers staged (test binaries pinned). G1, route-probe and direct-runtime runs blocked by #31. |
| Guest (ABI 8, 172.29.194.24) | F′ installed; S1–S5 signed; real smoke done; L5 N2/N5/N6 pass. Sudo narrowed to a reviewed operator dispatcher (`tbound-op`, Codex OK) plus your password sudo (owner channel). |
| Experiment | Pilot review packet + pre-registration draft ready for you and your advisor (`review-artifacts/`). Corpus review gate tightened for the next freeze. |

Needs you: pilot corpus review with your advisor (packet: `review-artifacts/pilot-review-packet-20261021.md`), decisions in the pre-registration draft.

Next: fix #31 → freeze F″ (rebuild, reinstall via your password sudo or the kit) → G1 → route probes → guest EVQ artifacts → reviewed manifest → pre-registration → pilot → thesis corpus.
