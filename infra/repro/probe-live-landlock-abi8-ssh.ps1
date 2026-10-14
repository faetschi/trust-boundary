#requires -Version 5.1
<#
.SYNOPSIS
    Verifies a live guest's exact Landlock ABI over pinned SSH.
.DESCRIPTION
    The expected ED25519 host-key fingerprint must come from the trusted
    powered-off-disk injection performed by bootstrap-hyperv-windows.ps1.
    This helper obtains the network host key, requires an exact fingerprint
    match, then runs the Landlock VERSION syscall in the live guest. ABI 8 is
    the only successful result. It does not read or print the private SSH key.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$HostName,
    [Parameter(Mandatory)][ValidatePattern('^SHA256:[A-Za-z0-9+/]{43}$')][string]$GuestHostKeyFingerprint,
    [ValidatePattern('^[a-z_][a-z0-9_-]{0,31}$')][string]$UserName = 'tboundadmin',
    [ValidateRange(1, 65535)][int]$Port = 22
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$TrustedWindowsOwner = 'NT SERVICE\TrustedInstaller'
$ExpectedOpenSshSha256 = @{
    'ssh.exe' = 'c9a80ae8af88aa0ff7a08a7e65dbad0d408854d757a433c6a78ab46d8d38aad1'
    'ssh-keyscan.exe' = 'bfb8caae4c31698c09686ae208dc204941bc185fa282f3973c4e0a44087cc08a'
    'ssh-keygen.exe' = '539ae392c61a53fadddf0bbb152126f3e0bc042e8a4ed50718ab16fbf872cbcc'
}

function Assert-TrustedOpenSshTool {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$ExpectedSha256)
    $windowsDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
    $systemDirectory = Join-Path $windowsDirectory 'System32'
    $openSshDirectory = Join-Path $systemDirectory 'OpenSSH'
    $trustedDirectories = @($windowsDirectory, $systemDirectory, $openSshDirectory)
    $expectedPath = [IO.Path]::GetFullPath((Join-Path $openSshDirectory ([IO.Path]::GetFileName($Path))))
    $fullPath = [IO.Path]::GetFullPath($Path)
    if ($fullPath -ine $expectedPath) { throw "OpenSSH executable is outside the canonical System32\OpenSSH path: $fullPath" }

    foreach ($directory in $trustedDirectories) {
        if (-not [IO.Directory]::Exists($directory)) { throw "Trusted OpenSSH directory is missing: $directory" }
        $directoryItem = Get-Item -LiteralPath $directory -Force -ErrorAction Stop
        if (($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
            (Get-Acl -LiteralPath $directory -ErrorAction Stop).Owner -cne $TrustedWindowsOwner) {
            throw "Trusted OpenSSH directory is a reparse point or is not owned by $TrustedWindowsOwner`: $directory"
        }
    }

    $root = [IO.Path]::GetPathRoot($fullPath)
    $current = $root
    foreach ($part in ($fullPath.Substring($root.Length) -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($part)) { continue }
        $current = Join-Path $current $part
        $item = Get-Item -LiteralPath $current -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "OpenSSH executable path contains a reparse point: $current"
        }
    }

    $tool = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    if ($tool.PSIsContainer -or (Get-Acl -LiteralPath $fullPath -ErrorAction Stop).Owner -cne $TrustedWindowsOwner) {
        throw "OpenSSH executable is not a regular file owned by $TrustedWindowsOwner`: $fullPath"
    }
    $actualSha256 = (Get-FileHash -LiteralPath $fullPath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
    if ($actualSha256 -cne $ExpectedSha256) { throw "Pinned OpenSSH SHA-256 mismatch: $fullPath" }
    $signature = Get-AuthenticodeSignature -FilePath $fullPath -ErrorAction Stop
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid -or
        $signature.SignerCertificate.Subject -notmatch '^CN=Microsoft Windows, O=Microsoft Corporation,') {
        throw "OpenSSH executable lacks a valid Microsoft Windows signature: $fullPath"
    }
    return $actualSha256
}

$KeyFile = Join-Path $env:LOCALAPPDATA 'TBound\abi8-qualification\id_ed25519'
$parsedAddress = $null
if (-not [Net.IPAddress]::TryParse($HostName, [ref]$parsedAddress) -or
    $parsedAddress.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork) {
    throw 'HostName must be the reported guest IPv4 address.'
}
if ($HostName -cne $parsedAddress.ToString()) { throw 'HostName must use canonical dotted-decimal IPv4 notation.' }
$resolvedKey = (Resolve-Path -LiteralPath $KeyFile -ErrorAction Stop).Path
if (-not (Test-Path -LiteralPath $resolvedKey -PathType Leaf)) { throw 'KeyFile must be an existing private-key file.' }
foreach ($componentPath in @($resolvedKey, [IO.Path]::GetTempPath())) {
    $fullPath = [IO.Path]::GetFullPath($componentPath)
    $root = [IO.Path]::GetPathRoot($fullPath)
    $current = $root
    foreach ($part in ($fullPath.Substring($root.Length) -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($part)) { continue }
        $current = Join-Path $current $part
        if (-not (Test-Path -LiteralPath $current)) { break }
        $item = Get-Item -LiteralPath $current -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "SSH input path contains a junction or symbolic link: $current"
        }
    }
}

$windowsDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
$openSshDirectory = Join-Path (Join-Path $windowsDirectory 'System32') 'OpenSSH'
$ssh = Join-Path $openSshDirectory 'ssh.exe'
$sshKeyscan = Join-Path $openSshDirectory 'ssh-keyscan.exe'
$sshKeygen = Join-Path $openSshDirectory 'ssh-keygen.exe'
$sshHash = Assert-TrustedOpenSshTool -Path $ssh -ExpectedSha256 $ExpectedOpenSshSha256['ssh.exe']
$sshKeyscanHash = Assert-TrustedOpenSshTool -Path $sshKeyscan -ExpectedSha256 $ExpectedOpenSshSha256['ssh-keyscan.exe']
$sshKeygenHash = Assert-TrustedOpenSshTool -Path $sshKeygen -ExpectedSha256 $ExpectedOpenSshSha256['ssh-keygen.exe']
$knownHosts = New-TemporaryFile
$probeInput = New-TemporaryFile
$sshConfig = New-TemporaryFile
$sshConfigLock = $null
try {
    foreach ($temporaryFile in @($knownHosts, $probeInput, $sshConfig)) {
        $temporaryItem = Get-Item -LiteralPath $temporaryFile.FullName -Force -ErrorAction Stop
        if ($temporaryItem.PSIsContainer -or ($temporaryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "SSH temporary input is not a regular file: $($temporaryFile.FullName)"
        }
    }
    [IO.File]::WriteAllText($sshConfig.FullName, '', [Text.Encoding]::ASCII)
    if ((Get-Item -LiteralPath $sshConfig.FullName -Force -ErrorAction Stop).Length -ne 0) {
        throw 'Could not create the empty task-specific SSH configuration file.'
    }
    $sshConfigLock = [IO.File]::Open($sshConfig.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    if ($sshConfigLock.Length -ne 0) { throw 'Task-specific SSH configuration must remain empty.' }
    $knownHostsHost = if ($Port -eq 22) { $HostName } else { '[{0}]:{1}' -f $HostName, $Port }
    $hostLines = @()
    for ($attempt = 0; $attempt -lt 12; $attempt++) {
        $scanOutput = @(& $sshKeyscan -p $Port -t ed25519 $HostName 2>$null)
        $hostLines = @($scanOutput | Where-Object {
            $fields = ([string]$_).Trim() -split '\s+'
            $fields.Count -eq 3 -and $fields[0] -ceq $knownHostsHost -and
                $fields[1] -ceq 'ssh-ed25519' -and $fields[2] -match '^[A-Za-z0-9+/=]+$'
        })
        if ($hostLines.Count -gt 0) { break }
        Start-Sleep -Seconds 5
    }
    if ($hostLines.Count -ne 1) {
        throw "Expected one ED25519 SSH host key from $HostName; got $($hostLines.Count)."
    }
    [IO.File]::WriteAllText($knownHosts.FullName, $hostLines[0] + [Environment]::NewLine, [Text.Encoding]::ASCII)
    $observedFingerprint = & $sshKeygen -lf $knownHosts.FullName -E sha256 2>$null
    if ($LASTEXITCODE -ne 0 -or $observedFingerprint -notmatch '(SHA256:[A-Za-z0-9+/]{43})' -or
        $Matches[1] -cne $GuestHostKeyFingerprint) {
        throw 'The network ED25519 host key does not match the offline-injection fingerprint.'
    }
    Write-Host "SSH host key matches offline fingerprint $GuestHostKeyFingerprint."
    Write-Host "Pinned OpenSSH SHA-256: ssh=$sshHash ssh-keyscan=$sshKeyscanHash ssh-keygen=$sshKeygenHash"

    $python = @'
import ctypes
import platform
import sys

libc = ctypes.CDLL(None, use_errno=True)
libc.syscall.restype = ctypes.c_long
abi = libc.syscall(444, None, ctypes.c_size_t(0), ctypes.c_uint(1))
err = ctypes.get_errno() if abi < 0 else 0
print(f"Kernel: {platform.release()}")
print(f"Landlock ABI: {abi}; errno: {err}")
sys.exit(0 if abi == 8 else 1)
'@
    [IO.File]::WriteAllText($probeInput.FullName, $python, (New-Object Text.UTF8Encoding($false)))
    $sshArgs = @(
        '-F', $sshConfig.FullName,
        '-p', $Port,
        '-o', 'BatchMode=yes',
        '-o', 'StrictHostKeyChecking=yes',
        '-o', ('UserKnownHostsFile={0}' -f $knownHosts.FullName),
        '-o', 'GlobalKnownHostsFile=none',
        '-o', 'KnownHostsCommand=none',
        '-o', 'IdentitiesOnly=yes',
        '-o', 'IdentityAgent=none',
        '-o', 'ProxyCommand=none',
        '-o', 'ProxyJump=none',
        '-o', 'PermitLocalCommand=no',
        '-o', 'PreferredAuthentications=publickey',
        '-o', 'ConnectTimeout=8',
        '-i', $resolvedKey
    )
    $target = '{0}@{1}' -f $UserName, $HostName
    $raw = Get-Content -LiteralPath $probeInput.FullName | & $ssh @sshArgs $target '/usr/bin/python3 -' 2>&1
    $probeExit = $LASTEXITCODE
    $output = ($raw | Out-String).Trim()
    Write-Output $output
    if ($probeExit -ne 0 -or $output -notmatch '(?m)^Landlock ABI: 8; errno: 0\r?$') {
        throw "Live guest probe did not return exactly Landlock ABI 8 (SSH/probe exit $probeExit)."
    }
}
finally {
    if ($null -ne $sshConfigLock) { $sshConfigLock.Dispose() }
    Remove-Item -LiteralPath $knownHosts.FullName -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $probeInput.FullName -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $sshConfig.FullName -Force -ErrorAction SilentlyContinue
}
