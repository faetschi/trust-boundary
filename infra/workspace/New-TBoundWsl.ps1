# Creates the dedicated WSL distro tbound-dev, with its disk under (Get-TBoundLocalRoot)\wsl\tbound-dev.
# Usage: infra\workspace\New-TBoundWsl.ps1 -RootfsPath <rootfs> -RootfsSha256 <hash>
# The rootfs must be Ubuntu 24.04 (noble). Ubuntu's daily WSL build is listed in SHA256SUMS at
# https://cdimages.ubuntu.com/ubuntu-wsl/noble/daily-live/current/ as noble-wsl-amd64.wsl. It changes daily, so the
# hash is not pinned here.
# Removes nothing. It refuses when tbound-dev is registered or its folder already holds files.
param(
    [Parameter(Mandatory)][string]$RootfsPath,
    [Parameter(Mandatory)][string]$RootfsSha256
)
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Windows only.' }
. (Join-Path $PSScriptRoot 'paths.ps1')

$distro = 'tbound-dev'
$user = 'tbound'
$wslRoot = Join-Path (Get-TBoundLocalRoot) 'wsl'
$installDir = Join-Path $wslRoot $distro

function Invoke-Wsl([string[]]$WslArgs) {
    & wsl.exe @WslArgs
    if ($LASTEXITCODE -ne 0) { throw "wsl.exe $($WslArgs -join ' ') exited with $LASTEXITCODE" }
}

$registered = (wsl.exe --list --quiet) -replace "`0", ''
$populated = (Test-Path -LiteralPath $installDir) -and [bool](Get-ChildItem -LiteralPath $installDir -Force)
if (($registered -contains $distro) -or $populated) { throw "$distro already exists. Nothing was changed." }
$rootfs = (Resolve-Path -LiteralPath $RootfsPath).Path
if ((Get-FileHash -LiteralPath $rootfs -Algorithm SHA256).Hash -ne $RootfsSha256) { throw "SHA-256 of $rootfs does not match -RootfsSha256." }

# Hyper-V refused a compressed folder here, and a VHDX should not sit in one either.
New-Item -ItemType Directory -Force -Path $wslRoot | Out-Null
if ((Get-Item -LiteralPath $wslRoot).Attributes.HasFlag([IO.FileAttributes]::Compressed)) {
    & compact.exe /U "/S:$wslRoot" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "compact.exe /U failed on $wslRoot" }
}

New-Item -ItemType Directory -Force -Path $installDir | Out-Null
Invoke-Wsl @('--import', $distro, $installDir, $rootfs, '--version', '2')
Invoke-Wsl @('-d', $distro, '-u', 'root', '--', 'grep', '-q', 'VERSION_CODENAME=noble', '/etc/os-release')
Invoke-Wsl @('-d', $distro, '-u', 'root', '--', 'useradd', '-m', '-s', '/bin/bash', $user)

# Sent as base64: piping text through wsl.exe would add CRLF and mangle the quotes.
$conf = (@('[automount]', 'enabled=true', 'mountFsTab=false', 'options="ro"', '[interop]', 'enabled=false', 'appendWindowsPath=false', '[boot]', 'systemd=true', '[user]', "default=$user") -join "`n") + "`n"
$b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($conf))
Invoke-Wsl @('-d', $distro, '-u', 'root', '--', 'sh', '-c', "echo $b64 | base64 -d > /etc/wsl.conf")
Invoke-Wsl @('--terminate', $distro)
Write-Output "Created $distro at $installDir. Start it with: wsl -d $distro"
