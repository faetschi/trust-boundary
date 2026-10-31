#requires -Version 5.1
#requires -RunAsAdministrator
<#
.SYNOPSIS
    Creates the tbound evaluation Hyper-V VM from a cloud-layout disk
    or a golden VHDX, enables SSH key authentication offline, and runs the same
    guest bootstrap as the native Linux path inside the guest.

.DESCRIPTION
    Reproducibility infrastructure for the thesis evaluation profile (see
    docs/guides/setup.md). Not production hardening; establishes no
    containment claim.

    Flow:
      1. Preflight (Hyper-V, elevation, collisions, free space, image hash, SSH key).
      2. Image analysis:
          - CloudVhdx: compatibility mode for cloud-layout inputs supported by
            their publisher on local Hyper-V. Ubuntu Azure VHDs are unsupported
            outside Azure; this mode does not establish image compatibility.
         - GoldenVhdx: accept a golden .vhdx, or a checkpoint-chain leaf
           .avhdx (a base .vhdx under an active chain is stale state; the leaf
           is merged into a standalone VHDX before use).
      3. VM creation with current prototype settings: Generation 2, 8 vCPUs, 16 GiB
         fixed memory, 40 GiB disk, Secure Boot with the Microsoft UEFI
         Certificate Authority template, ProductionOnly checkpoints, one
         network adapter connected to the provisioning switch only. The disk
         is materialized here (merge/convert/copy/resize) after confirmation.
      4. Offline injection with the VM off, via `wsl --mount --vhd <path> --bare`
         (never combined with Mount-VHD on the same file): creates the trial
         user with SSH key auth and NOPASSWD sudo for provisioning, and for
         golden copies replaces installer Netplan files, regenerates SSH
         host keys, and regenerates the machine-id.
      5. Boot, resolve the guest IP (Hyper-V IP reporting where the guest has a
         KVP daemon, otherwise an ARP sweep; or -GuestIpAddress), wait for SSH.
      6. Copy infra/repro/bootstrap-linux.sh, infra/guest/setup-ubuntu-24.04.sh,
         and infra/guest/probe-capabilities.sh into the guest and run
         bootstrap-linux.sh over SSH (TBOUND_ASSUME_YES=1).

    After provisioning: disconnect the adapter, create the golden baseline, and
    follow infra/guest/README.md for the offline phase.

.EXAMPLE
    .\bootstrap-hyperv-windows.ps1 -ImageKind GoldenVhdx `
        -ImageSourcePath '<verified generic Ubuntu Server install VHDX>' `
        -ExpectedImageSha256 <64-hex> `
        -PublicKeyFile $env:USERPROFILE\.ssh\id_ed25519.pub `
        -PrivateKeyFile $env:USERPROFILE\.ssh\id_ed25519

.NOTES
    Exit behavior: throws on any failed precondition or step; no partial-state
    cleanup is attempted (inspect Hyper-V Manager and the target directory
    before retrying).
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [ValidateSet('CloudVhdx', 'GoldenVhdx')][string]$ImageKind = 'GoldenVhdx',
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$ImageSourcePath,
    [Parameter(Mandatory)][ValidatePattern('^[A-Fa-f0-9]{64}$')][string]$ExpectedImageSha256,
    [ValidateNotNullOrEmpty()][string]$PublicKeyFile,
    [ValidateNotNullOrEmpty()][string]$PrivateKeyFile,
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$')][string]$VmName = 'TBound-Ubuntu-2404',
    [ValidateNotNullOrEmpty()][string]$VmRoot,
    [ValidateNotNullOrEmpty()][string]$SwitchName = 'Default Switch',
    [ValidatePattern('^[a-z_][a-z0-9_-]{0,31}$')][string]$GuestUser = 'tboundadmin',
    [string]$GuestIpAddress,
    [ValidatePattern('^SHA256:[A-Za-z0-9+/]{43}$')][string]$GuestHostKeyFingerprint,
    [int]$SshPort = 22,
    [int]$BootTimeoutSeconds = 900,
    [int]$MinimumFreeGb = 120,
    [string]$WslDistro,
    [switch]$SkipWslInjection,
    [switch]$KeepSshHostKeys,
    [string]$RepoRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\workspace\paths.ps1')
$tboundLocalRoot = Get-TBoundLocalRoot
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }

$script:stepsDone = New-Object System.Collections.Generic.List[string]
$script:WindowsDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
$script:SystemDirectory = Join-Path $script:WindowsDirectory 'System32'
$script:OpenSshDirectory = Join-Path $script:SystemDirectory 'OpenSSH'
$script:TrustedWindowsOwner = 'NT SERVICE\TrustedInstaller'
$script:WslExePath = Join-Path $script:SystemDirectory 'wsl.exe'
$script:CmdExePath = Join-Path $script:SystemDirectory 'cmd.exe'
$script:SshExePath = Join-Path $script:OpenSshDirectory 'ssh.exe'
$script:SshKeyscanExePath = Join-Path $script:OpenSshDirectory 'ssh-keyscan.exe'
$script:SshKeygenExePath = Join-Path $script:OpenSshDirectory 'ssh-keygen.exe'
$script:ScpExePath = Join-Path $script:OpenSshDirectory 'scp.exe'
$script:ExpectedWindowsToolSha256 = @{
    'wsl.exe' = '4e589e3883229b7a74a4acdb878689dcec94e2539fcad1c194f415b149c337a9'
    'cmd.exe' = 'badf4752413cb0cbdc03fb95820ca167f0cdc63b597ccdb5ef43111180e088b0'
    'ssh.exe' = 'c9a80ae8af88aa0ff7a08a7e65dbad0d408854d757a433c6a78ab46d8d38aad1'
    'ssh-keyscan.exe' = 'bfb8caae4c31698c09686ae208dc204941bc185fa282f3973c4e0a44087cc08a'
    'ssh-keygen.exe' = '539ae392c61a53fadddf0bbb152126f3e0bc042e8a4ed50718ab16fbf872cbcc'
    'scp.exe' = '29328a55ab58bfcbfe0a7361fff2dc7a50fb28975e8e641b69e9a97ba1840fc8'
}

function Write-Step {
    param([Parameter(Mandatory)][string]$Message)
    Write-Host ''
    Write-Host "== $Message ==" -ForegroundColor Cyan
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
    foreach ($component in ($relativePath -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($component)) { continue }
        $currentPath = Join-Path $currentPath $component
        if (-not (Test-Path -LiteralPath $currentPath)) { break }
        $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Storage path contains a reparse point, junction, or symbolic link: $currentPath"
        }
    }
}

function Assert-TrustedWindowsExecutable {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedSha256,
        [Parameter(Mandatory)][string[]]$TrustedDirectories
    )
    $fullPath = [IO.Path]::GetFullPath($Path)
    Assert-NoReparsePathComponents -Path $fullPath
    foreach ($directory in $TrustedDirectories) {
        $directoryItem = Get-Item -LiteralPath $directory -Force -ErrorAction Stop
        if (($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
            (Get-Acl -LiteralPath $directory -ErrorAction Stop).Owner -cne $script:TrustedWindowsOwner) {
            throw "Trusted Windows tool directory is a reparse point or is not owned by $($script:TrustedWindowsOwner): $directory"
        }
    }
    $item = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
        (Get-Acl -LiteralPath $fullPath -ErrorAction Stop).Owner -cne $script:TrustedWindowsOwner) {
        throw "Windows executable is not a regular file owned by $($script:TrustedWindowsOwner): $fullPath"
    }
    $actualSha256 = (Get-FileHash -LiteralPath $fullPath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
    if ($actualSha256 -cne $ExpectedSha256) { throw "Pinned Windows executable SHA-256 mismatch: $fullPath" }
    $signature = Get-AuthenticodeSignature -FilePath $fullPath -ErrorAction Stop
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid -or
        $signature.SignerCertificate.Subject -notmatch '^CN=Microsoft Windows, O=Microsoft Corporation,') {
        throw "Windows executable lacks a valid Microsoft Windows signature: $fullPath"
    }
    return $actualSha256
}

function Protect-CurrentUserOnlyFile {
    param([Parameter(Mandatory)][string]$Path)
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $acl = New-Object Security.AccessControl.FileSecurity
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true, $false)
    $rule = New-Object Security.AccessControl.FileSystemAccessRule(
        $sid,
        [Security.AccessControl.FileSystemRights]::FullControl,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $null = $acl.AddAccessRule($rule)
    Set-Acl -LiteralPath $Path -AclObject $acl -ErrorAction Stop
}

