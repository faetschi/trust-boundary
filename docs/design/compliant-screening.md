# B-only screening of the compliant tasks (design for review)

Status: design for review, 2026-11-23. Nothing here is built yet. Register: thesis D58 item 4, D41 as amended by D58, the D58 amendment (V leaves the thesis). The build is F33, the freeze H1 uses.

## What the register asks

Before `T2`, each of the 100 compliant tasks runs once in Configuration B, on the research route, with the pinned model and on the H1 build. A task is admitted only when its B run completes and passes its acceptance test. Every other end is a failure with its reason kept: failed acceptance, an `UNKNOWN` cause, or a budget end. A failed task is replaced by the next unused eligible item of the same source and tool group, in the source's own item order. The replacement must pass the adaptation rule, the construct checks and the acceptance witnesses, and it is then screened once in the same way. A failed item is never used again. A group that runs out of items is a compliant shortfall and blocks the corpus freeze. Screening runs are development evidence: their calls count against the research budget, and they never enter H1, RQ4 or the policy. The registration lists every screened task with its result and the reason for each replacement. The task-selection script is development tooling at a pinned commit.

## The constraint that shapes the design

The guest admits each `<case_id>/<role>/<attempt>` once per freeze (`corpusadmit.AdmissionID`). H1 runs Configuration B of every compliant task as role `b-compliant`, attempt 1. If screening used the confirmatory case ids, it would consume those admission ids on F33, and H1 could not run B for the same tasks. So screening runs each task under a fresh id, `<case_id>.s<N>` for screening round N. This is the precedent of the research pilot's `.rp<N>` ids (`corpus/sampling/research_pilot.py`, `carrier.rename_ids`). The screening manifest builder checks that each renamed case equals its base case in everything except the id, so the screened task is the confirmatory task.

## Components

Four parts. The first three are in the F33 build. The fourth is development tooling.

1. **Guest: the `screening` quota profile** (`supervisor/cmd/tbound`, `internal/piruntime`, `internal/evalcorpus`).
   - New profile value `screening`, beside `thesis` and `pilot` (`managed_live_root_linux.go:95-96`). The service accepts it only on the research route. On the engineering route it refuses to start, as it refuses a research pilot without a draw pin (`production_corpus_admission_linux.go:210-219`).
   - Its quotas (`evalcorpus`): compliant cases only, at most 25 per tool (read, write, edit, bash), at least one case. No adversarial or ambiguous case, and no draw or attacker pin.
   - Role gate (`productionCorpus.roleAllowed`): under `screening`, a case may run `b-compliant` only. The `compliant` role, which is Configuration C, is refused. This is the guest-side guarantee for D41: no C outcome exists for a screened task. It has the same form as the `pOnly` mark for TM-C fixtures of a pilot corpus (`:80`, `:113`).
   - Ceilings are the compliant ceilings of the thesis config.
2. **Runner: the `compliant-screening` purpose** (`internal/runner`, `cmd/tbound-run`).
   - `runner.PurposeScreening`, beside `PurposeConstructWitness` (`plan.go:36-45`). Its plan has one step per compliant case: Configuration B, role compliant, in the seeded `order/compliant` rank order. It refuses a manifest with any non-compliant case.
   - Like a construct-witness run, it is non-confirmatory. Export, report and calibration summary return `ErrNonConfirmatoryPurpose`. `tbound-run` requires quota profile `screening` for it and skips the registered-total check (`corpus.go:96-101`).
   - The plan digest binds the purpose, so a screening plan can never pass for a study plan.
   - No rerun. D58 item 4 counts every end other than an accepted completion as a failure, so a screening step has no rerun scope (`export.go:28`).
3. **Screening report: `tbound-run screening-report --config run.json`** (`cmd/tbound-run`).
   - The command reads the round's journal and prints one JSON row per task: the screening id, the base case id, the tool group, and the end. The end is `admitted`, `failed-acceptance`, `unknown:<cause>`, `budget-end`, or `not-run`.
   - The command also prints the digest of its output. The row set is what the registration lists. A step that never ran is `not-run`, never a pass.
4. **Task-selection script: `corpus/sampling/screening.py`** (development tooling at a pinned commit, not an H1 input).
   - `select`: given the selection record of the compliant set (`corpus/third/compliant/sources.json`, `selection`) and the screening ledger, it names the next round's items. Round 1 is the 100 tasks of the compliant set. Each later round names, for each failed task, the next unused eligible item of its source in the source's own item order. It stops with a named shortfall when a group runs out. It never names an item that is screened, failed, in the pilot, or recorded as unbuildable.
   - `build`: turns the built cases of a round into the round's manifest with the `.s<N>` ids, and checks each against its base case.
   - `record`: appends the round's `screening-report` rows to the screening ledger. The ledger is append-only, and a failed item stays failed.
   - The rule this script implements is committed before the first screening run (D58 item 4). It does not read or write any file bound by the `h1-sampling-frame` tag, or `corpus/third/` (it only reads the selection record).

Building a replacement case is corpus work in lane C: adaptation, construct checks and acceptance witnesses, then a corpus-only commit on top of the build (D39). The script only names the item.

## Flow of one round

```
screening.py select -> lane C builds the named items (round 2 on) -> screening.py build (.sN ids)
  -> install the round manifest on the guest (research route, profile screening)
  -> tbound-run run (purpose compliant-screening) -> tbound-run screening-report
  -> screening.py record -> next round, until no task failed or a group ran out
```

## Spend

Each screening step is one B run on the research route. The service books each call in the research USD ledger, as every research call is (D58 item 3). The research pilot measures the cost per unit before the first screening round.

## Questions for the reviewer and the owner

1. **Item order of a replacement.** D58 item 4 says "the source's own item order". The compliant set itself was chosen in a hashed order (`sources.json` `selection.rule`: SHA-256 of domain, kind and id, ascending). This design follows the register: the next item in the dataset's own order. The hashed order is the alternative. It is reproducible too, and it keeps the selection rule of the set. Owner call.
2. **Tool groups of HumanEval-JS.** RepoQA feeds `read`. One HumanEval-JS pool feeds `write`, `edit` and `bash`, by rank (`selection.humaneval.roles`). The source has no tool groups, so a replacement for any of the three comes from that one pool. "Same source and group" then means the same source, adapted to the failed task's tool.
3. **A provider outage.** D58 item 4 makes an `UNKNOWN` end a failure, so a task that hit a provider outage is replaced, not screened again. That follows the register, at the cost of losing a good task to a provider fault. The alternative is a register amendment.
4. **Recording.** The registration package carries the screening ledger and every round's report. This design does not change `tbound-profile preregistration-record`. The screening rows are archived beside it.

## Landing order

1. Guest profile, quotas and role gate, with tests: the profile is refused on the engineering route, a non-compliant case is refused, and `compliant` is refused while `b-compliant` is allowed.
2. Runner purpose and `tbound-run` wiring, with a plan test: one B step per compliant task and no other step, with the digest bound to the purpose.
3. `screening-report`, with a journal fixture for each end.
4. `screening.py`, after the review settles questions 1 and 2. It is dev tooling and may land after F33.

Parts 1 to 3 go into F33. Part 4 does not have to.
