#requires -RunAsAdministrator
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [Parameter(Mandatory = $true)]
    [string]$OperatorSid
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$installRoot = 'C:\ProgramData\TBoundHostAutomation'
$receiptRoot = Join-Path $installRoot 'receipts'
$operatorSshRoot = Join-Path $installRoot 'operator-ssh'
$operatorPublicRoot = Join-Path $installRoot 'operator-public'
$sourcePath = Join-Path $PSScriptRoot 'TBoundHostActions.ps1'
$accessPolicyPath = Join-Path $PSScriptRoot 'TBoundHostAccessPolicy.psm1'
$operatorSshBootstrapPath = Join-Path $PSScriptRoot 'Initialize-TBoundOperatorSsh.ps1'
$profilePath = Join-Path $installRoot 'profile.json'
$vmName = 'TBound-Ubuntu-2404'
$vmRoot = 'F:\TBoundVMs\TBound-Ubuntu-2404'
$expectedBaseDiskPath = 'F:\TBoundVMs\TBound-Ubuntu-2404\Virtual Hard Disks\TBound-Ubuntu-2404.vhdx'
$vmStorageRoot = 'F:\TBoundVMs'
$assetsRoot = 'F:\TBoundAssets'
$switchName = 'Default Switch'
$policy = 'TRUSTEDVERIFIERONLY'
$reviewedActionSha256 = '9EF7B338B18DA61204F84E3B74FD1ADA609652E4FC7A25DEB5CF633605581D26'
$reviewedAccessPolicySha256 = 'C06BAE160364EB4D88914A3468FBB766C426010145ACA9D2C43DD803A993BB15'
$reviewedOperatorSshBootstrapSha256 = 'E521CCB79AD0CF51E7F48A9B027082F5FD92D640697146F6DB0601BFFF596851'
$operatorHostAlias = '172.19.207.142'
$operatorHostFingerprint = 'SHA256:22ZTQXN5L54AhovENJ0Eo7ZViomYVvlHRWfbG3izY5g'
$GiB = [int64]1073741824
$taskLeaves = @('Inspect', 'Disconnect', 'StopOffline', 'ConnectOff', 'StartTrustedMaintenance')

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-FullPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    return [IO.Path]::GetFullPath($Path).TrimEnd('\')
}

function Get-Sha256Hex {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '') }
    finally { $sha.Dispose() }
}

function Test-UnderRoot {
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$Root)
    $p = Get-FullPath $Path
    $r = Get-FullPath $Root
    return ($p -ieq $r) -or $p.StartsWith($r + '\', [StringComparison]::OrdinalIgnoreCase)
}

function Assert-NoReparsePath {
    param([Parameter(Mandatory = $true)][string]$Path)
    $full = [IO.Path]::GetFullPath($Path)
    $driveRoot = [IO.Path]::GetPathRoot($full)
    $current = $driveRoot
    $rootItem = Get-Item -LiteralPath $driveRoot -Force -ErrorAction Stop
    if (($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw ('Reparse point at volume root: ' + $driveRoot) }
    foreach ($part in ($full.Substring($driveRoot.Length) -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($part)) { continue }
        $current = Join-Path $current $part
        if (-not (Test-Path -LiteralPath $current)) { break }
        $item = Get-Item -LiteralPath $current -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw ('Reparse point in path: ' + $current) }
    }
}

function Get-TrackedBytes {
    [int64]$sum = 0
    foreach ($root in @($assetsRoot, $vmStorageRoot)) {
        if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw ('Required tracked directory is missing: ' + $root) }
        Assert-NoReparsePath $root
        $pending = [Collections.Generic.Stack[string]]::new()
        $pending.Push([IO.Path]::GetFullPath($root))
        while ($pending.Count -gt 0) {
            $directory = $pending.Pop()
            foreach ($item in @(Get-ChildItem -LiteralPath $directory -Force -ErrorAction Stop)) {
                if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw ('Tracked storage contains a reparse point: ' + $item.FullName) }
                if ($item.PSIsContainer) { $pending.Push($item.FullName) } else { $sum += [int64]$item.Length }
            }
        }
    }
    return $sum
}

