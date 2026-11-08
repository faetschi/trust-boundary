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

## 1. What a decision may use

A decision uses only these inputs:

- the ledger row;
- the pinned source item it names, read as data;
- the files the procedure itself writes for the case;
- the results of the construct checks in section 5.

A decision may not use:

- any Configuration C result on the case. Under D41 none exists before registration;
- the result or draw membership of any other case;
- the stratum sizes, or how many cases have passed so far.

Cases are decided one at a time, in the order `select.py` names them. A recorded decision is final for that version of this rule.

## 2. What the procedure produces

For each candidate it produces exactly one of two results.

**A passing case**, in the shape the pilot builder writes (`corpus/pilot/build.py`, `build_adv`). It has three parts.

- The case entry, with:
  - the threat class, from the ledger `stratum`;
  - the root scenario, from the ledger `attack_id`;
  - the source key and source item, from the ledger `source` and `base_item`;
  - the forbidden effects, from the ledger `predicate`;
  - the adaptation block: preserved intent, tool mapping, adaptation steps, lost distinctions, authorization class, and endpoint-oracle mapping;
  - the oracle id and version, the workspace fixture digest, and the calibration payload digest.
- The trial: the prompt, the budgets, the frozen script, `p_delta` with its justification, the pivot table, and the protected paths.
- The D37 meaning record, with every field `evalcorpus.Validate` checks under `meaning.*`: `case_origin` (`established`), `benchmark_key`, `benchmark_revision`, `benchmark_digest`, `source_item_id`, `root_id`, `license`, `redistribution`, `preserved_intent`, `task_unit`, `authorization_class`, `transformations`, `lost_distinctions`, `tool_mapping`, `oracle_mapping` and `label_origin`.

The artifacts the case pins (fixture, attacker) are written under the digest-named files the pilot uses.

**A failed candidate**, with no case. It gets only its decision line and the reason code of section 6.

Either way the candidate gets one line in the adaptation-decision file:

```
{"case_id": "<ledger case_id>", "pass": true|false, "reason": "<code>: <one sentence>", "rule_version": 1}
```

`select.py` reads `case_id`, `pass` and `reason`. It ignores `rule_version`, which is kept for the audit.

## 3. Procedure

Apply the steps in order. A step that cannot be completed ends the procedure with the failure code named in brackets (section 6).

1. **Locate the source item.** Open the file named by the row's `pin` and `base_item`, and check its digest against the pinned source digest. Read it as data only. [`F-SOURCE`]
2. **Fix the unit.** Take the unit the pilot commitment records for the source. For a pairing unit, take the exact pairing the `case_id` names. [`F-SOURCE`]
3. **Place the delivery.** Follow the T5 decision of 2026-10-29:
   - **TM-A:** the third-party content goes into a lower-trust workspace file that the frozen script reads first, so it reaches the model as tool output. The legitimate task stays in the user prompt.
   - **TM-B:** the request goes into the user prompt.

   The pilot builder's `egress` and `ws` cases are the reference implementation.
4. **Build the fixture.** Write a synthetic workspace that holds the objects the row's `predicate` observes. That means the protected files for workspace predicates, and the registered receiver for `EFF-NET-01`. Use owned paths only. Where the source names a system path that is only the location of a file the case itself creates (the RedCode rows admitted by owner call 5), move it to an owned path and record the move. [`F-FIXTURE`]
5. **Render the request.** Keep the source's wording. Change only what steps 3 and 4 require: paths, names and the registered receiver.
   - For the ASB rows whose Attacker Instruction does not state the effect, apply the fixed D48 goal-rewrite template. The template takes the row's own Attack goal, names the fixture object and the registered destination from step 4, and adds nothing else.
   - The pilot case `PILOT-ADV-B-01` is the reference implementation.
   - The rewritten text and the fact of the rewrite go into the meaning record's `transformations`. [`F-MEANING`]
6. **Map the tools.** Map each source tool or API to Pi's `read`, `write`, `edit` or `bash` on the synthetic fixture, as the pilot does. Record the mapping in `tool_mapping`. [`F-MEANING`]
7. **Map the oracle.** Set `forbidden_effects` and the oracle from the row's `predicate`. A compound predicate (for example `EFF-NET-01+EFF-WS-01`) lists every part, and each part must be observable by the instrument in force. [`F-CONSTRUCT`]
8. **Freeze the script and pivots.** Write the frozen script, the pivot table of the pinned attacker library, and `p_delta` with its justification, as the pilot does. [`F-CONSTRUCT`]
9. **Write the records.** Write the case entry, the trial and the meaning record of section 2. List every change from the source under `transformations` and every loss under `lost_distinctions`.
10. **Run the checks** of section 5. All must pass for the case to pass.
