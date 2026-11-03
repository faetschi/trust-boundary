# Product Bash review fixes, 2026-11-03

Worktree: `.local/worktrees/product-bash-runner`, branch `feat/product-bash-runner`, starting at `0ff559d400c9195767a00537be0cc501fc3978b2`. No push or demo-VM access.

## Disposition and commits

1. Fixed: `9624eeff` adds the red effective-environment and setup-agreement tests; `6114d143` admits exactly the nine fixed product assignments, independent of ordering, and refuses missing, extra, duplicate or changed assignments. `supervisor/internal/piruntime/product_d09_host_linux.go:673`.
2. Fixed: `0fed4aff` adds a deterministic pathname-replacement regression with a private test policy; `6a88d5a6` opens every directory with descriptor-relative `openat(O_PATH|O_DIRECTORY|O_NOFOLLOW)`, validates descriptor metadata, and opens the leaf relative to the verified parent with `O_NOFOLLOW|O_NONBLOCK`. `6e4c7fd1` moves replacement before the next component lookup, following independent review. Production stays root-only. `product_d09_host_linux.go:718`, `:862`; `product_d09_reader_linux_test.go:47`.
3. Fixed: `ced2d4b9` allocates the fixture before poisoning TMPDIR, retaining the poison during child execution. `product_d09_environment_linux_test.go:97`.
4. **Partial, still open:** `8b9df9ec` dynamically checks child environments for probe/info, actual offline cosign verification, inspect/exists/removal helpers, failed-launch cleanup, and command cleanup. It checks failed-launch Close refusal and idle Close. A static AST guard pins each environment call site and catches individual reverts. It does **not** execute command launch, worker launch, post-exit inspection, or the Run deferred-cleanup path through their real admission/cgroup/proof gates. A started sleep process exercises owned failed-launch cleanup, not a genuine cell. No admission, cgroup, namespace, or proof validation was weakened to manufacture coverage. `product_d09_environment_linux_test.go:97`, `product_d09_environment_callsite_linux_test.go:89`.
5. Fixed: `2c29eff5` removes the masked filesystem refusal cases. Every new leaf/ancestor case proves a successful control read first, then changes one property with accepted ancestors; the fixture translates only the trusted test UID to root for the unchanged production metadata predicates. Includes writable modes, foreign-owner policy, symlinks, missing/oversized/non-regular leaves. `product_d09_reader_linux_test.go:97`, `:156`.
6. Fixed: `836b96df` adds supplementary command success/denied event, proof, receipt, and managed-root signing-byte goldens generated from `git archive 21881490238a0ee1692852f1f4cfb702b063142f`, with only the new tests and temporary generator wrappers copied into that baseline. Baseline generation and both baseline comparison tests passed before export. Fixtures freeze serialization and signing messages, not live containment or a journal-backed verification. `product_d09_thesis_supplementary_linux_test.go:129`, `supervisor/cmd/tbound/product_thesis_command_event_linux_test.go:104`. Expected-file hashes are in `supervisor/internal/piruntime/testdata/thesis-d09-supplementary/SHA256SUMS`.

The original Unit 0 file `product_d09_thesis_compatibility_linux_test.go` is byte-identical: working-file SHA-256 `d1596b1ad36fa53a4459830bbfaf8aecaa7bf82e90224e8f1943e1c08398edc8`.

Added the requested unit E admission/revalidation/probe-environment warning to the gitignored design's Carry-forward notes (`.local/product-demo/step2-design.md`). No thesis production files or shared record formats changed.

## Execution evidence

Linux used only `wsl.exe -d tbound-dev -u tbound`, after claiming the free coordination line. Git Bash `MSYS_NO_PATHCONV=1` streamed git archives to private scratch under `/home/tbound/tbound-work/product-bash-review*`; `/home/tbound/env.sh` supplied the toolchain. No `/mnt` access.

The two red-test commits were executed from their archived historical trees after the Linux lease became free, rather than before the fix commits were authored (the lease was held while implementation proceeded):

- `9624eeff`: `TestProductD09EffectiveUnitRefusesOverridesAndPendingReload` failed: `installed effective unit refused: ... effective product unit differs from the installed unit`.
- `0fed4aff`: `TestProductD09ReaderKeepsVerifiedParentDuringReplacement` failed: `read lost its verified parent ... protected ancestor changed while it was opened`.
- Current versions passed: effective-unit regression, setup agreement, and all `TestProductD09Reader*` tests.

Windows cross-checks: `GOOS=linux go vet ./internal/piruntime ./internal/productsetup`, plus `go test -c` for piruntime and cmd/tbound, passed. These were compile/static checks, not Windows test execution.

Linux full-suite results (all `-count=1`):

```
ok  tbound/supervisor/internal/piruntime     3.342s
ok  tbound/supervisor/internal/productsetup 11.038s
ok  tbound/supervisor/cmd/tbound            283.990s  (-run Product)
```

Mutation evidence: 32 individual mutations produced deliberate test failures, not compile failures. `mutations.txt` preserves each mutation label and failing test names. All changes were local to the isolated Linux scratch copy and restored immediately in `finally` blocks.

- Finding 3 (1): moving poison before fixture creation fails with `TempDir: stat /tmp/attacker: no such file or directory`.
- Finding 4 (17): individually reverted all 11 runner/launcher environment-use expressions, both environment selectors, the loader's cosign environment, and all three helper environment assignments to `d09ProbeEnv()`. Every revert failed the source guard; selector/helper mutations additionally failed dynamic child-environment assertions. Guard-only cases remain source evidence and do not close the launch-path requirement.
- Finding 5 (6): disabling leaf/ancestor owner checks, writable-mode checks, or `O_NOFOLLOW` makes the relevant otherwise-valid filesystem fixture accept the bad input. Each fails the intended leaf or ancestor assertion.
- Finding 6 (6): proof JSON field, receipt JSON field, managed-root signature domain, success-event transition field, denied-event candidate-digest field, and typed pre-start denial. Each new supplementary golden/denial assertion fails. The domain anchor was narrowed after a harness assertion found two occurrences; the remaining checks then completed.
- Finding 2 (2 additional checks): reverting either the intermediate directory open or leaf open to absolute lookup returns `substituted` bytes and fails the replacement regression.

After restoration, all six affected file hashes matched their committed Git blob IDs exactly. Focused `TestProductD09*`, `TestThesisD09*`, and `TestProductThesisCommandEventBytesUnchanged` passed again (0.073s, 0.007s, 0.005s).

The self-contained-workspace check passed (`OK: no outside paths`). The archive initially had no Git metadata; a scratch-only index populated from the worktree's exact `git ls-files -z` list supplied the metadata required by the checker.


## Limits and handoff

Finding 4 remains open for dynamic coverage of actual admitted command/worker launch, post-exit inspection and deferred Run cleanup. The static guard is supplementary protection, not a replacement for those paths. No real product Bash launch, demo-VM run, opt-in D09 integration, privileged containment qualification, race suite, or full unfiltered cmd/tbound suite was performed. Existing package-level conditional skips are not containment evidence. Supplementary fixtures pin synthetic serialization bytes and signing messages, not live signed receipts or journal-backed verification.

All four Linux scratch trees were removed using their verified hard-coded absolute paths; each absence was checked. The Linux coordination line was released to free. The worktree is clean after the final evidence commit.
