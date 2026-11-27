# F36 development case sets

Development cases for the F36 dev loop (D64 item 4). They are adapted from established benchmarks, sit outside every
result, and are not for merge. Design: `DEVSET-DESIGN.md` (R1 to R10, adopted).

Per carrier `<carrier>/`:

- `sources.json`: the selection record (pins, filters, seeded order, skips, the retained T5 cases, the placement).
- `overlap-check.json`: the no-overlap check (inputs with digests, allowlist, denylist, controls, verdict per case).
- `precheck.txt`: the offline precheck (`evalcorpus.Validate(PilotQuotas)`, `corpusadmit.CheckConstructs` with local node).
- `adapter-judgments.md`: the dev adapter judgments (not rule-v1 decisions).

Tools (`corpus/sampling/`): `dev_src.py` (shared), `dev_src_llmail.py`, `dev_src_oas.py`, `dev_src_cruxeval.py`
(renderers), `dev_carrier.py`, `dev_overlap.py`. Raw downloads sit in a new empty directory per source, outside the
repository; each renderer reads them as data and checks the pinned revision and digest.

Rebuild a carrier (`SRC` = the download directories, `EXE` = a `tbound-run` built from the repository, `OUT` and `REC` new):

    py -3 -I corpus/sampling/dev_src_llmail.py --hf $SRC/llmail-inject-hf --repo $SRC/llmail-inject/repo --index 1 --suffix .ds1 --tbound-run $EXE --out $OUT/tma --record $REC/llmail.json
    py -3 -I corpus/sampling/dev_src_cruxeval.py --src $SRC/cruxeval/repo --index 1 --suffix .ds1 --tbound-run $EXE --out $OUT/read --record $REC/cruxeval.json
    py -3 -I corpus/sampling/dev_src_oas.py --src $SRC/oas/repo --index 1 --suffix .ds1 --tbound-run $EXE --out $OUT/oas --record $REC/oas.json
    py -3 -I corpus/sampling/dev_carrier.py build --round devset-01 --suffix .ds1 --bundles $OUT/tma --bundles $OUT/read --bundles $OUT/oas \
        --records $REC/llmail.json --records $REC/cruxeval.json --records $REC/oas.json \
        --out corpus/sampling/carriers/devset-01 --selection-out evidence/F36/devset/devset-01/sources.json
    py -3 -I corpus/sampling/dev_overlap.py --carrier corpus/sampling/carriers/devset-01 \
        --selection evidence/F36/devset/devset-01/sources.json --out evidence/F36/devset/devset-01/overlap-check.json

A dropped case goes into a skip file (`--skips FILE`, JSON `{"<item id>": "<reason>"}`) and the renderers run again; the
next item of the seeded order replaces it. The offline precheck is the handoff `zz_carrier_precheck_test.go.txt`, run from
a scratch copy of `supervisor/` (never copied into the tree).

The downloads, with revisions and sha256 (also in each `sources.json` record):

| source | where | revision | file sha256 |
|---|---|---|---|
| LLMail-Inject dataset | https://huggingface.co/datasets/microsoft/llmail-inject-challenge | 1063bdf01ec8762b812d5e06ee768a06faa5a6f7 | `data/labelled_unique_submissions_phase2.json` f89af984e345430c3b357903890e30867bf4676f4ef10c138cc7bad218e890b8 |
| LLMail-Inject repository (inboxes, licence) | https://github.com/microsoft/llmail-inject-challenge | 3da2399839b285e129888161fcc8cb03169d1b43 | git objects |
| OpenAgentSafety | https://github.com/Open-Agent-Safety/OpenAgentSafety | 2fdd4057c8dae6e776999a55a5766a862e96f174 | git objects |
| CRUXEval | https://github.com/facebookresearch/cruxeval | 190faf16d175b5847b0af05d937872b1fb395942 | git objects (`data/cruxeval.jsonl` digest in the pin) |

The dataset card (README.md at the dataset revision) has sha256 d85767e349ab1abbaf43db9390c4e906e34954e5df3a1335c8f1516efa64e812.
