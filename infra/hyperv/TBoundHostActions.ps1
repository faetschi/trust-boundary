[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Inspect', 'Disconnect', 'StopOffline', 'ConnectOff', 'StartTrustedMaintenance')]
    [string]$Task
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:Root = 'C:\ProgramData\TBoundHostAutomation'
$script:Action = Join-Path $script:Root 'TBoundHostActions.ps1'
$script:ProfileFile = Join-Path $script:Root 'profile.json'
$script:Receipts = Join-Path $script:Root 'receipts'
$script:VmName = 'TBound-Ubuntu-2404'
$script:VmRoot = 'F:\TBoundVMs\TBound-Ubuntu-2404'
$script:BaseDiskPath = 'F:\TBoundVMs\TBound-Ubuntu-2404\Virtual Hard Disks\TBound-Ubuntu-2404.vhdx'
$script:StorageRoot = 'F:\TBoundVMs'
$script:AssetsRoot = 'F:\TBoundAssets'
$script:SwitchName = 'Default Switch'
$script:Policy = 'TRUSTEDVERIFIERONLY'
$script:GiB = [int64]1073741824
$script:MutexName = 'Global\TBoundHostAutomationActions'
$script:ProfileSha256 = ''
$script:MaintenanceStartAttempted = $false
$script:OfflineCleanupNeeded = $false
$script:ConnectAttempted = ($Task -eq 'ConnectOff')

function Get-ExpectedTaskTransition {
    param([Parameter(Mandatory = $true)][ValidateSet('Inspect', 'Disconnect', 'StopOffline', 'ConnectOff', 'StartTrustedMaintenance')][string]$Name)
    switch ($Name) {
        'Inspect' { return [pscustomobject]@{ InitialVmStates = @('*'); FinalVmState = '*'; FinalSwitchName = '*' } }
        'Disconnect' { return [pscustomobject]@{ InitialVmStates = @('*'); FinalVmState = 'SAME'; FinalSwitchName = '' } }
        'StopOffline' { return [pscustomobject]@{ InitialVmStates = @('*'); FinalVmState = 'Off'; FinalSwitchName = '' } }
        'ConnectOff' { return [pscustomobject]@{ InitialVmStates = @('Off'); FinalVmState = 'Off'; FinalSwitchName = $script:SwitchName } }
        'StartTrustedMaintenance' { return [pscustomobject]@{ InitialVmStates = @('Off'); FinalVmState = 'Running'; FinalSwitchName = $script:SwitchName } }
    }
}

function Stop-Action {
    param([Parameter(Mandatory = $true)][ValidateSet('FAIL', 'TIMEOUT', 'ABORTED_UNKNOWN')][string]$Result, [Parameter(Mandatory = $true)][string]$Message)
    $e = [InvalidOperationException]::new($Message)
    $e.Data['TBoundResult'] = $Result
    throw $e
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
    $root = [IO.Path]::GetPathRoot($full)
    $current = $root
    foreach ($part in @($root) + @($full.Substring($root.Length) -split '[\\/]')) {
        if ($part -eq $root) {
            $item = Get-Item -LiteralPath $root -Force -ErrorAction Stop
        }
        else {
            if ([string]::IsNullOrWhiteSpace($part)) { continue }
            $current = Join-Path $current $part
            if (-not (Test-Path -LiteralPath $current)) { break }
            $item = Get-Item -LiteralPath $current -Force -ErrorAction Stop
        }
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw ('Reparse point in protected path: ' + $item.FullName)
        }
    }
}

function Get-TrackedBytes {
    [int64]$sum = 0
    foreach ($root in @($script:AssetsRoot, $script:StorageRoot)) {
        if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw ('Missing tracked directory: ' + $root) }
        Assert-NoReparsePath $root
        $pending = [Collections.Generic.Stack[string]]::new()
        $pending.Push([IO.Path]::GetFullPath($root))
        while ($pending.Count -gt 0) {
            $dir = $pending.Pop()
            foreach ($item in @(Get-ChildItem -LiteralPath $dir -Force -ErrorAction Stop)) {
                if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw ('Reparse point in tracked storage: ' + $item.FullName) }
                if ($item.PSIsContainer) { $pending.Push($item.FullName) } else { $sum += [int64]$item.Length }
            }
        }
    }
    return $sum
}

