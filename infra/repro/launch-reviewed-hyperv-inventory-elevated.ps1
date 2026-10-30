#requires -Version 5.1
<#
.SYNOPSIS
  Launch one pinned, read-only Hyper-V inventory through a task-specific UAC child.
.DESCRIPTION
  No command, VM-name, or output-path is accepted. The launcher reads fixed
  source bytes once, verifies literal SHA-256 pins, stages and locks those exact
  bytes, then starts canonical System32 PowerShell with a small encoded
  bootstrap. The elevated bootstrap verifies the staged bytes before creating
  either ScriptBlock. It imports only the pinned Hyper-V manifest with ambient
  PSModulePath removed and writes evidence outside the source tree.
#>
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($args.Count -ne 0) { throw 'This launcher accepts no arguments.' }

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
    finally { if ($stream) { $stream.Dispose() } }
}

function Write-NewBytesFile {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][byte[]]$Bytes)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $stream.Write($Bytes, 0, $Bytes.Length); $stream.Flush($true) }
    finally { $stream.Dispose() }
}

function Read-LockedBytes {
    param([Parameter(Mandatory)][IO.FileStream]$Stream)
    $Stream.Position = 0
    $memory = [IO.MemoryStream]::new()
    try { $Stream.CopyTo($memory); return ,($memory.ToArray()) }
    finally { $memory.Dispose() }
}

$expectedInventorySha256 = '6FB3164026316D0E9D7D2C1FD62CA0F411A4E66EFFFED67911797A80ACAF4F11'
$expectedWorkerSha256 = '665D0307D0BB3F411A45E251B10F82718A1F889E28E5D8457313734AA012BCFB'
$expectedHyperVManifestSha256 = 'C5F845C5BA7A4960D876DB46D1E63905CD9AC8EFEF650CB3EC9DBBD8B713F4CC'
$expectedHyperVBinarySha256 = '0FD7A8B2CB8C15E9DA691726C1F1F71F3EC4F853837FD099BA3F9EB2E9399316'
$inventoryScript = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'probe-existing-tbound-guest.ps1'))
$workerScript = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'run-reviewed-hyperv-inventory-worker.ps1'))
foreach ($sourcePath in @($inventoryScript, $workerScript)) {
    $sourceItem = Get-Item -LiteralPath $sourcePath -Force
    if (($sourceItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Pinned source is a reparse point: $sourcePath" }
}
$inventoryBytes = [IO.File]::ReadAllBytes($inventoryScript)
$workerBytes = [IO.File]::ReadAllBytes($workerScript)
$actualInventorySha256 = Get-Sha256Bytes $inventoryBytes
$actualWorkerSha256 = Get-Sha256Bytes $workerBytes
if ($actualInventorySha256 -cne $expectedInventorySha256) { throw 'Read-only inventory bytes differ from the reviewed literal; refusing elevation.' }
if ($actualWorkerSha256 -cne $expectedWorkerSha256) { throw 'Worker bytes differ from the reviewed literal; refusing elevation.' }

$windowsDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
$canonicalSystemDirectory = Join-Path $windowsDirectory 'System32'
$canonicalPowerShellPath = Join-Path $canonicalSystemDirectory 'WindowsPowerShell\v1.0\powershell.exe'
$launchPowerShellPath = $canonicalPowerShellPath
if ([Environment]::Is64BitOperatingSystem -and -not [Environment]::Is64BitProcess) {
    $launchPowerShellPath = Join-Path $windowsDirectory 'Sysnative\WindowsPowerShell\v1.0\powershell.exe'
}
$hyperVModuleDirectory = Join-Path $canonicalSystemDirectory 'WindowsPowerShell\v1.0\Modules\Hyper-V\2.0.0.0'
$hyperVManifestPath = Join-Path $hyperVModuleDirectory 'Hyper-V.psd1'
$trustedSystemModulePath = Join-Path $canonicalSystemDirectory 'WindowsPowerShell\v1.0\Modules'
$hyperVBinaryPath = Join-Path $windowsDirectory 'Microsoft.Net\assembly\GAC_MSIL\Microsoft.HyperV.PowerShell.Cmdlets\v4.0_10.0.0.0__31bf3856ad364e35\Microsoft.HyperV.PowerShell.Cmdlets.dll'
foreach ($trustedPath in @($canonicalPowerShellPath, $hyperVManifestPath, $hyperVBinaryPath)) {
    if (-not [IO.File]::Exists($trustedPath)) { throw "Pinned trusted system file is missing: $trustedPath" }
    $trustedItem = Get-Item -LiteralPath $trustedPath -Force
    if (($trustedItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Pinned trusted system file is a reparse point: $trustedPath" }
}
if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($hyperVManifestPath))) -cne $expectedHyperVManifestSha256) { throw 'Installed Hyper-V manifest differs from its reviewed system pin.' }
if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($hyperVBinaryPath))) -cne $expectedHyperVBinarySha256) { throw 'Installed Hyper-V binary differs from its reviewed system pin.' }

