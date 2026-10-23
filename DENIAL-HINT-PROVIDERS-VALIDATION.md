# Denial hint providers validation ? 2026-10-23

Validated code commit: `2df906a83137f63f87f9fc29ea77083347efda37` on `feat/denial-hints`, isolated worktree `C:/Users/Admin/AppData/Local/Temp/tb-denial-hints`. Documentation-only commits follow. No push, live provider request, secret-file access, main-checkout modification or F: asset modification was performed. Public OpenRouter documentation was read to confirm the Decisions request/answer schema.

## Final checks

Go targeted tests: exit 0; 1255 tests passed, 0 failed, 8 environment-gated tests skipped. Final command, from this worktree in Windows PowerShell:

```powershell
$env:MSYS_NO_PATHCONV='1'
wsl.exe -e bash -lc 'export TMPDIR=$(mktemp -d /home/jeli2k/tmp/h.XXXX); umask 077; cd /mnt/c/Users/Admin/AppData/Local/Temp/tb-denial-hints/supervisor && /home/jeli2k/opt/go1.27.1/bin/go test -json ./internal/providerbridge/ ./cmd/tbound/ ./internal/workflowrecovery/ ./cmd/tbound-hint-eval/ -count=1'
```

The same four packages were checked with `/home/jeli2k/opt/go1.27.1/bin/go vet` in the same WSL/private-TMPDIR/umask environment (exit 0). All changed Go files were formatted with that distribution's gofmt. `git diff --check` passed. No full-repository race loop was run.

Windows adapter, cwd `adapter/`: `npm.cmd run check`, exit 0: tsc passed; 36 tests passed, zero failed, one skipped. Adapter runtime sources are identical to base bf9c30f (only MODEL-FEEDBACK.md changes in round 2). The official Pi integration is Linux-only and requires the separately installed CLI 0.87.1 and qualified Node v24.21.0; the Windows invocation intentionally skips it. No new skip was introduced by adapter code changes. The Linux-qualified Pi integration was not rerun in round 2.

The eight Go environment skips were:

- TestServePiDevFixtureLaunchesActualPinnedPiOffline: separate qualified Pi/Node/adapter/root fixture paths not supplied.
- TestRealProviderDurableLinux: live-provider tests deliberately disabled.
- TestServiceProviderCredentialsRefuseForeignOwner: needs root to create a foreign-owner file.
- TestReservedCapacityFilesystemExperiment: opt-in D14 ext4 experiment.
- TestKillCgroupEndsForkAndThreadChurnInARealCgroup: delegated real-cgroup fixture not enabled.
- TestOpenCellsCgroupChecksTheFilesystemAndTheInode: same prerequisite.
- TestKillCgroupNeverSucceedsWhileAZombieLeaderStillRuns: same prerequisite.
- TestResetHostRemovesNrCgroupsAndCreatesTheNetSinkParentOnRealCgroups: same prerequisite.

## Covered behavior

Existing static model-facing bytes and Go/TS parity; immutable complete candidate-table digest; choice success, low confidence, unknown candidate/type, non-finite/out-of-range confidence, timeout, disabled/config errors and HTTP failures/oversize/malformed responses. Local fake HTTP covers exact ID output, grammar/greedy controls, complete candidate logprobs and stable softmax, absent/truncated logprobs, low confidence, extra text/whitespace and rejection of public endpoints before dispatch. Manifest tests bind candidates, inputs, thresholds, timeout, model IDs, endpoint, transport and local model-file digest, and reject unresolved/extra chooser entries.

Fresh-process journal replay covers gate DENY, admission DENY and ALLOW+command_failed for static, decision choice, local classifier, timeout fallback and legacy round-1 static records. It checks subsequent provider request byte identity without calling the configured chooser. Changing a selected candidate while preserving the next recorded request fails byte-match verification. The full cmd/tbound gate also exercises the actual Store cross-link decoder; its new optional HintSelection field fixes the initially observed admission-denial incompatibility.

## Offline harness

From supervisor, in the same private WSL environment:

```sh
/home/jeli2k/opt/go1.27.1/bin/go run ./cmd/tbound-hint-eval
```

Exit 0. Twenty frozen synthetic scenarios each; sample output:

