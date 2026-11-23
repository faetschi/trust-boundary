# B-only screening of the compliant tasks (design)

Status: design, revised 2026-11-23 after an independent review and the owner's answers. Nothing here is built yet. Register: thesis D58 item 4, D41 as amended by D58, the D58 amendment (V leaves the thesis), and D59 (thesis PR #52: F33, the US$10 cap, the screening decisions below). The build is F33, the freeze H1 uses.

## What the register asks

Before `T2`, each of the 100 compliant tasks runs once in Configuration B, on the research route, with the pinned model and on the H1 build. A task is admitted only when its B run completes and passes its acceptance test. Every other end is a failure with its reason kept: failed acceptance, an `UNKNOWN` cause, or a budget end. A failed task is replaced by the next unused eligible item of the same source and tool group, in the source's own item order. The replacement must pass the adaptation rule, the construct checks and the acceptance witnesses, and it is then screened once in the same way. A failed item is never used again. A group that runs out of items is a compliant shortfall and blocks the corpus freeze. Screening runs are development evidence: their calls count against the research budget, and they never enter H1, RQ4 or the policy. The registration lists every screened task with its result and the reason for each replacement. The task-selection script is development tooling at a pinned commit.

## Owner decisions (2026-11-23, recorded in D59)

1. **Replacement order.** The source's own item order, as D58 item 4 says: the dataset's index order (RepoQA needle order for `read`, HumanEval index order for HumanEval-JS), skipping every item that is in the pilot, already used, screened, failed or recorded as unbuildable.
2. **One HumanEval-JS pool.** `write`, `edit` and `bash` share one HumanEval-JS pool, because the source has no tool groups. Failures are processed in compliant-set rank order; each takes the first unused item of the pool that can be adapted to the failed task's tool, and the replacement is set up for that tool.
3. **No re-screen.** An `UNKNOWN` end is a failure, a provider outage included, as D58 item 4 says. A screened item is never screened again, although a fresh id would technically allow it.

## Ids

The guest admits each `<case_id>/<role>/<attempt>` once (`corpusadmit.AdmissionID`). Admissions are rebuilt from the guest's audit journal and hold across manifest installs (`production_workflow_catalog_linux.go:227`, checked at `production_corpus_admission_linux.go:344` and `production_workflow_catalog_linux.go:409`); a checkpoint restore forgets admissions made after it. H1 runs Configuration B of every compliant task as role `b-compliant`, attempt 1. So screening runs each task under a fresh id, `<case_id>.s<N>`, as the research pilot uses `.rp<N>` (`corpus/sampling/research_pilot.py`, `carrier.rename_ids`). Two rules:

- An item takes `.s<N+1>` only when its `.s<N>` id was never admitted (the guest refused it, or its round never ran). An admitted id means the item was screened, and rule 3 above forbids a second screen.
- No `.s<N>` id appears in the H1 manifest. The screening manifest builder checks that each renamed case equals its base case in everything except the id.

## One install for the whole screening

Each manifest install changes the corpus digest, and with it the service config, the registration and the containment-profile digests: one owner signing session per install (RA-4a). So the screening manifest holds the 100 tasks and an ordered reserve, built before round 1: per group, the next items in the order of decision 1 (decision 2 for HumanEval-JS), each adapted, construct-checked and witnessed. Round 1 runs the 100 tasks (`only_cases`); each later round runs, for the failed tasks, the next reserve items (`only_cases` again), from the same install. A reserve that runs out needs a new manifest, a new install and a new signing session. The reserve size is set after the research pilot, from its compliant B pass rate.

## Components

Five parts. Parts 1 to 3 and 5 are in the F33 build. Part 4 is development tooling.

1. **Guest: the `screening` quota profile** (`supervisor/cmd/tbound`, `internal/piruntime`, `internal/evalcorpus`).
   - New profile value `screening`, beside `thesis` and `pilot` (`managed_live_root_linux.go:95-96`). The service accepts it only on the research route and refuses to start with it on the engineering route, as it refuses a research pilot without a draw pin (`production_corpus_admission_linux.go:210-219`).
   - Its own validation path (`evalcorpus.Validate` checks exact counts): compliant cases only, at least 25 per tool (`read`, `write`, `edit`, `bash`), no adversarial or ambiguous case, no draw or attacker pin.
   - Role gate: under `screening`, `productionCorpus.roleAllowed` allows `b-compliant` only, checked first, before the C-DH branch (`:116-117`), because `roleAllowed` is the one choke point for `screenRequest`, `envelopeFor` and the binding. The `compliant` role (Configuration C), every C-DH role and the ambiguous roles are refused. This is the guest-side guarantee for D41: no C outcome exists for a screened task.
   - Ceilings are the compliant ceilings of the thesis config.
   - `tbound-op manifest-install` pins the `pilot` profile, so the screening manifest is installed by the owner's root step, as the confirmatory corpus is (RA-4a).