$desktop = [IO.Path]::GetFullPath([Environment]::GetFolderPath([Environment+SpecialFolder]::DesktopDirectory)).TrimEnd('\')
$runId = 'tbound-hyperv-inventory-{0}-{1}' -f [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ'), ([guid]::NewGuid().ToString('N').Substring(0, 12))
$runDirectory = Join-Path $desktop $runId
if ([IO.Directory]::Exists($runDirectory) -or [IO.File]::Exists($runDirectory)) { throw 'Generated run directory already exists; refusing reuse.' }
[void][IO.Directory]::CreateDirectory($runDirectory)
$runItem = Get-Item -LiteralPath $runDirectory -Force
if (($runItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Created run directory unexpectedly resolved as a reparse point.' }

$workerSnapshotPath = Join-Path $runDirectory 'worker.snapshot.ps1'
$inventorySnapshotPath = Join-Path $runDirectory 'inventory.snapshot.ps1'
Write-NewBytesFile -Path $workerSnapshotPath -Bytes $workerBytes
Write-NewBytesFile -Path $inventorySnapshotPath -Bytes $inventoryBytes
$lockedStreams = [Collections.Generic.List[IO.FileStream]]::new()
$workerLock = [IO.File]::Open($workerSnapshotPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
$lockedStreams.Add($workerLock)
$inventoryLock = [IO.File]::Open($inventorySnapshotPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
$lockedStreams.Add($inventoryLock)
if ((Get-Sha256Bytes (Read-LockedBytes $workerLock)) -cne $expectedWorkerSha256) { throw 'Locked worker snapshot differs from the source byte pin.' }
if ((Get-Sha256Bytes (Read-LockedBytes $inventoryLock)) -cne $expectedInventorySha256) { throw 'Locked inventory snapshot differs from the source byte pin.' }

$bootstrapTemplate = @'
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
function Decode-Base64Utf8([string]$Value) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Value)) }
function Get-Sha256Bytes([byte[]]$Bytes) {
  $sha = [Security.Cryptography.SHA256]::Create()
  try { ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '').ToUpperInvariant() }
  finally { $sha.Dispose() }
}
function Write-NewUtf8File([string]$Path, [string]$Text) {
  $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
  try {
    $writer = [IO.StreamWriter]::new($stream, [Text.UTF8Encoding]::new($false))
    try { $writer.Write($Text) } finally { $writer.Dispose() }
  } finally { if ($stream) { $stream.Dispose() } }
}
function Decode-ScriptBytes([byte[]]$Bytes) {
  if ($Bytes.Length -ge 3 -and $Bytes[0] -eq 0xEF -and $Bytes[1] -eq 0xBB -and $Bytes[2] -eq 0xBF) {
    $Bytes = $Bytes[3..($Bytes.Length - 1)]
  }
  [Text.Encoding]::UTF8.GetString($Bytes)
}
$runId = Decode-Base64Utf8 '__RUN_ID__'
$runDirectory = Decode-Base64Utf8 '__RUN_DIRECTORY__'
$workerPath = Decode-Base64Utf8 '__WORKER_SNAPSHOT__'
$inventoryPath = Decode-Base64Utf8 '__INVENTORY_SNAPSHOT__'
$expectedWorkerHash = Decode-Base64Utf8 '__WORKER_HASH__'
$expectedInventoryHash = Decode-Base64Utf8 '__INVENTORY_HASH__'
$canonicalPowerShellPath = Decode-Base64Utf8 '__POWERSHELL_PATH__'
$systemModulePath = Decode-Base64Utf8 '__SYSTEM_MODULE_PATH__'
$hyperVModuleDirectory = Decode-Base64Utf8 '__HYPERV_MODULE_DIRECTORY__'
$hyperVManifestPath = Decode-Base64Utf8 '__HYPERV_MANIFEST_PATH__'
$expectedHyperVManifestHash = Decode-Base64Utf8 '__HYPERV_MANIFEST_HASH__'
$hyperVBinaryPath = Decode-Base64Utf8 '__HYPERV_BINARY_PATH__'
$expectedHyperVBinaryHash = Decode-Base64Utf8 '__HYPERV_BINARY_HASH__'
$bootstrapMarkerPath = Join-Path $runDirectory 'bootstrap.json'
$bootstrapErrorPath = Join-Path $runDirectory 'bootstrap-error.txt'
$exitCode = 2
try {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = [Security.Principal.WindowsPrincipal]::new($identity)
  $elevated = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  if (-not $elevated) { throw 'Elevated bootstrap token validation failed.' }
  $currentPowerShellPath = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
  if ($currentPowerShellPath -ine $canonicalPowerShellPath) { throw 'Bootstrap is not running from canonical System32 PowerShell.' }
  if ([IO.Path]::GetFileName($workerPath) -cne 'worker.snapshot.ps1' -or [IO.Path]::GetFileName($inventoryPath) -cne 'inventory.snapshot.ps1') { throw 'Snapshot names are not the fixed task-specific names.' }
  if ([IO.Path]::GetFullPath([IO.Path]::GetDirectoryName($workerPath)) -ine [IO.Path]::GetFullPath($runDirectory) -or [IO.Path]::GetFullPath([IO.Path]::GetDirectoryName($inventoryPath)) -ine [IO.Path]::GetFullPath($runDirectory)) { throw 'Snapshots escaped the exact per-run directory.' }
  $workerBytes = [IO.File]::ReadAllBytes($workerPath)
  $inventoryBytes = [IO.File]::ReadAllBytes($inventoryPath)
  $actualWorkerHash = Get-Sha256Bytes $workerBytes
  $actualInventoryHash = Get-Sha256Bytes $inventoryBytes
  if ($actualWorkerHash -cne $expectedWorkerHash -or $actualInventoryHash -cne $expectedInventoryHash) { throw 'Elevated source snapshot hash mismatch; refusing ScriptBlock creation.' }

  $env:PSModulePath = $hyperVModuleDirectory + ';' + $systemModulePath
  if ($env:PSModulePath -cne ($hyperVModuleDirectory + ';' + $systemModulePath)) { throw 'Ambient PSModulePath was not replaced by the pinned system module paths.' }
  if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($hyperVManifestPath))) -cne $expectedHyperVManifestHash) { throw 'Pinned Hyper-V manifest hash mismatch.' }
  if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($hyperVBinaryPath))) -cne $expectedHyperVBinaryHash) { throw 'Pinned Hyper-V binary hash mismatch.' }
  Import-Module -Name $hyperVManifestPath -Force -ErrorAction Stop | Out-Null
  $loadedHyperV = @(Get-Module -Name Hyper-V)
  if ($loadedHyperV.Count -ne 1 -or $loadedHyperV[0].Version.ToString() -cne '2.0.0.0' -or $loadedHyperV[0].Path -ine $hyperVBinaryPath) { throw 'Loaded Hyper-V module differs from the pinned path/version.' }

  $workerBlock = [ScriptBlock]::Create((Decode-ScriptBytes $workerBytes))
  $inventoryBlock = [ScriptBlock]::Create((Decode-ScriptBytes $inventoryBytes))
  $bootstrapRecord = [ordered]@{
    schema = 'tbound-hyperv-elevation-bootstrap/v1'
    status = 'verified-snapshots-and-module-before-scriptblock-invocation'
    runId = $runId
    capturedAt = [DateTimeOffset]::Now.ToString('o')
    identityName = [string]$identity.Name
    elevatedTokenVerified = [bool]$elevated
    canonicalPowerShellPath = $canonicalPowerShellPath
    observedPowerShellPath = $currentPowerShellPath
    ambientModulePathReplaced = $true
    effectiveModulePath = $env:PSModulePath
    workerSnapshotPath = $workerPath
    expectedWorkerSha256 = $expectedWorkerHash
    actualWorkerSha256 = $actualWorkerHash
    inventorySnapshotPath = $inventoryPath
    expectedInventorySha256 = $expectedInventoryHash
    actualInventorySha256 = $actualInventoryHash
    workerScriptBlockCreatedAfterHashVerification = $true
    inventoryScriptBlockCreatedAfterHashVerification = $true
    hyperVManifestPath = $hyperVManifestPath
    hyperVManifestSha256 = $expectedHyperVManifestHash
    hyperVBinaryPath = $hyperVBinaryPath
    hyperVBinarySha256 = $expectedHyperVBinaryHash
    hyperVVersion = $loadedHyperV[0].Version.ToString()
  }
  Write-NewUtf8File $bootstrapMarkerPath (($bootstrapRecord | ConvertTo-Json -Depth 6) + [Environment]::NewLine)
  $exitCode = [int](& $workerBlock -RunId $runId -RunDirectory $runDirectory -InventoryBlock $inventoryBlock -ExpectedWorkerSha256 $expectedWorkerHash -ExpectedInventorySha256 $expectedInventoryHash -CanonicalPowerShellPath $canonicalPowerShellPath -HyperVManifestPath $hyperVManifestPath -HyperVManifestSha256 $expectedHyperVManifestHash -HyperVBinaryPath $hyperVBinaryPath -HyperVBinarySha256 $expectedHyperVBinaryHash)
}
catch {
  $failure = $_.Exception.ToString()
  try { if (-not [IO.File]::Exists($bootstrapErrorPath)) { Write-NewUtf8File $bootstrapErrorPath ($failure + [Environment]::NewLine) } } catch { }
  [Console]::Error.WriteLine($failure)
  $exitCode = 2
}
exit $exitCode
'@
$bootstrapValues = @{
    '__RUN_ID__' = ConvertTo-Base64Utf8 $runId
    '__RUN_DIRECTORY__' = ConvertTo-Base64Utf8 $runDirectory
    '__WORKER_SNAPSHOT__' = ConvertTo-Base64Utf8 $workerSnapshotPath
    '__INVENTORY_SNAPSHOT__' = ConvertTo-Base64Utf8 $inventorySnapshotPath
    '__WORKER_HASH__' = ConvertTo-Base64Utf8 $expectedWorkerSha256
    '__INVENTORY_HASH__' = ConvertTo-Base64Utf8 $expectedInventorySha256
    '__POWERSHELL_PATH__' = ConvertTo-Base64Utf8 $canonicalPowerShellPath
    '__SYSTEM_MODULE_PATH__' = ConvertTo-Base64Utf8 $trustedSystemModulePath
    '__HYPERV_MODULE_DIRECTORY__' = ConvertTo-Base64Utf8 $hyperVModuleDirectory
    '__HYPERV_MANIFEST_PATH__' = ConvertTo-Base64Utf8 $hyperVManifestPath
    '__HYPERV_MANIFEST_HASH__' = ConvertTo-Base64Utf8 $expectedHyperVManifestSha256
    '__HYPERV_BINARY_PATH__' = ConvertTo-Base64Utf8 $hyperVBinaryPath
    '__HYPERV_BINARY_HASH__' = ConvertTo-Base64Utf8 $expectedHyperVBinarySha256
}
foreach ($key in $bootstrapValues.Keys) { $bootstrapTemplate = $bootstrapTemplate.Replace($key, $bootstrapValues[$key]) }
$bootstrapCommand = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($bootstrapTemplate))
$bootstrapCommandSha256 = Get-Sha256Bytes ([Text.Encoding]::Unicode.GetBytes($bootstrapTemplate))
$argumentString = '-NoProfile -NonInteractive -EncodedCommand ' + $bootstrapCommand

