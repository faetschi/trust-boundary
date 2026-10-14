#requires -Version 5.1
<#
.SYNOPSIS
  Read-only inventory of an existing TBound-Ubuntu-2404 Hyper-V VM.
.DESCRIPTION
  Reads Hyper-V state and configuration only. It never starts/stops a VM,
  changes VM/host/network settings, reads guest credentials, or connects to the
  guest. Guest kernel and Landlock ABI must be measured separately in its
  trusted console; this report labels that probe as not run.
#>
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this read-only inventory in the already authorized elevated PowerShell session; this script does not elevate itself.'
}

$systemDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::System)
$windowsDirectory = Split-Path -Parent $systemDirectory
$expectedHyperVBinary = Join-Path $windowsDirectory 'Microsoft.Net\assembly\GAC_MSIL\Microsoft.HyperV.PowerShell.Cmdlets\v4.0_10.0.0.0__31bf3856ad364e35\Microsoft.HyperV.PowerShell.Cmdlets.dll'
$loadedHyperV = @(Get-Module -Name Hyper-V)
if ($loadedHyperV.Count -ne 1 -or $loadedHyperV[0].Version.ToString() -cne '2.0.0.0' -or $loadedHyperV[0].Path -ine $expectedHyperVBinary) {
    throw 'The exact pinned Hyper-V module must be preloaded by the reviewed bootstrap.'
}
$VmName = 'TBound-Ubuntu-2404'
$vm = Get-VM -Name $VmName -ErrorAction Stop
if (@($vm).Count -ne 1) { throw "Expected exactly one VM named $VmName." }
$hostInfo = Get-VMHost -ErrorAction Stop
$adapters = @(Get-VMNetworkAdapter -VM $vm -ErrorAction Stop)
$switches = @(Get-VMSwitch -ErrorAction Stop)
$disks = @(Get-VMHardDiskDrive -VM $vm -ErrorAction Stop)
$dvdDrives = @(Get-VMDvdDrive -VM $vm -ErrorAction Stop)
$snapshots = @(Get-VMSnapshot -VM $vm -ErrorAction Stop)
$firmware = $null
$firmwareStatus = 'not-applicable-for-generation-1'
if ([int]$vm.Generation -eq 2) {
    $firmware = Get-VMFirmware -VM $vm -ErrorAction Stop
    $firmwareStatus = 'read'
}

$switchByName = @{}
foreach ($switch in $switches) { $switchByName[[string]$switch.Name] = [string]$switch.SwitchType }
$diskRows = @(
    foreach ($disk in $disks) {
        $row = [ordered]@{
            ControllerType = [string]$disk.ControllerType
            ControllerNumber = [int]$disk.ControllerNumber
            ControllerLocation = [int]$disk.ControllerLocation
            Path = [string]$disk.Path
        }
        if (-not [string]::IsNullOrWhiteSpace([string]$disk.Path) -and (Test-Path -LiteralPath $disk.Path -PathType Leaf)) {
            try {
                $vhd = Get-VHD -Path $disk.Path -ErrorAction Stop
                $row.Format = [string]$vhd.VhdFormat
                $row.Type = [string]$vhd.VhdType
                $row.VirtualSizeBytes = [int64]$vhd.Size
                $row.FileSizeBytes = [int64]$vhd.FileSize
                $row.ParentPath = [string]$vhd.ParentPath
            }
            catch {
                $row.MetadataReadError = $_.Exception.Message
            }
        }
        [pscustomobject]$row
    }
)

$report = [ordered]@{
    schema = 'tbound-hyperv-readonly-inventory/v1'
    capturedAt = (Get-Date).ToString('o')
    host = [ordered]@{
        computerName = [Environment]::MachineName
        hyperVModule = (Get-Module Hyper-V).Version.ToString()
        enhancedSessionModePolicy = [bool]$hostInfo.EnableEnhancedSessionMode
    }
    vm = [ordered]@{
        name = [string]$vm.Name
        id = [string]$vm.Id
        state = [string]$vm.State
        generation = [int]$vm.Generation
        version = [string]$vm.Version
        path = [string]$vm.Path
        configurationLocation = [string]$vm.ConfigurationLocation
        automaticCheckpointsEnabled = [bool]$vm.AutomaticCheckpointsEnabled
        checkpointType = [string]$vm.CheckpointType
        processorCount = [int]$vm.ProcessorCount
        dynamicMemoryEnabled = [bool]$vm.DynamicMemoryEnabled
        startupMemoryBytes = [int64]$vm.MemoryStartup
        minimumMemoryBytes = [int64]$vm.MemoryMinimum
        maximumMemoryBytes = [int64]$vm.MemoryMaximum
        firmwareStatus = $firmwareStatus
        secureBoot = if ($null -ne $firmware) { [string]$firmware.SecureBoot } else { $null }
        secureBootTemplate = if ($null -ne $firmware) { [string]$firmware.SecureBootTemplate } else { $null }
        networkAdapters = @(
            foreach ($adapter in $adapters) {
                $switchName = [string]$adapter.SwitchName
                [pscustomobject]@{
                    name = [string]$adapter.Name
                    switchName = $switchName
                    switchType = if ($switchByName.ContainsKey($switchName)) { $switchByName[$switchName] } else { $null }
                    status = [string]$adapter.Status
                    ipAddresses = @($adapter.IPAddresses | ForEach-Object { [string]$_ })
                }
            }
        )
        disks = $diskRows
        dvdDrives = @($dvdDrives | ForEach-Object { [pscustomobject]@{ controller = [string]$_.ControllerNumber; path = [string]$_.Path } })
        snapshots = @($snapshots | ForEach-Object { [pscustomobject]@{ name = [string]$_.Name; created = $_.CreationTime.ToString('o'); snapshotType = [string]$_.SnapshotType } })
    }
    visibleSwitches = @($switches | ForEach-Object { [pscustomobject]@{ name = [string]$_.Name; type = [string]$_.SwitchType } })
    guestKernelAndLandlockProbe = 'NOT RUN: execute the read-only command from the guest console; this host script has no guest channel.'
}

$report | ConvertTo-Json -Depth 8
