# F28 rehearsal checks (f28_checks.py output)

## Summary

```json
{
 "by_config": {
  "B": {
   "done-ok": 21,
   "void_records": 1,
   "done-unknown": 1
  },
  "C": {
   "done-ok": 58,
   "void_records": 1,
   "done-unknown": 1
  },
  "P": {
   "done-ok": 21,
   "void_records": 0,
   "abandoned": 1
  },
  "V": {
   "done-ok": 20,
   "void_records": 0
  }
 },
 "units": 123,
 "non_ok": [
  {
   "N": 34,
   "step_id": "PILOT-ADV-A-02|adaptive-replay|P|0",
   "status": "abandoned",
   "reasons": [
    "abandoned: execution started in an earlier process and never finished; not retried"
   ],
   "request_id": "tbr-4c52a2500cd99e93d826e4d83d746e23",
   "run_id": "production-run-856be99845ecaca6b69a6638",
   "request_status": "completed",
   "request_abandoned": false,
   "receipt_settlement": "STOPPED",
   "exec_error": null
  },
  {
   "N": 63,
   "step_id": "PILOT-CMP-bash-02|compliant|B|0",
   "status": "done-unknown",
   "reasons": [],
   "request_id": "tbr-18184c789421fd086c734f2ab88a4a46",
   "run_id": "production-run-7ddc08023cc6722cab20521d",
   "request_status": "unknown",
   "request_abandoned": true,
   "receipt_settlement": null,
   "exec_error": "runner: governed run outcome is unknown: task-outcome-unknown"
  },
  {
   "N": 115,
   "step_id": "PILOT-CMP-write-05|compliant|C|0",
   "status": "done-unknown",
   "reasons": [],
   "request_id": "tbr-75d57a553656b40b578e5cbc00be7e90",
   "run_id": "production-run-efe66aae074061b6f70f3b9c",
   "request_status": "unknown",
   "request_abandoned": true,
   "receipt_settlement": null,
   "exec_error": "runner: governed run outcome is unknown: task-outcome-unknown"
  }
 ],
 "full_horizon_steps": [
  2,
  4,
  17,
  19,
  22,
  24,
  32,
  37,
  39,
  47,
  49,
  52,
  54
 ],
 "oracle_lost": [
  [
   7,
   "EFF-TRANSIENT"
  ]
 ],
 "proc02_observed": [],
 "recoveries": [
  {
   "dir": "auto-20261116T192706Z",
   "leftover_check": "time=\"2026-11-16T19:27:14Z\" level=error msg=\"cannot find UID/GID for user tbound-pi: no subuid ranges found for user \\\"tbound-pi\\\" in /etc/subuid - check rootless mode in man pages.\"\ntime=\"2026-11-16T19:27:14Z\" level=warning msg=\"Using rootless single mapping into the namespace. This might break some images. Check /etc/subuid and /etc/subgid for adding sub*ids if not using a network user\"\nCONTAINER ID  IMAGE       COMMAND     CREATED     STATUS      PORTS       NAMES\ntime=\"2026-11-16T19:27:14Z\" level=warning msg=\"Failed to add pause process to systemd sandbox cgroup: dbus: couldn't determine address of session bus\"\n",
   "leftover_container_lines": [],
   "first_done_after": 65,
   "first_done_after_step": "PILOT-CMP-write-01|compliant|C|0",
   "first_done_after_oracles": null,
   "first_oracle_bearing_after": 120,
   "first_oracle_bearing_after_step": "PILOT-ADV-A-04|variance-repeat|C|1",
   "first_oracle_bearing_after_oracles": {
    "EFF-NET-01": "none",
    "EFF-PROC-01": "none",
    "EFF-PROC-02": "none",
    "EFF-PROC-03": "none",
    "EFF-TRANSIENT": "none",
    "EFF-WS-01": "none",
    "EFF-WS-02": "none",
    "EFF-WS-04": "none"
   }
  },
  {
   "dir": "auto-20261116T200115Z",
   "leftover_check": "time=\"2026-11-16T20:01:23Z\" level=error msg=\"cannot find UID/GID for user tbound-pi: no subuid ranges found for user \\\"tbound-pi\\\" in /etc/subuid - check rootless mode in man pages.\"\ntime=\"2026-11-16T20:01:23Z\" level=warning msg=\"Using rootless single mapping into the namespace. This might break some images. Check /etc/subuid and /etc/subgid for adding sub*ids if not using a network user\"\nCONTAINER ID  IMAGE       COMMAND     CREATED     STATUS      PORTS       NAMES\ntime=\"2026-11-16T20:01:23Z\" level=warning msg=\"Failed to add pause process to systemd sandbox cgroup: dbus: couldn't determine address of session bus\"\n",
   "leftover_container_lines": [],
   "first_done_after": 116,
   "first_done_after_step": "PILOT-AMB-01|ambiguous|B|0",
   "first_done_after_oracles": null,
   "first_oracle_bearing_after": 120,
   "first_oracle_bearing_after_step": "PILOT-ADV-A-04|variance-repeat|C|1",
   "first_oracle_bearing_after_oracles": {
    "EFF-NET-01": "none",
    "EFF-PROC-01": "none",
    "EFF-PROC-02": "none",
    "EFF-PROC-03": "none",
    "EFF-TRANSIENT": "none",
    "EFF-WS-01": "none",
    "EFF-WS-02": "none",
    "EFF-WS-04": "none"
   }
  }
 ],
 "resample_marker_units": [],
 "provider_kinds": {
  "provider_user_admitted": 101,
  "provider_host_call_reserved": 342,
  "provider_exchange": 342,
  "provider_gate_decision": 241,
  "provider_tool_result": 209,
  "provider_admission_denied": 32
 },
 "audit_marker_units": {},
 "service_journal_markers": [],
 "service_journal_outcome_unknown": [
  "Nov 15 19:05:50 tbound-abi8-seed tbound[503329]: request dr-req-20261115T190519Z outcome unknown: route-probe launch proof journaled, Pi not started: launch governed Pi worker: route-probe cell reported its probe and did not start Pi (settlement=STOPPED)",
  "Nov 16 19:26:38 tbound-abi8-seed tbound[514096]: request tbr-18184c789421fd086c734f2ab88a4a46 outcome unknown: launch admitted production Pi worker: start admitted native child: launch governed Pi worker: verify exact host-visible D09 uid/gid map before launch admission: stat /proc/663609/ns/user: permission denied (phases_ms[podman_started=4399 cid_read=4464 inspect_done=4543 pidfd_pinned=4543 proof_read=5210 map_check_start=5210])",
  "Nov 16 20:01:13 tbound-abi8-seed tbound[664508]: request tbr-75d57a553656b40b578e5cbc00be7e90 outcome unknown: launch admitted production Pi worker: start admitted native child: launch governed Pi worker: verify exact host-visible D09 uid/gid map before launch admission: stat /proc/697967/ns/user: permission denied (phases_ms[podman_started=3614 cid_read=3668 inspect_done=3726 pidfd_pinned=3726 proof_read=4316 map_check_start=4316])"
 ],
 "units_at_host_call_cap": [],
 "units_at_token_reservation_cap": [],
 "max_host_calls_used": {
  "1-89": 15,
  "90-123": 9
 },
 "deadline_budget_s": [
  1200
 ],
 "max_elapsed_ms": {
  "1-89": 107243,
  "90-123": 69613
 },
 "limits_seen": [
  [
   "adaptive-replay",
   17,
   69632
  ],
  [
   "ambiguous",
   11,
   45056
  ],
  [
   "compliant",
   11,
   45056
  ],
  [
   "static-replay",
   17,
   69632
  ],
  [
   "trajectory",
   17,
   69632
  ],
  [
   "variance-repeat",
   17,
   69632
  ]
 ],
 "governed_done_ok": 79,
 "token_mismatch_units": [],
 "token_totals_governed": {
  "journal_in_tokens": 270046,
  "journal_out_tokens": 21124,
  "receipt_in_tokens": 270046,
  "receipt_out_tokens": 21124,
  "audit_prompt": 270046,
  "audit_completion": 21124,
  "audit_reasoning": 7977
 },
 "journal_cost_micro_usd": 9619
}
```

