function ConvertTo-TBoundCanonicalDecimal {
    param([Parameter(Mandatory)][string]$Value)
    if ($Value -notmatch '^[0-9]+$') { return $null }
    return ($Value -replace '^0+(?=\d)', '')
}

function ConvertFrom-TBoundProbeOutput {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text)

    $rows = New-Object 'System.Collections.Generic.List[object]'
    $observedHeader = $null
    $summaryLine = $null
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^(PASS|FAIL|UNVERIFIED)\s+([A-Za-z0-9_.-]+):\s?(.*)$') {
            $rows.Add([pscustomobject]@{
                Status = $Matches[1]
                Name   = $Matches[2]
                Detail = $Matches[3]
            })
        }
        elseif ($line -match '^Observed:\s+(.*)$') {
            $observedHeader = $Matches[1].Trim()
        }
        elseif ($line -match '^Summary:\s+(.*)$') {
            $summaryLine = $Matches[1].Trim()
        }
    }
    return [pscustomobject]@{
        Rows             = $rows.ToArray()
        ObservedHeader   = $observedHeader
        ProbeSummaryLine = $summaryLine
    }
}

function Get-TBoundLandlockAbi {
    param([Parameter(Mandatory)][object[]]$Rows)

    $abiRows = @($Rows | Where-Object { $_.Name -eq 'landlock-abi' })
    if ($abiRows.Count -ne 1 -or $abiRows[0].Status -ne 'PASS') { return $null }
    if ($abiRows[0].Detail -notmatch '(?:^|\s)ABI\s+([0-9]+)(?:\s|$)') { return $null }
    $abi = ConvertTo-TBoundCanonicalDecimal -Value $Matches[1]
    if ($null -eq $abi -or $abi -eq '0') { return $null }
    return $abi
}

function Get-TBoundProbeExitCode {
    param(
        [Parameter(Mandatory)][object[]]$Rows,
        [Parameter(Mandatory)][ValidateSet('Wsl', 'Ssh', 'File')][string]$Target,
        [AllowNull()][Nullable[int]]$ProbeExitCode
    )

    $failures = @($Rows | Where-Object { $_.Status -eq 'FAIL' })
    $unverified = @($Rows | Where-Object { $_.Status -eq 'UNVERIFIED' })
    $unexpectedUnverified = @($unverified | Where-Object {
        $_.Name -notin @('cgroup.kill-write', 'uidmap-helpers')
    })
    $landlockAbi = Get-TBoundLandlockAbi -Rows $Rows
    $profileRows = @($Rows | Where-Object { $_.Name -eq 'd09-landlock-profile' })
    $profileFailure = @($profileRows | Where-Object { $_.Status -eq 'FAIL' })
    $abiMismatch = $null -ne $landlockAbi -and $landlockAbi -ne '8'

    if ($Target -eq 'File' -and @($Rows | Where-Object { $_.Name -eq 'cosign-cli' }).Count -gt 0) {
        $unexpectedUnverified = @($unexpectedUnverified | Where-Object { $_.Name -ne 'cosign-cli' })
    }

    if ($failures.Count -gt 0) {
        if (
            $ProbeExitCode -in @($null, 1) -and
            $failures.Count -eq 1 -and
            $failures[0].Name -eq 'd09-landlock-profile' -and
            $profileRows.Count -eq 1 -and
            $profileFailure.Count -eq 1 -and
            $profileRows[0].Detail -match 'requires exactly Landlock ABI 8' -and
            $landlockAbi -and
            $abiMismatch -and
            $unexpectedUnverified.Count -eq 0
        ) {
            return 3
        }
        # Existing FAIL precedence remains in force, including mixed FAIL/UNVERIFIED.
        return 1
    }

    $probeUnverified = @($unverified | Where-Object { $_.Name -ne 'cosign-cli' })
    $genericRows = @($Rows | Where-Object { $_.Name -eq 'landlock-abi' })
    if ($genericRows.Count -ne 1 -or $profileRows.Count -ne 1) { return 2 }
    if ($genericRows[0].Status -eq 'PASS' -and ($landlockAbi -ne '8' -or $profileRows[0].Status -ne 'PASS')) { return 2 }
    if ($genericRows[0].Status -eq 'UNVERIFIED' -and $profileRows[0].Status -ne 'UNVERIFIED') { return 2 }
    if ($genericRows.Count -eq 1 -and $genericRows[0].Status -eq 'PASS' -and $null -eq $landlockAbi) { return 2 }
    if ($null -ne $ProbeExitCode -and (
        $ProbeExitCode -notin @(0, 1, 2) -or
        $ProbeExitCode -eq 1 -or
        ($ProbeExitCode -eq 0 -and $probeUnverified.Count -gt 0) -or
        ($ProbeExitCode -eq 2 -and $probeUnverified.Count -eq 0)
    )) { return 2 }
    if ($abiMismatch) { return 1 }
    if ($unexpectedUnverified.Count -gt 0) { return 4 }
    return 0
}
