#requires -Version 5.1
#requires -RunAsAdministrator
<#
.SYNOPSIS
    Installs an unattended Ubuntu 24.04 HWE seed for an ABI-8 Hyper-V candidate.
.DESCRIPTION
    Uses the authenticated Ubuntu Server 24.04.5 ISO staged on F:. It creates
    one task-specific SSH key, remasters a copy of that ISO with a local
    NoCloud autoinstall, installs a small disposable seed VM, and leaves its
    standalone VHDX powered off for bootstrap-hyperv-windows.ps1 to clone.
    It never touches the existing development VM and never removes partial
    output. The remastered ISO is derived from the signed source; Canonical's
    signature does not cover the remastered file.
    This creates a candidate only. A live Landlock ABI result of exactly 8
    remains mandatory for the D09 host profile.
    ResumeVerifiedMedia reuses only the fixed, hash-pinned prepared ISO and
    task key from this run after exact Rock Ridge-path and private-file checks.
    FinalizeExistingSeed accepts only the one observed, already-created Off
    seed and records it after exact VM, disk, media, and key checks. It never
    creates or starts a VM.
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [ValidateNotNullOrEmpty()][string]$AssetRoot = 'F:\TBoundAssets\abi8-qualification-2026-10-14',
    [ValidateRange(30, 360)][int]$InstallTimeoutMinutes = 120,
    [ValidateRange(1, 999)][int]$MinimumFreeGb = 120,
    [switch]$ResumeVerifiedMedia,
    [switch]$FinalizeExistingSeed
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($ResumeVerifiedMedia -and $FinalizeExistingSeed) {
    throw 'ResumeVerifiedMedia and FinalizeExistingSeed are mutually exclusive.'
}

$script:IsoPath = 'F:\TBoundAssets\ubuntu-24.04.5\ubuntu-24.04.5-live-server-amd64.iso'
$script:ExpectedIsoSha256 = '97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8'
$script:AllowedAssetRoot = 'F:\TBoundAssets'
$script:VmStoreRoot = 'F:\TBoundVMs'
$script:SeedVmName = 'TBound-Ubuntu-ABI8-Seed'
$script:ExpectedSeedVmId = '82ffce59-06bf-4c1b-b43a-d406489c6d7f'
$script:FinalVmName = 'TBound-Ubuntu-2404-ABI8'
$script:SwitchName = 'Default Switch'
$script:WslDistro = 'Ubuntu'
$script:PrivateRoot = Join-Path $env:LOCALAPPDATA 'TBound\abi8-qualification'
$script:WindowsDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
$script:SystemDirectory = Join-Path $script:WindowsDirectory 'System32'
$script:WslExePath = Join-Path $script:SystemDirectory 'wsl.exe'
$script:SshKeygenPath = Join-Path $script:SystemDirectory 'OpenSSH\ssh-keygen.exe'
$script:TrustedWindowsOwner = 'NT SERVICE\TrustedInstaller'
$script:ExpectedWslExeSha256 = '4e589e3883229b7a74a4acdb878689dcec94e2539fcad1c194f415b149c337a9'
$script:ExpectedSshKeygenSha256 = '539ae392c61a53fadddf0bbb152126f3e0bc042e8a4ed50718ab16fbf872cbcc'
$script:ExpectedPreparedIsoSha256 = '1ec771177b5c8e347ea6829f40744cc9505a79d6faf79b3f7a3eda74b2f84e76'

function Assert-PathWithin {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$ParentPath)
    $full = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    $parent = [IO.Path]::GetFullPath($ParentPath).TrimEnd('\')
    if (-not $full.StartsWith(($parent + '\'), [StringComparison]::OrdinalIgnoreCase)) {
        throw "Output path is outside its approved parent: $full"
    }
    return $full
}

function Assert-NoReparsePathComponents {
    param([Parameter(Mandatory)][string]$Path)
    $fullPath = [IO.Path]::GetFullPath($Path)
    $pathRoot = [IO.Path]::GetPathRoot($fullPath)
    $currentPath = $pathRoot
    foreach ($component in ($fullPath.Substring($pathRoot.Length) -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($component)) { continue }
        $currentPath = Join-Path $currentPath $component
        if (-not (Test-Path -LiteralPath $currentPath)) { break }
        $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Path contains a junction or symbolic link: $currentPath"
        }
    }
}

function Assert-TrustedSystemTool {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedSha256,
        [Parameter(Mandatory)][string[]]$TrustedDirectories
    )
    Assert-NoReparsePathComponents -Path $Path
    foreach ($directory in $TrustedDirectories) {
        if (-not (Test-Path -LiteralPath $directory -PathType Container)) { throw "Trusted system directory is missing: $directory" }
        $directoryItem = Get-Item -LiteralPath $directory -Force -ErrorAction Stop
        if (($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
            (Get-Acl -LiteralPath $directory -ErrorAction Stop).Owner -cne $script:TrustedWindowsOwner) {
            throw "Trusted system directory is not directly owned by $($script:TrustedWindowsOwner): $directory"
        }
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "Trusted system executable is missing: $Path" }
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
        (Get-Acl -LiteralPath $Path -ErrorAction Stop).Owner -cne $script:TrustedWindowsOwner) {
        throw "Trusted system executable is not directly owned by $($script:TrustedWindowsOwner): $Path"
    }
    $actualHash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
    if ($actualHash -cne $ExpectedSha256) { throw "Trusted system executable hash mismatch: $Path" }
    $signature = Get-AuthenticodeSignature -FilePath $Path -ErrorAction Stop
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid -or
        $signature.SignerCertificate.Subject -notmatch '^CN=Microsoft Windows, O=Microsoft Corporation,') {
        throw "Trusted system executable does not have a valid Microsoft Windows signature: $Path"
    }
}

function Assert-UnoccupiedPath {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Description)
    if (Test-Path -LiteralPath $Path) {
        throw "$Description already exists; inspect it before retrying: $Path"
    }
}

function Assert-AssetRootEntryShape {
    param(
        [Parameter(Mandatory)][ValidateSet('Create', 'Resume', 'FinalizeExistingSeed')][string]$Mode,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Entries
    )

    switch ($Mode) {
        'Create' {
            if ($Entries.Count -ne 0) { throw 'Create requires an empty asset root.' }
        }
        'Resume' {
            if ($Entries.Count -ne 1 -or $Entries[0].Name -cne 'ubuntu-24.04.5-autoinstall.iso' -or $Entries[0].PSIsContainer) {
                throw 'Resume requires exactly the prepared ISO file in the asset root.'
            }
        }
        'FinalizeExistingSeed' {
            if ($Entries.Count -ne 2 -or
                @($Entries | Where-Object { $_.Name -ceq 'ubuntu-24.04.5-autoinstall.iso' -and -not $_.PSIsContainer }).Count -ne 1 -or
                @($Entries | Where-Object { $_.Name -ceq 'seed-vm' -and $_.PSIsContainer }).Count -ne 1) {
                throw 'FinalizeExistingSeed requires exactly the prepared ISO file and seed-vm directory in the asset root.'
            }
        }
    }
}

