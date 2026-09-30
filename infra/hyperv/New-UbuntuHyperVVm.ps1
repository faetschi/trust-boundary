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

$integrationServicePolicy = [ordered]@{
    '84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47' = @{ Label = 'Heartbeat'; Enabled = $true }
    '9F8233AC-BE49-4C79-8EE3-E7E1985B2077' = @{ Label = 'Shutdown'; Enabled = $true }
    '2497F4DE-E9FA-4204-80E4-4B75C46419C0' = @{ Label = 'Time Synchronization'; Enabled = $true }
    '6C09BB55-D683-4DA0-8931-C9BF705F6480' = @{ Label = 'Guest Service Interface'; Enabled = $false }
    '2A34B1C2-FD73-4043-8A5B-DD2159BC743F' = @{ Label = 'Key-Value Pair Exchange'; Enabled = $false }
    '5CED1297-4598-4915-A5FC-AD21BB4D02A4' = @{ Label = 'VSS'; Enabled = $false }
}
$requiredIntegrationServiceIds = @($integrationServicePolicy.Keys)

function Get-IntegrationServiceIdSuffix {
    param(
        [Parameter(Mandatory)][object]$Service,
        [Parameter(Mandatory)][guid]$VmId
    )

    $idProperty = $Service.PSObject.Properties['Id']
    if ($null -eq $idProperty -or $null -eq $idProperty.Value) {
        throw "Integration service object has no Id property. Display name: '$($Service.Name)'"
    }
    $match = [regex]::Match([string]$idProperty.Value, '(?i)(?<ServiceId>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$')
    if (-not $match.Success) {
        throw "Integration service Id does not end in a GUID. Display name: '$($Service.Name)'; Id: '$($idProperty.Value)'"
    }
    $serviceId = $match.Groups['ServiceId'].Value.ToUpperInvariant()
    $expectedObjectId = 'Microsoft:{0}\{1}' -f $VmId.ToString().ToUpperInvariant(), $serviceId
    if ([string]$idProperty.Value -ine $expectedObjectId) {
        throw "Integration service Id is not scoped to the target VM. Expected '$expectedObjectId'; received '$($idProperty.Value)'."
    }
    $vmIdProperty = $Service.PSObject.Properties['VMId']
    if ($null -ne $vmIdProperty -and $null -ne $vmIdProperty.Value -and
        -not [string]::IsNullOrWhiteSpace([string]$vmIdProperty.Value) -and [guid]$vmIdProperty.Value -ne $VmId) {
        throw "Integration service VMId does not match target VM '$VmId'."
    }
    return $serviceId
}

