Set-StrictMode -Version Latest

$script:TBoundExpectedOperatorSid = 'S-1-5-21-2350865082-651554413-1548572510-1004'
$script:TBoundSystemSid = 'S-1-5-18'
$script:TBoundAdministratorsSid = 'S-1-5-32-544'

if (-not ('TBoundWin32DirectoryCreator' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

public static class TBoundWin32DirectoryCreator {
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
    IntPtr descriptor = IntPtr.Zero;
    uint descriptorSize;
    if (!ConvertStringSecurityDescriptorToSecurityDescriptor(sddl, 1, out descriptor, out descriptorSize))
      return Marshal.GetLastWin32Error();
    try {
      SECURITY_ATTRIBUTES attributes = new SECURITY_ATTRIBUTES();
      attributes.nLength = Marshal.SizeOf(typeof(SECURITY_ATTRIBUTES));
      attributes.lpSecurityDescriptor = descriptor;
      attributes.bInheritHandle = false;
      if (!CreateDirectoryW(path, ref attributes)) return Marshal.GetLastWin32Error();
      return 0;
    }
    finally {
      if (descriptor != IntPtr.Zero) LocalFree(descriptor);
    }
  }
}
'@ -ErrorAction Stop | Out-Null
}

function Resolve-TBoundOperatorSid {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Value)

    if ($Value -cne $script:TBoundExpectedOperatorSid) {
        throw 'Operator SID must exactly match the dedicated FABIAN\CodexSandboxOffline account.'
    }
    return [Security.Principal.SecurityIdentifier]::new($Value)
}

function Get-TBoundTaskSecurityDescriptor {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$OperatorSid)

    $sid = Resolve-TBoundOperatorSid $OperatorSid
    return ('O:SYG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;GRGX;;;{0})' -f $sid.Value)
}

function Get-TBoundTaskAdministratorSecurityDescriptor {
    [CmdletBinding()]
    param()
    return 'O:SYG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)'
}

function New-TBoundDirectoryWithSecurity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][Security.AccessControl.DirectorySecurity]$Acl
    )

    $fullPath = [IO.Path]::GetFullPath($Path)
    $sddl = $Acl.GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::All)
    $errorCode = [TBoundWin32DirectoryCreator]::CreateNew($fullPath, $sddl)
    if ($errorCode -eq 183 -or $errorCode -eq 80) {
        throw ('Refusing to adopt an existing directory: ' + $fullPath)
    }
    if ($errorCode -ne 0) {
        $message = [ComponentModel.Win32Exception]::new($errorCode).Message
        throw ('Could not create protected directory ' + $fullPath + ': ' + $message)
    }
    # CreateDirectoryW has already confirmed this exact leaf was newly created.
    # Avoid opening/listing a key directory that intentionally grants data access only to the operator.
    return $fullPath
}

function Add-TBoundFileSystemAccessRule {
    param(
        [Parameter(Mandatory = $true)][object]$Acl,
        [Parameter(Mandatory = $true)][Security.Principal.SecurityIdentifier]$Sid,
        [Parameter(Mandatory = $true)][Security.AccessControl.FileSystemRights]$Rights,
        [Parameter(Mandatory = $true)][bool]$Directory
    )

    $inheritance = [Security.AccessControl.InheritanceFlags]::None
    if ($Directory) {
        $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    }
    $rule = [Security.AccessControl.FileSystemAccessRule]::new(
        $Sid,
        $Rights,
        $inheritance,
        [Security.AccessControl.PropagationFlags]::None,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $Acl.AddAccessRule($rule)
}

function New-TBoundProtectedPathSecurity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][bool]$Directory,
        [Parameter(Mandatory = $true)][string]$OperatorSid,
        [ValidateSet('None', 'Read', 'Modify')][string]$OperatorAccess = 'None'
    )

    if (-not $Directory -and $OperatorAccess -eq 'Modify') {
        throw 'Modify access is only valid for a dedicated data directory.'
    }

    $operator = Resolve-TBoundOperatorSid $OperatorSid
    $system = [Security.Principal.SecurityIdentifier]::new($script:TBoundSystemSid)
    $admins = [Security.Principal.SecurityIdentifier]::new($script:TBoundAdministratorsSid)
    $acl = if ($Directory) {
        [Security.AccessControl.DirectorySecurity]::new()
    } else {
        [Security.AccessControl.FileSecurity]::new()
    }
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner($admins)
    Add-TBoundFileSystemAccessRule $acl $system ([Security.AccessControl.FileSystemRights]::FullControl) $Directory
    Add-TBoundFileSystemAccessRule $acl $admins ([Security.AccessControl.FileSystemRights]::FullControl) $Directory

    if ($OperatorAccess -eq 'Read') {
        Add-TBoundFileSystemAccessRule $acl $operator ([Security.AccessControl.FileSystemRights]::ReadAndExecute) $Directory
    }
    elseif ($OperatorAccess -eq 'Modify') {
        Add-TBoundFileSystemAccessRule $acl $operator ([Security.AccessControl.FileSystemRights]::Modify) $true
    }
    return $acl
}

