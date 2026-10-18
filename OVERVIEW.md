# TBound — current state

Updated 2026-10-18. Branch `codex/tbound-prototype`, freeze candidate `12b5627` (full gate: 43 packages, 3073 pass / 18 skip / 0 fail). Priority: thesis completion. Green results verify code only: G1 false, H1 unexecuted, containment not established. Prototyping uses OpenCode Go only; no OpenRouter calls yet.

| Milestone | Current state |
| --- | --- |
| System (service, recovery, signed profiles) | Code complete and reviewed: drain proofs, `abandon-unknown` only after reconciliation, corpus-manifest admission (`case_id/role/attempt`), symlink-safe key files, bounded xattr authority, stable cgroup digest, reproducible cell image with owner cosign signing. Not yet installed on the guest. |
| Measurement | Oracles: workspace (wsoracle v7, E04 seven-point comparison fed by service-side lifecycle snapshots and a per-path publication ledger), network (netoracle v3, sink on the guest at 127.0.33.1:8080, canaries via evidence channel), process (procoracle v3, cell-uid scan, continuous monitor, registration bound to cell inode). Runner: 1,935-unit plan, Prepare/Execute, crash-safe journal, rerun rule, guest executor over SSH, evidence collectors via a root-only read-only `tbound evidence` command. Every increment adversarially reviewed by Codex; final review of the last three commits running. |
| Experiment | Tools: `tbound-profile corpus-digest`, `preregistration-record`. Pilot corpus draft (21 cases) awaits your review. Nothing run yet. |
| Guest (ABI 8) | Reachable at 172.22.198.27 (pinned key). Node mount and service units installed. Next: your signing session per `infra/guest/PROVISIONING-SIGNING.md` (5 signatures, privileged writes incl. evidence key, client key, netsink unit), then the first E2E smoke over OpenCode Go. |

Stated limits (thesis must say so): bounded xattr claim; PROC-02 misses crossings shorter than the poll gap; one cell per uid; no transparency log for the image signature; E04 cannot see effects made and undone between two lifecycle points; executors and the evidence channel are trusted.

Next: Codex review fixes, freeze + pre-registration record, signing session, smoke run, pilot corpus review, pilot run, thesis corpus, G0/G1.

Engineering: OpenCode Go. Final research (later): OpenRouter DeepSeek V4.1 Flash, US$5 cap, peak $0.30/$1.20 per M tokens.