function Resolve-IntegrationServicePolicy {
    param(
        [Parameter(Mandatory)][object[]]$Services,
        [Parameter(Mandatory)][guid]$VmId
    )

    $resolved = @(
        foreach ($service in $Services) {
            $serviceId = Get-IntegrationServiceIdSuffix -Service $service -VmId $VmId
            if (-not $integrationServicePolicy.Contains($serviceId)) {
                throw "Unknown integration service Id '$serviceId' (display name '$($service.Name)'). No service changes were made."
            }
            [pscustomobject]@{
                Id = $serviceId
                Label = $integrationServicePolicy[$serviceId].Label
                Enabled = [bool]$integrationServicePolicy[$serviceId].Enabled
                Service = $service
            }
        }
    )
    $duplicateIds = @($resolved | Group-Object Id | Where-Object Count -ne 1)
    $missingIds = @($requiredIntegrationServiceIds | Where-Object { $_ -notin @($resolved.Id) })
    if ($duplicateIds.Count -gt 0 -or $missingIds.Count -gt 0) {
        throw "Integration service inventory mismatch. Duplicate Ids: $(($duplicateIds.Name) -join ', '); missing Ids: $($missingIds -join ', '). No service changes were made."
    }
    return $resolved
}

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
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

    $relativePath = $fullPath.Substring($pathRoot.Length)
    $components = $relativePath -split '[\\/]'
    foreach ($component in $components) {
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
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Root
    )
    $fullPath = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    $fullRoot = [IO.Path]::GetFullPath($Root).TrimEnd('\')
    return ($fullPath -ieq $fullRoot) -or $fullPath.StartsWith($fullRoot + '\', [StringComparison]::OrdinalIgnoreCase)
}

function Get-TrackedStorageBytes {
    param([Parameter(Mandatory)][string[]]$RootPaths)

    [long]$totalBytes = 0
    foreach ($rootPath in $RootPaths) {
        if (-not (Test-Path -LiteralPath $rootPath -PathType Container)) {
            throw "Tracked storage directory does not exist: $rootPath"
        }
        $rootItem = Get-Item -LiteralPath $rootPath -Force -ErrorAction Stop
        if (($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Tracked storage root is a reparse point: $rootPath"
        }

        $pendingDirectories = [System.Collections.Generic.Stack[string]]::new()
        $pendingDirectories.Push([IO.Path]::GetFullPath($rootPath))
        while ($pendingDirectories.Count -gt 0) {
            $currentDirectory = $pendingDirectories.Pop()
            foreach ($item in @(Get-ChildItem -LiteralPath $currentDirectory -Force -ErrorAction Stop)) {
                if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                    throw "Tracked storage contains a reparse point, junction, or symbolic link: $($item.FullName)"
                }
                if ($item.PSIsContainer) {
                    $pendingDirectories.Push($item.FullName)
                }
                else {
                    $totalBytes += [int64]$item.Length
                }
            }
        }
    }
    return [int64]$totalBytes
}

if (-not (Test-IsAdministrator)) {
    throw 'Run this setup script from an elevated PowerShell session.'
}

Import-Module Hyper-V -ErrorAction Stop
$null = Get-VMHost -ErrorAction Stop
$requiredCommands = @(
    'Get-VM', 'Get-VMSwitch', 'New-VM', 'Set-VM', 'Set-VMProcessor',
    'Set-VMMemory', 'New-VHD', 'Add-VMHardDiskDrive', 'Add-VMDvdDrive',
    'Set-VMFirmware', 'Get-VMNetworkAdapter', 'Disconnect-VMNetworkAdapter',
    'Get-VMIntegrationService', 'Enable-VMIntegrationService', 'Disable-VMIntegrationService'
)
foreach ($commandName in $requiredCommands) {
    if (-not (Get-Command -Name $commandName -ErrorAction SilentlyContinue)) {
        throw "Required Hyper-V cmdlet is unavailable: $commandName"
    }
}
$enableIntegrationCommand = Get-Command -Name Enable-VMIntegrationService -ErrorAction Stop
$disableIntegrationCommand = Get-Command -Name Disable-VMIntegrationService -ErrorAction Stop
if (-not $enableIntegrationCommand.Parameters.ContainsKey('VMIntegrationService') -or
    -not $disableIntegrationCommand.Parameters.ContainsKey('VMIntegrationService')) {
    throw 'Hyper-V cmdlets do not support passing a VMIntegrationComponent object. No files or VM objects were created.'
}

# Reject collisions before creating any files or Hyper-V objects.
$existingVms = @(Get-VM -ErrorAction Stop)
if (@($existingVms | Where-Object { $_.Name -eq $VmName }).Count -gt 0) {
    throw "A VM named '$VmName' already exists. No changes were made."
}
if ($existingVms.Count -gt 0) {
    # If the host has any VM to inspect, validate the ID inventory before creating this VM.
    $referenceVm = $existingVms[0]
    $null = Resolve-IntegrationServicePolicy -Services @(Get-VMIntegrationService -VMName $referenceVm.Name -ErrorAction Stop) -VmId ([guid]$referenceVm.Id)
}

$fullVmRoot = [IO.Path]::GetFullPath($VmRoot)
$driveRoot = [IO.Path]::GetPathRoot($fullVmRoot)
$assetsRoot = Join-Path $driveRoot 'TBoundAssets'
$vmStorageRoot = Join-Path $driveRoot 'TBoundVMs'
$snapshotDirectory = $fullVmRoot
if ($driveRoot.TrimEnd('\') -ne 'F:') {
    throw "VmRoot must resolve to drive F:. Supplied path resolves to '$fullVmRoot'."
}
if ($fullVmRoot.TrimEnd('\') -eq $driveRoot.TrimEnd('\')) {
    throw 'VmRoot cannot be a drive root.'
}
Assert-NoReparsePathComponents -Path $fullVmRoot
Assert-NoReparsePathComponents -Path $assetsRoot
Assert-NoReparsePathComponents -Path $vmStorageRoot
if (-not (Test-PathUnderRoot -Path $fullVmRoot -Root $vmStorageRoot) -or ($fullVmRoot.TrimEnd('\') -ieq $vmStorageRoot.TrimEnd('\'))) {
    throw "VmRoot must be a new child directory under $vmStorageRoot."
}
if (-not (Test-Path -LiteralPath $assetsRoot -PathType Container)) {
    throw "The TBound asset directory must already exist: $assetsRoot"
}

$parentPath = [IO.Path]::GetDirectoryName($fullVmRoot.TrimEnd('\'))
if (-not (Test-Path -LiteralPath $parentPath -PathType Container)) {
    throw "The parent directory must already exist: $parentPath"
}
if (Test-Path -LiteralPath $fullVmRoot) {
    throw "VmRoot already exists: $fullVmRoot. No changes were made."
}

$diskDirectory = Join-Path $fullVmRoot 'Virtual Hard Disks'
$vhdPath = Join-Path $diskDirectory ($VmName + '.vhdx')
if ((Test-Path -LiteralPath $diskDirectory) -or (Test-Path -LiteralPath $vhdPath)) {
    throw 'A target disk path already exists. No changes were made.'
}

$driveLetter = $driveRoot.Substring(0, 1)
$drive = Get-PSDrive -Name $driveLetter -ErrorAction Stop
if ($null -eq $drive.Free -or $drive.Free -lt 800GB) {
    throw ("Drive {0}: must have at least 800 GiB free before VM creation (700 GiB reserve plus the 100 GiB TBound budget)." -f $driveLetter)
}

$trackedBytes = Get-TrackedStorageBytes -RootPaths @($assetsRoot, $vmStorageRoot)
if ($trackedBytes -gt 100GB) {
    throw ("Tracked TBound storage is {0:N2} GiB, above the 100 GiB limit. No changes were made." -f ($trackedBytes / 1GB))
}

$switchMatches = @(Get-VMSwitch | Where-Object { $_.Name -eq $ProvisioningSwitchName })
if ($switchMatches.Count -ne 1) {
    throw "Provisioning switch '$ProvisioningSwitchName' was not found exactly once."
}

$resolvedIso = (Resolve-Path -LiteralPath $IsoPath -ErrorAction Stop).Path
$resolvedIso = [IO.Path]::GetFullPath($resolvedIso)
Assert-NoReparsePathComponents -Path $resolvedIso
if (-not (Test-PathUnderRoot -Path $resolvedIso -Root $assetsRoot)) {
    throw "IsoPath must be under the tracked TBound assets directory $assetsRoot."
}
$isoItem = Get-Item -LiteralPath $resolvedIso -ErrorAction Stop
if ($isoItem.PSIsContainer) {
    throw "IsoPath must name an ISO file: $resolvedIso"
}
$actualIsoSha256 = (Get-FileHash -LiteralPath $resolvedIso -Algorithm SHA256).Hash
if ($actualIsoSha256 -ine $ExpectedIsoSha256) {
    throw "ISO SHA-256 mismatch. Expected $ExpectedIsoSha256, got $actualIsoSha256. No changes were made."
}

$vmDiskSize = 40GB
$vmMemory = 16GB
if (-not $PSCmdlet.ShouldProcess(
    "$VmName at $fullVmRoot",
    'Create an offline Gen 2 Ubuntu VM with 8 vCPU, 16 GiB fixed RAM, and a 40 GiB dynamic VHDX'
)) {
    return
}

try {
    # If a later Hyper-V operation fails, partial state is left for inspection; no cleanup is attempted.
    New-Item -ItemType Directory -Path $fullVmRoot -ErrorAction Stop | Out-Null
    New-Item -ItemType Directory -Path $diskDirectory -ErrorAction Stop | Out-Null

    $null = New-VM -Name $VmName -Generation 2 -MemoryStartupBytes $vmMemory -NoVHD -Path $fullVmRoot
    Set-VMProcessor -VMName $VmName -Count 8
    Set-VMMemory -VMName $VmName -DynamicMemoryEnabled $false -StartupBytes $vmMemory
    Set-VM -VMName $VmName -CheckpointType ProductionOnly -AutomaticCheckpointsEnabled $false -SnapshotFileLocation $snapshotDirectory

    $vmObject = Get-VM -Name $VmName -ErrorAction Stop
    $services = Resolve-IntegrationServicePolicy -Services @(Get-VMIntegrationService -VMName $VmName) -VmId ([guid]$vmObject.Id)
    foreach ($entry in $services) {
        if ([bool]$entry.Service.Enabled -ne $entry.Enabled) {
            if ($entry.Enabled) {
                Enable-VMIntegrationService -VMIntegrationService $entry.Service
            }
            else {
                Disable-VMIntegrationService -VMIntegrationService $entry.Service
            }
        }
    }
    $servicesAfter = Resolve-IntegrationServicePolicy -Services @(Get-VMIntegrationService -VMName $VmName) -VmId ([guid]$vmObject.Id)
    $serviceMismatches = @($servicesAfter | Where-Object { [bool]$_.Service.Enabled -ne $_.Enabled })
    if ($serviceMismatches.Count -gt 0) {
        throw "Integration service policy could not be enforced for Ids: $(($serviceMismatches.Id) -join ', ')."
    }

    $null = New-VHD -Path $vhdPath -Dynamic -SizeBytes $vmDiskSize
    $null = Add-VMHardDiskDrive -VMName $VmName -Path $vhdPath
    $dvdDrive = Add-VMDvdDrive -VMName $VmName -Path $resolvedIso -PassThru

    $firmwareSettings = @{
        VMName = $VmName
        EnableSecureBoot = 'On'
        SecureBootTemplate = 'MicrosoftUEFICertificateAuthority'
        FirstBootDevice = $dvdDrive
    }
    Set-VMFirmware @firmwareSettings

    $adapters = @(Get-VMNetworkAdapter -VMName $VmName)
    if ($adapters.Count -ne 1) {
        throw "Expected exactly one VM network adapter; found $($adapters.Count). Inspect this partial VM."
    }
    foreach ($adapter in $adapters) {
        if (-not [string]::IsNullOrWhiteSpace([string]$adapter.SwitchName)) {
            Disconnect-VMNetworkAdapter -VMNetworkAdapter $adapter
        }
    }
    $adaptersAfter = @(Get-VMNetworkAdapter -VMName $VmName)
    if ($adaptersAfter.Count -ne 1 -or -not [string]::IsNullOrWhiteSpace([string]$adaptersAfter[0].SwitchName)) {
        throw 'The sole VM network adapter is not disconnected; inspect this partial VM.'
    }

    $trackedBytesAfterCreate = Get-TrackedStorageBytes -RootPaths @($assetsRoot, $vmStorageRoot)
    if ($trackedBytesAfterCreate -gt 100GB) {
        throw ("Tracked TBound storage after creation is {0:N2} GiB, above the 100 GiB limit. Inspect this partial VM." -f ($trackedBytesAfterCreate / 1GB))
    }
    $driveAfterCreate = Get-PSDrive -Name $driveLetter -ErrorAction Stop
    if ($null -eq $driveAfterCreate.Free -or $driveAfterCreate.Free -lt 700GB) {
        throw 'F: has less than the required 700 GiB free after VM creation. Inspect this partial VM.'
    }

    Write-Host "Created VM '$VmName' in the Off state with exactly one disconnected network adapter."
    Write-Host "Provisioning switch '$ProvisioningSwitchName' was checked but not attached."
    Write-Host 'Run Test-UbuntuHyperVVm.ps1 -Mode Created to verify the initial install state.'
}
catch {
    Write-Warning "Setup stopped with a partial state possible for '$VmName'. No files or VM objects were removed. Inspect Hyper-V Manager and the target directory before retrying."
    throw
}
