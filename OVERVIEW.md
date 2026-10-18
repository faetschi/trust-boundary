# TBound — current state

Updated 2026-10-18. Branch `codex/tbound-prototype`. Last green gate: `08eef85` (42 packages, 3085 pass / 18 skip / 0 fail; Codex FREEZE-OK). Green results verify code only: G1 false, H1 unexecuted, containment not established. Prototyping uses OpenCode Go only; no OpenRouter calls yet.

| Milestone | Current state |
| --- | --- |
| System (service, recovery, signed profiles) | Code complete and reviewed. Not yet installed on the guest. |
| Measurement | Oracles (workspace, network, process) and runner library complete and reviewed. |
| Research enablement (new) | Gap found: the runner could not yet drive a real experiment. In progress per owner-approved design (`../review-artifacts/research-enablement-design-20261018.md`): A fixtures + per-trial reset, C host runner command `tbound-run` (both being implemented now); then B scripted channel-preserving P/C replays + C protected paths, B2 P deltas (net-sink, no-reap; guest spike), D configuration B, E adaptive scripted replay, F configuration V (container), G task oracle, H pilot content. |
| Experiment | Pilot corpus draft (21 cases) approved by owner; needs advisor review, real fixtures/scripts, then pre-registration record. Nothing run yet. |
| Guest (ABI 8) | At 172.30.8.106 (DHCP; pinned key via HostKeyAlias). Signing prep proven reproducible (P1–P3 at `08eef85`, byte-identical). One signing session after research enablement is frozen. |

Stated limits (thesis must say so): bounded xattr claim; PROC-02 misses crossings shorter than the poll gap; one cell per uid; no transparency log for the image signature; E04 cannot see effects made and undone between lifecycle points; executors and the evidence channel are trusted; replays use a scripted provider instead of the model; P deltas widen the P cell; WS-04 limited to metadata changes.

Next: finish research enablement (A–H) → freeze → one signing session + guest spike + E2E smoke → advisor review → pre-registration → pilot → thesis corpus → G0/G1.

Engineering: OpenCode Go. Final research (later): OpenRouter DeepSeek V4.1 Flash, US$5 cap.
