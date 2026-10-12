[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$expectedOperatorSid = 'S-1-5-21-2350865082-651554413-1548572510-1004'
$expectedHostAlias = '172.19.207.142'
$expectedHostFingerprint = 'SHA256:22ZTQXN5L54AhovENJ0Eo7ZViomYVvlHRWfbG3izY5g'
$policyPath = Join-Path $PSScriptRoot 'TBoundHostAccessPolicy.psm1'
$reviewedPolicySha256 = 'C06BAE160364EB4D88914A3468FBB766C426010145ACA9D2C43DD803A993BB15'
$programDataRoot = Join-Path $env:ProgramData 'TBoundHostAutomation'
$operatorKeyRoot = Join-Path $programDataRoot 'operator-ssh'
$operatorPublicRoot = Join-Path $programDataRoot 'operator-public'
$privateKeyPath = Join-Path $operatorKeyRoot 'guest-maintenance-ed25519'
$publicKeyPath = Join-Path $operatorPublicRoot 'guest-maintenance-ed25519.pub'
$knownHostsPath = Join-Path $operatorPublicRoot 'known_hosts'
$sshKeygen = Join-Path $env:WINDIR 'System32\OpenSSH\ssh-keygen.exe'

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if ($null -eq $identity.User -or $identity.User.Value -cne $expectedOperatorSid) {
    throw 'Run this key initializer as FABIAN\CodexSandboxOffline, not from the elevated Admin account.'
}
if (-not (Test-Path -LiteralPath $policyPath -PathType Leaf)) { throw 'Protected access policy module is missing.' }
$policyBytes = [IO.File]::ReadAllBytes($policyPath)
$sha = [Security.Cryptography.SHA256]::Create()
try { $policyHash = ([BitConverter]::ToString($sha.ComputeHash($policyBytes))).Replace('-', '') } finally { $sha.Dispose() }
if ($policyHash -cne $reviewedPolicySha256) { throw 'Protected access policy module hash differs from the reviewed source.' }
Import-Module -Name $policyPath -Force -ErrorAction Stop

foreach ($directory in @($programDataRoot, $operatorKeyRoot, $operatorPublicRoot)) {
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) { throw ('Protected operator access directory is missing: ' + $directory) }
    $item = Get-Item -LiteralPath $directory -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw ('Protected operator access directory is a reparse point: ' + $directory) }
}
if (-not (Test-TBoundOperatorKeyDirectorySecurity -Acl (Get-Acl -LiteralPath $operatorKeyRoot) -OperatorSid $expectedOperatorSid)) {
    throw 'Private-key directory ACL is not restricted to the exact operator SID.'
}
if (-not (Test-TBoundProtectedPathSecurity -Acl (Get-Acl -LiteralPath $programDataRoot) -Directory $true -OperatorSid $expectedOperatorSid -OperatorAccess Read)) {
    throw 'Protected ProgramData root ACL is not the fixed operator-read policy.'
}
if (-not (Test-TBoundProtectedPathSecurity -Acl (Get-Acl -LiteralPath $operatorPublicRoot) -Directory $true -OperatorSid $expectedOperatorSid -OperatorAccess Modify)) {
    throw 'Operator public-data directory ACL is not the fixed policy.'
}
if (-not (Test-Path -LiteralPath $knownHostsPath -PathType Leaf)) { throw 'Pinned guest known_hosts file is missing.' }
if (-not (Test-TBoundProtectedPathSecurity -Acl (Get-Acl -LiteralPath $knownHostsPath) -Directory $false -OperatorSid $expectedOperatorSid -OperatorAccess Read)) {
    throw 'Admin-installed known_hosts ACL does not match the protected read-only policy.'
}
if ((Test-Path -LiteralPath $privateKeyPath) -or (Test-Path -LiteralPath ($privateKeyPath + '.pub')) -or (Test-Path -LiteralPath $publicKeyPath)) {
    throw 'A dedicated operator key already exists; refusing to overwrite or adopt it.'
}
if (-not (Test-Path -LiteralPath $sshKeygen -PathType Leaf)) { throw ('Windows OpenSSH ssh-keygen is missing: ' + $sshKeygen) }

