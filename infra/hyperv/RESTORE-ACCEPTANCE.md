# Hyper-V disposable checkpoint-restore acceptance (ABI 8 guest)

**Status: planned and untested.** No checkpoint has been created and no restore has been performed on this guest. **Every VM state change below (shutdown, NIC change, start, checkpoint create, Apply) requires explicit owner approval, given separately per action and released by the coordinator.** This file does not grant any of them. All steps are run by a human in an elevated Hyper-V admin shell or the VM console; automation and agents do not run them. Do not run untrusted code, agent tasks, or fault injection during this procedure.

Scope: file-state restore only, no freeze/thaw. A pass shows that this checkpoint restored the tested guest file state on this VM. It makes no claim about production-checkpoint freeze/thaw, containment, G1 or H1. Record two separate fields: the VM `CheckpointType` (the policy; SETUP.md requires ProductionOnly with VSS disabled; an earlier VM reported Standard) and each snapshot's `SnapshotType` (a per-snapshot classification, not the policy). A ProductionOnly VM may produce a Recovery or Standard snapshot depending on the host, so a reported Standard `SnapshotType` alone proves no fallback or policy discrepancy. Make no freeze/thaw claim either way.

## Target

| Item | Value | Source |
| --- | --- | --- |
| VM | `TBound-Ubuntu-2404-ABI8`, Hyper-V id prefix `df4745b2` (Generation 2) | abi8-guest-qualification-progress-20261015.json |
| VM root | `F:\TBoundVMs\TBound-Ubuntu-2404-ABI8` | same |
| Guest | hostname `tbound-abi8-seed`, kernel `7.0.0-38-generic`, Ubuntu 24.04.5 LTS | abi8-readonly-remeasure-20261017.json |
| Runtime-bundle digest | `sha256:d7367e1b58db53c35fa967c333340599ea30aa43471d3a61c3c8cbbb59246884` (`tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime`; same under uid 995 and `tboundadmin`) | same |
| Checkpoints now | 0 (coordinator-stated; the 2026-10-17 re-measure could not query Get-VM) | coordinator |
| Network now | `Default Switch`; guest at 172.22.198.27 | re-measure receipt |

The retired VM `TBound-Ubuntu-2404` and its `ubuntu-24.04.5-golden-baseline` checkpoint are out of scope.

SSH reaches the guest only while its NIC is connected to `Default Switch`: Git Bash `ssh.exe`, the pinned known_hosts file, `StrictHostKeyChecking=yes`, plus `-o HostKeyAlias=172.18.155.19` for the new address. The restore test below runs with the NIC disconnected, so every guest step uses the VMConnect console. Reconnecting the NIC is a separate approved action and is not part of this test.

## 1. Preconditions (read-only; stop on any failure)

1. Confirm VMConnect shows Basic Session with no clipboard or device redirection (SETUP.md, "Clipboard and VMConnect session").
2. Shut the guest down gracefully from its console (`sudo poweroff`). Never `Stop-VM`. Disconnect the sole adapter, eject any ISO, and keep the installed disk first in boot order (SETUP.md Phase 2, steps 5-6).
3. With the VM Off, record state, adapter, snapshots and the disk chain:

       $vmName = 'TBound-Ubuntu-2404-ABI8'
       $vmRoot = 'F:\TBoundVMs\TBound-Ubuntu-2404-ABI8'
       Get-VM -Name $vmName | Select-Object Name, Id, State, CheckpointType
       Get-VMNetworkAdapter -VMName $vmName | Select-Object Name, SwitchName, Connected
       Get-VMSnapshot -VMName $vmName | Select-Object Name, SnapshotType
       Get-VMHardDiskDrive -VMName $vmName | ForEach-Object { Get-VHD -Path $_.Path } | Select-Object Path, VhdType, ParentPath

   Require: Off; exactly one adapter, disconnected; the Id begins `df4745b2`; the disk chain is complete and no deeper than 2.
4. Run host Verify with `$vmName`, `$vmRoot` and the ISO, hash and switch variables from SETUP.md (`-Mode Verify ... -BasicSessionConfirmed`). Require tracked TBound files at or below 100 GiB and F: free at or above 700 GiB. These are measured-file checks, not quotas; re-run them after each step that writes to F:.

## 2. Create the checkpoint

Allowed only when step 1 passes, no checkpoint exists, and the owner has approved this action. Use the command from SETUP.md Phase 2, step 8, with this VM and a recorded name:

    $cp = 'abi8-restore-baseline'   # owner may choose another name; record it
    Checkpoint-VM -Name $vmName -SnapshotName $cp

If it fails, stop and keep the error. Do not fall back to a Standard checkpoint, change the VSS policy, or create a second checkpoint. Then confirm the VM is Off, exactly one checkpoint exists, and host Verify passes (checkpoint files count toward the 100 GiB budget). Record this "before" Verify output and the disk chain (now base VHDX plus one AVHDX).

## 3. Marker test (the only guest writes)

