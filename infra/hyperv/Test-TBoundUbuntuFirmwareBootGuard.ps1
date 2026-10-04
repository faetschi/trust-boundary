[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$sourceFiles = @(
    (Join-Path $PSScriptRoot 'Install-TBoundHostAutomation.ps1'),
    (Join-Path $PSScriptRoot 'TBoundHostActions.ps1'),
    (Join-Path $PSScriptRoot 'Test-UbuntuHyperVVm.ps1')
)
$script:FixtureCount = 0
$expectedVmId = [guid]'C676170C-31A1-48E3-AA74-1D845411D253'
$firmwarePath = 'HD(1,GPT,CE904A5D-4CBF-4D44-B146-2ADCE6396701,0x800,0x219800)/\EFI\ubuntu\shimx64.efi'

function New-BootEntryFixture {
    param([hashtable]$Overrides = @{})
    $entry = [ordered]@{
        BootType = 'File'
        FirmwarePath = $firmwarePath
        Description = 'Ubuntu'
        Device = $null
        VMId = $expectedVmId
        VMName = 'TBound-Ubuntu-2404'
        VMCheckpointId = [guid]::Empty
        VMCheckpointName = ''
        VMSnapshotId = [guid]::Empty
        VMSnapshotName = ''
        IsDeleted = $false
    }
    foreach ($key in $Overrides.Keys) { $entry[$key] = $Overrides[$key] }
    return [pscustomobject]$entry
}

function Assert-Fixture {
    param([string]$Name, [object]$Entry, [bool]$Expected)
    $actualPath = Get-ValidatedUbuntuFirmwarePath -BootEntry $Entry -VmId $expectedVmId -VmName 'TBound-Ubuntu-2404'
    $actual = -not [string]::IsNullOrEmpty($actualPath)
    if ($actual -ne $Expected) {
        throw "Fixture '$Name' expected $Expected but returned path '$actualPath'."
    }
    $script:FixtureCount++
}

foreach ($path in $sourceFiles) {
    $tokens = $null
    $parseErrors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count -gt 0) {
        throw "PowerShell parser errors in source file: $($parseErrors[0].Message)"
    }
    $definitions = @($ast.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
            $node.Name -eq 'Get-ValidatedUbuntuFirmwarePath'
    }, $true))
    if ($definitions.Count -ne 1) {
        throw "Expected exactly one boot parser function in $path; found $($definitions.Count)."
    }
    . ([scriptblock]::Create($definitions[0].Extent.Text))

    Assert-Fixture 'captured Ubuntu GPT shim entry' (New-BootEntryFixture) $true
    $freshPartitionPath = 'HD(1,GPT,12345678-1234-4234-8234-1234567890AB,0x1000,0x100000)/\EFI\ubuntu\shimx64.efi'
    Assert-Fixture 'fresh host GPT GUID and geometry' (New-BootEntryFixture @{ FirmwarePath = $freshPartitionPath }) $true
    Assert-Fixture 'wrong VM identity' (New-BootEntryFixture @{ VMId = [guid]::NewGuid() }) $false
    Assert-Fixture 'wrong VM name' (New-BootEntryFixture @{ VMName = 'Other-VM' }) $false
    Assert-Fixture 'network entry with misleading hard disk label' (New-BootEntryFixture @{ BootType = 'Network'; FirmwarePath = 'NETWORK'; Description = 'Hard Disk Ubuntu' }) $false
    Assert-Fixture 'DVD entry' (New-BootEntryFixture @{ BootType = 'DVD'; FirmwarePath = 'DVD(0)'; Description = 'Hard Disk' }) $false
    Assert-Fixture 'different EFI executable' (New-BootEntryFixture @{ FirmwarePath = 'HD(1,GPT,CE904A5D-4CBF-4D44-B146-2ADCE6396701,0x800,0x219800)/\EFI\Microsoft\Boot\bootmgfw.efi' }) $false
    Assert-Fixture 'non-GPT path' (New-BootEntryFixture @{ FirmwarePath = 'HD(1,MBR,0x800,0x219800)/\EFI\ubuntu\shimx64.efi' }) $false
    Assert-Fixture 'checkpoint identity' (New-BootEntryFixture @{ VMCheckpointId = [guid]::NewGuid() }) $false
    Assert-Fixture 'checkpoint name' (New-BootEntryFixture @{ VMSnapshotName = 'old-checkpoint' }) $false
    Assert-Fixture 'deleted firmware entry' (New-BootEntryFixture @{ IsDeleted = $true }) $false
    Assert-Fixture 'non-null firmware device' (New-BootEntryFixture @{ Device = [pscustomobject]@{ Name = 'disk' } }) $false
}

Write-Output ("Validated {0} pure firmware-entry fixtures across {1} source files." -f $script:FixtureCount, $sourceFiles.Count)