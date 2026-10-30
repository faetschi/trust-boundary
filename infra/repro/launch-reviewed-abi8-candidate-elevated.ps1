#requires -Version 5.1
<#
.SYNOPSIS
  Runs the fixed ABI-8 seed helper through a source-checked UAC child.
.DESCRIPTION
  Accepted operations include fixed read-only WhatIf and Observe actions,
  creation of the one predeclared disposable seed, or continuation using the
  fixed already-pinned ISO and task key. Observe reports fixed seed metadata
  without invoking candidate code. No paths, VM names, switches, or commands
  are accepted from the caller.
  Candidate and bootstrap bytes and the installed Hyper-V module are pinned
  before elevation and rechecked before a ScriptBlock is created.
  Create and Resume are bounded by the seed helper's 120-minute installer limit
  plus ten minutes of setup slack. FinalizeExistingSeed accepts only the fixed
  observed VM and does not create or start one. A timeout is recorded without
  deleting or powering off a partially created VM.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateSet('WhatIf', 'Create', 'Resume', 'Observe', 'FinalizeExistingSeed', 'BootstrapCandidate')]
    [string]$Action
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

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

$expectedCandidateSha256 = '9B0FCB40408FC472ED829B665FF0B13ADF30FB2FB9B51B9EA74921B19B826B03'
$expectedHyperVManifestSha256 = 'C5F845C5BA7A4960D876DB46D1E63905CD9AC8EFEF650CB3EC9DBBD8B713F4CC'
$expectedHyperVBinarySha256 = '0FD7A8B2CB8C15E9DA691726C1F1F71F3EC4F853837FD099BA3F9EB2E9399316'
$expectedWslExeSha256 = '4e589e3883229b7a74a4acdb878689dcec94e2539fcad1c194f415b149c337a9'
$expectedSshKeygenSha256 = '539ae392c61a53fadddf0bbb152126f3e0bc042e8a4ed50718ab16fbf872cbcc'
$expectedSeedReceiptSha256 = '86705E12F8D52BF7AFA8810A32ADA1D60370A61807E5BFD2AEAA13271326D53F'
$expectedSeedDiskSha256 = 'EDF7AE59B39A8D80AFD5B74FC0D798D16DED5FC5E32F94CCD617897A3BAEEC00'
$bootstrapDependencyPins = [ordered]@{
    'infra/repro/bootstrap-hyperv-windows.ps1' = '303AE7287934EC244ED55A1C260406C9EE0837C9D2972EC87794C27B148DD6EE'
    'infra/repro/bootstrap-linux.sh' = 'AD26638C8CB18F73AA45BEF50ABF5609BDA0B48EA7430DC029BD49224294068F'
    'infra/repro/landlock-profile-gate.sh' = '4C7040443E8EB4312E5793515B26C37C6D2C66D28361E63C23426BDA7751448B'
    'infra/guest/setup-ubuntu-24.04.sh' = '478484545BB73A531F20732DE784B5A3EDF2C816BD0B5ED3519E8546A16D5443'
    'infra/guest/probe-capabilities.sh' = '6E4DAAAF27DCD838E233AD8AEE191C07FDD328A9EA56908FC54C06D59446A3B8'
    'infra/repro/probe-live-landlock-abi8-ssh.ps1' = '5F81221869AD1E1912154DE9F430EFA58DFA7768401DF9E0A21342AA7E44C296'
}
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
$bootstrapInputLocks = [System.Collections.Generic.List[IO.FileStream]]::new()
$bootstrapSourceRoot = $null
$bootstrapSourceManifestPath = $null
$bootstrapSourceManifestSha256 = $null
$bootstrapDependencyRecords = @()
Write-NewBytesFile -Path $candidateSnapshotPath -Bytes $candidateBytes
$candidateLock = [IO.File]::Open($candidateSnapshotPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
if ((Get-Sha256Bytes (Read-LockedBytes $candidateLock)) -cne $expectedCandidateSha256) { throw 'Locked candidate snapshot differs from the source pin.' }
if ($Action -ceq 'BootstrapCandidate') {
    $repositoryRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
    Assert-NoReparsePathComponents -Path $repositoryRoot
    $bootstrapSourceRoot = Join-Path $runDirectory 'bootstrap-source'
    [IO.Directory]::CreateDirectory($bootstrapSourceRoot) | Out-Null
    foreach ($relativePath in $bootstrapDependencyPins.Keys) {
        $sourcePath = [IO.Path]::GetFullPath((Join-Path $repositoryRoot ($relativePath.Replace('/', '\'))))
        Assert-NoReparsePathComponents -Path $sourcePath
        $sourceItem = Get-Item -LiteralPath $sourcePath -Force -ErrorAction Stop
        if ($sourceItem.PSIsContainer -or ($sourceItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Pinned bootstrap dependency is not a regular file: $relativePath"
        }
        $sourceLock = [IO.File]::Open($sourcePath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
        try { $sourceBytes = Read-LockedBytes $sourceLock }
        finally { $sourceLock.Dispose() }
        $sourceSha256 = Get-Sha256Bytes $sourceBytes
        if ($sourceSha256 -cne $bootstrapDependencyPins[$relativePath]) {
            throw "Pinned bootstrap dependency hash mismatch: $relativePath"
        }
        $snapshotPath = [IO.Path]::GetFullPath((Join-Path $bootstrapSourceRoot ($relativePath.Replace('/', '\'))))
        $snapshotDirectory = [IO.Path]::GetDirectoryName($snapshotPath)
        [IO.Directory]::CreateDirectory($snapshotDirectory) | Out-Null
        Assert-NoReparsePathComponents -Path $snapshotPath
        Write-NewBytesFile -Path $snapshotPath -Bytes $sourceBytes
        $snapshotLock = [IO.File]::Open($snapshotPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
        if ((Get-Sha256Bytes (Read-LockedBytes $snapshotLock)) -cne $sourceSha256) {
            $snapshotLock.Dispose()
            throw "Pinned bootstrap dependency snapshot mismatch: $relativePath"
        }
        $bootstrapInputLocks.Add($snapshotLock)
        $bootstrapDependencyRecords += ,[ordered]@{
            path = $relativePath
            sha256 = $sourceSha256
            byteLength = $sourceBytes.Length
        }
    }
    $bootstrapSourceManifestPath = Join-Path $runDirectory 'bootstrap-source-manifest.json'
    $bootstrapSourceManifest = [ordered]@{
        schema = 'tbound-abi8-bootstrap-source-manifest/v1'
        snapshotRoot = 'bootstrap-source'
        files = $bootstrapDependencyRecords
    }
    $bootstrapSourceManifestText = ($bootstrapSourceManifest | ConvertTo-Json -Depth 6) + [Environment]::NewLine
    Write-NewUtf8File -Path $bootstrapSourceManifestPath -Text $bootstrapSourceManifestText
    $bootstrapSourceManifestBytes = [Text.UTF8Encoding]::new($false).GetBytes($bootstrapSourceManifestText)
    $bootstrapSourceManifestSha256 = Get-Sha256Bytes $bootstrapSourceManifestBytes
    $bootstrapInputLocks.Add([IO.File]::Open($bootstrapSourceManifestPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read))
}

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
function Write-NewBytesDurably([string]$Path, [byte[]]$Bytes) {
  $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
  try { $stream.Write($Bytes, 0, $Bytes.Length); $stream.Flush($true) }
  finally { $stream.Dispose() }
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
$bootstrapSourceRoot = From-B64 '__BOOTSTRAP_SOURCE_ROOT__'
$expectedBootstrapSourceManifestHash = From-B64 '__BOOTSTRAP_SOURCE_MANIFEST_HASH__'
$expectedSeedReceiptHash = From-B64 '__SEED_RECEIPT_HASH__'
$expectedSeedDiskHash = From-B64 '__SEED_DISK_HASH__'
$childResultPath = Join-Path $runDirectory 'child-result.json'
$childErrorPath = Join-Path $runDirectory 'child-error.txt'
$candidateOutputPath = Join-Path $runDirectory 'candidate-output.txt'
$seedStatePath = Join-Path $runDirectory 'seed-state.json'
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
  seedStateSha256 = $null
  bootstrapSourceManifestSha256 = $null
  bootstrapCandidate = $null
  exitCode = 2
}
function Assert-FixedSeedReceiptMetadata {
  param([object]$Receipt, [string]$DiskPath, [string]$ExpectedDiskHash, [string]$PrivateKeyPath)
  if ($Receipt.schema -cne 'tbound-abi8-hyperv-seed/v1' -or
      $Receipt.seedVm.id -cne '82ffce59-06bf-4c1b-b43a-d406489c6d7f' -or
      $Receipt.seedVm.name -cne 'TBound-Ubuntu-ABI8-Seed' -or $Receipt.seedVm.state -cne 'Off' -or
      $Receipt.seedVm.generation -ne 2 -or $Receipt.seedVm.processorCount -ne 2 -or
      $Receipt.seedVm.fixedMemoryBytes -ne 4GB -or $Receipt.seedVm.virtualDiskBytes -ne 40GB -or
      $Receipt.seedVm.diskType -cne 'Dynamic' -or $Receipt.seedVm.diskPath -ine $DiskPath -or
      $Receipt.seedVm.diskSha256 -ine $ExpectedDiskHash -or $Receipt.seedVm.parentPath -or
      $Receipt.provisioningKey.privateKeyPath -ine $PrivateKeyPath -or
      $Receipt.provisioningKey.privateKeyMaterialIncluded -ne $false) {
    throw 'Finalized seed receipt did not match the fixed clone source and profile.'
  }
}
function Assert-FixedCandidateVmMetadata {
  param([object]$Vm, [object]$Cpu, [object]$Memory, [object]$Firmware,
        [object[]]$Adapters, [object[]]$Disks, [object[]]$Snapshots,
        [object]$Vhd, [string]$DiskPath)
  if ($Disks.Count -ne 1 -or $Disks[0].Path -ine $DiskPath) { throw 'Final candidate has an unexpected disk attachment.' }
  if ($Vm.State -ne 'Running' -or $Vm.Generation -ne 2 -or $Cpu.Count -ne 8 -or
      $Memory.DynamicMemoryEnabled -or $Memory.Startup -ne 16GB -or
      $Firmware.SecureBoot -ne 'On' -or $Firmware.SecureBootTemplate -notmatch 'MicrosoftUEFICertificateAuthority' -or
      $Adapters.Count -ne 1 -or $Adapters[0].SwitchName -cne 'Default Switch' -or
      $Snapshots.Count -ne 0 -or $Vhd.VhdFormat -ne 'VHDX' -or $Vhd.VhdType -ne 'Dynamic' -or
      $Vhd.Size -ne 40GB -or $Vhd.ParentPath) {
    throw 'Final candidate differs from the fixed 8-CPU/16-GiB/40-GiB standalone profile.'
  }
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
  $assetRoot = 'F:\TBoundAssets\abi8-qualification-2026-10-14'
  if ($action -ceq 'Observe') {
    $seedVms = @(Get-VM -Name 'TBound-Ubuntu-ABI8-Seed' -ErrorAction Stop)
    if ($seedVms.Count -ne 1) { throw 'Read-only observation did not resolve exactly one fixed seed VM.' }
    $seedVm = $seedVms[0]
    $seedCpu = Get-VMProcessor -VMName $seedVm.Name -ErrorAction Stop
    $seedMemory = Get-VMMemory -VMName $seedVm.Name -ErrorAction Stop
    $seedFirmware = Get-VMFirmware -VMName $seedVm.Name -ErrorAction Stop
    $seedDvds = @(Get-VMDvdDrive -VMName $seedVm.Name -ErrorAction Stop | ForEach-Object {
      [ordered]@{
        controllerNumber = [int]$_.ControllerNumber
        controllerLocation = [int]$_.ControllerLocation
        path = [string]$_.Path
      }
    })
    $seedDisks = @(Get-VMHardDiskDrive -VMName $seedVm.Name -ErrorAction Stop | ForEach-Object {
      [ordered]@{
        controllerNumber = [int]$_.ControllerNumber
        controllerLocation = [int]$_.ControllerLocation
        path = [string]$_.Path
      }
    })
    $seedState = [ordered]@{
      schema = 'tbound-abi8-seed-state/v1'
      runId = $runId
      observedAt = [DateTimeOffset]::Now.ToString('o')
      vm = [ordered]@{
        name = [string]$seedVm.Name
        id = [string]$seedVm.Id
        state = [string]$seedVm.State
        generation = [int]$seedVm.Generation
        processorCount = [int]$seedCpu.Count
        startupMemoryBytes = [Int64]$seedMemory.Startup
        dynamicMemoryEnabled = [bool]$seedMemory.DynamicMemoryEnabled
        secureBoot = [string]$seedFirmware.SecureBoot
        secureBootTemplate = [string]$seedFirmware.SecureBootTemplate
        dvdDrives = $seedDvds
        hardDisks = $seedDisks
      }
    }
    $seedStateJson = ($seedState | ConvertTo-Json -Depth 6) + [Environment]::NewLine
    $seedStateBytes = [Text.UTF8Encoding]::new($false).GetBytes($seedStateJson)
    if ([IO.Path]::GetFileName($seedStatePath) -cne 'seed-state.json' -or
        [IO.Path]::GetFullPath([IO.Path]::GetDirectoryName($seedStatePath)) -ine [IO.Path]::GetFullPath($runDirectory)) {
      throw 'Read-only state receipt escaped its fixed per-run path.'
    }
    Assert-NoReparsePath -Path $seedStatePath
    Write-NewBytesDurably -Path $seedStatePath -Bytes $seedStateBytes
    $result.seedStateSha256 = (Hash-Bytes $seedStateBytes).ToLowerInvariant()
    $result.invocationCompleted = $true
    $result.outcome = 'observed-seed-state'
    $exitCode = 0
  }
  else {
    $candidateBlock = [ScriptBlock]::Create((Decode-Script $candidateBytes))
    $captured = @()
    if ($action -ceq 'BootstrapCandidate') { $captured = @() }
    elseif ($action -ceq 'WhatIf') {
      $captured = @(& $candidateBlock -AssetRoot $assetRoot -InstallTimeoutMinutes 120 -MinimumFreeGb 120 -WhatIf *>&1)
    }
    elseif ($action -ceq 'Create') {
      $captured = @(& $candidateBlock -AssetRoot $assetRoot -InstallTimeoutMinutes 120 -MinimumFreeGb 120 -Confirm:$false *>&1)
    }
  elseif ($action -ceq 'Resume') {
    $captured = @(& $candidateBlock -AssetRoot $assetRoot -InstallTimeoutMinutes 120 -MinimumFreeGb 120 -ResumeVerifiedMedia -Confirm:$false *>&1)
  }
  elseif ($action -ceq 'FinalizeExistingSeed') {
    $captured = @(& $candidateBlock -AssetRoot $assetRoot -InstallTimeoutMinutes 120 -MinimumFreeGb 120 -FinalizeExistingSeed -Confirm:$false *>&1)
  }
    else { throw 'Action is not one of the fixed reviewed operations.' }
    if ($action -cne 'BootstrapCandidate') {
      $capturedText = @($captured | ForEach-Object { [string]$_ })
      $capturedUtf8 = [Text.Encoding]::UTF8.GetBytes(($capturedText -join [Environment]::NewLine) + [Environment]::NewLine)
      $result.invocationOutputLineCount = $capturedText.Count
      $result.outputSha256 = Hash-Bytes $capturedUtf8
      $result.invocationCompleted = $true
      [IO.File]::WriteAllBytes($candidateOutputPath, $capturedUtf8)
    }
    if ($action -ceq 'BootstrapCandidate') {
      $expectedSourceRoot = [IO.Path]::GetFullPath((Join-Path $runDirectory 'bootstrap-source'))
      if ([IO.Path]::GetFullPath($bootstrapSourceRoot) -ine $expectedSourceRoot -or
          $expectedBootstrapSourceManifestHash -notmatch '^[0-9A-F]{64}$') {
        throw 'Pinned bootstrap source snapshot path or manifest hash is invalid.'
      }
      Assert-NoReparsePath -Path $expectedSourceRoot
      $result.bootstrapSourceManifestSha256 = $expectedBootstrapSourceManifestHash

      $assetRoot = 'F:\TBoundAssets\abi8-qualification-2026-10-14'
      $seedReceiptPath = Join-Path $assetRoot 'abi8-seed-receipt.json'
      $seedDiskPath = 'F:\TBoundAssets\abi8-qualification-2026-10-14\seed-vm\Virtual Hard Disks\TBound-Ubuntu-ABI8-Seed.vhdx'
      if ((Hash-Bytes ([IO.File]::ReadAllBytes($seedReceiptPath))) -cne $expectedSeedReceiptHash) { throw 'Finalized seed receipt hash changed.' }
      $seedReceipt = Get-Content -LiteralPath $seedReceiptPath -Raw | ConvertFrom-Json -ErrorAction Stop
      Assert-FixedSeedReceiptMetadata -Receipt $seedReceipt -DiskPath $seedDiskPath `
        -ExpectedDiskHash $expectedSeedDiskHash -PrivateKeyPath (Join-Path $env:LOCALAPPDATA 'TBound\abi8-qualification\id_ed25519')
      if ((Get-FileHash -LiteralPath $seedDiskPath -Algorithm SHA256 -ErrorAction Stop).Hash -cne $expectedSeedDiskHash) { throw 'Finalized seed VHDX hash changed.' }
      $seedVms = @(Get-VM -Name 'TBound-Ubuntu-ABI8-Seed' -ErrorAction Stop)
      if ($seedVms.Count -ne 1) { throw 'Expected exactly one fixed seed VM.' }
      $seedVm = $seedVms[0]
      $seedDiskDrives = @(Get-VMHardDiskDrive -VMName $seedVm.Name -ErrorAction Stop)
      if ($seedVm.Id.ToString() -ine $seedReceipt.seedVm.id -or $seedVm.State -ne 'Off' -or
          $seedVm.Generation -ne 2 -or $seedDiskDrives.Count -ne 1 -or
          $seedDiskDrives[0].Path -ine $seedDiskPath) { throw 'Live seed identity, power state, or source disk differs from its receipt.' }

      $originalVms = @(Get-VM -Name 'TBound-Ubuntu-2404' -ErrorAction SilentlyContinue)
      if ($originalVms.Count -ne 1) { throw 'The existing original VM was not found exactly once; refusing the replacement clone action.' }
      $originalId = $originalVms[0].Id.ToString()
      $originalState = [string]$originalVms[0].State
      $candidateVmName = 'TBound-Ubuntu-2404-ABI8'
      $candidateVmRoot = 'F:\TBoundVMs\TBound-Ubuntu-2404-ABI8'
      $publicKeyPath = Join-Path $env:LOCALAPPDATA 'TBound\abi8-qualification\id_ed25519.pub'
      $privateKeyPath = Join-Path $env:LOCALAPPDATA 'TBound\abi8-qualification\id_ed25519'

      $bootstrapScriptPath = Join-Path $expectedSourceRoot 'infra\repro\bootstrap-hyperv-windows.ps1'
      $probeScriptPath = Join-Path $expectedSourceRoot 'infra\repro\probe-live-landlock-abi8-ssh.ps1'
      $captured = @(& $bootstrapScriptPath -ImageKind GoldenVhdx -ImageSourcePath $seedDiskPath `
        -ExpectedImageSha256 $expectedSeedDiskHash -PublicKeyFile $publicKeyPath -PrivateKeyFile $privateKeyPath `
        -VmName $candidateVmName -VmRoot $candidateVmRoot -SwitchName 'Default Switch' -GuestUser 'tboundadmin' `
        -SshPort 22 -BootTimeoutSeconds 900 -MinimumFreeGb 120 -RepoRoot $expectedSourceRoot -Confirm:$false *>&1)
      $bootstrapOutput = @($captured | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
      if ($bootstrapOutput -notmatch '(?m)^SSH private key matches a supplied public key \(comparison passed\)\.\r?$') {
        throw 'Bootstrap did not report successful private/public key comparison.'
      }
      $hostMemoryMatches = [regex]::Matches($bootstrapOutput, '(?m)^Host available physical memory immediately before VM start: ([0-9]+(?:\.[0-9]+)?) GiB\.\r?$')
      if ($hostMemoryMatches.Count -ne 1) { throw 'Bootstrap did not report one read-only host-memory measurement immediately before VM start.' }
      $hostAvailableMemoryGiBAtStart = [double]::Parse($hostMemoryMatches[0].Groups[1].Value, [Globalization.CultureInfo]::InvariantCulture)
      if ($hostAvailableMemoryGiBAtStart -lt 16) { throw 'Host free memory fell below the fixed 16 GiB startup allocation before VM start.' }
      $summaryPath = Join-Path $candidateVmRoot 'provisioning-summary.txt'
      $summary = Get-Content -LiteralPath $summaryPath -Raw -ErrorAction Stop
      $ipMatches = [regex]::Matches($summary, '(?m)^Guest IP:\s+((?:\d{1,3}\.){3}\d{1,3})\s*(?:\(|$)')
      $fingerprintMatches = [regex]::Matches($summary, '(?m)^Guest ED25519 host key SHA256:\s*(SHA256:[A-Za-z0-9+/]{43})\s*$')
      if ($ipMatches.Count -ne 1 -or $fingerprintMatches.Count -ne 1) { throw 'Bootstrap summary did not contain exactly one guest IPv4 and offline-derived ED25519 fingerprint.' }
      $guestIp = $ipMatches[0].Groups[1].Value
      $offlineFingerprint = $fingerprintMatches[0].Groups[1].Value
      $probeCaptured = @(& $probeScriptPath -HostName $guestIp -GuestHostKeyFingerprint $offlineFingerprint -UserName 'tboundadmin' -Port 22 *>&1)
      $probeText = @($probeCaptured | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
      if ($probeText -notmatch '(?m)^Landlock ABI: 8; errno: 0\s*$') { throw 'Live guest did not return exact Landlock ABI 8 with errno 0.' }

      $candidateVm = Get-VM -Name $candidateVmName -ErrorAction Stop
      $candidateCpu = Get-VMProcessor -VMName $candidateVmName -ErrorAction Stop
      $candidateMemory = Get-VMMemory -VMName $candidateVmName -ErrorAction Stop
      $candidateFirmware = Get-VMFirmware -VMName $candidateVmName -ErrorAction Stop
      $candidateAdapters = @(Get-VMNetworkAdapter -VMName $candidateVmName -ErrorAction Stop)
      $candidateDisks = @(Get-VMHardDiskDrive -VMName $candidateVmName -ErrorAction Stop)
      $candidateSnapshots = @(Get-VMSnapshot -VMName $candidateVmName -ErrorAction Stop)
      $candidateVhd = Get-VHD -Path $candidateDisks[0].Path -ErrorAction Stop
      Assert-FixedCandidateVmMetadata -Vm $candidateVm -Cpu $candidateCpu -Memory $candidateMemory `
        -Firmware $candidateFirmware -Adapters $candidateAdapters -Disks $candidateDisks `
        -Snapshots $candidateSnapshots -Vhd $candidateVhd `
        -DiskPath (Join-Path $candidateVmRoot 'Virtual Hard Disks\TBound-Ubuntu-2404-ABI8.vhdx')
      $seedVmAfter = Get-VM -Name 'TBound-Ubuntu-ABI8-Seed' -ErrorAction Stop
      if ((Get-FileHash -LiteralPath $seedDiskPath -Algorithm SHA256 -ErrorAction Stop).Hash -cne $expectedSeedDiskHash -or
          $seedVmAfter.Id.ToString() -ine $seedVm.Id.ToString() -or $seedVmAfter.State -ne 'Off') {
        throw 'Seed VM identity, state, or source VHDX changed while the clone was provisioned.'
      }
      $originalAfter = Get-VM -Name 'TBound-Ubuntu-2404' -ErrorAction Stop
      if ($originalAfter.Id.ToString() -cne $originalId -or [string]$originalAfter.State -cne $originalState) {
        throw 'Original VM identity or power state changed during candidate provisioning.'
      }
      $finalInvocationText = $bootstrapOutput + [Environment]::NewLine + $probeText
      $finalInvocationBytes = [Text.Encoding]::UTF8.GetBytes($finalInvocationText + [Environment]::NewLine)
      $result.invocationOutputLineCount = @($finalInvocationText -split '\r?\n').Count
      $result.outputSha256 = Hash-Bytes $finalInvocationBytes
      [IO.File]::WriteAllBytes($candidateOutputPath, $finalInvocationBytes)
      $result.invocationCompleted = $true
      $result.bootstrapCandidate = [ordered]@{
        vmName = $candidateVmName
        vmId = $candidateVm.Id.ToString()
        state = [string]$candidateVm.State
        generation = [int]$candidateVm.Generation
        processorCount = [int]$candidateCpu.Count
        startupMemoryBytes = [Int64]$candidateMemory.Startup
        dynamicMemoryEnabled = [bool]$candidateMemory.DynamicMemoryEnabled
        switchName = [string]$candidateAdapters[0].SwitchName
        diskPath = [string]$candidateDisks[0].Path
        diskType = [string]$candidateVhd.VhdType
        diskSizeBytes = [Int64]$candidateVhd.Size
        diskParentPath = [string]$candidateVhd.ParentPath
        diskSha256 = $null
        diskSha256Status = 'not-captured-while-running'
        guestIpv4 = $guestIp
        guestEd25519Fingerprint = $offlineFingerprint
        liveLandlockAbi = 8
        liveLandlockErrno = 0
        privatePublicKeyMatchReported = $true
        hostAvailablePhysicalMemoryGiBAtStart = $hostAvailableMemoryGiBAtStart
        seedVmId = $seedVm.Id.ToString()
        seedRemainedOff = $true
        seedDiskSha256 = $expectedSeedDiskHash.ToLowerInvariant()
        originalVmId = $originalId
        originalVmStateUnchanged = $true
      }
      $result.outcome = 'bootstrap-candidate-created-live-abi8-verified'
      $exitCode = 0
    }
    elseif ($action -ceq 'WhatIf') {
      if ([IO.Directory]::Exists($assetRoot) -or (Get-VM -Name 'TBound-Ubuntu-ABI8-Seed' -ErrorAction SilentlyContinue) -or
          (Get-VM -Name 'TBound-Ubuntu-2404-ABI8' -ErrorAction SilentlyContinue)) {
        throw 'WhatIf unexpectedly created a candidate output directory or VM.'
      }
      $result.outcome = 'whatif-preflight-completed-no-vm-or-output-created'
      $exitCode = 0
    }
    elseif ($action -in @('Create', 'Resume', 'FinalizeExistingSeed')) {
      $receiptPath = Join-Path $assetRoot 'abi8-seed-receipt.json'
      if (-not [IO.File]::Exists($receiptPath)) { throw 'Create completed without the fixed seed receipt.' }
      $receipt = Get-Content -LiteralPath $receiptPath -Raw | ConvertFrom-Json -ErrorAction Stop
      if ($receipt.schema -cne 'tbound-abi8-hyperv-seed/v1' -or
          $receipt.sourceMedia.sha256 -cne '97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8' -or
          $receipt.derivedMedia.canonicalSignatureCoversDerivedMedia -ne $false -or
          $receipt.derivedMedia.xorrisoVersion -notmatch '^xorriso 1\.5\.6' -or
          ($action -in @('Resume', 'FinalizeExistingSeed') -and ($receipt.derivedMedia.sha256 -cne '1ec771177b5c8e347ea6829f40744cc9505a79d6faf79b3f7a3eda74b2f84e76' -or $receipt.derivedMedia.reusedExistingMedia -ne $true)) -or
          ($action -ceq 'Create' -and $receipt.derivedMedia.reusedExistingMedia -ne $false) -or
          ($action -ceq 'FinalizeExistingSeed' -and $receipt.seedVm.id -cne '82ffce59-06bf-4c1b-b43a-d406489c6d7f') -or
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
      if ($seedVm.Id.ToString() -ine $receipt.seedVm.id) { throw 'Live seed VM ID differs from its fixed receipt.' }
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
      $result.outcome = if ($action -ceq 'FinalizeExistingSeed') {
        'seed-finalized-existing-off-state-receipt-validated'
      }
      else { 'seed-created-and-off-state-receipt-validated' }
      $exitCode = 0
    }
    else { throw 'Action is not one of the fixed reviewed operations.' }
  }
}
catch {
  $failureText = $_.Exception.ToString() + [Environment]::NewLine + 'ScriptStackTrace:' + [Environment]::NewLine + [string]$_.ScriptStackTrace
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
    '__BOOTSTRAP_SOURCE_ROOT__' = ConvertTo-Base64Utf8 ([string]$bootstrapSourceRoot)
    '__BOOTSTRAP_SOURCE_MANIFEST_HASH__' = ConvertTo-Base64Utf8 ([string]$bootstrapSourceManifestSha256)
    '__SEED_RECEIPT_HASH__' = ConvertTo-Base64Utf8 $expectedSeedReceiptSha256
    '__SEED_DISK_HASH__' = ConvertTo-Base64Utf8 $expectedSeedDiskSha256
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
    bootstrapSourceRoot = $bootstrapSourceRoot
    bootstrapSourceManifestPath = $bootstrapSourceManifestPath
    bootstrapSourceManifestSha256 = $bootstrapSourceManifestSha256
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
$deadlineMinutes = if ($Action -in @('Create', 'Resume', 'BootstrapCandidate')) { 130 } else { 10 }
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
                $childResult.exitCode -eq 0
            )
            if ($Action -ceq 'WhatIf') {
                $childResultValidated = $childResultValidated -and
                    $childResult.invocationCompleted -eq $true -and
                    $childResult.outcome -ceq 'whatif-preflight-completed-no-vm-or-output-created'
            }
            elseif ($Action -ceq 'Observe') {
                $seedStateValidated = $false
                try {
                    $seedStatePath = Join-Path $runDirectory 'seed-state.json'
                    Assert-NoReparsePathComponents -Path $seedStatePath
                    if ($childResult.seedStateSha256 -match '^[0-9A-Fa-f]{64}$' -and [IO.File]::Exists($seedStatePath)) {
                        $seedStateItem = Get-Item -LiteralPath $seedStatePath -Force -ErrorAction Stop
                        if (-not $seedStateItem.PSIsContainer -and ($seedStateItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0) {
                            $seedStateBytes = [IO.File]::ReadAllBytes($seedStatePath)
                            if ((Get-Sha256Bytes $seedStateBytes) -ieq $childResult.seedStateSha256) {
                                $strictUtf8 = [Text.UTF8Encoding]::new($false, $true)
                                $seedStateText = $strictUtf8.GetString($seedStateBytes)
                                $seedState = $seedStateText | ConvertFrom-Json -ErrorAction Stop
                                $dvdEntries = @($seedState.vm.dvdDrives)
                                $diskEntries = @($seedState.vm.hardDisks)
                                $seedStateValidated = (
                                    $seedState.schema -ceq 'tbound-abi8-seed-state/v1' -and
                                    $seedState.runId -ceq $runId -and
                                    $seedState.vm.name -ceq 'TBound-Ubuntu-ABI8-Seed' -and
                                    $seedState.vm.id -match '^[0-9a-fA-F-]{36}$' -and
                                    $dvdEntries.Count -le 8 -and $diskEntries.Count -le 16
                                )
                            }
                        }
                    }
                }
                catch { $seedStateValidated = $false }
                $childResultValidated = $childResultValidated -and
                    $childResult.outcome -ceq 'observed-seed-state' -and
                    $childResult.seedStateSha256 -match '^[0-9A-Fa-f]{64}$' -and
                    $seedStateValidated
            }
            elseif ($Action -ceq 'BootstrapCandidate') {
                $candidate = $childResult.bootstrapCandidate
                $childResultValidated = $childResultValidated -and $null -ne $candidate
                if ($null -ne $candidate) {
                    $guestAddress = $null
                    $guestAddressParsed = [Net.IPAddress]::TryParse([string]$candidate.guestIpv4, [ref]$guestAddress) -and
                        $guestAddress.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork -and
                        $guestAddress.ToString() -ceq [string]$candidate.guestIpv4
                    $childResultValidated = $childResultValidated -and
                    $childResult.invocationCompleted -eq $true -and
                    $childResult.outcome -ceq 'bootstrap-candidate-created-live-abi8-verified' -and
                    $childResult.bootstrapSourceManifestSha256 -ceq $bootstrapSourceManifestSha256 -and
                    $candidate.vmName -ceq 'TBound-Ubuntu-2404-ABI8' -and
                    $candidate.vmId -match '^[0-9a-fA-F-]{36}$' -and
                    $candidate.state -ceq 'Running' -and $candidate.generation -eq 2 -and
                    $candidate.processorCount -eq 8 -and $candidate.startupMemoryBytes -eq 16GB -and
                    $candidate.dynamicMemoryEnabled -eq $false -and $candidate.switchName -ceq 'Default Switch' -and
                    $candidate.diskPath -ieq 'F:\TBoundVMs\TBound-Ubuntu-2404-ABI8\Virtual Hard Disks\TBound-Ubuntu-2404-ABI8.vhdx' -and
                    $candidate.diskType -ceq 'Dynamic' -and $candidate.diskSizeBytes -eq 40GB -and
                    [string]::IsNullOrEmpty([string]$candidate.diskParentPath) -and
                    [string]::IsNullOrEmpty([string]$candidate.diskSha256) -and
                    $candidate.diskSha256Status -ceq 'not-captured-while-running' -and
                    $guestAddressParsed -and $candidate.guestEd25519Fingerprint -match '^SHA256:[A-Za-z0-9+/]{43}$' -and
                    $candidate.liveLandlockAbi -eq 8 -and $candidate.liveLandlockErrno -eq 0 -and
                    $candidate.privatePublicKeyMatchReported -eq $true -and
                    $candidate.hostAvailablePhysicalMemoryGiBAtStart -ge 16 -and
                    $candidate.seedVmId -ceq '82ffce59-06bf-4c1b-b43a-d406489c6d7f' -and
                    $candidate.seedRemainedOff -eq $true -and
                    $candidate.seedDiskSha256 -ceq $expectedSeedDiskSha256.ToLowerInvariant() -and
                    $candidate.originalVmId -match '^[0-9a-fA-F-]{36}$' -and
                    $candidate.originalVmStateUnchanged -eq $true
                }
            }
            else {
                $childResultValidated = $childResultValidated -and
                    $childResult.invocationCompleted -eq $true -and
                    $childResult.outcome -ceq $(if ($Action -ceq 'FinalizeExistingSeed') { 'seed-finalized-existing-off-state-receipt-validated' } else { 'seed-created-and-off-state-receipt-validated' }) -and
                    $childResult.candidateReceiptSha256 -match '^[0-9A-F]{64}$'
            }
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
    foreach ($inputLock in $bootstrapInputLocks) { $inputLock.Dispose() }
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
    seedState = if ($Action -ceq 'Observe' -and [IO.File]::Exists((Join-Path $runDirectory 'seed-state.json'))) { Join-Path $runDirectory 'seed-state.json' } else { $null }
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