$launchPath = Join-Path $runDirectory 'launch.json'
$resultPath = Join-Path $runDirectory 'result.json'
$completionPath = Join-Path $runDirectory 'completion.json'
$bootstrapMarkerPath = Join-Path $runDirectory 'bootstrap.json'
$inventoryOutputPath = Join-Path $runDirectory 'inventory.json'
$launcherIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
$launcherPrincipal = [Security.Principal.WindowsPrincipal]::new($launcherIdentity)
$launchRecord = [ordered]@{
    schema = 'tbound-hyperv-elevation-launch/v2'
    status = 'elevation-requested'
    startedAt = [DateTimeOffset]::Now.ToString('o')
    launcherTokenElevated = [bool]$launcherPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    launcherIdentity = [string]$launcherIdentity.Name
    runId = $runId
    outputDirectory = $runDirectory
    canonicalPowerShellPath = $canonicalPowerShellPath
    launchPowerShellPath = $launchPowerShellPath
    childArguments = @('-NoProfile', '-NonInteractive', '-EncodedCommand', $bootstrapCommand)
    bootstrapCommandSha256 = $bootstrapCommandSha256
    workerSnapshot = $workerSnapshotPath
    inventorySnapshot = $inventorySnapshotPath
    workerSha256 = $actualWorkerSha256
    inventoryScriptSha256 = $actualInventorySha256
    hyperVManifestPath = $hyperVManifestPath
    hyperVManifestSha256 = $expectedHyperVManifestSha256
    hyperVBinaryPath = $hyperVBinaryPath
    hyperVBinarySha256 = $expectedHyperVBinarySha256
    elevationVerb = 'RunAs'
    windowStyle = 'Hidden'
}
Write-NewUtf8File -Path $launchPath -Text (($launchRecord | ConvertTo-Json -Depth 8) + [Environment]::NewLine)