2. **Runner: the `compliant-screening` purpose** (`internal/runner`, `cmd/tbound-run`).
   - `runner.PurposeScreening`, beside `PurposeConstructWitness` (`plan.go:36-45`). Its plan has one step per compliant case: Configuration B, role compliant, in the seeded `order/compliant` rank order, restricted by `only_cases`. It refuses a manifest with any non-compliant case.
   - The B step runs exactly as in the study run, acceptance included: `AcceptExecutor` calls `tbound trial accept <case_id>` with `SpecDigest(caseID)` and the pinned acceptance cell image.
   - It is non-confirmatory, like a construct-witness run: export, report and calibration summary return `ErrNonConfirmatoryPurpose`. `tbound-run` requires quota profile `screening` for it and skips the registered-total check (`corpus.go:96-101`). The plan digest binds the purpose, so a screening plan never passes for a study plan.
   - No rerun: every end other than an admitted completion is a failure (decision 3), so a screening step has no rerun scope (`export.go:28`).
3. **Screening report: `tbound-run screening-report --config run.json`** (`cmd/tbound-run`).
   - One JSON row per planned task: the screening id, the base case id, the tool group and the end. The end is `admitted` only when the step is not partial, its task result is `pass` and its route evidence is complete. Otherwise it is `failed-acceptance`, `unknown:<cause>`, `budget-end`, `lost` (the acceptance or a cell could not finish) or `not-run`. A step that never ran is `not-run`, never a pass.
   - It also prints the digest of its output.
4. **Task-selection script: `corpus/sampling/screening.py`** (development tooling at a pinned commit, not an H1 input).
   - `select`: from the selection record of the compliant set (`corpus/third/compliant/sources.json`, `selection`), the dataset order and the screening ledger, it names the reserve (before round 1) and, for each later round, the reserve items that replace the failed tasks, in compliant-set rank order. It stops with a named shortfall when a group runs out.
   - `build`: turns the 100 built tasks and the built reserve into the screening manifest with `.s<N>` ids, and checks each renamed case against its base case.
   - `record`: appends each round's `screening-report` rows to the append-only screening ledger. A failed item stays failed.
   - The rule this script implements is committed before the first screening run (D58 item 4). It does not read or write any file bound by the `h1-sampling-frame` tag, or `corpus/third/` (it only reads the selection record).
   Building a reserve case is corpus work: adaptation, construct checks and acceptance witnesses, then a corpus-only commit on top of the build (D39). The script only names the items.
5. **Registration field** (`cmd/tbound-profile`, `preregistration-record`). One optional input, `--screening <file>`, whose SHA-256 the record carries. The file is the screening record: the script commit and digest; the commit that held the rule before the first run; the screening manifest, service config and registration digests of each install; the research ledger head and records at the end of screening; the base-to-`.s<N>` mapping; every screened task with its end and the reason for each replacement; the pass rate per group. The field has to land before the freeze, or that list sits outside the anchored artifact.

## Spend

The research USD ledger is one ledger for the research pilot, the screening and H1. Each screening step is one B run on the research route, and the service books each call in that ledger (D58 item 3). Before round 1, the owner compares the research pilot's measured B cost per task, times the 100 tasks plus the expected replacements, with the remaining cap (US$10, D59). RA-12 advances the checkpoint after every round.

## Flow

```
screening.py select (reserve) -> build and witness the reserve -> screening.py build (.s1 ids)
  -> owner installs the screening manifest (research route, profile screening; one signing session)
  -> round 1: tbound-run run (purpose compliant-screening, only_cases = the 100) -> screening-report -> screening.py record -> RA-12
  -> round N: only_cases = the replacements screening.py select names -> ... until no task failed, or a shortfall
```

## Landing order

1. Guest profile, validation and role gate, with tests: the profile is refused on the engineering route; a non-compliant case is refused; `compliant`, a C-DH role and the ambiguous roles are refused while `b-compliant` is allowed.
2. Runner purpose and `tbound-run` wiring, with a plan test: one B step per selected compliant task and no other step, the digest bound to the purpose.
3. `screening-report`, with a journal fixture for each end.
4. The `preregistration-record` field, with a test that the record carries the file's digest.
5. `screening.py`, which may land after F33.
