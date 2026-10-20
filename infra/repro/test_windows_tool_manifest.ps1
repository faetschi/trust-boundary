#requires -Version 5.1
# Mock test for the portable Windows tool-manifest parser. Runs on any
# PowerShell host; it does not inspect Windows binaries or signatures.
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$scriptPath = Join-Path $PSScriptRoot 'bootstrap-hyperv-windows.ps1'
$content = Get-Content -LiteralPath $scriptPath -Raw -ErrorAction Stop
$start = $content.IndexOf('function Read-WindowsToolManifest {')
if ($start -lt 0) { throw 'Read-WindowsToolManifest not found in bootstrap-hyperv-windows.ps1' }
$endMarker = "`n}`n`nfunction Assert-TrustedWindowsExecutable"
$end = $content.IndexOf($endMarker, $start)
if ($end -lt 0) { throw 'Read-WindowsToolManifest end not found' }
$functionText = $content.Substring($start, $end - $start + 3)

function Assert-NoReparsePathComponents { param([Parameter(Mandatory)][string]$Path) }
$script:WindowsToolNames = @('wsl.exe', 'cmd.exe', 'ssh.exe', 'ssh-keyscan.exe', 'ssh-keygen.exe', 'scp.exe')
Invoke-Expression $functionText

$dir = Join-Path ([IO.Path]::GetTempPath()) ('tbound-manifest-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $dir | Out-Null
try {
    $valid = Join-Path $dir 'valid.json'
    ('{"cmd.exe":"' + ('a' * 64) + '","ssh.exe":"' + ('b' * 64) + '"}') |
        Set-Content -LiteralPath $valid -NoNewline
    $hashes = Read-WindowsToolManifest -Path $valid
    if ($hashes.Count -ne 2 -or $hashes['cmd.exe'] -ne ('a' * 64)) {
        throw 'valid manifest was not parsed exactly'
    }

    $empty = Join-Path $dir 'empty.json'
    '{}' | Set-Content -LiteralPath $empty -NoNewline
    if ((Read-WindowsToolManifest -Path $empty).Count -ne 0) {
        throw 'empty manifest did not return an empty table'
    }

    $bad = Join-Path $dir 'bad.json'
    '{"cmd.exe":"short"}' | Set-Content -LiteralPath $bad -NoNewline
    try {
        Read-WindowsToolManifest -Path $bad | Out-Null
        throw 'invalid SHA-256 was accepted'
    }
    catch {
        if ($_.Exception.Message -notmatch 'SHA-256 is invalid') { throw }
    }

    $unknown = Join-Path $dir 'unknown.json'
    ('{"other.exe":"' + ('c' * 64) + '"}') | Set-Content -LiteralPath $unknown -NoNewline
    try {
        Read-WindowsToolManifest -Path $unknown | Out-Null
        throw 'unknown tool was accepted'
    }
    catch {
        if ($_.Exception.Message -notmatch 'unknown tool') { throw }
    }

    Write-Output 'Windows tool manifest mock tests: PASS'
}
finally {
    Remove-Item -LiteralPath $dir -Recurse -Force -ErrorAction SilentlyContinue
}