function New-TBoundOperatorKeyDirectorySecurity {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$OperatorSid)

    $operator = Resolve-TBoundOperatorSid $OperatorSid
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner([Security.Principal.SecurityIdentifier]::new($script:TBoundAdministratorsSid))
    Add-TBoundFileSystemAccessRule $acl $operator ([Security.AccessControl.FileSystemRights]::FullControl) $true
    return $acl
}

function New-TBoundOperatorPublicDirectorySecurity {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$OperatorSid)

    return New-TBoundProtectedPathSecurity -Directory:$true -OperatorSid $OperatorSid -OperatorAccess Modify
}

function New-TBoundOperatorPrivateKeySecurity {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$OperatorSid)

    $operator = Resolve-TBoundOperatorSid $OperatorSid
    $acl = [Security.AccessControl.FileSecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner($operator)
    Add-TBoundFileSystemAccessRule $acl $operator ([Security.AccessControl.FileSystemRights]::FullControl) $false
    return $acl
}

function Test-TBoundOperatorKeyDirectorySecurity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][object]$Acl,
        [Parameter(Mandatory = $true)][string]$OperatorSid
    )

    try {
        $operator = Resolve-TBoundOperatorSid $OperatorSid
        if (-not $Acl.AreAccessRulesProtected) { return $false }
        if ($Acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -cne $script:TBoundAdministratorsSid) { return $false }
        $rules = @($Acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
        if ($rules.Count -ne 1) { return $false }
        $rule = $rules[0]
        $expectedInheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
        return ($rule.IdentityReference.Value -ceq $operator.Value -and
            $rule.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
            $rule.FileSystemRights -eq [Security.AccessControl.FileSystemRights]::FullControl -and
            $rule.InheritanceFlags -eq $expectedInheritance -and
            $rule.PropagationFlags -eq [Security.AccessControl.PropagationFlags]::None -and
            -not $rule.IsInherited)
    }
    catch {
        return $false
    }
}

function Get-TBoundAccessRuleKeys {
    param([Parameter(Mandatory = $true)][object[]]$Rules)

    return @($Rules | ForEach-Object {
        '{0}|{1}|{2}|{3}|{4}|{5}' -f $_.IdentityReference.Value,
            [int]$_.FileSystemRights,
            [int]$_.InheritanceFlags,
            [int]$_.PropagationFlags,
            [int]$_.AccessControlType,
            [bool]$_.IsInherited
    } | Sort-Object)
}

function Test-TBoundProtectedPathSecurity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][object]$Acl,
        [Parameter(Mandatory = $true)][bool]$Directory,
        [Parameter(Mandatory = $true)][string]$OperatorSid,
        [ValidateSet('None', 'Read', 'Modify')][string]$OperatorAccess = 'None'
    )

    try {
        $operator = Resolve-TBoundOperatorSid $OperatorSid
        if (-not $Acl.AreAccessRulesProtected) { return $false }
        $owner = $Acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
        if ($owner -cne $script:TBoundAdministratorsSid) { return $false }
        $expected = New-TBoundProtectedPathSecurity -Directory:$Directory -OperatorSid $operator.Value -OperatorAccess $OperatorAccess
        $actualRules = @(Get-TBoundAccessRuleKeys @($Acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])))
        $expectedRules = @(Get-TBoundAccessRuleKeys @($expected.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])))
        if ($actualRules.Count -ne $expectedRules.Count) { return $false }
        for ($index = 0; $index -lt $actualRules.Count; $index++) {
            if ($actualRules[$index] -cne $expectedRules[$index]) { return $false }
        }
        return $true
    }
    catch {
        return $false
    }
}

