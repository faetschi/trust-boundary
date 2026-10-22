# TBound — current state

Updated 2026-10-22. Freeze **F3 = ea59706** is installed on the guest. **F4 (`next/f4`, tip d237d39, gate 4307/0/24) is in final review before freezing.** Engineering route OpenCode Go only. Green results verify code only: G1 false, H1 unexecuted.

**Headline: one full real run (G1: read, edit, bash, publish) is the gate; F4 now fixes everything known to block it.** An outside pre-mortem found a blocker no test covered: publication could never succeed, because the live root was its own mount point. F4 moves it to `/var/lib/tbound/live/root`.

| Milestone | Current state |
| --- | --- |
| System | Built. F4 fixes: streaming capture, cleanup + abandon after provider-turn failure and failed launch, direct-runtime launch proof, live-root layout (publication), G1 host-call budget (reads count), bash cell umask, refusal hints, cgroup-test guard, net-sink per reset. |
| Measurement | All observers have production sources; 18 of 22 EVQ met offline. |
| Research enablement | Corpus tooling, adaptive attacker, per-trial reset, oracle, `tbound-run`: built. Pilot budgets too small (2N+1 host calls); fix with the reviewed pilot manifest. |
| Guest (ABI 8, 172.29.194.24) | Qualified, F3 installed, sudo narrowed. Stuck F3 G1 request → option A: archive the service state, start fresh on F4. |
| G1 (first full governed run) | Next, on F4. |
| Route probes, last 4 EVQ | After G1. |
| Pre-registration, pilot | Drafts ready; waiting for your and your advisor's review. |
| Research claims (H1 corpus) | After the pilot. |
| Host | WSL on F:. Subagents: DeepSeek V4.1 Flash 1017 (max); Codex for reviews when reachable. |

Needs you: pilot corpus review with your advisor; pre-registration decisions.

Next: review fixes → freeze + sign F4 → archive guest state → install F4 → G1 → route probe → direct-runtime probe → guest EVQ → pilot freeze (budgets + reviewed manifest) → pilot → H1.
