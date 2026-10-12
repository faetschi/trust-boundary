[CmdletBinding()]
param(
    [string]$TemporaryParent = [IO.Path]::GetTempPath()
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:Passed = 0
$script:Failed = 0

function Assert-True {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][bool]$Condition
    )

    if ($Condition) {
        $script:Passed++
        Write-Host ('PASS: ' + $Name)
    }
    else {
        $script:Failed++
        Write-Host ('FAIL: ' + $Name)
    }
}

function Assert-False {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][bool]$Condition
    )

    Assert-True -Name $Name -Condition (-not $Condition)
}

function Assert-Throws {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Action
    )

    $threw = $false
    try {
        $null = & $Action
    }
    catch {
        $threw = $true
    }
    Assert-True -Name $Name -Condition $threw
}

function Get-RuleArray {
    param([Parameter(Mandatory = $true)][object]$Acl)

    return @($Acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
}

function Get-DescriptorSddl {
    param([Parameter(Mandatory = $true)][string]$Path)

    $acl = Get-Acl -LiteralPath $Path -ErrorAction Stop
    return $acl.GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::All)
}

if ($PSVersionTable.PSVersion.Major -ne 5 -or $PSVersionTable.PSVersion.Minor -lt 1) {
    throw ('Run this harness with Windows PowerShell 5.1; current runtime is ' + $PSVersionTable.PSVersion)
}

$modulePath = Join-Path $PSScriptRoot 'TBoundHostAccessPolicy.psm1'
if (-not (Test-Path -LiteralPath $modulePath -PathType Leaf)) {
    throw ('Policy module is missing: ' + $modulePath)
}
Import-Module -Name $modulePath -Force -ErrorAction Stop

$policyModuleTokens = $null
$policyModuleParseErrors = $null
$null = [Management.Automation.Language.Parser]::ParseFile(
    $modulePath,
    [ref]$policyModuleTokens,
    [ref]$policyModuleParseErrors
)
Assert-True -Name 'PowerShell 5.1 parser accepts TBoundHostAccessPolicy.psm1' -Condition ($policyModuleParseErrors.Count -eq 0)
if ($policyModuleParseErrors.Count -gt 0) {
    Write-Host ('  Parser details: ' + (($policyModuleParseErrors | ForEach-Object { $_.Message }) -join '; '))
}