```text
static             fallback=0.00 rejection=0.00 mean_ms=0.00 p95_ms=0  mean_bytes=90.90 max_bytes=104 next=7/7/6
offline/chooser-a  fallback=0.40 rejection=0.20 mean_ms=3.95 p95_ms=5  mean_bytes=76.20 max_bytes=104 next=6/7/7
offline/chooser-b  fallback=0.30 rejection=0.00 mean_ms=9.50 p95_ms=11 mean_bytes=84.85 max_bytes=104 next=7/6/7
```

next is alternative_allowed/retry_denied/no_further_action. These timings, answers and continuation outcomes are fake test recordings, not model or pilot results. No data-bearing robustness claim follows. The future offline recorded-response command is `go run ./cmd/tbound-hint-eval -fixtures <recorded-scenarios.json> -manifest <finalized-cdh-manifest.json>`; it requires exactly the two manifest models. It cannot collect or call a live model.

## Pins and deferred work

- Provider history digest domain advances from tbound/provider-history/v1 to v2. Provider journal heads/transcript commitments and the supervisor executable must be re-pinned before freeze. No existing external pin/manifest file was rewritten. Wire Result bytes/digests are unchanged. Adapter runtime/attacker/pilot corpus files are unchanged in round 2.
- Immutable candidate table SHA-256 (compact sorted JSON): e28ad5dac7c75e7cfb23a0fd22ff946218f5dffe8c56754724e31f01611f1d9e.
- C-DH manifest digest is intentionally unresolved until the owner chooses the second model, local file digest/endpoint or hosted route, confidence threshold and timeout. H1 C remains static. No C-DH runner enum/schedule was added; exact scheduling, host binding, attestation, accounting, recovery and report seams are in docs/design/denial-hint-providers.md.
- Product concrete-call dry-run and funded/live collection remain unimplemented, as directed. Local deployment must attest the loaded model file against its pinned digest; injected clients must honor contexts, refuse redirects and disable proxies for local inference. All transports default off and all HTTP tests use fakes.

## Changed code/fixture bytes at validated commit

SHA-256 over worktree bytes (Windows working-tree line endings), listed only for changed code and fixtures:

```text
f150d27d9f9bb7be4cf22687da78bf9816fa16df27c60a1e5e9c53f549461916  supervisor/cmd/tbound-hint-eval/main.go
a581833e3df0fb78834f3fa39c628f9c16f92602743d7eeab6507167c63d6c95  supervisor/cmd/tbound/governed_session_linux.go
05448057922b457813b5eef9060377a0622803c4559d110aebcc3836292c752e  supervisor/internal/providerbridge/conversation.go
817e38e4f67e00bfc526f2ecba13c69946b15ec22c08f1d52dd48f9ea1de54e2  supervisor/internal/providerbridge/hint_decisions.go
f867a462ee2a4bfd8d9f26b4dbcc2f78972dfce4cd01f26f2406dc2e24fd7612  supervisor/internal/providerbridge/hint_eval.go
6026bdc129717f037f0468dbc958f3d3db0c7e112308f1e95171e30436909c00  supervisor/internal/providerbridge/hint_eval_test.go
b9c8fd2e02f5de30d3e1e0eaa8b75e1f874aeb06e65d143e212bea500722c600  supervisor/internal/providerbridge/hint_local.go
1b95ecb8a3638ce066356593753d14133fb7f3b7fbce6655f59f6de01f25e183  supervisor/internal/providerbridge/hint_local_test.go
a18b75356e5b511a16bb03e5675a67f90aeb19d80bfb5cd131e4e758f314f132  supervisor/internal/providerbridge/hint_manifest.go
4acc528dfb80d499022f1731ea77cefc4602f2755d6832a02c8bb6a7f2b3e7d4  supervisor/internal/providerbridge/hint_provider.go
7510ac6310edf2d35f29b245c44899b108ec005f05daa637f3b72798e4ac7a96  supervisor/internal/providerbridge/hint_provider_test.go
90c5279e64052b231bef2315ae46546be25d7b3cc77d4af70ea3cf3dee939173  supervisor/internal/providerbridge/hint_replay_linux_test.go
98d33b608769bd217d7babd1cdf0b7e570915cbc743d409da75637bde148d318  supervisor/internal/providerbridge/recovery.go
21cfd12cbffb47f419c025e2c2a5db8f5180e652e4e3863cf2f5ee9a78fea768  supervisor/internal/providerbridge/result_hints.go
2de5452c1d4b72fd94abc0441332053b4f4a6526a642136f6e0fb4413cd27a98  supervisor/internal/providerbridge/testdata/hint_eval.json
```