function ConvertTo-WslPath {
    param([Parameter(Mandatory)][string]$WindowsPath)
    $full = [IO.Path]::GetFullPath($WindowsPath)
    $match = [regex]::Match($full, '^([A-Za-z]):\\(.*)$')
    if (-not $match.Success) { throw "WSL input must be on a mounted drive: $full" }
    return '/mnt/' + $match.Groups[1].Value.ToLowerInvariant() + '/' + $match.Groups[2].Value.Replace('\', '/')
}

function Test-FileByteEquality {
    param([Parameter(Mandatory)][string]$LeftPath, [Parameter(Mandatory)][string]$RightPath)
    $left = $null
    $right = $null
    $leftBuffer = New-Object byte[] 65536
    $rightBuffer = New-Object byte[] 65536
    try {
        $left = [IO.File]::Open($LeftPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
        $right = [IO.File]::Open($RightPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
        if ($left.Length -ne $right.Length) { return $false }
        while ($true) {
            $leftCount = $left.Read($leftBuffer, 0, $leftBuffer.Length)
            $rightCount = $right.Read($rightBuffer, 0, $rightBuffer.Length)
            if ($leftCount -ne $rightCount) { return $false }
            if ($leftCount -eq 0) { return $true }
            for ($index = 0; $index -lt $leftCount; $index++) {
                if ($leftBuffer[$index] -ne $rightBuffer[$index]) { return $false }
            }
        }
    }
    finally {
        [Array]::Clear($leftBuffer, 0, $leftBuffer.Length)
        [Array]::Clear($rightBuffer, 0, $rightBuffer.Length)
        if ($null -ne $right) { $right.Dispose() }
        if ($null -ne $left) { $left.Dispose() }
    }
}

function Test-FileContainsUtf8Literal {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Literal)
    $data = [IO.File]::ReadAllBytes($Path)
    $needle = [Text.UTF8Encoding]::new($false, $true).GetBytes($Literal)
    try {
        for ($start = 0; $start -le ($data.Length - $needle.Length); $start++) {
            $matches = $true
            for ($offset = 0; $offset -lt $needle.Length; $offset++) {
                if ($data[$start + $offset] -ne $needle[$offset]) { $matches = $false; break }
            }
            if ($matches) { return $true }
        }
        return $false
    }
    finally {
        [Array]::Clear($data, 0, $data.Length)
        [Array]::Clear($needle, 0, $needle.Length)
    }
}

function Invoke-IsoReadback {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$IsoRelativePath,
        [Parameter(Mandatory)][scriptblock]$Verifier
    )
    if (@('boot/grub/grub.cfg', 'nocloud/user-data', 'nocloud/meta-data') -cnotcontains $IsoRelativePath) {
        throw "ISO readback path is outside the fixed verification allowlist: $IsoRelativePath"
    }
    $isoFull = [IO.Path]::GetFullPath($Path)
    Assert-NoReparsePathComponents -Path $isoFull
    $privateParent = Join-Path $env:LOCALAPPDATA 'TBound'
    if (-not [IO.Directory]::Exists($privateParent)) { $null = [IO.Directory]::CreateDirectory($privateParent) }
    Assert-NoReparsePathComponents -Path $privateParent
    $scratchRoot = Assert-PathWithin -Path (Join-Path $privateParent ('abi8-iso-readback-' + [guid]::NewGuid().ToString('N'))) -ParentPath $privateParent
    Assert-UnoccupiedPath -Path $scratchRoot -Description 'ISO readback scratch directory'
    Set-PrivateDirectory -Path $scratchRoot
    Assert-NoReparsePathComponents -Path $scratchRoot
    $scratchAcl = Get-Acl -LiteralPath $scratchRoot -ErrorAction Stop
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    if (-not $scratchAcl.AreAccessRulesProtected -or $scratchAcl.Owner -cne $identity.Name) {
        throw 'ISO readback scratch directory is not private to the current user.'
    }
    $destination = $null
    $operationResult = $null
    $operationFailure = $null
    try {
        $destination = Join-Path $scratchRoot $IsoRelativePath.Replace('/', '\')
        $destinationDirectory = Split-Path -Parent $destination
        $null = [IO.Directory]::CreateDirectory($destinationDirectory)
        Assert-NoReparsePathComponents -Path $destinationDirectory
        $null = Invoke-Xorriso -XorrisoArguments @(
            '-osirrox', 'on',
            '-indev', (ConvertTo-WslPath -WindowsPath $isoFull),
            '-extract', ('/' + $IsoRelativePath),
            (ConvertTo-WslPath -WindowsPath $destination)
        )
        if (-not [IO.File]::Exists($destination)) { throw "xorriso could not extract the exact ISO path $IsoRelativePath" }
        $item = Get-Item -LiteralPath $destination -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or $item.Length -gt 4MB) {
            throw "Extracted ISO entry is not a bounded regular file: $IsoRelativePath"
        }
        $operationResult = & $Verifier $destination
    }
    catch {
        $operationFailure = $_.Exception
    }

    $cleanupFailure = $null
    try {
        if ([IO.Directory]::Exists($scratchRoot)) {
            $safeScratchRoot = Assert-PathWithin -Path $scratchRoot -ParentPath $privateParent
            if ($safeScratchRoot -ine [IO.Path]::GetFullPath($scratchRoot)) {
                throw 'ISO readback cleanup path changed after resolution.'
            }
            Assert-NoReparsePathComponents -Path $scratchRoot
            if ($null -ne $destination -and [IO.File]::Exists($destination)) {
                $safeDestination = Assert-PathWithin -Path $destination -ParentPath $safeScratchRoot
                if ($safeDestination -ine [IO.Path]::GetFullPath($destination)) {
                    throw 'ISO readback file escaped its private scratch root.'
                }
                Assert-NoReparsePathComponents -Path $safeDestination
                $item = Get-Item -LiteralPath $safeDestination -Force -ErrorAction Stop
                $expectedLeaf = [IO.Path]::GetFileName($IsoRelativePath)
                if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
                    $item.Name -cne $expectedLeaf -or $item.Length -gt 4MB) {
                    throw 'ISO readback cleanup refused an unexpected or non-regular extracted entry.'
                }
                $fileAcl = Get-Acl -LiteralPath $safeDestination -ErrorAction Stop
                $fileOwnerSid = $fileAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value
                $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
                if ($fileOwnerSid -cne $currentSid -and $fileOwnerSid -cne 'S-1-5-32-544') {
                    throw 'ISO readback cleanup refused an extracted file with an unexpected owner.'
                }
                if ($fileAcl.Access.Count -ne 1 -or
                    $fileAcl.Access[0].IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -cne $currentSid -or
                    $fileAcl.Access[0].AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
                    -not $fileAcl.Access[0].IsInherited) {
                    throw 'ISO readback cleanup refused an extracted file with an unexpected access-control list.'
                }
                if (($item.Attributes -band [IO.FileAttributes]::ReadOnly) -ne 0) {
                    [IO.File]::SetAttributes($safeDestination, ($item.Attributes -band (-bnot [IO.FileAttributes]::ReadOnly)))
                }
                [IO.File]::Delete($safeDestination)
            }

            $directories = New-Object System.Collections.Generic.List[string]
            $currentDirectory = $safeScratchRoot
            $components = @($IsoRelativePath -split '/')
            for ($index = 0; $index -lt ($components.Length - 1); $index++) {
                $currentDirectory = Join-Path $currentDirectory $components[$index]
                $directories.Add($currentDirectory)
            }
            for ($index = $directories.Count - 1; $index -ge 0; $index--) {
                $directory = Assert-PathWithin -Path $directories[$index] -ParentPath $safeScratchRoot
                Assert-NoReparsePathComponents -Path $directory
                if ([IO.Directory]::Exists($directory)) {
                    if (@([IO.Directory]::GetFileSystemEntries($directory)).Count -ne 0) {
                        throw 'ISO readback cleanup found an unexpected entry in its private scratch tree.'
                    }
                    [IO.Directory]::Delete($directory, $false)
                }
            }
            if (@([IO.Directory]::GetFileSystemEntries($safeScratchRoot)).Count -ne 0) {
                throw 'ISO readback cleanup found an unexpected entry in the private scratch root.'
            }
            [IO.Directory]::Delete($safeScratchRoot, $false)
        }
    }
    catch {
        $cleanupFailure = $_.Exception
    }
    if ($null -ne $cleanupFailure) {
        if ($null -ne $operationFailure) {
            throw "ISO readback failed: $($operationFailure.Message). Scratch cleanup also failed: $($cleanupFailure.Message)"
        }
        throw "ISO readback scratch cleanup failed: $($cleanupFailure.Message)"
    }
    if ($null -ne $operationFailure) { throw $operationFailure }
    return $operationResult
}