Boot the VM with the NIC still disconnected and open the console. Choose a short ID (below, `a91c2e`) and record it on the host. If the check does not print `ABSENT_BEFORE`, choose another ID. The marker is created at an exact absolute path, never `$HOME` or `~`. The path below assumes the account home is `/home/tboundadmin`; confirm with `getent passwd tboundadmin | cut -d: -f6` and substitute if it differs.

    id -u
    cat /proc/sys/kernel/random/boot_id
    test ! -e /home/tboundadmin/.tbound-restore-a91c2e && test ! -L /home/tboundadmin/.tbound-restore-a91c2e && echo ABSENT_BEFORE
    (set -C; printf 'TBOUND restore a91c2e\n' > /home/tboundadmin/.tbound-restore-a91c2e)
    sha256sum /home/tboundadmin/.tbound-restore-a91c2e

Record the boot ID, the marker path, the creating UID (`id -u`) and the marker hash. Confirm no default route (`ip -4 route show default`, `ip -6 route show default`) and carrier 0 on every non-loopback interface. Run `sudo poweroff`, wait for Off, and run host Verify again.

## 4. Apply (irreversible)

**Applying a checkpoint cannot be undone.** Everything written to the VM after the checkpoint is discarded, including the marker. Proceed only with the owner's explicit approval and the coordinator's release, with the VM Off and section 3 recorded.

    Restore-VMSnapshot -VMName $vmName -Name $cp    # answer the confirmation prompt yourself

In Hyper-V Manager the equivalent is Apply on the checkpoint; do not choose "Create Checkpoint and Apply". Afterwards confirm the VM is Off, the same single checkpoint remains, the disk chain is complete, the adapter is still disconnected, and host Verify passes. If a parent disk is missing, a second checkpoint appears, the network state is unexpected, or a storage gate fails, stop. Do not repair or delete checkpoint files.

## 5. Verify after restore

Boot offline and run at the console. Run as the SAME UID recorded as `marker_uid` in section 3 and show the output. If `id -u` differs, the result is INCONCLUSIVE, not a pass: another account may not see the marker and report it absent. Check the exact absolute marker path, never `$HOME`.

    id -u
    test ! -e /home/tboundadmin/.tbound-restore-a91c2e && test ! -L /home/tboundadmin/.tbound-restore-a91c2e && echo RESTORE_PASS || echo RESTORE_FAIL
    hostname; uname -r; cat /proc/sys/kernel/random/boot_id
    tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime
    ip -4 route show default; ip -6 route show default
    for f in /sys/class/net/*/carrier; do echo "$f"; cat "$f"; done

Pass requires all of: `RESTORE_PASS` at the exact marker path; `id -u` equal to `marker_uid` (else INCONCLUSIVE); hostname `tbound-abi8-seed`; kernel `7.0.0-38-generic`; digest equal to `sha256:d7367e1b...` above; a boot ID different from the marker-boot ID (a fresh boot; the 2026-10-17 value `f94189ae-...` is not expected to recur); no default route; carrier 0 on every non-loopback interface (ignore loopback). Then shut down and run host Verify once more with the VM Off.

## Receipt template (planned)

Write one JSON receipt under `docs/evidence/` after the run. Include no keys, key paths or secrets.

    {
      "receipt": "abi8-checkpoint-restore-<yyyymmdd>",
      "status": "RESTORE_PASS | RESTORE_FAIL | INCONCLUSIVE | NOT_RUN",
      "approvals": [{"action": "...", "approvedBy": "owner", "releasedBy": "coordinator", "utc": "..."}],
      "vm": {"name": "TBound-Ubuntu-2404-ABI8", "id": "df4745b2-...", "checkpointType": "<VM CheckpointType>", "stateBeforeApply": "Off", "stateAfterApply": "Off"},
      "checkpoint": {"name": "...", "snapshotType": "<snapshot SnapshotType>", "count": 1, "createdUtc": "..."},
      "diskChain": {"beforeApply": ["<path>", "..."], "afterApply": ["<path>", "..."], "complete": true},
      "hostVerify": {"beforeApply": {"exit": 0, "output": "<ref or sha256>"}, "afterApply": {"exit": 0, "output": "<ref or sha256>"}},
      "storage": {"trackedBytes": 0, "freeBytes": 0},
      "marker": {"id": "...", "marker_path": "/home/tboundadmin/.tbound-restore-<id>", "marker_uid": 0, "verify_uid": 0, "absentBefore": true, "sha256": "...", "markerBootId": "...", "result": "RESTORE_PASS | RESTORE_FAIL | INCONCLUSIVE"},
      "guestAfterRestore": {"hostname": "tbound-abi8-seed", "kernel": "7.0.0-38-generic", "bootId": "...",
                            "runtimeBundleDigest": "sha256:d7367e1b...", "digestMatchesRemeasure": true},
      "network": {"adapterConnected": false, "defaultRoutes": "none", "nonLoopbackCarrier": "0"},
      "claims": {"freezeThaw": false, "containment": false, "G1": false, "H1": false}
    }
