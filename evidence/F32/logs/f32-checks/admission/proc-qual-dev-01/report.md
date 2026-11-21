# Deterministic construct checks

Manifest: `sha256:f54057418913e2f181123dd22af649ae2daf582c4caa409dda085b41d220af46`

Local checks passed: **true**. The AI panel may run once they pass; guest-only checks must pass before the corpus freeze.

Human/AI review rules are outside step 1; 0 existing review violations retained in JSON.

| Check | pass | fail | guest-only | not-checked |
|---|---:|---:|---:|---:|
| acceptance-cell | 0 | 0 | 20 | 15 |
| artifacts-reference | 35 | 0 | 0 | 0 |
| attacker-artifact | 1 | 0 | 0 | 0 |
| calibration-p | 0 | 0 | 13 | 22 |
| effect-satisfiable | 4 | 0 | 9 | 22 |
| leak-fixture | 15 | 0 | 0 | 20 |
| leak-prompt | 33 | 0 | 0 | 2 |
| manifest-digest | 1 | 0 | 0 | 0 |
| manifest-validation | 1 | 0 | 0 | 0 |
| mutation | 20 | 0 | 0 | 15 |
| root-disjoint | 0 | 0 | 0 | 35 |
| root-unique | 35 | 0 | 0 | 0 |
| source-license | 35 | 0 | 0 | 0 |
| trial-admission | 35 | 0 | 0 | 0 |
| validation | 35 | 0 | 0 | 0 |

Failures and license conditions (all case results and reasons are in JSON):

- License condition (15 cases): licenses/MultiPL-E-repository-LICENSE: 4.  The contents of this repository may not be used as training data for any machine learning model, including but not limited to neural networks.
- License condition (15 cases): licenses/MultiPL-E-repository-LICENSE: BSD 3-Clause License with Machine Learning Restriction
