| PR | what | independent review (Claude Sonnet 5.5) | tests before merge |
|---|---|---|---|
| #268 | panel packet name (#190) | f33-review-261-268-241-1: PASS | Windows go test cmd/tbound-panel |
| #265 | CI race shards | reviewed before 2026-11-23 (owner); CI dispatch run 45065380850 green | CI |
| #261 | no hardcoded paths | f33-review-261-268-241-1: PASS | check-self-contained.sh |
| #241 | docs (Hyper-V guide) | f33-review-261-268-241-1: FAIL (1 false sentence), fixed as prescribed | doc links |
| #272 | V-free planning | f33-review-272-274-1: PASS (nits fixed); plan equivalence F32 vs F33 | Windows runner, evalreport, evalscore, tbound-run; Linux-only compiled |
| #275 | comment ratchet after #265/#268 | f33-review-274b-275-1: PASS | check-comments + self-test |
| #273 | tbound-run caps (D32 item 6) | f33-review-273-270-271-1: PASS; f33-rereview-270-273-1: PASS | Windows calibration, runner, tbound-run |
| #270 | runbook research route F33 | f33-review-273-270-271-1: FAIL -> fixed; f33-rereview-270-273-1: FAIL (cross-freeze ledger record) -> fixed as prescribed | doc links, path check |
| #274 | US$10 cap | f33-review-272-274-1: FAIL (draft, digests) -> rebased; f33-review-274b-275-1: PASS (RA-1 pointer added) | Windows evalreport, runner, researchroute, researchprofile, tbound-run |
| #271 | screening design (doc) | f33-review-273-270-271-1: FAIL -> revised; f33-rereview-271-1: FAIL (wiring list) -> fixed | doc links |
| #276 | registration binds screening record | f33-review-276-1: PASS | Windows evalreport; Linux-only compiled |
| #277 | screening guest profile + runner purpose (agent f33-screening-impl-12-1) | f33-review-277-1: FAIL (screening rerun) -> fixed with a test that fails without the guard | Windows runner, evalreport, tbound-run, evalcorpus, corpusadmit; Linux-only (cmd/tbound) compiled |
| #278 | screening-report, corpus-digest profile | f33-review-278-1: FAIL (admitted without identity/evidence check) -> fixed with tests | Windows runner, tbound-run; Linux-only compiled |
| #279 | fresh admission ids | f33-review-279-1 | - |
| #238 | comment sweep | f32-review-238-240-1: PASS (integration) | full race gate g240 (7792 pass, 52 skip, 0 fail) on the merged tree 305e440d |
| #240 | Go cleanup incl. df3596eb (product) | f32-review-238-240-1: PASS; df3596eb noted as product-only | same g240 gate |
| #281 | V design doc (D61) | revised after its own review (next-steps thread); throwaway-ledger detail added | doc links |
| #284 | V switch in the plan (D61 item 3) | f33-review-284-1: PASS + 3 fixes (plan_v claim check; PlanV refused with C-DH/construct-witness; comments); plan proofs on the PR | Windows runner/evalreport; Linux tbound-profile, tbound-run, runner, evalreport (lt284c) |
| #286 | V forwarder in the research service (D61 item 4) | f33-review-286-key-1: PASS (7 hardening); f33-review-286-ledger-1: FAIL (shallow allowlist) -> 12 fixes; f33-rereview-286-1: PASS; strict-flag fix verified against Pi 0.87.1 source | Linux -race: researchroute, vrun, broker, piruntime, usdledger, cmd/tbound (lt286) |
| #287 | kit V route probe (D61 item 8) | f33-review-287-1: FAIL (stub start hang) -> 10 fixes; f33-rereview-287-1: FAIL (3 gaps) -> 5 fixes; mutation tests | test-vstub.sh 60 PASS; check-doc-links, comments, self-contained |
| #280 #282 #285 | other: adapt.py, ADAPT.md docs, walk bundles+evidence (owner-merged) | their own threads | - |