$status = 'uac-denied-or-launch-failed'
$launchError = $null
$processId = $null
$childExitCode = $null
$waitCompleted = $false
$validatedBootstrap = $false
$validatedWorkerResult = $false
$bootstrapStatus = $null
$workerStatus = $null
$workerReportSha256 = $null
$lockedStreamsReleasedAfterExit = $false
$child = $null
try {
    $child = Start-Process -FilePath $launchPowerShellPath -ArgumentList $argumentString -Verb RunAs -WindowStyle Hidden -PassThru
    $processId = $child.Id
    $waitCompleted = $child.WaitForExit(45000)
    if (-not $waitCompleted) {
        $status = 'child-timeout-terminated'
        try { $child.Kill(); [void]$child.WaitForExit(5000) } catch { $launchError = "Task-scoped child termination failed: $($_.Exception.Message)" }
        $waitCompleted = $child.HasExited
        if ($waitCompleted) { $childExitCode = $child.ExitCode }
    }
    else {
        $childExitCode = $child.ExitCode
        if ([IO.File]::Exists($bootstrapMarkerPath)) {
            $bootstrapResult = Get-Content -LiteralPath $bootstrapMarkerPath -Raw | ConvertFrom-Json -ErrorAction Stop
            $bootstrapStatus = [string]$bootstrapResult.status
            $validatedBootstrap = (
                $bootstrapResult.schema -ceq 'tbound-hyperv-elevation-bootstrap/v1' -and
                $bootstrapResult.status -ceq 'verified-snapshots-and-module-before-scriptblock-invocation' -and
                $bootstrapResult.runId -ceq $runId -and
                $bootstrapResult.elevatedTokenVerified -eq $true -and
                $bootstrapResult.canonicalPowerShellPath -ieq $canonicalPowerShellPath -and
                $bootstrapResult.observedPowerShellPath -ieq $canonicalPowerShellPath -and
                $bootstrapResult.ambientModulePathReplaced -eq $true -and
                $bootstrapResult.workerScriptBlockCreatedAfterHashVerification -eq $true -and
                $bootstrapResult.inventoryScriptBlockCreatedAfterHashVerification -eq $true -and
                $bootstrapResult.expectedWorkerSha256 -ceq $expectedWorkerSha256 -and
                $bootstrapResult.actualWorkerSha256 -ceq $expectedWorkerSha256 -and
                $bootstrapResult.expectedInventorySha256 -ceq $expectedInventorySha256 -and
                $bootstrapResult.actualInventorySha256 -ceq $expectedInventorySha256 -and
                $bootstrapResult.hyperVManifestPath -ieq $hyperVManifestPath -and
                $bootstrapResult.hyperVManifestSha256 -ceq $expectedHyperVManifestSha256 -and
                $bootstrapResult.hyperVBinaryPath -ieq $hyperVBinaryPath -and
                $bootstrapResult.hyperVBinarySha256 -ceq $expectedHyperVBinarySha256 -and
                $bootstrapResult.hyperVVersion -ceq '2.0.0.0'
            )
        }
        if ([IO.File]::Exists($completionPath)) {
            $workerResult = Get-Content -LiteralPath $completionPath -Raw | ConvertFrom-Json -ErrorAction Stop
            $workerStatus = [string]$workerResult.status
            $workerReportSha256 = [string]$workerResult.inventoryOutputSha256
            $reportMatches = $false
            if ([IO.File]::Exists($inventoryOutputPath) -and $workerReportSha256 -match '^[0-9A-F]{64}$') {
                $actualReportHash = Get-Sha256Bytes ([IO.File]::ReadAllBytes($inventoryOutputPath))
                $reportMatches = $actualReportHash -ceq $workerReportSha256
                if ($reportMatches) {
                    $reportObject = Get-Content -LiteralPath $inventoryOutputPath -Raw | ConvertFrom-Json -ErrorAction Stop
                    $reportMatches = ($reportObject.schema -ceq 'tbound-hyperv-readonly-inventory/v1' -and $reportObject.vm.name -ceq 'TBound-Ubuntu-2404' -and $reportObject.guestKernelAndLandlockProbe -like 'NOT RUN:*')
                }
            }
            $validatedWorkerResult = (
                $workerResult.schema -ceq 'tbound-hyperv-elevated-worker-result/v2' -and
                $workerResult.runId -ceq $runId -and
                $workerResult.elevatedTokenVerified -eq $true -and
                $workerResult.workerExitCode -eq 0 -and
                $workerResult.snapshotBytesVerifiedBeforeScriptBlockInvocation -eq $true -and
                $workerResult.expectedWorkerSha256 -ceq $expectedWorkerSha256 -and
                $workerResult.expectedInventorySha256 -ceq $expectedInventorySha256 -and
                $workerResult.observedPowerShellPath -ieq $canonicalPowerShellPath -and
                $workerResult.hyperVManifestPath -ieq $hyperVManifestPath -and
                $workerResult.hyperVManifestSha256 -ceq $expectedHyperVManifestSha256 -and
                $workerResult.hyperVBinaryPath -ieq $hyperVBinaryPath -and
                $workerResult.hyperVBinarySha256 -ceq $expectedHyperVBinarySha256 -and
                $workerStatus -ceq 'completed-read-only-inventory' -and
                $reportMatches
            )
        }
        if ($validatedBootstrap -and $validatedWorkerResult -and $childExitCode -eq 0) { $status = 'verified-read-only-inventory-success' }
        elseif (-not [IO.File]::Exists($bootstrapMarkerPath)) { $status = 'child-exited-without-verified-bootstrap' }
        else { $status = 'child-result-failed-validation' }
    }
}
catch {
    $launchError = $_.Exception.Message
    $status = 'uac-denied-or-launch-failed'
}
finally {
    if ($child -and -not $child.HasExited -and $status -eq 'child-timeout-terminated') {
        try { $child.Kill(); [void]$child.WaitForExit(5000) } catch { }
    }
    foreach ($stream in $lockedStreams) { $stream.Dispose() }
    $lockedStreamsReleasedAfterExit = $null -eq $child -or $child.HasExited
}

