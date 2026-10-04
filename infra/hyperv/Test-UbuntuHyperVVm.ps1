[CmdletBinding()]
param(
    [ValidateSet('Preflight', 'Created', 'Verify')][string]$Mode = 'Preflight',
    [Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$')][string]$VmName,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$VmRoot,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$IsoPath,
    [Parameter(Mandatory)][ValidatePattern('^[A-Fa-f0-9]{64}$')][string]$ExpectedIsoSha256,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$ProvisioningSwitchName,
    [switch]$BasicSessionConfirmed
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:Failures = [System.Collections.Generic.List[string]]::new()
$integrationServicePolicy = [ordered]@{
    '84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47' = @{ Label = 'Heartbeat'; Enabled = $true }
    '9F8233AC-BE49-4C79-8EE3-E7E1985B2077' = @{ Label = 'Shutdown'; Enabled = $true }
    '2497F4DE-E9FA-4204-80E4-4B75C46419C0' = @{ Label = 'Time Synchronization'; Enabled = $true }
    '6C09BB55-D683-4DA0-8931-C9BF705F6480' = @{ Label = 'Guest Service Interface'; Enabled = $false }
    '2A34B1C2-FD73-4043-8A5B-DD2159BC743F' = @{ Label = 'Key-Value Pair Exchange'; Enabled = $false }
    '5CED1297-4598-4915-A5FC-AD21BB4D02A4' = @{ Label = 'VSS'; Enabled = $false }
}
$requiredIntegrationServiceIds = @($integrationServicePolicy.Keys)

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}
function Add-Check {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][bool]$Passed,
        [string]$Detail = ''
    )
    if ($Passed) {
        Write-Host "[PASS] $Name"
    }
    else {
        $script:Failures.Add($Name) | Out-Null
        Write-Host "[FAIL] $Name"
    }
    if (-not [string]::IsNullOrWhiteSpace($Detail)) {
        Write-Host "       $Detail"
    }
}
function Get-FullPath {
    param([Parameter(Mandatory)][string]$Path)
    return [IO.Path]::GetFullPath($Path)
}
function Get-IntegrationServiceIdSuffix {
    param(
        [Parameter(Mandatory)][object]$Service,
        [Parameter(Mandatory)][guid]$VmId
    )

    $idProperty = $Service.PSObject.Properties['Id']
    if ($null -eq $idProperty -or $null -eq $idProperty.Value) { return '' }
    $match = [regex]::Match([string]$idProperty.Value, '(?i)(?<ServiceId>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$')
    if (-not $match.Success) { return '' }
    $serviceId = $match.Groups['ServiceId'].Value.ToUpperInvariant()
    $expectedObjectId = 'Microsoft:{0}\{1}' -f $VmId.ToString().ToUpperInvariant(), $serviceId
    if ([string]$idProperty.Value -ine $expectedObjectId) { return '' }
    $vmIdProperty = $Service.PSObject.Properties['VMId']
    if ($null -ne $vmIdProperty -and $null -ne $vmIdProperty.Value -and
        -not [string]::IsNullOrWhiteSpace([string]$vmIdProperty.Value) -and [guid]$vmIdProperty.Value -ne $VmId) { return '' }
    return $serviceId
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
function Test-NoReparsePathComponents {
    param([Parameter(Mandatory)][string]$Path)

    $fullPath = [IO.Path]::GetFullPath($Path)
    $pathRoot = [IO.Path]::GetPathRoot($fullPath)
    $currentPath = $pathRoot
    $rootItem = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
    if (($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        return $false
    }

    $relativePath = $fullPath.Substring($pathRoot.Length)
    $components = $relativePath -split '[\\/]'
    foreach ($component in $components) {
        if ([string]::IsNullOrWhiteSpace($component)) { continue }
        $currentPath = Join-Path $currentPath $component
        if (-not (Test-Path -LiteralPath $currentPath)) { break }
        $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            return $false
        }
    }
    return $true
}
function Get-BootTypeName {
    param([AllowNull()][object]$BootEntry)
    if ($null -eq $BootEntry) { return '' }

    # Firmware entries may report a generic BootType such as 'Drive'. Include
    # the Device component and its runtime/type names to distinguish DVDDrive
    # from HardDiskDrive entries.
    $parts = [System.Collections.Generic.List[string]]::new()
    foreach ($typeName in @($BootEntry.PSObject.TypeNames)) {
        if (-not [string]::IsNullOrWhiteSpace([string]$typeName)) { $parts.Add([string]$typeName) }
    }
    $parts.Add($BootEntry.GetType().Name)

    foreach ($propertyName in @('BootType', 'DeviceType', 'Device', 'DeviceName', 'Description', 'Name')) {
        $property = $BootEntry.PSObject.Properties[$propertyName]
        if ($null -eq $property -or $null -eq $property.Value) { continue }

        $value = $property.Value
        $parts.Add([string]$value)
        if ($value -isnot [string]) {
            $parts.Add($value.GetType().Name)
            foreach ($typeName in @($value.PSObject.TypeNames)) {
                if (-not [string]::IsNullOrWhiteSpace([string]$typeName)) { $parts.Add([string]$typeName) }
            }
            foreach ($nestedName in @('DeviceType', 'DeviceName', 'Name', 'Type')) {
                $nestedProperty = $value.PSObject.Properties[$nestedName]
                if ($null -ne $nestedProperty -and $null -ne $nestedProperty.Value) {
                    $parts.Add([string]$nestedProperty.Value)
                }
            }
        }
    }
    return ($parts -join ' ')
}

function Get-ValidatedUbuntuFirmwarePath {
    param(
        [AllowNull()][object]$BootEntry,
        [Parameter(Mandatory = $true)][guid]$VmId,
        [Parameter(Mandatory = $true)][string]$VmName
    )
    if ($null -eq $BootEntry -or $VmId -eq [guid]::Empty) { return '' }

    $bootType = $BootEntry.PSObject.Properties['BootType']
    $device = $BootEntry.PSObject.Properties['Device']
    $firmwarePath = $BootEntry.PSObject.Properties['FirmwarePath']
    $entryVmId = $BootEntry.PSObject.Properties['VMId']
    $entryVmName = $BootEntry.PSObject.Properties['VMName']
    $checkpointId = $BootEntry.PSObject.Properties['VMCheckpointId']
    $checkpointName = $BootEntry.PSObject.Properties['VMCheckpointName']
    $snapshotId = $BootEntry.PSObject.Properties['VMSnapshotId']
    $snapshotName = $BootEntry.PSObject.Properties['VMSnapshotName']
    $isDeleted = $BootEntry.PSObject.Properties['IsDeleted']
    foreach ($property in @($bootType, $device, $firmwarePath, $entryVmId, $entryVmName, $checkpointId, $checkpointName, $snapshotId, $snapshotName, $isDeleted)) {
        if ($null -eq $property) { return '' }
    }

    $bootTypeIsFile = $false
    if ($bootType.Value -is [string]) {
        $bootTypeIsFile = [string]$bootType.Value -ceq 'File'
    }
    elseif ($null -ne $bootType.Value) {
        $bootTypeValueType = $bootType.Value.GetType()
        if ($bootTypeValueType.IsEnum -and $bootTypeValueType.FullName -ceq 'Microsoft.HyperV.PowerShell.VMBootSourceType') {
            $bootTypeIsFile = [Enum]::GetName($bootTypeValueType, $bootType.Value) -ceq 'File'
        }
    }
    if (-not $bootTypeIsFile -or $null -ne $device.Value) { return '' }
    if ($null -eq $entryVmId.Value -or $entryVmName.Value -isnot [string] -or [string]$entryVmName.Value -cne $VmName) { return '' }
    try { $actualVmId = [guid]$entryVmId.Value } catch { return '' }
    if ($actualVmId -ne $VmId) { return '' }

    foreach ($checkpoint in @($checkpointId, $snapshotId)) {
        if ($null -eq $checkpoint.Value) { return '' }
        try { $checkpointGuid = [guid]$checkpoint.Value } catch { return '' }
        if ($checkpointGuid -ne [guid]::Empty) { return '' }
    }
    if ([string]$checkpointName.Value -cne '' -or [string]$snapshotName.Value -cne '') { return '' }
    if ($isDeleted.Value -isnot [bool] -or [bool]$isDeleted.Value) { return '' }

    $path = [string]$firmwarePath.Value
    $match = [regex]::Match($path, '(?i)^HD\(1,GPT,(?<PartitionId>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}),0x[0-9a-f]+,0x[0-9a-f]+\)/\\EFI\\ubuntu\\shimx64\.efi$')
    if (-not $match.Success) { return '' }
    try { $partitionGuid = [guid]$match.Groups['PartitionId'].Value } catch { return '' }
    if ($partitionGuid -eq [guid]::Empty) { return '' }
    return $path
}
$fullVmRoot = Get-FullPath -Path $VmRoot
$driveRoot = [IO.Path]::GetPathRoot($fullVmRoot)
$assetsRoot = Join-Path $driveRoot 'TBoundAssets'
$vmStorageRoot = Join-Path $driveRoot 'TBoundVMs'
$parentPath = [IO.Path]::GetDirectoryName($fullVmRoot.TrimEnd('\'))
$diskPath = Join-Path (Join-Path $fullVmRoot 'Virtual Hard Disks') ($VmName + '.vhdx')
$isoFullPath = [IO.Path]::GetFullPath($IsoPath)

Add-Check -Name 'PowerShell is elevated for Hyper-V access' -Passed (Test-IsAdministrator)

try {
    Import-Module Hyper-V -ErrorAction Stop
    $hostInfo = Get-VMHost -ErrorAction Stop
    Add-Check -Name 'Hyper-V PowerShell module and host are available' -Passed $true
}
catch {
    Add-Check -Name 'Hyper-V PowerShell module and host are available' -Passed $false -Detail $_.Exception.Message
    throw 'Preflight cannot continue without Hyper-V host access.'
}

Add-Check -Name 'VmRoot is on F:' -Passed ($driveRoot.TrimEnd('\') -eq 'F:')
Add-Check -Name 'VmRoot is not the drive root' -Passed ($fullVmRoot.TrimEnd('\') -ne $driveRoot.TrimEnd('\'))
Add-Check -Name 'Existing F: path components contain no reparse points' -Passed (Test-NoReparsePathComponents -Path $fullVmRoot) -Detail $fullVmRoot
Add-Check -Name 'VmRoot parent directory exists' -Passed (Test-Path -LiteralPath $parentPath -PathType Container) -Detail $parentPath
Add-Check -Name 'VmRoot is inside the tracked TBound VM directory' -Passed ((Test-PathUnderRoot -Path $fullVmRoot -Root $vmStorageRoot) -and ($fullVmRoot.TrimEnd('\') -ine $vmStorageRoot.TrimEnd('\'))) -Detail $vmStorageRoot
Add-Check -Name 'Tracked TBound asset and VM directories exist' -Passed ((Test-Path -LiteralPath $assetsRoot -PathType Container) -and (Test-Path -LiteralPath $vmStorageRoot -PathType Container)) -Detail "$assetsRoot; $vmStorageRoot"
Add-Check -Name 'Tracked asset/VM roots contain no reparse path components' -Passed ((Test-NoReparsePathComponents -Path $assetsRoot) -and (Test-NoReparsePathComponents -Path $vmStorageRoot))
Add-Check -Name 'ISO is under the tracked TBound asset directory' -Passed (Test-PathUnderRoot -Path $isoFullPath -Root $assetsRoot) -Detail $isoFullPath

try {
    $driveLetter = $driveRoot.Substring(0, 1)
    $drive = Get-PSDrive -Name $driveLetter -ErrorAction Stop
    $hasReserve = ($null -ne $drive.Free) -and ($drive.Free -ge 700GB)
    Add-Check -Name 'F: retains at least 700 GiB free' -Passed $hasReserve -Detail ("Free bytes: {0}" -f $drive.Free)
    if ($Mode -eq 'Preflight') {
        $hasCreationHeadroom = ($null -ne $drive.Free) -and ($drive.Free -ge 800GB)
        Add-Check -Name 'F: has at least 800 GiB free before VM creation' -Passed $hasCreationHeadroom -Detail 'Creation gate reserves 700 GiB for the host plus 100 GiB for all tracked TBound files.'
    }
}
catch {
    Add-Check -Name 'F: free space is readable' -Passed $false -Detail $_.Exception.Message
}

try {
    $trackedBytes = Get-TrackedStorageBytes -RootPaths @($assetsRoot, $vmStorageRoot)
    Add-Check -Name 'Tracked TBound storage is at most 100 GiB' -Passed ($trackedBytes -le 100GB) -Detail ("Tracked bytes: {0}; {1:N2} GiB across {2} and {3}" -f $trackedBytes, ($trackedBytes / 1GB), $assetsRoot, $vmStorageRoot)
}
catch {
    Add-Check -Name 'Tracked TBound storage can be scanned safely' -Passed $false -Detail $_.Exception.Message
}

try {
    $switchMatches = @(Get-VMSwitch | Where-Object { $_.Name -eq $ProvisioningSwitchName })
    Add-Check -Name 'Provisioning switch exists exactly once' -Passed ($switchMatches.Count -eq 1) -Detail $ProvisioningSwitchName
}
catch {
    Add-Check -Name 'Provisioning switch can be inspected' -Passed $false -Detail $_.Exception.Message
}

try {
    $resolvedIso = (Resolve-Path -LiteralPath $IsoPath -ErrorAction Stop).Path
    $isoItem = Get-Item -LiteralPath $resolvedIso -ErrorAction Stop
    $isFile = -not $isoItem.PSIsContainer
    $isoTracked = Test-PathUnderRoot -Path $resolvedIso -Root $assetsRoot
    $isoHasNoReparseComponents = Test-NoReparsePathComponents -Path $resolvedIso
    Add-Check -Name 'ISO is a file inside the tracked TBound asset directory' -Passed ($isFile -and $isoTracked -and $isoHasNoReparseComponents) -Detail $resolvedIso
    if ($isFile) {
        $actualIsoSha256 = (Get-FileHash -LiteralPath $resolvedIso -Algorithm SHA256).Hash
        Add-Check -Name 'ISO SHA-256 matches verified Ubuntu checksum' -Passed ($actualIsoSha256 -ieq $ExpectedIsoSha256) -Detail ("Actual: {0}" -f $actualIsoSha256)
    }
}
catch {
    Add-Check -Name 'ISO exists and can be hashed' -Passed $false -Detail $_.Exception.Message
}

try {
    $allVms = @(Get-VM -ErrorAction Stop)
    $vmMatches = @($allVms | Where-Object { $_.Name -eq $VmName })
}
catch {
    Add-Check -Name 'Hyper-V VM inventory can be read' -Passed $false -Detail $_.Exception.Message
    throw 'Preflight cannot continue without VM inventory access.'
}

if ($Mode -eq 'Preflight') {
    Add-Check -Name 'VM name is unused' -Passed ($vmMatches.Count -eq 0) -Detail $VmName
    Add-Check -Name 'VmRoot does not already exist' -Passed (-not (Test-Path -LiteralPath $fullVmRoot)) -Detail $fullVmRoot
    Add-Check -Name 'VHDX target path does not already exist' -Passed (-not (Test-Path -LiteralPath $diskPath)) -Detail $diskPath
}
else {
    Add-Check -Name 'VM exists exactly once' -Passed ($vmMatches.Count -eq 1) -Detail $VmName
    Add-Check -Name 'VmRoot exists as a directory' -Passed (Test-Path -LiteralPath $fullVmRoot -PathType Container) -Detail $fullVmRoot
    Add-Check -Name 'VM root and VHDX paths contain no reparse points' -Passed ((Test-NoReparsePathComponents -Path $fullVmRoot) -and (Test-NoReparsePathComponents -Path $diskPath)) -Detail $diskPath

    if ($vmMatches.Count -eq 1) {
        $vm = $vmMatches[0]
        $expectedRoot = [IO.Path]::GetFullPath($fullVmRoot).TrimEnd('\')
        $expectedVmPath = [IO.Path]::GetFullPath((Join-Path $fullVmRoot $VmName)).TrimEnd('\')
        $actualRoot = [IO.Path]::GetFullPath([string]$vm.Path).TrimEnd('\')
        Add-Check -Name 'VM uses Generation 2' -Passed ([int]$vm.Generation -eq 2)
        Add-Check -Name 'VM configuration is stored at VmRoot' -Passed (($actualRoot -ieq $expectedRoot) -or ($actualRoot -ieq $expectedVmPath)) -Detail $actualRoot
        Add-Check -Name 'VM is shut down' -Passed ([string]$vm.State -eq 'Off') -Detail ([string]$vm.State)
        Add-Check -Name 'VM has ProductionOnly checkpoints' -Passed ([string]$vm.CheckpointType -eq 'ProductionOnly') -Detail ([string]$vm.CheckpointType)
        Add-Check -Name 'Automatic checkpoints are disabled' -Passed (-not [bool]$vm.AutomaticCheckpointsEnabled)
        $actualSnapshotPath = [string]$vm.SnapshotFileLocation
        $snapshotPathMatches = $false
        if (-not [string]::IsNullOrWhiteSpace($actualSnapshotPath)) {
            $actualSnapshotPath = [IO.Path]::GetFullPath($actualSnapshotPath).TrimEnd('\')
            $snapshotPathMatches = $actualSnapshotPath -ieq $fullVmRoot.TrimEnd('\')
        }
        Add-Check -Name 'Checkpoint files are stored under the tracked VM directory' -Passed $snapshotPathMatches -Detail $actualSnapshotPath

        $checkpoints = @(Get-VMSnapshot -VMName $VmName -ErrorAction Stop)
        Add-Check -Name 'VM has no more than one checkpoint' -Passed ($checkpoints.Count -le 1) -Detail ("Checkpoint count: {0}" -f $checkpoints.Count)
        if ($Mode -eq 'Created') {
            Add-Check -Name 'New VM has no checkpoints yet' -Passed ($checkpoints.Count -eq 0) -Detail ("Checkpoint count: {0}" -f $checkpoints.Count)
        }

        $processor = Get-VMProcessor -VMName $VmName
        Add-Check -Name 'VM has 8 virtual processors' -Passed ([int]$processor.Count -eq 8) -Detail ([string]$processor.Count)

        $memory = Get-VMMemory -VMName $VmName
        Add-Check -Name 'VM has fixed 16 GiB memory' -Passed ((-not [bool]$memory.DynamicMemoryEnabled) -and ([int64]$memory.Startup -eq 16GB))

        $attachedDisks = @(Get-VMHardDiskDrive -VMName $VmName)
        Add-Check -Name 'Exactly one hard disk is attached' -Passed ($attachedDisks.Count -eq 1)
        if ($attachedDisks.Count -eq 1) {
            $diskChain = [System.Collections.Generic.List[object]]::new()
            $seenDiskPaths = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
            $chainPath = [IO.Path]::GetFullPath([string]$attachedDisks[0].Path)
            $chainComplete = $true
            while (-not [string]::IsNullOrWhiteSpace($chainPath)) {
                if ($diskChain.Count -ge 3 -or -not $seenDiskPaths.Add($chainPath)) {
                    $chainComplete = $false
                    break
                }
                $chainVhd = Get-VHD -Path $chainPath -ErrorAction Stop
                $diskChain.Add($chainVhd)
                $chainPath = [string]$chainVhd.ParentPath
                if (-not [string]::IsNullOrWhiteSpace($chainPath)) {
                    $chainPath = [IO.Path]::GetFullPath($chainPath)
                }
            }
            $baseDiskMatches = @($diskChain | Where-Object { [IO.Path]::GetFullPath([string]$_.Path) -ieq [IO.Path]::GetFullPath($diskPath) })
            $chainBottomIsExpected = ($diskChain.Count -gt 0) -and ([IO.Path]::GetFullPath([string]$diskChain[$diskChain.Count - 1].Path) -ieq [IO.Path]::GetFullPath($diskPath))
            $chainUnderVmRoot = ($diskChain.Count -gt 0) -and (@($diskChain | Where-Object { -not (Test-PathUnderRoot -Path ([string]$_.Path) -Root $fullVmRoot) }).Count -eq 0)
            Add-Check -Name 'Attached disk chain is complete and no deeper than one checkpoint' -Passed ($chainComplete -and $diskChain.Count -le 2) -Detail ("Disk chain depth: {0}" -f $diskChain.Count)
            Add-Check -Name 'Expected VHDX is the base of the attached disk chain' -Passed (($baseDiskMatches.Count -eq 1) -and $chainBottomIsExpected) -Detail $diskPath
            Add-Check -Name 'All attached disk/checkpoint files are under the tracked VM directory' -Passed $chainUnderVmRoot
            if (($baseDiskMatches.Count -eq 1) -and $chainBottomIsExpected) {
                $baseVhd = $diskChain[$diskChain.Count - 1]
                Add-Check -Name 'Base VHDX is dynamic with a 40 GiB maximum' -Passed (([string]$baseVhd.VhdType -eq 'Dynamic') -and ([int64]$baseVhd.Size -eq 40GB)) -Detail ("Type: {0}; size: {1} bytes" -f $baseVhd.VhdType, $baseVhd.Size)
            }
            $attachedRoot = [IO.Path]::GetPathRoot([IO.Path]::GetFullPath([string]$attachedDisks[0].Path))
            Add-Check -Name 'Attached disk chain is stored on F:' -Passed ($attachedRoot.TrimEnd('\') -eq 'F:')
        }

        $services = @(Get-VMIntegrationService -VMName $VmName)
        $serviceRows = @(
            foreach ($service in $services) {
                $serviceId = Get-IntegrationServiceIdSuffix -Service $service -VmId ([guid]$vm.Id)
                $knownId = $integrationServicePolicy.Contains($serviceId)
                [pscustomobject]@{
                    Id = $serviceId
                    Label = if ($knownId) { $integrationServicePolicy[$serviceId].Label } else { 'Unknown' }
                    Known = $knownId
                    Enabled = [bool]$service.Enabled
                    ExpectedEnabled = if ($knownId) { [bool]$integrationServicePolicy[$serviceId].Enabled } else { $null }
                }
            }
        )
        $unknownServices = @($serviceRows | Where-Object { -not $_.Known })
        $missingRequiredServices = @($requiredIntegrationServiceIds | Where-Object { $_ -notin @($serviceRows.Id) })
        $serviceMismatches = @($serviceRows | Where-Object { -not $_.Known -or ($_.Enabled -ne $_.ExpectedEnabled) })
        $duplicateServiceIds = @($serviceRows | Group-Object Id | Where-Object { $_.Count -gt 1 })
        Add-Check -Name 'Integration services match the explicit ID allowlist' -Passed (($unknownServices.Count -eq 0) -and ($missingRequiredServices.Count -eq 0) -and ($serviceMismatches.Count -eq 0) -and ($duplicateServiceIds.Count -eq 0)) -Detail ('Enabled: Heartbeat, Shutdown, Time Synchronization. Disabled: Guest Service Interface, Key-Value Pair Exchange, VSS. IDs are compared, so localized display names do not affect this check.')
        Add-Check -Name 'Guest Service Interface is disabled' -Passed ((@($serviceRows | Where-Object { $_.Id -eq '6C09BB55-D683-4DA0-8931-C9BF705F6480' -and -not $_.Enabled }).Count) -eq 1)

        $adapters = @(Get-VMNetworkAdapter -VMName $VmName)
        $adapterDisconnected = ($adapters.Count -eq 1) -and [string]::IsNullOrWhiteSpace([string]$adapters[0].SwitchName)
        Add-Check -Name 'Exactly one VM network adapter exists and is disconnected' -Passed $adapterDisconnected

        $firmware = Get-VMFirmware -VMName $VmName
        $secureBoot = ([string]$firmware.SecureBoot -eq 'On') -and ([string]$firmware.SecureBootTemplate -eq 'MicrosoftUEFICertificateAuthority')
        Add-Check -Name 'Secure Boot uses the Linux template' -Passed $secureBoot -Detail ("SecureBoot: {0}; template: {1}" -f $firmware.SecureBoot, $firmware.SecureBootTemplate)

        $dvdDrives = @(Get-VMDvdDrive -VMName $VmName)
        $firstBoot = $null
        if (@($firmware.BootOrder).Count -gt 0) {
            $firstBoot = @($firmware.BootOrder)[0]
        }
        $firstBootType = Get-BootTypeName -BootEntry $firstBoot

        if ($Mode -eq 'Created') {
            $matchingDvd = @($dvdDrives | Where-Object {
                -not [string]::IsNullOrWhiteSpace([string]$_.Path) -and
                [IO.Path]::GetFullPath([string]$_.Path) -ieq $isoFullPath
            })
            Add-Check -Name 'Verified Ubuntu ISO is attached for installation' -Passed (($dvdDrives.Count -eq 1) -and ($matchingDvd.Count -eq 1))
            Add-Check -Name 'Installer DVD is first in boot order' -Passed ($firstBootType -match '(?i)DVD') -Detail $firstBootType
        }
        else {
            $dvdEjected = ($dvdDrives.Count -eq 1) -and [string]::IsNullOrWhiteSpace([string]$dvdDrives[0].Path)
            Add-Check -Name 'Installation ISO is ejected' -Passed $dvdEjected
            $ubuntuFirmwarePath = ''
            if ($null -ne $firstBoot) {
                $ubuntuFirmwarePath = Get-ValidatedUbuntuFirmwarePath -BootEntry $firstBoot -VmId ([guid]$vm.Id) -VmName $VmName
            }
            Add-Check -Name 'Ubuntu EFI shim is first in firmware boot order' -Passed (-not [string]::IsNullOrEmpty($ubuntuFirmwarePath)) -Detail $ubuntuFirmwarePath
        }

        if ([bool]$hostInfo.EnableEnhancedSessionMode) {
            Write-Host '[INFO] Host Enhanced Session policy is enabled globally; Microsoft documents clipboard/resource redirection for Windows guests. Ubuntu should remain in Basic Session.'
        }
        else {
            Write-Host '[INFO] Host Enhanced Session policy is disabled.'
        }
        Write-Host '[INFO] The verifier cannot inspect an open VMConnect window. For Ubuntu, confirm the VMConnect toolbar reports Basic Session or Enhanced Session unavailable; do not enable clipboard or resource redirection.'
        if ($Mode -eq 'Verify') {
            if ($BasicSessionConfirmed) {
                Write-Host '[ATTESTED] Operator reports Basic Session with no clipboard/device redirection; this is not machine-verified.'
            }
            else {
                Write-Host '[INCOMPLETE] Operator must confirm Basic Session with no clipboard/device redirection in VMConnect.'
            }
        }
    }
}

if ($script:Failures.Count -gt 0) {
    Write-Host ''
    Write-Host ("Preflight/verification failed: {0} check(s)." -f $script:Failures.Count)
    exit 1
}
Write-Host ''
if ($Mode -eq 'Verify' -and -not $BasicSessionConfirmed) {
    Write-Host 'Automated Verify checks passed, but the golden baseline is INCOMPLETE until the VMConnect Basic Session check is recorded.'
    exit 2
}
if ($Mode -eq 'Verify') {
    Write-Host 'Automated Verify checks passed; the Basic Session/no-redirection status was recorded by operator attestation and is not machine-verified.'
    exit 0
}
Write-Host "All $Mode checks passed."
