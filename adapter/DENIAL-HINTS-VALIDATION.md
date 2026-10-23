# Denial-hint implementation receipt (2026-10-23)

Branch: `feat/denial-hints`; base: `acc1f5a`.
Implementation: `155024f8bc2799bbb9e3d5c5dace987e7589cf11`.
Final tested source: `79fd30e3262d35d3b80f4d8e74e606c3dcb9d054`.
The later documentation commit changes only this receipt and `TASK_STATE.md`.

## Contract and table

Exact model-facing format: unchanged compact Result JSON, one LF, `hint: `,
then the fixed sentence, with no final newline. This suffix appears only on a
known DENY or the existing ALLOW `command_failed` status. Successful ALLOW,
unknown reasons, and probe replies retain JSON-only text.

The complete final table is in `supervisor/internal/providerbridge/result_hints.go`;
the entire TypeScript copy `adapter/src/result-hints.ts` is golden-file checked.
`adapter/MODEL-FEEDBACK.md` documents the contract and table update procedure.

The bridge owns provider history; Pi does not submit tool-result text back over
the provider channel. Live construction and recovery both recompute the exact
hinted text. Replay refuses altered, omitted, or extended hints by comparing the
rebuilt request bytes. The new recovery test runs each replay in a fresh process.
The scripted transport removes only a verified fixed suffix, preserving D32's
Result-only source input and existing attacker projection.

## Verification

Initial red test: base production source `acc1f5a`, with the new
`TestDenialHintExactLiveAndReplay` fixture. Exit 1: live history omitted the hint;
exact hinted replay was refused; hint-free replay was incorrectly accepted.

Go commands ran through WSL from
`/mnt/c/Users/Admin/AppData/Local/Temp/tb-denial-hints/supervisor`, with `umask 077`
and `TMPDIR=/home/jeli2k/tmp`. The private temporary root and umask are necessary
for the audit journal's existing permissions checks.

```sh
/home/jeli2k/opt/go1.27.1/bin/go test ./internal/providerbridge/ ./internal/gate/ ./cmd/tbound/ ./internal/piruntime/ ./internal/broker/ ./internal/broker/protocol/ -count=1
/home/jeli2k/opt/go1.27.1/bin/go test ./internal/providerbridge/ ./internal/gate/ ./cmd/tbound/ ./internal/piruntime/ ./internal/broker/ ./internal/broker/protocol/ -json -count=1 > /home/jeli2k/tmp/tbound-denial-hints-go-79fd30e.jsonl
/home/jeli2k/opt/go1.27.1/bin/go vet ./internal/providerbridge/ ./internal/gate/ ./cmd/tbound/ ./internal/piruntime/ ./internal/broker/ ./internal/broker/protocol/
```

All exited 0. Final JSON run: 1,555 passing test/subtest records, 0 failed,
8 skipped. Skipped tests and prerequisites:

- `TestD09CellTemplateDraftIsNotAdmissible`: registered xattr scanner privilege profile.
- `TestServePiDevFixtureLaunchesActualPinnedPiOffline`: development-fixture environment variables.
- `TestRealProviderDurableLinux`: explicit real-provider/network enablement.
- `TestServiceProviderCredentialsRefuseForeignOwner`: root for a foreign-uid fixture.
- `TestKillCgroupEndsForkAndThreadChurnInARealCgroup`: delegated real cgroup.
- `TestOpenCellsCgroupChecksTheFilesystemAndTheInode`: delegated real cgroup.
- `TestKillCgroupNeverSucceedsWhileAZombieLeaderStillRuns`: delegated real cgroup.
- `TestResetHostRemovesNrCgroupsAndCreatesTheNetSinkParentOnRealCgroups`: delegated real cgroup.

All changed Go files were formatted with the pinned `gofmt`; a final `gofmt -l`
over those files emitted nothing (exit 0). `git diff --check` passed (exit 0).

Windows `npm.cmd run check` in `adapter/`: exit 0, typecheck passed, 36 tests
passed, 1 Linux-only CLI integration test skipped. Final Linux run used separately
installed official Pi 0.87.1 and qualified Node v24.21.0:

```sh
export PATH=/home/jeli2k/tmp/tbound-denial-runtime.UuQEmu/node-v24.21.0-linux-x64/bin:/usr/local/bin:/usr/bin:/bin
export TMPDIR=/home/jeli2k/tmp
export TBOUND_REQUIRE_PI_CLI_INTEGRATION=1
export TBOUND_PI_CLI_BIN=/home/jeli2k/tmp/tbound-denial-runtime.UuQEmu/pi/node_modules/.bin/pi
cd /mnt/c/Users/Admin/AppData/Local/Temp/tb-denial-hints/adapter
node node_modules/typescript/bin/tsc --noEmit && npm test
```

Exit 0: typecheck passed; 37 tests passed, 0 failed, 0 skipped. The CLI test
asserts the exact hinted completed tool-result event for all four tools.
The direct compiler entry avoids the Windows-generated `.bin/tsc` shim, which
references `node.exe` and cannot execute under Linux; an initial Linux
`npm run check` attempt failed on that shim before running tests.

From the worktree root, under WSL:

```sh
TMPDIR=/home/jeli2k/tmp python3 -m unittest discover -s scripts -p test_prepare_official_pi_runtime.py
bash -n scripts/build-release.sh
```

Both exited 0; Python ran 9 tests with no skips. An earlier Windows Python run
had 2 errors and 3 skips due to unsupported filesystem APIs, including
`os.utime(..., follow_symlinks=False)`; the Linux run supersedes it.

## Files touched

- `adapter/MODEL-FEEDBACK.md`
- `adapter/DENIAL-HINTS-VALIDATION.md`
- `adapter/src/ipc-transport.ts`
- `adapter/src/ipc-transport.test.ts`
- `adapter/src/proxy-tools.ts`
- `adapter/src/result-hints.ts`
- `adapter/src/tbound-cli-extension.test.ts`
- `scripts/build-release.sh`
- `scripts/prepare_official_pi_runtime.py`
- `supervisor/internal/piruntime/development_linux.go`
- `supervisor/internal/providerbridge/conversation.go`
- `supervisor/internal/providerbridge/conversation_test.go`
- `supervisor/internal/providerbridge/recovery.go`
- `supervisor/internal/providerbridge/result_hints.go`
- `supervisor/internal/providerbridge/result_hints_test.go`
- `supervisor/internal/providerbridge/result_hints_linux_test.go`
- `supervisor/internal/providerbridge/scripted.go`
- `supervisor/internal/providerbridge/scripted_test.go`
- `supervisor/internal/providerbridge/provider_e06_outside_read_disclosure_linux_test.go`
- `TASK_STATE.md`

## Freeze impact and remaining work

Rebuild and re-pin the supervisor executable and adapter runtime bundle, plus
dependent manifest/profile/registration pins, before the next freeze. This change
also changes model-facing provider request bytes and the measured C treatment.
The existing wire Result schema, gate, Result evidence/digests, receipt schema,
`internal/attacker`, and `corpus/` have no diff. No generated bundle, corpus pin,
or attacker artifact was rewritten. No push, signing, guest change, real-provider
call, full-repository race run, or parent full gate was performed.