function Get-IsoFileText {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$IsoRelativePath)
    return Invoke-IsoReadback -Path $Path -IsoRelativePath $IsoRelativePath -Verifier {
        param($ExtractedPath)
        return [IO.File]::ReadAllText($ExtractedPath, [Text.UTF8Encoding]::new($false, $true))
    }
}

function Test-IsoFileMatchesLocalFile {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$IsoRelativePath,
        [Parameter(Mandatory)][string]$ExpectedLocalPath
    )
    $verifier = {
        param($ExtractedPath)
        return Test-FileByteEquality -LeftPath $ExtractedPath -RightPath $ExpectedLocalPath
    }
    return Invoke-IsoReadback -Path $Path -IsoRelativePath $IsoRelativePath -Verifier $verifier
}

function Invoke-WslOpenSslHash {
    param([Parameter(Mandatory)][string]$Plaintext)
    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $script:WslExePath
    $startInfo.Arguments = '--distribution Ubuntu --user root --exec /usr/bin/openssl passwd -6 -stdin'
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $startInfo
    if (-not $process.Start()) { throw 'Could not start the WSL OpenSSL password-hash process.' }
    try {
        $process.StandardInput.WriteLine($Plaintext)
        $process.StandardInput.Close()
        $hash = $process.StandardOutput.ReadToEnd().Trim()
        $null = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        if ($process.ExitCode -ne 0 -or $hash -notmatch '^\$6\$[A-Za-z0-9./]+\$[A-Za-z0-9./]+$') {
            throw 'WSL OpenSSL did not return a SHA-512-crypt password hash.'
        }
        return $hash
    }
    finally {
        $process.Dispose()
    }
}

function Invoke-TaskSshKeygen {
    param([Parameter(Mandatory)][string]$PrivateKeyPath)
    if ($PrivateKeyPath.Contains('"')) { throw 'The fixed SSH key path contains an unsupported quote character.' }
    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $script:SshKeygenPath
    # Windows PowerShell 5.1 drops an empty native argument when passed as -N ''.
    # ProcessStartInfo preserves the explicit empty string needed by ssh-keygen.
    $startInfo.Arguments = '-q -t ed25519 -N "" -C "tbound-abi8-qualification-20261014" -f "' + $PrivateKeyPath + '"'
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) { throw 'Could not start the task-specific ssh-keygen process.' }
        $process.StandardInput.Close()
        if (-not $process.WaitForExit(30000)) {
            try { $process.Kill(); [void]$process.WaitForExit(5000) } catch { }
            throw 'Task-specific ssh-keygen exceeded its 30-second bound; partial private output, if any, was retained.'
        }
        $null = $process.StandardOutput.ReadToEnd()
        $null = $process.StandardError.ReadToEnd()
        if ($process.ExitCode -ne 0) { throw "Task-specific ssh-keygen failed with exit code $($process.ExitCode); partial output, if any, was retained." }
        return [int]$process.ExitCode
    }
    finally {
        $process.Dispose()
    }
}

function ConvertTo-WindowsNativeArgument {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Argument)
    if ($Argument -notmatch '[\s"]') { return $Argument }
    $builder = New-Object Text.StringBuilder
    [void]$builder.Append([char]34)
    $backslashCount = 0
    foreach ($character in $Argument.ToCharArray()) {
        if ($character -eq [char]92) { $backslashCount++; continue }
        if ($character -eq [char]34) {
            for ($index = 0; $index -lt (2 * $backslashCount + 1); $index++) { [void]$builder.Append([char]92) }
            [void]$builder.Append([char]34)
            $backslashCount = 0
            continue
        }
        for ($index = 0; $index -lt $backslashCount; $index++) { [void]$builder.Append([char]92) }
        [void]$builder.Append($character)
        $backslashCount = 0
    }
    for ($index = 0; $index -lt (2 * $backslashCount); $index++) { [void]$builder.Append([char]92) }
    [void]$builder.Append([char]34)
    return $builder.ToString()
}

function Invoke-NativeCaptured {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$Arguments
    )
    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $FilePath
    $startInfo.Arguments = (($Arguments | ForEach-Object { ConvertTo-WindowsNativeArgument -Argument $_ }) -join ' ')
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) { throw 'Could not start the pinned WSL process.' }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        return [pscustomobject]@{
            ExitCode = $process.ExitCode
            StandardOutput = [string]$stdout
            StandardError = [string]$stderr
        }
    }
    finally {
        $process.Dispose()
    }
}

function Invoke-WslCaptured {
    param([Parameter(Mandatory)][string[]]$Arguments)
    return Invoke-NativeCaptured -FilePath $script:WslExePath -Arguments $Arguments
}

function Invoke-Xorriso {
    param([Parameter(Mandatory)][string[]]$XorrisoArguments)
    $wslArgs = @('--distribution', 'Ubuntu', '--user', 'root', '--exec', '/usr/bin/xorriso') + $XorrisoArguments
    $result = Invoke-WslCaptured -Arguments $wslArgs
    $combined = @($result.StandardOutput, $result.StandardError | Where-Object { $_ }) -join [Environment]::NewLine
    if ($result.ExitCode -ne 0) {
        $tail = @($combined -split "`r?`n" | Where-Object { $_ } | Select-Object -Last 12) -join [Environment]::NewLine
        throw "xorriso failed with exit $($result.ExitCode). Last diagnostic lines:$([Environment]::NewLine)$tail"
    }
    return $combined
}

function Set-PrivateDirectory {
    param([Parameter(Mandatory)][string]$Path)
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true, $false)
    $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $rule = New-Object Security.AccessControl.FileSystemAccessRule(
        $sid,
        [Security.AccessControl.FileSystemRights]::FullControl,
        $inherit,
        [Security.AccessControl.PropagationFlags]::None,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $null = $acl.AddAccessRule($rule)
    $null = [IO.Directory]::CreateDirectory($Path, $acl)
}

function Write-NewUtf8FileDurably {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Text)
    $bytes = (New-Object System.Text.UTF8Encoding($false)).GetBytes($Text)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try {
        $stream.Write($bytes, 0, $bytes.Length)
        $stream.Flush($true)
    }
    finally {
        $stream.Dispose()
    }
}

# Preflight is read-only. Create requires new output paths; resume accepts only the fixed existing media and key paths.
$assetRootFull = Assert-PathWithin -Path $AssetRoot -ParentPath $script:AllowedAssetRoot
$vmStoreFull = Assert-PathWithin -Path $script:VmStoreRoot -ParentPath 'F:\'
$finalVmRoot = Join-Path $vmStoreFull $script:FinalVmName
$privateRootFull = [IO.Path]::GetFullPath($script:PrivateRoot)
$seedVmRoot = Join-Path $assetRootFull 'seed-vm'
$seedDiskDirectory = Join-Path $seedVmRoot 'Virtual Hard Disks'
$seedVhdx = Join-Path $seedDiskDirectory ($script:SeedVmName + '.vhdx')
$customIso = Join-Path $assetRootFull 'ubuntu-24.04.5-autoinstall.iso'
$receiptPath = Join-Path $assetRootFull 'abi8-seed-receipt.json'
$privateConfigRoot = Join-Path $privateRootFull 'autoinstall'
$keyPath = Join-Path $privateRootFull 'id_ed25519'
$publicKeyPath = $keyPath + '.pub'

