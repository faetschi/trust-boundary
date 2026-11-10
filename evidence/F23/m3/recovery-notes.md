# M3 F23 rehearsal: recovery 1 (host shutdown)

- Cause: user-initiated host shutdown 2026-11-08 21:02 local (Windows events 1074/6006); host booted 2026-11-09 15:17. Not a provider outage, not a code defect.
- Host runner pid 38160 died with it. Journal had 18 done, seq 55 begin + seq 56 started for `PILOT-ADV-B-03|adaptive-replay|P|0` (index 18, ~20:52), no terminal.
- Guest (Hyper-V TBound-Ubuntu-2404-ABI8, MAC 00-15-5D-0C-40-02): VM running, guest uptime 3 d (state kept), service+socket active, pilot state=started. DHCP address changed 172.26.25.5 -> 172.25.64.14 (run.json `host` updated; plan_digest/run_binding unchanged, verified with `tbound-run plan`).
- Guest request journal (diag/recover-1/requests.jsonl): 22 accepted, 22 terminal, 1 abandoned (the earlier dr). The unit-18 request tbr-6572bb00d60c9d8520299ed26e744c05 is terminal status=completed (run production-run-506d64545a5c09d8f4f23481), its workflow terminal, owner scope drained. So nothing to abandon on the guest; only the host lost the result.
- Settlement: `tbound-run run` closed seq 57 `abandoned` (rule: begun in an earlier process, never finished, not retried). Unit 18 therefore counts as UNKNOWN(host interruption) with the guest receipt retained; rerun decision at the end (registered rerun rule is for provider outages; this is a host interruption).
- Resumed: pid 10068 (runs/chunk-02.txt), 2026-11-09 ~16:35 local. Journal backup: diag/recover-1/journal-before-resume.jsonl (sha256 4cb2a462...). run.json before: diag/recover-1/run.json.before.
- Recovery effort: about 25 min of agent time, no code change, no new freeze, no guest write (read-only collect + one host config edit).

## UNKNOWN log (cause classes; provider outages reported separately)
| # | step | request | cause class | detail |
|---|---|---|---|---|
| 1 | ADV-B-03 adaptive-replay P (unit 18) | tbr-6572bb00... | host interruption (not an outage) | host shutdown; guest request completed, host lost result |
| 2 | ADV-A-07 adaptive-replay P (unit 23) | tbr-52db2a08... | pre-admission launch race (code, F19/F21 family) | `D09 worker cell exited before launch admission ... open /proc/329541/uid_map: no such file or directory`, phases_ms podman_started=2631 pidfd_pinned=2710 proof_read=3159; diag/unit23 |
| 3 | ADV-A-02 trajectory C (unit 30) | tbr-7ca9364d... | parallel tool call refused (decision 13, not an outage) | `capture OpenRouter response: tool-call index must be zero`; diag/auto-20261109T170915Z/journal-1649.txt |

Mechanics: abandon stops the guest service; `tbound-op svc start` is needed before the runner's trial reset check passes ("registered cell cannot be resolved" otherwise). `recover.sh` (detached) automates abandon+svc start+restart when the guest preview is clean; log in recover.log. Observation: EFF-PROC-01=lost on every step of cases A-07, B-02 (partly), A-02: to analyse in the extraction.