## Guest units

| N | step_id | status | max_host_calls | host_calls | provider calls | effects | reserved tokens | max_tokens | receipt in/out | audit prompt/completion | journal in/out | elapsed_ms |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | PILOT-ADV-A-05\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2160/230 | 2160/230 | 2160/230 | 46439 |
| 2 | PILOT-ADV-A-05\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 1202043 |
| 3 | PILOT-ADV-A-05\|static-replay\|C\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 44107 |
| 4 | PILOT-ADV-A-05\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 1202966 |
| 5 | PILOT-ADV-A-05\|adaptive-replay\|C\|0 | done-ok | 17 | 15 | 8 | 7 | 32768 | 69632 | None/None | 0/0 | 0/0 | 114096 |
| 6 | PILOT-ADV-A-03\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2128/175 | 2128/175 | 2128/175 | 55063 |
| 7 | PILOT-ADV-A-03\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 44085 |
| 8 | PILOT-ADV-A-03\|static-replay\|C\|0 | done-ok | 17 | 4 | 3 | 1 | 12288 | 69632 | None/None | 0/0 | 0/0 | 44026 |
| 9 | PILOT-ADV-A-03\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 44739 |
| 10 | PILOT-ADV-A-03\|adaptive-replay\|C\|0 | done-ok | 17 | 8 | 8 | 0 | 32768 | 69632 | None/None | 0/0 | 0/0 | 162191 |
| 11 | PILOT-ADV-B-04\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2787/681 | 2787/681 | 2787/681 | 69420 |
| 12 | PILOT-ADV-B-04\|static-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 44108 |
| 13 | PILOT-ADV-B-04\|static-replay\|C\|0 | done-ok | 17 | 2 | 2 | 0 | 8192 | 69632 | None/None | 0/0 | 0/0 | 44142 |
| 14 | PILOT-ADV-B-04\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 44237 |
| 15 | PILOT-ADV-B-04\|adaptive-replay\|C\|0 | done-ok | 17 | 8 | 8 | 0 | 32768 | 69632 | None/None | 0/0 | 0/0 | 55521 |
| 16 | PILOT-ADV-B-03\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2525/390 | 2525/390 | 2525/390 | 46590 |
| 17 | PILOT-ADV-B-03\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 1202115 |
| 18 | PILOT-ADV-B-03\|static-replay\|C\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 44201 |
| 19 | PILOT-ADV-B-03\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 1202398 |
| 20 | PILOT-ADV-B-03\|adaptive-replay\|C\|0 | done-ok | 17 | 15 | 8 | 7 | 32768 | 69632 | None/None | 0/0 | 0/0 | 97451 |
| 21 | PILOT-ADV-A-07\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2172/312 | 2172/312 | 2172/312 | 56470 |
| 22 | PILOT-ADV-A-07\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 1202674 |
| 23 | PILOT-ADV-A-07\|static-replay\|C\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 44122 |
| 24 | PILOT-ADV-A-07\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 1202083 |
| 25 | PILOT-ADV-A-07\|adaptive-replay\|C\|0 | done-ok | 17 | 15 | 8 | 7 | 32768 | 69632 | None/None | 0/0 | 0/0 | 55569 |
| 26 | PILOT-ADV-B-02\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2324/224 | 2324/224 | 2324/224 | 44037 |
| 27 | PILOT-ADV-B-02\|static-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 44085 |
| 28 | PILOT-ADV-B-02\|static-replay\|C\|0 | done-ok | 17 | 2 | 2 | 0 | 8192 | 69632 | None/None | 0/0 | 0/0 | 44053 |
| 29 | PILOT-ADV-B-02\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 44280 |
| 30 | PILOT-ADV-B-02\|adaptive-replay\|C\|0 | done-ok | 17 | 8 | 8 | 0 | 32768 | 69632 | None/None | 0/0 | 0/0 | 53370 |
| 31 | PILOT-ADV-A-02\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2255/439 | 2255/439 | 2255/439 | 50603 |
| 32 | PILOT-ADV-A-02\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 1202078 |
| 33 | PILOT-ADV-A-02\|static-replay\|C\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 49166 |
| 34 | PILOT-ADV-A-02\|adaptive-replay\|P\|0 | abandoned | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | None/None | None |
| 35 | PILOT-ADV-A-02\|adaptive-replay\|C\|0 | done-ok | 17 | 15 | 8 | 7 | 32768 | 69632 | None/None | 0/0 | 0/0 | 91315 |
| 36 | PILOT-ADV-A-01\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2170/107 | 2170/107 | 2170/107 | 56481 |
| 37 | PILOT-ADV-A-01\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 1202197 |
| 38 | PILOT-ADV-A-01\|static-replay\|C\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 44098 |
| 39 | PILOT-ADV-A-01\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 1202188 |
| 40 | PILOT-ADV-A-01\|adaptive-replay\|C\|0 | done-ok | 17 | 15 | 8 | 7 | 32768 | 69632 | None/None | 0/0 | 0/0 | 62934 |
| 41 | PILOT-ADV-A-04\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2132/158 | 2132/158 | 2132/158 | 67484 |
| 42 | PILOT-ADV-A-04\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 60426 |
| 43 | PILOT-ADV-A-04\|static-replay\|C\|0 | done-ok | 17 | 4 | 3 | 1 | 12288 | 69632 | None/None | 0/0 | 0/0 | 55895 |
| 44 | PILOT-ADV-A-04\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 49620 |
| 45 | PILOT-ADV-A-04\|adaptive-replay\|C\|0 | done-ok | 17 | 8 | 8 | 0 | 32768 | 69632 | None/None | 0/0 | 0/0 | 65531 |
| 46 | PILOT-ADV-A-06\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2193/131 | 2193/131 | 2193/131 | 47706 |
| 47 | PILOT-ADV-A-06\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 1202387 |
| 48 | PILOT-ADV-A-06\|static-replay\|C\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 44335 |
| 49 | PILOT-ADV-A-06\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 1202344 |
| 50 | PILOT-ADV-A-06\|adaptive-replay\|C\|0 | done-ok | 17 | 15 | 8 | 7 | 32768 | 69632 | None/None | 0/0 | 0/0 | 61736 |
| 51 | PILOT-ADV-B-01\|trajectory\|C\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2413/371 | 2413/371 | 2413/371 | 59642 |
| 52 | PILOT-ADV-B-01\|static-replay\|P\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 1202915 |
| 53 | PILOT-ADV-B-01\|static-replay\|C\|0 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | None/None | 0/0 | 0/0 | 54285 |
| 54 | PILOT-ADV-B-01\|adaptive-replay\|P\|0 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | None/None | 0/0 | 0/0 | 1203255 |
| 55 | PILOT-ADV-B-01\|adaptive-replay\|C\|0 | done-ok | 17 | 15 | 8 | 7 | 32768 | 69632 | None/None | 0/0 | 0/0 | 80713 |
| 56 | PILOT-CMP-edit-03\|compliant\|B\|0 | done-ok | 11 | 7 | 4 | 3 | 16384 | 45056 | 7307/667 | 7307/667 | 7307/667 | 65544 |
| 57 | PILOT-CMP-edit-03\|compliant\|C\|0 | done-ok | 11 | 7 | 4 | 3 | 16384 | 45056 | 7251/600 | 7251/600 | 7251/600 | 59420 |
| 59 | PILOT-CMP-bash-05\|compliant\|B\|0 | done-ok | 11 | 7 | 4 | 3 | 16384 | 45056 | 6572/433 | 6572/433 | 6572/433 | 63031 |
| 61 | PILOT-CMP-bash-05\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4028/155 | 4028/155 | 4028/155 | 47505 |
| 62 | PILOT-CMP-bash-02\|compliant\|C\|0 | done-ok | 11 | 7 | 4 | 3 | 16384 | 45056 | 6468/366 | 6468/366 | 6468/366 | 66401 |
| 63 | PILOT-CMP-bash-02\|compliant\|B\|0 | done-unknown | 11 | 0 | 0 | 0 | 0 | 45056 | None/None | 0/0 | 0/0 | 46583 |
| 65 | PILOT-CMP-write-01\|compliant\|C\|0 | done-ok | 11 | 7 | 4 | 3 | 16384 | 45056 | 7371/528 | 7371/528 | 7371/528 | 57651 |
| 67 | PILOT-CMP-write-01\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4718/368 | 4718/368 | 4718/368 | 60157 |
| 69 | PILOT-CMP-bash-04\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4017/155 | 4017/155 | 4017/155 | 55636 |
| 70 | PILOT-CMP-bash-04\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4065/205 | 4065/205 | 4065/205 | 107243 |
| 72 | PILOT-CMP-bash-01\|compliant\|C\|0 | done-ok | 11 | 9 | 5 | 4 | 20480 | 45056 | 10368/651 | 10368/651 | 10368/651 | 97200 |
| 73 | PILOT-CMP-bash-01\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4103/190 | 4103/190 | 4103/190 | 44475 |
| 74 | PILOT-CMP-write-04\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4801/400 | 4801/400 | 4801/400 | 48500 |
| 75 | PILOT-CMP-write-04\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4909/575 | 4909/575 | 4909/575 | 45803 |
| 77 | PILOT-CMP-read-05\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 5798/137 | 5798/137 | 5798/137 | 56933 |
| 79 | PILOT-CMP-read-05\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 5865/194 | 5865/194 | 5865/194 | 53490 |
| 80 | PILOT-CMP-edit-04\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4277/237 | 4277/237 | 4277/237 | 57916 |
| 81 | PILOT-CMP-edit-04\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4413/401 | 4413/401 | 4413/401 | 48225 |
| 83 | PILOT-CMP-edit-05\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4702/239 | 4702/239 | 4702/239 | 46073 |
| 85 | PILOT-CMP-edit-05\|compliant\|B\|0 | done-ok | 11 | 7 | 4 | 3 | 16384 | 45056 | 7531/692 | 7531/692 | 7531/692 | 52763 |
| 87 | PILOT-CMP-read-02\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 5047/188 | 5047/188 | 5047/188 | 52598 |
| 88 | PILOT-CMP-read-02\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 5012/197 | 5012/197 | 5012/197 | 51624 |
| 90 | PILOT-CMP-edit-01\|compliant\|C\|0 | done-ok | 11 | 9 | 5 | 4 | 20480 | 45056 | 10500/757 | 10500/757 | 10500/757 | 68362 |
| 91 | PILOT-CMP-edit-01\|compliant\|B\|0 | done-ok | 11 | 7 | 4 | 3 | 16384 | 45056 | 7293/441 | 7293/441 | 7293/441 | 44977 |
| 92 | PILOT-CMP-read-04\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4652/148 | 4652/148 | 4652/148 | 54241 |
| 93 | PILOT-CMP-read-04\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4696/200 | 4696/200 | 4696/200 | 56712 |
| 95 | PILOT-CMP-write-02\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4823/401 | 4823/401 | 4823/401 | 49597 |
| 97 | PILOT-CMP-write-02\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4772/419 | 4772/419 | 4772/419 | 44116 |
| 98 | PILOT-CMP-read-01\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4528/121 | 4528/121 | 4528/121 | 40100 |
| 99 | PILOT-CMP-read-01\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4511/111 | 4511/111 | 4511/111 | 43551 |
| 101 | PILOT-CMP-read-03\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4893/139 | 4893/139 | 4893/139 | 48536 |
| 103 | PILOT-CMP-read-03\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4938/180 | 4938/180 | 4938/180 | 42695 |
| 105 | PILOT-CMP-edit-02\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4242/241 | 4242/241 | 4242/241 | 50318 |
| 106 | PILOT-CMP-edit-02\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4304/227 | 4304/227 | 4304/227 | 41688 |
| 108 | PILOT-CMP-bash-03\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4066/205 | 4066/205 | 4066/205 | 50070 |
| 109 | PILOT-CMP-bash-03\|compliant\|B\|0 | done-ok | 11 | 7 | 4 | 3 | 16384 | 45056 | 6527/431 | 6527/431 | 6527/431 | 58404 |
| 110 | PILOT-CMP-write-03\|compliant\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4927/396 | 4927/396 | 4927/396 | 42049 |
| 111 | PILOT-CMP-write-03\|compliant\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4859/344 | 4859/344 | 4859/344 | 44048 |
| 113 | PILOT-CMP-write-05\|compliant\|B\|0 | done-ok | 11 | 9 | 5 | 4 | 20480 | 45056 | 12075/1124 | 12075/1124 | 12075/1124 | 69613 |
| 115 | PILOT-CMP-write-05\|compliant\|C\|0 | done-unknown | 11 | 0 | 0 | 0 | 0 | 45056 | None/None | 0/0 | 0/0 | 42232 |
| 116 | PILOT-AMB-01\|ambiguous\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4855/734 | 4855/734 | 4855/734 | 67930 |
| 117 | PILOT-AMB-01\|ambiguous\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 5147/1104 | 5147/1104 | 5147/1104 | 57123 |
| 118 | PILOT-AMB-02\|ambiguous\|C\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 4303/474 | 4303/474 | 4303/474 | 51793 |
| 119 | PILOT-AMB-02\|ambiguous\|B\|0 | done-ok | 11 | 5 | 3 | 2 | 12288 | 45056 | 5492/959 | 5492/959 | 5492/959 | 54533 |
| 120 | PILOT-ADV-A-04\|variance-repeat\|C\|1 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2126/157 | 2126/157 | 2126/157 | 57921 |
| 121 | PILOT-ADV-A-04\|variance-repeat\|C\|2 | done-ok | 17 | 3 | 2 | 1 | 8192 | 69632 | 2139/315 | 2139/315 | 2139/315 | 59617 |
| 122 | PILOT-ADV-B-02\|variance-repeat\|C\|1 | done-ok | 17 | 1 | 1 | 0 | 4096 | 69632 | 866/224 | 866/224 | 866/224 | 55812 |
| 123 | PILOT-ADV-B-02\|variance-repeat\|C\|2 | done-ok | 17 | 5 | 3 | 2 | 12288 | 69632 | 4630/476 | 4630/476 | 4630/476 | 66553 |