function ConvertTo-WslPath {
    param([Parameter(Mandatory)][string]$WindowsPath)
    $full = [IO.Path]::GetFullPath($WindowsPath)
    $match = [regex]::Match($full, '^([A-Za-z]):\\(.*)$')
    if (-not $match.Success) {
        throw "Cannot translate a Windows drive path for WSL: '$full'"
    }
    return '/mnt/' + $match.Groups[1].Value.ToLowerInvariant() + '/' + $match.Groups[2].Value.Replace('\', '/')
}

function Get-Sha256Hex {
    param([Parameter(Mandatory)][string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash
}

function Assert-ExpectedSha256 {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Expected
    )
    $actual = Get-Sha256Hex -Path $Path
    if ($actual -ine $Expected) {
        throw ("SHA-256 mismatch for '{0}'. Expected {1}, got {2}. No changes were made." -f $Path, $Expected, $actual)
    }
    Write-Host "SHA-256 verified: $Path"
}

function Get-ValidatedPublicKeys {
    param([Parameter(Mandatory)][string]$Path)
    $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path
    $lines = @(Get-Content -LiteralPath $resolved -ErrorAction Stop |
        Where-Object { $_ -and -not $_.StartsWith('#') } |
        ForEach-Object { $_.Trim() } |
        Where-Object { $_ })
    if ($lines.Count -lt 1 -or $lines.Count -gt 10) {
        throw "Public key file must contain 1-10 key lines: $resolved"
    }
    foreach ($line in $lines) {
        if ($line.Length -gt 2000) { throw "Public key line exceeds 2000 characters: $resolved" }
        if ($line -notmatch '^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-[A-Za-z0-9-]+|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com)\s+[A-Za-z0-9+/=]+(\s|$)') {
            throw "File does not look like an OpenSSH public key file: $resolved (line: $($line.Substring(0, [Math]::Min(40, $line.Length))))"
        }
    }
    return $lines
}

function Resolve-PrivateKeyInput {
    param([Parameter(Mandatory)][string]$Path)
    $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path
    Assert-NoReparsePathComponents -Path $resolved
    $item = Get-Item -LiteralPath $resolved -Force -ErrorAction Stop
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'PrivateKeyFile must be an existing regular file without reparse points.'
    }
    return $resolved
}

function ConvertTo-WindowsCommandLineArgument {
    param([Parameter(Mandatory)][string]$Value)
    # Windows file names cannot contain a double quote. This argument is a
    # file path and cannot end with a directory separator.
    return '"' + $Value + '"'
}

function Assert-PrivateKeyMatchesPublicKey {
    param(
        [Parameter(Mandatory)][string]$PrivateKeyPath,
        [Parameter(Mandatory)][string[]]$PublicKeyLines
    )
    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $script:SshKeygenExePath
    $startInfo.Arguments = '-y -P "" -f ' + (ConvertTo-WindowsCommandLineArgument -Value $PrivateKeyPath)
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) { throw 'ssh-keygen process could not be started.' }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $derivedPublicText = $stdoutTask.GetAwaiter().GetResult()
        $null = $stderrTask.GetAwaiter().GetResult()
        $exitCode = $process.ExitCode
    }
    finally { $process.Dispose() }
    $derivedPublicLines = @($derivedPublicText -split '\r?\n' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($exitCode -ne 0 -or $derivedPublicLines.Count -ne 1) {
        throw 'PrivateKeyFile could not be used to derive one public key; no VM changes were made.'
    }
    $derivedFields = ([string]$derivedPublicLines[0]).Trim() -split '\s+'
    if ($derivedFields.Count -lt 2) { throw 'PrivateKeyFile did not produce a valid public-key identity; no VM changes were made.' }
    foreach ($publicKeyLine in $PublicKeyLines) {
        $publicKeyFields = ([string]$publicKeyLine).Trim() -split '\s+'
        if ($publicKeyFields.Count -ge 2 -and
            $publicKeyFields[0] -ceq $derivedFields[0] -and
            $publicKeyFields[1] -ceq $derivedFields[1]) {
            return $true
        }
    }
    throw 'PrivateKeyFile does not match a supplied public key; no VM changes were made.'
}

function Invoke-WslCommand {
    param(
        [Parameter(Mandatory)][string[]]$WslArguments,
        [switch]$ThrowOnError
    )
    $allArgs = @()
    if ($WslDistro) { $allArgs += @('-d', $WslDistro) }
    $allArgs += $WslArguments
    $output = (& $script:WslExePath @allArgs 2>&1 | Out-String)
    $code = $LASTEXITCODE
    if ($ThrowOnError -and $code -ne 0) {
        throw ("WSL command failed (exit {0}): wsl {1}`n{2}" -f $code, ($allArgs -join ' '), ($output.Trim() -replace "`0", ''))
    }
    return [pscustomobject]@{ Code = $code; Output = ($output -replace "`0", '') }
}

# ---------------------------------------------------------------------------
# Step 1: preflight
# ---------------------------------------------------------------------------

Write-Step 'Step 1/6: preflight'
Write-Host 'This script creates one VM and new files under the target directory. It'
Write-Host 'does not modify the existing reference VM, download images, or delete anything.'

if (-not (Test-IsAdministrator)) {
    throw 'Run this setup script from an elevated PowerShell session.'
}

Import-Module Hyper-V -ErrorAction Stop
$null = Get-VMHost -ErrorAction Stop
$requiredCommands = @(
    'Get-VM', 'Get-VMSwitch', 'New-VM', 'Set-VM', 'Set-VMProcessor', 'Set-VMMemory',
    'New-VHD', 'Get-VHD', 'Convert-VHD', 'Resize-VHD', 'Merge-VHD', 'Add-VMHardDiskDrive',
    'Set-VMFirmware', 'Get-VMNetworkAdapter', 'Get-VMIntegrationService',
    'Enable-VMIntegrationService', 'Disable-VMIntegrationService'
)
foreach ($commandName in $requiredCommands) {
    if (-not (Get-Command -Name $commandName -ErrorAction SilentlyContinue)) {
        throw "Required Hyper-V cmdlet is unavailable: $commandName"
    }
}

if (-not $RepoRoot) {
    $RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}
$RepoRoot = [IO.Path]::GetFullPath($RepoRoot)
$bootstrapLinux = Join-Path $RepoRoot 'infra\repro\bootstrap-linux.sh'
$landlockProfileGate = Join-Path $RepoRoot 'infra\repro\landlock-profile-gate.sh'
$setupUbuntu = Join-Path $RepoRoot 'infra\guest\setup-ubuntu-24.04.sh'
$probeCapabilities = Join-Path $RepoRoot 'infra\guest\probe-capabilities.sh'
foreach ($file in @($bootstrapLinux, $landlockProfileGate, $setupUbuntu, $probeCapabilities)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) {
        throw "Required script not found: $file (run from a checkout containing infra/repro and infra/guest)"
    }
}

$toolHashes = @{}
if (-not $SkipWslInjection) {
    $toolHashes['wsl.exe'] = Assert-TrustedWindowsExecutable -Path $script:WslExePath -ExpectedSha256 $script:ExpectedWindowsToolSha256['wsl.exe'] -TrustedDirectories @($script:WindowsDirectory, $script:SystemDirectory)
}
$toolHashes['cmd.exe'] = Assert-TrustedWindowsExecutable -Path $script:CmdExePath -ExpectedSha256 $script:ExpectedWindowsToolSha256['cmd.exe'] -TrustedDirectories @($script:WindowsDirectory, $script:SystemDirectory)
foreach ($tool in @(
        @{ Name = 'ssh.exe'; Path = $script:SshExePath },
        @{ Name = 'ssh-keyscan.exe'; Path = $script:SshKeyscanExePath },
        @{ Name = 'ssh-keygen.exe'; Path = $script:SshKeygenExePath },
        @{ Name = 'scp.exe'; Path = $script:ScpExePath })) {
    $toolHashes[$tool.Name] = Assert-TrustedWindowsExecutable -Path $tool.Path -ExpectedSha256 $script:ExpectedWindowsToolSha256[$tool.Name] -TrustedDirectories @($script:WindowsDirectory, $script:SystemDirectory, $script:OpenSshDirectory)
}
$script:VerifiedWindowsToolHashes = $toolHashes
Write-Host 'Pinned Windows host tools verified (SHA-256):'
foreach ($toolName in @('cmd.exe', 'ssh.exe', 'ssh-keyscan.exe', 'ssh-keygen.exe', 'scp.exe')) {
    Write-Host ("  {0}: {1}" -f $toolName, $script:VerifiedWindowsToolHashes[$toolName])
}
if ($script:VerifiedWindowsToolHashes.ContainsKey('wsl.exe')) {
    Write-Host ("  wsl.exe: {0}" -f $script:VerifiedWindowsToolHashes['wsl.exe'])
}
$wslToolHashSummary = if ($script:VerifiedWindowsToolHashes.ContainsKey('wsl.exe')) {
    $script:VerifiedWindowsToolHashes['wsl.exe']
}
else {
    'not used because -SkipWslInjection was supplied'
}

