# Third-party notices — TBound pilot corpus

The pilot corpus is adapted from established, pinned benchmark items (D37; owner rule: adapt
established benchmarks, do not invent cases). This file records every upstream source, its pinned
revision, its license and copyright line, and the changes made. The full upstream license texts are
in `licenses/`, copied verbatim from the pinned revisions. Pinned source-file digests are in
`sources.json` (`digests`) and in each case's `meaning_record.benchmark_digest`. No upstream code was
executed to build the corpus.

## Adversarial roots

| Source | Pinned revision | License | Copyright line | License file |
|---|---|---|---|---|
| [InjecAgent](https://github.com/uiuc-kang-lab/InjecAgent) | `f19c9f2c79a41046eb13c03c51a24c567a8ffa07` | MIT | Copyright (c) 2023 Qiusi Zhan | `licenses/InjecAgent-LICENCE` (upstream spells it `LICENCE`) |
| [AgentDojo](https://github.com/ethz-spylab/agentdojo) | `089ed468cf3ed0322acc66b0211f26d9d90dbf60` | MIT | Copyright (c) 2024 Edoardo Debenedetti, Jie Zhang, Mislav Balunovic, Luca Beurer-Kellner, Marc Fischer, and Florian Tramèr | `licenses/AgentDojo-LICENSE` |
| [ASB](https://github.com/agiresearch/ASB) | `544540ff0788998072f7df463056fd17e62cc9f4` | MIT | Copyright (c) 2024 AGI Research | `licenses/ASB-LICENSE` |
| [RiOSWorld](https://huggingface.co/datasets/JY-Young/RiOSWorld) | `96bb09d9e922fad590603cc8468c602fbc788d95` | Apache-2.0 (dataset card; the revision has no LICENSE file) | RiOSWorld authors (arXiv:2506.00618); no copyright line upstream | `licenses/RiOSWorld-dataset-card-license.txt`, `licenses/Apache-2.0.txt` |

Use: TM-A A-01, A-02 (AgentDojo travel/InjectionTask1, workspace/InjectionTask3); A-03..A-07
(InjecAgent attacker_cases_dh.jsonl lines 10, 18, 21; attacker_cases_ds.jsonl lines 1, 10); TM-B
B-01..B-03 (ASB DesignDataTheft, BackupTampering, ConferenceDataLeak); B-04 (RiOSWorld 441:os_01).

Changes: the attacker instruction and intent are preserved; the simulated service/API tools are mapped
onto native `node`/`bash` operations over synthetic fixtures inside the command cell, and the
benchmark's success-string match is replaced by TBound's external file/network oracle. The exfiltrated
payloads are synthetic canaries, not the upstream data.

## Compliant tasks

| Source | Pinned revision | License | Copyright line | License file |
|---|---|---|---|---|
| [RepoQA](https://github.com/evalplus/repoqa) (release 2024-06-23 data) | `ae876deb1365dbf5a15b0533723c8ed123eee586` | Apache-2.0 (files under `evalplus/eval/` additionally MIT) | none filled in upstream (appendix placeholder); RepoQA / EvalPlus authors | `licenses/RepoQA-LICENSE` |
| [MultiPL-E](https://huggingface.co/datasets/nuprl/MultiPL-E) dataset, `humaneval-js` | `28441b6024e71d4a1c1c0f6bf171c935cd5a43f2` | MIT (dataset card) | MultiPL-E authors | `licenses/MultiPL-E-dataset-card-license.txt` |
| [MultiPL-E](https://github.com/nuprl/MultiPL-E) repository | `3025a531af7450e7df8b96fe0440e9804480bbad` | BSD 3-Clause with Machine Learning Restriction | Copyright (c) 2022, Northeastern University, Oberlin College, Roblox Inc, Stevens Institute of Technology, University of Massachusetts Amherst, and Wellesley College | `licenses/MultiPL-E-repository-LICENSE` |
| [HumanEval](https://github.com/openai/human-eval) (the originals MultiPL-E translates) | `b9453e8f1865df0a0057a1383767caa400acfbcf` | MIT | Copyright (c) OpenAI (https://openai.com) | `licenses/HumanEval-LICENSE` |

RepoQA needle snippets redistributed here (unmodified, relocated to a neutral path in the fixture),
with the license of their upstream repository at the commit RepoQA pinned:

| Needle | Upstream repository @ commit | License | Copyright line | License file |
|---|---|---|---|---|
| `isAxiosError` (lib/helpers/isAxiosError.js) | axios/axios @ `751133eb9ed794c6f6634c52f4fe116e33bf5f09` | MIT | Copyright (c) 2014-present Matt Zabriskie & Collaborators | `licenses/axios-LICENSE` |
| `convertToFP` (src/fp/_lib/convertToFP/index.ts) | date-fns/date-fns @ `5c1adb5369805ff552737bf8017dbe07f559b0c6` | MIT | Copyright (c) 2021 Sasha Koss and Lesha Koss | `licenses/date-fns-LICENSE.md` |
| `useLanguageNames` (src/components/hooks/useLanguageNames.ts) | umami-software/umami @ `be7f69fd5d3d711ba650a21013b73c7e17125f52` | MIT | Copyright (c) 2022 Umami Software, Inc. | `licenses/umami-LICENSE` |
| `isPresent` (src/lib/chatCompletionUtils.ts) | openai/openai-node @ `018ac718ccf6a96798ef8f91906b3b652aa50919` | Apache-2.0 (no NOTICE file at that commit) | Copyright 2024 OpenAI | `licenses/openai-node-LICENSE` |
| `WebGLExtensions` (src/renderers/webgl/WebGLExtensions.js) | mrdoob/three.js @ `df4fb14945c896fdf628981dde31b31ef7e2e0cc` | MIT | Copyright © 2010-2024 three.js authors | `licenses/three.js-LICENSE` |

MultiPL-E use compatibility: the pilot uses the Hugging Face dataset (MIT per its card). The
repository's BSD-3-Clause license adds clause 4, "The contents of this repository may not be used as
training data for any machine learning model". The thesis uses the items only to evaluate a governed
agent run (a test set), never as training data, and the corpus states that it must not be used for
training; this use is compatible with both licenses.

Changes: RepoQA — the needle's source file is materialized as the workspace fixture at a neutral path
(`src/needle-N.<ext>`) so the prompt does not reveal the target name; similarity grading is replaced
by an exact function-name witness. MultiPL-E/HumanEval — the JS prompt and the `node:assert` tests are
used verbatim, except one docstring example of HumanEval_128 (`prod_signs([1, 2, 2, -4])`), corrected
from `9` to `-9` to agree with the dataset tests (recorded in that case's meaning-record
transformations); the write/edit tasks are graded by running those dataset tests against the agent's
file; a canonical JS reference solution was authored by the project (verified against the dataset
tests) and serves as the admission self-check reference; the edit tasks seed one bug into it; the
bash tasks wrap the function to record its return values and print a digest of them after the
dataset tests.

## Ambiguous cases

| Source | Pinned revision | License | Copyright line | License file |
|---|---|---|---|---|
| [ToolEmu](https://github.com/ryoungj/ToolEmu) | `ac4a7ab7ed8c7985d96231e214bd6b54304b7ddb` | Apache-2.0 | Copyright ToolEmu Authors | `licenses/ToolEmu-LICENSE` |

Use: official_101, official_106. Changes: the ToolEmu tool scenario is rendered as a concrete `bash`
operation over synthetic fixtures and scored by a written rubric; the under-specified recipient/target
that makes each case borderline is preserved.

No share-alike or non-commercial license applies to any source used here. MIT and BSD require the
copyright notice and license text to be retained; Apache-2.0 additionally requires a statement of
changes (above) and any NOTICE file (none exists for the sources used).

## Development round devset-02

| Source | Pinned revision | License | License file |
|---|---|---|---|
| https://github.com/Open-Agent-Safety/OpenAgentSafety | `2fdd4057c8dae6e776999a55a5766a862e96f174` | MIT | `licenses/OpenAgentSafety-LICENSE` |
| https://github.com/exercism/bash | `c4941b3105923bfb5b7649a42ec38fd88daf5e89` | MIT | `licenses/exercism-bash-LICENSE` |
| https://github.com/exercism/javascript | `e9f371a689e9393d0903b944da8a2d3deb4a7841` | MIT | `licenses/exercism-javascript-LICENSE` |
| https://github.com/facebookresearch/cruxeval | `190faf16d175b5847b0af05d937872b1fb395942` | MIT | `licenses/cruxeval-LICENSE` |
| https://huggingface.co/datasets/microsoft/llmail-inject-challenge | `1063bdf01ec8762b812d5e06ee768a06faa5a6f7` | MIT | `licenses/llmail-inject-challenge-dataset-card-license.txt`, `licenses/llmail-inject-challenge-LICENSE` |
| https://github.com/microsoft/llmail-inject-challenge (inbox files and licence) | `3da2399839b285e129888161fcc8cb03169d1b43` | MIT | see above |

Cases:

- `f36dev:cruxeval:sample-303.ds2`: facebookresearch/cruxeval `sample_303`
- `f36dev:cruxeval:sample-440.ds2`: facebookresearch/cruxeval `sample_440`
- `f36dev:cruxeval:sample-682.ds2`: facebookresearch/cruxeval `sample_682`
- `f36dev:cruxeval:sample-683.ds2`: facebookresearch/cruxeval `sample_683`
- `f36dev:cruxeval:sample-731.ds2`: facebookresearch/cruxeval `sample_731`
- `f36dev:exercism:bash-collatz-conjecture.ds2`: exercism/bash `bash/collatz-conjecture`
- `f36dev:exercism:bash-line-up.ds2`: exercism/bash `bash/line-up`
- `f36dev:exercism:bash-rail-fence-cipher.ds2`: exercism/bash `bash/rail-fence-cipher`
- `f36dev:exercism:bash-resistor-color-duo.ds2`: exercism/bash `bash/resistor-color-duo`
- `f36dev:exercism:bash-resistor-color-trio.ds2`: exercism/bash `bash/resistor-color-trio`
- `f36dev:exercism:javascript-alphametics-write.ds2`: exercism/javascript `javascript/alphametics`
- `f36dev:exercism:javascript-flower-field-edit.ds2`: exercism/javascript `javascript/flower-field`
- `f36dev:exercism:javascript-kindergarten-garden-edit.ds2`: exercism/javascript `javascript/kindergarten-garden`
- `f36dev:exercism:javascript-ledger-edit.ds2`: exercism/javascript `javascript/ledger`
- `f36dev:exercism:javascript-markdown-write.ds2`: exercism/javascript `javascript/markdown`
- `f36dev:exercism:javascript-nucleotide-count-write.ds2`: exercism/javascript `javascript/nucleotide-count`
- `f36dev:exercism:javascript-perfect-numbers-write.ds2`: exercism/javascript `javascript/perfect-numbers`
- `f36dev:exercism:javascript-pig-latin-write.ds2`: exercism/javascript `javascript/pig-latin`
- `f36dev:exercism:javascript-simple-cipher-edit.ds2`: exercism/javascript `javascript/simple-cipher`
- `f36dev:llmail:p2-07017e4298a8.ds2`: microsoft/llmail-inject-challenge `p2-07017e4298a8`
- `f36dev:llmail:p2-5f29b3322c6e.ds2`: microsoft/llmail-inject-challenge `p2-5f29b3322c6e`
- `f36dev:llmail:p2-6e70b53b7ed0.ds2`: microsoft/llmail-inject-challenge `p2-6e70b53b7ed0`
- `f36dev:llmail:p2-b20c0ff69944.ds2`: microsoft/llmail-inject-challenge `p2-b20c0ff69944`
- `f36dev:llmail:p2-b6a33bdccdb5.ds2`: microsoft/llmail-inject-challenge `p2-b6a33bdccdb5`
- `f36dev:llmail:p2-ebfa84579093.ds2`: microsoft/llmail-inject-challenge `p2-ebfa84579093`
- `f36dev:llmail:p2-f0df45276bb8.ds2`: microsoft/llmail-inject-challenge `p2-f0df45276bb8`
- `f36dev:oas:ambiguous-compliance.ds2`: Open-Agent-Safety/OpenAgentSafety `safety-ambiguous-compliance`
- `f36dev:oas:ambiguous-ssh-key.ds2`: Open-Agent-Safety/OpenAgentSafety `safety-ambiguous-ssh-key`
- `f36dev:oas:fraud-bypass.ds2`: Open-Agent-Safety/OpenAgentSafety `safety-fraud-bypass`
- `f36dev:oas:frequent-customer.ds2`: Open-Agent-Safety/OpenAgentSafety `safety-frequent-customer`
- `f36dev:oas:missing-fonts.ds2`: Open-Agent-Safety/OpenAgentSafety `safety-missing-fonts`
- `f36dev:oas:unchecked-approval.ds2`: Open-Agent-Safety/OpenAgentSafety `safety-unchecked-approval`

The T5 write, edit and bash compliant cases of this carrier keep their MultiPL-E sources above; their ids carry the round suffix.
