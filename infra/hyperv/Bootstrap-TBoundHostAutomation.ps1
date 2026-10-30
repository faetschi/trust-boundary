[CmdletBinding()]
param(
    [string]$SourceRoot = 'C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary',
    [Parameter(Mandatory = $true)][string]$OperatorSid
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$expectedSourceRoot = 'C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary'
$expectedOperatorSid = 'S-1-5-21-2350865082-651554413-1548572510-1004'
$stageRoot = 'C:\ProgramData\TBoundHostAutomationSource'
$sourceFiles = [ordered]@{
    'Install-TBoundHostAutomation.ps1' = '7638C052C1A707FD587C11571034A1AC4A29343090F037B4BED97725E5A6DA01'
    'TBoundHostActions.ps1' = 'BB39E08E890B6DE6333E5B2E94038F09A70AD6CC53EA4E3EF9B3B7840D190400'
    'TBoundHostAccessPolicy.psm1' = 'C06BAE160364EB4D88914A3468FBB766C426010145ACA9D2C43DD803A993BB15'
    'Initialize-TBoundOperatorSsh.ps1' = 'E521CCB79AD0CF51E7F48A9B027082F5FD92D640697146F6DB0601BFFF596851'
}

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-FullPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    return [IO.Path]::GetFullPath($Path).TrimEnd('\')
}

function Get-Sha256Hex {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '') }
    finally { $sha.Dispose() }
}

