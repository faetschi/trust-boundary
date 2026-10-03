# Hyper-V disposable restore acceptance

**Status: planned and untested.** No checkpoint restore has been performed. This runbook does not authorize the restore now: wait until the latest offline-verifier failure has been collected and diagnosed and the coordinator has issued the restore step. Temporary trusted maintenance networking is already approved; the required host NIC connect/disconnect remains a human Hyper-V action.

This is a narrow test of the existing checkpoint on the existing VM. Treat every command here as executable code; run only the listed trusted host and console operations. Do not run untrusted code, agent tasks, adversarial tests, or fault injection during this procedure. Do not create a checkpoint, export or duplicate the VM, expand its disk, or delete AVHDX files.

## 1. Preserve the missing evidence and freeze replay inputs

After the pending maintenance reconnect, copy the complete guest run directory ~/.tbound-offline-queued.qm5j16OwKf to a new directory under F:\TBoundAssets\Evidence. Preserve its status, completion, exit-code, launcher log, verifier JSON, and stderr files. Hash the host copy and confirm that no verifier or launcher remains active. Do not restore until the failure has been reviewed and the coordinator has issued the restore step.

Freeze the reviewed source commit and full commit ID in the evidence record before the restore. Prepare a source-only archive from that commit; never archive the guest home, caches, node_modules, credentials, or untracked files. The existing Export-TBoundGuestBundle.ps1 rejects any untracked file because it runs git status --porcelain=v1 --untracked-files=all; the current unrelated vm_start.txt therefore blocks that exporter. Leave it untouched. Use Git's commit-only archive operation instead, with an unused output path under F:\TBoundAssets\GuestHandoff:

    $repo = 'C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary'
    $commit = '<reviewed full commit ID>'
    $archive = 'F:\TBoundAssets\GuestHandoff\tbound-<commit-prefix>\tbound-<commit-prefix>.tar'
    $parent = Split-Path -Parent $archive
    if (-not [IO.Path]::IsPathRooted($archive)) { throw 'Archive path must be absolute.' }
    if (-not $archive.StartsWith('F:\TBoundAssets\GuestHandoff\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Archive must remain under the tracked handoff directory.' }
    if (Test-Path -LiteralPath $archive) { throw 'Archive already exists; choose a new path.' }
    if (Test-Path -LiteralPath $parent) { throw 'Archive parent already exists; choose a new unique directory.' }
    # Run the storage projection check described below before creating this directory.
    New-Item -ItemType Directory -Path $parent | Out-Null
    if (Test-Path -LiteralPath $archive) { throw 'Archive appeared before creation; stop.' }
    git -C $repo -c "safe.directory=$repo" archive --format=tar --prefix=tbound/ --output=$archive $commit
    if ($LASTEXITCODE -ne 0) { throw 'Source archive failed.' }
    Get-FileHash -Algorithm SHA256 -LiteralPath $archive

Before creating the parent directory or archive, run host Verify and estimate the archive size from that commit's tree. Include that size in the projection: tracked TBound files must remain at or below 100 GiB and free F: space at or above 700 GiB. Re-run Verify after archive creation. Validate the exact member list against the committed tree and require only regular files and directories; reject symlinks and special files. Record the commit ID and archive SHA-256. A Git archive uses the committed tree, so it omits the unrelated untracked file. See [Git archive documentation](https://git-scm.com/docs/git-archive).

Record the current guest hashes for supervisor/go.sum and infra/guest/verify-offline.py. The expected reviewed artifacts currently recorded are SHA-256 2ff7956b129800bfed3b5f9ca671ece52317fc4fa4786c76f4672abb075f1aea and bbcefad25a5184bb87b2efaba3b73b2480be609f6daff30a376aed9d184b2272. If both match the frozen commit, the Git artifact is sufficient; preserve only a hash manifest, not another source-tree copy. If either differs, preserve and review that exact file delta before proceeding.

Do not duplicate the Go or npm cache. Recreate the Go module cache after restore with the documented GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org go mod download all during trusted maintenance. Verify the recorded F: dependency-preparation artifact in place. Reuse the restored npm cache if present; if absent, prepare it from the pinned lockfile with lifecycle scripts disabled. Never copy node_modules or an arbitrary home directory.

Before and after each host-side artifact or evidence copy, use the documented storage checks. Keep tracked TBound files at or below 100 GiB and F: free space at or above 700 GiB. These measured-file checks are not hard quotas. Stop if a projected or measured limit fails.

## 2. Prove rollback with a plain-text marker

Use infra/hyperv/SETUP.md to confirm Basic Session and run Test-UbuntuHyperVVm.ps1 -Mode Verify with the existing VM, ISO, and hash arguments. The VM must be Off, its sole adapter disconnected, and the single named baseline checkpoint and complete disk chain must be present. Confirm the storage gates. Record the checkpoint name, CheckpointType, SnapshotType, disk chain, and verifier output.

Boot the VM with its adapter disconnected. At the Ubuntu console, choose a new short ID and record it on the host. Replace a91c2e below with that ID. If the first command does not print ABSENT_BEFORE, choose another ID. These are the only guest writes in the restore test:

    test ! -e "$HOME/.tbound-restore-a91c2e" && test ! -L "$HOME/.tbound-restore-a91c2e" && echo ABSENT_BEFORE
    (set -C; printf 'TBOUND restore a91c2e\n' > "$HOME/.tbound-restore-a91c2e")
    test -f "$HOME/.tbound-restore-a91c2e" && test ! -L "$HOME/.tbound-restore-a91c2e" && sha256sum "$HOME/.tbound-restore-a91c2e"

Require ABSENT_BEFORE and record the marker hash. Confirm both default-route commands print no route and that each non-loopback carrier is 0. Then shut Ubuntu down with sudo poweroff and run host Verify again while the VM is Off.

**Proceed only after the saved failure report has been reviewed and the coordinator has issued the restore step.** In Hyper-V Manager, select TBound-Ubuntu-2404, right-click ubuntu-24.04.5-golden-baseline, and choose Apply. If prompted, choose Apply alone; do not choose Create Checkpoint and Apply. Applying the selected checkpoint cannot be undone. See [Microsoft checkpoint guidance](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/checkpoints).

Afterward, confirm the VM is Off, the same single checkpoint remains, the disk chain is complete, the adapter remains disconnected, and host Verify passes. Boot offline and use these simple guest probes:

    test ! -e "$HOME/.tbound-restore-a91c2e" && test ! -L "$HOME/.tbound-restore-a91c2e" && echo RESTORE_PASS || echo RESTORE_FAIL
    ip -4 route show default
    ip -6 route show default
    for f in /sys/class/net/*/carrier; do echo "$f"; cat "$f"; done
    id -u
    hostname
    uname -r

Require RESTORE_PASS, no IPv4 or IPv6 default route, carrier 0 on every non-loopback interface, the recorded guest identity, and passing host Verify before and after boot. Ignore the loopback carrier. Record console output and host inventories. RESTORE_FAIL, a missing parent disk, an extra checkpoint, unexpected network state, or a storage-gate failure stops the procedure. Do not repair or delete checkpoint files.

## 3. Reapply reviewed state after the offline proof

Only after recording the successful offline marker check, shut down cleanly. The human operator may use the already-approved temporary Default Switch maintenance connection. Verify the guest SSH host key at its console and use the dedicated pinned host-key file; if it differs, stop and resolve the identity from the console. Re-enroll only the approved public key if needed. Keep the private key on the host.

Transfer and verify the frozen commit archive using infra/guest/SOURCE-HANDOFF.md. Extract it into a new guest directory and record source hashes. Recreate the Go module cache from the pinned go.mod and go.sum during trusted online maintenance. Recheck the npm cache and prepare it from the lockfile only if absent, with scripts disabled. Record cache paths and sizes; do not copy caches. Have the human disconnect the host NIC again, confirm the guest offline gate, shut down, and rerun host Verify before later trials.

## Interpretation

A passing marker test demonstrates that this checkpoint restored the tested guest file state on this VM. It does not establish production-checkpoint freeze/thaw behavior. The existing record reports SnapshotType=Standard while the VM policy is ProductionOnly and VSS is disabled; record both and make no freeze/thaw claim.
