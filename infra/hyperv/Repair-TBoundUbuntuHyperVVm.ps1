#requires -RunAsAdministrator
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$')][string]$VmName,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$VmRoot,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$IsoPath,
    [Parameter(Mandatory)][ValidatePattern('^[A-Fa-f0-9]{64}$')][string]$ExpectedIsoSha256,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$ProvisioningSwitchName
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$expectedVmName = 'TBound-Ubuntu-2404'
$expectedVmRoot = 'F:\TBoundVMs\TBound-Ubuntu-2404'
$vmDiskSize = 40GB
$vmMemory = 16GB
$integrationServicePolicy = [ordered]@{
    '84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47' = @{ Label = 'Heartbeat'; Enabled = $true }
    '9F8233AC-BE49-4C79-8EE3-E7E1985B2077' = @{ Label = 'Shutdown'; Enabled = $true }
    '2497F4DE-E9FA-4204-80E4-4B75C46419C0' = @{ Label = 'Time Synchronization'; Enabled = $true }
    '6C09BB55-D683-4DA0-8931-C9BF705F6480' = @{ Label = 'Guest Service Interface'; Enabled = $false }
    '2A34B1C2-FD73-4043-8A5B-DD2159BC743F' = @{ Label = 'Key-Value Pair Exchange'; Enabled = $false }
    '5CED1297-4598-4915-A5FC-AD21BB4D02A4' = @{ Label = 'VSS'; Enabled = $false }
}

function Assert-NoReparsePathComponents {
    param([Parameter(Mandatory)][string]$Path)
    $fullPath = [IO.Path]::GetFullPath($Path)
    $pathRoot = [IO.Path]::GetPathRoot($fullPath)
    $currentPath = $pathRoot
    $rootItem = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
    if (($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Storage root is a reparse point: $currentPath"
    }
    foreach ($component in ($fullPath.Substring($pathRoot.Length) -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($component)) { continue }
        $currentPath = Join-Path $currentPath $component
        if (-not (Test-Path -LiteralPath $currentPath)) { break }
        $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Storage path contains a reparse point, junction, or symbolic link: $currentPath"
        }
    }
}

function Test-PathUnderRoot {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Root)
    $fullPath = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    $fullRoot = [IO.Path]::GetFullPath($Root).TrimEnd('\')
    return ($fullPath -ieq $fullRoot) -or $fullPath.StartsWith($fullRoot + '\', [StringComparison]::OrdinalIgnoreCase)
}

function Get-TrackedStorageBytes {
    param([Parameter(Mandatory)][string[]]$RootPaths)
    [long]$totalBytes = 0
    foreach ($rootPath in $RootPaths) {
        if (-not (Test-Path -LiteralPath $rootPath -PathType Container)) { throw "Tracked storage directory does not exist: $rootPath" }
        Assert-NoReparsePathComponents -Path $rootPath
        $pendingDirectories = [System.Collections.Generic.Stack[string]]::new()
        $pendingDirectories.Push([IO.Path]::GetFullPath($rootPath))
        while ($pendingDirectories.Count -gt 0) {
            $currentDirectory = $pendingDirectories.Pop()
            foreach ($item in @(Get-ChildItem -LiteralPath $currentDirectory -Force -ErrorAction Stop)) {
                if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                    throw "Tracked storage contains a reparse point, junction, or symbolic link: $($item.FullName)"
                }
                if ($item.PSIsContainer) { $pendingDirectories.Push($item.FullName) }
                else { $totalBytes += [int64]$item.Length }
            }
        }
    }
    return [int64]$totalBytes
}

