$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'landlock-profile-gate.ps1')

function Assert-GateCase {
    param(
        [string]$Name,
        [string]$Text,
        [int]$Expected,
        [AllowNull()][Nullable[int]]$ProbeExitCode
    )
    $parsed = ConvertFrom-TBoundProbeOutput -Text $Text
    $actual = Get-TBoundProbeExitCode -Rows $parsed.Rows -Target Ssh -ProbeExitCode $ProbeExitCode
    if ($actual -ne $Expected) {
        throw "$Name`: expected exit $Expected, got $actual"
    }
}

$abi4 = @'
PASS        landlock-abi: landlock_create_ruleset VERSION query returned ABI 4
FAIL        d09-landlock-profile: registered D09 profile requires exactly Landlock ABI 8; kernel reports ABI 4
UNVERIFIED  cgroup.kill-write: not written
UNVERIFIED  uidmap-helpers: behavior not inferred
'@
$abi8 = @'
PASS        landlock-abi: landlock_create_ruleset VERSION query returned ABI 8
PASS        d09-landlock-profile: registered D09 profile requires exactly Landlock ABI 8
UNVERIFIED  cgroup.kill-write: not written
UNVERIFIED  uidmap-helpers: behavior not inferred
'@
$abi9LeadingZero = @'
PASS        landlock-abi: landlock_create_ruleset VERSION query returned ABI 0009
FAIL        d09-landlock-profile: registered D09 profile requires exactly Landlock ABI 8; kernel reports ABI 0009
'@
$abi4Unexpected = $abi4 + "`nUNVERIFIED  extra-check: unavailable"
$abi4OtherFail = $abi4 + "`nFAIL        other-check: failed"
$unavailable = @'
FAIL        landlock-abi: kernel reports Landlock unavailable
FAIL        d09-landlock-profile: kernel reports Landlock unavailable
'@
$denied = @'
UNVERIFIED  landlock-abi: policy denied the ABI query
UNVERIFIED  d09-landlock-profile: policy denied the ABI query
'@
$otherError = @'
FAIL        landlock-abi: ABI query failed
UNVERIFIED  d09-landlock-profile: exact ABI could not be measured
'@
$existingFail = @'
PASS        landlock-abi: landlock_create_ruleset VERSION query returned ABI 8
PASS        d09-landlock-profile: registered D09 profile requires exactly Landlock ABI 8
FAIL        user-namespace: failed
UNVERIFIED  extra-check: unavailable
'@
$unexpectedOnly = @'
PASS        landlock-abi: landlock_create_ruleset VERSION query returned ABI 8
PASS        d09-landlock-profile: registered D09 profile requires exactly Landlock ABI 8
UNVERIFIED  extra-check: unavailable
'@
$nonDecimalAbi = @'
PASS        landlock-abi: landlock_create_ruleset VERSION query returned ABI nope
PASS        d09-landlock-profile: registered D09 profile requires exactly Landlock ABI 8
'@
$passWithUnexpectedUnverified = @'
PASS        landlock-abi: landlock_create_ruleset VERSION query returned ABI 8
PASS        d09-landlock-profile: registered D09 profile requires exactly Landlock ABI 8
UNVERIFIED  extra-check: unavailable
'@

Assert-GateCase 'ABI 4 mismatch plus by-design rows' $abi4 3 1
Assert-GateCase 'ABI 8 exact profile' $abi8 0 2
Assert-GateCase 'ABI 9 with leading zeros' $abi9LeadingZero 3 1
Assert-GateCase 'mismatch plus unexpected UNVERIFIED' $abi4Unexpected 1 1
Assert-GateCase 'mismatch plus another FAIL' $abi4OtherFail 1 1
Assert-GateCase 'unavailable is a FAIL, not mismatch exit 3' $unavailable 1 1
Assert-GateCase 'denied query is unexpected UNVERIFIED' $denied 4 2
Assert-GateCase 'other query error preserves FAIL precedence' $otherError 1 1
Assert-GateCase 'existing FAIL plus UNVERIFIED' $existingFail 1 1
Assert-GateCase 'unexpected UNVERIFIED without FAIL' $unexpectedOnly 4 2
Assert-GateCase 'non-decimal ABI is an invocation error' $nonDecimalAbi 2 0
Assert-GateCase 'PASS exit contradicts UNVERIFIED row' $passWithUnexpectedUnverified 2 0
Assert-GateCase 'UNVERIFIED exit contradicts PASS-only rows' ($abi8 -replace '(?m)^UNVERIFIED[^\r\n]*\r?\n?', '') 2 2
Assert-GateCase 'unsupported probe exit is an invocation error' $abi8 2 9
Assert-GateCase 'existing FAIL takes precedence over invalid probe exit' $unavailable 1 9

Assert-GateCase 'missing both gate rows' 'PASS other-check: passed' 2 0
Assert-GateCase 'missing profile row' 'PASS landlock-abi: ABI 8' 2 0
$completePass = $abi8 -replace '(?m)^UNVERIFIED[^\r\n]*\r?\n?', ''
Assert-GateCase 'duplicate generic row' ($completePass + "`nPASS landlock-abi: ABI 8") 2 0
Assert-GateCase 'duplicate profile row' ($completePass + "`nPASS d09-landlock-profile: requires exactly Landlock ABI 8") 2 0
Assert-GateCase 'contradictory gate rows' ($completePass -replace 'returned ABI 8', 'returned ABI 9') 2 0
Assert-GateCase 'live Cosign unknown blocks mismatch shortcut' ($abi4 + "`nUNVERIFIED cosign-cli: query denied") 1 1
$savedCosign = ConvertFrom-TBoundProbeOutput -Text ($abi4 + "`nUNVERIFIED cosign-cli: saved capture")
if ((Get-TBoundProbeExitCode -Rows $savedCosign.Rows -Target File -ProbeExitCode $null) -ne 3) {
    throw 'Saved File Cosign reporting gap should preserve mismatch exit 3'
}
Write-Output 'PowerShell Landlock profile gate cases passed.'