$result = [ordered]@{
    schema = 'tbound-hyperv-elevation-launch-result/v2'
    status = $status
    finishedAt = [DateTimeOffset]::Now.ToString('o')
    runId = $runId
    outputDirectory = $runDirectory
    inventoryOutput = if ([IO.File]::Exists($inventoryOutputPath)) { $inventoryOutputPath } else { $null }
    bootstrapMarker = if ([IO.File]::Exists($bootstrapMarkerPath)) { $bootstrapMarkerPath } else { $null }
    completionMarker = if ([IO.File]::Exists($completionPath)) { $completionPath } else { $null }
    launchMarker = $launchPath
    childProcessId = $processId
    childWaitCompleted = $waitCompleted
    childExitCode = $childExitCode
    bootstrapStatus = $bootstrapStatus
    workerStatus = $workerStatus
    workerReportSha256 = $workerReportSha256
    bootstrapResultValidated = $validatedBootstrap
    workerResultValidated = $validatedWorkerResult
    snapshotLocksReleasedAfterChildExit = $lockedStreamsReleasedAfterExit
    launchError = $launchError
}
Write-NewUtf8File -Path $resultPath -Text (($result | ConvertTo-Json -Depth 8) + [Environment]::NewLine)
Write-Output ($result | ConvertTo-Json -Depth 8)
if ($status -ne 'verified-read-only-inventory-success') { exit 2 }
exit 0
