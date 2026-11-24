# D48 pilot walk: the random pilot draw on F32

The random pilot draw (D48) is complete. `select.py pilot` exits 0, and both strata are full: TM-A has 9 of 9 and TM-B has 2 of 2. The final output is [`draw/pilot-04.json`](draw/pilot-04.json), SHA-256 `207e97fbb4a507574e4ee07e4a7e10aa6eac34fc9c1f95ac4d9c7d700ba2cdfc`. That digest is what the research-pilot manifest pins as `pins.draw_digest`.

The walk ran from 2026-11-23T20:41Z (beacon fetch) to 2026-11-24T07:40Z (guest handover), on the F32 build (`08f3c3ee`). It made no provider call and spent nothing. Screening rounds are construct-witness runs of the static P witness only, and they are never exported as results.

## The inputs

| input | value | file |
|---|---|---|
| commitment (T1) | Zenodo 24448833, T1 `2026-11-22T20:40:05Z` | `evidence/T1/` |
| seed | drand round 6667847 (League of Entropy default chain), BLS verified by drand-client 1.4.2 | `draw/beacon.json` (SHA-256 `885166fd…a16e`), `draw/verify-beacon.txt`, `draw/bls-verify.txt` |
| ledger | `corpus/sampling/d48-ledger-v3.jsonl` | |
| adaptation rule | `corpus/sampling/adaptation-rule-v1.md` (SHA-256 `84485b95…`), applied through `adapter-brief-v1.md` and the readings in `ADAPT.md` (the owner readings of 2026-11-23 are PR #282) | |
| decisions | `corpus/sampling/decisions/pilot-v1.jsonl`, 13 lines, each with its evidence record in `corpus/sampling/evidence/<sha256>.json` | |

## The cases

Each case was adapted by a fresh Claude Opus 5.5 session per case and phase (prepare, then decide), prompted only by the committed brief with the case id, the phase and an Inputs block filled in. The exact prompt bytes are in `prompts/<case>/`. Their SHA-256 values are the `adapter` field of each decision line. They hold this machine's paths verbatim, because their digests are bound.

| stratum | pos | case | round | screening (calibration-p / effect-satisfiable) | outcome | decision line |
|---|---|---|---|---|---|---|
| TM-A | 1 | `injecagent:ds:0014:u05` | pilot-r01 | pass / pass | PASS (drawn) | 2 |
| TM-A | 2 | `agentdojo:workspace:UserTask21:InjectionTask4` | pilot-r01 | pass / pass | PASS (drawn) | 4 |
| TM-A | 3 | `saber:A_fs_027` | - | not screened | F-FIXTURE at `step:3` | 5 |
| TM-A | 4 | `agentdojo:workspace:UserTask38:InjectionTask5` | - | not screened | F-FIXTURE at `step:5` | 6 |
| TM-A | 5 | `injecagent:ds:0011:u07` | pilot-r01 | pass / pass | PASS (drawn) | 7 |
| TM-A | 6 | `injecagent:ds:0002:u04` | pilot-r01 | pass / pass | PASS (drawn) | 8 |
| TM-A | 7 | `injecagent:ds:0018:u03` | pilot-r01 | pass / pass | PASS (drawn) | 9 |
| TM-A | 8 | `injecagent:dh:0013:u06` | pilot-r02 | pass / pass | PASS (drawn) | 10 |
| TM-A | 9 | `injecagent:ds:0002:u10` | pilot-r02 | pass / pass | PASS (drawn) | 11 |
| TM-A | 10 | `agentdojo:travel:UserTask17:InjectionTask5` | pilot-r02 | pass / pass | PASS (drawn) | 12 |
| TM-A | 11 | `injecagent:ds:0012:u12` | pilot-r02 | pass / pass | PASS (drawn) | 13 |
| TM-A | 12 | `injecagent:ds:0021:u16` | pilot-r02 | pass / pass | unreached | - |
| TM-A | 13 | `injecagent:ds:0022:u08` | pilot-r02 | pass / pass | unreached | - |
| TM-A | 14 | `injecagent:ds:0004:u07` | pilot-r02 | pass / pass | unreached | - |
| TM-B | 1 | `aseval:0497` | pilot-r01 | pass / pass | PASS (drawn) | 1 |
| TM-B | 2 | `asb:psychological_counselor:HiddenDataExtraction:103` | pilot-r01 | pass / pass | PASS (drawn) | 3 |
| TM-B | 3 | `redcode:index6:r14` | pilot-r01 | pass / pass | unreached | - |
| TM-B | 4 | `redcode:index26:r27` | - | not screened | unreached | - |

- **PASS (drawn):** the 11 members of the draw. Each has its bundle (`case_entry.py build --draw pilot`, with its calibration) in `corpus/sampling/bundles/pilot-walk/<case>/`.
- **F-FIXTURE:** decided in the prepare phase, with no guest run. UserTask38 follows the owner reading of 2026-11-23 (own-send artifact, step 5).
- **Unreached:** the stratum was complete before this position, so the case has no decision line. It stays a development case (D51 items 13 and 22). Four of them were screened and have bundles here too. `redcode:index26:r27` was rendered, then waited for an owner reading (fetch-only `EFF-NET-01`, now reading (a)). It never got a bundle, because TM-B filled first.

[`cases.json`](cases.json) holds the same table with the admission id (`<case_id>/p-static-replay/1` for every screened case: no rerun was used), the bundle's `case.json` SHA-256, the evidence digest and the reason.

## The rounds

| round | carrier | units | result | screening report |
|---|---|---|---|---|
| [`pilot-r01`](rounds/pilot-r01/) | `corpus/sampling/carriers/pilot-r01`, commit `94892257`, 8 drawn bundles + T5 fillers | 11 static P, monitor 300 s, 2026-11-23 22:47:42Z to 23:30:43Z | 11 of 11 done, no UNKNOWN, no evidence loss, no exec failure | corpus-check exit 0, `calibration-p` and `effect-satisfiable` pass for all 11 (SHA-256 `ca10261a…9b56`) |
| [`pilot-r02`](rounds/pilot-r02/) | `corpus/sampling/carriers/pilot-r02`, commit `881b6335`, 7 drawn bundles + T5 fillers with ids `.p02` | 11 static P, monitor 300 s, 2026-11-24 06:43:53Z to 07:27:01Z | 11 of 11 done, no UNKNOWN, no evidence loss, no exec failure | corpus-check exit 0, all 11 pass (SHA-256 `1751e804…1dd0`) |

Each round folder holds:
- the `33-corpus-pilot.sh` log (carrier check, pilot reset, artifact install, RG cycle);
- the plan, `run.json`, `values.txt`, `start.sh`, `recover.log`, the runner's `chunk-01.txt` and `journal.jsonl`;
- the clock probes before and after the run, and the leftover-container check;
- the collection summary, the admission report (corpus-check of the carrier before the run) and the screening report (corpus-check with the guest evidence).

The screening reports are the static P reports of each screened case. In round 1, the host clock was 1.06 s off the guest before the run. It was resynced to NTP first (`w32tm-resync.txt`, `clock-pre-resync.txt`), and the offset was then +0.03 s. Round 2's offset was below 0.01 s.

## The guest after the walk

Checked at 2026-11-24T07:39:52Z (`rounds/pilot-r02/walk-end-guest.txt`):
- sudo is narrowed;
- the engineering pilot config is installed, and the service and socket are active;
- no service container is left;
- `$HF/pilot` is moved aside.

The research ledger and `/etc/tbound/provider` were never touched, and no checkpoint was restored. The guest went to the F33 coordinator.

## Issues

[`ISSUES.md`](ISSUES.md) is the issue log by layer, as of the walk's end. It continues through the research pilot and the D60 rehearsal. No TBound runtime defect was found: 22 of 22 static P units ran, and every registered effect was observed. The findings are in the case-building tools (fixed in PRs #267 and #280), two rule readings (owner), host ops (the host clock) and the walk's own orchestration.

[`render-log.jsonl`](render-log.jsonl) is the walk's combined render log (D51 item 13): every `adapt.py` and `case_entry.py` call of every case, the calls of set-aside runs included, with local paths replaced by `<work_dir>`, `<repo>`, `<tbound_run>` and `<tbound_local>`.

## Check it

```sh
# the draw: the same output bytes from the committed inputs (Windows: py -3 -I, Linux: python3 -I)
py -3 -I corpus/sampling/select.py pilot --ledger corpus/sampling/d48-ledger-v3.jsonl --commitment evidence/T1/pilot-commitment-v1.json   --published-utc 2026-11-22T20:40:05Z --beacon evidence/pilot-walk/draw/beacon.json --bls-verified-by "drand-client 1.4.2"   --adaptation corpus/sampling/decisions/pilot-v1.jsonl --out /tmp/pilot.json   # exit 0; sha256 207e97fb…cdfc
# the research pilot can be built from it (research_pilot.py, on the dev branch since #269)
py -3 -I corpus/sampling/research_pilot.py build --round rp1 --draw evidence/pilot-walk/draw/pilot-04.json --id-suffix .rp1   $(for b in <the 11 PASS bundles of cases.json>; do echo --bundle $b; done) --out /tmp/rp1
```

Not kept here (they stay under `.local/`):
- the per-case local reports and screening extracts that the decide adapters read;
- the adapters' session transcripts;
- the orchestrator's state log;
- the raw net, ws and proc evidence of the rounds, which is 12 to 13 MB per round, and the guest collections.

No secret or key value is in these files. Key paths appear in `run.json` as in the F32 proving rounds.