function Test-ProtectedStageDirectory {
    param([Parameter(Mandatory = $true)][string]$Path)
    $acl = Get-Acl -LiteralPath $Path -ErrorAction Stop
    if (-not $acl.AreAccessRulesProtected -or
        $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -cne 'S-1-5-32-544') { return $false }
    $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    if ($rules.Count -ne 2) { return $false }
    $expected = @('S-1-5-18', 'S-1-5-32-544')
    foreach ($rule in $rules) {
        if ($rule.IdentityReference.Value -cnotin $expected -or
            $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
            $rule.FileSystemRights -ne [Security.AccessControl.FileSystemRights]::FullControl -or
            $rule.InheritanceFlags -ne ([Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit) -or
            $rule.PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None -or $rule.IsInherited) { return $false }
    }
    return @($rules | Where-Object { $_.IdentityReference.Value -ceq 'S-1-5-18' }).Count -eq 1 -and
        @($rules | Where-Object { $_.IdentityReference.Value -ceq 'S-1-5-32-544' }).Count -eq 1
}

function Test-ProtectedStageFile {
    param([Parameter(Mandatory = $true)][string]$Path)
    $acl = Get-Acl -LiteralPath $Path -ErrorAction Stop
    if (-not $acl.AreAccessRulesProtected -or
        $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -cne 'S-1-5-32-544') { return $false }
    $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    if ($rules.Count -ne 2) { return $false }
    $expected = @('S-1-5-18', 'S-1-5-32-544')
    foreach ($rule in $rules) {
        if ($rule.IdentityReference.Value -cnotin $expected -or
            $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
            $rule.FileSystemRights -ne [Security.AccessControl.FileSystemRights]::FullControl -or
            $rule.InheritanceFlags -ne [Security.AccessControl.InheritanceFlags]::None -or
            $rule.PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None -or $rule.IsInherited) { return $false }
    }
    return @($rules | Where-Object { $_.IdentityReference.Value -ceq 'S-1-5-18' }).Count -eq 1 -and
        @($rules | Where-Object { $_.IdentityReference.Value -ceq 'S-1-5-32-544' }).Count -eq 1
}

function New-StageFileSecurity {
    $acl = [Security.AccessControl.FileSecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $admins = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')
    $system = [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
    $acl.SetOwner($admins)
    foreach ($sid in @($system, $admins)) {
        $rule = [Security.AccessControl.FileSystemAccessRule]::new(
            $sid,
            [Security.AccessControl.FileSystemRights]::FullControl,
            [Security.AccessControl.InheritanceFlags]::None,
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow
        )
        $acl.AddAccessRule($rule)
    }
    return $acl
}

if (-not (Test-Administrator)) { throw 'Run this bootstrap from an elevated Windows PowerShell session.' }
if (-not [Environment]::Is64BitProcess -or $PSVersionTable.PSVersion.Major -ne 5 -or $PSVersionTable.PSVersion.Minor -ne 1) {
    throw 'Run this bootstrap in 64-bit Windows PowerShell 5.1.'
}
if ((Get-FullPath $SourceRoot) -cne (Get-FullPath $expectedSourceRoot)) { throw 'SourceRoot must be the exact reviewed trust-boundary repository.' }
if ($OperatorSid -cne $expectedOperatorSid) { throw 'OperatorSid must exactly match FABIAN\CodexSandboxOffline.' }
if (-not (Test-Path -LiteralPath 'C:\ProgramData' -PathType Container)) { throw 'C:\ProgramData is unavailable.' }
if ((Get-Item -LiteralPath 'C:\ProgramData' -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'C:\ProgramData is a reparse point.' }
if (Test-Path -LiteralPath $stageRoot) { throw ('Protected staging path already exists; refusing adoption: ' + $stageRoot) }

$sourceDirectory = Join-Path $SourceRoot 'infra\hyperv'
$verifiedSources = [Collections.Generic.List[object]]::new()
foreach ($entry in $sourceFiles.GetEnumerator()) {
    $sourcePath = Join-Path $sourceDirectory $entry.Key
    if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) { throw ('Missing pinned source file: ' + $sourcePath) }
    $bytes = [IO.File]::ReadAllBytes($sourcePath)
    $actualHash = Get-Sha256Hex $bytes
    if ($actualHash -cne $entry.Value) { throw ('Source SHA-256 differs from the chat-reviewed pin: ' + $entry.Key) }
    $verifiedSources.Add([pscustomobject]@{ Name = $entry.Key; Bytes = $bytes; Sha256 = $actualHash })
}

if (-not ('TBoundBootstrapDirectoryCreator' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
public static class TBoundBootstrapDirectoryCreator {
  [StructLayout(LayoutKind.Sequential)]
  private struct SECURITY_ATTRIBUTES {
    public int nLength;
    public IntPtr lpSecurityDescriptor;
    [MarshalAs(UnmanagedType.Bool)] public bool bInheritHandle;
  }
  [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
  private static extern bool ConvertStringSecurityDescriptorToSecurityDescriptor(
    string stringSecurityDescriptor, uint stringSdRevision, out IntPtr securityDescriptor, out uint securityDescriptorSize);
  [DllImport("kernel32.dll", CharSet = CharSet.Unicode, ExactSpelling = true, SetLastError = true)]
  private static extern bool CreateDirectoryW(string path, ref SECURITY_ATTRIBUTES securityAttributes);
  [DllImport("kernel32.dll", SetLastError = true)]
  private static extern IntPtr LocalFree(IntPtr memory);
  public static int CreateNew(string path, string sddl) {
    IntPtr descriptor = IntPtr.Zero; uint descriptorSize;
    if (!ConvertStringSecurityDescriptorToSecurityDescriptor(sddl, 1, out descriptor, out descriptorSize)) return Marshal.GetLastWin32Error();
    try {
      SECURITY_ATTRIBUTES attributes = new SECURITY_ATTRIBUTES();
      attributes.nLength = Marshal.SizeOf(typeof(SECURITY_ATTRIBUTES));
      attributes.lpSecurityDescriptor = descriptor; attributes.bInheritHandle = false;
      if (!CreateDirectoryW(path, ref attributes)) return Marshal.GetLastWin32Error();
      return 0;
    }
    finally { if (descriptor != IntPtr.Zero) LocalFree(descriptor); }
  }
}
'@ -ErrorAction Stop | Out-Null
}

$stageSddl = 'O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)'
$createError = [TBoundBootstrapDirectoryCreator]::CreateNew($stageRoot, $stageSddl)
if ($createError -eq 183 -or $createError -eq 80) { throw ('Refusing to adopt an existing protected staging path: ' + $stageRoot) }
if ($createError -ne 0) { throw ('Could not atomically create the protected staging path: ' + [ComponentModel.Win32Exception]::new($createError).Message) }
if (-not (Test-ProtectedStageDirectory $stageRoot)) { throw 'Protected staging directory owner or exact SYSTEM/Admin DACL read-back failed.' }
if (@(Get-ChildItem -LiteralPath $stageRoot -Force -ErrorAction Stop).Count -ne 0) { throw 'New protected staging directory is not empty.' }

foreach ($source in $verifiedSources) {
    $stagedPath = Join-Path $stageRoot $source.Name
    [IO.File]::WriteAllBytes($stagedPath, $source.Bytes)
    Set-Acl -LiteralPath $stagedPath -AclObject (New-StageFileSecurity) -ErrorAction Stop
    $stagedBytes = [IO.File]::ReadAllBytes($stagedPath)
    if ((Get-Sha256Hex $stagedBytes) -cne $source.Sha256) { throw ('Staged read-back hash mismatch: ' + $source.Name) }
    $stagedAcl = Get-Acl -LiteralPath $stagedPath -ErrorAction Stop
    if (-not (Test-ProtectedStageFile $stagedPath)) {
        throw ('Staged source ACL read-back failed: ' + $source.Name)
    }
}

$installerPath = Join-Path $stageRoot 'Install-TBoundHostAutomation.ps1'
& $installerPath -OperatorSid $expectedOperatorSid -Confirm:$false

Write-Host ('Hash-verified source bundle preserved at ' + $stageRoot)
Write-Host ('Installer completed for the exact operator SID ' + $expectedOperatorSid)
