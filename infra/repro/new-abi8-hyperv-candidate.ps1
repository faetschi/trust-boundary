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
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [ValidateNotNullOrEmpty()][string]$AssetRoot = 'F:\TBoundAssets\abi8-qualification-2026-10-14',
    [ValidateRange(30, 360)][int]$InstallTimeoutMinutes = 120,
    [ValidateRange(1, 999)][int]$MinimumFreeGb = 120
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:IsoPath = 'F:\TBoundAssets\ubuntu-24.04.5\ubuntu-24.04.5-live-server-amd64.iso'
$script:ExpectedIsoSha256 = '97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8'
$script:AllowedAssetRoot = 'F:\TBoundAssets'
$script:VmStoreRoot = 'F:\TBoundVMs'
$script:SeedVmName = 'TBound-Ubuntu-ABI8-Seed'
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

function ConvertTo-WslPath {
    param([Parameter(Mandatory)][string]$WindowsPath)
    $full = [IO.Path]::GetFullPath($WindowsPath)
    $match = [regex]::Match($full, '^([A-Za-z]):\\(.*)$')
    if (-not $match.Success) { throw "WSL input must be on a mounted drive: $full" }
    return '/mnt/' + $match.Groups[1].Value.ToLowerInvariant() + '/' + $match.Groups[2].Value.Replace('\', '/')
}

function Get-IsoFileText {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$IsoRelativePath)
    $image = Get-DiskImage -ImagePath $Path -ErrorAction SilentlyContinue
    $wasAttached = $null -ne $image -and [bool]$image.Attached
    if (-not $wasAttached) {
        $image = Mount-DiskImage -ImagePath $Path -StorageType ISO -Access ReadOnly -PassThru -ErrorAction Stop
    }
    try {
        $volumes = @($image | Get-Volume -ErrorAction Stop | Where-Object { $_.DriveLetter })
        if ($volumes.Count -ne 1) { throw "Expected one mounted ISO volume with a drive letter: $Path" }
        $root = [string]$volumes[0].DriveLetter + ':\'
        $file = Join-Path $root $IsoRelativePath.Replace('/', '\')
        if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "ISO is missing required file $IsoRelativePath" }
        return [IO.File]::ReadAllText($file)
    }
    finally {
        if (-not $wasAttached) { Dismount-DiskImage -ImagePath $Path -ErrorAction Stop | Out-Null }
    }
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

# Preflight is read-only. Outputs must be new, bounded paths on F:.
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
Assert-UnoccupiedPath -Path $assetRootFull -Description 'Asset root'
Assert-UnoccupiedPath -Path $privateRootFull -Description 'Private key root'
Assert-UnoccupiedPath -Path $finalVmRoot -Description 'Final VM root'
if (Get-VM -Name $script:SeedVmName -ErrorAction SilentlyContinue) { throw "Seed VM already exists: $($script:SeedVmName)" }
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
$grubText = Get-IsoFileText -Path $isoFull -IsoRelativePath 'boot/grub/grub.cfg'
$grubLines = @($grubText -split '\r?\n')
$bootLines = @($grubLines | Where-Object { $_ -match '^\s*linux\s+/casper/(?:hwe-)?vmlinuz(?:\s|$)' })
if ($bootLines.Count -ne 2 -or
    @($bootLines | Where-Object { $_ -match '/casper/vmlinuz(?:\s|$)' }).Count -ne 1 -or
    @($bootLines | Where-Object { $_ -match '/casper/hwe-vmlinuz(?:\s|$)' }).Count -ne 1) {
    throw 'Ubuntu ISO GRUB layout differs from the reviewed 24.04.5 layout; refusing to remaster it.'
}
if ($grubText -match 'autoinstall|ds=nocloud') { throw 'Source ISO already has autoinstall kernel arguments.' }
Assert-TrustedSystemTool -Path $script:WslExePath -ExpectedSha256 $script:ExpectedWslExeSha256 -TrustedDirectories @($script:WindowsDirectory, $script:SystemDirectory)
Assert-TrustedSystemTool -Path $script:SshKeygenPath -ExpectedSha256 $script:ExpectedSshKeygenSha256 -TrustedDirectories @($script:WindowsDirectory, $script:SystemDirectory, (Join-Path $script:SystemDirectory 'OpenSSH'))
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
if (-not $PSCmdlet.ShouldProcess(
        "$($script:SeedVmName) and $($script:FinalVmName)",
        'Create a private SSH key and derived autoinstall ISO, install one new seed VM, and leave its source VHDX powered off')) {
    return
}

# xorriso is checked after ShouldProcess so -WhatIf works even before it is installed.
# WSL may print its normal version banner on stderr; ProcessStartInfo captures both streams
# while the process exit code, rather than stderr presence, determines success.
$xorrisoVersionResult = Invoke-WslCaptured -Arguments @('--distribution', 'Ubuntu', '--user', 'root', '--exec', '/usr/bin/xorriso', '-version')
$xorrisoVersionText = @($xorrisoVersionResult.StandardOutput, $xorrisoVersionResult.StandardError | Where-Object { $_ }) -join [Environment]::NewLine
$versionMatch = [regex]::Match($xorrisoVersionText, '(?im)^\s*(xorriso\s+\d+\.\d+(?:\.\d+)?)')
if ($xorrisoVersionResult.ExitCode -ne 0 -or -not $versionMatch.Success) {
    throw 'xorriso is missing in WSL Ubuntu. Install it with: wsl.exe --distribution Ubuntu --user root -- apt-get update; then run: wsl.exe --distribution Ubuntu --user root -- apt-get install -y xorriso'
}
$xorrisoVersion = $versionMatch.Groups[1].Value

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
$derivedIsoHash = (Get-FileHash -LiteralPath $customIso -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
$derivedGrub = Get-IsoFileText -Path $customIso -IsoRelativePath 'boot/grub/grub.cfg'
$derivedUserData = Get-IsoFileText -Path $customIso -IsoRelativePath 'nocloud/user-data'
$derivedMetaData = Get-IsoFileText -Path $customIso -IsoRelativePath 'nocloud/meta-data'
if (@([regex]::Matches($derivedGrub, 'autoinstall')).Count -ne 2 -or
    @([regex]::Matches($derivedGrub, 'ds=nocloud;s=file:///cdrom/nocloud/')).Count -ne 2 -or
    $derivedUserData -cne ([IO.File]::ReadAllText($userDataPath)) -or
    $derivedMetaData -cne ([IO.File]::ReadAllText($metaDataPath))) {
    throw 'Derived ISO failed kernel-argument or NoCloud-content verification.'
}
$bootReport = Invoke-Xorriso -XorrisoArguments @('-indev', $outputWsl, '-report_el_torito', 'as_mkisofs')
if ($bootReport -notmatch '(?i)eltorito|boot') { throw 'xorriso did not report boot metadata for the derived ISO.' }

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
        canonicalSignatureCoversDerivedMedia = $false
    }
    provisioningKey = [ordered]@{
        publicFingerprint = $publicKeyFingerprint
        privateKeyPath = $keyPath
        privateKeyMaterialIncluded = $false
    }
    seedVm = [ordered]@{
        name = $script:SeedVmName
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
