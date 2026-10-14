#requires -Version 5.1
<#
.SYNOPSIS
  Runs the fixed ABI-8 seed helper through a source-checked UAC child.
.DESCRIPTION
  Accepted operations are a read-only WhatIf, creation of the one predeclared
  disposable seed, or continuation using the fixed already-pinned ISO and task
  key. No paths, VM names, switches, or commands are accepted from the caller.
  Candidate and bootstrap bytes and the installed Hyper-V module are pinned
  before elevation and rechecked before a ScriptBlock is created.
  Create and Resume are bounded by the seed helper's 120-minute installer limit
  plus ten minutes of setup slack. A timeout is recorded without deleting or
  powering off a partially created VM.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateSet('WhatIf', 'Create', 'Resume')]
    [string]$Action
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($args.Count -ne 0) { throw 'This launcher accepts only -Action WhatIf or -Action Create.' }

function Get-Sha256Bytes {
    param([Parameter(Mandatory)][byte[]]$Bytes)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '').ToUpperInvariant() }
    finally { $sha.Dispose() }
}

function ConvertTo-Base64Utf8 {
    param([Parameter(Mandatory)][string]$Value)
    return [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($Value))
}

function Write-NewUtf8File {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Text)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try {
        $writer = [IO.StreamWriter]::new($stream, [Text.UTF8Encoding]::new($false))
        try { $writer.Write($Text) }
        finally { $writer.Dispose() }
    }
    finally { $stream.Dispose() }
}

function Write-NewBytesFile {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][byte[]]$Bytes)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $stream.Write($Bytes, 0, $Bytes.Length); $stream.Flush($true) }
    finally { $stream.Dispose() }
}

function Assert-NoReparsePathComponents {
    param([Parameter(Mandatory)][string]$Path)
    $fullPath = [IO.Path]::GetFullPath($Path)
    $pathRoot = [IO.Path]::GetPathRoot($fullPath)
    $currentPath = $pathRoot
    foreach ($component in ($fullPath.Substring($pathRoot.Length) -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($component)) { continue }
        $currentPath = Join-Path $currentPath $component
        if (-not (Test-Path -LiteralPath $currentPath)) { break }
        $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Path contains a junction or symbolic link: $currentPath"
        }
    }
}

function Read-LockedBytes {
    param([Parameter(Mandatory)][IO.FileStream]$Stream)
    $Stream.Position = 0
    $memory = [IO.MemoryStream]::new()
    try { $Stream.CopyTo($memory); return ,($memory.ToArray()) }
    finally { $memory.Dispose() }
}

function ConvertTo-CanonicalUtf8LfBytes {
    param([Parameter(Mandatory)][byte[]]$Bytes)
    if ($Bytes.Length -ge 3 -and $Bytes[0] -eq 0xEF -and $Bytes[1] -eq 0xBB -and $Bytes[2] -eq 0xBF) {
        if ($Bytes.Length -eq 3) { throw 'Pinned source contains only a UTF-8 BOM.' }
        $Bytes = $Bytes[3..($Bytes.Length - 1)]
    }
    $strictUtf8 = [Text.UTF8Encoding]::new($false, $true)
    try { $text = $strictUtf8.GetString($Bytes) }
    catch { throw 'Pinned candidate source is not strict UTF-8; refusing elevation.' }
    $text = $text.Replace("`r`n", "`n")
    if ($text.Contains("`r")) { throw 'Pinned candidate source contains a bare CR line ending; refusing normalization.' }
    return ,$strictUtf8.GetBytes($text)
}

function Assert-TrustedSystemTool {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedSha256,
        [Parameter(Mandatory)][string[]]$TrustedDirectories
    )
    foreach ($directory in $TrustedDirectories) {
        $directoryItem = Get-Item -LiteralPath $directory -Force -ErrorAction Stop
        if (($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
            (Get-Acl -LiteralPath $directory -ErrorAction Stop).Owner -cne 'NT SERVICE\TrustedInstaller') {
            throw "Trusted system directory is not directly owned by NT SERVICE\TrustedInstaller: $directory"
        }
    }
    $fullPath = [IO.Path]::GetFullPath($Path)
    $item = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
        (Get-Acl -LiteralPath $fullPath -ErrorAction Stop).Owner -cne 'NT SERVICE\TrustedInstaller') {
        throw "Trusted system executable is not a directly owned regular file: $fullPath"
    }
    if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($fullPath))).ToLowerInvariant() -cne $ExpectedSha256) {
        throw "Trusted system executable hash mismatch: $fullPath"
    }
    $signature = Get-AuthenticodeSignature -FilePath $fullPath -ErrorAction Stop
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid -or
        $signature.SignerCertificate.Subject -notmatch '^CN=Microsoft Windows, O=Microsoft Corporation,') {
        throw "Trusted system executable lacks a valid Microsoft Windows signature: $fullPath"
    }
}