function ConvertTo-MacAddress {
    param([Parameter(Mandatory = $true)][string]$Address)
    $hex = ($Address -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
    if ($hex.Length -ne 12) { throw 'Hyper-V adapter MAC address is missing or malformed.' }
    $parts = @()
    for ($index = 0; $index -lt 12; $index += 2) { $parts += $hex.Substring($index, 2) }
    return ($parts -join ':')
}

function Assert-IntegrationServicePolicy {
    param([Parameter(Mandatory = $true)][guid]$VmId)
    $policy = [ordered]@{
        '84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47' = $true
        '9F8233AC-BE49-4C79-8EE3-E7E1985B2077' = $true
        '2497F4DE-E9FA-4204-80E4-4B75C46419C0' = $true
        '6C09BB55-D683-4DA0-8931-C9BF705F6480' = $false
        '2A34B1C2-FD73-4043-8A5B-DD2159BC743F' = $false
        '5CED1297-4598-4915-A5FC-AD21BB4D02A4' = $false
    }
    $services = @(Get-VMIntegrationService -VMName $vmName -ErrorAction Stop)
    $found = [Collections.Generic.List[string]]::new()
    foreach ($service in $services) {
        $idProperty = $service.PSObject.Properties['Id']
        if ($null -eq $idProperty -or $null -eq $idProperty.Value) { throw 'Integration service is missing its stable Id.' }
        $match = [regex]::Match([string]$idProperty.Value, '(?i)(?<ServiceId>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$')
        if (-not $match.Success) { throw 'Integration service Id does not end in a GUID.' }
        $serviceId = $match.Groups['ServiceId'].Value.ToUpperInvariant()
        $expectedObjectId = 'Microsoft:{0}\{1}' -f $VmId.ToString().ToUpperInvariant(), $serviceId
        if ([string]$idProperty.Value -ine $expectedObjectId -or -not $policy.Contains($serviceId)) { throw 'Integration service scope or GUID is not allowlisted.' }
        $vmIdProperty = $service.PSObject.Properties['VMId']
        if ($null -ne $vmIdProperty -and $null -ne $vmIdProperty.Value -and [guid]$vmIdProperty.Value -ne $VmId) { throw 'Integration service is scoped to a different VM.' }
        $found.Add($serviceId)
        if ([bool]$service.Enabled -ne [bool]$policy[$serviceId]) { throw 'Integration service enabled state differs from the fixed allowlist.' }
    }
    $duplicates = @($found | Group-Object | Where-Object Count -ne 1)
    $missing = @($policy.Keys | Where-Object { $_ -notin $found })
    if ($duplicates.Count -gt 0 -or $missing.Count -gt 0 -or $services.Count -ne $policy.Count) {
        throw 'Integration service inventory contains missing or duplicate allowlisted GUIDs.'
    }
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

function Get-VhdChain {
    param([Parameter(Mandatory = $true)][string]$Path)
    $chain = [Collections.Generic.List[object]]::new()
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $current = [IO.Path]::GetFullPath($Path)
    while (-not [string]::IsNullOrWhiteSpace($current)) {
        if (-not $seen.Add($current) -or -not (Test-UnderRoot $current $vmRoot)) { throw 'Virtual disk chain cycles or leaves the tracked VM root.' }
        Assert-NoReparsePath $current
        $vhd = Get-VHD -Path $current -ErrorAction Stop
        $chain.Add($vhd)
        if ($chain.Count -gt 2) { throw 'Virtual disk chain exceeds one checkpoint.' }
        $current = [string]$vhd.ParentPath
    }
    if ($chain.Count -lt 1) { throw 'Virtual disk chain is empty.' }
    $base = $chain[$chain.Count - 1]
    if ([string]$base.VhdType -ne 'Dynamic' -or [int64]$base.Size -ne (40 * $GiB)) { throw 'Base disk must be the expected 40 GiB dynamic VHD.' }
    foreach ($disk in $chain) {
        if ([string]$disk.VhdType -notin @('Dynamic', 'Differencing')) { throw 'Virtual disk chain contains an unsupported disk type.' }
    }
    if ((Get-FullPath ([string]$base.Path)) -ine (Get-FullPath $expectedBaseDiskPath)) { throw 'Virtual disk chain does not terminate at the exact base VHDX path.' }
    return ,$chain.ToArray()
}

function Set-ProtectedPathAcl {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][bool]$Directory,
        [ValidateSet('None', 'Read')][string]$OperatorAccess = 'None'
    )
    $acl = New-TBoundProtectedPathSecurity -Directory:$Directory -OperatorSid $script:OperatorSid.Value -OperatorAccess $OperatorAccess
    Set-Acl -LiteralPath $Path -AclObject $acl -ErrorAction Stop
    $readback = Get-Acl -LiteralPath $Path -ErrorAction Stop
    if (-not (Test-TBoundProtectedPathSecurity -Acl $readback -Directory:$Directory -OperatorSid $script:OperatorSid.Value -OperatorAccess $OperatorAccess)) {
        throw ('Protected path ACL read-back differs from the fixed policy: ' + $Path)
    }
}

