#requires -RunAsAdministrator
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$installRoot = 'C:\ProgramData\TBoundHostAutomation'
$receiptRoot = Join-Path $installRoot 'receipts'
$sourcePath = Join-Path $PSScriptRoot 'TBoundHostActions.ps1'
$profilePath = Join-Path $installRoot 'profile.json'
$vmName = 'TBound-Ubuntu-2404'
$vmRoot = 'F:\TBoundVMs\TBound-Ubuntu-2404'
$expectedBaseDiskPath = 'F:\TBoundVMs\TBound-Ubuntu-2404\Virtual Hard Disks\TBound-Ubuntu-2404.vhdx'
$vmStorageRoot = 'F:\TBoundVMs'
$assetsRoot = 'F:\TBoundAssets'
$switchName = 'Default Switch'
$policy = 'TRUSTEDVERIFIERONLY'
$reviewedActionSha256 = '19FD7656AAE0B7A3DF9F10AD98FE7E0B92668B07E8AD85933789B1BA64B37986'
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

function Get-BootTypeName {
    param([AllowNull()][object]$BootEntry)
    if ($null -eq $BootEntry) { return '' }
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
                if ($null -ne $nestedProperty -and $null -ne $nestedProperty.Value) { $parts.Add([string]$nestedProperty.Value) }
            }
        }
    }
    return ($parts -join ' ')
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

function Add-AclRule {
    param(
        [Parameter(Mandatory = $true)][object]$Acl,
        [Parameter(Mandatory = $true)][Security.Principal.SecurityIdentifier]$Sid,
        [Parameter(Mandatory = $true)][Security.AccessControl.FileSystemRights]$Rights,
        [Parameter(Mandatory = $true)][bool]$Directory
    )
    $inherit = [Security.AccessControl.InheritanceFlags]::None
    if ($Directory) { $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit }
    $rule = [Security.AccessControl.FileSystemAccessRule]::new($Sid, $Rights, $inherit, [Security.AccessControl.PropagationFlags]::None, [Security.AccessControl.AccessControlType]::Allow)
    $Acl.AddAccessRule($rule)
}

function Set-ProtectedPathAcl {
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][bool]$Directory, [switch]$OperatorReadOnly)
    if ($Directory) { $acl = [Security.AccessControl.DirectorySecurity]::new() } else { $acl = [Security.AccessControl.FileSecurity]::new() }
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner($script:AdminsSid)
    Add-AclRule $acl $script:SystemSid ([Security.AccessControl.FileSystemRights]::FullControl) $Directory
    Add-AclRule $acl $script:AdminsSid ([Security.AccessControl.FileSystemRights]::FullControl) $Directory
    if ($OperatorReadOnly) {
        Add-AclRule $acl $script:OperatorSid ([Security.AccessControl.FileSystemRights]::ReadAndExecute) $Directory
    }
    Set-Acl -LiteralPath $Path -AclObject $acl -ErrorAction Stop
}