$knownHostMatches = @(& $sshKeygen -F $expectedHostAlias -f $knownHostsPath 2>$null)
if ($LASTEXITCODE -ne 0) { throw 'Admin-installed known_hosts lacks the exact guest host alias.' }
$knownHostLines = @($knownHostMatches | Where-Object { $_ -and -not $_.StartsWith('#') })
if ($knownHostLines.Count -ne 1 -or $knownHostLines[0] -cnotmatch ('^' + [regex]::Escape($expectedHostAlias) + '\s+ssh-ed25519\s+\S+(?:\s+.*)?$')) {
    throw 'Admin-installed known_hosts must contain one ED25519 record for the exact guest host alias.'
}
$fingerprintPath = Join-Path ([IO.Path]::GetTempPath()) ('tbound-operator-hostkey-' + [guid]::NewGuid().ToString('N') + '.txt')
try {
    [IO.File]::WriteAllText($fingerprintPath, ($knownHostLines[0] + "`n"), [Text.UTF8Encoding]::new($false))
    $fingerprintLines = @(& $sshKeygen -lf $fingerprintPath 2>$null)
    if ($LASTEXITCODE -ne 0 -or -not (Test-TBoundPinnedEd25519Fingerprint -Lines $fingerprintLines -ExpectedFingerprint $expectedHostFingerprint)) {
        throw 'Admin-installed known_hosts content differs from the fixed guest ED25519 host fingerprint.'
    }
}
finally { Remove-Item -LiteralPath $fingerprintPath -Force -ErrorAction SilentlyContinue }

$arguments = '-q -t ed25519 -C tbound-codex-operator -f "' + $privateKeyPath + '" -N ""'
$startInfo = [Diagnostics.ProcessStartInfo]::new()
$startInfo.FileName = $sshKeygen
$startInfo.Arguments = $arguments
$startInfo.UseShellExecute = $false
$startInfo.CreateNoWindow = $true
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
$process = [Diagnostics.Process]::Start($startInfo)
$stdout = $process.StandardOutput.ReadToEnd()
$stderr = $process.StandardError.ReadToEnd()
$process.WaitForExit()
if ($process.ExitCode -ne 0) {
    throw ('ssh-keygen failed without changing an existing key: ' + (($stderr -replace '[\r\n]+', ' ').Trim()))
}
if (-not (Test-Path -LiteralPath $privateKeyPath -PathType Leaf) -or -not (Test-Path -LiteralPath ($privateKeyPath + '.pub') -PathType Leaf)) {
    throw 'ssh-keygen did not create both expected key files.'
}

$privateAcl = New-TBoundOperatorPrivateKeySecurity -OperatorSid $expectedOperatorSid
Set-Acl -LiteralPath $privateKeyPath -AclObject $privateAcl -ErrorAction Stop
$privateReadback = Get-Acl -LiteralPath $privateKeyPath -ErrorAction Stop
if (-not (Test-TBoundOperatorPrivateKeySecurity -Acl $privateReadback -OperatorSid $expectedOperatorSid)) {
    throw 'Generated private key ACL read-back is not operator-only.'
}

$publicLine = [IO.File]::ReadAllText(($privateKeyPath + '.pub'), [Text.Encoding]::UTF8).Trim()
if ($publicLine -cnotmatch '^ssh-ed25519 [A-Za-z0-9+/]+={0,2} tbound-codex-operator$') {
    throw 'Generated public key does not match the fixed Ed25519 key profile.'
}
[IO.File]::WriteAllText($publicKeyPath, ($publicLine + "`n"), [Text.UTF8Encoding]::new($false))
$publicReadback = [IO.File]::ReadAllText($publicKeyPath, [Text.Encoding]::UTF8).Trim()
if ($publicReadback -cne $publicLine) { throw 'Public key outbox read-back differs from the generated public key.' }

$fingerprintLines = @(& $sshKeygen -lf $publicKeyPath 2>$null)
if ($LASTEXITCODE -ne 0 -or $fingerprintLines.Count -ne 1 -or $fingerprintLines[0] -notmatch '\(ED25519\)') {
    throw 'Could not compute the generated public key fingerprint.'
}
Write-Host ('Created operator-only Ed25519 key. Public key fingerprint: ' + $fingerprintLines[0].Trim())
Write-Host ('Public key for one-time Admin enrollment: ' + $publicKeyPath)
Write-Host 'The private key contents were not read or printed.'
