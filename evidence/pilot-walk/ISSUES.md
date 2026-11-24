# Issue log by layer (pilot walk, research pilot, D60 rehearsal)

Owner request 2026-11-24 (D60): one log across the walk, the research pilot and the rehearsal, so repeated findings can be read by layer. "Round" names where an issue was found. Status: fixed (with the fix), ruled (owner reading or orchestrator ruling), open, or limit (accepted and disclosed).

## TBound runtime (F32 binaries: supervisor, runner, profile, service)

None found so far. Round 1: 11 of 11 static P units done, no evidence loss, no exec error, every registered effect observed; the screening corpus-check passed all 11 (measured, 2026-11-23/24).

## Case-building tools (corpus/sampling: adapt.py, case_entry.py, carrier.py, round_plan.py, select.py)

| # | Round | Issue | Status |
|---|---|---|---|
| T1 | prep (2026-11-22) | No pilot round planner (`round_plan.py` refused pilot outputs) | fixed, PR #267 |
| T2 | prep | T5 filler ids would collide from round 3 on (admission once per freeze); `--id-suffix` would rename drawn cases | fixed, `carrier.py --base-id-suffix`, PR #267 |
| T3 | round 1 prepare (aseval:0497) | Step 6 records missing: shared preamble not recorded by RedCode/SABER/ASEval/ASB; language swap not recorded by SABER/ASEval; ASEval lost both step 6 lost lines | fixed, PR #280 (`done()` records once) |
| T4 | review of #280 | SABER `init_commands` `.py` rename unrecorded; SABER path line claimed a swap | fixed, PR #280 second commit |
| T5 | review of #280 | InjecAgent, AgentDojo and ASB renderers never apply the Python swap | open (latent): an adapter that meets the word Python there stops (brief section 6) |
| T6 | round 1 prepare (InjecAgent) | No adapter key to add a `lost_distinctions` line (email channel replaced by an HTTP POST) | ruled: the change is recorded under transformations (step 6) and `tool_mapping` (step 7) |
| T7 | round 2 prepare (injecagent:ds:0022:u08 report) | The case tools write absolute local paths into render.log | open: tokenise the paths when the combined render log is built at walk end |

## Adaptation rule (adaptation-rule-v1.md)

| # | Round | Issue | Status |
|---|---|---|---|
| R1 | round 1 prepare (agentdojo:workspace:UserTask38:InjectionTask5) | A WS effect on an artifact only the attack's own send creates has no pre-trial fixture | ruled: owner reading (c), F-FIXTURE at step 5 (2026-11-23, ADAPT.md); register line suggested before T2 |
| R2 | round 1 prepare (redcode:index26:r27) | A fetch-only GET with `EFF-NET-01` carries no canary | ruled: owner reading (a), F-FIXTURE at step 5, D51 item 20 logic (2026-11-23, ADAPT.md); register line suggested before T2 |

## Host and ops (Windows host, WSL, guest, agent platform)

| # | Round | Issue | Status |
|---|---|---|---|
| H1 | round 1 pre-run | Windows clock unsynced (w32time on Local CMOS Clock), host 1.09 s behind NTP, guest-host offset +1.06 s | fixed: w32time to time.windows.com with UAC; offset +0.03 s |
| H2 | round 1 prepare | Windows Python with `-I` writes cp1252 to a pipe; a CJK source record cannot print | limit: the adapter adds `-X utf8` (mechanical, recorded in attempts) |
| H3 | round 1 prepare (UT38) | A safety classifier halted an adapter while it wrote an exfiltration-and-delete script | limit: no retry needed under reading (c); a repeat would make the owner the adapter for that case (rule section 1 allows a person) |
| H4 | walk (several) | Background job completion notices lost on Claude session restarts | limit: state is checked from files on every wake; WALK.md is the source of truth |
| H5 | walk | Guest IP changed since F32 (172.29.57.244) | routine: looked up by MAC |

## Orchestration (the walk's own scripts and procedure)

| # | Round | Issue | Status |
|---|---|---|---|
| O1 | prep (2026-11-23) | Native Windows Python read `/f/x` as `F:\f\x`; cleanup removed the parent `F:\f` without checking it first | fixed: `cygpath -m` for native tools, delete only exact created paths; owner told |
| O2 | round 1 prepare | Adapters ran `git status` / `git log` in the repo against the brief and saw tooling commit subjects | fixed: adapters get a plain no-git export (`adapter-repo`, `adapter-repo2`); orchestrator ruled nothing forbidden was seen |
| O3 | round 1 decide | `local-report.sh` did not pass `--other-manifest`, so `root-disjoint` was not-checked (rule 5.1 must pass, section 4 against T5) | fixed: helper passes the T5 manifest; local report 2 rerun for all 8 cases, all pass |
| O4 | round 1 decide | Screening provenance held an absolute WSL path that would enter the public evidence record | fixed: per-case extracts tokenise it (`<screening_dir>/`) |
| O5 | round 1 decide (agentdojo UT21) | The no-git adapter export sits inside the `F:	rust-boundary` checkout, so `git log` walked up to the main branch (F32 + corpus commits, no walk data) | fixed: an invalid `.git` file in each export makes git fail there; orchestrator ruled nothing forbidden was seen |
| O6 | round 2 prepare (injecagent:ds:0022:u08) | Adapter wrote one helper script in its own session scratchpad, outside work_dir (it wrote only evidence.draft.json into work_dir) | ruled: no rule data read or changed outside the case; accepted |
| O7 | walk PR (2026-11-24) | The carrier commits of rounds 1 and 2 were pushed without running the corpus/sampling suite; `test_dev_ids` RealInputs (a snapshot of the real carriers) then failed, because `dev_ids.py` correctly adds the walk's 15 screened ledger ids as development cases | fixed in the walk PR: the test's expected set names the walk's carriers (flagged for the owner, AGENTS.md); run the suite before every carrier commit |

## V wiring (D61: plan field and V schedule, research route, key out of the container, ledger booking, per-request provider time, V route probe)

Not built yet. One row per finding once the V wiring exists (F33 qualification, research pilot, D60 rehearsal).

| # | Round | Issue | Status |
|---|---|---|---|

## Owner decisions (plan notes)

| # | Raised | Decision needed | Deadline |
|---|---|---|---|
| D-V | 2026-11-24, RQ4 side thread | V back in RQ4 | DECIDED 2026-11-24 (owner): V returns (D61, thesis PR #56; F33 changes in trust-boundary PR #281). V->B->C on the 100 compliant tasks, research route, key outside V's container, usdledger booking, six balanced orders, plan-digest off switch. Go/no-go on V spend after the research pilot. F33 is not qualified until #56 is merged and the D61 changes landed with review + full gate |
