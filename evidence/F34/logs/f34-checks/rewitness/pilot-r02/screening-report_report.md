# Deterministic construct checks

Manifest: `sha256:a6805ea7ac50733520586725c1b833f94195d298feeca525d5ba699efbcb74f9`

Local checks passed: **true**. The AI panel may run once they pass; guest-only checks must pass before the corpus freeze.

Human/AI review rules are outside step 1; 0 existing review violations retained in JSON.

Imported guest results are recomputed from original producer files the operator kept (operator-retained, no external anchor); each row's provenance in report.json lists their sha256.

| Check | pass | fail | guest-only | not-checked |
|---|---:|---:|---:|---:|
| acceptance-cell | 0 | 0 | 20 | 13 |
| artifacts-reference | 33 | 0 | 0 | 0 |
| attacker-artifact | 1 | 0 | 0 | 0 |
| calibration-p | 11 | 0 | 0 | 22 |
| effect-satisfiable | 11 | 0 | 0 | 22 |
| leak-fixture | 15 | 0 | 0 | 18 |
| leak-prompt | 31 | 0 | 0 | 2 |
| manifest-digest | 1 | 0 | 0 | 0 |
| manifest-validation | 1 | 0 | 0 | 0 |
| mutation | 20 | 0 | 0 | 13 |
| root-disjoint | 0 | 0 | 0 | 33 |
| root-unique | 33 | 0 | 0 | 0 |
| source-license | 33 | 0 | 0 | 0 |
| trial-admission | 33 | 0 | 0 | 0 |
| validation | 33 | 0 | 0 | 0 |

Failures and license conditions (all case results and reasons are in JSON):

- License condition (15 cases): licenses/MultiPL-E-repository-LICENSE: 4.  The contents of this repository may not be used as training data for any machine learning model, including but not limited to neural networks.
- License condition (15 cases): licenses/MultiPL-E-repository-LICENSE: BSD 3-Clause License with Machine Learning Restriction
