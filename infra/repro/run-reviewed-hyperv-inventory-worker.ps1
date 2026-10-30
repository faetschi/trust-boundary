#requires -Version 5.1
<#
.SYNOPSIS
  Fixed-purpose worker invoked only after the encoded bootstrap verified its bytes.
.DESCRIPTION
  It accepts one verified inventory ScriptBlock, runs the fixed inventory for
  TBound-Ubuntu-2404, and writes a report and completion marker in a fresh run
  directory. It has no VM-name, command, or output-root input.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^tbound-hyperv-inventory-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}$')]
    [string]$RunId,
    [Parameter(Mandatory)]
    [string]$RunDirectory,
    [Parameter(Mandatory)]
    [ScriptBlock]$InventoryBlock,
    [Parameter(Mandatory)]
    [ValidatePattern('^[0-9A-F]{64}$')]
    [string]$ExpectedWorkerSha256,
    [Parameter(Mandatory)]
    [ValidatePattern('^[0-9A-F]{64}$')]
    [string]$ExpectedInventorySha256,
    [Parameter(Mandatory)]
    [string]$CanonicalPowerShellPath,
    [Parameter(Mandatory)]
    [string]$HyperVManifestPath,
    [Parameter(Mandatory)]
    [ValidatePattern('^[0-9A-F]{64}$')]
    [string]$HyperVManifestSha256,
    [Parameter(Mandatory)]
    [string]$HyperVBinaryPath,
    [Parameter(Mandatory)]
    [ValidatePattern('^[0-9A-F]{64}$')]
    [string]$HyperVBinarySha256
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

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

function Get-Sha256Bytes {
    param([Parameter(Mandatory)][byte[]]$Bytes)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '').ToUpperInvariant() }
    finally { $sha.Dispose() }
}

$desktop = [IO.Path]::GetFullPath([Environment]::GetFolderPath([Environment+SpecialFolder]::DesktopDirectory)).TrimEnd('\')
$expectedRunDirectory = [IO.Path]::GetFullPath((Join-Path $desktop $RunId))
$completionPath = Join-Path $expectedRunDirectory 'completion.json'
$inventoryOutputPath = Join-Path $expectedRunDirectory 'inventory.json'
$failureOutputPath = Join-Path $expectedRunDirectory 'worker-error.txt'
$inventoryVmName = 'TBound-Ubuntu-2404'
$startedAt = [DateTimeOffset]::Now.ToString('o')
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
$isElevated = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$reportSha256 = $null
$failureText = $null
$resultStatus = 'refused-or-failed'
$workerExitCode = 2

try {
    if (-not $isElevated) { throw 'Elevated worker token validation failed.' }
    if ($expectedRunDirectory -ine [IO.Path]::GetFullPath($RunDirectory)) { throw 'Output directory is not the fixed direct Desktop child.' }
    if (-not [IO.Directory]::Exists($expectedRunDirectory)) { throw 'The fixed per-run directory does not exist.' }
    $runItem = Get-Item -LiteralPath $expectedRunDirectory -Force
    if (($runItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'The per-run output directory must not be a reparse point.' }
    if ([IO.File]::Exists($completionPath) -or [IO.File]::Exists($inventoryOutputPath) -or [IO.File]::Exists($failureOutputPath)) {
        throw 'A worker output already exists; refusing overwrite or reuse.'
    }

    $currentPowerShellPath = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
    if ($currentPowerShellPath -ine $CanonicalPowerShellPath) { throw 'The worker is not running from canonical System32 PowerShell.' }
    $loadedModule = @(Get-Module -Name Hyper-V)
    if ($loadedModule.Count -ne 1 -or $loadedModule[0].Version.ToString() -cne '2.0.0.0' -or $loadedModule[0].Path -ine $HyperVBinaryPath) {
        throw 'The loaded Hyper-V module differs from the bootstrap-pinned binary.'
    }
    if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($HyperVManifestPath))) -cne $HyperVManifestSha256) { throw 'Hyper-V manifest hash changed during the run.' }
    if ((Get-Sha256Bytes ([IO.File]::ReadAllBytes($HyperVBinaryPath))) -cne $HyperVBinarySha256) { throw 'Hyper-V module binary hash changed during the run.' }

    $rawReport = & $InventoryBlock 2>&1
    $reportText = @($rawReport | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
    $reportObject = $reportText | ConvertFrom-Json -ErrorAction Stop
    if ($reportObject.schema -cne 'tbound-hyperv-readonly-inventory/v1' -or $reportObject.vm.name -cne $inventoryVmName) {
        throw 'Inventory output schema or fixed VM identity did not match.'
    }
    if ($reportObject.guestKernelAndLandlockProbe -notlike 'NOT RUN:*') {
        throw 'Host inventory must leave the separate guest ABI probe marked NOT RUN.'
    }
    Write-NewUtf8File -Path $inventoryOutputPath -Text ($reportText + [Environment]::NewLine)
    $reportSha256 = Get-Sha256Bytes ([IO.File]::ReadAllBytes($inventoryOutputPath))
    $resultStatus = 'completed-read-only-inventory'
    $workerExitCode = 0
}
catch {
    $failureText = $_.Exception.Message
    if ([IO.Directory]::Exists($expectedRunDirectory) -and -not [IO.File]::Exists($failureOutputPath)) {
        try { Write-NewUtf8File -Path $failureOutputPath -Text ($failureText + [Environment]::NewLine) } catch { }
    }
}

$completion = [ordered]@{
    schema = 'tbound-hyperv-elevated-worker-result/v2'
    status = $resultStatus
    workerExitCode = $workerExitCode
    elevatedTokenVerified = [bool]$isElevated
    identityName = [string]$identity.Name
    runId = $RunId
    capturedAt = [DateTimeOffset]::Now.ToString('o')
    inventoryVmName = $inventoryVmName
    inventoryArguments = @()
    expectedWorkerSha256 = $ExpectedWorkerSha256
    expectedInventorySha256 = $ExpectedInventorySha256
    snapshotBytesVerifiedBeforeScriptBlockInvocation = $true
    canonicalPowerShellPath = $CanonicalPowerShellPath
    observedPowerShellPath = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
    hyperVManifestPath = $HyperVManifestPath
    hyperVManifestSha256 = $HyperVManifestSha256
    hyperVBinaryPath = $HyperVBinaryPath
    hyperVBinarySha256 = $HyperVBinarySha256
    inventoryOutput = if ($reportSha256) { $inventoryOutputPath } else { $null }
    inventoryOutputSha256 = $reportSha256
    failure = $failureText
    startedAt = $startedAt
}

try {
    Write-NewUtf8File -Path $completionPath -Text (($completion | ConvertTo-Json -Depth 8) + [Environment]::NewLine)
}
catch {
    [Console]::Error.WriteLine("Could not create completion marker: $($_.Exception.Message)")
    return 2
}

return $workerExitCode