if (-not (Test-Administrator)) { throw 'Run this installer from an elevated Windows PowerShell session.' }
if (-not [Environment]::Is64BitProcess) { throw 'Run the 64-bit Windows PowerShell executable.' }
if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) { throw ('Missing action source: ' + $sourcePath) }
if (Test-Path -LiteralPath $installRoot) { throw ('Refusing to overwrite existing protected install: ' + $installRoot) }
Assert-NoReparsePath $sourcePath
Assert-NoReparsePath 'C:\ProgramData'
if (-not (Test-Path -LiteralPath $vmRoot -PathType Container)) { throw ('Expected VM root is missing: ' + $vmRoot) }
Assert-NoReparsePath $vmRoot
$env:PSModulePath = Join-Path $PSHOME 'Modules'
$moduleManifest = Join-Path $PSHOME 'Modules\Hyper-V\Hyper-V.psd1'
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
if ($bootOrder.Count -lt 1 -or (Get-BootTypeName $bootOrder[0]) -notmatch '(?i)Hard.?Disk') {
    throw 'Target VM must boot from its hard disk first.'
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

$operatorSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
if ($null -eq $operatorSid) { throw 'Could not resolve installing operator SID.' }
$script:OperatorSid = $operatorSid
$script:SystemSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
$script:AdminsSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')

$scheduler = New-Object -ComObject Schedule.Service
$scheduler.Connect()
$rootFolder = $scheduler.GetFolder('\')
$existingFolders = @($rootFolder.GetFolders(0) | Where-Object { $_.Name -ceq 'TBound' })
if ($existingFolders.Count -ne 0) { throw 'Task Scheduler folder \TBound already exists; refusing to alter it.' }

$sourceBytesOnDisk = [IO.File]::ReadAllBytes($sourcePath)
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$byteOffset = 0
if ($sourceBytesOnDisk.Length -ge 3 -and $sourceBytesOnDisk[0] -eq 0xEF -and $sourceBytesOnDisk[1] -eq 0xBB -and $sourceBytesOnDisk[2] -eq 0xBF) { $byteOffset = 3 }
$sourceText = $utf8Strict.GetString($sourceBytesOnDisk, $byteOffset, $sourceBytesOnDisk.Length - $byteOffset)
$sourceText = $sourceText.Replace("`r`n", "`n").Replace("`r", "`n")
$sourceBytes = [Text.UTF8Encoding]::new($false).GetBytes($sourceText)
$sha = [Security.Cryptography.SHA256]::Create()
try { $sourceHash = ([BitConverter]::ToString($sha.ComputeHash($sourceBytes))).Replace('-', '') } finally { $sha.Dispose() }
if ($sourceHash -cne $reviewedActionSha256) { throw 'Action source does not match the installer pinned reviewed hash.' }
$profile = [ordered]@{
    schema = 1
    vmName = $vmName
    vmId = ([guid]$vm.Id).ToString()
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

if (-not $PSCmdlet.ShouldProcess($installRoot, 'Install protected TBound host tasks for the pinned VM and adapter')) { return }

New-Item -ItemType Directory -Path $installRoot -ErrorAction Stop | Out-Null
Assert-NoReparsePath $installRoot
Set-ProtectedPathAcl -Path $installRoot -Directory $true -OperatorReadOnly
New-Item -ItemType Directory -Path $receiptRoot -ErrorAction Stop | Out-Null
Assert-NoReparsePath $receiptRoot
Set-ProtectedPathAcl -Path $receiptRoot -Directory $true -OperatorReadOnly

$installedAction = Join-Path $installRoot 'TBoundHostActions.ps1'
[IO.File]::WriteAllBytes($installedAction, $sourceBytes)
Set-ProtectedPathAcl -Path $installedAction -Directory $false
$installedHash = (Get-FileHash -LiteralPath $installedAction -Algorithm SHA256 -ErrorAction Stop).Hash.ToUpperInvariant()
if ($installedHash -cne $reviewedActionSha256 -or $installedHash -cne $sourceHash) {
    throw 'Protected action read-back hash does not match the pinned reviewed source; no tasks were registered.'
}
[IO.File]::WriteAllText($profilePath, $profileJson, [Text.UTF8Encoding]::new($false))
Set-ProtectedPathAcl -Path $profilePath -Directory $false
Set-ProtectedPathAcl -Path $receiptRoot -Directory $true -OperatorReadOnly

$taskSddl = 'O:SYG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;GRGX;;;' + $operatorSid.Value + ')'
$folderSddl = $taskSddl
$taskFolder = $rootFolder.CreateFolder('TBound', $folderSddl)
$powerShellExe = Join-Path $PSHOME 'powershell.exe'
if (-not (Test-Path -LiteralPath $powerShellExe -PathType Leaf)) { throw ('Windows PowerShell executable missing: ' + $powerShellExe) }

foreach ($leaf in $taskLeaves) {
    $definition = $scheduler.NewTask(0)
    $definition.RegistrationInfo.Description = ('Fixed SYSTEM host action for ' + $vmName + '; no caller-controlled VM arguments.')
    $definition.Principal.UserId = 'S-1-5-18'
    $definition.Principal.LogonType = 5
    $definition.Principal.RunLevel = 1
    $definition.Settings.Enabled = $true
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
    $null = $taskFolder.RegisterTaskDefinition($leaf, $definition, 2, 'SYSTEM', $null, 5, $taskSddl)
}

Write-Host ('Installed five fixed SYSTEM tasks in \TBound for VM GUID ' + ([guid]$vm.Id).ToString() + '.')
Write-Host ('Protected action source: ' + $installedAction)
Write-Host ('Protected receipts: ' + $receiptRoot)