function Test-TBoundOperatorPrivateKeySecurity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][object]$Acl,
        [Parameter(Mandatory = $true)][string]$OperatorSid
    )

    try {
        $operator = Resolve-TBoundOperatorSid $OperatorSid
        if (-not $Acl.AreAccessRulesProtected) { return $false }
        if ($Acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -cne $operator.Value) { return $false }
        $actualRules = @($Acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
        if ($actualRules.Count -ne 1) { return $false }
        $rule = $actualRules[0]
        return ($rule.IdentityReference.Value -ceq $operator.Value -and
            $rule.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
            $rule.FileSystemRights -eq [Security.AccessControl.FileSystemRights]::FullControl -and
            $rule.InheritanceFlags -eq [Security.AccessControl.InheritanceFlags]::None -and
            $rule.PropagationFlags -eq [Security.AccessControl.PropagationFlags]::None -and
            -not $rule.IsInherited)
    }
    catch {
        return $false
    }
}

function Test-TBoundTaskSecurityDescriptor {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Sddl,
        [AllowEmptyString()][string]$OperatorSid = ''
    )

    try {
        $operator = if ([string]::IsNullOrEmpty($OperatorSid)) { $null } else { Resolve-TBoundOperatorSid $OperatorSid }
        $descriptor = [Security.AccessControl.CommonSecurityDescriptor]::new($false, $false, $Sddl)
        if (($descriptor.ControlFlags -band [Security.AccessControl.ControlFlags]::DiscretionaryAclProtected) -eq 0) { return $false }
        if ($descriptor.Owner.Value -cne $script:TBoundSystemSid -or $descriptor.Group.Value -cne $script:TBoundSystemSid) { return $false }
        $expectedAceCount = if ($null -eq $operator) { 2 } else { 3 }
        if ($null -eq $descriptor.DiscretionaryAcl -or $descriptor.DiscretionaryAcl.Count -ne $expectedAceCount) { return $false }

        $found = @{}
        foreach ($ace in $descriptor.DiscretionaryAcl) {
            if ($ace -isnot [Security.AccessControl.CommonAce] -or
                $ace.AceType -ne [Security.AccessControl.AceType]::AccessAllowed -or
                $ace.AceFlags -ne [Security.AccessControl.AceFlags]::None) { return $false }
            $sid = $ace.SecurityIdentifier.Value
            if ($found.ContainsKey($sid)) { return $false }
            $found[$sid] = [uint32]([BitConverter]::ToUInt32([BitConverter]::GetBytes([int]$ace.AccessMask), 0))
        }

        if ($found.Count -ne $expectedAceCount -or -not $found.ContainsKey($script:TBoundSystemSid) -or
            -not $found.ContainsKey($script:TBoundAdministratorsSid)) { return $false }
        $expectedFullControl = [uint32]::Parse('001F01FF', [Globalization.NumberStyles]::HexNumber)
        $expectedRunQuery = [uint32]::Parse('A0000000', [Globalization.NumberStyles]::HexNumber)
        if ($found[$script:TBoundSystemSid] -ne $expectedFullControl -or $found[$script:TBoundAdministratorsSid] -ne $expectedFullControl) { return $false }
        if ($null -eq $operator) { return $true }
        return ($found.ContainsKey($operator.Value) -and $found[$operator.Value] -eq $expectedRunQuery)
    }
    catch {
        return $false
    }
}

function Test-TBoundPinnedEd25519Fingerprint {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][AllowEmptyCollection()][string[]]$Lines,
        [Parameter(Mandatory = $true)][string]$ExpectedFingerprint
    )

    if ($ExpectedFingerprint -cnotmatch '^SHA256:[A-Za-z0-9+/]+={0,2}$' -or $Lines.Count -ne 1) { return $false }
    $line = [string]$Lines[0]
    $fingerprint = [regex]::Match($line, '(?:^|\s)(?<Fingerprint>SHA256:[A-Za-z0-9+/]+={0,2})(?:\s|$)')
    $algorithm = [regex]::Match($line, '\((?<Algorithm>[^)]+)\)')
    return ($fingerprint.Success -and $algorithm.Success -and
        $fingerprint.Groups['Fingerprint'].Value -ceq $ExpectedFingerprint -and
        $algorithm.Groups['Algorithm'].Value -ceq 'ED25519')
}

Export-ModuleMember -Function Resolve-TBoundOperatorSid, Get-TBoundTaskSecurityDescriptor, Get-TBoundTaskAdministratorSecurityDescriptor, New-TBoundDirectoryWithSecurity, New-TBoundProtectedPathSecurity, New-TBoundOperatorKeyDirectorySecurity, New-TBoundOperatorPublicDirectorySecurity, New-TBoundOperatorPrivateKeySecurity, Test-TBoundProtectedPathSecurity, Test-TBoundOperatorKeyDirectorySecurity, Test-TBoundOperatorPrivateKeySecurity, Test-TBoundTaskSecurityDescriptor, Test-TBoundPinnedEd25519Fingerprint
