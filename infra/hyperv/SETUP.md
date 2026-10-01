# Hyper-V Ubuntu VM setup

This directory contains a two-phase setup for an Ubuntu Server 24.04.5 AMD64 VM. The scripts create only the named VM and new files under the requested F: path. They do not download installation media or remove existing objects.

## Fixed VM profile

- Hyper-V Generation 2
- Ubuntu Server 24.04.5 AMD64 ISO
- 8 virtual processors
- 16 GiB fixed startup memory
- 40 GiB maximum dynamic VHDX on F:
- Secure Boot enabled with the Microsoft UEFI Certificate Authority template for Linux
- ProductionOnly checkpoints; automatic checkpoints disabled
- Exactly one Hyper-V network adapter, disconnected at creation and at the golden baseline

The integration service allowlist keeps **Heartbeat**, **Shutdown**, and **Time Synchronization** enabled. Heartbeat lets Hyper-V report whether the guest OS booted; Shutdown permits a graceful host-requested shutdown; Time Synchronization limits guest clock drift. The setup disables **Guest Service Interface** (host/guest file copy), **Key-Value Pair Exchange** (host/guest metadata exchange), and **VSS** (live guest backup coordination). It identifies services by the GUID suffix in `Get-VMIntegrationService.Id`, scoped to the target VM's GUID, so localized display names do not affect policy. The six expected service IDs were checked against this Windows host; unknown, missing, duplicate, or differently scoped IDs fail closed. Microsoft documents the Hyper-V resource component's `Name` as a language-neutral service key and the integration-service cmdlets can take the component object directly: [Msvm_VirtualSystemResourceComponent](https://learn.microsoft.com/en-us/windows/win32/hyperv_v2/msvm-virtualsystemresourcecomponent), [Enable-VMIntegrationService](https://learn.microsoft.com/en-us/powershell/module/hyper-v/enable-vmintegrationservice?view=windowsserver2025-ps), [Disable-VMIntegrationService](https://learn.microsoft.com/en-us/powershell/module/hyper-v/disable-vmintegrationservice?view=windowsserver2025-ps).

## Storage budget and operating limits

- TBound is limited to 100 GiB of tracked files on F:, including the ISO and other assets, VM files, the VHDX, and checkpoint files. The read-only verifier recursively totals files in only `F:\TBoundAssets` and `F:\TBoundVMs`; it refuses reparse points, junctions, and symbolic links rather than following them. Keep all TBound files under those two directories so the budget scan includes them. It does not scan the rest of F: or other volumes.
- Keep at least 700 GiB free on F:. Preflight requires 800 GiB free before creation (700 GiB reserved for the host plus up to 100 GiB of TBound files); the creator repeats that 800 GiB gate. Later checks fail below the 700 GiB free reserve or above the 100 GiB tracked-file budget.
- The dynamic VHDX has a 40 GiB virtual maximum. It grows physically as the guest writes data. Do not resize or expand it, and create at most one ProductionOnly checkpoint. Do not make a separate golden export or duplicate VM under this budget.
- These checks report current file sizes and free space; they are not a hard storage quota. Guest writes, checkpoint growth, filesystem metadata, other host activity, or files outside the tracked directories can change actual volume use between checks. Re-run Verify while the VM is off and disconnected before each untrusted run, and stop before tracked use exceeds 100 GiB or free space approaches the 700 GiB reserve.

## Phase 1: prepare and create the disconnected VM

1. Enable Hyper-V on the host if needed. Confirm that the Hyper-V service is running and the Hyper-V PowerShell module is available.
2. Confirm `F:\TBoundAssets` contains the ISO and create `F:\TBoundVMs` in File Explorer if it does not exist. The scripts require both tracked roots and the VM parent to exist; `F:\TBoundVMs\TBound-Ubuntu-2404` itself must be absent. Existing reparse points, junctions, and symbolic links along or inside the tracked storage paths are rejected.
3. Download `ubuntu-24.04.5-live-server-amd64.iso`, `SHA256SUMS`, and `SHA256SUMS.gpg` from the official [Ubuntu 24.04.5 release directory](https://releases.ubuntu.com/24.04.5/).
4. Verify the signature on the checksum file before trusting its SHA-256 entry. This host already has Git for Windows GPG at the path below:

   ~~~powershell
   & 'C:\Program Files\Git\usr\bin\gpg.exe' --keyid-format long --verify .\SHA256SUMS.gpg .\SHA256SUMS
   if ($LASTEXITCODE -ne 0) { throw 'Ubuntu checksum signature verification failed.' }
   ~~~

   GPG must report a good signature from an Ubuntu signing key. If the public key is missing, follow Ubuntu's [download verification guide](https://ubuntu.com/tutorials/how-to-verify-ubuntu) to obtain it and verify its full fingerprint against Ubuntu's published key information. Do not accept an unknown key based only on its short key ID.
5. Read the SHA-256 value for `ubuntu-24.04.5-live-server-amd64.iso` from the verified `SHA256SUMS`. Compare it with `Get-FileHash -Algorithm SHA256 -LiteralPath <ISO path>`. Supply that 64-character signed value to both scripts as `ExpectedIsoSha256`.
6. Open PowerShell as Administrator. The example uses the existing `Default Switch`, a host-managed NAT switch, for temporary Ubuntu installation/provisioning connectivity only. Do not use it during untrusted runtime. The scripts only check its name and leave the VM adapter disconnected at creation and at the golden baseline.
7. The ISO checksum below was read from Ubuntu's signed checksum file and independently verified with an Ubuntu signing key. Run the read-only preflight first. Do not run the creator unless every preflight check passes:

   ~~~powershell
   $vmName = 'TBound-Ubuntu-2404'
   $vmRoot = 'F:\TBoundVMs\TBound-Ubuntu-2404'
   $isoPath = 'F:\TBoundAssets\ubuntu-24.04.5\ubuntu-24.04.5-live-server-amd64.iso'
   $isoSha256 = '97F3D7FFB032C3EB3B23D2C8BE9CC76E60C2C1F2C0146BA5BA9FE01CAFAE0FD8'
   $switchName = 'Default Switch'

   .\Test-UbuntuHyperVVm.ps1 -Mode Preflight -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName
   ~~~

   Only after preflight passes, create the VM:

   ~~~powershell
   .\New-UbuntuHyperVVm.ps1 -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName
   ~~~

   If creation succeeds, verify its initial state:

   ~~~powershell
   .\Test-UbuntuHyperVVm.ps1 -Mode Created -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName
   ~~~

The creator refuses an existing VM name, VM root, or target path; an absent switch; an unreadable or untracked ISO; a checksum mismatch; unsafe reparse-point components; tracked TBound files above 100 GiB; and less than 800 GiB free on F:. It creates a 40 GiB dynamic VHDX and stores checkpoint files under the tracked VM directory. It does not use `-Force`, download files, remove partial output after an error, or export a golden VM. If setup stops partway, do not rerun the creator or delete anything. Inspect Hyper-V Manager and the VM directory first.

### Resume the currently inspected partial VM

The current host has an inspected partial VM named `TBound-Ubuntu-2404`: it is Off, Generation 2, stored at `F:\TBoundVMs\TBound-Ubuntu-2404\TBound-Ubuntu-2404`, has one disconnected adapter, and has no hard disk. The explicit repair script is restricted to that name and root. It additionally requires the VM to have 8 processors, fixed 16 GiB memory, the expected checkpoint settings, no checkpoint, no DVD, an empty `Virtual Hard Disks` directory, and exactly the six known service IDs scoped to this VM. It checks the ISO hash and storage budget again. It only proceeds after all preconditions match; it leaves the VM Off and disconnected and does not remove anything. If a check fails, stop and inspect the current state.

With the variables from step 7 still set, preview the action and then run the repair command. The actual command prompts through PowerShell `ShouldProcess` after its read-only checks:

~~~powershell
.\Repair-TBoundUbuntuHyperVVm.ps1 -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName -WhatIf
.\Repair-TBoundUbuntuHyperVVm.ps1 -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName
.\Test-UbuntuHyperVVm.ps1 -Mode Created -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName
~~~

The repair script applies the ID-based service policy, adds the verified ISO and a 40 GiB dynamic VHDX, and sets the Linux Secure Boot template. It does not start the VM or connect its adapter. Only proceed to the interactive install steps after the read-only `Created` check passes.

## Phase 2: install, provision temporarily, then disconnect

1. Run the complete read-only Created check below. The new VM should be Off, have the verified installer ISO as its first boot device, and have exactly one disconnected adapter:

   ~~~powershell
   .\Test-UbuntuHyperVVm.ps1 -Mode Created -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName
   ~~~

2. In elevated PowerShell, connect the adapter to the host-managed `Default Switch` only when installation or guest provisioning needs connectivity, then start the VM. Never use this network during untrusted runtime; disconnect it before running any untrusted code and before creating the baseline:

   ~~~powershell
   Get-VMNetworkAdapter -VMName $vmName | Connect-VMNetworkAdapter -SwitchName $switchName
   Start-VM -Name $vmName
   ~~~

3. Open the VM in Hyper-V Manager and complete the Ubuntu Server installer interactively from the attached ISO. Create the guest account and any credentials interactively; do not place passwords or keys in these scripts or this guide. Use the temporary network only for installation and guest provisioning work that requires connectivity.
4. Before running untrusted code, and before shutting down for the baseline, inspect the running VMConnect session. Confirm it shows **Basic Session** (or Enhanced Session unavailable) and that clipboard and device redirection are unavailable. If you cannot confirm this, do not run untrusted code or create the baseline.
5. When provisioning is finished, shut down Ubuntu from its console (`sudo poweroff`) and wait until `Get-VM -Name $vmName` reports `Off`.
6. **Before creating the golden baseline, disconnect every network adapter, set the installed VHDX first in boot order, and eject the installer ISO.** Run this in elevated PowerShell:

   ~~~powershell
   Disconnect-VMNetworkAdapter -VMName $vmName
   $disk = Get-VMHardDiskDrive -VMName $vmName
   Set-VMFirmware -VMName $vmName -FirstBootDevice $disk
   Get-VMDvdDrive -VMName $vmName | Set-VMDvdDrive -Path $null
   Get-VMNetworkAdapter -VMName $vmName | Format-Table Name, SwitchName
   ~~~

7. Run final read-only verification after confirming the Basic Session state. Pass `-BasicSessionConfirmed` only after observing it while the guest is running:

   ~~~powershell
   .\Test-UbuntuHyperVVm.ps1 -Mode Verify -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName -BasicSessionConfirmed
   ~~~

   Verify requires the VM to be Off, exactly one network adapter disconnected, the installation ISO ejected, the hard disk first in boot order, and the operator's Basic Session attestation. Without that attestation, it labels the baseline incomplete and exits with code 2.
8. After all checks pass, create the ProductionOnly golden checkpoint:

   ~~~powershell
   Checkpoint-VM -Name $vmName -SnapshotName 'ubuntu-24.04.5-golden-baseline'
   ~~~

   Treat this as a host-specific compatibility gate. Microsoft documents production checkpoints as using VSS or Linux file-system freeze, and documents the VSS integration service / `hv_vss_daemon` for live guest backups. Its documentation does not establish whether a cleanly powered-off Linux VM can create a `ProductionOnly` checkpoint while VSS is disabled ([checkpoint behavior](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/checkpoints), [integration services](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/integration-services), [Linux VSS daemon](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/manage/manage-hyper-v-integration-services)). Accept the baseline only if the command above succeeds on this host after all prior gates pass. If it fails, stop and retain the error for diagnosis; do not fall back to a Standard checkpoint or change the VSS service policy. After success, use these read-only checks to confirm the VM remains Off and exactly one checkpoint exists, then run the Verify command in step 9:

   ~~~powershell
   Get-VM -Name $vmName | Select-Object Name, State
   Get-VMSnapshot -VMName $vmName | Select-Object Name, SnapshotType
   ~~~

9. Run read-only verification again after creating the checkpoint. This check counts the checkpoint files against the 100 GiB budget and confirms that no more than one checkpoint exists:

   ~~~powershell
   .\Test-UbuntuHyperVVm.ps1 -Mode Verify -VmName $vmName -VmRoot $vmRoot -IsoPath $isoPath -ExpectedIsoSha256 $isoSha256 -ProvisioningSwitchName $switchName -BasicSessionConfirmed
   ~~~

10. Before each untrusted run, shut the VM down, leave its adapter disconnected, and run the same complete Verify command. Continue only if tracked TBound storage is at most 100 GiB, F: has at least 700 GiB free, and the other checks pass. Start the VM without connecting `Default Switch`; do not create another checkpoint or export/duplicate the VM.

## Clipboard and VMConnect session

The setup does not change the host's Enhanced Session policy because that setting is host-wide and would affect other VMs. The verifier cannot inspect an open VMConnect session. Confirm the Basic Session state and absence of clipboard/device redirection before recording the baseline and again before each run of untrusted code. Pass `-BasicSessionConfirmed` only after observing that state; it records an operator attestation and is not automatic detection. If this cannot be confirmed, do not create the baseline or run untrusted code. The separate Guest Service Interface file-copy service is explicitly disabled.

## Official references

- Microsoft Learn: [Create a virtual machine in Hyper-V](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/get-started/create-a-virtual-machine-in-hyper-v) and [`New-VM`](https://learn.microsoft.com/en-us/powershell/module/hyper-v/new-vm?view=windowsserver2025-ps).
- Microsoft Learn: [Generation 2 VM security features](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/generation-2-virtual-machine-security-features) describes the Linux Secure Boot template.
- Microsoft Learn: [`Set-VM`](https://learn.microsoft.com/en-us/powershell/module/hyper-v/set-vm?view=windowsserver2025-ps) for checkpoint storage location, [`Set-VMFirmware`](https://learn.microsoft.com/en-us/powershell/module/hyper-v/set-vmfirmware?view=windowsserver2025-ps), [`New-VHD`](https://learn.microsoft.com/en-us/powershell/module/hyper-v/new-vhd?view=windowsserver2025-ps), and [checkpoints](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/manage/enable-or-disable-checkpoints-in-hyper-v).
- Microsoft Learn: [Hyper-V Integration Services](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/integration-services) explains these service capabilities; [Manage Hyper-V Integration Services](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/manage/manage-hyper-v-integration-services) documents enabling and disabling services.
- Microsoft Learn: [Share devices with your Hyper-V virtual machine](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/enhanced-session-mode), including clipboard redirection and Windows guest support; [Set-VMDvdDrive](https://learn.microsoft.com/en-us/powershell/module/hyper-v/set-vmdvddrive?view=windowsserver2025-ps) documents ejecting ISO media with `-Path $null`.
- Microsoft Learn: [`Connect-VMNetworkAdapter`](https://learn.microsoft.com/en-us/powershell/module/hyper-v/connect-vmnetworkadapter?view=windowsserver2025-ps) and [`Disconnect-VMNetworkAdapter`](https://learn.microsoft.com/en-us/powershell/module/hyper-v/disconnect-vmnetworkadapter?view=windowsserver2025-ps).
- Ubuntu: [Ubuntu 24.04.5 release files](https://releases.ubuntu.com/24.04.5/) and [How to verify your Ubuntu download](https://ubuntu.com/tutorials/how-to-verify-ubuntu).