function Get-IntegrationServiceIdSuffix {
    param([Parameter(Mandatory)][object]$Service, [Parameter(Mandatory)][guid]$VmId)
    $idProperty = $Service.PSObject.Properties['Id']
    if ($null -eq $idProperty -or $null -eq $idProperty.Value) { throw "Integration service object has no Id property: $($Service.Name)" }
    $match = [regex]::Match([string]$idProperty.Value, '(?i)(?<ServiceId>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$')
    if (-not $match.Success) { throw "Integration service Id does not end in a GUID: $($idProperty.Value)" }
    $serviceId = $match.Groups['ServiceId'].Value.ToUpperInvariant()
    $expectedId = 'Microsoft:{0}\{1}' -f $VmId.ToString().ToUpperInvariant(), $serviceId
    if ([string]$idProperty.Value -ine $expectedId) { throw "Integration service Id is not scoped to this VM. Expected '$expectedId'; received '$($idProperty.Value)'." }
    $vmIdProperty = $Service.PSObject.Properties['VMId']
    if ($null -ne $vmIdProperty -and $null -ne $vmIdProperty.Value -and -not [string]::IsNullOrWhiteSpace([string]$vmIdProperty.Value) -and [guid]$vmIdProperty.Value -ne $VmId) {
        throw 'Integration service VMId does not match the partial VM.'
    }
    return $serviceId
}

function Resolve-IntegrationServicePolicy {
    param([Parameter(Mandatory)][object[]]$Services, [Parameter(Mandatory)][guid]$VmId)
    $resolved = @(
        foreach ($service in $Services) {
            $serviceId = Get-IntegrationServiceIdSuffix -Service $service -VmId $VmId
            if (-not $integrationServicePolicy.Contains($serviceId)) {
                throw "Unknown integration service Id '$serviceId' (display name '$($service.Name)'). No changes were made."
            }
            [pscustomobject]@{
                Id = $serviceId
                Label = $integrationServicePolicy[$serviceId].Label
                Enabled = [bool]$integrationServicePolicy[$serviceId].Enabled
                Service = $service
            }
        }
    )
    $duplicates = @($resolved | Group-Object Id | Where-Object { $_.Count -gt 1 })
    $missing = @($integrationServicePolicy.Keys | Where-Object { $_ -notin @($resolved.Id) })
    if ($duplicates.Count -gt 0 -or $missing.Count -gt 0) {
        throw "Integration service inventory mismatch. Duplicate IDs: $(($duplicates.Name) -join ', '); missing IDs: $($missing -join ', '). No changes were made."
    }
    return $resolved
}

