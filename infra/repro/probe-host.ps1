#requires -Version 5.1
<#
.SYNOPSIS
    One-command tbound host capability report for a Linux evaluation host
    (native box or VM reached over SSH, or a WSL distro for development smoke).

.DESCRIPTION
    Reuses infra/guest/probe-capabilities.sh BY PATH (never edits it), runs it
    against the selected target, appends a live Cosign check, and prints a
    PASS/FAIL/UNVERIFIED summary with a plain-language "what to fix" footer.
    The report is a capability snapshot for the thesis profile freeze; it is
    not containment evidence and does not establish G1.

    UNVERIFIED is not success. Two probe items are UNVERIFIED by design of the
    read-only probe (cgroup.kill-write is never written; uidmap helper mapping
    behavior is not inferred from presence).

.EXAMPLE
    .\probe-host.ps1 -Target Ssh -HostName 172.30.32.185 -UserName tboundadmin -KeyFile $env:USERPROFILE\.ssh\id_ed25519
    .\probe-host.ps1 -Target Wsl -Distro Ubuntu-24.04
    .\probe-host.ps1 -Target File -ReportFile .\probe-capture.txt
    .\probe-host.ps1 -Target Ssh -HostName 172.30.32.185 -UserName tboundadmin -OutFile host-report.txt

.NOTES
    Exit codes: 0 no FAIL and no unexpected UNVERIFIED; 1 a FAIL (including
    mixed FAIL/UNVERIFIED); 2 tool or invocation error; 3 the sole FAIL is a
    measured exact D09 ABI mismatch with no unexpected UNVERIFIED; 4 unexpected
    UNVERIFIED when there is no FAIL.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Wsl', 'Ssh', 'File')][string]$Target,
    # Wsl mode
    [string]$Distro,
    [string]$WslUser,
    # Ssh mode
    [string]$HostName,
    [string]$UserName,
    [string]$KeyFile,
    [int]$Port = 22,
    # File mode
    [string]$ReportFile,
    # Shared
    [string]$RepoRoot,
    [string]$OutFile
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'landlock-profile-gate.ps1')
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }

# ---------------------------------------------------------------------------
# Resolve inputs
# ---------------------------------------------------------------------------

if (-not $RepoRoot) {
    $RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}
