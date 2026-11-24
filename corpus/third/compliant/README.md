# Confirmatory compliant cases (C8)

100 compliant control tasks for H1's over-refusal rate (ORR): 25 read, 25 write, 25 edit, 25 bash (`evalcorpus.ThesisQuotas`). They follow the pilot's compliant shapes exactly (`corpus/pilot/README.md`, "Cases"), on items disjoint from the pilot's 20.

**Not on dev before F-H1.** Under D39 the confirmatory corpus `corpus/third/` stays out of the F-H1 build commit. It lands on top of F-H1 in a corpus-only commit with its own signed tag, and `git diff <build tag> <corpus tag>` must touch only `corpus/third/`.

## Files

- `sources.json`: the pins, the selection record, and the embedded items:
  - 25 RepoQA needles;
  - 75 MultiPL-E HumanEval-JS items, with their authored reference solutions, the seeded edit bugs and the pinned bash stdout.
- `build.py`: writes `cases.json` (manifest case entries, `tbound-c8-compliant-cases/v1`) and `artifacts/` (fixture, acceptance and solution files named by digest). It imports the pilot builder's helpers. On every run it checks that its four templates still rebuild the pilot's 20 compliant specs exactly. Two runs are byte-identical.

      python3 -I corpus/third/compliant/build.py --tbound-run <tbound-run built from HEAD>

  Trial budgets are 11 host calls, 45,056 tokens (11 x 4,096, D50: every provider call books the full 4,096-token completion cap) and 1,200 s. 11 host calls allow 5 tools and the final reply, the pilot's compliant budget (#149, owner 2026-11-14), which the owner extended to this set on 2026-11-15. The pilot's 10,000 tokens is refused by a D50 build.

- `tools/c8-extract.py` (selection and embedding) and `tools/c8-verify.py` (node verification and merge): copies of the scripts that produced `sources.json`. They ran from `.local/source-screen/scripts/` and read the release files at the paths in their headers.
- `THIRD_PARTY_NOTICES.md` and `licenses/`: upstream licenses. This set is a test set and must not be used as training data (MultiPL-E repository license, clause 4).

## Selection

The rule was fixed before any item was looked at for its content. The `selection` block in `sources.json` records it with the full ordered candidate lists, so anyone can recompute it.

- Key: `SHA-256("tbound-c8-compliant/v1|" + kind + "|" + id)`, sorted ascending.
- **read:** the first 25 RepoQA typescript-bucket needles that are not in the pilot and whose file is at most 16,000 bytes (60 of 95 eligible).
  - The limit keeps one read of the needle file near 4k tokens, so the task fits the 7-host-call compliant budget with room to spare. It is a byte limit, not a token limit.
  - Disclosure: it was set after looking at the aggregate file-size distribution of the 95 free needles, but before looking at any item's content, name or rank. Every limit from 15,881 to 17,686 bytes gives the same 60-item pool.
  - The limit removes 35 needles, so these files are on average larger than the pilot's five (all under 2 KB). That is a known difference from the pilot.
- **write, edit, bash:** the first 75 HumanEval-JS items not in the pilot. Ranks 1 to 25 are write, 26 to 50 edit, and 51 to 75 bash.
- **Replacements.** An item that cannot be built, or that fails a mechanical admission check, gives its slot to the next eligible item after the selected ranks, in the same role. Each item and its reason is in `sources.json`.
  - Checks applied to every selected item: the dataset tests run; `leak-prompt`; and, for write and edit, whose prompts show the spec, that each spec example agrees with the dataset tests.
  - Unbuildable: `HumanEval_51_remove_vowels`, whose tests have raw newlines inside a string literal.
  - Inadmissible:
    - one needle, `str`, whose name occurs in its own prompt;
    - four edit items, `HumanEval_47_median`, `HumanEval_116_sort_array`, `HumanEval_148_bf` and `HumanEval_149_sorted_list_sum`, whose spec examples contradict the dataset tests.
  - Found by the independent C8 review of 2026-11-08 (`.local/source-screen/output/c8-review.md`).

## Verification

- Every reference solution passes its dataset tests under node. Every seeded edit bug makes them fail. Every bash program prints the pinned line.
  - Tool: `.local/source-screen/scripts/c8-verify.py`, which runs node with `--permission` and read access to its temp dir only.
- `corpusadmit.CheckConstructs` with a local node, on these cases wrapped in a compliant-only manifest and checked against the pilot manifest, passes. All 100 pass `source-license`, `leak-prompt`, `mutation`, `trial-admission`, `artifacts-reference`, `root-unique`, `root-disjoint` and `validation`. `acceptance-cell` is guest-only, and `leak-fixture` reports 25 cases as not checked. This includes the admission self-check `tbound-profile corpus-digest --node` runs: the untouched fixture fails and the reference solution passes.
- The MultiPL-E items come from the Hugging Face rows export. It equals the pinned parquet: 161 rows, 0 differences in name, prompt and tests, checked with pyarrow 26.0.1 in a scratch venv (`.local/source-screen/scripts/c8-parquet-check.py`, result `.local/source-screen/output/c8-parquet-check.md`).
- **Guest-only, not run here:** trial acceptance in the isolated acceptance cell, and the cases inside a full manifest under `evalcorpus.Validate`. The manifest is assembled at C10.
