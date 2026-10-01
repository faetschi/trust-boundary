[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ExpectedBranch = 'codex/tbound-prototype'
$ExpectedTBoundAttributesBlob = '1bbd6959aa788033f8382a3e750d9fdbaf8a98d4'
$AssetsRoot = 'F:\TBoundAssets'
$VmStorageRoot = 'F:\TBoundVMs'
$ExportRoot = Join-Path $AssetsRoot 'GuestHandoff'
$MaximumTrackedBytes = [int64]100GB
$MinimumFreeAfterExportBytes = [int64]700GB
$ManifestBudgetBytes = [int64]4096
$MetadataReserveBytes = [int64]4MB
$TarBlockBytes = [int64]512
$EstimateFixedPaddingBytes = [int64]1MB

$RepoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$stagingDirectory = $null
$finalDirectory = $null

function Invoke-Git {
    param([Parameter(Mandatory = $true)][string[]]$GitArguments)

    $gitOutput = & git -C $RepoRoot -c "safe.directory=$RepoRoot" @GitArguments 2>&1
    $gitExitCode = $LASTEXITCODE
    if ($gitExitCode -ne 0) {
        $detail = [string]::Join([Environment]::NewLine, @($gitOutput))
        throw "git $($GitArguments -join ' ') failed with exit code $gitExitCode. $detail"
    }

    return $gitOutput
}

function Assert-NoReparsePointInExistingPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $volumeRoot = [System.IO.Path]::GetPathRoot($fullPath)
    $currentPath = $volumeRoot
    $rootItem = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
    if (([int]$rootItem.Attributes -band [int][System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing a reparse-point volume root: $currentPath"
    }

    $relativePath = $fullPath.Substring($volumeRoot.Length)
    foreach ($segment in ($relativePath -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($segment)) { continue }
        $currentPath = Join-Path $currentPath $segment
        if (-not (Test-Path -LiteralPath $currentPath)) { break }

        $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction Stop
        if (([int]$item.Attributes -band [int][System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Refusing a path containing a reparse point: $currentPath"
        }
    }
}

function Get-TrackedStorageBytes {
    param([Parameter(Mandatory = $true)][string[]]$RootPaths)

    [int64]$totalBytes = 0
    foreach ($rootPath in $RootPaths) {
        if (-not (Test-Path -LiteralPath $rootPath -PathType Container)) {
            throw "Tracked storage directory does not exist: $rootPath"
        }
        $rootItem = Get-Item -LiteralPath $rootPath -Force -ErrorAction Stop
        if (($rootItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Tracked storage root is a reparse point: $rootPath"
        }

        $pendingDirectories = [System.Collections.Generic.Stack[string]]::new()
        $pendingDirectories.Push([System.IO.Path]::GetFullPath($rootPath))
        while ($pendingDirectories.Count -gt 0) {
            $currentDirectory = $pendingDirectories.Pop()
            foreach ($item in @(Get-ChildItem -LiteralPath $currentDirectory -Force -ErrorAction Stop)) {
                if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                    throw "Tracked storage contains a reparse point, junction, or symbolic link: $($item.FullName)"
                }
                if ($item.PSIsContainer) {
                    $pendingDirectories.Push($item.FullName)
                }
                else {
                    $totalBytes += [int64]$item.Length
                }
            }
        }
    }

    return [int64]$totalBytes
}

function Assert-StorageBudget {
    param(
        [Parameter(Mandatory = $true)][int64]$ProjectedAdditionalBytes,
        [Parameter(Mandatory = $true)][string]$Phase
    )

    Assert-NoReparsePointInExistingPath -Path $AssetsRoot
    Assert-NoReparsePointInExistingPath -Path $VmStorageRoot
    $trackedBytes = Get-TrackedStorageBytes -RootPaths @($AssetsRoot, $VmStorageRoot)
    if (($trackedBytes + $ProjectedAdditionalBytes) -gt $MaximumTrackedBytes) {
        throw ("Tracked TBound storage is {0:N2} GiB; the {1} projection would exceed the 100 GiB limit across {2} and {3}." -f ($trackedBytes / 1GB), $Phase, $AssetsRoot, $VmStorageRoot)
    }

    $drive = Get-PSDrive -Name F -ErrorAction Stop
    if ($drive.Provider.Name -ne 'FileSystem' -or $drive.Root -ine 'F:\') {
        throw 'F: is not the expected FileSystem drive.'
    }
    if ($null -eq $drive.Free -or [int64]$drive.Free -lt ($MinimumFreeAfterExportBytes + $ProjectedAdditionalBytes)) {
        throw "F: does not have enough free space for the $Phase projection while preserving the 700 GiB reserve."
    }

    return [pscustomobject]@{
        TrackedBytes = [int64]$trackedBytes
        FreeBytes = [int64]$drive.Free
    }
}

function Test-TarArchiveScope {
    param(
        [Parameter(Mandatory = $true)][string]$ArchivePath,
        [Parameter(Mandatory = $true)][System.Collections.Generic.HashSet[string]]$ExpectedFiles
    )

    $tarPath = (Get-Command -Name 'tar.exe' -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
    $memberOutput = & $tarPath -tf $ArchivePath 2>&1
    $memberExitCode = $LASTEXITCODE
    if ($memberExitCode -ne 0) {
        throw "tar could not read the staged archive member list (exit $memberExitCode)."
    }
    $verboseOutput = & $tarPath -tvf $ArchivePath 2>&1
    $verboseExitCode = $LASTEXITCODE
    if ($verboseExitCode -ne 0) {
        throw "tar could not inspect staged archive member types (exit $verboseExitCode)."
    }

    $members = @($memberOutput | ForEach-Object { ([string]$_).Trim() })
    $verboseMembers = @($verboseOutput | ForEach-Object { [string]$_ })
    if ($members.Count -eq 0 -or $members.Count -ne $verboseMembers.Count) {
        throw 'The staged archive has an empty or ambiguous member listing.'
    }

    $expectedDirectories = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    [void]$expectedDirectories.Add('tbound')
    foreach ($filePath in $ExpectedFiles) {
        $segments = $filePath.Split('/')
        for ($segmentIndex = 1; $segmentIndex -lt $segments.Length; $segmentIndex++) {
            $directoryPath = [string]::Join('/', $segments[0..($segmentIndex - 1)])
            [void]$expectedDirectories.Add($directoryPath)
        }
    }

    $seenFiles = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    for ($memberIndex = 0; $memberIndex -lt $members.Count; $memberIndex++) {
        $member = $members[$memberIndex]
        $verboseLine = $verboseMembers[$memberIndex]
        if ($member -notmatch '^tbound(?:/[A-Za-z0-9._-]+)*/?$' -or $member.Length -gt 247) {
            throw "The staged archive contains an unsafe or out-of-scope path: $member"
        }

        $normalizedMember = $member.TrimEnd('/')
        $pathSegments = $normalizedMember.Split('/')
        if ($pathSegments -contains '.' -or $pathSegments -contains '..' -or $pathSegments -contains '.git') {
            throw "The staged archive contains a special path component: $member"
        }
        if ([string]::IsNullOrEmpty($verboseLine) -or $verboseLine[0] -notin @('d', '-')) {
            throw "The staged archive contains a non-directory, non-regular member: $member"
        }

        if ($verboseLine[0] -eq 'd') {
            if (-not $expectedDirectories.Contains($normalizedMember)) {
                throw "The staged archive contains an unexpected directory: $member"
            }
        }
        else {
            if ($member.EndsWith('/') -or -not $ExpectedFiles.Contains($member)) {
                throw "The staged archive contains an unexpected regular file: $member"
            }
            if (-not $seenFiles.Add($member)) {
                throw "The staged archive contains a duplicate file path: $member"
            }
        }
    }

    if ($seenFiles.Count -ne $ExpectedFiles.Count) {
        throw 'The staged archive file set does not match the committed repository source inventory.'
    }
}

try {
    if (-not [System.IO.Directory]::Exists($RepoRoot)) {
        throw "Repository root does not exist: $RepoRoot"
    }
    Assert-NoReparsePointInExistingPath -Path $RepoRoot

    $reportedRoot = [System.IO.Path]::GetFullPath(([string](Invoke-Git @('rev-parse', '--show-toplevel') | Select-Object -First 1)).Trim())
    if (-not [string]::Equals($reportedRoot, $RepoRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Script is not under the Git worktree it resolved: $reportedRoot"
    }

    $branch = ([string](Invoke-Git @('symbolic-ref', '--quiet', '--short', 'HEAD') | Select-Object -First 1)).Trim()
    if ($branch -cne $ExpectedBranch) {
        throw "Expected branch '$ExpectedBranch'; found '$branch'. No bundle was written."
    }

    $status = @(Invoke-Git @('status', '--porcelain=v1', '--untracked-files=all'))
    if ($status.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace([string]::Join('', $status))) {
        throw "The worktree is dirty. Commit or remove changes before exporting. First status entry: $($status[0])"
    }

    $commit = ([string](Invoke-Git @('rev-parse', '--verify', 'HEAD^{commit}') | Select-Object -First 1)).Trim()
    if ($commit -notmatch '^[0-9a-f]{40,64}$') {
        throw 'Git returned an invalid commit object ID.'
    }
    $shortCommit = $commit.Substring(0, 12)
    $gitVersion = ([string](Invoke-Git @('--version') | Select-Object -First 1)).Trim()

    # Git archive honors export-ignore/export-subst attributes. Refuse local
    # overrides and permit only the reviewed LF checkout rules at the repository root.
    $attributeConfig = & git -C $RepoRoot -c "safe.directory=$RepoRoot" config --show-origin --get-all core.attributesFile 2>&1
    $attributeConfigExit = $LASTEXITCODE
    if ($attributeConfigExit -eq 0 -and @($attributeConfig).Count -gt 0) {
        throw 'A configured core.attributesFile could alter the archive. Remove that configuration and retry.'
    }
    if ($attributeConfigExit -gt 1) {
        throw 'Could not verify Git attribute configuration.'
    }
    $allTrackedPaths = @(Invoke-Git @('ls-tree', '-r', '--full-tree', '--name-only', $commit))
    $trackedAttributeFiles = @($allTrackedPaths | Where-Object { $_ -match '(^|/)\.gitattributes$' })
    if ($trackedAttributeFiles.Count -ne 1 -or $trackedAttributeFiles[0] -cne '.gitattributes') {
        throw 'Expected exactly one committed attribute file at .gitattributes; other attribute files are not allowed.'
    }
    $attributeEntry = @(Invoke-Git @('ls-tree', '-r', '-l', '--full-tree', $commit, '--', '.gitattributes'))
    $attributeMatch = [regex]::Match([string]($attributeEntry | Select-Object -First 1), '^100644 blob (?<oid>[0-9a-f]{40,64})\s+[0-9]+\t\.gitattributes$')
    if ($attributeEntry.Count -ne 1 -or -not $attributeMatch.Success -or $attributeMatch.Groups['oid'].Value -cne $ExpectedTBoundAttributesBlob) {
        throw '.gitattributes does not match the reviewed text/eol=lf rules; archive-affecting attributes are not allowed.'
    }
    $infoAttributesPath = ([string](Invoke-Git @('rev-parse', '--git-path', 'info/attributes') | Select-Object -First 1)).Trim()
    if (Test-Path -LiteralPath $infoAttributesPath -PathType Leaf) {
        throw "A repository info/attributes file could alter archive contents: $infoAttributesPath"
    }
    $env:GIT_ATTR_NOSYSTEM = '1'

    $inventory = @(Invoke-Git @('ls-tree', '-rl', '--full-tree', $commit))
    if ($inventory.Count -eq 0) {
        throw 'The committed repository tree is empty.'
    }

    [int64]$blobBytes = 0
    [int64]$fileCount = 0
    $expectedSourceFiles = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    $expectedArchiveDirectories = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    [void]$expectedArchiveDirectories.Add('tbound')
    foreach ($line in $inventory) {
        $match = [regex]::Match([string]$line, '^(?<mode>[0-9]{6}) blob [0-9a-f]{40,64} (?<size>[0-9]+)\t(?<path>.+)$')
        if (-not $match.Success) {
            throw "Unexpected Git tree entry; refusing an ambiguous source inventory: $line"
        }

        $mode = $match.Groups['mode'].Value
        if ($mode -cnotin @('100644', '100755')) {
            throw "Refusing non-regular tracked file mode $mode in the source subtree."
        }

        $path = $match.Groups['path'].Value
        if ($path -notmatch '^[A-Za-z0-9._/-]+$' -or $path.Length -gt 240) {
            throw "Refusing an unusual or overlong source path: $path"
        }
        [void]$expectedSourceFiles.Add(('tbound/' + $path))
        $segments = @('tbound') + $path.Split('/')
        if (@($segments | Where-Object { $_.Length -gt 100 }).Count -gt 0) {
            throw "Refusing a path component longer than 100 characters: $path"
        }
        if ($segments -contains '.' -or $segments -contains '..' -or $segments -contains '.git') {
            throw "Refusing a path with a special component: $path"
        }
        for ($segmentIndex = 1; $segmentIndex -lt $segments.Length; $segmentIndex++) {
            $directoryPath = [string]::Join('/', $segments[0..($segmentIndex - 1)])
            [void]$expectedArchiveDirectories.Add($directoryPath)
        }
        if ($segments | Where-Object { $_ -in @('node_modules', 'vendor', '.venv', 'venv', 'site-packages', 'bower_components') }) {
            throw "Refusing a vendored dependency path: $path"
        }
        $leaf = $segments[-1]
        if ($leaf -match '(?i)^(?:\.env(?:\..*)?|id_(?:rsa|ed25519)|.*(?:private[-_]?key|credentials?)(?:\..*)?)$' -or
            $leaf -match '(?i)\.(?:pem|p12|pfx|key|kdbx)$') {
            throw "Refusing a secret-like tracked filename: $path"
        }

        [int64]$entryBytes = 0
        if (-not [int64]::TryParse($match.Groups['size'].Value, [ref]$entryBytes)) {
            throw "Could not parse the tracked file size for: $path"
        }
        $blobBytes += $entryBytes
        $fileCount++
    }

    $secretPattern = '-----BEGIN (OPENSSH|RSA|EC|DSA|ENCRYPTED) PRIVATE KEY-----|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|https?://[^/[:space:]:]+:[^@[:space:]]+@'
    & git -C $RepoRoot -c "safe.directory=$RepoRoot" grep --quiet -I -E -e $secretPattern $commit 2>$null
    $secretScanExit = $LASTEXITCODE
    if ($secretScanExit -eq 0) {
        throw 'A high-confidence credential pattern was found in committed tbound content. Review the source before exporting.'
    }
    if ($secretScanExit -ne 1) {
        throw "Could not complete the committed-source credential scan (git grep exit $secretScanExit)."
    }

    # Two 512-byte blocks per file bound its header plus payload padding; the
    # directory count covers every directory header, with fixed PAX/footer slack.
    $estimatedArchiveBytes = $blobBytes + ($fileCount * (2 * $TarBlockBytes)) + ($expectedArchiveDirectories.Count * $TarBlockBytes) + $EstimateFixedPaddingBytes
    $projectedBundleBytes = $estimatedArchiveBytes + $ManifestBudgetBytes + $MetadataReserveBytes

    $exportFullPath = [System.IO.Path]::GetFullPath($ExportRoot)
    if (-not [string]::Equals([System.IO.Path]::GetPathRoot($exportFullPath), 'F:\', [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "The export destination must remain on F:; resolved path: $exportFullPath"
    }
    if (-not [string]::Equals($exportFullPath, 'F:\TBoundAssets\GuestHandoff', [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "The export destination differs from the reviewed fixed path: $exportFullPath"
    }

    $preflightSnapshot = Assert-StorageBudget -ProjectedAdditionalBytes $projectedBundleBytes -Phase 'archive plus manifest and metadata reserve'
    $null = Get-Command -Name 'tar.exe' -CommandType Application -ErrorAction Stop

    Assert-NoReparsePointInExistingPath -Path $exportFullPath
    $finalDirectory = Join-Path $exportFullPath "tbound-$shortCommit"
    if (Test-Path -LiteralPath $finalDirectory) {
        throw "Refusing to overwrite an existing export directory: $finalDirectory"
    }

    if (-not [System.IO.Directory]::Exists($exportFullPath)) {
        [void][System.IO.Directory]::CreateDirectory($exportFullPath)
    }
    Assert-NoReparsePointInExistingPath -Path $exportFullPath
    $staleStageFilter = ".tbound-$shortCommit.staging-*"
    $staleStages = @(Get-ChildItem -LiteralPath $exportFullPath -Directory -Force -Filter $staleStageFilter -ErrorAction Stop)
    if ($staleStages.Count -gt 0) {
        throw "A staging folder for this commit already exists; inspect it before retrying: $($staleStages[0].FullName)"
    }
    $stagingName = ".tbound-$shortCommit.staging-$([guid]::NewGuid().ToString('N'))"
    $stagingDirectory = Join-Path $exportFullPath $stagingName
    if (Test-Path -LiteralPath $stagingDirectory) {
        throw "Refusing to reuse an existing staging folder: $stagingDirectory"
    }
    [void](New-Item -Path $stagingDirectory -ItemType Directory -ErrorAction Stop)
    Assert-NoReparsePointInExistingPath -Path $stagingDirectory

    # Recheck as close as possible to the archive write. This is a measured
    # guard; the workflow requires VM/checkpoint writers to be quiesced.
    $beforeWriteSnapshot = Assert-StorageBudget -ProjectedAdditionalBytes $projectedBundleBytes -Phase 'immediately before archive creation'

    $archiveName = "tbound-$shortCommit.tar"
    $manifestName = "tbound-$shortCommit.manifest.txt"
    $archivePath = Join-Path $stagingDirectory $archiveName
    $manifestPath = Join-Path $stagingDirectory $manifestName
    if ((Test-Path -LiteralPath $archivePath) -or (Test-Path -LiteralPath $manifestPath)) {
        throw 'An output file already exists; refusing to overwrite it.'
    }

    $archiveOutput = & git -C $RepoRoot -c "safe.directory=$RepoRoot" -c 'tar.umask=0000' archive --format=tar --prefix=tbound/ --output=$archivePath $commit 2>&1
    $archiveExit = $LASTEXITCODE
    if ($archiveExit -ne 0) {
        $detail = [string]::Join([Environment]::NewLine, @($archiveOutput))
        throw "git archive failed with exit code $archiveExit. $detail"
    }

    $archiveItem = Get-Item -LiteralPath $archivePath -Force -ErrorAction Stop
    if (([int]$archiveItem.Attributes -band [int][System.IO.FileAttributes]::ReparsePoint) -ne 0 -or $archiveItem.Length -le 0) {
        throw "Archive size is outside the allowed range: $($archiveItem.Length) bytes."
    }
    Test-TarArchiveScope -ArchivePath $archivePath -ExpectedFiles $expectedSourceFiles
    $archiveHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()

    $manifestLines = @(
        'format=git-archive-tar'
        "source_branch=$branch"
        "source_commit=$commit"
        'source_subtree=repository-root'
        'archive_prefix=tbound/'
        "archive_file=$archiveName"
        "archive_bytes=$($archiveItem.Length)"
        "archive_sha256=$archiveHash"
        "git_version=$gitVersion"
    )
    $manifestText = [string]::Join("`n", $manifestLines) + "`n"
    $expectedManifestBytes = [System.Text.Encoding]::UTF8.GetByteCount($manifestText)
    if ($expectedManifestBytes -gt $ManifestBudgetBytes) {
        throw 'The generated manifest exceeds its conservative size budget.'
    }
    $manifestProjection = [int64]$expectedManifestBytes + $MetadataReserveBytes
    $manifestWriteSnapshot = Assert-StorageBudget -ProjectedAdditionalBytes $manifestProjection -Phase 'actual archive plus exact manifest and metadata reserve'
    [System.IO.File]::WriteAllText($manifestPath, $manifestText, [System.Text.UTF8Encoding]::new($false))

    $manifestItem = Get-Item -LiteralPath $manifestPath -Force -ErrorAction Stop
    if (([int]$manifestItem.Attributes -band [int][System.IO.FileAttributes]::ReparsePoint) -ne 0 -or $manifestItem.Length -ne $expectedManifestBytes) {
        throw 'The staged manifest is a reparse point or differs from its expected byte count.'
    }
    $manifestHash = (Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $actualBundleBytes = [int64]$archiveItem.Length + [int64]$manifestItem.Length
    $stagedSnapshot = Assert-StorageBudget -ProjectedAdditionalBytes $MetadataReserveBytes -Phase 'completed staged bundle plus metadata reserve'

    if (Test-Path -LiteralPath $finalDirectory) {
        throw "The final export directory appeared before publish; refusing to replace it: $finalDirectory"
    }
    [System.IO.Directory]::Move($stagingDirectory, $finalDirectory)
    Assert-NoReparsePointInExistingPath -Path $finalDirectory

    $publishedArchivePath = Join-Path $finalDirectory $archiveName
    $publishedManifestPath = Join-Path $finalDirectory $manifestName
    $publishedArchiveHash = (Get-FileHash -LiteralPath $publishedArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    $publishedManifestHash = (Get-FileHash -LiteralPath $publishedManifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($publishedArchiveHash -cne $archiveHash -or $publishedManifestHash -cne $manifestHash) {
        throw 'An artifact hash changed during same-volume publish; inspect the published directory.'
    }
    $publishedSnapshot = Assert-StorageBudget -ProjectedAdditionalBytes $MetadataReserveBytes -Phase 'published bundle plus metadata reserve'

    Write-Output "Bundle: $publishedArchivePath"
    Write-Output "Evidence: $publishedManifestPath"
    Write-Output "Commit: $commit"
    Write-Output "SHA-256: $archiveHash"
    Write-Output "Manifest SHA-256: $manifestHash"
    Write-Output "Bundle bytes: $actualBundleBytes"
    Write-Output 'No guest connection or provisioning action was performed.'
}
catch {
    [Console]::Error.WriteLine("Export stopped: $($_.Exception.Message)")
    if ($null -ne $stagingDirectory -and [System.IO.Directory]::Exists($stagingDirectory)) {
        [Console]::Error.WriteLine("No cleanup was attempted. Inspect retained staging directory: $stagingDirectory")
    }
    if ($null -ne $finalDirectory -and [System.IO.Directory]::Exists($finalDirectory)) {
        [Console]::Error.WriteLine("No cleanup was attempted. Inspect published directory: $finalDirectory")
    }
    exit 1
}