$RepoRoot = [IO.Path]::GetFullPath($RepoRoot)
$probeScript = Join-Path $RepoRoot 'infra\guest\probe-capabilities.sh'
if (-not (Test-Path -LiteralPath $probeScript -PathType Leaf)) {
    throw "Probe script not found at '$probeScript'; run this from a checkout that contains infra/guest/probe-capabilities.sh."
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

$probeOutputText = $null
$probeExitCode = $null
$targetDescription = $null

switch ($Target) {
    'Wsl' {
        if (-not $Distro) { throw 'Wsl mode requires -Distro (see: wsl --list --verbose).' }
        $wslProbe = ConvertTo-WslPath -WindowsPath $probeScript
        $targetDescription = "WSL distro '$Distro'"
        Write-Host "Running the read-only probe inside WSL distro '$Distro' (no container is started, no image is pulled)..."
        $wslArgs = @('-d', $Distro)
        if ($WslUser) { $wslArgs += @('-u', $WslUser) }
        $wslArgs += @('--', 'bash', $wslProbe)
        $raw = & wsl.exe @wslArgs 2>&1
        $probeExitCode = $LASTEXITCODE
        $probeOutputText = (($raw | Out-String) -replace "`0", '')
        if ($probeExitCode -ne 0 -and $probeOutputText -match 'DISTRO_NOT_FOUND|keine Verteilung|no distribution') {
            throw "WSL distro '$Distro' was not found. List distributions with: wsl --list --verbose"
        }
        # Live Cosign check (the probe itself does not cover Cosign).
        Write-Host "Checking Cosign inside WSL distro '$Distro'..."
        $cosignSnippet = 'if command -v cosign >/dev/null 2>&1; then cosign version 2>/dev/null | head -n 1; else echo COSIGN_MISSING; fi'
        $cosignArgs = @('-d', $Distro)
        if ($WslUser) { $cosignArgs += @('-u', $WslUser) }
        $cosignArgs += @('--', 'bash', '-lc', $cosignSnippet)
        $cosignRaw = & wsl.exe @cosignArgs 2>&1
        $script:cosignLine = (($cosignRaw | Out-String) -replace "`0", '').Trim()
    }
    'Ssh' {
        if (-not $HostName) { throw 'Ssh mode requires -HostName.' }
        $sshExe = (Get-Command ssh.exe -ErrorAction Stop).Source
        # BatchMode: key-based only; a password prompt fails closed instead of hanging.
        # Require a previously verified host key in known_hosts. BatchMode keeps
        # this report non-interactive; it must not silently trust a first-seen key.
        $sshArgs = @('-p', $Port, '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'ConnectTimeout=10')
        if ($KeyFile) {
            $resolvedKey = (Resolve-Path -LiteralPath $KeyFile -ErrorAction Stop).Path
            $sshArgs += @('-i', ('"{0}"' -f $resolvedKey))
        }
        $remoteTarget = if ($UserName) { '{0}@{1}' -f $UserName, $HostName } else { $HostName }
        $targetDescription = "SSH host '$remoteTarget' (port $Port)"

        # Feed the probe bytes exactly (LF-normalized) via cmd redirection; a
        # PowerShell pipeline would inject CRLF and break the embedded heredoc.
        $normalized = ([IO.File]::ReadAllText($probeScript)) -replace "`r`n", "`n"
        $tmpInput = Join-Path ([IO.Path]::GetTempPath()) ('tbound-probe-stdin-{0}.sh' -f [guid]::NewGuid().ToString('N'))
        [IO.File]::WriteAllText($tmpInput, $normalized, (New-Object System.Text.UTF8Encoding($false)))
        try {
            $cmdline = '"{0}" {1} "{2}" bash -s < "{3}" 2>&1' -f $sshExe, ($sshArgs -join ' '), $remoteTarget, $tmpInput
            Write-Host "Running the read-only probe over SSH on '$remoteTarget' (no container is started, no image is pulled)..."
            $raw = & cmd.exe /c $cmdline
            $probeExitCode = $LASTEXITCODE
        }
        finally {
            Remove-Item -LiteralPath $tmpInput -Force -ErrorAction SilentlyContinue
        }
        $probeOutputText = ($raw | Out-String)
        if ($probeExitCode -eq 255 -and $probeOutputText -notmatch '^(PASS|FAIL|UNVERIFIED)') {
            throw ((("SSH execution failed (exit 255) before the probe could run. Check the host, user, key, and that " +
                    "'bash' exists remotely. Output: {0}") -f $probeOutputText.Trim()))
        }
        # Live Cosign check (the probe itself does not cover Cosign).
        Write-Host "Checking Cosign on '$remoteTarget'..."
        $cosignCmd = 'if command -v cosign >/dev/null 2>&1; then cosign version 2>/dev/null | head -n 1; else echo COSIGN_MISSING; fi'
        $cosignOut = (& cmd.exe /c ('"{0}" {1} "{2}" bash -c "{3}" 2>&1' -f $sshExe, ($sshArgs -join ' '), $remoteTarget, ($cosignCmd -replace '"', '\"'))) -join "`n"
        $script:cosignLine = ($cosignOut | Out-String).Trim()
    }
    'File' {
        if (-not $ReportFile) { throw 'File mode requires -ReportFile (a saved probe-capabilities.sh output capture).' }
        $resolved = (Resolve-Path -LiteralPath $ReportFile -ErrorAction Stop).Path
        $targetDescription = "saved probe capture '$resolved'"
        $probeOutputText = [IO.File]::ReadAllText($resolved)
        $probeExitCode = $null
        $script:cosignLine = $null
    }
}

# ---------------------------------------------------------------------------
# Parse probe output
# ---------------------------------------------------------------------------

$parsedProbe = ConvertFrom-TBoundProbeOutput -Text $probeOutputText
$rows = New-Object 'System.Collections.Generic.List[object]'
foreach ($row in $parsedProbe.Rows) { $rows.Add($row) }
$observedHeader = $parsedProbe.ObservedHeader
$probeSummaryLine = $parsedProbe.ProbeSummaryLine
if ($rows.Count -eq 0) {
    throw ((("No PASS/FAIL/UNVERIFIED rows were found in the probe output for {0}. " +
            "Raw output start: {1}") -f $targetDescription, ($probeOutputText.Trim().Substring(0, [Math]::Min(200, $probeOutputText.Trim().Length)))))
}

# Live Cosign row (File mode: not observable).
if ($Target -eq 'File') {
    $rows.Add([pscustomobject]@{
            Status = 'UNVERIFIED'
            Name   = 'cosign-cli'
            Detail = 'not observable from a saved capture; run -Target Ssh or -Target Wsl for the live check'
        })
}
else {
    $cosignText = [string]$script:cosignLine
    if ($cosignText -match 'COSIGN_MISSING' -or [string]::IsNullOrWhiteSpace(($cosignText -replace "`0", ''))) {
        $rows.Add([pscustomobject]@{
                Status = 'FAIL'
                Name   = 'cosign-cli'
                Detail = 'cosign executable is missing (required by the offline verifier)'
            })
    }
    elseif ($cosignText -match 'v?(\d+\.\d+\.\d+)') {
        $cosignVersion = $Matches[1]
        if ($cosignVersion -eq '3.1.3') {
            $rows.Add([pscustomobject]@{
                    Status = 'PASS'
                    Name   = 'cosign-cli'
                    Detail = "cosign reports version $cosignVersion (matches the pinned v3.1.3)"
                })
        }
        else {
            $rows.Add([pscustomobject]@{
                    Status = 'FAIL'
                    Name   = 'cosign-cli'
                    Detail = "cosign reports version $cosignVersion; the evaluation profile pins v3.1.3"
                })
        }
    }
    else {
        $rows.Add([pscustomobject]@{
                Status = 'UNVERIFIED'
                Name   = 'cosign-cli'
                Detail = ("cosign ran but its version could not be parsed from: {0}" -f ($cosignText -replace "`0", '').Trim())
            })
    }
}

# ---------------------------------------------------------------------------
# Summary, remediation footer, and report assembly
# ---------------------------------------------------------------------------

$passCount = @($rows | Where-Object Status -eq 'PASS').Count
$failCount = @($rows | Where-Object Status -eq 'FAIL').Count
$unverifiedCount = @($rows | Where-Object Status -eq 'UNVERIFIED').Count

function Get-Remediation {
    param([Parameter(Mandatory)][string]$Name, [AllowEmptyString()][string]$Detail)
    switch ($Name) {
        'user-namespace' {
            if ($Detail -match 'probe must run as the non-root trial account') {
                return 'Run the probe as the dedicated non-root trial user (not root, not sudo).'
            }
            if ($Detail -match 'uid_map|Operation not permitted') {
                return ('Ubuntu 24.04 AppArmor restriction. As root: create /etc/sysctl.d/99-tbound-userns.conf with ' +
                        '"kernel.apparmor_restrict_unprivileged_userns = 0", run "sudo sysctl -w ' +
                        'kernel.apparmor_restrict_unprivileged_userns=0", then re-run the probe. ' +
                        'bootstrap-linux.sh Step 4 does this after explicit confirmation.')
            }
            return 'Inspect the detail line; unshare must succeed for the non-root trial user.'
        }
        'landlock-abi' {
            return 'The live Landlock ABI query did not establish availability; inspect the measured result and query error.'
        }
        'd09-landlock-profile' {
            return ('The registered D09 profile requires exactly Landlock ABI 8. Ubuntu 24.04 HWE may provide ABI 8 ' +
                    'on an older GA installation; record and measure the running system after reboot. A later ABI ' +
                    'requires explicit protocol/profile reconciliation.')
        }
        'pidfd' { return 'Kernel too old for pidfd_open; upgrade the kernel (on Ubuntu 24.04: the HWE stack).' }
        'seccomp-filter' { return 'Kernel lacks seccomp filter mode; check kernel configuration or upgrade (HWE).' }
        'subuid' { return 'Add a range of at least 65536 subordinate UIDs for the trial user (bootstrap-linux.sh Step 1).' }
        'subgid' { return 'Add a range of at least 65536 subordinate GIDs for the trial user (bootstrap-linux.sh Step 1).' }
        'uidmap-helpers' {
            return 'Install the uidmap package so newuidmap/newgidmap exist ("sudo apt-get install uidmap").'
        }
        'podman-rootless-crun' {
            return ('Install rootless Podman with the crun runtime ("sudo apt-get install podman crun uidmap") and ' +
                    'run the probe as the non-root trial user after logging in again.')
        }
        'podman-subid-maps' {
            return 'Configure the subordinate UID/GID ranges, then log the trial user out and in again before re-running.'
        }
        'node-runtime' {
            return 'Pinned Node v24.21.0 missing at /opt/tbound/toolchains/node-v24.21.0-linux-x64; run infra/guest/setup-ubuntu-24.04.sh (via bootstrap-linux.sh Step 2).'
        }
        'go-runtime' {
            return 'Pinned Go 1.27.1 missing at /opt/tbound/toolchains/go1.27.1; run infra/guest/setup-ubuntu-24.04.sh (via bootstrap-linux.sh Step 2).'
        }
        'cgroup-v2' { return 'No cgroup2 mount visible; the profile requires a real systemd host with cgroup v2 (not WSL1, not a plain container).' }
        'cgroup-domain' { return 'The session cgroup must be a domain cgroup under a delegated user session; check systemd user session setup.' }
        'cgroup.kill-interface' { return 'cgroup.kill must exist on the session cgroup; check cgroup v2 delegation for the user session.' }
        'cosign-cli' {
            return ('Install the pinned Cosign static binary under /opt/tbound/toolchains with a /usr/local/bin/cosign ' +
                    'symlink (bootstrap-linux.sh Step 3; Cosign is not packaged for Ubuntu 24.04).')
        }
        'probe-runtime' { return 'python3 is required on the host to run the probe.' }
        default { return 'Inspect the detail line above, fix the underlying condition, and re-run.' }
    }
}

$report = New-Object System.Text.StringBuilder
[void]$report.AppendLine('TBound host capability report')
[void]$report.AppendLine("Generated:   $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss K')")
[void]$report.AppendLine("Target:      $targetDescription")
if ($observedHeader) { [void]$report.AppendLine("Probe view:  $observedHeader") }
[void]$report.AppendLine('')
[void]$report.AppendLine('Check                          Status      Detail')
foreach ($row in $rows) {
    $line = ('{0,-30} {1,-11} {2}' -f $row.Name, $row.Status, $row.Detail)
    [void]$report.AppendLine($line)
}
[void]$report.AppendLine('')
[void]$report.AppendLine("Summary: PASS=$passCount FAIL=$failCount UNVERIFIED=$unverifiedCount")
if ($probeSummaryLine) { [void]$report.AppendLine("Probe self-report: $probeSummaryLine") }
if ($null -ne $probeExitCode) { [void]$report.AppendLine("Probe exit code: $probeExitCode (0 pass, 1 fail, 2 unverified)") }
[void]$report.AppendLine('')
[void]$report.AppendLine('--- Full probe output ---')
[void]$report.Append($probeOutputText)
if (-not $probeOutputText.EndsWith("`n")) { [void]$report.AppendLine('') }
[void]$report.AppendLine('--- What to fix ---')

$fixes = @()
foreach ($row in ($rows | Where-Object { $_.Status -eq 'FAIL' })) {
    $fixes += ('FAIL {0}: {1}' -f $row.Name, (Get-Remediation -Name $row.Name -Detail $row.Detail))
}
foreach ($row in ($rows | Where-Object { $_.Status -eq 'UNVERIFIED' })) {
    if ($row.Name -in @('cgroup.kill-write', 'uidmap-helpers', 'cosign-cli')) {
        if ($row.Name -eq 'cosign-cli' -and $Target -eq 'File') {
            $fixes += ('UNVERIFIED {0}: {1}' -f $row.Name, $row.Detail)
        }
        else {
            $fixes += ('UNVERIFIED {0}: expected by design of the read-only probe ({1})' -f $row.Name, $row.Detail)
        }
    }
    else {
        $fixes += ('UNVERIFIED {0}: {1}' -f $row.Name, $row.Detail)
    }
}
if ($fixes.Count -eq 0) {
    [void]$report.AppendLine('Nothing to fix: every check passed and only the by-design UNVERIFIED items remain.')
}
else {
    foreach ($fix in $fixes) { [void]$report.AppendLine($fix) }
}
[void]$report.AppendLine('')
$landlockAbi = Get-TBoundLandlockAbi -Rows $rows
[void]$report.AppendLine('D09 Landlock profile requirement: exactly ABI 8 (LANDLOCK_RESTRICT_SELF_TSYNC).')
if ($null -ne $landlockAbi) {
    if ($landlockAbi -eq '8') {
        [void]$report.AppendLine('Observed ABI 8 matches the exact D09 profile requirement.')
    }
    elseif ($landlockAbi -match '^[1-7]$') {
        [void]$report.AppendLine("Observed ABI $landlockAbi is below the exact requirement; HWE may provide ABI 8 on this installation. Measure again after reboot.")
    }
    else {
        [void]$report.AppendLine("Observed ABI $landlockAbi does not match the exact requirement; reconcile a later ABI with the protocol/profile.")
    }
}
else {
    [void]$report.AppendLine('The Landlock ABI value could not be read from this report; verify it on the host.')
}
[void]$report.AppendLine('Reminder: UNVERIFIED is not success; two probe items are UNVERIFIED by design (cgroup.kill-write is never written; uidmap helper behavior is not inferred from presence).')
[void]$report.AppendLine('This report is a host capability snapshot for the thesis profile freeze. It is not containment evidence and does not establish G1 (see docs/reproducible-setup.md).')

$reportText = $report.ToString()
Write-Output $reportText
if ($OutFile) {
    [IO.File]::WriteAllText($OutFile, $reportText, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ''
    Write-Host "Report saved to: $((Resolve-Path -LiteralPath ([IO.Path]::GetFullPath($OutFile))).Path)"
}

# ---------------------------------------------------------------------------
# Exit status
# ---------------------------------------------------------------------------

$exitCode = Get-TBoundProbeExitCode -Rows $rows -Target $Target -ProbeExitCode $probeExitCode
exit $exitCode