if (-not (Test-Path -LiteralPath $expectedVmRoot)) { throw "Expected partial VM root is missing: $expectedVmRoot" }
if ($VmName -cne $expectedVmName -or [IO.Path]::GetFullPath($VmRoot).TrimEnd('\') -ine $expectedVmRoot) {
    throw "This repair is restricted to '$expectedVmName' at '$expectedVmRoot'. No changes were made."
}
if (-not [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this repair script from an elevated PowerShell session.'
}

Import-Module Hyper-V -ErrorAction Stop
foreach ($commandName in @('Get-VM', 'Get-VMProcessor', 'Get-VMMemory', 'Get-VMSnapshot', 'Get-VMHardDiskDrive', 'Get-VMDvdDrive', 'Get-VMNetworkAdapter', 'Get-VMIntegrationService', 'Enable-VMIntegrationService', 'Disable-VMIntegrationService', 'New-VHD', 'Add-VMHardDiskDrive', 'Add-VMDvdDrive', 'Set-VMFirmware', 'Get-VMSwitch')) {
    if (-not (Get-Command -Name $commandName -ErrorAction SilentlyContinue)) { throw "Required Hyper-V cmdlet is unavailable: $commandName" }
}
if (-not (Get-Command Enable-VMIntegrationService).Parameters.ContainsKey('VMIntegrationService') -or
    -not (Get-Command Disable-VMIntegrationService).Parameters.ContainsKey('VMIntegrationService')) {
    throw 'Hyper-V cmdlets cannot accept integration service objects. No changes were made.'
}

$fullVmRoot = [IO.Path]::GetFullPath($VmRoot)
$assetsRoot = 'F:\TBoundAssets'
$vmStorageRoot = 'F:\TBoundVMs'
$expectedVmPath = Join-Path $fullVmRoot $VmName
$diskDirectory = Join-Path $fullVmRoot 'Virtual Hard Disks'
$vhdPath = Join-Path $diskDirectory ($VmName + '.vhdx')
Assert-NoReparsePathComponents -Path $assetsRoot
Assert-NoReparsePathComponents -Path $vmStorageRoot
Assert-NoReparsePathComponents -Path $fullVmRoot
if (-not (Test-PathUnderRoot -Path $fullVmRoot -Root $vmStorageRoot) -or $fullVmRoot.TrimEnd('\') -ieq $vmStorageRoot) {
    throw "VM root is not a child of $vmStorageRoot. No changes were made."
}
if (-not (Test-Path -LiteralPath $assetsRoot -PathType Container) -or -not (Test-Path -LiteralPath $vmStorageRoot -PathType Container)) {
    throw 'Both tracked storage roots must exist before repair.'
}
if (-not (Test-Path -LiteralPath $diskDirectory -PathType Container)) { throw "Expected empty disk directory is missing: $diskDirectory" }
Assert-NoReparsePathComponents -Path $diskDirectory
if (Test-Path -LiteralPath $vhdPath) { throw "Target VHDX already exists: $vhdPath. Inspect manually; no changes were made." }
if (@(Get-ChildItem -LiteralPath $diskDirectory -Force -ErrorAction Stop).Count -ne 0) {
    throw "Expected an empty disk directory: $diskDirectory. Inspect manually; no changes were made."
}

$resolvedIso = (Resolve-Path -LiteralPath $IsoPath -ErrorAction Stop).Path
Assert-NoReparsePathComponents -Path $resolvedIso
if (-not (Test-PathUnderRoot -Path $resolvedIso -Root $assetsRoot)) { throw "ISO must be under $assetsRoot." }
$isoItem = Get-Item -LiteralPath $resolvedIso -ErrorAction Stop
if ($isoItem.PSIsContainer) { throw 'IsoPath must identify an ISO file.' }
$actualIsoSha256 = (Get-FileHash -LiteralPath $resolvedIso -Algorithm SHA256).Hash
if ($actualIsoSha256 -ine $ExpectedIsoSha256) { throw "ISO SHA-256 mismatch. Expected $ExpectedIsoSha256, got $actualIsoSha256." }
if (@(Get-VMSwitch -ErrorAction Stop | Where-Object { $_.Name -eq $ProvisioningSwitchName }).Count -ne 1) {
    throw "Provisioning switch '$ProvisioningSwitchName' was not found exactly once."
}

$drive = Get-PSDrive -Name F -ErrorAction Stop
if ($null -eq $drive.Free -or $drive.Free -lt 800GB) { throw 'F: must have at least 800 GiB free before repairing this VM.' }
$trackedBytes = Get-TrackedStorageBytes -RootPaths @($assetsRoot, $vmStorageRoot)
if ($trackedBytes -gt 100GB) { throw 'Tracked TBound files already exceed the 100 GiB limit.' }

$vmMatches = @(Get-VM -Name $VmName -ErrorAction Stop)
if ($vmMatches.Count -ne 1) { throw "Expected exactly one partial VM named '$VmName'; found $($vmMatches.Count)." }
$vm = $vmMatches[0]
if ([string]$vm.State -ne 'Off' -or [int]$vm.Generation -ne 2) { throw 'Partial VM must be Off and Generation 2.' }
if ([IO.Path]::GetFullPath([string]$vm.Path).TrimEnd('\') -ine $expectedVmPath) { throw "VM Path is not the expected partial path '$expectedVmPath'." }
if ([int](Get-VMProcessor -VMName $VmName).Count -ne 8) { throw 'Partial VM does not have exactly 8 virtual processors.' }
$memory = Get-VMMemory -VMName $VmName
if ([bool]$memory.DynamicMemoryEnabled -or [int64]$memory.Startup -ne $vmMemory) { throw 'Partial VM does not have fixed 16 GiB startup memory.' }
if ([string]$vm.CheckpointType -ne 'ProductionOnly' -or [bool]$vm.AutomaticCheckpointsEnabled) { throw 'Partial VM checkpoint settings do not match ProductionOnly with automatic checkpoints disabled.' }
$snapshotPath = [IO.Path]::GetFullPath([string]$vm.SnapshotFileLocation).TrimEnd('\')
if ($snapshotPath -ine $fullVmRoot.TrimEnd('\')) { throw 'Partial VM checkpoint path is not the tracked VM root.' }
if (@(Get-VMSnapshot -VMName $VmName -ErrorAction Stop).Count -ne 0) { throw 'Partial VM unexpectedly has a checkpoint; no changes were made.' }
if (@(Get-VMHardDiskDrive -VMName $VmName -ErrorAction Stop).Count -ne 0) { throw 'Partial VM already has a hard disk; no changes were made.' }
if (@(Get-VMDvdDrive -VMName $VmName -ErrorAction Stop).Count -ne 0) { throw 'Partial VM already has a DVD drive; no changes were made.' }
$adapters = @(Get-VMNetworkAdapter -VMName $VmName -ErrorAction Stop)
if ($adapters.Count -ne 1 -or -not [string]::IsNullOrWhiteSpace([string]$adapters[0].SwitchName)) {
    throw 'Partial VM must have exactly one disconnected network adapter.'
}
$services = Resolve-IntegrationServicePolicy -Services @(Get-VMIntegrationService -VMName $VmName -ErrorAction Stop) -VmId ([guid]$vm.Id)

if (-not $PSCmdlet.ShouldProcess($VmName, 'Resume the exact offline, diskless partial VM by applying the service policy, adding the verified ISO and 40 GiB dynamic disk, and setting Linux Secure Boot')) { return }

try {
    foreach ($entry in $services) {
        if ([bool]$entry.Service.Enabled -ne $entry.Enabled) {
            if ($entry.Enabled) { Enable-VMIntegrationService -VMIntegrationService $entry.Service }
            else { Disable-VMIntegrationService -VMIntegrationService $entry.Service }
        }
    }
    $servicesAfter = Resolve-IntegrationServicePolicy -Services @(Get-VMIntegrationService -VMName $VmName -ErrorAction Stop) -VmId ([guid]$vm.Id)
    $serviceMismatches = @($servicesAfter | Where-Object { [bool]$_.Service.Enabled -ne $_.Enabled })
    if ($serviceMismatches.Count -gt 0) { throw "Integration service policy mismatch for IDs: $(($serviceMismatches.Id) -join ', ')." }

    $null = New-VHD -Path $vhdPath -Dynamic -SizeBytes $vmDiskSize
    $null = Add-VMHardDiskDrive -VMName $VmName -Path $vhdPath
    $dvdDrive = Add-VMDvdDrive -VMName $VmName -Path $resolvedIso -PassThru
    Set-VMFirmware -VMName $VmName -EnableSecureBoot On -SecureBootTemplate MicrosoftUEFICertificateAuthority -FirstBootDevice $dvdDrive

    if ([string](Get-VM -Name $VmName -ErrorAction Stop).State -ne 'Off') { throw 'VM state changed during repair; stop and inspect it.' }
    $adaptersAfter = @(Get-VMNetworkAdapter -VMName $VmName -ErrorAction Stop)
    if ($adaptersAfter.Count -ne 1 -or -not [string]::IsNullOrWhiteSpace([string]$adaptersAfter[0].SwitchName)) {
        throw 'VM adapter count or connection state changed during repair; inspect it before proceeding.'
    }
    $trackedBytesAfter = Get-TrackedStorageBytes -RootPaths @($assetsRoot, $vmStorageRoot)
    if ($trackedBytesAfter -gt 100GB) { throw 'Tracked TBound files exceed 100 GiB after repair; inspect partial state.' }
    $driveAfter = Get-PSDrive -Name F -ErrorAction Stop
    if ($null -eq $driveAfter.Free -or $driveAfter.Free -lt 700GB) { throw 'F: has less than 700 GiB free after repair; inspect partial state.' }

    Write-Host "Repair completed for '$VmName'. It remains Off with one disconnected adapter."
    Write-Host 'Run Test-UbuntuHyperVVm.ps1 -Mode Created with the same arguments before starting the VM.'
}
catch {
    Write-Warning "Repair stopped. No VM or files were removed. Inspect '$VmName' and its storage before any further action."
    throw
}