function Assert-StorageBudget {
    if ((Get-TrackedBytes) -gt (100 * $script:GiB)) { Stop-Action 'FAIL' 'Tracked TBound storage exceeds 100 GiB.' }
    $drive = [IO.DriveInfo]::new('F:\')
    if (-not $drive.IsReady -or [int64]$drive.AvailableFreeSpace -lt (700 * $script:GiB)) {
        Stop-Action 'FAIL' 'F: is unavailable or has less than 700 GiB free.'
    }
}

function Get-VhdChain {
    param([Parameter(Mandatory = $true)][string]$Path)
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $chain = [Collections.Generic.List[object]]::new()
    $current = [IO.Path]::GetFullPath($Path)
    while (-not [string]::IsNullOrWhiteSpace($current)) {
        if (-not $seen.Add($current) -or -not (Test-UnderRoot $current $script:VmRoot)) { throw 'Virtual disk chain cycles or leaves the tracked VM root.' }
        Assert-NoReparsePath $current
        $vhd = Get-VHD -Path $current -ErrorAction Stop
        $chain.Add($vhd)
        if ($chain.Count -gt 2) { throw 'Virtual disk chain exceeds one checkpoint.' }
        $current = [string]$vhd.ParentPath
    }
    if ($chain.Count -lt 1) { throw 'Virtual disk chain is empty.' }
    $base = $chain[$chain.Count - 1]
    if ([string]$base.VhdType -ne 'Dynamic' -or [int64]$base.Size -ne (40 * $script:GiB)) { throw 'Base disk is not the expected 40 GiB dynamic VHD.' }
    foreach ($disk in $chain) {
        if ([string]$disk.VhdType -notin @('Dynamic', 'Differencing')) { throw 'Virtual disk chain contains an unsupported disk type.' }
    }
    if ((Get-FullPath ([string]$base.Path)) -ine (Get-FullPath $script:BaseDiskPath)) { throw 'Virtual disk chain does not terminate at the pinned base VHDX.' }
    return ,$chain.ToArray()
}

function Read-Profile {
    if (-not (Test-Path -LiteralPath $script:Action -PathType Leaf) -or -not (Test-Path -LiteralPath $script:ProfileFile -PathType Leaf) -or -not (Test-Path -LiteralPath $script:Receipts -PathType Container)) {
        Stop-Action 'FAIL' 'Protected host automation installation is incomplete.'
    }
    Assert-NoReparsePath $script:Action
    Assert-NoReparsePath $script:ProfileFile
    Assert-NoReparsePath $script:Receipts
    if ((Get-FullPath $PSCommandPath) -ine (Get-FullPath $script:Action)) { Stop-Action 'FAIL' 'Actions may run only from the protected installed path.' }
    $p = Get-Content -LiteralPath $script:ProfileFile -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
    if ([int]$p.schema -ne 2 -or [string]$p.vmName -cne $script:VmName -or [string]$p.vmRoot -ine $script:VmRoot -or
        [string]$p.vmStorageRoot -ine $script:StorageRoot -or [string]$p.assetsRoot -ine $script:AssetsRoot -or
        [string]$p.switchName -cne $script:SwitchName -or [string]$p.maintenancePolicy -cne $script:Policy -or
        [string]$p.adapterMacAddress -cnotmatch '^(?:[0-9A-F]{2}:){5}[0-9A-F]{2}$') {
        Stop-Action 'FAIL' 'Installed profile differs from fixed host policy.'
    }
    $hash = (Get-FileHash -LiteralPath $script:Action -Algorithm SHA256 -ErrorAction Stop).Hash.ToUpperInvariant()
    if ([string]$p.profileSha256 -cne $hash) { Stop-Action 'FAIL' 'Installed source hash does not match protected profile.' }
    $script:ProfileSha256 = $hash
    return $p
}

function ConvertTo-MacAddress {
    param([Parameter(Mandatory = $true)][string]$Address)
    $hex = ($Address -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
    if ($hex.Length -ne 12) { throw 'Hyper-V adapter MAC address is missing or malformed.' }
    $parts = @()
    for ($index = 0; $index -lt 12; $index += 2) { $parts += $hex.Substring($index, 2) }
    return ($parts -join ':')
}

function Get-TargetSnapshot {
    param([Parameter(Mandatory = $true)][object]$Profile)
    $vm = Get-VM -Id ([guid]$Profile.vmId) -ErrorAction Stop
    $matches = @(Get-VM -ErrorAction Stop | Where-Object { $_.Name -ceq $script:VmName })
    if ($matches.Count -ne 1 -or [guid]$matches[0].Id -ne [guid]$Profile.vmId -or [string]$vm.Name -cne $script:VmName) {
        Stop-Action 'FAIL' 'Installed VM name/GUID does not resolve uniquely.'
    }
    $adapters = @(Get-VMNetworkAdapter -VMName $script:VmName -ErrorAction Stop)
    if ($adapters.Count -ne 1 -or [guid]$adapters[0].Id -ne [guid]$Profile.adapterId) {
        Stop-Action 'FAIL' 'Installed NIC GUID does not resolve to exactly one adapter.'
    }
    $mac = ConvertTo-MacAddress ([string]$adapters[0].MacAddress)
    if ($mac -cne [string]$Profile.adapterMacAddress) { Stop-Action 'FAIL' 'Installed NIC MAC changed.' }
    return [pscustomobject]@{ Vm = $vm; Adapter = $adapters[0]; VmState = [string]$vm.State; SwitchName = [string]$adapters[0].SwitchName; AdapterMacAddress = $mac }
}

function Assert-IntegrationServicePolicy {
    param([Parameter(Mandatory = $true)][object]$Profile)
    $policy = [ordered]@{
        '84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47' = $true
        '9F8233AC-BE49-4C79-8EE3-E7E1985B2077' = $true
        '2497F4DE-E9FA-4204-80E4-4B75C46419C0' = $true
        '6C09BB55-D683-4DA0-8931-C9BF705F6480' = $false
        '2A34B1C2-FD73-4043-8A5B-DD2159BC743F' = $false
        '5CED1297-4598-4915-A5FC-AD21BB4D02A4' = $false
    }
    $services = @(Get-VMIntegrationService -VMName $script:VmName -ErrorAction Stop)
    $found = [Collections.Generic.List[string]]::new()
    foreach ($service in $services) {
        $idProperty = $service.PSObject.Properties['Id']
        if ($null -eq $idProperty -or $null -eq $idProperty.Value) { throw 'Integration service is missing its stable Id.' }
        $match = [regex]::Match([string]$idProperty.Value, '(?i)(?<ServiceId>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$')
        if (-not $match.Success) { throw 'Integration service Id does not end in a GUID.' }
        $serviceId = $match.Groups['ServiceId'].Value.ToUpperInvariant()
        $expectedObjectId = 'Microsoft:{0}\{1}' -f ([guid]$Profile.vmId).ToString().ToUpperInvariant(), $serviceId
        if ([string]$idProperty.Value -ine $expectedObjectId -or -not $policy.Contains($serviceId)) { throw 'Integration service scope or GUID is not allowlisted.' }
        $vmIdProperty = $service.PSObject.Properties['VMId']
        if ($null -ne $vmIdProperty -and $null -ne $vmIdProperty.Value -and [guid]$vmIdProperty.Value -ne [guid]$Profile.vmId) {
            throw 'Integration service is scoped to a different VM.'
        }
        $found.Add($serviceId)
        if ([bool]$service.Enabled -ne [bool]$policy[$serviceId]) { throw 'Integration service enabled state differs from the fixed allowlist.' }
    }
    $duplicates = @($found | Group-Object | Where-Object Count -ne 1)
    $missing = @($policy.Keys | Where-Object { $_ -notin $found })
    if ($duplicates.Count -gt 0 -or $missing.Count -gt 0 -or $services.Count -ne $policy.Count) {
        throw 'Integration service inventory contains missing or duplicate allowlisted GUIDs.'
    }
}

function Import-TrustedHyperVModule {
    $expectedPowerShellHome = Get-FullPath (Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0')
    if ((Get-FullPath $PSHOME) -ine $expectedPowerShellHome) { Stop-Action 'FAIL' 'Host actions require 64-bit Windows PowerShell 5.1.' }
    $env:PSModulePath = Join-Path $PSHOME 'Modules'
    $moduleManifest = Join-Path $PSHOME 'Modules\Hyper-V\2.0.0.0\Hyper-V.psd1'
    if (-not (Test-Path -LiteralPath $moduleManifest -PathType Leaf)) { Stop-Action 'FAIL' 'Protected Windows Hyper-V module manifest is missing.' }
    Assert-NoReparsePath $moduleManifest
    Import-Module -Name $moduleManifest -ErrorAction Stop
}

function Assert-PinnedIdentity {
    param([Parameter(Mandatory = $true)][object]$Profile)
    return (Get-TargetSnapshot $Profile)
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

function Assert-MaintenanceProfile {
    param([Parameter(Mandatory = $true)][object]$Profile, [switch]$StorageCheck, [switch]$ServiceCheck)
    $initial = Assert-PinnedIdentity $Profile
    $vm = $initial.Vm
    $actualVmPath = Get-FullPath ([string]$vm.Path)
    $allowedVmPaths = @((Get-FullPath $script:VmRoot), (Get-FullPath (Join-Path $script:VmRoot $script:VmName)))
    if ([int]$vm.Generation -ne 2 -or $actualVmPath -ine (Get-FullPath ([string]$Profile.vmPath)) -or
        $actualVmPath -notin $allowedVmPaths -or (Get-FullPath ([string]$vm.SnapshotFileLocation)) -ine $script:VmRoot) {
        Stop-Action 'FAIL' 'VM generation or installed storage path changed.'
    }
    Assert-NoReparsePath $script:VmRoot
    if ([int](Get-VMProcessor -VMName $script:VmName -ErrorAction Stop).Count -ne 8) { Stop-Action 'FAIL' 'VM must retain 8 processors.' }
    $memory = Get-VMMemory -VMName $script:VmName -ErrorAction Stop
    if ([bool]$memory.DynamicMemoryEnabled -or [int64]$memory.Startup -ne (16 * $script:GiB)) { Stop-Action 'FAIL' 'VM must retain fixed 16 GiB memory.' }
    if ([string]$vm.CheckpointType -ne 'ProductionOnly' -or [bool]$vm.AutomaticCheckpointsEnabled) { Stop-Action 'FAIL' 'VM checkpoint policy changed.' }
    if (@(Get-VMSnapshot -VMName $script:VmName -ErrorAction Stop).Count -gt 1) { Stop-Action 'FAIL' 'VM has more than one checkpoint.' }
    $disks = @(Get-VMHardDiskDrive -VMName $script:VmName -ErrorAction Stop)
    if ($disks.Count -ne 1) { Stop-Action 'FAIL' 'VM must have exactly one attached disk.' }
    $null = Get-VhdChain ([string]$disks[0].Path)
    $firmware = Get-VMFirmware -VMName $script:VmName -ErrorAction Stop
    if ([string]$firmware.SecureBoot -cne 'On' -or [string]$firmware.SecureBootTemplate -cne 'MicrosoftUEFICertificateAuthority') {
        Stop-Action 'FAIL' 'Secure Boot or the Linux certificate template changed.'
    }
    $dvdDrives = @(Get-VMDvdDrive -VMName $script:VmName -ErrorAction Stop)
    if ($dvdDrives.Count -ne 1 -or -not [string]::IsNullOrWhiteSpace([string]$dvdDrives[0].Path)) {
        Stop-Action 'FAIL' 'VM must have exactly one DVD drive with no ISO attached.'
    }
    $bootOrder = @($firmware.BootOrder)
    $actualFirmwarePath = ''
    if ($bootOrder.Count -gt 0) {
        $actualFirmwarePath = Get-ValidatedUbuntuFirmwarePath -BootEntry $bootOrder[0] -VmId ([guid]$Profile.vmId) -VmName $script:VmName
    }
    if ([string]::IsNullOrEmpty($actualFirmwarePath) -or $actualFirmwarePath -cne [string]$Profile.firmwarePath) {
        Stop-Action 'FAIL' 'The pinned Ubuntu EFI shim entry must remain first in firmware boot order.'
    }
    $switches = @(Get-VMSwitch -ErrorAction Stop | Where-Object { $_.Name -ceq $script:SwitchName })
    if ($switches.Count -ne 1 -or [guid]$switches[0].Id -ne [guid]$Profile.switchId) { Stop-Action 'FAIL' 'Installed Default Switch GUID is missing or ambiguous.' }
    if ($ServiceCheck) { Assert-IntegrationServicePolicy $Profile }
    if ($StorageCheck) { Assert-StorageBudget }
    return (Assert-PinnedIdentity $Profile)
}

function Wait-ForState {
    param([object]$Profile, [string]$VmState, [string]$SwitchName, [int]$Seconds)
    $until = [DateTimeOffset]::UtcNow.AddSeconds($Seconds)
    do {
        $s = Get-TargetSnapshot $Profile
        if (($VmState -ceq '*' -or $s.VmState -ceq $VmState) -and $s.SwitchName -ceq $SwitchName) { return $s }
        Start-Sleep -Milliseconds 750
    } while ([DateTimeOffset]::UtcNow -lt $until)
    Stop-Action 'TIMEOUT' ('Timed out waiting for VM=' + $VmState + ', switch=' + $SwitchName + '.')
}

function Assert-FreshReceipt {
    param([string]$Leaf, [object]$Profile, [DateTimeOffset]$Now)
    $path = Join-Path $script:Receipts ($Leaf + '.json')
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { Stop-Action 'FAIL' ('Missing fresh ' + $Leaf + ' receipt.') }
    Assert-NoReparsePath $path
    try {
        $r = Get-Content -LiteralPath $path -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
        $start = [DateTimeOffset]::Parse([string]$r.startedUtc)
        $done = [DateTimeOffset]::Parse([string]$r.completedUtc)
        if ([int]$r.schema -ne 1 -or [string]$r.task -cne ('\TBound\' + $Leaf) -or [string]$r.operation -cne $Leaf -or
            [string]$r.result -cne 'PASS' -or [guid]$r.vmId -ne [guid]$Profile.vmId -or [guid]$r.adapterId -ne [guid]$Profile.adapterId -or
            [string]$r.profileSha256 -cne $script:ProfileSha256 -or $start -gt $Now.AddSeconds(5) -or $done -lt $start -or
            ($Now - $done).TotalSeconds -lt -5 -or ($Now - $done).TotalMinutes -gt 15) { Stop-Action 'FAIL' ('Stale or mismatched ' + $Leaf + ' receipt.') }
        return $r
    }
    catch {
        if ($_.Exception.Data.Contains('TBoundResult')) { throw }
        Stop-Action 'FAIL' ('Invalid ' + $Leaf + ' receipt.')
    }
}

function Invoke-TaskAction {
    param([object]$Profile, [object]$Initial)
    $policy = Get-ExpectedTaskTransition $Task
    if ($policy.InitialVmStates -notcontains '*' -and $Initial.VmState -notin $policy.InitialVmStates) {
        Stop-Action 'FAIL' ('Task refuses initial VM state ' + $Initial.VmState + '.')
    }

    switch ($Task) {
        'Inspect' { return }
        'Disconnect' {
            if (-not [string]::IsNullOrWhiteSpace($Initial.SwitchName)) { Disconnect-VMNetworkAdapter -VMNetworkAdapter $Initial.Adapter -ErrorAction Stop }
            $null = Wait-ForState $Profile '*' '' 30
            return
        }
        'StopOffline' {
            $script:OfflineCleanupNeeded = $true
            if (-not [string]::IsNullOrWhiteSpace($Initial.SwitchName)) { Disconnect-VMNetworkAdapter -VMNetworkAdapter $Initial.Adapter -ErrorAction Stop }
            $s = Wait-ForState $Profile '*' '' 30
            if ($s.VmState -eq 'Off') { return }
            if ($s.VmState -cne 'Running') { Stop-Action 'FAIL' ('Unsupported VM state after NIC disconnect: ' + $s.VmState + '.') }
            $s = Assert-PinnedIdentity $Profile
            if ($s.VmState -cne 'Running' -or -not [string]::IsNullOrWhiteSpace($s.SwitchName)) {
                Stop-Action 'ABORTED_UNKNOWN' 'VM or NIC changed during StopOffline admission; NIC remains disconnected.'
            }
            try { Stop-VM -Name $script:VmName -ErrorAction Stop }
            catch { Stop-Action 'ABORTED_UNKNOWN' 'Graceful host shutdown request failed; NIC remains disconnected.' }
            $null = Wait-ForState $Profile 'Off' '' 180
            return
        }
        'ConnectOff' {
            $script:ConnectAttempted = $true
            if ($Initial.VmState -cne 'Off') { Stop-Action 'FAIL' 'ConnectOff requires host VM state Off.' }
            if (-not [string]::IsNullOrWhiteSpace($Initial.SwitchName)) {
                if ($Initial.SwitchName -ceq $script:SwitchName) { return }
                Stop-Action 'FAIL' 'ConnectOff refuses an adapter attached to another switch.'
            }
            $s = Assert-MaintenanceProfile $Profile -StorageCheck -ServiceCheck
            if ($s.VmState -cne 'Off' -or -not [string]::IsNullOrWhiteSpace($s.SwitchName)) { Stop-Action 'FAIL' 'VM or NIC changed before ConnectOff.' }
            $script:ConnectAttempted = $true
            Connect-VMNetworkAdapter -VMNetworkAdapter $s.Adapter -SwitchName $script:SwitchName -ErrorAction Stop
            $null = Wait-ForState $Profile 'Off' $script:SwitchName 30
            return
        }
        'StartTrustedMaintenance' {
            if ([string]$Profile.maintenancePolicy -cne $script:Policy) { Stop-Action 'FAIL' 'Only the installed trusted-verifier policy is permitted.' }
            if ($Initial.VmState -cne 'Off' -or $Initial.SwitchName -cne $script:SwitchName) { Stop-Action 'FAIL' 'Trusted maintenance requires host VM Off and connected to Default Switch.' }
            $now = [DateTimeOffset]::UtcNow
            $stopped = Assert-FreshReceipt 'StopOffline' $Profile $now
            $connected = Assert-FreshReceipt 'ConnectOff' $Profile $now
            if ([DateTimeOffset]::Parse([string]$connected.completedUtc) -lt [DateTimeOffset]::Parse([string]$stopped.completedUtc) -or
                [string]$stopped.finalVmState -cne 'Off' -or -not [string]::IsNullOrWhiteSpace([string]$stopped.finalSwitchName) -or
                [string]$connected.finalVmState -cne 'Off' -or [string]$connected.finalSwitchName -cne $script:SwitchName) {
                Stop-Action 'FAIL' 'StopOffline/ConnectOff receipts do not establish recent host quiescence.'
            }
            $s = Assert-MaintenanceProfile $Profile -StorageCheck -ServiceCheck
            if ($s.VmState -cne 'Off' -or $s.SwitchName -cne $script:SwitchName) { Stop-Action 'FAIL' 'Host state changed before trusted maintenance start.' }
            $script:MaintenanceStartAttempted = $true
            Start-VM -Name $script:VmName -ErrorAction Stop
            $null = Wait-ForState $Profile 'Running' $script:SwitchName 180
            return
        }
    }
}

function Write-Receipt {
    param([object]$Receipt)
    if (-not (Test-Path -LiteralPath $script:Receipts -PathType Container)) { return }
    $leaf = [IO.Path]::GetFileNameWithoutExtension($script:ReceiptPath)
    $tmp = Join-Path $script:Receipts ('.' + $leaf + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
    [IO.File]::WriteAllText($tmp, ($Receipt | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $tmp -Destination (Join-Path $script:Receipts ($leaf + '.json')) -Force -ErrorAction Stop
}

$script:ReceiptPath = Join-Path $script:Receipts ($Task + '.json')
$startedUtc = [DateTimeOffset]::UtcNow
$invocationId = [guid]::NewGuid().ToString()
$result = 'FAIL'
$message = ''
$vmId = $null
$adapterId = $null
$adapterMacAddress = $null
$initialVmState = $null
$finalVmState = $null
$initialSwitchName = $null
$finalSwitchName = $null
$profile = $null
$initial = $null
$mutex = $null
$mutexHeld = $false

try {
    $profile = Read-Profile
    $vmId = [string]$profile.vmId
    $adapterId = [string]$profile.adapterId
    $adapterMacAddress = [string]$profile.adapterMacAddress
    $mutex = [Threading.Mutex]::new($false, $script:MutexName)
    try {
        $mutexHeld = $mutex.WaitOne([TimeSpan]::FromSeconds(30))
    }
    catch [Threading.AbandonedMutexException] {
        $mutexHeld = $true
        Stop-Action 'ABORTED_UNKNOWN' 'Previous action abandoned the host mutex; inspect VM state.'
    }
    if (-not $mutexHeld) { Stop-Action 'TIMEOUT' 'Timed out waiting for another host action.' }
    if ($Task -eq 'ConnectOff') { $script:ConnectAttempted = $true }
    Import-TrustedHyperVModule
    $initial = Assert-PinnedIdentity $profile
    $initialVmState = $initial.VmState
    $initialSwitchName = $initial.SwitchName
    if ($Task -in @('Inspect', 'ConnectOff', 'StartTrustedMaintenance')) {
        $initial = Assert-MaintenanceProfile $profile -StorageCheck:($Task -in @('Inspect', 'ConnectOff', 'StartTrustedMaintenance')) -ServiceCheck
        $initialVmState = $initial.VmState
        $initialSwitchName = $initial.SwitchName
    }
    Invoke-TaskAction $profile $initial
    $result = 'PASS'
    $message = 'Fixed host action and required host state transition confirmed.'
}
catch {
    if ($_.Exception.Data.Contains('TBoundResult')) { $result = [string]$_.Exception.Data['TBoundResult'] }
    else { $result = 'FAIL' }
    $message = ([string]$_.Exception.Message -replace '[\r\n]+', ' ')
    if ([string]::IsNullOrWhiteSpace($message)) { $message = 'Host action failed.' }
    if ($message.Length -gt 1000) { $message = $message.Substring(0, 1000) }
}
finally {
    if ($mutexHeld) {
        try {
            if ($null -ne $profile) {
            try {
                $final = Get-TargetSnapshot $profile
                $finalVmState = $final.VmState
                $finalSwitchName = $final.SwitchName
            }
            catch {
                if ($result -eq 'PASS') { $result = 'ABORTED_UNKNOWN' }
                $message = ($message + ' Final host state unavailable.')
            }
        }

        if ($result -eq 'PASS' -and $null -ne $initial) {
            $transition = Get-ExpectedTaskTransition $Task
            if ($transition.FinalVmState -eq 'SAME' -and [string]$finalVmState -cne [string]$initialVmState) {
                $result = 'ABORTED_UNKNOWN'
                $message = 'Final VM state changed unexpectedly.'
            }
            elseif ($transition.FinalVmState -ne '*' -and [string]$finalVmState -cne $transition.FinalVmState) {
                $result = 'ABORTED_UNKNOWN'
                $message = 'Final VM state does not match task policy.'
            }
            if ($transition.FinalSwitchName -ne '*' -and [string]$finalSwitchName -cne $transition.FinalSwitchName) {
                $result = 'ABORTED_UNKNOWN'
                $message = 'Final switch does not match task policy.'
            }
        }

        $mustDisconnectOnFailure = ($Task -in @('Disconnect', 'StopOffline')) -or
            ($Task -eq 'ConnectOff' -and $script:ConnectAttempted) -or
            ($Task -eq 'StartTrustedMaintenance' -and ($script:MaintenanceStartAttempted -or
                ($finalVmState -eq 'Running' -and -not [string]::IsNullOrWhiteSpace([string]$finalSwitchName))))
        if ($mustDisconnectOnFailure -and $result -ne 'PASS' -and $null -ne $profile) {
            try {
                $current = Assert-PinnedIdentity $profile
                if (-not [string]::IsNullOrWhiteSpace($current.SwitchName)) {
                    Disconnect-VMNetworkAdapter -VMNetworkAdapter $current.Adapter -ErrorAction Stop
                }
                $null = Wait-ForState $profile '*' '' 30
            }
            catch {
                $result = 'ABORTED_UNKNOWN'
                $message = ($message + ' Failed to confirm required NIC disconnect during cleanup.')
            }
            try {
                $final = Assert-PinnedIdentity $profile
                $finalVmState = $final.VmState
                $finalSwitchName = $final.SwitchName
            }
            catch {
                $result = 'ABORTED_UNKNOWN'
                $finalVmState = $null
                $finalSwitchName = $null
                $message = ($message + ' Final host state unavailable after cleanup.')
            }
        }

        if ($result -eq 'PASS' -and $null -ne $initial) {
            $transition = Get-ExpectedTaskTransition $Task
            if ($transition.FinalVmState -eq 'SAME' -and [string]$finalVmState -cne [string]$initialVmState) {
                $result = 'ABORTED_UNKNOWN'
                $message = 'Final VM state changed unexpectedly.'
            }
            elseif ($transition.FinalVmState -ne '*' -and [string]$finalVmState -cne $transition.FinalVmState) {
                $result = 'ABORTED_UNKNOWN'
                $message = 'Final VM state does not match task policy.'
            }
            if ($transition.FinalSwitchName -ne '*' -and [string]$finalSwitchName -cne $transition.FinalSwitchName) {
                $result = 'ABORTED_UNKNOWN'
                $message = 'Final switch does not match task policy.'
            }
        }

        $receipt = [ordered]@{
            schema = 1
            task = ('\TBound\' + $Task)
            operation = $Task
            invocationId = $invocationId
            startedUtc = $startedUtc.ToString('o', [Globalization.CultureInfo]::InvariantCulture)
            completedUtc = [DateTimeOffset]::UtcNow.ToString('o', [Globalization.CultureInfo]::InvariantCulture)
            result = $result
            vmId = $vmId
            adapterId = $adapterId
            adapterMacAddress = $adapterMacAddress
            initialVmState = $initialVmState
            finalVmState = $finalVmState
            initialSwitchName = $initialSwitchName
            finalSwitchName = $finalSwitchName
            message = $message
            profileSha256 = $script:ProfileSha256
        }
        try { Write-Receipt $receipt }
        catch { Write-Error ('Could not write protected receipt: ' + $_.Exception.Message); exit 2 }
    }
        finally {
            if ($mutexHeld -and $null -ne $mutex) { try { $mutex.ReleaseMutex() } catch {} }
            if ($null -ne $mutex) { $mutex.Dispose() }
        }
    }
    else {
        if ($null -ne $mutex) { $mutex.Dispose() }
    }
}
if ($result -ne 'PASS') { Write-Error ('TBound action ' + $Task + ' returned ' + $result + ': ' + $message); exit 1 }
