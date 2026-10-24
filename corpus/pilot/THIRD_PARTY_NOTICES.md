# Third-party notices — TBound pilot corpus

The pilot corpus is adapted from established, pinned benchmark items (D37; owner rule: adapt
established benchmarks, do not invent cases). This file records every upstream source, its
pinned revision, its license, and the changes made. Pinned file digests are in
`corpus/pilot/sources.json` (`digests`) and in each case's `meaning_record.benchmark_digest`.
No upstream code was executed to build the corpus.

## Adversarial roots

| Source | Pinned revision | License | Used for |
|---|---|---|---|
| [InjecAgent](https://github.com/uiuc-kang-lab/InjecAgent) | `f19c9f2c79a41046eb13c03c51a24c567a8ffa07` | MIT | TM-A A-03..A-07 (attacker_cases_dh.jsonl lines 10, 18, 21; attacker_cases_ds.jsonl lines 1, 10) |
| [AgentDojo](https://github.com/ethz-spylab/agentdojo) | `089ed468cf3ed0322acc66b0211f26d9d90dbf60` | MIT | TM-A A-01, A-02 (travel/InjectionTask1, workspace/InjectionTask3) |
| [ASB](https://github.com/agiresearch/ASB) | `544540ff0788998072f7df463056fd17e62cc9f4` | MIT | TM-B B-01, B-02, B-03 (DesignDataTheft, BackupTampering, ConferenceDataLeak) |
| [RiOSWorld](https://huggingface.co/datasets/JY-Young/RiOSWorld) | `96bb09d9e922fad590603cc8468c602fbc788d95` | Apache-2.0 | TM-B B-04 (data.json id 441:os_01) |

Changes: the attacker instruction and intent are preserved; the simulated service/API tools are
mapped onto native `node`/`bash` operations over synthetic fixtures inside the command cell, and
the benchmark's success-string match is replaced by TBound's external file/network oracle. The
exfiltrated payloads are synthetic canaries, not the upstream data. MIT and Apache-2.0 require the
copyright and license notice to be retained; no share-alike applies.

## Compliant tasks

| Source | Pinned revision | License | Used for |
|---|---|---|---|
| [RepoQA](https://github.com/evalplus/repoqa) (release 2024-06-23 data) | `ae876deb1365dbf5a15b0533723c8ed123eee586` | Apache-2.0 (code); dataset bundles upstream repo snippets under their own licenses | 5 read tasks (typescript bucket needles) |
| [MultiPL-E](https://huggingface.co/datasets/nuprl/MultiPL-E) (humaneval-js) | `28441b6024e71d4a1c1c0f6bf171c935cd5a43f2` | MIT (dataset and the original HumanEval) | 15 write/edit/bash tasks |

RepoQA needle snippets redistributed here (unmodified, relocated to a neutral path in the fixture):

| Needle | Upstream repository | License (at the pinned RepoQA commit) |
|---|---|---|
| `isAxiosError` (lib/helpers/isAxiosError.js) | axios/axios | MIT |
| `convertToFP` (src/fp/_lib/convertToFP/index.ts) | date-fns/date-fns | MIT |
| `useLanguageNames` (src/components/hooks/useLanguageNames.ts) | umami-software/umami | MIT |
| `isPresent` (src/lib/chatCompletionUtils.ts) | openai/openai-node | Apache-2.0 |
| `WebGLExtensions` (src/renderers/webgl/WebGLExtensions.js) | mrdoob/three.js | MIT |

Changes: RepoQA — the needle's source file is materialized as the workspace fixture at a neutral
path (`src/needle-N.<ext>`) so the prompt does not reveal the target name; similarity grading is
replaced by an exact function-name witness. The RepoQA LICENSE/NOTICE and each upstream repository
license are retained. MultiPL-E/HumanEval — the JS prompt and the `node:assert` tests are used
verbatim; a canonical JS reference solution was authored by the project (verified against the
dataset tests); the bash tasks append a success marker to the dataset tests. MIT and Apache-2.0
notices are retained.

## Ambiguous cases

| Source | Pinned revision | License | Used for |
|---|---|---|---|
| [ToolEmu](https://github.com/ryoungj/ToolEmu) | `ac4a7ab7ed8c7985d96231e214bd6b54304b7ddb` | Apache-2.0 | 2 ambiguous cases (official_101, official_106) |

Changes: the ToolEmu tool scenario is rendered as a concrete `bash` operation over synthetic
fixtures and scored by a written rubric; the under-specified recipient/target that makes each case
borderline is preserved. The Apache-2.0 notice is retained.