function New-ProtectedDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [ValidateSet('None', 'Read', 'Modify')][string]$OperatorAccess = 'None',
        [switch]$OperatorKeyOnly
    )
    $acl = if ($OperatorKeyOnly) {
        New-TBoundOperatorKeyDirectorySecurity -OperatorSid $script:OperatorSid.Value
    }
    else {
        New-TBoundProtectedPathSecurity -Directory $true -OperatorSid $script:OperatorSid.Value -OperatorAccess $OperatorAccess
    }
    $null = New-TBoundDirectoryWithSecurity -Path $Path -Acl $acl
    if (-not $OperatorKeyOnly) { Assert-NoReparsePath $Path }
    $readback = Get-Acl -LiteralPath $Path -ErrorAction Stop
    $valid = if ($OperatorKeyOnly) {
        Test-TBoundOperatorKeyDirectorySecurity -Acl $readback -OperatorSid $script:OperatorSid.Value
    }
    else {
        Test-TBoundProtectedPathSecurity -Acl $readback -Directory $true -OperatorSid $script:OperatorSid.Value -OperatorAccess $OperatorAccess
    }
    if (-not $valid) { throw ('Protected directory ACL read-back differs from the fixed policy: ' + $Path) }
    if (-not $OperatorKeyOnly -and @(Get-ChildItem -LiteralPath $Path -Force -ErrorAction Stop).Count -ne 0) {
        throw ('New protected directory is not empty: ' + $Path)
    }
}