$parseTargets = @(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*.ps1' -File -ErrorAction Stop | Sort-Object Name)
foreach ($parseTarget in $parseTargets) {
    $fileName = $parseTarget.Name
    $filePath = $parseTarget.FullName
    $tokens = $null
    $parseErrors = $null
    $null = [Management.Automation.Language.Parser]::ParseFile(
        $filePath,
        [ref]$tokens,
        [ref]$parseErrors
    )
    $parseSummary = if ($parseErrors.Count -eq 0) {
        $true
    }
    else {
        ($parseErrors | ForEach-Object { $_.Message }) -join '; '
    }
    Assert-True -Name ('PowerShell 5.1 parser accepts ' + $fileName) -Condition ($parseErrors.Count -eq 0)
    if ($parseErrors.Count -gt 0) {
        Write-Host ('  Parser details: ' + $parseSummary)
    }
}

$operatorSid = 'S-1-5-21-2350865082-651554413-1548572510-1004'
$operator = [Security.Principal.SecurityIdentifier]::new($operatorSid)
$wrongOperatorSid = 'S-1-5-21-2350865082-651554413-1548572510-1005'

$resolvedOperator = Resolve-TBoundOperatorSid -Value $operatorSid
Assert-True -Name 'Resolve accepts only the pinned operator SID' -Condition ($resolvedOperator.Value -ceq $operatorSid)
foreach ($rejectedSid in @(
    'S-1-5-18',
    'S-1-5-32-544',
    $wrongOperatorSid,
    ($operatorSid + ' '),
    ($operatorSid.ToLowerInvariant())
)) {
    Assert-Throws -Name ('Resolve rejects SID input ' + $rejectedSid) -Action {
        Resolve-TBoundOperatorSid -Value $rejectedSid
    }
}

$taskSddl = Get-TBoundTaskSecurityDescriptor -OperatorSid $operatorSid
$expectedTaskSddl = 'O:SYG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;GRGX;;;' + $operatorSid + ')'
Assert-True -Name 'Task SDDL has exact owner, group, protected DACL, and three ACEs' -Condition ($taskSddl -ceq $expectedTaskSddl)
Assert-True -Name 'Task SDDL readback accepts the exact operator policy' -Condition (Test-TBoundTaskSecurityDescriptor -Sddl $taskSddl -OperatorSid $operatorSid)

$adminTaskSddl = Get-TBoundTaskAdministratorSecurityDescriptor
$expectedAdminTaskSddl = 'O:SYG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)'
Assert-True -Name 'Administrator-only SDDL has exact owner, group, protected DACL, and two ACEs' -Condition ($adminTaskSddl -ceq $expectedAdminTaskSddl)
Assert-True -Name 'Task SDDL readback accepts the administrator-only policy' -Condition (Test-TBoundTaskSecurityDescriptor -Sddl $adminTaskSddl)
Assert-False -Name 'Task SDDL readback rejects a different operator SID' -Condition (Test-TBoundTaskSecurityDescriptor -Sddl $taskSddl -OperatorSid $wrongOperatorSid)
Assert-False -Name 'Task SDDL readback rejects the operator ACE when no operator is expected' -Condition (Test-TBoundTaskSecurityDescriptor -Sddl $taskSddl)
Assert-False -Name 'Task SDDL readback rejects a missing operator ACE' -Condition (Test-TBoundTaskSecurityDescriptor -Sddl $adminTaskSddl -OperatorSid $operatorSid)
Assert-False -Name 'Task SDDL readback rejects an unprotected DACL' -Condition (Test-TBoundTaskSecurityDescriptor -Sddl ($taskSddl.Replace('D:P', 'D:')) -OperatorSid $operatorSid)
Assert-False -Name 'Task SDDL readback rejects an extra broad Users ACE' -Condition (Test-TBoundTaskSecurityDescriptor -Sddl ($taskSddl.Replace('D:P(', 'D:P(A;;GR;;;BU)(')) -OperatorSid $operatorSid)
Assert-False -Name 'Task SDDL readback rejects a widened operator mask' -Condition (Test-TBoundTaskSecurityDescriptor -Sddl ($taskSddl.Replace('GRGX', 'FA')) -OperatorSid $operatorSid)

$policyCases = @(
    @{
        Name = 'Protected data directory without operator access'
        Acl = (New-TBoundProtectedPathSecurity -Directory:$true -OperatorSid $operatorSid -OperatorAccess None)
        Directory = $true
        Access = 'None'
    },
    @{
        Name = 'Protected public directory with operator modify access'
        Acl = (New-TBoundOperatorPublicDirectorySecurity -OperatorSid $operatorSid)
        Directory = $true
        Access = 'Modify'
    },
    @{
        Name = 'Protected file with operator read access'
        Acl = (New-TBoundProtectedPathSecurity -Directory:$false -OperatorSid $operatorSid -OperatorAccess Read)
        Directory = $false
        Access = 'Read'
    }
)
foreach ($case in $policyCases) {
    Assert-True -Name ($case.Name + ' passes exact ACL readback') -Condition (
        Test-TBoundProtectedPathSecurity -Acl $case.Acl -Directory $case.Directory -OperatorSid $operatorSid -OperatorAccess $case.Access
    )
    $rules = @(Get-RuleArray -Acl $case.Acl)
    $hasBroadUsers = @($rules | Where-Object {
        $_.IdentityReference.Value -in @('S-1-5-32-545', 'S-1-1-0', 'S-1-5-11')
    }).Count -gt 0
    $hasInherited = @($rules | Where-Object { $_.IsInherited }).Count -gt 0
    Assert-False -Name ($case.Name + ' contains no broad Users ACE or inherited ACE') -Condition ($hasBroadUsers -or $hasInherited)
}
Assert-Throws -Name 'File policy rejects operator modify access' -Action {
    New-TBoundProtectedPathSecurity -Directory:$false -OperatorSid $operatorSid -OperatorAccess Modify
}
Assert-False -Name 'Protected path readback rejects a different operator SID' -Condition (
    Test-TBoundProtectedPathSecurity -Acl $policyCases[0].Acl -Directory:$true -OperatorSid $wrongOperatorSid -OperatorAccess None
)

$mutatedPathAcl = New-TBoundProtectedPathSecurity -Directory:$true -OperatorSid $operatorSid -OperatorAccess Modify
$usersSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-545')
$usersReadRule = [Security.AccessControl.FileSystemAccessRule]::new(
    $usersSid,
    [Security.AccessControl.FileSystemRights]::ReadAndExecute,
    [Security.AccessControl.InheritanceFlags]::None,
    [Security.AccessControl.PropagationFlags]::None,
    [Security.AccessControl.AccessControlType]::Allow
)
$null = $mutatedPathAcl.AddAccessRule($usersReadRule)
Assert-False -Name 'Protected path readback rejects an added Users ACE' -Condition (
    Test-TBoundProtectedPathSecurity -Acl $mutatedPathAcl -Directory:$true -OperatorSid $operatorSid -OperatorAccess Modify
)

$keyDirectoryAcl = New-TBoundOperatorKeyDirectorySecurity -OperatorSid $operatorSid
Assert-True -Name 'Operator key directory policy passes exact ACL readback' -Condition (
    Test-TBoundOperatorKeyDirectorySecurity -Acl $keyDirectoryAcl -OperatorSid $operatorSid
)
$keyDirectoryRules = @(Get-RuleArray -Acl $keyDirectoryAcl)
Assert-True -Name 'Operator key directory grants one explicit operator FullControl ACE' -Condition (
    $keyDirectoryAcl.AreAccessRulesProtected -and
    $keyDirectoryAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value -ceq 'S-1-5-32-544' -and
    $keyDirectoryRules.Count -eq 1 -and
    $keyDirectoryRules[0].IdentityReference.Value -ceq $operatorSid -and
    $keyDirectoryRules[0].FileSystemRights -eq [Security.AccessControl.FileSystemRights]::FullControl -and
    $keyDirectoryRules[0].InheritanceFlags -eq ([Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit) -and
    -not $keyDirectoryRules[0].IsInherited
)
$mutatedKeyDirectoryAcl = New-TBoundOperatorKeyDirectorySecurity -OperatorSid $operatorSid
$null = $mutatedKeyDirectoryAcl.AddAccessRule($usersReadRule)
Assert-False -Name 'Operator key directory readback rejects an added Users ACE' -Condition (
    Test-TBoundOperatorKeyDirectorySecurity -Acl $mutatedKeyDirectoryAcl -OperatorSid $operatorSid
)

$privateKeyAcl = New-TBoundOperatorPrivateKeySecurity -OperatorSid $operatorSid
Assert-True -Name 'Operator private key policy passes exact ACL readback' -Condition (
    Test-TBoundOperatorPrivateKeySecurity -Acl $privateKeyAcl -OperatorSid $operatorSid
)
$privateKeyRules = @(Get-RuleArray -Acl $privateKeyAcl)
Assert-True -Name 'Operator private key grants one non-inherited operator FullControl ACE' -Condition (
    $privateKeyAcl.AreAccessRulesProtected -and
    $privateKeyAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value -ceq $operatorSid -and
    $privateKeyRules.Count -eq 1 -and
    $privateKeyRules[0].IdentityReference.Value -ceq $operatorSid -and
    $privateKeyRules[0].FileSystemRights -eq [Security.AccessControl.FileSystemRights]::FullControl -and
    $privateKeyRules[0].InheritanceFlags -eq [Security.AccessControl.InheritanceFlags]::None -and
    -not $privateKeyRules[0].IsInherited
)
$mutatedPrivateKeyAcl = New-TBoundOperatorPrivateKeySecurity -OperatorSid $operatorSid
$null = $mutatedPrivateKeyAcl.AddAccessRule($usersReadRule)
Assert-False -Name 'Operator private key readback rejects an added Users ACE' -Condition (
    Test-TBoundOperatorPrivateKeySecurity -Acl $mutatedPrivateKeyAcl -OperatorSid $operatorSid
)

$tempParentPath = [IO.Path]::GetFullPath($TemporaryParent)
if (-not (Test-Path -LiteralPath $tempParentPath -PathType Container)) {
    throw ('Temporary parent directory does not exist: ' + $tempParentPath)
}
$tempPath = Join-Path $tempParentPath ('TBoundHostAccessPolicy-' + [Guid]::NewGuid().ToString('N'))
if (Test-Path -LiteralPath $tempPath) {
    throw ('Refusing to reuse an existing temporary test directory: ' + $tempPath)
}

try {
    $null = New-Item -ItemType Directory -Path $tempPath -ErrorAction Stop

    $currentUserSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $currentUserAcl = [Security.AccessControl.DirectorySecurity]::new()
    $currentUserAcl.SetAccessRuleProtection($true, $false)
    $currentUserAcl.SetOwner($currentUserSid)
    $currentUserRule = [Security.AccessControl.FileSystemAccessRule]::new(
        $currentUserSid,
        [Security.AccessControl.FileSystemRights]::FullControl,
        ([Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit),
        [Security.AccessControl.PropagationFlags]::None,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $null = $currentUserAcl.AddAccessRule($currentUserRule)

    $createdPath = Join-Path $tempPath 'native-created'
    $createdDirectory = New-TBoundDirectoryWithSecurity -Path $createdPath -Acl $currentUserAcl
    $createdAcl = Get-Acl -LiteralPath $createdPath -ErrorAction Stop
    $createdRules = @(Get-RuleArray -Acl $createdAcl)
    Assert-True -Name 'Native CreateDirectoryW creates a new directory with supplied ACL' -Condition (
        (Test-Path -LiteralPath $createdPath -PathType Container) -and
        $createdDirectory -ceq [IO.Path]::GetFullPath($createdPath)
    )
    Assert-True -Name 'Native created directory has protected DACL and current-user owner' -Condition (
        $createdAcl.AreAccessRulesProtected -and
        $createdAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value -ceq $currentUserSid.Value
    )
    Assert-True -Name 'Native created directory has exactly one explicit current-user FullControl ACE' -Condition (
        $createdRules.Count -eq 1 -and
        $createdRules[0].IdentityReference.Value -ceq $currentUserSid.Value -and
        $createdRules[0].FileSystemRights -eq [Security.AccessControl.FileSystemRights]::FullControl -and
        $createdRules[0].InheritanceFlags -eq ([Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit) -and
        $createdRules[0].PropagationFlags -eq [Security.AccessControl.PropagationFlags]::None -and
        -not $createdRules[0].IsInherited
    )
    $createdBroadUsersRules = @($createdRules | Where-Object {
        $_.IdentityReference.Value -in @('S-1-5-32-545', 'S-1-1-0', 'S-1-5-11')
    })
    $createdInheritedRules = @($createdRules | Where-Object { $_.IsInherited })
    Assert-True -Name 'Native created directory has no inherited or broad Users permissions' -Condition (
        $createdBroadUsersRules.Count -eq 0 -and $createdInheritedRules.Count -eq 0
    )

    $existingDirectoryPath = Join-Path $tempPath 'existing-directory'
    $null = New-Item -ItemType Directory -Path $existingDirectoryPath -ErrorAction Stop
    $existingDirectoryMarker = Join-Path $existingDirectoryPath 'marker.txt'
    Set-Content -LiteralPath $existingDirectoryMarker -Value 'preserve-directory' -Encoding ASCII
    $existingDirectorySddlBefore = Get-DescriptorSddl -Path $existingDirectoryPath
    Assert-Throws -Name 'Native creator rejects an existing directory' -Action {
        New-TBoundDirectoryWithSecurity -Path $existingDirectoryPath -Acl $currentUserAcl
    }
    $existingDirectorySddlAfter = Get-DescriptorSddl -Path $existingDirectoryPath
    Assert-True -Name 'Existing directory ACL and contents remain untouched' -Condition (
        $existingDirectorySddlBefore -ceq $existingDirectorySddlAfter -and
        (Get-Content -LiteralPath $existingDirectoryMarker -Raw) -ceq 'preserve-directory' + [Environment]::NewLine
    )

    $existingFilePath = Join-Path $tempPath 'existing-file'
    Set-Content -LiteralPath $existingFilePath -Value 'preserve-file' -Encoding ASCII
    $existingFileSddlBefore = Get-DescriptorSddl -Path $existingFilePath
    Assert-Throws -Name 'Native creator rejects an existing file' -Action {
        New-TBoundDirectoryWithSecurity -Path $existingFilePath -Acl $currentUserAcl
    }
    $existingFileSddlAfter = Get-DescriptorSddl -Path $existingFilePath
    Assert-True -Name 'Existing file ACL and contents remain untouched' -Condition (
        $existingFileSddlBefore -ceq $existingFileSddlAfter -and
        (Get-Content -LiteralPath $existingFilePath -Raw) -ceq 'preserve-file' + [Environment]::NewLine
    )
}
finally {
    if (Test-Path -LiteralPath $tempPath -PathType Container) {
        Remove-Item -LiteralPath $tempPath -Recurse -Force -ErrorAction Stop
    }
}

Write-Host ''
Write-Host ('Passed: ' + $script:Passed + '; Failed: ' + $script:Failed)
if ($script:Failed -gt 0) {
    exit 1
}
exit 0
