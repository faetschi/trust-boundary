# TBound — current state

Updated 2026-10-21 night. Freeze **F3 = ea59706** is installed on the guest; **F4 is ready to review and freeze** (`next/f4` tip `1e8364b`, gate 4282/0/24). Engineering route OpenCode Go only. Green results verify code only: G1 false, H1 unexecuted.

**Headline: the build and qualification phase is done; the last gate before evidence collection is a successful G1 run on F4.** On F3, #31 was fixed on the real guest (the earlier smoke run settled, G1 admitted and ran), but G1's first model turn failed on a streaming-capture bug. Because cleanup was held back afterwards, the stuck request cannot be abandoned, so the guest needs a one-time owner recovery.

| Milestone | Current state |
| --- | --- |
| System | Built. Freezes F0–F3 done. F4 fixes: streaming capture, failed-launch cleanup (#33), provider-turn-failure cleanup + abandon, direct-runtime launch proof, refusal hints, cgroup-test guard, net-sink per reset. Codex review of F4 pending. |
| Measurement | All observers have production sources; 18 of 22 EVQ met offline. |
| Research enablement | Corpus tooling, adaptive attacker, per-trial reset, task oracle, `tbound-run`: built. |
| Guest (ABI 8, 172.29.194.24) | Qualified, F3 installed, sudo narrowed. One stuck `unknown` G1 request: needs owner recovery (recommended: archive the service state, start fresh on F4). |
| G1 (first full governed run) | Blocked → F4. |
| Route probes, last 4 EVQ | After G1 (route probe, direct-runtime probe, guest EVQ). |
| Pre-registration, pilot | Drafts ready; waiting for your and your advisor's review. |
| Research claims (H1 corpus) | After the pilot. |
| Host | WSL moved to F: (C: full caused today's WSL hangs). |
| Product | Web chat frozen for the thesis; post-thesis backlog in `review-artifacts/product-backlog-20261021.md`. |

Needs you: guest recovery choice (A archive + fresh state / B checkpoint revert / C in-place code recovery); pilot corpus review with your advisor; pre-registration decisions.

Next: Codex review of F4 → freeze + sign F4 (kit `F:\TBoundAssets\recovery-20261015\F4\`) → guest recovery → install F4 → G1 → route probe → direct-runtime probe (+ abandon) → guest EVQ → reviewed manifest → pre-registration → pilot → H1 corpus.
