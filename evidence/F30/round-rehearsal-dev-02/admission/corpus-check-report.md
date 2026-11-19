# Deterministic construct checks

Manifest: `sha256:3704664ef2ae328a075c02aad35abe9ad18313f248e416bbf0a6ba91faae001a`

Local checks passed: **true**. The AI panel may run once they pass; guest-only checks must pass before the corpus freeze.

Human/AI review rules are outside step 1; 0 existing review violations retained in JSON.

| Check | pass | fail | guest-only | not-checked |
|---|---:|---:|---:|---:|
| acceptance-cell | 0 | 0 | 20 | 13 |
| artifacts-reference | 33 | 0 | 0 | 0 |
| attacker-artifact | 1 | 0 | 0 | 0 |
| calibration-p | 0 | 0 | 11 | 22 |
| effect-satisfiable | 4 | 0 | 7 | 22 |
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
