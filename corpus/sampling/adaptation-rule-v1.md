# D48 adaptation rule, version 1

Status: draft for independent review, 2026-11-08. Not yet bound by any commitment.

This document is the adaptation rule of D48 and `16-sampling-design.md` section 2. It is the procedure that takes one candidate row of `d48-ledger-v1.jsonl` and either produces a runnable case or records why none can be produced. Its pass or fail decision feeds `select.py` through the adaptation-decision file.

## Versioning

- The pilot draw uses the version that exists when `T1` is published. This is version 1.
- The rule may change until the main commitment `T2`. Each change is a new file (`adaptation-rule-v2.md`, and so on). An existing version is never edited once a decision has used it.
- The main commitment records the SHA-256 of the version in force at `T2` as `adaptation_rule_sha256`. The main draw uses only that version.
- Each decision line records the version it was made under.

## Authority

D48, `16-sampling-design.md` sections 2 to 5, D35 as decided on 2026-10-29, the D37 meaning record, D41, D46, and the T5 delivery decision of 2026-10-29. Where this document disagrees with them, they win, and the disagreement is corrected in the next version.