function Assert-RegisteredTaskPolicy {
    param(
        [Parameter(Mandatory = $true)][string]$Leaf,
        [Parameter(Mandatory = $true)][bool]$ExpectedEnabled,
        [Parameter(Mandatory = $true)][ValidateSet('AdministratorOnly', 'OperatorRunQuery')][string]$AccessMode
    )

    $tasks = @(Get-ScheduledTask -TaskPath '\TBound\' -TaskName $Leaf -ErrorAction Stop)
    if ($tasks.Count -ne 1) { throw ('Task registration is missing or ambiguous: \TBound\' + $Leaf) }
    $task = $tasks[0]
    $expectedArguments = '-NoLogo -NoProfile -NonInteractive -File "' + (Join-Path $installRoot 'TBoundHostActions.ps1') + '" -Task ' + $Leaf
    if (@($task.Actions).Count -ne 1 -or
        [IO.Path]::GetFullPath([string]$task.Actions[0].Execute) -ine (Get-FullPath (Join-Path $PSHOME 'powershell.exe')) -or
        [string]$task.Actions[0].Arguments -cne $expectedArguments -or
        [IO.Path]::GetFullPath([string]$task.Actions[0].WorkingDirectory) -ine (Get-FullPath $installRoot)) {
        throw ('Task action read-back differs from the fixed action: \TBound\' + $Leaf)
    }
    $principalSid = [string]$task.Principal.UserId
    if ($principalSid -notin @('S-1-5-18', 'SYSTEM', 'NT AUTHORITY\SYSTEM')) {
        try { $principalSid = ([Security.Principal.NTAccount]::new($principalSid)).Translate([Security.Principal.SecurityIdentifier]).Value }
        catch { throw ('Could not resolve registered task principal: ' + $principalSid) }
    }
    if ($principalSid -cne 'S-1-5-18' -or
        [string]$task.Principal.LogonType -cne 'ServiceAccount' -or
        [string]$task.Principal.RunLevel -cne 'Highest') {
        throw ('Task principal read-back differs from the fixed SYSTEM policy: \TBound\' + $Leaf)
    }
    if (@($task.Triggers).Count -ne 0 -or [bool]$task.Settings.Enabled -ne $ExpectedEnabled -or
        -not [bool]$task.Settings.AllowDemandStart -or [bool]$task.Settings.StartWhenAvailable -or
        [string]$task.Settings.MultipleInstances -cne 'IgnoreNew' -or
        [string]$task.Settings.ExecutionTimeLimit -cne 'PT0S' -or
        [bool]$task.Settings.DisallowStartIfOnBatteries -or
        [bool]$task.Settings.StopIfGoingOnBatteries -or
        [bool]$task.Settings.RunOnlyIfIdle) {
        throw ('Task settings read-back differs from the fixed on-demand policy: \TBound\' + $Leaf)
    }

    $registeredTask = $script:TaskFolder.GetTask($Leaf)
    $taskSddl = [string]$registeredTask.GetSecurityDescriptor(0x7)
    $validTaskAcl = if ($AccessMode -eq 'AdministratorOnly') {
        Test-TBoundTaskSecurityDescriptor -Sddl $taskSddl -OperatorSid ''
    }
    else {
        Test-TBoundTaskSecurityDescriptor -Sddl $taskSddl -OperatorSid $script:OperatorSid.Value
    }
    if (-not $validTaskAcl) {
        throw ('Task security descriptor read-back differs from fixed ' + $AccessMode + ' policy: \TBound\' + $Leaf)
    }
}

if (-not (Test-Administrator)) { throw 'Run this installer from an elevated Windows PowerShell session.' }
if (-not [Environment]::Is64BitProcess) { throw 'Run the 64-bit Windows PowerShell executable.' }
$expectedPowerShellHome = Get-FullPath (Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0')
if ((Get-FullPath $PSHOME) -ine $expectedPowerShellHome) { throw 'Run this installer from 64-bit Windows PowerShell 5.1.' }
if ((Get-FullPath $PSScriptRoot) -ine (Get-FullPath 'C:\ProgramData\TBoundHostAutomationSource')) {
    throw 'Run only the hash-verified installer staged under C:\ProgramData\TBoundHostAutomationSource.'
}
if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) { throw ('Missing action source: ' + $sourcePath) }
if (-not (Test-Path -LiteralPath $accessPolicyPath -PathType Leaf)) { throw ('Missing access policy source: ' + $accessPolicyPath) }
if (-not (Test-Path -LiteralPath $operatorSshBootstrapPath -PathType Leaf)) { throw ('Missing operator SSH bootstrap source: ' + $operatorSshBootstrapPath) }
if (Test-Path -LiteralPath $installRoot) { throw ('Refusing to overwrite existing protected install: ' + $installRoot) }
Assert-NoReparsePath $sourcePath
Assert-NoReparsePath $accessPolicyPath
Assert-NoReparsePath $operatorSshBootstrapPath
Assert-NoReparsePath 'C:\ProgramData'
$accessPolicyBytes = [IO.File]::ReadAllBytes($accessPolicyPath)
$operatorSshBootstrapBytes = [IO.File]::ReadAllBytes($operatorSshBootstrapPath)
if ((Get-Sha256Hex $accessPolicyBytes) -cne $reviewedAccessPolicySha256) { throw 'Access policy source does not match the pinned reviewed hash.' }
if ((Get-Sha256Hex $operatorSshBootstrapBytes) -cne $reviewedOperatorSshBootstrapSha256) { throw 'Operator SSH bootstrap source does not match the pinned reviewed hash.' }
Import-Module -Name $accessPolicyPath -Force -ErrorAction Stop
$script:OperatorSid = Resolve-TBoundOperatorSid $OperatorSid
Import-Module ScheduledTasks -ErrorAction Stop
if (-not (Test-Path -LiteralPath $vmRoot -PathType Container)) { throw ('Expected VM root is missing: ' + $vmRoot) }
Assert-NoReparsePath $vmRoot
$env:PSModulePath = Join-Path $PSHOME 'Modules'
$moduleManifest = Join-Path $PSHOME 'Modules\Hyper-V\2.0.0.0\Hyper-V.psd1'
if (-not (Test-Path -LiteralPath $moduleManifest -PathType Leaf)) { throw 'Protected Windows Hyper-V module manifest is missing.' }
Assert-NoReparsePath $moduleManifest
Import-Module -Name $moduleManifest -ErrorAction Stop
$switches = @(Get-VMSwitch -ErrorAction Stop | Where-Object { $_.Name -ceq $switchName })
if ($switches.Count -ne 1) { throw 'Default Switch must exist exactly once.' }

$vms = @(Get-VM -ErrorAction Stop | Where-Object { $_.Name -ceq $vmName })
if ($vms.Count -ne 1) { throw ('Expected exactly one existing VM named ' + $vmName + '.') }
$vm = $vms[0]
if ([int]$vm.Generation -ne 2) { throw 'Target VM must be Generation 2.' }
$actualVmPath = Get-FullPath ([string]$vm.Path)
$allowedVmPaths = @((Get-FullPath $vmRoot), (Get-FullPath (Join-Path $vmRoot $vmName)))
if ($actualVmPath -notin $allowedVmPaths) { throw ('VM configuration path is outside the fixed root: ' + $actualVmPath) }
if ((Get-FullPath ([string]$vm.SnapshotFileLocation)) -ine (Get-FullPath $vmRoot)) { throw 'Checkpoint files must remain under the fixed VM root.' }
if ([string]$vm.State -cne 'Off') { throw 'Install requires the target VM host state Off.' }
if ([string]$vm.CheckpointType -ne 'ProductionOnly' -or [bool]$vm.AutomaticCheckpointsEnabled) { throw 'Target VM checkpoint policy must be ProductionOnly with automatic checkpoints disabled.' }

$processor = Get-VMProcessor -VMName $vmName -ErrorAction Stop
if ([int]$processor.Count -ne 8) { throw 'Target VM must have exactly 8 processors.' }
$memory = Get-VMMemory -VMName $vmName -ErrorAction Stop
if ([bool]$memory.DynamicMemoryEnabled -or [int64]$memory.Startup -ne (16 * $GiB)) { throw 'Target VM must have fixed 16 GiB startup memory.' }

$snapshots = @(Get-VMSnapshot -VMName $vmName -ErrorAction Stop)
if ($snapshots.Count -gt 1) { throw 'Target VM may have at most one checkpoint.' }
$disks = @(Get-VMHardDiskDrive -VMName $vmName -ErrorAction Stop)
if ($disks.Count -ne 1) { throw 'Target VM must have exactly one attached virtual disk.' }
$diskChain = Get-VhdChain ([string]$disks[0].Path)
Assert-IntegrationServicePolicy ([guid]$vm.Id)
$firmware = Get-VMFirmware -VMName $vmName -ErrorAction Stop
if ([string]$firmware.SecureBoot -cne 'On' -or [string]$firmware.SecureBootTemplate -cne 'MicrosoftUEFICertificateAuthority') {
    throw 'Target VM must retain Secure Boot with the Microsoft UEFI Certificate Authority template.'
}
$dvdDrives = @(Get-VMDvdDrive -VMName $vmName -ErrorAction Stop)
if ($dvdDrives.Count -ne 1 -or -not [string]::IsNullOrWhiteSpace([string]$dvdDrives[0].Path)) { throw 'Target VM must have exactly one DVD drive with no ISO attached.' }
$bootOrder = @($firmware.BootOrder)
$firmwarePath = ''
if ($bootOrder.Count -gt 0) {
    $firmwarePath = Get-ValidatedUbuntuFirmwarePath -BootEntry $bootOrder[0] -VmId ([guid]$vm.Id) -VmName $vmName
}
if ([string]::IsNullOrEmpty($firmwarePath)) {
    throw 'Target VM must boot from the pinned Ubuntu EFI shim entry first.'
}

$adapters = @(Get-VMNetworkAdapter -VMName $vmName -ErrorAction Stop)
if ($adapters.Count -ne 1) { throw 'Target VM must have exactly one network adapter.' }
$adapter = $adapters[0]
$adapterMacAddress = ConvertTo-MacAddress ([string]$adapter.MacAddress)
if (-not [string]::IsNullOrWhiteSpace([string]$adapter.SwitchName)) { throw 'Install requires the target VM NIC disconnected.' }

$trackedBytes = Get-TrackedBytes
if ($trackedBytes -gt (100 * $GiB)) { throw 'Tracked TBound files exceed the 100 GiB budget.' }
$drive = [IO.DriveInfo]::new('F:\')
if (-not $drive.IsReady -or [int64]$drive.AvailableFreeSpace -lt (700 * $GiB)) { throw 'F: is unavailable or has less than 700 GiB free.' }

$allVms = @(Get-VM -ErrorAction Stop)
if (@($allVms | Where-Object { [guid]$_.Id -eq [guid]$vm.Id }).Count -ne 1) { throw 'VM GUID did not resolve uniquely.' }
if ((Get-FullPath ([string]$disks[0].Path)) -notin @((Get-FullPath $vmRoot)) -and -not (Test-UnderRoot ([string]$disks[0].Path) $vmRoot)) {
    throw 'Attached disk path is not under the fixed VM root.'
}

$scheduler = New-Object -ComObject Schedule.Service
$scheduler.Connect()
$rootFolder = $scheduler.GetFolder('\')
$existingFolders = @($rootFolder.GetFolders(0) | Where-Object { $_.Name -ceq 'TBound' })
if ($existingFolders.Count -ne 0) { throw 'Task Scheduler folder \TBound already exists; refusing to alter it.' }

$sshKeygen = Join-Path $env:WINDIR 'System32\OpenSSH\ssh-keygen.exe'
$adminKnownHosts = 'F:\TBoundAssets\ProvisioningSSH\known_hosts'
if (-not (Test-Path -LiteralPath $sshKeygen -PathType Leaf) -or -not (Test-Path -LiteralPath $adminKnownHosts -PathType Leaf)) {
    throw 'Windows OpenSSH ssh-keygen or the existing public guest known_hosts file is unavailable.'
}
$hostMatches = @(& $sshKeygen -F $operatorHostAlias -f $adminKnownHosts 2>$null)
if ($LASTEXITCODE -ne 0) { throw 'The existing known_hosts file lacks the fixed guest host alias.' }
$hostLines = @($hostMatches | Where-Object { $_ -and -not $_.StartsWith('#') })
if ($hostLines.Count -ne 1 -or $hostLines[0] -notmatch '^\S+\s+ssh-ed25519\s+\S+(?:\s+.*)?$') {
    throw 'The fixed guest host alias must resolve to exactly one ED25519 public host key.'
}
$fingerprintPath = Join-Path ([IO.Path]::GetTempPath()) ('tbound-host-fingerprint-' + [guid]::NewGuid().ToString('N') + '.txt')
try {
    [IO.File]::WriteAllText($fingerprintPath, ($hostLines[0] + "`n"), [Text.UTF8Encoding]::new($false))
    $hostFingerprintLines = @(& $sshKeygen -lf $fingerprintPath 2>$null)
    $fingerprintMatch = if ($hostFingerprintLines.Count -eq 1) { [regex]::Match($hostFingerprintLines[0], '(?:^|\s)(?<Fingerprint>SHA256:[A-Za-z0-9+/]+={0,2})(?:\s|$)') } else { [regex]::Match('', 'a^') }
    if ($LASTEXITCODE -ne 0 -or -not (Test-TBoundPinnedEd25519Fingerprint -Lines $hostFingerprintLines -ExpectedFingerprint $operatorHostFingerprint)) {
        throw 'The existing public host key does not match the exact pinned ED25519 fingerprint.'
    }
}
finally {
    Remove-Item -LiteralPath $fingerprintPath -Force -ErrorAction SilentlyContinue
}

$sourceBytesOnDisk = [IO.File]::ReadAllBytes($sourcePath)
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$byteOffset = 0
if ($sourceBytesOnDisk.Length -ge 3 -and $sourceBytesOnDisk[0] -eq 0xEF -and $sourceBytesOnDisk[1] -eq 0xBB -and $sourceBytesOnDisk[2] -eq 0xBF) { $byteOffset = 3 }
$sourceText = $utf8Strict.GetString($sourceBytesOnDisk, $byteOffset, $sourceBytesOnDisk.Length - $byteOffset)
$sourceText = $sourceText.Replace("`r`n", "`n").Replace("`r", "`n")
$sourceBytes = [Text.UTF8Encoding]::new($false).GetBytes($sourceText)
$sourceHash = Get-Sha256Hex $sourceBytes
if ($sourceHash -cne $reviewedActionSha256) { throw 'Action source does not match the installer pinned reviewed hash.' }
$profile = [ordered]@{
    schema = 2
    vmName = $vmName
    vmId = ([guid]$vm.Id).ToString()
    firmwarePath = $firmwarePath
    vmPath = $actualVmPath
    vmRoot = (Get-FullPath $vmRoot)
    vmStorageRoot = (Get-FullPath $vmStorageRoot)
    assetsRoot = (Get-FullPath $assetsRoot)
    adapterId = ([guid]$adapter.Id).ToString()
    adapterMacAddress = $adapterMacAddress
    switchName = $switchName
    switchId = ([guid]$switches[0].Id).ToString()
    maintenancePolicy = $policy
    profileSha256 = $sourceHash
}
$profileJson = $profile | ConvertTo-Json -Depth 4
$profileBytes = [Text.UTF8Encoding]::new($false).GetBytes($profileJson)
$expectedProfileHash = Get-Sha256Hex $profileBytes

if (-not $PSCmdlet.ShouldProcess($installRoot, 'Install protected TBound host tasks for the pinned VM and adapter')) { return }

$script:TaskFolder = $null
$taskFolder = $null
$taskSddl = Get-TBoundTaskSecurityDescriptor -OperatorSid $script:OperatorSid.Value
$adminTaskSddl = Get-TBoundTaskAdministratorSecurityDescriptor

try {
New-ProtectedDirectory -Path $installRoot -OperatorAccess Read
New-ProtectedDirectory -Path $receiptRoot -OperatorAccess Read
New-ProtectedDirectory -Path $operatorSshRoot -OperatorKeyOnly
New-ProtectedDirectory -Path $operatorPublicRoot -OperatorAccess Modify

$installedAction = Join-Path $installRoot 'TBoundHostActions.ps1'
[IO.File]::WriteAllBytes($installedAction, $sourceBytes)
Set-ProtectedPathAcl -Path $installedAction -Directory $false
$installedHash = (Get-FileHash -LiteralPath $installedAction -Algorithm SHA256 -ErrorAction Stop).Hash.ToUpperInvariant()
if ($installedHash -cne $reviewedActionSha256 -or $installedHash -cne $sourceHash) {
    throw 'Protected action read-back hash does not match the pinned reviewed source; no tasks were registered.'
}
$installedPolicy = Join-Path $installRoot 'TBoundHostAccessPolicy.psm1'
[IO.File]::WriteAllBytes($installedPolicy, $accessPolicyBytes)
Set-ProtectedPathAcl -Path $installedPolicy -Directory $false -OperatorAccess Read
if ((Get-FileHash -LiteralPath $installedPolicy -Algorithm SHA256 -ErrorAction Stop).Hash.ToUpperInvariant() -cne $reviewedAccessPolicySha256) {
    throw 'Protected access policy read-back hash does not match the pinned reviewed source.'
}
$installedSshBootstrap = Join-Path $installRoot 'Initialize-TBoundOperatorSsh.ps1'
[IO.File]::WriteAllBytes($installedSshBootstrap, $operatorSshBootstrapBytes)
Set-ProtectedPathAcl -Path $installedSshBootstrap -Directory $false -OperatorAccess Read
if ((Get-FileHash -LiteralPath $installedSshBootstrap -Algorithm SHA256 -ErrorAction Stop).Hash.ToUpperInvariant() -cne $reviewedOperatorSshBootstrapSha256) {
    throw 'Protected operator SSH bootstrap read-back hash does not match the pinned reviewed source.'
}
[IO.File]::WriteAllBytes($profilePath, $profileBytes)
Set-ProtectedPathAcl -Path $profilePath -Directory $false
if ((Get-FileHash -LiteralPath $profilePath -Algorithm SHA256 -ErrorAction Stop).Hash.ToUpperInvariant() -cne $expectedProfileHash) {
    throw 'Protected profile read-back hash does not match the exact profile written.'
}
$operatorKnownHostsPath = Join-Path $operatorPublicRoot 'known_hosts'
$operatorKnownHostsBytes = [Text.UTF8Encoding]::new($false).GetBytes($hostLines[0] + "`n")
[IO.File]::WriteAllBytes($operatorKnownHostsPath, $operatorKnownHostsBytes)
Set-ProtectedPathAcl -Path $operatorKnownHostsPath -Directory $false -OperatorAccess Read
if ((Get-FileHash -LiteralPath $operatorKnownHostsPath -Algorithm SHA256 -ErrorAction Stop).Hash.ToUpperInvariant() -cne (Get-Sha256Hex $operatorKnownHostsBytes)) {
    throw 'Protected operator known_hosts read-back hash differs from the pinned public host key.'
}

$rootAclReadback = Get-Acl -LiteralPath $installRoot -ErrorAction Stop
if (-not (Test-TBoundProtectedPathSecurity -Acl $rootAclReadback -Directory $true -OperatorSid $script:OperatorSid.Value -OperatorAccess Read)) {
    throw 'ProgramData install-root ACL read-back differs from the fixed policy.'
}
$receiptAclReadback = Get-Acl -LiteralPath $receiptRoot -ErrorAction Stop
if (-not (Test-TBoundProtectedPathSecurity -Acl $receiptAclReadback -Directory $true -OperatorSid $script:OperatorSid.Value -OperatorAccess Read)) {
    throw 'ProgramData receipt-root ACL read-back differs from the fixed read-only policy.'
}

$folderSddl = $adminTaskSddl
$taskFolder = $rootFolder.CreateFolder('TBound', $folderSddl)
$script:TaskFolder = $taskFolder
$folderReadback = [string]$taskFolder.GetSecurityDescriptor(0x7)
if (-not (Test-TBoundTaskSecurityDescriptor -Sddl $folderReadback -OperatorSid '')) {
    throw 'New task folder does not have the fixed administrator-only staging ACL.'
}
$powerShellExe = Join-Path $PSHOME 'powershell.exe'
if (-not (Test-Path -LiteralPath $powerShellExe -PathType Leaf)) { throw ('Windows PowerShell executable missing: ' + $powerShellExe) }

    foreach ($leaf in $taskLeaves) {
        $definition = $scheduler.NewTask(0)
        $definition.RegistrationInfo.Description = ('Fixed SYSTEM host action for ' + $vmName + '; no caller-controlled VM arguments.')
        $definition.Principal.UserId = 'S-1-5-18'
        $definition.Principal.LogonType = 5
        $definition.Principal.RunLevel = 1
        $definition.Settings.Enabled = $false
        $definition.Settings.AllowDemandStart = $true
        $definition.Settings.StartWhenAvailable = $false
        $definition.Settings.MultipleInstances = 2
        $definition.Settings.ExecutionTimeLimit = 'PT0S'
        $definition.Settings.DisallowStartIfOnBatteries = $false
        $definition.Settings.StopIfGoingOnBatteries = $false
        $definition.Settings.RunOnlyIfIdle = $false
        $action = $definition.Actions.Create(0)
        $action.Path = $powerShellExe
        $action.WorkingDirectory = $installRoot
        $action.Arguments = ('-NoLogo -NoProfile -NonInteractive -File "' + $installedAction + '" -Task ' + $leaf)
        $null = $taskFolder.RegisterTaskDefinition($leaf, $definition, 2, 'SYSTEM', $null, 5, $adminTaskSddl)
    }

    $folderReadback = [string]$taskFolder.GetSecurityDescriptor(0x7)
    if (-not (Test-TBoundTaskSecurityDescriptor -Sddl $folderReadback -OperatorSid '')) {
        throw 'Task folder staging ACL changed before task verification.'
    }
    foreach ($leaf in $taskLeaves) { Assert-RegisteredTaskPolicy -Leaf $leaf -ExpectedEnabled $false -AccessMode AdministratorOnly }

    foreach ($leaf in $taskLeaves) { $taskFolder.GetTask($leaf).Enabled = $true }
    foreach ($leaf in $taskLeaves) { Assert-RegisteredTaskPolicy -Leaf $leaf -ExpectedEnabled $true -AccessMode AdministratorOnly }

    foreach ($leaf in $taskLeaves) { $taskFolder.GetTask($leaf).SetSecurityDescriptor($taskSddl, 0) }
    $taskFolder.SetSecurityDescriptor($taskSddl, 0)
    foreach ($leaf in $taskLeaves) { Assert-RegisteredTaskPolicy -Leaf $leaf -ExpectedEnabled $true -AccessMode OperatorRunQuery }
    $folderReadback = [string]$taskFolder.GetSecurityDescriptor(0x7)
    if (-not (Test-TBoundTaskSecurityDescriptor -Sddl $folderReadback -OperatorSid $script:OperatorSid.Value)) {
        throw 'Task folder ACL read-back is not SYSTEM/Admin control plus exact operator run/query.'
    }
}
catch {
    $installFailure = [string]$_.Exception.Message
    $recoveryProblems = [Collections.Generic.List[string]]::new()
    if ($null -ne $taskFolder) {
        foreach ($leaf in $taskLeaves) {
            try {
                $registeredTask = $taskFolder.GetTask($leaf)
                if ($null -ne $registeredTask) {
                    $registeredTask.Enabled = $false
                    $registeredTask.SetSecurityDescriptor($adminTaskSddl, 0)
                }
            }
            catch { $recoveryProblems.Add($leaf + ': ' + [string]$_.Exception.Message) }
        }
        try { $taskFolder.SetSecurityDescriptor($adminTaskSddl, 0) }
        catch { $recoveryProblems.Add('folder ACL: ' + [string]$_.Exception.Message) }
    }
    if ($recoveryProblems.Count -eq 0) {
        throw ('Host automation preparation failed: ' + $installFailure + '. Any recognized tasks were left disabled with the administrator-only ACL. Preserve ' + $installRoot + ' and ' + 'C:\ProgramData\TBoundHostAutomationSource' + '; do not retry until the exact partial state has been inspected.')
    }
    throw ('Host automation preparation failed: ' + $installFailure + '. Recovery did not finish for: ' + ($recoveryProblems -join '; ') + '. Preserve ' + $installRoot + ' and C:\ProgramData\TBoundHostAutomationSource; inspect Task Scheduler before any retry.')
}

Write-Host ('Installed five fixed SYSTEM tasks in \TBound for VM GUID ' + ([guid]$vm.Id).ToString() + '.')
Write-Host ('Protected action source: ' + $installedAction)
Write-Host ('Protected receipts: ' + $receiptRoot)
Write-Host ('Operator task access: ' + $script:OperatorSid.Value + ' (run/query only)')
Write-Host ('Operator key directory: ' + $operatorSshRoot + ' (dedicated SID only)')