$expectedCandidateSha256 = '668494F0CF495C14926234E43659738A4F605AB6C635A3C0B22E284057C38A93'
$expectedHyperVManifestSha256 = 'C5F845C5BA7A4960D876DB46D1E63905CD9AC8EFEF650CB3EC9DBBD8B713F4CC'
$expectedHyperVBinarySha256 = '0FD7A8B2CB8C15E9DA691726C1F1F71F3EC4F853837FD099BA3F9EB2E9399316'
$expectedWslExeSha256 = '4e589e3883229b7a74a4acdb878689dcec94e2539fcad1c194f415b149c337a9'
$expectedSshKeygenSha256 = '539ae392c61a53fadddf0bbb152126f3e0bc042e8a4ed50718ab16fbf872cbcc'
$candidatePath = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'new-abi8-hyperv-candidate.ps1'))
Assert-NoReparsePathComponents -Path $candidatePath
$candidateItem = Get-Item -LiteralPath $candidatePath -Force -ErrorAction Stop
if (($candidateItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Pinned candidate source is a reparse point.' }
$candidateSourceBytes = [IO.File]::ReadAllBytes($candidatePath)
$actualCandidateSourceSha256 = Get-Sha256Bytes $candidateSourceBytes
$candidateBytes = ConvertTo-CanonicalUtf8LfBytes -Bytes $candidateSourceBytes
$actualCandidateSha256 = Get-Sha256Bytes $candidateBytes
if ($expectedCandidateSha256 -notmatch '^[0-9A-F]{64}$' -or $actualCandidateSha256 -cne $expectedCandidateSha256) {
    throw 'Canonical UTF-8/LF candidate source does not match the reviewed literal SHA-256 pin; refusing elevation.'
}

$windowsDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
$systemDirectory = Join-Path $windowsDirectory 'System32'
$wslExePath = Join-Path $systemDirectory 'wsl.exe'
$sshKeygenPath = Join-Path $systemDirectory 'OpenSSH\ssh-keygen.exe'
$canonicalPowerShellPath = Join-Path $systemDirectory 'WindowsPowerShell\v1.0\powershell.exe'
$launchPowerShellPath = $canonicalPowerShellPath
if ([Environment]::Is64BitOperatingSystem -and -not [Environment]::Is64BitProcess) {
    $launchPowerShellPath = Join-Path $windowsDirectory 'Sysnative\WindowsPowerShell\v1.0\powershell.exe'
}
$hyperVModuleDirectory = Join-Path $systemDirectory 'WindowsPowerShell\v1.0\Modules\Hyper-V\2.0.0.0'
$systemModuleDirectory = Join-Path $systemDirectory 'WindowsPowerShell\v1.0\Modules'
$hyperVManifestPath = Join-Path $hyperVModuleDirectory 'Hyper-V.psd1'
$hyperVBinaryPath = Join-Path $windowsDirectory 'Microsoft.Net\assembly\GAC_MSIL\Microsoft.HyperV.PowerShell.Cmdlets\v4.0_10.0.0.0__31bf3856ad364e35\Microsoft.HyperV.PowerShell.Cmdlets.dll'
foreach ($trustedPath in @($canonicalPowerShellPath, $hyperVManifestPath, $hyperVBinaryPath)) {
    if (-not [IO.File]::Exists($trustedPath)) { throw "Pinned trusted system file is missing: $trustedPath" }
    if (((Get-Item -LiteralPath $trustedPath -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Pinned trusted system file is a reparse point: $trustedPath"
    }
}
if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($hyperVManifestPath))) -cne $expectedHyperVManifestSha256) { throw 'Installed Hyper-V manifest differs from its reviewed system pin.' }
if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($hyperVBinaryPath))) -cne $expectedHyperVBinarySha256) { throw 'Installed Hyper-V binary differs from its reviewed system pin.' }
Assert-TrustedSystemTool -Path $wslExePath -ExpectedSha256 $expectedWslExeSha256 -TrustedDirectories @($windowsDirectory, $systemDirectory)
Assert-TrustedSystemTool -Path $sshKeygenPath -ExpectedSha256 $expectedSshKeygenSha256 -TrustedDirectories @($windowsDirectory, $systemDirectory, (Join-Path $systemDirectory 'OpenSSH'))