foreach ($path in @($script:AllowedAssetRoot, $vmStoreFull, $script:IsoPath, $privateRootFull)) {
    Assert-NoReparsePathComponents -Path $path
}
if (-not (Test-Path -LiteralPath $script:AllowedAssetRoot -PathType Container)) { throw "Asset parent is missing: $($script:AllowedAssetRoot)" }
if (-not (Test-Path -LiteralPath $vmStoreFull -PathType Container)) { throw "VM storage parent is missing: $vmStoreFull" }
if ($ResumeVerifiedMedia -or $FinalizeExistingSeed) {
    $expectedAssetRoot = 'F:\TBoundAssets\abi8-qualification-2026-10-14'
    $expectedPrivateRoot = [IO.Path]::GetFullPath((Join-Path $env:LOCALAPPDATA 'TBound\abi8-qualification'))
    if ($assetRootFull -ine $expectedAssetRoot -or $privateRootFull -ine $expectedPrivateRoot) {
        throw 'Resume and FinalizeExistingSeed accept only the fixed prepared-media and task-key paths.'
    }
    if (-not [IO.Directory]::Exists($assetRootFull) -or -not [IO.Directory]::Exists($privateRootFull)) {
        throw 'Resume requires the existing prepared-media and private task-key directories.'
    }
    $assetEntries = @(Get-ChildItem -LiteralPath $assetRootFull -Force -ErrorAction Stop)
    $assetEntryMode = if ($FinalizeExistingSeed) { 'FinalizeExistingSeed' } else { 'Resume' }
    Assert-AssetRootEntryShape -Mode $assetEntryMode -Entries $assetEntries
    $privateEntries = @(Get-ChildItem -LiteralPath $privateRootFull -Force -ErrorAction Stop)
    $expectedPrivateEntries = @('autoinstall', 'id_ed25519', 'id_ed25519.pub')
    if ($privateEntries.Count -ne 3 -or @($privateEntries | Where-Object { $expectedPrivateEntries -cnotcontains $_.Name }).Count -ne 0) {
        throw 'Resume private root contains unexpected entries; refusing to reuse it.'
    }
    foreach ($path in @($customIso, $privateConfigRoot, $keyPath, $publicKeyPath)) { Assert-NoReparsePathComponents -Path $path }
    foreach ($directory in @($assetRootFull, $privateRootFull, $privateConfigRoot)) {
        if (-not [IO.Directory]::Exists($directory)) { throw "Resume input directory is missing: $directory" }
    }
    $currentUserSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $allowedOutputOwnerSids = @($currentUserSid, 'S-1-5-32-544')
    foreach ($directory in @($privateRootFull, $privateConfigRoot)) {
        $directoryAcl = Get-Acl -LiteralPath $directory -ErrorAction Stop
        $directoryOwnerSid = $directoryAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value
        if ($allowedOutputOwnerSids -cnotcontains $directoryOwnerSid) {
            throw "Resume private directory has an unexpected owner: $directory"
        }
        if ([IO.Path]::GetFullPath($directory) -ieq $privateRootFull -and -not $directoryAcl.AreAccessRulesProtected) {
            throw 'Resume private task-key root does not have a protected ACL.'
        }
        foreach ($rule in $directoryAcl.Access) {
            $ruleSid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
            if ($ruleSid -cne [Security.Principal.WindowsIdentity]::GetCurrent().User.Value -or
                $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) {
                throw "Resume private directory ACL includes an unexpected principal or deny rule: $directory"
            }
        }
    }
    foreach ($path in @($keyPath, $publicKeyPath)) {
        $keyItem = Get-Item -LiteralPath $path -Force -ErrorAction Stop
        $keyAcl = Get-Acl -LiteralPath $path -ErrorAction Stop
        if ($keyItem.PSIsContainer -or ($keyItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
            $keyItem.Length -lt 1 -or $keyItem.Length -gt 4096 -or
            $allowedOutputOwnerSids -cnotcontains $keyAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value) {
            throw "Resume key input failed its path, owner, or size check: $path"
        }
    }
    $privateKeyAcl = Get-Acl -LiteralPath $keyPath -ErrorAction Stop
    $allowedPrivateKeySids = @(
        [Security.Principal.WindowsIdentity]::GetCurrent().User.Value,
        'S-1-5-18',
        'S-1-5-32-544'
    )
    if (-not $privateKeyAcl.AreAccessRulesProtected -or $privateKeyAcl.Access.Count -gt 8) {
        throw 'Existing private task key does not have a bounded protected ACL.'
    }
    foreach ($rule in $privateKeyAcl.Access) {
        $ruleSid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
            $allowedPrivateKeySids -cnotcontains $ruleSid) {
            throw 'Existing private task key ACL grants an unexpected principal or deny rule.'
        }
    }
    $privateConfigEntries = @(Get-ChildItem -LiteralPath $privateConfigRoot -Force -ErrorAction Stop)
    $expectedConfigEntries = @('grub.cfg', 'meta-data', 'user-data')
    if ($privateConfigEntries.Count -ne 3 -or @($privateConfigEntries | Where-Object { $_.PSIsContainer -or $expectedConfigEntries -cnotcontains $_.Name }).Count -ne 0) {
        throw 'Resume autoinstall directory does not contain exactly the three expected regular files.'
    }
    foreach ($entryName in $expectedConfigEntries) {
        $configPath = Join-Path $privateConfigRoot $entryName
        Assert-NoReparsePathComponents -Path $configPath
        $configAcl = Get-Acl -LiteralPath $configPath -ErrorAction Stop
        if ($allowedOutputOwnerSids -cnotcontains $configAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value) {
            throw "Resume autoinstall file has an unexpected owner: $entryName"
        }
        foreach ($rule in $configAcl.Access) {
            $ruleSid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
            if ($ruleSid -cne [Security.Principal.WindowsIdentity]::GetCurrent().User.Value -or
                $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) {
                throw "Resume autoinstall file ACL includes an unexpected principal or deny rule: $entryName"
            }
        }
    }
    if ($FinalizeExistingSeed) {
        if (-not [IO.Directory]::Exists($seedVmRoot) -or [IO.File]::Exists($receiptPath) -or [IO.Directory]::Exists($receiptPath)) {
            throw 'FinalizeExistingSeed requires the existing fixed seed directory and no prior seed receipt.'
        }
        $seedVmConfigurationDirectory = Join-Path $seedVmRoot $script:SeedVmName
        foreach ($path in @($seedVmRoot, $seedVmConfigurationDirectory, $seedDiskDirectory, $seedVhdx)) {
            Assert-NoReparsePathComponents -Path $path
        }
        $seedRootEntries = @(Get-ChildItem -LiteralPath $seedVmRoot -Force -ErrorAction Stop)
        $expectedSeedRootEntries = @($script:SeedVmName, 'Virtual Hard Disks')
        if ($seedRootEntries.Count -ne 2 -or @($seedRootEntries | Where-Object { $expectedSeedRootEntries -cnotcontains $_.Name }).Count -ne 0 -or
            @($seedRootEntries | Where-Object { $_.Name -ceq $script:SeedVmName -and -not $_.PSIsContainer }).Count -ne 0 -or
            @($seedRootEntries | Where-Object { $_.Name -ceq 'Virtual Hard Disks' -and -not $_.PSIsContainer }).Count -ne 0) {
            throw 'FinalizeExistingSeed found an unexpected entry in the existing fixed seed directory.'
        }
        $seedDiskEntries = @(Get-ChildItem -LiteralPath $seedDiskDirectory -Force -ErrorAction Stop)
        if ($seedDiskEntries.Count -ne 1 -or $seedDiskEntries[0].PSIsContainer -or
            $seedDiskEntries[0].Name -cne [IO.Path]::GetFileName($seedVhdx)) {
            throw 'FinalizeExistingSeed found unexpected content in the fixed seed VHD directory.'
        }
    }
    else {
        Assert-UnoccupiedPath -Path $seedVmRoot -Description 'Seed VM output directory'
        Assert-UnoccupiedPath -Path $receiptPath -Description 'Seed receipt'
    }
}
else {
    $createAssetEntries = @()
    if ([IO.Directory]::Exists($assetRootFull)) {
        $createAssetEntries = @(Get-ChildItem -LiteralPath $assetRootFull -Force -ErrorAction Stop)
    }
    Assert-AssetRootEntryShape -Mode 'Create' -Entries $createAssetEntries
    Assert-UnoccupiedPath -Path $assetRootFull -Description 'Asset root'
    Assert-UnoccupiedPath -Path $privateRootFull -Description 'Private key root'
}
Assert-UnoccupiedPath -Path $finalVmRoot -Description 'Final VM root'
if (-not $FinalizeExistingSeed -and (Get-VM -Name $script:SeedVmName -ErrorAction SilentlyContinue)) {
    throw "Seed VM already exists: $($script:SeedVmName)"
}
if (Get-VM -Name $script:FinalVmName -ErrorAction SilentlyContinue) { throw "Final candidate VM already exists: $($script:FinalVmName)" }
$switchMatches = @(Get-VMSwitch -ErrorAction Stop | Where-Object { $_.Name -ceq $script:SwitchName })
if ($switchMatches.Count -ne 1) { throw "Provisioning switch '$($script:SwitchName)' must exist exactly once." }
$driveName = ([IO.Path]::GetPathRoot($assetRootFull)).TrimEnd('\').TrimEnd(':')
$drive = Get-PSDrive -Name $driveName -ErrorAction Stop
if ($null -eq $drive.Free -or $drive.Free -lt ($MinimumFreeGb * 1GB)) {
    throw ("Drive {0}: requires at least {1} GiB free; found {2:N1} GiB." -f $driveName, $MinimumFreeGb, ($drive.Free / 1GB))
}
$isoFull = [IO.Path]::GetFullPath((Resolve-Path -LiteralPath $script:IsoPath -ErrorAction Stop).Path)
$isoItem = Get-Item -LiteralPath $isoFull -ErrorAction Stop
if ($isoItem.Length -ne 4080486400) { throw "Unexpected Ubuntu ISO length: $($isoItem.Length) bytes." }
$isoHash = (Get-FileHash -LiteralPath $isoFull -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
if ($isoHash -cne $script:ExpectedIsoSha256) { throw "Ubuntu ISO hash mismatch; expected $($script:ExpectedIsoSha256), got $isoHash." }
Assert-TrustedSystemTool -Path $script:WslExePath -ExpectedSha256 $script:ExpectedWslExeSha256 -TrustedDirectories @($script:WindowsDirectory, $script:SystemDirectory)
Assert-TrustedSystemTool -Path $script:SshKeygenPath -ExpectedSha256 $script:ExpectedSshKeygenSha256 -TrustedDirectories @($script:WindowsDirectory, $script:SystemDirectory, (Join-Path $script:SystemDirectory 'OpenSSH'))
# WSL emits xorriso's version banner on stderr. Capture both streams and use the real process exit code.
$xorrisoVersionResult = Invoke-WslCaptured -Arguments @('--distribution', 'Ubuntu', '--user', 'root', '--exec', '/usr/bin/xorriso', '-version')
$xorrisoVersionText = @($xorrisoVersionResult.StandardOutput, $xorrisoVersionResult.StandardError | Where-Object { $_ }) -join [Environment]::NewLine
$versionMatch = [regex]::Match($xorrisoVersionText, '(?im)^\s*(xorriso\s+\d+\.\d+(?:\.\d+)?)')
if ($xorrisoVersionResult.ExitCode -ne 0 -or -not $versionMatch.Success) {
    throw 'xorriso is missing in WSL Ubuntu. Install it with: wsl.exe --distribution Ubuntu --user root -- apt-get update; then run: wsl.exe --distribution Ubuntu --user root -- apt-get install -y xorriso'
}
$xorrisoVersion = $versionMatch.Groups[1].Value
$grubText = Get-IsoFileText -Path $isoFull -IsoRelativePath 'boot/grub/grub.cfg'
$grubLines = @($grubText -split '\r?\n')
$bootLines = @($grubLines | Where-Object { $_ -match '^\s*linux\s+/casper/(?:hwe-)?vmlinuz(?:\s|$)' })
if ($bootLines.Count -ne 2 -or
    @($bootLines | Where-Object { $_ -match '/casper/vmlinuz(?:\s|$)' }).Count -ne 1 -or
    @($bootLines | Where-Object { $_ -match '/casper/hwe-vmlinuz(?:\s|$)' }).Count -ne 1) {
    throw 'Ubuntu ISO GRUB layout differs from the reviewed 24.04.5 layout; refusing to remaster it.'
}
if ($grubText -match 'autoinstall|ds=nocloud') { throw 'Source ISO already has autoinstall kernel arguments.' }
$hyperVCommands = @(
    'New-VHD', 'New-VM', 'Set-VMProcessor', 'Set-VMMemory', 'Set-VM',
    'Add-VMHardDiskDrive', 'Add-VMDvdDrive', 'Set-VMFirmware', 'Start-VM',
    'Get-VM', 'Get-VMProcessor', 'Get-VMMemory', 'Get-VMNetworkAdapter',
    'Get-VMFirmware', 'Get-VMDvdDrive', 'Get-VMHardDiskDrive', 'Get-VHD',
    'Get-VMSnapshot', 'Set-VMDvdDrive'
)
foreach ($commandName in $hyperVCommands) {
    if (-not (Get-Command $commandName -ErrorAction SilentlyContinue)) { throw "Required Hyper-V command is unavailable: $commandName" }
}
$operationDescription = if ($FinalizeExistingSeed) {
    'Validate and finalize only the exact existing Off seed VM; do not create or start a VM'
}
elseif ($ResumeVerifiedMedia) {
    'Verify the existing pinned autoinstall ISO and task-key files, then install one new seed VM and leave its source VHDX powered off'
}
else {
    'Create a private SSH key and derived autoinstall ISO, install one new seed VM, and leave its source VHDX powered off'
}
$operationTarget = if ($FinalizeExistingSeed) { $script:SeedVmName } else { "$($script:SeedVmName) and $($script:FinalVmName)" }
if (-not $PSCmdlet.ShouldProcess($operationTarget, $operationDescription)) {
    return
}

if (-not ($ResumeVerifiedMedia -or $FinalizeExistingSeed)) {
# Create the task-specific SSH key and NoCloud autoinstall data in private storage.
Set-PrivateDirectory -Path $privateRootFull
Assert-NoReparsePathComponents -Path $privateRootFull
$null = [IO.Directory]::CreateDirectory($privateConfigRoot)
$keygenExitCode = Invoke-TaskSshKeygen -PrivateKeyPath $keyPath
if ($keygenExitCode -ne 0 -or -not (Test-Path -LiteralPath $keyPath -PathType Leaf) -or
    -not (Test-Path -LiteralPath $publicKeyPath -PathType Leaf)) {
    throw 'Could not create the task-specific Ed25519 key. Any partial files were retained for inspection.'
}
$publicKeyLines = @(Get-Content -LiteralPath $publicKeyPath -ErrorAction Stop | Where-Object { $_ -and -not $_.StartsWith('#') })
if ($publicKeyLines.Count -ne 1 -or $publicKeyLines[0] -notmatch '^ssh-ed25519\s+[A-Za-z0-9+/=]+(\s|$)') {
    throw 'The new public key did not validate as one OpenSSH Ed25519 key.'
}
$fingerprintResult = Invoke-NativeCaptured -FilePath $script:SshKeygenPath -Arguments @('-lf', $publicKeyPath, '-E', 'sha256')
$fingerprintText = $fingerprintResult.StandardOutput + [Environment]::NewLine + $fingerprintResult.StandardError
$fingerprintMatch = [regex]::Match($fingerprintText, '(SHA256:[A-Za-z0-9+/]{43})')
if ($fingerprintResult.ExitCode -ne 0 -or -not $fingerprintMatch.Success) { throw 'Could not compute the public-key fingerprint.' }
$publicKeyFingerprint = $fingerprintMatch.Groups[1].Value

$randomBytes = New-Object byte[] 48
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $rng.GetBytes($randomBytes)
    $installPassword = [Convert]::ToBase64String($randomBytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
    $passwordHash = Invoke-WslOpenSslHash -Plaintext $installPassword
}
finally {
    $rng.Dispose()
    [Array]::Clear($randomBytes, 0, $randomBytes.Length)
    $installPassword = $null
}

$patchedLines = New-Object System.Collections.Generic.List[string]
$patchedCount = 0
foreach ($line in $grubLines) {
    if ($line -match '^\s*linux\s+/casper/(?:hwe-)?vmlinuz(?:\s|$)') {
        if ($line -notmatch '\s---\s*$') { throw 'An expected GRUB kernel line lacks the installer delimiter.' }
        $line = [regex]::Replace($line, '\s+---\s*$', " autoinstall 'ds=nocloud;s=file:///cdrom/nocloud/' ---")
        $patchedCount++
    }
    $patchedLines.Add($line)
}
if ($patchedCount -ne 2) { throw "Expected to patch two boot entries; patched $patchedCount." }
$encoding = New-Object System.Text.UTF8Encoding($false)
$patchedGrubPath = Join-Path $privateConfigRoot 'grub.cfg'
$userDataPath = Join-Path $privateConfigRoot 'user-data'
$metaDataPath = Join-Path $privateConfigRoot 'meta-data'
[IO.File]::WriteAllText($patchedGrubPath, ($patchedLines -join [Environment]::NewLine) + [Environment]::NewLine, $encoding)
$userData = @"
#cloud-config
autoinstall:
  version: 1
  locale: en_US.UTF-8
  keyboard:
    layout: us
  identity:
    hostname: tbound-abi8-seed
    username: tboundadmin
    password: '$passwordHash'
  storage:
    layout:
      # A plain ext4 root partition keeps later report extraction isolated
      # from the host's other VGs; the disposable qualification guest needs no LVM.
      name: direct
  kernel:
    flavor: hwe
  updates: all
  ssh:
    install-server: true
    authorized-keys:
      - $($publicKeyLines[0])
    allow-pw: false
  shutdown: poweroff
"@
[IO.File]::WriteAllText($userDataPath, $userData.TrimStart([char[]]@([char]13, [char]10)) + [Environment]::NewLine, $encoding)
[IO.File]::WriteAllText($metaDataPath, 'instance-id: iid-tbound-abi8-seed-20261014' + [Environment]::NewLine + 'local-hostname: tbound-abi8-seed' + [Environment]::NewLine, $encoding)
$passwordHash = $null

# Remaster to a new path, preserve boot records, then read back and verify all edits.
$sourceWsl = ConvertTo-WslPath -WindowsPath $isoFull
$outputWsl = ConvertTo-WslPath -WindowsPath $customIso
$grubWsl = ConvertTo-WslPath -WindowsPath $patchedGrubPath
$noCloudWsl = ConvertTo-WslPath -WindowsPath $privateConfigRoot
New-Item -ItemType Directory -Path $assetRootFull -ErrorAction Stop | Out-Null
Assert-NoReparsePathComponents -Path $assetRootFull
$null = Invoke-Xorriso -XorrisoArguments @(
    '-indev', $sourceWsl,
    '-outdev', $outputWsl,
    '-overwrite', 'on',
    '-map', $grubWsl, '/boot/grub/grub.cfg',
    '-map', $noCloudWsl, '/nocloud',
    '-boot_image', 'any', 'replay'
)
if (-not (Test-Path -LiteralPath $customIso -PathType Leaf)) { throw 'xorriso completed without creating the derived ISO.' }
    $derivedGrub = Get-IsoFileText -Path $customIso -IsoRelativePath 'boot/grub/grub.cfg'
$derivedGrubMatches = Test-IsoFileMatchesLocalFile -Path $customIso -IsoRelativePath 'boot/grub/grub.cfg' -ExpectedLocalPath $patchedGrubPath
$derivedUserDataMatches = Test-IsoFileMatchesLocalFile -Path $customIso -IsoRelativePath 'nocloud/user-data' -ExpectedLocalPath $userDataPath
$derivedMetaDataMatches = Test-IsoFileMatchesLocalFile -Path $customIso -IsoRelativePath 'nocloud/meta-data' -ExpectedLocalPath $metaDataPath
if (@([regex]::Matches($derivedGrub, 'autoinstall')).Count -ne 2 -or
    @([regex]::Matches($derivedGrub, 'ds=nocloud;s=file:///cdrom/nocloud/')).Count -ne 2 -or
    -not $derivedGrubMatches -or -not $derivedUserDataMatches -or -not $derivedMetaDataMatches) {
    throw 'Derived ISO failed kernel-argument or NoCloud-content verification.'
}
$derivedIsoHash = (Get-FileHash -LiteralPath $customIso -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
}
else {
    $derivedIsoItem = Get-Item -LiteralPath $customIso -Force -ErrorAction Stop
    if ($derivedIsoItem.PSIsContainer -or ($derivedIsoItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
        $derivedIsoItem.Length -ne 4079484928) {
        throw 'Prepared ISO failed its fixed path, type, or length check.'
    }
    $derivedIsoHash = (Get-FileHash -LiteralPath $customIso -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
    if ($derivedIsoHash -cne $script:ExpectedPreparedIsoSha256) {
        throw 'Prepared ISO SHA-256 differs from the reviewed existing-media pin.'
    }
    $userDataPath = Join-Path $privateConfigRoot 'user-data'
    $metaDataPath = Join-Path $privateConfigRoot 'meta-data'
    $patchedGrubPath = Join-Path $privateConfigRoot 'grub.cfg'
    $derivedGrub = Get-IsoFileText -Path $customIso -IsoRelativePath 'boot/grub/grub.cfg'
    $derivedGrubMatches = Test-IsoFileMatchesLocalFile -Path $customIso -IsoRelativePath 'boot/grub/grub.cfg' -ExpectedLocalPath $patchedGrubPath
    $derivedUserDataMatches = Test-IsoFileMatchesLocalFile -Path $customIso -IsoRelativePath 'nocloud/user-data' -ExpectedLocalPath $userDataPath
    $derivedMetaDataMatches = Test-IsoFileMatchesLocalFile -Path $customIso -IsoRelativePath 'nocloud/meta-data' -ExpectedLocalPath $metaDataPath
    if (@([regex]::Matches($derivedGrub, 'autoinstall')).Count -ne 2 -or
        @([regex]::Matches($derivedGrub, 'ds=nocloud;s=file:///cdrom/nocloud/')).Count -ne 2 -or
        -not $derivedGrubMatches -or -not $derivedUserDataMatches -or -not $derivedMetaDataMatches) {
        throw 'Prepared ISO failed exact Rock Ridge path, kernel-argument, or private-content verification.'
    }
    $publicKeyLines = @(Get-Content -LiteralPath $publicKeyPath -ErrorAction Stop | Where-Object { $_ -and -not $_.StartsWith('#') })
    if ($publicKeyLines.Count -ne 1 -or $publicKeyLines[0] -notmatch '^ssh-ed25519\s+[A-Za-z0-9+/=]+(\s|$)') {
        throw 'Existing public key failed its OpenSSH Ed25519 format check.'
    }
    if (-not (Test-FileContainsUtf8Literal -Path $userDataPath -Literal ('      - ' + $publicKeyLines[0]))) {
        throw 'Existing task public-key fingerprint is not bound to the NoCloud authorized-key entry.'
    }
    $fingerprintResult = Invoke-NativeCaptured -FilePath $script:SshKeygenPath -Arguments @('-lf', $publicKeyPath, '-E', 'sha256')
    $fingerprintText = $fingerprintResult.StandardOutput + [Environment]::NewLine + $fingerprintResult.StandardError
    $fingerprintMatch = [regex]::Match($fingerprintText, '(SHA256:[A-Za-z0-9+/]{43})')
    if ($fingerprintResult.ExitCode -ne 0 -or -not $fingerprintMatch.Success) { throw 'Could not validate the existing public-key fingerprint.' }
    $publicKeyFingerprint = $fingerprintMatch.Groups[1].Value
}
$outputWsl = ConvertTo-WslPath -WindowsPath $customIso
$bootReport = Invoke-Xorriso -XorrisoArguments @('-indev', $outputWsl, '-report_el_torito', 'as_mkisofs')
if ($bootReport -notmatch '(?i)eltorito|boot') { throw 'xorriso did not report boot metadata for the derived ISO.' }

if ($FinalizeExistingSeed) {
    # This action is bound to the VM instance observed Off after installation.
    # It never creates or starts a VM and accepts only the fixed empty/installer DVD states.
    $seedVms = @(Get-VM -Name $script:SeedVmName -ErrorAction Stop)
    if ($seedVms.Count -ne 1) { throw 'FinalizeExistingSeed did not resolve exactly one existing seed VM.' }
    $seedVm = $seedVms[0]
    $seedProcessor = Get-VMProcessor -VMName $script:SeedVmName -ErrorAction Stop
    $seedMemory = Get-VMMemory -VMName $script:SeedVmName -ErrorAction Stop
    $seedAdapter = @(Get-VMNetworkAdapter -VMName $script:SeedVmName -ErrorAction Stop)
    $seedFirmware = Get-VMFirmware -VMName $script:SeedVmName -ErrorAction Stop
    $seedDvds = @(Get-VMDvdDrive -VMName $script:SeedVmName -ErrorAction Stop)
    $seedHardDisks = @(Get-VMHardDiskDrive -VMName $script:SeedVmName -ErrorAction Stop)
    $seedSnapshots = @(Get-VMSnapshot -VMName $script:SeedVmName -ErrorAction Stop)
    if ($seedVm.Id.ToString() -ine $script:ExpectedSeedVmId -or $seedVm.State -ne 'Off' -or
        $seedVm.Generation -ne 2 -or $seedProcessor.Count -ne 2 -or
        $seedMemory.DynamicMemoryEnabled -or $seedMemory.Startup -ne 4GB -or
        $seedAdapter.Count -ne 1 -or $seedAdapter[0].SwitchName -cne $script:SwitchName -or
        $seedFirmware.SecureBoot -ne 'On' -or
        $seedFirmware.SecureBootTemplate -notmatch 'MicrosoftUEFICertificateAuthority' -or
        $seedDvds.Count -ne 1 -or $seedDvds[0].ControllerNumber -ne 0 -or $seedDvds[0].ControllerLocation -ne 1 -or
        ($seedDvds[0].Path -and $seedDvds[0].Path -ine $customIso) -or
        $seedHardDisks.Count -ne 1 -or $seedHardDisks[0].ControllerNumber -ne 0 -or
        $seedHardDisks[0].ControllerLocation -ne 0 -or $seedHardDisks[0].Path -ine $seedVhdx -or
        $seedSnapshots.Count -ne 0) {
        throw 'Existing seed VM differs from the exact observed Off profile; refusing to finalize it.'
    }

    $seedVhdInfo = Get-VHD -Path $seedVhdx -ErrorAction Stop
    if ($seedVhdInfo.VhdFormat -ne 'VHDX' -or $seedVhdInfo.Size -ne 40GB -or
        $seedVhdInfo.VhdType -ne 'Dynamic' -or $seedVhdInfo.ParentPath) {
        throw 'Existing seed VHDX is not the standalone 40 GiB dynamic disk required for finalization.'
    }
    $seedVhdItem = Get-Item -LiteralPath $seedVhdx -Force -ErrorAction Stop
    if ($seedVhdItem.PSIsContainer -or ($seedVhdItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'Existing seed VHDX is not a regular non-reparse file.'
    }

    if ($seedDvds[0].Path -and $seedDvds[0].Path -ieq $customIso) {
        $null = Set-VMDvdDrive -VMDvdDrive $seedDvds[0] -Path $null -ErrorAction Stop
    }
    $null = Set-VMFirmware -VMName $script:SeedVmName -FirstBootDevice $seedHardDisks[0] -ErrorAction Stop

    $seedVm = Get-VM -Name $script:SeedVmName -ErrorAction Stop
    $seedProcessor = Get-VMProcessor -VMName $script:SeedVmName -ErrorAction Stop
    $seedMemory = Get-VMMemory -VMName $script:SeedVmName -ErrorAction Stop
    $seedAdapter = @(Get-VMNetworkAdapter -VMName $script:SeedVmName -ErrorAction Stop)
    $seedFirmware = Get-VMFirmware -VMName $script:SeedVmName -ErrorAction Stop
    $seedDvds = @(Get-VMDvdDrive -VMName $script:SeedVmName -ErrorAction Stop)
    $seedHardDisks = @(Get-VMHardDiskDrive -VMName $script:SeedVmName -ErrorAction Stop)
    $seedSnapshots = @(Get-VMSnapshot -VMName $script:SeedVmName -ErrorAction Stop)
    if ($seedVm.Id.ToString() -ine $script:ExpectedSeedVmId -or $seedVm.State -ne 'Off' -or $seedVm.Generation -ne 2 -or
        $seedProcessor.Count -ne 2 -or $seedMemory.DynamicMemoryEnabled -or $seedMemory.Startup -ne 4GB -or
        $seedAdapter.Count -ne 1 -or $seedAdapter[0].SwitchName -cne $script:SwitchName -or
        $seedDvds.Count -ne 1 -or $seedDvds[0].ControllerNumber -ne 0 -or $seedDvds[0].ControllerLocation -ne 1 -or $seedDvds[0].Path -or
        $seedHardDisks.Count -ne 1 -or $seedHardDisks[0].Path -ine $seedVhdx -or
        $seedSnapshots.Count -ne 0 -or $seedFirmware.SecureBoot -ne 'On' -or
        $seedFirmware.SecureBootTemplate -notmatch 'MicrosoftUEFICertificateAuthority') {
        throw 'Existing seed changed during finalization; no receipt was written.'
    }
    $seedVhdInfo = Get-VHD -Path $seedVhdx -ErrorAction Stop
    if ($seedVhdInfo.VhdFormat -ne 'VHDX' -or $seedVhdInfo.Size -ne 40GB -or
        $seedVhdInfo.VhdType -ne 'Dynamic' -or $seedVhdInfo.ParentPath) {
        throw 'Existing seed VHDX changed during finalization; no receipt was written.'
    }
    $seedDiskHash = (Get-FileHash -LiteralPath $seedVhdx -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
}
else {
    # Create one fresh, bounded installer VM. Failure leaves state for inspection.
    $seedDiskDirectory = Join-Path $seedVmRoot 'Virtual Hard Disks'
    New-Item -ItemType Directory -Path $seedVmRoot -ErrorAction Stop | Out-Null
    New-Item -ItemType Directory -Path $seedDiskDirectory -ErrorAction Stop | Out-Null
    Assert-NoReparsePathComponents -Path $seedVmRoot
    $null = New-VHD -Path $seedVhdx -SizeBytes 40GB -Dynamic -ErrorAction Stop
    $null = New-VM -Name $script:SeedVmName -Generation 2 -MemoryStartupBytes 4GB -NoVHD -Path $seedVmRoot -SwitchName $script:SwitchName -ErrorAction Stop
    $null = Set-VMProcessor -VMName $script:SeedVmName -Count 2 -ErrorAction Stop
    $null = Set-VMMemory -VMName $script:SeedVmName -DynamicMemoryEnabled $false -StartupBytes 4GB -ErrorAction Stop
    $null = Set-VM -VMName $script:SeedVmName -AutomaticCheckpointsEnabled $false -CheckpointType ProductionOnly -ErrorAction Stop
    $null = Add-VMHardDiskDrive -VMName $script:SeedVmName -Path $seedVhdx -ErrorAction Stop
    $dvd = Add-VMDvdDrive -VMName $script:SeedVmName -Path $customIso -PassThru -ErrorAction Stop
    $null = Set-VMFirmware -VMName $script:SeedVmName -EnableSecureBoot On -SecureBootTemplate MicrosoftUEFICertificateAuthority -FirstBootDevice $dvd -ErrorAction Stop

    $seedVm = Get-VM -Name $script:SeedVmName -ErrorAction Stop
    $seedProcessor = Get-VMProcessor -VMName $script:SeedVmName -ErrorAction Stop
    $seedMemory = Get-VMMemory -VMName $script:SeedVmName -ErrorAction Stop
    $seedDiskInfo = Get-VHD -Path $seedVhdx -ErrorAction Stop
    $seedAdapter = @(Get-VMNetworkAdapter -VMName $script:SeedVmName -ErrorAction Stop)
    $seedFirmware = Get-VMFirmware -VMName $script:SeedVmName -ErrorAction Stop
    if ($seedVm.Generation -ne 2 -or $seedProcessor.Count -ne 2 -or
        $seedMemory.DynamicMemoryEnabled -or $seedMemory.Startup -ne 4GB -or
        $seedDiskInfo.Size -ne 40GB -or $seedDiskInfo.VhdType -ne 'Dynamic' -or
        $seedAdapter.Count -ne 1 -or $seedAdapter[0].SwitchName -cne $script:SwitchName -or
        $seedFirmware.SecureBoot -ne 'On' -or
        $seedFirmware.SecureBootTemplate -notmatch 'MicrosoftUEFICertificateAuthority') {
        throw 'Seed VM settings differ from the installer profile; inspect the partial VM before retrying.'
    }

    Write-Host "Starting unattended installation in $($script:SeedVmName). It will power off at completion."
    $null = Start-VM -Name $script:SeedVmName -ErrorAction Stop
    $deadline = (Get-Date).AddMinutes($InstallTimeoutMinutes)
    $lastStatus = ''
    while ((Get-Date) -lt $deadline) {
        $state = [string](Get-VM -Name $script:SeedVmName -ErrorAction Stop).State
        if ($state -eq 'Off') { break }
        if ($state -ne $lastStatus) { Write-Host "Installer VM state: $state"; $lastStatus = $state }
        Start-Sleep -Seconds 15
    }
    if ((Get-VM -Name $script:SeedVmName -ErrorAction Stop).State -ne 'Off') {
        throw "Autoinstall did not power off within $InstallTimeoutMinutes minutes. No forced poweroff or cleanup was attempted; inspect the VM and installer log."
    }

    $seedDvd = @(Get-VMDvdDrive -VMName $script:SeedVmName -ErrorAction Stop | Where-Object { $_.Path -eq $customIso })
    if ($seedDvd.Count -ne 1) { throw 'Could not identify exactly one installer DVD to detach.' }
    $null = Set-VMDvdDrive -VMDvdDrive $seedDvd[0] -Path $null -ErrorAction Stop
    $seedHardDisks = @(Get-VMHardDiskDrive -VMName $script:SeedVmName -ErrorAction Stop)
    if ($seedHardDisks.Count -ne 1 -or $seedHardDisks[0].Path -ine $seedVhdx) { throw 'Unexpected seed disk attachment; refusing to hash it.' }
    $null = Set-VMFirmware -VMName $script:SeedVmName -FirstBootDevice $seedHardDisks[0] -ErrorAction Stop
    if (@(Get-VMSnapshot -VMName $script:SeedVmName -ErrorAction Stop).Count -ne 0) { throw 'Seed VM has checkpoints; refusing to hash it.' }
    $seedVhdInfo = Get-VHD -Path $seedVhdx -ErrorAction Stop
    if ($seedVhdInfo.VhdFormat -ne 'VHDX' -or $seedVhdInfo.Size -ne 40GB -or $seedVhdInfo.ParentPath) {
        throw 'Seed is not a standalone 40 GiB VHDX; refusing to record it as a clone source.'
    }
    $seedDiskHash = (Get-FileHash -LiteralPath $seedVhdx -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
}

$receipt = [ordered]@{
    schema = 'tbound-abi8-hyperv-seed/v1'
    capturedAt = (Get-Date).ToString('o')
    claimStatus = 'candidate only; live Landlock ABI 8 and all host profile checks remain required'
    sourceMedia = [ordered]@{
        path = $isoFull
        filename = [IO.Path]::GetFileName($isoFull)
        sha256 = $isoHash
    }
    derivedMedia = [ordered]@{
        path = $customIso
        sha256 = $derivedIsoHash
        xorrisoVersion = $xorrisoVersion
        reusedExistingMedia = [bool]($ResumeVerifiedMedia -or $FinalizeExistingSeed)
        canonicalSignatureCoversDerivedMedia = $false
    }
    provisioningKey = [ordered]@{
        publicFingerprint = $publicKeyFingerprint
        privateKeyPath = $keyPath
        privateKeyMaterialIncluded = $false
    }
    seedVm = [ordered]@{
        name = $script:SeedVmName
        id = $seedVm.Id.ToString()
        state = 'Off'
        generation = 2
        processorCount = 2
        fixedMemoryBytes = [int64]4GB
        virtualDiskBytes = [int64]$seedVhdInfo.Size
        diskType = [string]$seedVhdInfo.VhdType
        diskPath = $seedVhdx
        diskSha256 = $seedDiskHash
        parentPath = $null
        checkpointCount = 0
        switchName = $script:SwitchName
        secureBoot = [string]$seedFirmware.SecureBoot
        secureBootTemplate = [string]$seedFirmware.SecureBootTemplate
    }
    followOn = [ordered]@{
        vmName = $script:FinalVmName
        vmRoot = $finalVmRoot
        command = "& .\infra\repro\bootstrap-hyperv-windows.ps1 -ImageKind GoldenVhdx -ImageSourcePath '$seedVhdx' -ExpectedImageSha256 '$seedDiskHash' -PublicKeyFile '$publicKeyPath' -VmName '$($script:FinalVmName)' -VmRoot '$finalVmRoot' -SwitchName '$($script:SwitchName)'"
    }
}
Write-NewUtf8FileDurably -Path $receiptPath -Text (($receipt | ConvertTo-Json -Depth 8) + [Environment]::NewLine)

Write-Host ''
Write-Host '=== ABI-8 candidate seed ready ===' -ForegroundColor Green
Write-Host "Derived ISO SHA-256: $derivedIsoHash"
Write-Host "Seed disk SHA-256:   $seedDiskHash"
Write-Host 'Seed state:          Off (DVD detached; no checkpoints; standalone VHDX)'
Write-Host "Receipt:             $receiptPath"
Write-Host 'No Landlock ABI or host-profile qualification has been asserted.'
Write-Host 'Review the receipt, then run its followOn.command to create the fresh final candidate and perform the live probe.'
