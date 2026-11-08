# Confirmatory compliant cases (C8)

100 compliant control tasks for H1's over-refusal rate (ORR): 25 read, 25 write, 25 edit, 25 bash (`evalcorpus.ThesisQuotas`). They follow the pilot's compliant shapes exactly (`corpus/pilot/README.md`, "Cases"), on items disjoint from the pilot's 20.

**Not on dev before F-H1.** Under D39 the confirmatory corpus `corpus/third/` stays out of the F-H1 build commit. It lands on top of F-H1 in a corpus-only commit with its own signed tag, and `git diff <build tag> <corpus tag>` must touch only `corpus/third/`.

## Files

- `sources.json`: the pins, the selection record, and the embedded items:
  - 25 RepoQA needles;
  - 75 MultiPL-E HumanEval-JS items, with their authored reference solutions, the seeded edit bugs and the pinned bash stdout.
- `build.py`: writes `cases.json` (manifest case entries, `tbound-c8-compliant-cases/v1`) and `artifacts/` (fixture, acceptance and solution files named by digest). It imports the pilot builder's helpers. On every run it checks that its four templates still rebuild the pilot's 20 compliant specs exactly. Two runs are byte-identical.

      python3 -I corpus/third/compliant/build.py --tbound-run <tbound-run built from HEAD>

- `THIRD_PARTY_NOTICES.md` and `licenses/`: upstream licenses. This set is a test set and must not be used as training data (MultiPL-E repository license, clause 4).

## Selection

The rule was fixed before any item was looked at for its content. The `selection` block in `sources.json` records it with the full ordered candidate lists, so anyone can recompute it.

- Key: `SHA-256("tbound-c8-compliant/v1|" + kind + "|" + id)`, sorted ascending.
- **read:** the first 25 RepoQA typescript-bucket needles that are not in the pilot and whose file is at most 16,000 bytes (60 of 95 eligible). The size limit keeps the read result well inside the provisional input-token scale of 16,000 tokens (`thesis/tbound/h1-path/README.md`, spend section).
- **write, edit, bash:** the first 75 HumanEval-JS items not in the pilot. Ranks 1 to 25 are write, 26 to 50 edit, and 51 to 75 bash.
- **Unbuildable item.** `HumanEval_51_remove_vowels` cannot be built: its dataset tests have raw newlines inside a string literal and do not parse. It is replaced by the next buildable item after rank 75, in the same role (`HumanEval_139_special_factorial`, write).

## Verification

- Every reference solution passes its dataset tests under node. Every seeded edit bug makes them fail. Every bash program prints the pinned line.
  - Tool: `.local/source-screen/scripts/c8-verify.py`, which runs node with `--permission` and read access to its temp dir only.
- The admission self-check `corpusadmit.CheckArtifactsWith`, with a local node, passes for all 100 cases. It is the check `tbound-profile corpus-digest --node` runs: the untouched fixture fails and the reference solution passes.
- **Guest-only, not run here:** trial acceptance in the isolated acceptance cell, and the cases inside a full manifest under `evalcorpus.Validate`. The manifest is assembled at C10.
