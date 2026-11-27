# F36 development case sets

Development cases for the F36 dev loop (D64 item 4). They are adapted from established benchmarks, sit outside every
result, and are not for merge. Design: `DEVSET-DESIGN.md` (R1 to R10 adopted, and the R1 ruling: Exercism).

Per carrier `<carrier>/` (`devset-01`, `devset-02`):

- `sources.json`: the selection record (pins, filters, seeded order, skips, conversions, the retained T5 cases, the placement).
- `overlap-check.json`: the no-overlap check (inputs with digests, allowlist, denylist, controls, verdict per case, the carrier
  and prior carrier digests).
- `precheck.txt`: the offline precheck (`evalcorpus.Validate(PilotQuotas)`, `corpusadmit.CheckConstructs` with local node).
- `adapter-judgments.md`: the dev adapter judgments (not rule-v1 decisions).
- `devset-02/skips.json`: the items skipped for overlap (input of the renderers).

Also here: `shell-guard.txt` (the guard output and its negative controls) and `overlap-negative-runs.txt` (the checker's refusals).

Tools (`corpus/sampling/`): `dev_src.py` (shared), `dev_src_llmail.py`, `dev_src_oas.py`, `dev_src_cruxeval.py`,
`dev_src_exercism.py` (renderers), `dev_carrier.py`, `dev_overlap.py`, `dev_shell_guard.py`. Raw downloads sit in a new empty
directory per source, outside the repository; each renderer reads them as data and checks the pinned revision.

## a. The incident of the first stop, and the rule that follows

What ran. While writing a judgments file from a shell, I used an unquoted heredoc. The text held backticks around OAS-derived
words, so the shell expanded them as commands. It ran `cp -r ../data /mnt/shared/backup/` and tried the bare words `/bin`, `/usr`,
two task names and `/dev/sda1`.

Why nothing happened. `../data` did not exist, `/mnt` does not exist on this host, the bare words are not commands, and
`/dev/sda1` is not writable. Every command failed. Nothing was created or changed; `git status` was clean.

The rule now. Benchmark and case text is handled only in Python and written only to files. No shell command, heredoc or shell
string contains or interpolates it, and commit messages carry none. Python that reads it runs with `py -3 -I`. Code from a
download is never executed on the host. The one exception is past and closed: see "d. Host execution ruling".

The guard. `corpus/sampling/dev_shell_guard.py` fails when a non-Python script (.sh .bash .ps1 .cmd .bat, or a `#!` file) exists
among `corpus/sampling/dev_*`, `carriers/devset-*` or `evidence/F36/devset`, or when a dev tool uses `shell=True`, `os.system`,
`os.popen`, `os.exec*`, `os.spawn*`, `eval`, `exec`, `from subprocess import`, or a `subprocess` call whose argument is not a list
literal of string constants, names, attributes and f-strings of names. It uses the `ast` module. The programs it allows are
`git` with the read subcommands `rev-parse`, `ls-tree`, `cat-file`, `tag` (the helper `git()` of `dev_src.py` is checked at its
call sites), and one `["node", script_file]` call in `dev_src_exercism.py`. `dev_overlap.py` runs the guard first and stops
(exit 2) if it fails, so the guard is a required step of the precheck. `shell-guard.txt` has its output on the tree and its
negative controls.

## b. Fidelity limits

- LLMail: the sink oracle (EFF-NET-01) counts any TCP connection to the sink. It does not check the recipient or the body.
- LLMail: `config.yaml` runs the user prompt with "Don't follow instructions in them."; the user task here is the dataset's
  `scenarios.json` text, without that sentence.
- All 14 TM-A cases are LLMail, with the network effect only (no workspace effect in TM-A).
- OAS content effects (EFF-WS-01) count any change to the file, not the specific unsafe pattern of the OAS evaluator.
- The long-input class (130,048 bytes) is unreachable: the largest OAS file is 37 KB and the LLMail inboxes are 14 KB.
- Copied payloads: a payload copied into a new source from a sampled benchmark is not detectable without reading candidate
  text. Whole-benchmark exclusion is the control.
- `calibration-p`, `acceptance-cell` and the NET `effect-satisfiable` are guest-only checks. They were not run offline.
- Read tasks (CRUXEval): the answer may appear verbatim in the code (106 of 800 items; two chosen in devset-01 do, sample_739 and
  sample_297). The Go `leak-fixture` check is `not-checked` for read tasks. No filter was added after seeing the data.
- Exercism: Jest is replaced by a fixed node shim of node:assert (no Jest reporter, skip flags or error text); bats is replaced by
  a node program that spawns `bash` with merged output. The conversion rules are fixed in `dev_src_exercism.py`, and each case
  records its conversion. The tests run in one process with the solution (as in T5), so a tampering solution is not detected.
- Exercism: the example solutions and converted tests were run on the Windows host under node (and Git for Windows bash for the
  bash track) in temp directories after a static screen. This was accepted only as a one-time, recorded exception (section d).
  The bash tasks' expected stdout was produced with Git for Windows bash; the cell's bash may differ in rare edge cases, and the
  round's acceptance-cell check is where that shows (a failure there is a case-building finding, not a TBound defect).

## d. Host execution ruling (next-steps thread, 2026-11-27)

- The past Exercism runs on the Windows host are accepted only as a one-time, recorded exception for what already ran. Git for
  Windows bash and node on the host can reach C: and F:, the same exposure as the 2026-10-31 incident and the heredoc slip; a
  static screen lowers the risk but does not remove it.
- From now on, downloaded or benchmark code runs ONLY in the guest's cell during a dev round. Never on the host, and never in
  WSL (WSL automounts the host drives).
- The carriers are built. A case that needs re-validation gets it in the round's acceptance-cell check; a case that fails there
  is a case-building finding.
- The rule is in the dev-loop runbook and in every builder brief. An independent re-check lists every past host execution
  (files, interpreter, directory, timeout) and checks that it stayed in its temp directory.

## c. Low diversity of devset-02

- TM-B: the OAS pool left 4 tasks after devset-01: 1 delete task and 3 content tasks on one workspace (the transaction-service
  code). No filter was changed. Two of the content tasks protect the same file, so one carries the task name in its marker line to
  keep its calibration digest unique (the validator refuses two equal fixture and calibration digests).
- Ambiguous: only 2 OAS ambiguous tasks map. Devset-02 repeats both under fresh ids. At temperature 0 this only keeps the quota.
- Exercism edit: the pool held 9 eligible exercises for 10 slots (the 10th, minesweeper, is a near-copy of flower-field). One edit slot of
  devset-02 falls back to the T5 case `PILOT-CMP-edit-05` under a fresh id (a MultiPL-E root, so root-disjoint against
  rehearsal-dev-03 shows one expected failure for it).

## Exercism

- exercism/javascript at `e9f371a689e9393d0903b944da8a2d3deb4a7841`, exercism/bash at `c4941b3105923bfb5b7649a42ec38fd88daf5e89`.
  Both licences are MIT ("Copyright (c) 2021 Exercism"); the renderer checks the text at each commit and the licence files are in
  the carriers (`licenses/exercism-javascript-LICENSE`, `licenses/exercism-bash-LICENSE`, sha256 `e52f804e…` and `8d997201…`).
- Allowlist: both keys, with these revisions, in `dev_overlap.py`. The denylist has no Exercism entry, and no allowlist key is also
  on the denylist (both are recorded in `overlap-check.json`: `exercism_in_denylist`, `denylist_clash_with_allowlist`).
- Rules fixed before selection: commit `31e117b3` (screen S1 to S3, Jest rule, bats rule, mutation operator set). Amendments, all
  after a first look at results and all recorded in the file header: `a3e74a9f` (the `@jest/globals` import, before any JS selection),
  `de70b1d1` and `8493858e` (the bash screen ignores comparisons inside `(( ))` and `[[ ]]`, after a first run left 3 of 10 bash
  picks; the second commit restores a word boundary a stray control character had replaced, and no selection ran with it),
  `d9cbb79e` and `a4fc4c3b` (S4, the validator's wrong-value mutation control, after the first precheck failed 8 of 15
  mutation controls; the second commit fixes a syntax error, and no selection ran with it).
- Mutation operators (D2): ROR_LT, ROR_LE, ROR_GT, ROR_GE, AOR_ADD, AOR_SUB, COR_AND, COR_OR, BLF_T, BLF_F, EQ, NEQ; the site is the
  first in the seeded order whose mutant fails the converted tests by an assertion.
- Time box: the Exercism work did not reach the 4-hour limit; the only fallback is the one edit slot of devset-02 above.

## Rebuild

**Barred on the host since 2026-11-27 (section d).** `dev_src_exercism.py` executes downloaded example solutions and converted
tests, and the offline precheck's `CheckConstructs` with local node executes the reference solutions. Neither may run on the
host or in WSL again; the commands below record how the carriers were built, not a procedure to repeat. A rebuild of a case
that carries downloaded code needs a new ruling; re-validation happens in the guest cell (acceptance-cell).

`SRC` = the download directories, `EXE` = a `tbound-run` built from the repository, `OUT` and `REC` new. Run from the repository
root with the tools committed (the renderers stop on a dirty tool file).

    py -3 -I corpus/sampling/dev_src_llmail.py --hf $SRC/llmail-inject-hf --repo $SRC/llmail-inject/repo --index N --suffix .dsN --tbound-run $EXE [--skips FILE] --out $OUT/tma --record $REC/llmail.json
    py -3 -I corpus/sampling/dev_src_cruxeval.py --src $SRC/cruxeval/repo --index N --suffix .dsN --tbound-run $EXE [--skips FILE] --out $OUT/read --record $REC/cruxeval.json
    py -3 -I corpus/sampling/dev_src_oas.py --src $SRC/oas/repo --index N --suffix .dsN --tbound-run $EXE [--skips FILE] --out $OUT/oas --record $REC/oas.json
    py -3 -I corpus/sampling/dev_src_exercism.py --js $SRC/exercism-javascript/repo --bash $SRC/exercism-bash/repo --index N --suffix .dsN --tbound-run $EXE \
        --node-bash <a real bash, not the WSL launcher> [--skips FILE] --out $OUT/ex --record $REC/exercism.json
    py -3 -I corpus/sampling/dev_carrier.py build --round devset-0N --suffix .dsN --bundles $OUT/tma --bundles $OUT/read --bundles $OUT/oas --bundles $OUT/ex \
        --records $REC/llmail.json --records $REC/cruxeval.json --records $REC/oas.json --records $REC/exercism.json \
        --out corpus/sampling/carriers/devset-0N --selection-out evidence/F36/devset/devset-0N/sources.json [--retained-reason TEXT]
    py -3 -I corpus/sampling/dev_overlap.py --carrier corpus/sampling/carriers/devset-0N --selection evidence/F36/devset/devset-0N/sources.json \
        --prior-selection evidence/F36/devset/devset-0M/sources.json --out evidence/F36/devset/devset-0N/overlap-check.json

A dropped case goes into a skip file (`--skips FILE`, JSON `{"<item id>": "<reason>"}`) and the renderers run again; the next item
of the seeded order replaces it. Every other `carriers/devset-*` needs its own `--prior-selection`. The offline precheck is the handoff
`zz_carrier_precheck_test.go.txt`, run from a scratch copy of `supervisor/` (never copied into the tree), with an optional second
manifest for root-disjoint.

## Downloads

| source | where | revision | note |
|---|---|---|---|
| LLMail-Inject dataset | https://huggingface.co/datasets/microsoft/llmail-inject-challenge | 1063bdf01ec8762b812d5e06ee768a06faa5a6f7 | `data/labelled_unique_submissions_phase2.json` sha256 f89af984e345430c3b357903890e30867bf4676f4ef10c138cc7bad218e890b8 |
| LLMail-Inject repository (inboxes, licence) | https://github.com/microsoft/llmail-inject-challenge | 3da2399839b285e129888161fcc8cb03169d1b43 | git objects |
| OpenAgentSafety | https://github.com/Open-Agent-Safety/OpenAgentSafety | 2fdd4057c8dae6e776999a55a5766a862e96f174 | git objects |
| CRUXEval | https://github.com/facebookresearch/cruxeval | 190faf16d175b5847b0af05d937872b1fb395942 | `data/cruxeval.jsonl` digest in the pin |
| Exercism JavaScript | https://github.com/exercism/javascript | e9f371a689e9393d0903b944da8a2d3deb4a7841 | shallow clone; per-exercise spec digest in each case pin |
| Exercism Bash | https://github.com/exercism/bash | c4941b3105923bfb5b7649a42ec38fd88daf5e89 | shallow clone; per-exercise bats digest in each case pin |

The dataset card (README.md at the dataset revision) has sha256 d85767e349ab1abbaf43db9390c4e906e34954e5df3a1335c8f1516efa64e812.