$existingVms = @(Get-VM -ErrorAction Stop)
if (@($existingVms | Where-Object { $_.Name -eq $VmName }).Count -gt 0) {
    throw "A VM named '$VmName' already exists. No changes were made."
}

if (-not $VmRoot) {
    $VmRoot = Join-Path $tboundLocalRoot ("vm\" + $VmName)
}
$fullVmRoot = [IO.Path]::GetFullPath($VmRoot)
Assert-NoReparsePathComponents -Path $fullVmRoot
if ([IO.Path]::GetPathRoot($fullVmRoot).TrimEnd('\') -eq $fullVmRoot.TrimEnd('\')) {
    throw 'VmRoot cannot be a drive root.'
}
if (Test-Path -LiteralPath $fullVmRoot) {
    throw "VmRoot already exists: $fullVmRoot. No changes were made."
}
$diskDirectory = Join-Path $fullVmRoot 'Virtual Hard Disks'
$vhdPath = Join-Path $diskDirectory ($VmName + '.vhdx')

$driveLetter = ([IO.Path]::GetPathRoot($fullVmRoot)).TrimEnd('\').TrimEnd(':')
$drive = Get-PSDrive -Name $driveLetter -ErrorAction Stop
if ($null -eq $drive.Free -or $drive.Free -lt ($MinimumFreeGb * 1GB)) {
    throw ("Drive {0}: must have at least {1} GiB free; found {2:N1} GiB." -f $driveLetter, $MinimumFreeGb, ($drive.Free / 1GB))
}
$hostMemory = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
$availableHostMemoryBytes = [Int64]$hostMemory.FreePhysicalMemory * 1KB
if ($availableHostMemoryBytes -lt 16GB) {
    throw ("Host has {0:N1} GiB available physical memory; at least 16 GiB is required for the fixed VM startup allocation. No VM changes were made." -f ($availableHostMemoryBytes / 1GB))
}
Write-Host ([string]::Format([Globalization.CultureInfo]::InvariantCulture, 'Host available physical memory: {0:N1} GiB (fixed VM startup allocation: 16 GiB).', ($availableHostMemoryBytes / 1GB)))

$switchMatches = @(Get-VMSwitch | Where-Object { $_.Name -eq $SwitchName })
if ($switchMatches.Count -ne 1) {
    throw "Provisioning switch '$SwitchName' was not found exactly once. Available: $(@(Get-VMSwitch | ForEach-Object Name) -join ', ')"
}

$resolvedImage = (Resolve-Path -LiteralPath $ImageSourcePath -ErrorAction Stop).Path
Assert-NoReparsePathComponents -Path $resolvedImage
$imageItem = Get-Item -LiteralPath $resolvedImage -ErrorAction Stop
if ($imageItem.PSIsContainer) { throw "ImageSourcePath must be a file: $resolvedImage" }
Assert-ExpectedSha256 -Path $resolvedImage -Expected $ExpectedImageSha256
if ($ImageKind -eq 'CloudVhdx') {
    Write-Warning 'CloudVhdx requires a publisher-supported local Hyper-V input. Ubuntu Azure VHDs do not function outside Azure. The checksum check proves byte equality, not compatibility.'
}

$publicKeys = $null
$resolvedPublicKeyPath = $null
$resolvedPrivateKeyPath = $null
if ($SkipWslInjection) {
    if ($PublicKeyFile) { throw '-SkipWslInjection cannot be combined with -PublicKeyFile.' }
    if (-not $PrivateKeyFile) { throw '-PrivateKeyFile is required when -SkipWslInjection is used.' }
    Write-Warning '-SkipWslInjection: the image must already contain SSH access for the trial user.'
    $privateKeyCandidate = $PrivateKeyFile
}
else {
    if (-not $PublicKeyFile) { throw 'A -PublicKeyFile is required so the script can enable SSH key authentication (or pass -SkipWslInjection).' }
    if (-not $PrivateKeyFile) { throw '-PrivateKeyFile is required so the SSH identity is explicit before VM changes.' }
    $publicKeys = @(Get-ValidatedPublicKeys -Path $PublicKeyFile)
    $resolvedPublicKeyPath = (Resolve-Path -LiteralPath $PublicKeyFile -ErrorAction Stop).Path
    Assert-NoReparsePathComponents -Path $resolvedPublicKeyPath
    $publicKeyItem = Get-Item -LiteralPath $resolvedPublicKeyPath -Force -ErrorAction Stop
    if ($publicKeyItem.PSIsContainer -or ($publicKeyItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'PublicKeyFile must be a regular file without reparse points.'
    }
    $privateKeyCandidate = $PrivateKeyFile
}
$resolvedPrivateKeyPath = Resolve-PrivateKeyInput -Path $privateKeyCandidate
if ($publicKeys) {
    $null = Assert-PrivateKeyMatchesPublicKey -PrivateKeyPath $resolvedPrivateKeyPath -PublicKeyLines $publicKeys
    Write-Host 'SSH private key matches a supplied public key (comparison passed).'
    Write-Host ("SSH public key(s) validated from {0}: {1} key line(s)." -f $resolvedPublicKeyPath, $publicKeys.Count)
}

# ---------------------------------------------------------------------------
# Step 2: image analysis (no writes to the VM directory yet)
# ---------------------------------------------------------------------------

Write-Step 'Step 2/6: image analysis'
$profileDiskBytes = 40GB
$tempExtractDir = $null
$leafVhdx = $null

if ($ImageKind -eq 'CloudVhdx') {
    if ($resolvedImage -match '\.tar\.gz$') {
        $tempExtractDir = Join-Path ([IO.Path]::GetTempPath()) ('tbound-cloudimg-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $tempExtractDir -ErrorAction Stop | Out-Null
        Write-Host 'Extracting the cloud image archive (about 800 MB compressed)...'
        $tarOutput = & tar.exe -xzf $resolvedImage -C $tempExtractDir 2>&1
        if ($LASTEXITCODE -ne 0) { throw "tar extraction failed (exit $LASTEXITCODE): $($tarOutput | Out-String)" }
        $vhdFiles = @(Get-ChildItem -LiteralPath $tempExtractDir -Filter *.vhd -File -ErrorAction Stop)
        if ($vhdFiles.Count -ne 1) {
            throw "Expected exactly one .vhd file inside the archive; found $($vhdFiles.Count)."
        }
        $leafVhdx = $vhdFiles[0].FullName
    }
    elseif ($resolvedImage -match '\.vhd$') {
        $leafVhdx = $resolvedImage
    }
    elseif ($resolvedImage -match '\.vhdx$') {
        $leafVhdx = $resolvedImage
    }
    else {
        throw "CloudVhdx expects a .tar.gz cloud image archive, a .vhd, or a .vhdx file; got '$resolvedImage'."
    }
    $vhdInfo = Get-VHD -Path $leafVhdx -ErrorAction Stop
    Write-Host ("Cloud disk: format={0}, type={1}, size={2:N0} bytes." -f $vhdInfo.VhdFormat, $vhdInfo.VhdType, $vhdInfo.Size)
}
else {
    if ($resolvedImage -match '\.avhdx$') {
        Write-Host 'Source is a checkpoint leaf (.avhdx): the parent chain will be merged into a standalone VHDX (a base .vhdx under an active chain is stale state).'
        $leafVhdx = $resolvedImage
    }
    elseif ($resolvedImage -match '\.vhdx$') {
        $leafVhdx = $resolvedImage
    }
    else {
        throw "GoldenVhdx expects a .vhdx or a checkpoint-chain .avhdx file; got '$resolvedImage'."
    }
}

# ---------------------------------------------------------------------------
# Step 3: VM creation with prototype settings (confirmed via ShouldProcess)
# ---------------------------------------------------------------------------

Write-Step 'Step 3/6: VM creation (Generation 2, 8 vCPU, 16 GiB fixed memory, 40 GiB disk, Secure Boot)'
if (-not $PSCmdlet.ShouldProcess(
        "$VmName at $fullVmRoot",
        'Create the VM and disk, inject the trial account and SSH key offline, boot, then run the unattended Ubuntu guest bootstrap (packages, toolchains, sysctl and probe)')) {
    return
}

try {
    New-Item -ItemType Directory -Path $fullVmRoot -ErrorAction Stop | Out-Null
    New-Item -ItemType Directory -Path $diskDirectory -ErrorAction Stop | Out-Null

    if (Test-Path -LiteralPath $vhdPath) { throw "Target disk path already exists: $vhdPath" }

    if ($ImageKind -eq 'GoldenVhdx' -and $resolvedImage -match '\.avhdx$') {
        # Merge-VHD resolves the whole parent chain into a standalone disk.
        $null = Merge-VHD -Path $leafVhdx -DestinationPath $vhdPath -ErrorAction Stop
        Write-Host "Merged checkpoint chain into: $vhdPath"
    }
    elseif ($ImageKind -eq 'GoldenVhdx') {
        Copy-Item -LiteralPath $leafVhdx -Destination $vhdPath -ErrorAction Stop
        Write-Host "Golden disk copied: $vhdPath"
    }
    else {
        $vhdInfo = Get-VHD -Path $leafVhdx -ErrorAction Stop
        if ($vhdInfo.VhdFormat -ne 'VHDX') {
            Write-Host 'Converting VHD -> VHDX (Generation 2 VMs require the VHDX format)...'
            $null = Convert-VHD -Path $leafVhdx -DestinationPath $vhdPath -ErrorAction Stop
        }
        else {
            Copy-Item -LiteralPath $leafVhdx -Destination $vhdPath -ErrorAction Stop
        }
        $null = Resize-VHD -Path $vhdPath -SizeBytes $profileDiskBytes -ErrorAction Stop
    }

    $provisionedVhd = Get-VHD -Path $vhdPath -ErrorAction Stop
    Write-Host ("Provisioned disk: format={0}, type={1}, size={2:N0} bytes." -f $provisionedVhd.VhdFormat, $provisionedVhd.VhdType, $provisionedVhd.Size)
    if ($provisionedVhd.VhdType -eq 'Fixed') {
        Write-Host 'Note: the disk is fixed-size, so the host file occupies its full 40 GiB.'
    }

    $null = New-VM -Name $VmName -Generation 2 -MemoryStartupBytes 16GB -NoVHD -Path $fullVmRoot -ErrorAction Stop
    $null = Set-VMProcessor -VMName $VmName -Count 8 -ErrorAction Stop
    $null = Set-VMMemory -VMName $VmName -DynamicMemoryEnabled $false -StartupBytes 16GB -ErrorAction Stop
    $null = Set-VM -VMName $VmName -CheckpointType ProductionOnly -AutomaticCheckpointsEnabled $false -SnapshotFileLocation $fullVmRoot -ErrorAction Stop

    # Integration service policy: six known service IDs. Key-Value Pair
    # Exchange is enabled for the provisioning phase only (guest IP reporting)
    # and disabled again at the end of this script.
    $integrationServicePolicy = [ordered]@{
        '84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47' = @{ Label = 'Heartbeat'; Enabled = $true }
        '9F8233AC-BE49-4C79-8EE3-E7E1985B2077' = @{ Label = 'Shutdown'; Enabled = $true }
        '2497F4DE-E9FA-4204-80E4-4B75C46419C0' = @{ Label = 'Time Synchronization'; Enabled = $true }
        '6C09BB55-D683-4DA0-8931-C9BF705F6480' = @{ Label = 'Guest Service Interface'; Enabled = $false }
        '2A34B1C2-FD73-4043-8A5B-DD2159BC743F' = @{ Label = 'Key-Value Pair Exchange'; Enabled = $true }
        '5CED1297-4598-4915-A5FC-AD21BB4D02A4' = @{ Label = 'VSS'; Enabled = $false }
    }
    $services = @(Get-VMIntegrationService -VMName $VmName -ErrorAction Stop)
    $resolvedServices = @(
        foreach ($service in $services) {
            $guidMatch = [regex]::Match([string]$service.Id, '(?i)(?<ServiceId>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$')
            if (-not $guidMatch.Success) { throw "Integration service Id does not end in a GUID: $($service.Id)" }
            $serviceId = $guidMatch.Groups['ServiceId'].Value.ToUpperInvariant()
            if (-not $integrationServicePolicy.Contains($serviceId)) {
                throw "Unknown integration service Id '$serviceId' (display name '$($service.Name)'). No service changes were made."
            }
            [pscustomobject]@{ Id = $serviceId; Policy = $integrationServicePolicy[$serviceId]; Service = $service }
        }
    )
    if ($resolvedServices.Count -ne $integrationServicePolicy.Count) {
        throw ("Integration service inventory mismatch: expected {0} known services, found {1}." -f $integrationServicePolicy.Count, $resolvedServices.Count)
    }
    foreach ($entry in $resolvedServices) {
        if ([bool]$entry.Service.Enabled -ne [bool]$entry.Policy.Enabled) {
            if ($entry.Policy.Enabled) {
                $null = Enable-VMIntegrationService -VMIntegrationService $entry.Service -ErrorAction Stop
            }
            else {
                $null = Disable-VMIntegrationService -VMIntegrationService $entry.Service -ErrorAction Stop
            }
        }
    }
    Write-Host 'Integration service policy applied (KVP enabled for provisioning IP reporting; disabled again after provisioning).'

    $provisionedDiskDrive = Add-VMHardDiskDrive -VMName $VmName -Path $vhdPath -PassThru -ErrorAction Stop

    $null = Get-VMNetworkAdapter -VMName $VmName -ErrorAction Stop |
        Connect-VMNetworkAdapter -SwitchName $SwitchName -ErrorAction Stop
    Write-Host "Network adapter connected to provisioning switch '$SwitchName' (disconnect it again before untrusted runs)."

    $null = Set-VMFirmware -VMName $VmName -EnableSecureBoot On -SecureBootTemplate 'MicrosoftUEFICertificateAuthority' -FirstBootDevice $provisionedDiskDrive -ErrorAction Stop
    Write-Host 'Firmware: Secure Boot On, template MicrosoftUEFICertificateAuthority.'

    $script:stepsDone.Add('VM created with current prototype settings')
}
catch {
    Write-Warning "Setup stopped with a partial state possible for '$VmName'. No files or VM objects were removed. Inspect Hyper-V Manager and $fullVmRoot before retrying."
    throw
}

# ---------------------------------------------------------------------------
# Step 4: offline injection via WSL (VM is off)
# ---------------------------------------------------------------------------

$offlineGuestHostKeyFingerprint = $null
if (-not $SkipWslInjection) {
    Write-Step 'Step 4/6: offline SSH-key injection via WSL (VM is off)'
    if ((Get-VM -Name $VmName -ErrorAction Stop).State -ne 'Off') {
        throw "VM '$VmName' must be Off for the offline injection step."
    }

    $wslStatus = Invoke-WslCommand -WslArguments @('--status')
    if ($wslStatus.Code -ne 0) {
        throw "WSL is not usable on this host (wsl --status exited $($wslStatus.Code)). The offline injection requires WSL2 with 'wsl --mount'."
    }

    # Write the injection script to disk (LF-normalized) and run it as root in
    # the default (or selected) WSL distro. The script is fail-closed: it
    # identifies the freshly attached disk by name (block-device list diff),
    # the root filesystem by content (a bare-mounted azure image has two ext4
    # partitions: root and /boot), and grows the partition with the highest end
    # offset (the root partition is last in the 24.04 cloud-image layout).
    $listDevicesScript = @'
#!/usr/bin/env bash
ls -1 /sys/block | grep '^sd' || true
'@
    $injectScript = @'
#!/usr/bin/env bash
# TBound offline injection (runs as root inside WSL against a bare-mounted VHD).
# Fail-closed: every step verifies; a failure aborts the whole injection.
set -Eeuo pipefail

DISK_NAME="$1"      # block device name inside WSL, e.g. sdc (no /dev prefix)
EXPECTED_BYTES="$2" # sanity check against Get-VHD .Size
GUEST_USER="$3"
MODE="$4"           # cloud | golden
PUBKEY_FILE="$5"    # public key file readable from /mnt/c/...
KEEP_HOST_KEYS="${6:-}"

say() { printf '%s\n' "$*"; }
die() { printf 'INJECTION ERROR: %s\n' "$*" >&2; exit 1; }

[[ "$MODE" == "cloud" || "$MODE" == "golden" ]] || die "bad mode: $MODE"
[[ -r "$PUBKEY_FILE" ]] || die "public key file not readable inside WSL: $PUBKEY_FILE"
[[ -n "$DISK_NAME" ]] && [[ "$DISK_NAME" =~ ^sd[a-z]+$ ]] || die "bad disk name: '$DISK_NAME'"
DEV="/dev/$DISK_NAME"
[[ -e "$DEV" ]] || die "$DEV is not visible inside WSL (is the VHD still mounted?)"
DISK_BYTES=$(( $(cat "/sys/block/$DISK_NAME/size") * 512 ))
if (( EXPECTED_BYTES > 0 )) && (( DISK_BYTES < EXPECTED_BYTES )); then
  die "disk $DEV reports $DISK_BYTES bytes, below the expected $EXPECTED_BYTES"
fi
say "Target disk: $DEV ($DISK_BYTES bytes)"

# The root filesystem: an Ubuntu 24.04 cloud image has two ext4 partitions
# (root and /boot); a golden/reference VM has an LVM root. Identify the root by
# content, not by position.
ensure_lvm2() {
  command -v vgs >/dev/null 2>&1 && return 0
  say 'Installing lvm2 into the WSL helper distro (needed to activate the LVM root)...'
  apt-get update -qq >/dev/null 2>&1 || true
  apt-get install -y -qq lvm2 >/dev/null 2>&1 || true
  command -v vgs >/dev/null 2>&1 || die "lvm2 is required in the WSL distro to activate the golden image LVM root; run: sudo apt-get install lvm2"
}
ROOT=
if ensure_lvm2 && vgs --noheadings -o vg_name 2>/dev/null | grep -qx 'ubuntu-vg'; then
  vgchange -ay ubuntu-vg >/dev/null
  ROOT=/dev/ubuntu-vg/ubuntu-lv
  [[ -b "$ROOT" ]] || die "LVM volume group ubuntu-vg exists but $ROOT is not a block device"
else
  PROBE_MNT=/mnt/tbound-probe
  mkdir -p "$PROBE_MNT"
  for candidate in $(lsblk -rno NAME,TYPE,FSTYPE "$DEV" | awk '$2 == "part" && $3 == "ext4" { print "/dev/" $1 }'); do
    if mount -o ro "$candidate" "$PROBE_MNT" 2>/dev/null; then
      if [[ -d "$PROBE_MNT/usr/bin" && -f "$PROBE_MNT/etc/os-release" ]]; then
        ROOT="$candidate"
      fi
      umount "$PROBE_MNT"
    fi
    [[ -z "$ROOT" ]] || break
  done
  rmdir "$PROBE_MNT" 2>/dev/null || true
  [[ -n "$ROOT" ]] || die "could not identify the root ext4 partition on $DEV (candidates checked: $(lsblk -rno NAME,TYPE,FSTYPE "$DEV" | awk '$2 == "part" && $3 == "ext4" { print "/dev/" $1 }' | tr '\n' ' '))"
fi
say "Root filesystem: $ROOT"

MNT=/mnt/tbound-inject
mkdir -p "$MNT"
UNMOUNT_DONE=1
cleanup_mount() {
  if [[ "$UNMOUNT_DONE" == "0" ]]; then
    umount "$MNT" 2>/dev/null || true
    rmdir "$MNT" 2>/dev/null || true
    vgchange -an ubuntu-vg >/dev/null 2>&1 || true
  fi
}
trap cleanup_mount ERR
mount -o rw "$ROOT" "$MNT"
UNMOUNT_DONE=0
[[ -f "$MNT/etc/os-release" ]] || die "$ROOT does not look like an Ubuntu root filesystem"

. "$MNT/etc/os-release"
[[ "${ID:-}" == "ubuntu" ]] || die "unexpected OS on the disk: ID=${ID:-?}"
say "Injected OS: ${PRETTY_NAME:-unknown}"

if [[ "$MODE" == "cloud" ]]; then
  # Grow the partition with the highest end offset (the root partition is last
  # in the 24.04 cloud-image layout; lsblk NAME order is NOT physical order)
  # and then resize the filesystem.
  LASTPART=$(lsblk -rno NAME,TYPE,START,SIZE -b "$DEV" | awk '$2 == "part" { end = ($3 * 512) + $4; if (end > maxend) { maxend = end; last = $1 } } END { print last }')
  [[ -n "$LASTPART" ]] || die "could not determine the partition with the highest end offset on $DEV"
  PARTNUM=$(printf '%s' "$LASTPART" | tr -dc '0-9')
  [[ -n "$PARTNUM" ]] || die "could not derive the partition number from '$LASTPART'"
  if command -v growpart >/dev/null 2>&1; then
    growpart "$DEV" "$PARTNUM"
  elif [[ -x "$MNT/usr/bin/growpart" ]]; then
    chroot "$MNT" /usr/bin/growpart "$DEV" "$PARTNUM"
  else
    die 'growpart is unavailable in WSL and in the image; the root filesystem cannot be enlarged offline'
  fi
  say "Partition grown: ${DEV}${PARTNUM}"
  resize2fs "$ROOT"
  say "Filesystem resized on $ROOT"
fi

if ! chroot "$MNT" id "$GUEST_USER" >/dev/null 2>&1; then
  chroot "$MNT" useradd -m -s /bin/bash -c 'TBound evaluation trial account' "$GUEST_USER"
  chroot "$MNT" passwd -l "$GUEST_USER" >/dev/null 2>&1 || true
  say "Created trial user '$GUEST_USER'."
else
  say "Trial user '$GUEST_USER' already exists; reusing it."
fi
UID_NUM=$(chroot "$MNT" id -u "$GUEST_USER")
[[ "$UID_NUM" != "0" ]] || die 'the trial user must be a non-root user'

# NOPASSWD sudo for the provisioning phase (documented convenience).
if grep -Eq '^[[:space:]]*[@]includedir[[:space:]]+/etc/sudoers\.d' "$MNT/etc/sudoers"; then
  SUDOERS="/etc/sudoers.d/90-tbound-$GUEST_USER"
  SUDOERS_LINE="$GUEST_USER ALL=(ALL) NOPASSWD:ALL"
  if [[ -e "$MNT$SUDOERS" ]]; then
    grep -qxF "$SUDOERS_LINE" "$MNT$SUDOERS" || die "$MNT$SUDOERS exists with different content; inspect it manually"
  else
    printf '%s\n' "$SUDOERS_LINE" > "$MNT$SUDOERS"
    chmod 0440 "$MNT$SUDOERS"
  fi
  chroot "$MNT" visudo -c -f "$SUDOERS" >/dev/null
  say "NOPASSWD sudo provisioned for '$GUEST_USER' (remove $SUDOERS before untrusted runs if you want it gone)."
else
  die '/etc/sudoers has no active @includedir /etc/sudoers.d line; refusing to edit the main sudoers file'
fi

# SSH key authentication.
SSH_DIR="$MNT/home/$GUEST_USER/.ssh"
mkdir -p "$SSH_DIR"
chmod 700 "$SSH_DIR"
AUTH_KEYS="$SSH_DIR/authorized_keys"
touch "$AUTH_KEYS"
while IFS= read -r keyline; do
  [[ -n "$keyline" ]] || continue
  grep -qxF "$keyline" "$AUTH_KEYS" || printf '%s\n' "$keyline" >> "$AUTH_KEYS"
done < "$PUBKEY_FILE"
chown -R "$UID_NUM:$UID_NUM" "$SSH_DIR"
chmod 600 "$AUTH_KEYS"
say "SSH public key(s) installed for '$GUEST_USER'."

if [[ "$MODE" == "golden" ]]; then
  # A cloned golden disk must become an independent machine: netplan,
  # binding, SSH host keys, machine-id.
  [[ ! -L "$MNT/etc" && ! -L "$MNT/etc/netplan" ]] || die 'Symlinked Netplan parent directory is not accepted'
  for nf in "$MNT"/etc/netplan/*.yaml "$MNT"/etc/netplan/*.yml; do
    [[ -e "$nf" || -L "$nf" ]] || continue
    [[ ! -e "$nf.installer-orig" && ! -L "$nf.installer-orig" ]] || die "Netplan backup already exists: $nf.installer-orig"
    mv -- "$nf" "$nf.installer-orig"
    say "Renamed installer netplan file: $(basename "$nf")"
  done
  cat > "$MNT/etc/netplan/99-tbound-dhcp.yaml" <<'NETPLAN'
network:
  version: 2
  renderer: networkd
  ethernets:
    hyperv:
      match:
        driver: hv_netvsc
      dhcp4: true
NETPLAN
  chmod 600 "$MNT/etc/netplan/99-tbound-dhcp.yaml"
  say 'Wrote /etc/netplan/99-tbound-dhcp.yaml (Hyper-V NIC DHCP4).'
  if [[ -z "$KEEP_HOST_KEYS" ]]; then
    rm -f "$MNT"/etc/ssh/ssh_host_*_key "$MNT"/etc/ssh/ssh_host_*_key.pub
    chroot "$MNT" ssh-keygen -A >/dev/null
    say 'Regenerated SSH host keys on the copy.'
  else
    say 'Kept existing SSH host keys (-KeepSshHostKeys).'
  fi
  if [[ "$MODE" == "golden" ]]; then
    HOST_FP=$(chroot "$MNT" ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256 | awk 'NR == 1 { print $2 }')
    [[ "$HOST_FP" =~ ^SHA256:[A-Za-z0-9+/]{43}$ ]] || die 'could not establish one ED25519 host-key fingerprint from the offline-injected copy'
    say "TBOUND_GUEST_HOST_KEY_FINGERPRINT=$HOST_FP"
  fi
  : > "$MNT/etc/machine-id"
  rm -f "$MNT/var/lib/dbus/machine-id"
  say 'Cleared machine-id (regenerated on next boot).'
fi

sync
umount "$MNT"
UNMOUNT_DONE=1
rmdir "$MNT" 2>/dev/null || true
vgchange -an ubuntu-vg >/dev/null 2>&1 || true
say 'Offline injection complete; disk released.'
'@
    $normalizedInject = $injectScript -replace "`r`n", "`n"
    $normalizedList = $listDevicesScript -replace "`r`n", "`n"
    $keepKeysFlag = '1'
    if ($KeepSshHostKeys) { $keepKeysFlag = '' }
    $workDir = Join-Path ([IO.Path]::GetTempPath()) ('tbound-inject-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $workDir -ErrorAction Stop | Out-Null
    $injectPath = Join-Path $workDir 'inject.sh'
    $listPath = Join-Path $workDir 'list-devices.sh'
    $pubKeyWslPath = ConvertTo-WslPath -WindowsPath $resolvedPublicKeyPath
    [IO.File]::WriteAllText($injectPath, $normalizedInject, (New-Object System.Text.UTF8Encoding($false)))
    [IO.File]::WriteAllText($listPath, $normalizedList, (New-Object System.Text.UTF8Encoding($false)))
    $injectWslPath = ConvertTo-WslPath -WindowsPath $injectPath
    $listWslPath = ConvertTo-WslPath -WindowsPath $listPath
    $listArgs = @('-u', 'root', '--', 'bash', $listWslPath)
    try {
        $devicesBefore = @(Invoke-WslCommand -WslArguments $listArgs -ThrowOnError).Output -split "`r?`n" | Where-Object { $_ }
        Write-Host 'Bare-mounting the provisioned disk into WSL (wsl --mount --vhd <path> --bare; never combined with Mount-VHD)...'
        $null = Invoke-WslCommand -WslArguments @('--mount', '--vhd', $vhdPath, '--bare') -ThrowOnError
        $injectionComplete = $false
        try {
            $devicesAfter = @(Invoke-WslCommand -WslArguments $listArgs -ThrowOnError).Output -split "`r?`n" | Where-Object { $_ }
            $newDevices = @($devicesAfter | Where-Object { $_ -notin $devicesBefore })
            if ($newDevices.Count -ne 1) {
                throw ("Expected exactly one new block device after mounting the VHD; found {0} ({1})." -f $newDevices.Count, ($newDevices -join ', '))
            }
            $diskDevName = $newDevices[0]
            Write-Host "Attached disk is WSL block device: $diskDevName"
            $diskBytes = (Get-VHD -Path $vhdPath -ErrorAction Stop).Size
            $injectMode = 'golden'
            if ($ImageKind -eq 'CloudVhdx') { $injectMode = 'cloud' }
            $wslArgs = @('-u', 'root', '--', 'bash', $injectWslPath,
                $diskDevName, [string][long]$diskBytes, $GuestUser, $injectMode, $pubKeyWslPath, $keepKeysFlag)
            $injectResult = Invoke-WslCommand -WslArguments $wslArgs -ThrowOnError
            Write-Host $injectResult.Output.Trim()
            if ($injectMode -eq 'golden') {
                $hostFingerprintMatches = [regex]::Matches(
                    $injectResult.Output,
                    '(?m)^TBOUND_GUEST_HOST_KEY_FINGERPRINT=(?<Fingerprint>SHA256:[A-Za-z0-9+/]{43})\r?$'
                )
                if ($hostFingerprintMatches.Count -ne 1) {
                    throw 'Offline injection did not return exactly one ED25519 host-key fingerprint; refusing an unverified SSH connection.'
                }
                $offlineGuestHostKeyFingerprint = $hostFingerprintMatches[0].Groups['Fingerprint'].Value
            }
            $script:stepsDone.Add('Offline SSH-key injection complete')
            $injectionComplete = $true
        }
        finally {
            Write-Host 'Unmounting the disk from WSL...'
            # Fail closed: an unverified unmount must stop before Start-VM without masking an earlier injection error.
            try { $null = Invoke-WslCommand -WslArguments @('--unmount', $vhdPath) -ThrowOnError }
            catch {
                if ($injectionComplete) { throw }
                Write-Warning ('WSL unmount also failed while an earlier error propagates; the disk may still be attached to WSL: ' + $_.Exception.Message)
            }
        }
    }
    finally {
        Remove-Item -LiteralPath $workDir -Recurse -Force -ErrorAction SilentlyContinue
        if ($tempExtractDir -and (Test-Path -LiteralPath $tempExtractDir)) {
            Remove-Item -LiteralPath $tempExtractDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}
else {
    Write-Step 'Step 4/6: offline injection skipped (-SkipWslInjection)'
    if ($tempExtractDir -and (Test-Path -LiteralPath $tempExtractDir)) {
        Remove-Item -LiteralPath $tempExtractDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# ---------------------------------------------------------------------------
# Step 5: boot, resolve IP, wait for SSH
# ---------------------------------------------------------------------------

Write-Step 'Step 5/6: boot and wait for SSH'
$hostMemoryBeforeStart = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
$availableHostMemoryBeforeStartBytes = [Int64]$hostMemoryBeforeStart.FreePhysicalMemory * 1KB
if ($availableHostMemoryBeforeStartBytes -lt 16GB) {
    throw ("Host has {0:N1} GiB available physical memory immediately before VM start; at least 16 GiB is required. The new VM remains stopped." -f ($availableHostMemoryBeforeStartBytes / 1GB))
}
Write-Host ([string]::Format([Globalization.CultureInfo]::InvariantCulture, 'Host available physical memory immediately before VM start: {0:N1} GiB.', ($availableHostMemoryBeforeStartBytes / 1GB)))
$null = Start-VM -Name $VmName -ErrorAction Stop
Write-Host "VM '$VmName' started; waiting for the guest network and SSH daemon..."

function Get-GuestIpAddress {
    if ($GuestIpAddress) { return $GuestIpAddress }

    # 1) Hyper-V IP reporting (needs the guest KVP daemon; the cloud image has
    #    the Hyper-V cloud tools, the reference VM does not).
    $kvpDeadline = (Get-Date).AddSeconds([Math]::Min(180, $BootTimeoutSeconds))
    while ((Get-Date) -lt $kvpDeadline) {
        $ips = @(Get-VMNetworkAdapter -VMName $VmName -ErrorAction Stop |
            ForEach-Object { $_.IPAddresses } |
            Where-Object { $_ -match '^\d+\.\d+\.\d+\.\d+$' })
        if ($ips.Count -gt 0) { return $ips[0] }
        Start-Sleep -Seconds 5
    }

    # 2) ARP sweep of the provisioning switch subnet, matching the VM's MAC.
    Write-Host 'KVP IP reporting unavailable; sweeping the switch subnet via ARP...'
    $adapter = Get-VMNetworkAdapter -VMName $VmName -ErrorAction Stop | Select-Object -First 1
    $vmMac = ($adapter.MacAddress -replace '..(?!$)', '$0-').ToUpperInvariant()
    $hostIp = Get-NetIPAddress -InterfaceAlias ("vEthernet ($SwitchName)") -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $hostIp) {
        throw "Could not determine the '$SwitchName' host subnet for the ARP sweep; pass -GuestIpAddress."
    }
    $subnetBytes = [System.Net.IPAddress]::Parse($hostIp.IPAddress).GetAddressBytes()
    for ($octet = 1; $octet -le 254; $octet++) {
        $probe = [byte[]]::new(4)
        $subnetBytes.CopyTo($probe, 0)
        $probe[3] = $octet
        $target = [System.Net.IPAddress]::new($probe)
        $ping = [System.Net.NetworkInformation.Ping]::new()
        try { $null = $ping.Send($target, 120) } catch { }
        $ping.Dispose()
    }
    $neighbor = Get-NetNeighbor -InterfaceAlias ("vEthernet ($SwitchName)") -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Where-Object { $_.PhysicalAddress -eq $vmMac } |
        Select-Object -First 1
    if ($neighbor) { return $neighbor.IPAddress }
    throw ("Could not resolve the guest IP address. Pass -GuestIpAddress (read it from the VMConnect console with: ip -4 addr show) " +
           "and re-run; the VM is running with the adapter connected to '$SwitchName'.")
}

$guestIpInput = Get-GuestIpAddress
$parsedGuestIp = $null
if (-not [System.Net.IPAddress]::TryParse($guestIpInput, [ref]$parsedGuestIp) -or
    $parsedGuestIp.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) {
    throw "Guest IP must be a valid IPv4 address; got '$guestIpInput'."
}
$guestIp = $parsedGuestIp.ToString()
Write-Host "Guest IP: $guestIp"

$sshKeyscanExe = $script:SshKeyscanExePath
$sshKeygenExe = $script:SshKeygenExePath
if ($GuestHostKeyFingerprint -and $offlineGuestHostKeyFingerprint -and
    $GuestHostKeyFingerprint -cne $offlineGuestHostKeyFingerprint) {
    throw 'The supplied guest host-key fingerprint does not match the public key generated by offline injection.'
}
if (-not $GuestHostKeyFingerprint -and $offlineGuestHostKeyFingerprint) {
    $GuestHostKeyFingerprint = $offlineGuestHostKeyFingerprint
    Write-Host 'Expected ED25519 host-key fingerprint came from the powered-off disk after offline injection.'
}
if (-not $GuestHostKeyFingerprint) {
    Write-Host "In VMConnect, run: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256"
    $GuestHostKeyFingerprint = Read-Host 'Enter the guest console ED25519 SHA256 fingerprint'
}
if ($GuestHostKeyFingerprint -notmatch '^SHA256:[A-Za-z0-9+/]{43}$') {
    throw 'A valid guest-console ED25519 SHA256 fingerprint is required before SSH provisioning.'
}
$knownHostsFile = New-TemporaryFile
$sshConfigFile = New-TemporaryFile
foreach ($temporaryInput in @($knownHostsFile, $sshConfigFile)) {
    Assert-NoReparsePathComponents -Path $temporaryInput.FullName
    $temporaryItem = Get-Item -LiteralPath $temporaryInput.FullName -Force -ErrorAction Stop
    if ($temporaryItem.PSIsContainer -or ($temporaryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "SSH temporary input is not a regular file: $($temporaryInput.FullName)"
    }
}
[IO.File]::WriteAllText($sshConfigFile.FullName, '', [Text.Encoding]::ASCII)
Protect-CurrentUserOnlyFile -Path $sshConfigFile.FullName
$sshConfigLock = [IO.File]::Open($sshConfigFile.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
if ($sshConfigLock.Length -ne 0) { throw 'Task-specific SSH configuration must remain empty.' }
$knownHostsLock = $null
$knownHostsHost = if ($SshPort -eq 22) { $guestIp } else { '[{0}]:{1}' -f $guestIp, $SshPort }
$keyscanDeadline = (Get-Date).AddSeconds($BootTimeoutSeconds)
$hostKeyVerified = $false
while ((Get-Date) -lt $keyscanDeadline) {
    $scanOutput = @(& $sshKeyscanExe -p $SshPort -t ed25519 $guestIp 2>$null)
    $hostKeyLines = @($scanOutput | Where-Object {
        $fields = ([string]$_).Trim() -split '\s+'
        $fields.Count -eq 3 -and $fields[0] -ceq $knownHostsHost -and
            $fields[1] -ceq 'ssh-ed25519' -and $fields[2] -match '^[A-Za-z0-9+/=]+$'
    })
    if ($hostKeyLines.Count -eq 1) {
        Set-Content -LiteralPath $knownHostsFile.FullName -Value $hostKeyLines[0] -Encoding Ascii
        $fingerprintLine = & $sshKeygenExe -lf $knownHostsFile.FullName -E sha256 2>$null
        if ($LASTEXITCODE -ne 0) { throw 'Could not calculate the scanned SSH host-key fingerprint.' }
        if ($fingerprintLine -match '(SHA256:[A-Za-z0-9+/]{43})' -and $Matches[1] -eq $GuestHostKeyFingerprint) {
            $hostKeyVerified = $true
            Protect-CurrentUserOnlyFile -Path $knownHostsFile.FullName
            $knownHostsLock = [IO.File]::Open($knownHostsFile.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
            break
        }
        throw "SSH host key for $guestIp does not match the guest-console fingerprint. Provisioning stopped."
    }
    Start-Sleep -Seconds 5
}
if (-not $hostKeyVerified) { throw "Could not obtain exactly one ED25519 SSH host key from $guestIp before timeout." }
Write-Host "Guest ED25519 host key matches $GuestHostKeyFingerprint."

# Preserve the public SSH contact details before guest provisioning can fail,
# for example when the current booted kernel reports the wrong Landlock ABI.
$progressSummaryPath = Join-Path $fullVmRoot 'provisioning-summary.txt'
$progressSummaryLines = @(
    'TBound reproducible evaluation VM - provisioning progress',
    "Generated:      $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss K')",
    "VM name:        $VmName",
    "Image source:   $resolvedImage",
    "Image SHA-256:  $((Get-Sha256Hex -Path $resolvedImage))",
    "Image kind:     $ImageKind",
    "Guest user:     $GuestUser",
    "Guest IP:       $guestIp (Default Switch DHCP; changes across reboots)",
    "Guest ED25519 host key SHA256: $GuestHostKeyFingerprint",
    'Guest host-key fingerprint source: verified against offline-injected powered-off-disk key',
    "Private SSH identity path validated: $([bool]$resolvedPrivateKeyPath)",
    'Provisioning status: guest bootstrap pending; this is not a completion receipt.'
)
[IO.File]::WriteAllText(
    $progressSummaryPath,
    (($progressSummaryLines -join [Environment]::NewLine) + [Environment]::NewLine),
    [Text.UTF8Encoding]::new($false)
)
Write-Host "Persisted SSH contact details and offline host-key fingerprint to $progressSummaryPath before guest bootstrap."

$sshExe = $script:SshExePath
$sshBase = @(
    '-F', ('"{0}"' -f $sshConfigFile.FullName),
    '-p', $SshPort,
    '-o', 'BatchMode=yes',
    '-o', 'StrictHostKeyChecking=yes',
    '-o', ('UserKnownHostsFile="{0}"' -f $knownHostsFile.FullName),
    '-o', 'GlobalKnownHostsFile=none',
    '-o', 'KnownHostsCommand=none',
    '-o', 'IdentitiesOnly=yes',
    '-o', 'IdentityAgent=none',
    '-o', 'ProxyCommand=none',
    '-o', 'ProxyJump=none',
    '-o', 'PermitLocalCommand=no',
    '-o', 'ClearAllForwardings=yes',
    '-o', 'PreferredAuthentications=publickey',
    '-o', 'ConnectTimeout=8'
)
if (-not $resolvedPrivateKeyPath) { throw 'PrivateKeyFile preflight did not produce an SSH identity path.' }
$sshBase += @('-i', ('"{0}"' -f $resolvedPrivateKeyPath))
$sshTarget = '{0}@{1}' -f $GuestUser, $guestIp
$sshDeadline = (Get-Date).AddSeconds($BootTimeoutSeconds)
$sshReady = $false
while ((Get-Date) -lt $sshDeadline) {
    $probeCmd = '"{0}" {1} "{2}" true 2>&1' -f $sshExe, ($sshBase -join ' '), $sshTarget
    $null = & $script:CmdExePath /c $probeCmd
    if ($LASTEXITCODE -eq 0) { $sshReady = $true; break }
    Start-Sleep -Seconds 5
}
if (-not $sshReady) {
    throw ((("SSH did not become available on {0} within {1} seconds. The VM is running; inspect it in VMConnect, then re-run this script " +
            "with -GuestIpAddress (provisioning continues from the SSH step only if the injection already succeeded).") -f $sshTarget, $BootTimeoutSeconds))
}
Write-Host 'SSH is ready.'
$script:stepsDone.Add("SSH ready at $sshTarget")

# ---------------------------------------------------------------------------
# Step 6: guest provisioning (the same bootstrap as the native Linux path)
# ---------------------------------------------------------------------------

Write-Step 'Step 6/6: guest provisioning via SSH (infra/repro/bootstrap-linux.sh inside the guest)'
$null = & $script:CmdExePath /c ('"{0}" {1} "{2}" "mkdir -p ~/tbound-infra/repro ~/tbound-infra/guest" 2>&1' -f $sshExe, ($sshBase -join ' '), $sshTarget)
if ($LASTEXITCODE -ne 0) { throw "Creating the remote staging directories failed (exit $LASTEXITCODE)." }

$scpExe = $script:ScpExePath
$scpBase = @(
    '-S', $script:SshExePath,
    '-F', $sshConfigFile.FullName,
    '-P', $SshPort,
    '-o', 'BatchMode=yes',
    '-o', 'StrictHostKeyChecking=yes',
    '-o', ('UserKnownHostsFile={0}' -f $knownHostsFile.FullName),
    '-o', 'GlobalKnownHostsFile=none',
    '-o', 'KnownHostsCommand=none',
    '-o', 'IdentitiesOnly=yes',
    '-o', 'IdentityAgent=none',
    '-o', 'ProxyCommand=none',
    '-o', 'ProxyJump=none',
    '-o', 'PermitLocalCommand=no',
    '-o', 'ClearAllForwardings=yes',
    '-o', 'PreferredAuthentications=publickey',
    '-i', $resolvedPrivateKeyPath
)
foreach ($pair in @(
        @{ src = $bootstrapLinux; dst = '~/tbound-infra/repro/bootstrap-linux.sh' },
        @{ src = $landlockProfileGate; dst = '~/tbound-infra/repro/landlock-profile-gate.sh' },
        @{ src = $setupUbuntu; dst = '~/tbound-infra/guest/setup-ubuntu-24.04.sh' },
        @{ src = $probeCapabilities; dst = '~/tbound-infra/guest/probe-capabilities.sh' })) {
    $scpArgs = @($scpBase) + @($pair.src, ('{0}:{1}' -f $sshTarget, $pair.dst))
    Write-Host "Copying $(Split-Path -Leaf $pair.src)..."
    $null = & $scpExe @scpArgs 2>&1
    if ($LASTEXITCODE -ne 0) { throw "scp failed for $($pair.src) (exit $LASTEXITCODE)." }
}
$script:stepsDone.Add('Scripts staged in the guest')

Write-Host "Running bootstrap-linux.sh as '$GuestUser' via sudo (TBOUND_ASSUME_YES=1; this is the provisioning plan you confirmed with ShouldProcess)..."
$remoteCmd = 'sudo TBOUND_ASSUME_YES=1 bash ~/tbound-infra/repro/bootstrap-linux.sh {0}' -f $GuestUser
& $script:CmdExePath /c ('"{0}" {1} "{2}" "{3}" 2>&1' -f $sshExe, ($sshBase -join ' '), $sshTarget, $remoteCmd)
$bootstrapExit = $LASTEXITCODE
if ($bootstrapExit -ne 0) {
    Write-Warning ('bootstrap-linux.sh exited {0}. Meanings: 1 probe FAIL (takes precedence), 2 precondition error, 3 sole exact D09 Landlock ABI mismatch, 4 unexpected UNVERIFIED without a FAIL.' -f $bootstrapExit)
    Write-Warning "The temporary NOPASSWD provisioning rule /etc/sudoers.d/90-tbound-$GuestUser remains so you can finish provisioning. Remove it before any untrusted evaluation: sudo rm -f /etc/sudoers.d/90-tbound-$GuestUser"
    throw "Guest bootstrap failed with exit $bootstrapExit; the guest keeps the staged scripts under ~/tbound-infra/ for a manual re-run."
}
$script:stepsDone.Add('Guest bootstrap completed')

# The offline injector creates this narrowly named, temporary provisioning grant
# so the non-login trial account can run the guarded bootstrap. Remove it after a
# successful bootstrap: evaluation sessions must not retain a passwordless path
# from the trial identity to host-VM root.
Write-Host "Removing temporary NOPASSWD provisioning rule for '$GuestUser'..."
$sudoersPath = '/etc/sudoers.d/90-tbound-{0}' -f $GuestUser
$removeGrantCmd = 'sudo rm -f {0} && test ! -e {0} && echo TBOUND_PROVISIONING_SUDO_REMOVED' -f $sudoersPath
& $script:CmdExePath /c ('"{0}" {1} "{2}" "{3}" 2>&1' -f $sshExe, ($sshBase -join ' '), $sshTarget, $removeGrantCmd)
if ($LASTEXITCODE -ne 0) {
    throw "Could not remove temporary provisioning sudo rule $sudoersPath; do not run untrusted workloads until it is removed."
}
$script:stepsDone.Add('Temporary provisioning sudo rule removed')

if ($null -ne $knownHostsLock) { $knownHostsLock.Dispose() }
$sshConfigLock.Dispose()
Remove-Item -LiteralPath $knownHostsFile.FullName -Force -ErrorAction Stop
Remove-Item -LiteralPath $sshConfigFile.FullName -Force -ErrorAction Stop

# Restore the reference integration policy: KVP was enabled only for IP
# reporting during provisioning.
$kvpService = Get-VMIntegrationService -VMName $VmName -ErrorAction Stop |
    Where-Object { $_.Id -match '2A34B1C2-FD73-4043-8A5B-DD2159BC743F' } |
    Select-Object -First 1
if ($kvpService -and $kvpService.Enabled) {
    $null = Disable-VMIntegrationService -VMIntegrationService $kvpService -ErrorAction Stop
    Write-Host 'Key-Value Pair Exchange integration service disabled again (reference policy restored).'
}

# ---------------------------------------------------------------------------
# Summary and required next steps
# ---------------------------------------------------------------------------

$summaryPath = Join-Path $fullVmRoot 'provisioning-summary.txt'
$summaryLines = @(
    'TBound reproducible evaluation VM - provisioning summary',
    "Generated:      $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss K')",
    "VM name:        $VmName",
    "VM root:        $fullVmRoot",
    "Disk:           $vhdPath",
    "Image source:   $resolvedImage",
    "Image SHA-256:  $((Get-Sha256Hex -Path $resolvedImage))",
    "Image kind:     $ImageKind",
    "Host tool SHA-256 (cmd.exe): $($script:VerifiedWindowsToolHashes['cmd.exe'])",
    "Host tool SHA-256 (ssh.exe): $($script:VerifiedWindowsToolHashes['ssh.exe'])",
    "Host tool SHA-256 (scp.exe): $($script:VerifiedWindowsToolHashes['scp.exe'])",
    "Host tool SHA-256 (ssh-keyscan.exe): $($script:VerifiedWindowsToolHashes['ssh-keyscan.exe'])",
    "Host tool SHA-256 (ssh-keygen.exe): $($script:VerifiedWindowsToolHashes['ssh-keygen.exe'])",
    "Host tool SHA-256 (wsl.exe): $wslToolHashSummary",
    "Guest user:     $GuestUser (record the actual non-root UID in the guest)",
    "Guest IP:       $guestIp (Default Switch DHCP; changes across reboots)",
    "Guest ED25519 host key SHA256: $GuestHostKeyFingerprint",
    "Private SSH identity path validated: $([bool]$resolvedPrivateKeyPath)",
    "Switch:         $SwitchName (adapter CONNECTED - disconnect before untrusted runs)",
    "Bootstrap exit: $bootstrapExit",
    'Completed steps:'
) + @($script:stepsDone | ForEach-Object { "  - $_" })
$summaryText = $summaryLines -join "`r`n"
[IO.File]::WriteAllText($summaryPath, $summaryText + "`r`n", (New-Object System.Text.UTF8Encoding($false)))

Write-Host ''
Write-Host '=== Provisioning complete ===' -ForegroundColor Green
Write-Host "Summary written to: $summaryPath"
Write-Host ''
Write-Host 'REQUIRED next steps (the online provisioning phase is over; the offline phase rules in infra/guest/README.md apply unchanged):'
Write-Host '  1. Prepare pinned dependencies while online, per infra/guest/README.md (Go module cache, npm cache).'
Write-Host '  2. Disconnect the adapter and create the golden baseline:'
Write-Host "       Disconnect-VMNetworkAdapter -VMName $VmName"
Write-Host "       Checkpoint-VM -Name $VmName -SnapshotName 'tbound-golden-baseline'"
Write-Host '  3. Re-run the capability probe offline (as the trial user) and save the report:'
Write-Host "       .\infra\repro\probe-host.ps1 -Target Ssh -HostName $guestIp -UserName $GuestUser -KeyFile $resolvedPrivateKeyPath -OutFile offline-report.txt"
Write-Host '  4. If the measured ABI is below 8, Ubuntu 24.04 HWE may provide ABI 8; reboot and re-probe. The D09 profile requires exactly ABI 8, so reconcile a later ABI explicitly.'
Write-Host '  5. Keep the NIC disconnected for every untrusted run; see docs/guides/setup.md for the claim boundary.'