$desktop = [IO.Path]::GetFullPath([Environment]::GetFolderPath([Environment+SpecialFolder]::DesktopDirectory)).TrimEnd('\')
Assert-NoReparsePathComponents -Path $desktop
$runId = 'tbound-abi8-seed-{0}-{1}' -f $Action.ToLowerInvariant(), [guid]::NewGuid().ToString('N').Substring(0, 12)
$runDirectory = Join-Path $desktop $runId
if ([IO.Directory]::Exists($runDirectory) -or [IO.File]::Exists($runDirectory)) { throw 'Generated per-run directory already exists; refusing reuse.' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$runAcl = New-Object Security.AccessControl.DirectorySecurity
$runAcl.SetOwner($identity.User)
$runAcl.SetAccessRuleProtection($true, $false)
$inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
$rule = New-Object Security.AccessControl.FileSystemAccessRule(
    $identity.User,
    [Security.AccessControl.FileSystemRights]::FullControl,
    $inherit,
    [Security.AccessControl.PropagationFlags]::None,
    [Security.AccessControl.AccessControlType]::Allow
)
$null = $runAcl.AddAccessRule($rule)
$null = [IO.Directory]::CreateDirectory($runDirectory, $runAcl)
Assert-NoReparsePathComponents -Path $runDirectory
if (((Get-Item -LiteralPath $runDirectory -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Created per-run directory is a reparse point.' }
$actualRunAcl = Get-Acl -LiteralPath $runDirectory -ErrorAction Stop
if (-not $actualRunAcl.AreAccessRulesProtected -or $actualRunAcl.Owner -cne $identity.Name) { throw 'Created per-run directory ACL is not protected and owned by the current user.' }

$candidateSnapshotPath = Join-Path $runDirectory 'candidate.snapshot.ps1'
$bootstrapSnapshotPath = Join-Path $runDirectory 'elevated-bootstrap.snapshot.ps1'
$childResultPath = Join-Path $runDirectory 'child-result.json'
$childErrorPath = Join-Path $runDirectory 'child-error.txt'
$launchResultPath = Join-Path $runDirectory 'launch-result.json'
Write-NewBytesFile -Path $candidateSnapshotPath -Bytes $candidateBytes
$candidateLock = [IO.File]::Open($candidateSnapshotPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
if ((Get-Sha256Bytes (Read-LockedBytes $candidateLock)) -cne $expectedCandidateSha256) { throw 'Locked candidate snapshot differs from the source pin.' }

$bootstrap = @'
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
function From-B64([string]$Value) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Value)) }
function Hash-Bytes([byte[]]$Bytes) {
  $sha = [Security.Cryptography.SHA256]::Create()
  try { ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '').ToUpperInvariant() }
  finally { $sha.Dispose() }
}
function Write-New([string]$Path, [string]$Text) {
  $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
  try {
    $writer = [IO.StreamWriter]::new($stream, [Text.UTF8Encoding]::new($false))
    try { $writer.Write($Text) } finally { $writer.Dispose() }
  } finally { $stream.Dispose() }
}
function Decode-Script([byte[]]$Bytes) {
  if ($Bytes.Length -ge 3 -and $Bytes[0] -eq 0xEF -and $Bytes[1] -eq 0xBB -and $Bytes[2] -eq 0xBF) { $Bytes = $Bytes[3..($Bytes.Length - 1)] }
  [Text.Encoding]::UTF8.GetString($Bytes)
}
function Assert-NoReparsePath([string]$Path) {
  $fullPath = [IO.Path]::GetFullPath($Path)
  $pathRoot = [IO.Path]::GetPathRoot($fullPath)
  $currentPath = $pathRoot
  foreach ($component in ($fullPath.Substring($pathRoot.Length) -split '[\\/]')) {
    if ([string]::IsNullOrWhiteSpace($component)) { continue }
    $currentPath = Join-Path $currentPath $component
    if (-not (Test-Path -LiteralPath $currentPath)) { break }
    $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Run path contains a reparse component: $currentPath" }
  }
}
function Assert-TrustedHostTool([string]$Path, [string]$ExpectedHash) {
  $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
  if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
      (Get-Acl -LiteralPath $Path -ErrorAction Stop).Owner -cne 'NT SERVICE\TrustedInstaller' -or
      (Hash-Bytes ([IO.File]::ReadAllBytes($Path))).ToLowerInvariant() -cne $ExpectedHash) {
    throw "Trusted host executable failed its owner, path, or byte pin: $Path"
  }
  $signature = Get-AuthenticodeSignature -FilePath $Path -ErrorAction Stop
  if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid -or
      $signature.SignerCertificate.Subject -notmatch '^CN=Microsoft Windows, O=Microsoft Corporation,') {
    throw "Trusted host executable lacks a valid Microsoft Windows signature: $Path"
  }
}
$runId = From-B64 '__RUN_ID__'
$runDirectory = From-B64 '__RUN_DIR__'
$candidatePath = From-B64 '__CANDIDATE_PATH__'
$expectedCandidateHash = From-B64 '__CANDIDATE_HASH__'
$action = From-B64 '__ACTION__'
$canonicalPowerShellPath = From-B64 '__POWERSHELL_PATH__'
$wslExePath = From-B64 '__WSL_EXE_PATH__'
$expectedWslExeHash = From-B64 '__WSL_EXE_HASH__'
$sshKeygenPath = From-B64 '__SSH_KEYGEN_PATH__'
$expectedSshKeygenHash = From-B64 '__SSH_KEYGEN_HASH__'
$hyperVModuleDirectory = From-B64 '__HYPERV_MODULE_DIRECTORY__'
$systemModuleDirectory = From-B64 '__SYSTEM_MODULE_DIRECTORY__'
$hyperVManifestPath = From-B64 '__HYPERV_MANIFEST_PATH__'
$expectedHyperVManifestHash = From-B64 '__HYPERV_MANIFEST_HASH__'
$hyperVBinaryPath = From-B64 '__HYPERV_BINARY_PATH__'
$expectedHyperVBinaryHash = From-B64 '__HYPERV_BINARY_HASH__'
$childResultPath = Join-Path $runDirectory 'child-result.json'
$childErrorPath = Join-Path $runDirectory 'child-error.txt'
$candidateOutputPath = Join-Path $runDirectory 'candidate-output.txt'
$exitCode = 2
$result = [ordered]@{
  schema = 'tbound-abi8-elevated-candidate-result/v1'
  runId = $runId
  action = $action
  startedAt = [DateTimeOffset]::Now.ToString('o')
  elevatedTokenVerified = $false
  canonicalPowerShellVerified = $false
  moduleVerified = $false
  hostExecutablesVerified = $false
  wslExecutableSha256 = $null
  sshKeygenExecutableSha256 = $null
  candidateBytesVerifiedBeforeScriptBlockCreation = $false
  candidateSha256 = $null
  invocationCompleted = $false
  invocationOutputLineCount = 0
  outputSha256 = $null
  outcome = 'refused-or-failed'
  candidateReceiptSha256 = $null
  exitCode = 2
}
try {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = [Security.Principal.WindowsPrincipal]::new($identity)
  $elevated = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  if (-not $elevated) { throw 'Elevated child token validation failed.' }
  $result.elevatedTokenVerified = $true
  $observedPowerShellPath = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
  if ($observedPowerShellPath -ine $canonicalPowerShellPath) { throw 'Child is not running from canonical System32 PowerShell.' }
  $result.canonicalPowerShellVerified = $true
  Assert-NoReparsePath -Path $runDirectory
  $desktopDirectory = [IO.Path]::GetFullPath([Environment]::GetFolderPath([Environment+SpecialFolder]::DesktopDirectory)).TrimEnd('\\')
  if ([IO.Path]::GetFileName($runDirectory) -cne $runId -or
      [IO.Path]::GetFullPath([IO.Path]::GetDirectoryName($runDirectory)).TrimEnd('\\') -ine $desktopDirectory) {
    throw 'Per-run directory is outside the fixed Desktop receipt area or differs from its run id.'
  }
  $runAcl = Get-Acl -LiteralPath $runDirectory -ErrorAction Stop
  if (-not $runAcl.AreAccessRulesProtected -or $runAcl.Owner -cne $identity.Name) { throw 'Per-run directory ACL is not protected and owned by the launching user.' }
  $windowsDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
  $systemDirectory = Join-Path $windowsDirectory 'System32'
  if ($wslExePath -ine (Join-Path $systemDirectory 'wsl.exe') -or
      $sshKeygenPath -ine (Join-Path $systemDirectory 'OpenSSH\ssh-keygen.exe')) {
    throw 'Pinned host executable path differs from the canonical System32 locations.'
  }
  foreach ($directory in @($windowsDirectory, $systemDirectory, (Join-Path $systemDirectory 'OpenSSH'))) {
    if (-not [IO.Directory]::Exists($directory) -or
        ((Get-Item -LiteralPath $directory -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
        (Get-Acl -LiteralPath $directory).Owner -cne 'NT SERVICE\TrustedInstaller') {
      throw "Trusted system directory is missing, reparse-backed, or not owned by TrustedInstaller: $directory"
    }
  }
  Assert-TrustedHostTool -Path $wslExePath -ExpectedHash $expectedWslExeHash
  Assert-TrustedHostTool -Path $sshKeygenPath -ExpectedHash $expectedSshKeygenHash
  $env:PATH = $systemDirectory + ';' + (Join-Path $systemDirectory 'OpenSSH') + ';' + $windowsDirectory
  if ([IO.Path]::GetFileName($candidatePath) -cne 'candidate.snapshot.ps1' -or
      [IO.Path]::GetFullPath([IO.Path]::GetDirectoryName($candidatePath)) -ine [IO.Path]::GetFullPath($runDirectory)) {
    throw 'Candidate snapshot escaped its fixed per-run directory.'
  }
  Assert-NoReparsePath -Path $candidatePath
  $candidateBytes = [IO.File]::ReadAllBytes($candidatePath)
  $actualCandidateHash = Hash-Bytes $candidateBytes
  if ($actualCandidateHash -cne $expectedCandidateHash) { throw 'Elevated candidate snapshot hash mismatch.' }
  $result.candidateSha256 = $actualCandidateHash
  $result.candidateBytesVerifiedBeforeScriptBlockCreation = $true

  $env:PSModulePath = $hyperVModuleDirectory + ';' + $systemModuleDirectory
  if ((Hash-Bytes ([IO.File]::ReadAllBytes($hyperVManifestPath))) -cne $expectedHyperVManifestHash) { throw 'Pinned Hyper-V manifest hash mismatch.' }
  if ((Hash-Bytes ([IO.File]::ReadAllBytes($hyperVBinaryPath))) -cne $expectedHyperVBinaryHash) { throw 'Pinned Hyper-V binary hash mismatch.' }
  Import-Module -Name $hyperVManifestPath -Force -ErrorAction Stop | Out-Null
  $loadedHyperV = @(Get-Module -Name Hyper-V)
  if ($loadedHyperV.Count -ne 1 -or $loadedHyperV[0].Version.ToString() -cne '2.0.0.0' -or $loadedHyperV[0].Path -ine $hyperVBinaryPath) { throw 'Loaded Hyper-V module differs from the pinned module.' }
  $result.moduleVerified = $true

  Assert-TrustedHostTool -Path $wslExePath -ExpectedHash $expectedWslExeHash
  Assert-TrustedHostTool -Path $sshKeygenPath -ExpectedHash $expectedSshKeygenHash
  $result.wslExecutableSha256 = $expectedWslExeHash
  $result.sshKeygenExecutableSha256 = $expectedSshKeygenHash
  $result.hostExecutablesVerified = $true
  $candidateBlock = [ScriptBlock]::Create((Decode-Script $candidateBytes))
  $assetRoot = 'F:\TBoundAssets\abi8-qualification-2026-10-14'
  $captured = @()
  if ($action -ceq 'WhatIf') {
    $captured = @(& $candidateBlock -AssetRoot $assetRoot -InstallTimeoutMinutes 120 -MinimumFreeGb 120 -WhatIf *>&1)
  }
  elseif ($action -ceq 'Create') {
    $captured = @(& $candidateBlock -AssetRoot $assetRoot -InstallTimeoutMinutes 120 -MinimumFreeGb 120 -Confirm:$false *>&1)
  }
  elseif ($action -ceq 'Resume') {
    $captured = @(& $candidateBlock -AssetRoot $assetRoot -InstallTimeoutMinutes 120 -MinimumFreeGb 120 -ResumeVerifiedMedia -Confirm:$false *>&1)
  }
  else { throw 'Action is not one of the fixed reviewed operations.' }
  $capturedText = @($captured | ForEach-Object { [string]$_ })
  $capturedUtf8 = [Text.Encoding]::UTF8.GetBytes(($capturedText -join [Environment]::NewLine) + [Environment]::NewLine)
  $result.invocationOutputLineCount = $capturedText.Count
  $result.outputSha256 = Hash-Bytes $capturedUtf8
  $result.invocationCompleted = $true
  [IO.File]::WriteAllBytes($candidateOutputPath, $capturedUtf8)
  if ($action -ceq 'WhatIf') {
    if ([IO.Directory]::Exists($assetRoot) -or (Get-VM -Name 'TBound-Ubuntu-ABI8-Seed' -ErrorAction SilentlyContinue) -or
        (Get-VM -Name 'TBound-Ubuntu-2404-ABI8' -ErrorAction SilentlyContinue)) {
      throw 'WhatIf unexpectedly created a candidate output directory or VM.'
    }
    $result.outcome = 'whatif-preflight-completed-no-vm-or-output-created'
    $exitCode = 0
  }
  else {
    $receiptPath = Join-Path $assetRoot 'abi8-seed-receipt.json'
    if (-not [IO.File]::Exists($receiptPath)) { throw 'Create completed without the fixed seed receipt.' }
    $receipt = Get-Content -LiteralPath $receiptPath -Raw | ConvertFrom-Json -ErrorAction Stop
    if ($receipt.schema -cne 'tbound-abi8-hyperv-seed/v1' -or
        $receipt.sourceMedia.sha256 -cne '97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8' -or
        $receipt.derivedMedia.canonicalSignatureCoversDerivedMedia -ne $false -or
        $receipt.derivedMedia.xorrisoVersion -notmatch '^xorriso 1\.5\.6' -or
        ($action -ceq 'Resume' -and ($receipt.derivedMedia.sha256 -cne '1ec771177b5c8e347ea6829f40744cc9505a79d6faf79b3f7a3eda74b2f84e76' -or $receipt.derivedMedia.reusedExistingMedia -ne $true)) -or
        ($action -ceq 'Create' -and $receipt.derivedMedia.reusedExistingMedia -ne $false) -or
        $receipt.provisioningKey.privateKeyPath -ine (Join-Path $env:LOCALAPPDATA 'TBound\abi8-qualification\id_ed25519') -or
        $receipt.provisioningKey.privateKeyMaterialIncluded -ne $false -or
        $receipt.seedVm.name -cne 'TBound-Ubuntu-ABI8-Seed' -or
        $receipt.seedVm.state -cne 'Off' -or $receipt.seedVm.generation -ne 2 -or
        $receipt.seedVm.processorCount -ne 2 -or $receipt.seedVm.fixedMemoryBytes -ne 4GB -or
        $receipt.seedVm.virtualDiskBytes -ne 40GB -or $receipt.seedVm.diskType -cne 'Dynamic' -or
        $receipt.seedVm.diskSha256 -notmatch '^[0-9a-f]{64}$' -or
        $receipt.seedVm.switchName -cne 'Default Switch' -or
        $receipt.seedVm.secureBoot -cne 'On' -or
        $receipt.seedVm.secureBootTemplate -notmatch 'MicrosoftUEFICertificateAuthority' -or
        $receipt.seedVm.parentPath) { throw 'Seed receipt did not match the fixed media, key-path, or VM profile.' }
    $actualDiskHash = (Get-FileHash -LiteralPath $receipt.seedVm.diskPath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
    if ($actualDiskHash -cne $receipt.seedVm.diskSha256) { throw 'Seed disk hash differs from its receipt.' }
    $seedVm = Get-VM -Name 'TBound-Ubuntu-ABI8-Seed' -ErrorAction Stop
    $seedCpu = Get-VMProcessor -VMName $seedVm.Name -ErrorAction Stop
    $seedMemory = Get-VMMemory -VMName $seedVm.Name -ErrorAction Stop
    $seedFirmware = Get-VMFirmware -VMName $seedVm.Name -ErrorAction Stop
    $seedAdapters = @(Get-VMNetworkAdapter -VMName $seedVm.Name -ErrorAction Stop)
    $seedDisks = @(Get-VMHardDiskDrive -VMName $seedVm.Name -ErrorAction Stop)
    $seedSnapshots = @(Get-VMSnapshot -VMName $seedVm.Name -ErrorAction Stop)
    $seedDvds = @(Get-VMDvdDrive -VMName $seedVm.Name -ErrorAction Stop | Where-Object { $_.Path })
    if ($seedVm.State -ne 'Off' -or $seedVm.Generation -ne 2 -or $seedCpu.Count -ne 2 -or
        $seedMemory.DynamicMemoryEnabled -or $seedMemory.Startup -ne 4GB -or
        $seedFirmware.SecureBoot -ne 'On' -or $seedFirmware.SecureBootTemplate -notmatch 'MicrosoftUEFICertificateAuthority' -or
        $seedAdapters.Count -ne 1 -or $seedAdapters[0].SwitchName -cne 'Default Switch' -or
        $seedDisks.Count -ne 1 -or $seedDisks[0].Path -ine $receipt.seedVm.diskPath -or
        $seedSnapshots.Count -ne 0 -or $seedDvds.Count -ne 0) { throw 'Live seed VM settings differ from the fixed off-state receipt.' }
    $result.candidateReceiptSha256 = (Hash-Bytes ([IO.File]::ReadAllBytes($receiptPath))).ToLowerInvariant()
    $result.outcome = 'seed-created-and-off-state-receipt-validated'
    $exitCode = 0
  }
}
catch {
  $failureText = $_.Exception.ToString()
  try { if (-not [IO.File]::Exists($childErrorPath)) { Write-New $childErrorPath ($failureText + [Environment]::NewLine) } } catch { }
  [Console]::Error.WriteLine($failureText)
  $result.outcome = 'refused-or-failed'
  $exitCode = 2
}
$result.finishedAt = [DateTimeOffset]::Now.ToString('o')
$result.exitCode = $exitCode
try { Assert-NoReparsePath -Path $runDirectory; Write-New $childResultPath (($result | ConvertTo-Json -Depth 8) + [Environment]::NewLine) } catch { [Console]::Error.WriteLine('Could not write the child result receipt.'); $exitCode = 2 }
exit $exitCode
'@
$bootstrapValues = @{
    '__RUN_ID__' = ConvertTo-Base64Utf8 $runId
    '__RUN_DIR__' = ConvertTo-Base64Utf8 $runDirectory
    '__CANDIDATE_PATH__' = ConvertTo-Base64Utf8 $candidateSnapshotPath
    '__CANDIDATE_HASH__' = ConvertTo-Base64Utf8 $expectedCandidateSha256
    '__ACTION__' = ConvertTo-Base64Utf8 $Action
    '__POWERSHELL_PATH__' = ConvertTo-Base64Utf8 $canonicalPowerShellPath
    '__WSL_EXE_PATH__' = ConvertTo-Base64Utf8 $wslExePath
    '__WSL_EXE_HASH__' = ConvertTo-Base64Utf8 $expectedWslExeSha256
    '__SSH_KEYGEN_PATH__' = ConvertTo-Base64Utf8 $sshKeygenPath
    '__SSH_KEYGEN_HASH__' = ConvertTo-Base64Utf8 $expectedSshKeygenSha256
    '__HYPERV_MODULE_DIRECTORY__' = ConvertTo-Base64Utf8 $hyperVModuleDirectory
    '__SYSTEM_MODULE_DIRECTORY__' = ConvertTo-Base64Utf8 $systemModuleDirectory
    '__HYPERV_MANIFEST_PATH__' = ConvertTo-Base64Utf8 $hyperVManifestPath
    '__HYPERV_MANIFEST_HASH__' = ConvertTo-Base64Utf8 $expectedHyperVManifestSha256
    '__HYPERV_BINARY_PATH__' = ConvertTo-Base64Utf8 $hyperVBinaryPath
    '__HYPERV_BINARY_HASH__' = ConvertTo-Base64Utf8 $expectedHyperVBinarySha256
}
foreach ($key in $bootstrapValues.Keys) { $bootstrap = $bootstrap.Replace($key, $bootstrapValues[$key]) }
$bootstrapSnapshotBytes = [Text.Encoding]::UTF8.GetBytes($bootstrap)
$bootstrapSnapshotSha256 = Get-Sha256Bytes $bootstrapSnapshotBytes
Write-NewBytesFile -Path $bootstrapSnapshotPath -Bytes $bootstrapSnapshotBytes
$bootstrapLock = [IO.File]::Open($bootstrapSnapshotPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
if ((Get-Sha256Bytes (Read-LockedBytes $bootstrapLock)) -cne $bootstrapSnapshotSha256) { throw 'Locked elevated bootstrap snapshot differs from its creation hash.' }

$elevationWrapper = @'
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
function From-B64([string]$Value) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Value)) }
function Hash-Bytes([byte[]]$Bytes) {
  $sha = [Security.Cryptography.SHA256]::Create()
  try { ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '').ToUpperInvariant() }
  finally { $sha.Dispose() }
}
function Assert-NoReparsePath([string]$Path) {
  $fullPath = [IO.Path]::GetFullPath($Path)
  $pathRoot = [IO.Path]::GetPathRoot($fullPath)
  $currentPath = $pathRoot
  foreach ($component in ($fullPath.Substring($pathRoot.Length) -split '[\\/]')) {
    if ([string]::IsNullOrWhiteSpace($component)) { continue }
    $currentPath = Join-Path $currentPath $component
    if (-not (Test-Path -LiteralPath $currentPath)) { break }
    $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Bootstrap path contains a reparse component: $currentPath" }
  }
}
$runDirectory = [IO.Path]::GetFullPath((From-B64 '__RUN_DIR__'))
$bootstrapPath = [IO.Path]::GetFullPath((From-B64 '__BOOTSTRAP_PATH__'))
$expectedBootstrapPath = [IO.Path]::GetFullPath((Join-Path $runDirectory 'elevated-bootstrap.snapshot.ps1'))
$expectedBootstrapHash = From-B64 '__BOOTSTRAP_HASH__'
if ($bootstrapPath -ine $expectedBootstrapPath -or $expectedBootstrapHash -notmatch '^[0-9A-F]{64}$') {
  throw 'Elevated bootstrap path or SHA-256 pin is invalid.'
}
Assert-NoReparsePath -Path $runDirectory
Assert-NoReparsePath -Path $bootstrapPath
$bootstrapItem = Get-Item -LiteralPath $bootstrapPath -Force -ErrorAction Stop
if ($bootstrapItem.PSIsContainer -or ($bootstrapItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
  throw 'Elevated bootstrap snapshot is not a regular file.'
}
$bootstrapStream = [IO.File]::Open($bootstrapPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
try {
  $bootstrapMemory = [IO.MemoryStream]::new()
  try { $bootstrapStream.CopyTo($bootstrapMemory); $bootstrapBytes = $bootstrapMemory.ToArray() }
  finally { $bootstrapMemory.Dispose() }
}
finally { $bootstrapStream.Dispose() }
$actualBootstrapHash = Hash-Bytes $bootstrapBytes
if ($actualBootstrapHash -cne $expectedBootstrapHash) { throw 'Elevated bootstrap snapshot SHA-256 mismatch.' }
$strictUtf8 = [Text.UTF8Encoding]::new($false, $true)
try { $bootstrapSource = $strictUtf8.GetString($bootstrapBytes) }
catch { throw 'Elevated bootstrap snapshot is not strict UTF-8.' }
$bootstrapBlock = [ScriptBlock]::Create($bootstrapSource)
& $bootstrapBlock
'@
$wrapperValues = @{
    '__RUN_DIR__' = ConvertTo-Base64Utf8 $runDirectory
    '__BOOTSTRAP_PATH__' = ConvertTo-Base64Utf8 $bootstrapSnapshotPath
    '__BOOTSTRAP_HASH__' = ConvertTo-Base64Utf8 $bootstrapSnapshotSha256
}
foreach ($key in $wrapperValues.Keys) { $elevationWrapper = $elevationWrapper.Replace($key, $wrapperValues[$key]) }
$wrapperBytes = [Text.Encoding]::Unicode.GetBytes($elevationWrapper)
$bootstrapCommand = [Convert]::ToBase64String($wrapperBytes)
$bootstrapWrapperSha256 = Get-Sha256Bytes $wrapperBytes
$argumentString = '-NoProfile -NonInteractive -EncodedCommand ' + $bootstrapCommand
if ($argumentString.Length -gt 8192) { throw 'Elevated bootstrap wrapper exceeds the reviewed command-line size bound.' }
$launchRecord = [ordered]@{
    schema = 'tbound-abi8-elevation-launch/v1'
    status = 'elevation-requested'
    startedAt = [DateTimeOffset]::Now.ToString('o')
    action = $Action
    runId = $runId
    outputDirectory = $runDirectory
    candidateSha256 = $actualCandidateSha256
    sourceBytesSha256 = $actualCandidateSourceSha256
    sourceCanonicalization = 'strict UTF-8; optional UTF-8 BOM removed; CRLF normalized to LF; bare CR rejected'
    candidateSnapshot = $candidateSnapshotPath
    canonicalPowerShellPath = $canonicalPowerShellPath
    launchPowerShellPath = $launchPowerShellPath
    hyperVManifestPath = $hyperVManifestPath
    hyperVManifestSha256 = $expectedHyperVManifestSha256
    hyperVBinaryPath = $hyperVBinaryPath
    hyperVBinarySha256 = $expectedHyperVBinarySha256
    bootstrapWrapperSha256 = $bootstrapWrapperSha256
    bootstrapSnapshotPath = $bootstrapSnapshotPath
    bootstrapSnapshotSha256 = $bootstrapSnapshotSha256
    bootstrapSnapshotByteLength = $bootstrapSnapshotBytes.Length
    elevationArgumentCharacterCount = $argumentString.Length
    elevationVerb = 'RunAs'
    windowStyle = 'Hidden'
    childProcessId = $null
    childExitCode = $null
    childWaitCompleted = $false
    childResultValidated = $false
    rawLaunchError = $null
}
$launchPath = Join-Path $runDirectory 'launch.json'
Write-NewUtf8File -Path $launchPath -Text (($launchRecord | ConvertTo-Json -Depth 8) + [Environment]::NewLine)

$child = $null
$status = 'elevation-request-rejected-or-canceled'
$childExitCode = $null
$waitCompleted = $false
$rawLaunchError = $null
$deadlineMinutes = if ($Action -in @('Create', 'Resume')) { 130 } else { 10 }
try {
    $child = Start-Process -FilePath $launchPowerShellPath -ArgumentList $argumentString -Verb RunAs -WindowStyle Hidden -PassThru
    $launchRecord.childProcessId = $child.Id
    $launchRecord.status = 'child-launched-awaiting-result'
    $launchRecord.launchedAt = [DateTimeOffset]::Now.ToString('o')
    [IO.File]::WriteAllText($launchPath, ($launchRecord | ConvertTo-Json -Depth 8) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    Write-Output (($launchRecord | ConvertTo-Json -Depth 6))
    $deadline = [DateTime]::UtcNow.AddMinutes($deadlineMinutes)
    while (-not $child.HasExited -and [DateTime]::UtcNow -lt $deadline) { [void]$child.WaitForExit(5000) }
    $waitCompleted = $child.HasExited
    if ($waitCompleted) {
        $childExitCode = $child.ExitCode
        $childResultPath = Join-Path $runDirectory 'child-result.json'
        if ([IO.File]::Exists($childResultPath)) {
            $childResult = Get-Content -LiteralPath $childResultPath -Raw | ConvertFrom-Json -ErrorAction Stop
            $childResultValidated = (
                $childResult.schema -ceq 'tbound-abi8-elevated-candidate-result/v1' -and
                $childResult.runId -ceq $runId -and $childResult.action -ceq $Action -and
                $childResult.elevatedTokenVerified -eq $true -and
                $childResult.canonicalPowerShellVerified -eq $true -and
                $childResult.moduleVerified -eq $true -and
                $childResult.hostExecutablesVerified -eq $true -and
                $childResult.wslExecutableSha256 -ceq $expectedWslExeSha256 -and
                $childResult.sshKeygenExecutableSha256 -ceq $expectedSshKeygenSha256 -and
                $childResult.candidateBytesVerifiedBeforeScriptBlockCreation -eq $true -and
                $childResult.candidateSha256 -ceq $expectedCandidateSha256 -and
                $childResult.invocationCompleted -eq $true -and $childResult.exitCode -eq 0
            )
            if ($Action -ceq 'WhatIf') { $childResultValidated = $childResultValidated -and $childResult.outcome -ceq 'whatif-preflight-completed-no-vm-or-output-created' }
            else { $childResultValidated = $childResultValidated -and $childResult.outcome -ceq 'seed-created-and-off-state-receipt-validated' -and $childResult.candidateReceiptSha256 -match '^[0-9A-F]{64}$' }
            $status = if ($childExitCode -eq 0 -and $childResultValidated) { 'verified-' + $childResult.outcome } else { 'child-result-failed-validation' }
            $launchRecord.childResultValidated = [bool]$childResultValidated
        }
        else { $status = 'child-exited-without-result-receipt' }
    }
    else { $status = 'child-still-running-at-bounded-wait; do-not-relaunch' }
}
catch {
    $rawLaunchError = $_.Exception.ToString()
    $status = 'elevation-request-rejected-or-canceled'
}
finally {
    $bootstrapLock.Dispose()
    $candidateLock.Dispose()
}

$launchRecord.status = $status
$launchRecord.finishedAt = [DateTimeOffset]::Now.ToString('o')
$launchRecord.childExitCode = $childExitCode
$launchRecord.childWaitCompleted = $waitCompleted
$launchRecord.rawLaunchError = $rawLaunchError
$result = [ordered]@{
    schema = 'tbound-abi8-elevation-launch-result/v1'
    status = $status
    action = $Action
    runId = $runId
    outputDirectory = $runDirectory
    childResult = if ([IO.File]::Exists((Join-Path $runDirectory 'child-result.json'))) { Join-Path $runDirectory 'child-result.json' } else { $null }
    candidateOutput = if ([IO.File]::Exists((Join-Path $runDirectory 'candidate-output.txt'))) { Join-Path $runDirectory 'candidate-output.txt' } else { $null }
    childError = if ([IO.File]::Exists((Join-Path $runDirectory 'child-error.txt'))) { Join-Path $runDirectory 'child-error.txt' } else { $null }
    launchError = $rawLaunchError
}
$launchRecord.status = $status
$launchRecord.finishedAt = [DateTimeOffset]::Now.ToString('o')
$launchRecord.childExitCode = $childExitCode
$launchRecord.childWaitCompleted = $waitCompleted
$launchRecord.childResultValidated = [bool]($status -like 'verified-*')
$launchRecord.rawLaunchError = $rawLaunchError
[IO.File]::WriteAllText($launchPath, ($launchRecord | ConvertTo-Json -Depth 8) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
$launchResultPath = Join-Path $runDirectory 'launch-result.json'
Write-NewUtf8File -Path $launchResultPath -Text (($result | ConvertTo-Json -Depth 6) + [Environment]::NewLine)
Write-Output ($result | ConvertTo-Json -Depth 6)
if ($status -notlike 'verified-*') { exit 2 }
exit 0
