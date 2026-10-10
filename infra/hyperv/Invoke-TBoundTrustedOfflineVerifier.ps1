[CmdletBinding()]
param(
    [switch]$PolicyOnly,
    [string]$InitialGuestIPv4 = '172.24.196.42',
    [int]$RunBudgetMinutes = 45
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:TaskRoot = '\TBound'
$script:ReceiptRoot = 'C:\ProgramData\TBoundHostAutomation\receipts'
$script:ProfileRoot = 'C:\ProgramData\TBoundHostAutomation'
$script:KnownLeaves = @('Inspect','Disconnect','StopOffline','ConnectOff','StartTrustedMaintenance')
$script:ExpectedVmName = 'TBound-Ubuntu-2404'
$script:ExpectedSwitch = 'Default Switch'
$script:SshUser = 'tboundadmin'
$script:HostAlias = '172.19.207.142'
$script:HostFingerprint = 'SHA256:22ZTQXN5L54AhovENJ0Eo7ZViomYVvlHRWfbG3izY5g'
$script:OperatorSid = 'S-1-5-21-2350865082-651554413-1548572510-1004'
$script:HostAccessPolicySha256 = 'C06BAE160364EB4D88914A3468FBB766C426010145ACA9D2C43DD803A993BB15'
$script:PrivateGuestPrefix = '/home/tboundadmin/.tbound-offline-queued.'
$script:ReviewedSourceCommit = '5ccc52b2fa35be3dde9d3839f63ec613627b796b'
$script:ReviewedLauncherSha256 = 'F0BF235943EE13B3EE44614D6E172BF8179E3DEADDE882662EA8009C6E711148'
$script:ArchivedLauncherSha256 = '63636080B9A90A433C4829C60DF0692090CF2BA158350B34ED4F5E3F10530B15'
$script:ReviewedVerifierSha256 = 'CB4D8D2A235548B2A3A1528BEFB880E0DC5F1619D5AC46591672ED17547B204A'
if (-not ('TBoundBoundedProcessRunner' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Threading.Tasks;
public sealed class TBoundProcessResult {
  public int ExitCode; public byte[] Stdout; public byte[] Stderr; public bool OutputLimitExceeded; public bool TimedOut;
}
public static class TBoundBoundedProcessRunner {
  private static string Quote(string value) {
    if (value.Length > 0 && value.IndexOfAny(new char[] {' ', '\t', '\n', '\v', '"'}) < 0) return value;
    var b = new StringBuilder(); b.Append('"'); int slashes = 0;
    foreach (char c in value) {
      if (c == '\\') { slashes++; continue; }
      if (c == '"') { b.Append('\\', slashes * 2 + 1); b.Append('"'); slashes = 0; continue; }
      b.Append('\\', slashes); slashes = 0; b.Append(c);
    }
    b.Append('\\', slashes * 2); b.Append('"'); return b.ToString();
  }
  private static void Pump(Stream source, MemoryStream target, object gate, Process process, int maxBytes, int[] total, bool[] overflow) {
    byte[] buffer = new byte[8192]; int count;
    try {
      while ((count = source.Read(buffer, 0, buffer.Length)) > 0) {
        lock (gate) {
          if (total[0] + count > maxBytes) {
            overflow[0] = true; try { if (!process.HasExited) process.Kill(); } catch {} return;
          }
          total[0] += count; target.Write(buffer, 0, count);
        }
      }
    } catch {}
  }
  public static TBoundProcessResult Run(string file, string[] arguments, int maxBytes, int timeoutMilliseconds) {
    var p = new Process(); var info = new ProcessStartInfo();
    info.FileName = file; info.Arguments = String.Join(" ", Array.ConvertAll(arguments, Quote));
    info.UseShellExecute = false; info.CreateNoWindow = true; info.RedirectStandardOutput = true; info.RedirectStandardError = true;
    p.StartInfo = info; p.Start();
    var stdout = new MemoryStream(); var stderr = new MemoryStream(); var gate = new object();
    var total = new int[] { 0 }; var overflow = new bool[] { false };
    var outTask = Task.Factory.StartNew(() => Pump(p.StandardOutput.BaseStream, stdout, gate, p, maxBytes, total, overflow), TaskCreationOptions.LongRunning);
    var errTask = Task.Factory.StartNew(() => Pump(p.StandardError.BaseStream, stderr, gate, p, maxBytes, total, overflow), TaskCreationOptions.LongRunning);
    bool exited = p.WaitForExit(timeoutMilliseconds);
    if (!exited) { try { p.Kill(); } catch {} }
    Task.WaitAll(new Task[] { outTask, errTask });
    var result = new TBoundProcessResult(); result.ExitCode = exited ? p.ExitCode : -1;
    result.Stdout = stdout.ToArray(); result.Stderr = stderr.ToArray(); result.OutputLimitExceeded = overflow[0]; result.TimedOut = !exited;
    p.Dispose(); stdout.Dispose(); stderr.Dispose(); return result;
  }
}
'@
}
function Get-TBoundReceiptPolicyFailure {
    param($Receipt, [string]$Operation, [string]$VmId, [string]$AdapterId, [string]$MacAddress, [string]$ProfileSha256,
        [string]$PreviousInvocationId, [DateTimeOffset]$NowUtc = [DateTimeOffset]::UtcNow, [int]$MaxAgeMinutes = 15)
    if ($null -eq $Receipt) { return 'receipt missing' }
    if ([int]$Receipt.schema -ne 1) { return 'schema mismatch' }
    if ([string]$Receipt.task -cne ('\TBound\' + $Operation) -or [string]$Receipt.operation -cne $Operation) { return 'cross-operation receipt' }
    if ([string]$Receipt.result -cne 'PASS') { return 'host action did not PASS' }
    $invocation = [guid]::Empty
    if (-not [guid]::TryParse([string]$Receipt.invocationId, [ref]$invocation) -or $invocation -eq [guid]::Empty) { return 'invalid invocation id' }
    if ($PreviousInvocationId -and $invocation.ToString() -eq $PreviousInvocationId) { return 'stale invocation id' }
    $start = [DateTimeOffset]::MinValue; $done = [DateTimeOffset]::MinValue
    if (-not [DateTimeOffset]::TryParse([string]$Receipt.startedUtc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::AssumeUniversal, [ref]$start) -or
        -not [DateTimeOffset]::TryParse([string]$Receipt.completedUtc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::AssumeUniversal, [ref]$done)) { return 'invalid receipt time' }
    if ($start -gt $done -or $done -gt $NowUtc.AddMinutes(1) -or $done -lt $NowUtc.AddMinutes(-$MaxAgeMinutes)) { return 'stale or future receipt' }
    if ([string]$Receipt.vmId -notmatch '^[0-9a-fA-F-]{36}$' -or [string]$Receipt.adapterId -notmatch '^[0-9a-fA-F-]{36}$') { return 'invalid VM or adapter identity' }
    if (($VmId -and [string]$Receipt.vmId -cne $VmId) -or ($AdapterId -and [string]$Receipt.adapterId -cne $AdapterId)) { return 'VM or adapter identity mismatch' }
    if ([string]$Receipt.adapterMacAddress -cnotmatch '^(?:[0-9A-F]{2}:){5}[0-9A-F]{2}$' -or ($MacAddress -and [string]$Receipt.adapterMacAddress -cne $MacAddress)) { return 'adapter MAC mismatch' }
    if ([string]$Receipt.profileSha256 -cnotmatch '^[0-9A-F]{64}$' -or ($ProfileSha256 -and [string]$Receipt.profileSha256 -cne $ProfileSha256)) { return 'profile hash mismatch' }
    $iv = [string]$Receipt.initialVmState; $fv = [string]$Receipt.finalVmState
    $isw = [string]$Receipt.initialSwitchName; $fsw = [string]$Receipt.finalSwitchName
    switch ($Operation) {
        'Inspect' { if ($iv -cne $fv -or $isw -cne $fsw) { return 'Inspect changed VM or switch state' } }
        'Disconnect' { if ($iv -notin @('Running','Off') -or $fv -cne $iv -or ($isw -and $isw -cne 'Default Switch') -or $fsw) { return 'Disconnect transition mismatch' } }
        'StopOffline' { if ($iv -cne 'Running' -or $isw -or $fv -cne 'Off' -or $fsw) { return 'StopOffline did not establish Running/disconnected to Off/disconnected' } }
        'ConnectOff' { if ($iv -cne 'Off' -or $fv -cne 'Off' -or $isw -or $fsw -cne 'Default Switch') { return 'ConnectOff transition mismatch' } }
        'StartTrustedMaintenance' { if ($iv -cne 'Off' -or $fv -cne 'Running' -or $isw -cne 'Default Switch' -or $fsw -cne 'Default Switch') { return 'maintenance boot transition mismatch' } }
        default { return 'operation is not allowlisted' }
    }
    return $null
}

function Get-TBoundQueueProofFailure {
    param([string]$PidText, [string]$RunPath, [string]$RecordedPath, [string]$Status,
        [string]$ProcessUid, [string]$ExpectedUid, [string]$RunDirOwner, [string]$RunDirMode,
        [string]$WorkingDirectory, [string]$ExpectedWorkingDirectory, [string[]]$Argv)
    if ($PidText -cnotmatch '^[1-9][0-9]{0,9}$') { return 'PID is not decimal' }
    $pidValue = 0L
    if (-not [long]::TryParse($PidText, [ref]$pidValue) -or $pidValue -gt 2147483647) { return 'PID is out of range' }
    if ($RunPath -cnotmatch '^/home/tboundadmin/\.tbound-offline-queued\.[A-Za-z0-9]{10}$' -or $RecordedPath -cne $RunPath) { return 'run path is outside exact private prefix or contradictory' }
    if ($Status -cne 'WAITING_FOR_OFFLINE') { return 'launcher is not waiting for offline gate' }
    if ($ExpectedUid -cnotmatch '^[0-9]{1,8}$' -or $ProcessUid -cne $ExpectedUid -or $RunDirOwner -cne $ExpectedUid -or $RunDirMode -cne '700') { return 'launcher UID or private directory mode mismatch' }
    if ($WorkingDirectory -cne $ExpectedWorkingDirectory) { return 'launcher working directory mismatch' }
    if ($Argv.Count -ne 3 -or $Argv[0] -notmatch '(^|/)bash$' -or $Argv[1] -cne ($ExpectedWorkingDirectory + '/infra/guest/run-offline-queued.sh') -or $Argv[2] -cne '--host-adapter-disconnected') { return 'launcher argv mismatch' }
    return $null
}
function Test-TBoundCleanupDisconnectRequired {
    param([bool]$CleanupEnabled,[bool]$RunFailed,[bool]$QueueStarted,[bool]$DisconnectRequested)
    return ($CleanupEnabled -and $RunFailed)
}
function Get-TBoundReconnectGateFailure {
    param($OffReceipt, $ObservedReceipt, [string]$VmId, [string]$AdapterId, [string]$MacAddress, [string]$ProfileSha256, [string]$PreviousInvocationId)
    $failure = Get-TBoundReceiptPolicyFailure $OffReceipt 'StopOffline' $VmId $AdapterId $MacAddress $ProfileSha256 $PreviousInvocationId
    if ($failure) { return $failure }
    if ([string]$OffReceipt.finalVmState -cne 'Off' -or [string]$OffReceipt.finalSwitchName) { return 'only host-observed Off plus disconnected permits reconnect' }
    $failure = Get-TBoundReceiptPolicyFailure $ObservedReceipt 'Inspect' $VmId $AdapterId $MacAddress $ProfileSha256 $null
    if ($failure) { return $failure }
    if ([string]$ObservedReceipt.finalVmState -cne 'Off' -or [string]$ObservedReceipt.finalSwitchName) { return 'fresh host Inspect did not confirm Off and disconnected' }
    return $null
}

function Get-TBoundTestResult {
    param([bool]$TimedOut, [hashtable]$Artifacts, $Report)
    if ($TimedOut) { return 'ABORTED_UNKNOWN' }
    foreach ($name in @('status','runner.exit-code','launcher.exit-code','completed','verification.json','verification.stderr','launcher.log','launcher.pid','location')) {
        if (-not $Artifacts.ContainsKey($name) -or $null -eq $Artifacts[$name]) { return 'ABORTED_UNKNOWN' }
    }
    $finished=[DateTimeOffset]::MinValue
    if (-not [DateTimeOffset]::TryParse($Artifacts.completed.Trim(),[Globalization.CultureInfo]::InvariantCulture,[Globalization.DateTimeStyles]::AssumeUniversal,[ref]$finished)) { return 'ABORTED_UNKNOWN' }
    if ($null -eq $Report -or $Report.schema -cne 'tbound.guest-verification/v2' -or @($Report.failures).Count -eq 0 -and $Report.status -cne 'PASS') { return 'ABORTED_UNKNOWN' }
    $status=$Artifacts.status.Trim(); $runner=$Artifacts['runner.exit-code'].Trim(); $launcher=$Artifacts['launcher.exit-code'].Trim()
    if ($runner -notmatch '^(0|[1-9][0-9]{0,2})$' -or $launcher -cne $runner) { return 'ABORTED_UNKNOWN' }
    if ($status -cne [string]$Report.status) { return 'ABORTED_UNKNOWN' }
    if ($Report.cleanup.status -cne 'PASS') { return 'ABORTED_UNKNOWN' }
    if ($status -ceq 'FAIL') {
        if ($runner -eq '0' -or @($Report.failures).Count -eq 0) { return 'ABORTED_UNKNOWN' }
        return 'FAIL'
    }
    if ($status -cne 'PASS' -or $runner -cne '0' -or @($Report.failures).Count -ne 0 -or
        $Report.guest.offline_prerequisite.host_adapter_disconnected_attested -ne $true) { return 'ABORTED_UNKNOWN' }
    foreach ($claim in @('live_provider_exchange','live_effect_execution','containment_established','g1_durability_established')) {
        if ($Report.claims.$claim -ne $false) { return 'ABORTED_UNKNOWN' }
    }
    if ($null -eq $Report.checks -or @($Report.checks.PSObject.Properties).Count -eq 0) { return 'ABORTED_UNKNOWN' }
    foreach ($check in $Report.checks.PSObject.Properties) { if ($check.Value.status -cne 'PASS') { return 'ABORTED_UNKNOWN' } }
    return 'PASS'
}
function ConvertTo-TBoundSafeRemotePath {
    param([string]$Path)
    if ($Path -cnotmatch '^/home/tboundadmin/\.tbound-offline-queued\.[A-Za-z0-9]{10}$') { throw 'Rejected unsafe guest run path.' }
    return $Path
}

function Get-TBoundSshPath {
    $candidate = Join-Path $env:WINDIR 'System32\OpenSSH\ssh.exe'
    if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) { throw 'Windows OpenSSH ssh.exe is unavailable.' }
    return $candidate
}
function Get-Sha256Hex {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '') }
    finally { $sha.Dispose() }
}
function Get-TBoundSshContext {
    $installRoot = Join-Path $env:ProgramData 'TBoundHostAutomation'
    $keyRoot = Join-Path $installRoot 'operator-ssh'
    $publicRoot = Join-Path $installRoot 'operator-public'
    $key = Join-Path $keyRoot 'guest-maintenance-ed25519'
    $known = Join-Path $publicRoot 'known_hosts'
    $policyPath = Join-Path $installRoot 'TBoundHostAccessPolicy.psm1'
    $ssh = Join-Path $env:WINDIR 'System32\OpenSSH\ssh.exe'
    $scp = Join-Path $env:WINDIR 'System32\OpenSSH\scp.exe'
    $sshKeygen = Join-Path $env:WINDIR 'System32\OpenSSH\ssh-keygen.exe'
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    if ($null -eq $identity.User -or $identity.User.Value -cne $script:OperatorSid) { throw 'Trusted SSH controller must run as the exact FABIAN\CodexSandboxOffline SID.' }
    foreach ($p in @($key,$known,$policyPath,$ssh,$scp,$sshKeygen)) { if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { throw 'Pinned OpenSSH client or operator SSH material is unavailable.' } }
    foreach ($p in @($installRoot,$keyRoot,$publicRoot)) {
        if (-not (Test-Path -LiteralPath $p -PathType Container) -or
            ((Get-Item -LiteralPath $p -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw ('Protected operator SSH directory is missing or a reparse point: ' + $p)
        }
    }
    foreach ($p in @($key,$known,$policyPath)) {
        if (((Get-Item -LiteralPath $p -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw ('Operator SSH input is a reparse point: ' + $p)
        }
    }
    $policyBytes = [IO.File]::ReadAllBytes($policyPath)
    if ((Get-Sha256Hex $policyBytes) -cne $script:HostAccessPolicySha256) { throw 'Protected host access policy hash differs from the controller pin.' }
    Import-Module -Name $policyPath -Force -ErrorAction Stop
    if (-not (Test-TBoundProtectedPathSecurity -Acl (Get-Acl -LiteralPath $installRoot) -Directory $true -OperatorSid $script:OperatorSid -OperatorAccess Read) -or
        -not (Test-TBoundOperatorKeyDirectorySecurity -Acl (Get-Acl -LiteralPath $keyRoot) -OperatorSid $script:OperatorSid) -or
        -not (Test-TBoundProtectedPathSecurity -Acl (Get-Acl -LiteralPath $publicRoot) -Directory $true -OperatorSid $script:OperatorSid -OperatorAccess Modify) -or
        -not (Test-TBoundOperatorPrivateKeySecurity -Acl (Get-Acl -LiteralPath $key) -OperatorSid $script:OperatorSid) -or
        -not (Test-TBoundProtectedPathSecurity -Acl (Get-Acl -LiteralPath $known) -Directory $false -OperatorSid $script:OperatorSid -OperatorAccess Read) -or
        -not (Test-TBoundProtectedPathSecurity -Acl (Get-Acl -LiteralPath $policyPath) -Directory $false -OperatorSid $script:OperatorSid -OperatorAccess Read)) {
        throw 'Operator SSH or protected policy ACLs differ from the exact installed SID policy.'
    }
    $entries=@(& $sshKeygen -F $script:HostAlias -f $known 2>$null)
    if ($LASTEXITCODE -ne 0) { throw 'The pinned host alias is absent from the dedicated known_hosts file.' }
    $entries=@($entries | Where-Object { $_ -and -not $_.StartsWith('#') })
    if ($entries.Count -ne 1 -or $entries[0] -cnotmatch ('^' + [regex]::Escape($script:HostAlias) + '\s+ssh-ed25519\s+\S+(?:\s+.*)?$')) { throw 'The exact host alias must resolve to one ED25519 public host key.' }
    $temporaryHostKey=Join-Path ([IO.Path]::GetTempPath()) ('tbound-knownhost-' + [guid]::NewGuid().ToString('N') + '.txt')
    try {
        [IO.File]::WriteAllLines($temporaryHostKey,$entries,[Text.UTF8Encoding]::new($false))
        $fingerprintLines=@(& $sshKeygen -lf $temporaryHostKey 2>$null)
        if ($LASTEXITCODE -ne 0 -or -not (Test-TBoundPinnedEd25519Fingerprint -Lines $fingerprintLines -ExpectedFingerprint $script:HostFingerprint)) { throw 'The exact host alias does not have the pinned ED25519 fingerprint.' }
    } finally { Remove-Item -LiteralPath $temporaryHostKey -Force -ErrorAction SilentlyContinue }
    return [pscustomobject]@{ Ssh=$ssh; Scp=$scp; Key=$key; Known=$known }
}
function Get-TBoundHermeticSshOptions {
    param($Context)
    return @('-4','-F','NUL','-i',$Context.Key,'-o','IdentityAgent=none','-o','UserKnownHostsFile=' + $Context.Known,'-o','GlobalKnownHostsFile=NUL',
        '-o','HostKeyAlias=' + $script:HostAlias,'-o','StrictHostKeyChecking=yes','-o','HostKeyAlgorithms=ssh-ed25519','-o','KexAlgorithms=curve25519-sha256',
        '-o','ForwardAgent=no','-o','ClearAllForwardings=yes','-o','IdentitiesOnly=yes','-o','RequestTTY=no','-o','UpdateHostKeys=no','-o','BatchMode=yes',
        '-o','PasswordAuthentication=no','-o','KbdInteractiveAuthentication=no','-o','ConnectTimeout=5','-o','ConnectionAttempts=1','-o','ProxyCommand=none','-o','ProxyJump=none')
}
function Invoke-TBoundBoundedProcess {
    param([string]$File,[string[]]$Arguments,[int]$MaximumBytes=8388608,[int]$TimeoutSeconds=60)
    $result=[TBoundBoundedProcessRunner]::Run($File,$Arguments,$MaximumBytes,$TimeoutSeconds*1000)
    if ($result.OutputLimitExceeded) { throw 'Native command output exceeded the fixed byte limit.' }
    if ($result.TimedOut) { throw 'Native command exceeded its fixed time limit.' }
    $stdout=[Text.Encoding]::UTF8.GetString($result.Stdout)
    $lines=@($stdout -split "`n" | ForEach-Object { ([string]$_).TrimEnd([char]13) })
    while ($lines.Count -gt 0 -and $lines[$lines.Count-1] -eq '') {
        if ($lines.Count -eq 1) { $lines=@(); break }
        $lines=@($lines[0..($lines.Count-2)])
    }
    return [pscustomobject]@{ ExitCode=$result.ExitCode; Lines=$lines }
}
function Invoke-TBoundSsh {
    param($Context,[string]$Address,[string]$RemoteCommand,[switch]$AllowFailure)
    if ($Address -notmatch '^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$') { throw 'Refusing a non-IPv4 SSH destination.' }
    $opts = @(Get-TBoundHermeticSshOptions $Context)
    $target = $script:SshUser + '@' + $Address
    $process=Invoke-TBoundBoundedProcess $Context.Ssh (@($opts)+@($target,$RemoteCommand))
    $code = $process.ExitCode
    if ($code -ne 0 -and -not $AllowFailure) { throw 'Pinned SSH command failed; attention is required.' }
    return $process
}
function Get-TBoundNeighborAddress {
    param([string]$MacAddress)
    if ($MacAddress -cnotmatch '^(?:[0-9A-F]{2}:){5}[0-9A-F]{2}$') { throw 'Protected receipt lacks a canonical adapter MAC.' }
    $interface = Get-NetAdapter -Name 'vEthernet (Default Switch)' -ErrorAction Stop
    $rows = @(Get-NetNeighbor -InterfaceIndex $interface.ifIndex -AddressFamily IPv4 -ErrorAction Stop |
        Where-Object { ([string]$_.LinkLayerAddress).Replace('-',':').ToUpperInvariant() -ceq $MacAddress -and $_.State -notin @('Incomplete','Unreachable') })
    $addresses = @($rows | ForEach-Object { [string]$_.IPAddress } | Sort-Object -Unique)
    if ($addresses.Count -ne 1) { throw 'No unique IPv4 neighbor matches the protected VM adapter MAC on Default Switch.' }
    $parsed = $null
    if (-not [Net.IPAddress]::TryParse($addresses[0],[ref]$parsed) -or $parsed.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork) { throw 'MAC-matched neighbor was not IPv4.' }
    return $addresses[0]
}
function Read-TBoundReceiptSnapshot {
    param([string]$Leaf)
    $path = Join-Path $script:ReceiptRoot ($Leaf + '.json')
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return [pscustomobject]@{ Hash=''; InvocationId='' } }
    $raw = [IO.File]::ReadAllText($path)
    $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToUpperInvariant()
    $id = ''
    try { $old = $raw | ConvertFrom-Json; $id = [string]$old.invocationId } catch { }
    return [pscustomobject]@{ Hash=$hash; InvocationId=$id }
}
function Invoke-TBoundHostTask {
    param([ValidateSet('Inspect','Disconnect','StopOffline','ConnectOff','StartTrustedMaintenance')][string]$Leaf,
        [string]$VmId,[string]$AdapterId,[string]$MacAddress,[string]$ProfileSha256,[int]$TimeoutSeconds=120)
    $taskPath = '\TBound\'; $fullName = $taskPath + $Leaf
    $task = Get-ScheduledTask -TaskPath $taskPath -TaskName $Leaf -ErrorAction Stop
    if ($task.State -ne 'Ready') { throw ('Fixed host task is not Ready before trigger: ' + $Leaf) }
    $beforeRun = (Get-ScheduledTaskInfo -TaskPath $taskPath -TaskName $Leaf -ErrorAction Stop).LastRunTime
    $snapshot = Read-TBoundReceiptSnapshot $Leaf
    $receiptPath = Join-Path $script:ReceiptRoot ($Leaf + '.json')
    $trigger = @(& (Join-Path $env:WINDIR 'System32\schtasks.exe') /Run /TN $fullName 2>$null)
    if ($LASTEXITCODE -ne 0) { throw ('Could not trigger fixed host task: ' + $Leaf) }
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        Start-Sleep -Milliseconds 500
        $task = Get-ScheduledTask -TaskPath $taskPath -TaskName $Leaf -ErrorAction Stop
        $info = Get-ScheduledTaskInfo -TaskPath $taskPath -TaskName $Leaf -ErrorAction Stop
        if ($task.State -eq 'Ready' -and $info.LastRunTime -gt $beforeRun) { break }
    } while ([DateTimeOffset]::UtcNow -lt $deadline)
    if ($task.State -ne 'Ready' -or $info.LastRunTime -le $beforeRun) { throw ('Fixed host task did not complete freshly: ' + $Leaf) }
    if (-not (Test-Path -LiteralPath $receiptPath -PathType Leaf)) { throw ('Fresh protected receipt missing: ' + $Leaf) }
    $newHash = (Get-FileHash -LiteralPath $receiptPath -Algorithm SHA256).Hash.ToUpperInvariant()
    if ($newHash -ceq $snapshot.Hash) { throw ('Protected receipt was not replaced: ' + $Leaf) }
    $receipt = [IO.File]::ReadAllText($receiptPath) | ConvertFrom-Json
    $failure = Get-TBoundReceiptPolicyFailure $receipt $Leaf $VmId $AdapterId $MacAddress $ProfileSha256 $snapshot.InvocationId
    if ($failure) { throw ('Fresh host receipt rejected for ' + $Leaf + ': ' + $failure) }
    $start = [DateTimeOffset]::Parse([string]$receipt.startedUtc,[Globalization.CultureInfo]::InvariantCulture)
    if ($start.LocalDateTime -lt $beforeRun.AddSeconds(-2)) { throw ('Receipt predates scheduled task run: ' + $Leaf) }
    return $receipt
}
function New-TBoundQueueMetadataCommand {
    param([string]$RunPath)
    $safe=ConvertTo-TBoundSafeRemotePath $RunPath
    $quoted="'" + $safe + "'"
    return 'set -o pipefail; d=' + $quoted + '; test -d "$d" && test ! -L "$d" && test -f "$d/launcher.pid" && test ! -L "$d/launcher.pid" && test -f "$d/location" && test ! -L "$d/location" && test -f "$d/status" && test ! -L "$d/status" || exit 4; printf ''PID:''; head -c 12 -- "$d/launcher.pid" | base64 -w0; printf ''\nPATH:''; head -c 129 -- "$d/location" | base64 -w0; printf ''\nSTATUS:''; head -c 64 -- "$d/status" | base64 -w0; printf ''\nSTAT:%s\n'' "$(stat -c ''%u %a'' -- "$d")"; printf ''UID:%s\n'' "$(id -u)"'
}
function ConvertFrom-TBoundBoundedText {
    param([string]$Encoded,[int]$MaximumBytes)
    if ($MaximumBytes -lt 0 -or $Encoded.Length -gt (4 * [Math]::Ceiling($MaximumBytes / 3.0))) { throw 'Guest response exceeded its fixed encoded byte limit.' }
    try { $bytes=[Convert]::FromBase64String($Encoded) } catch { throw 'Bounded guest response was not valid base64.' }
    if ($bytes.Length -gt $MaximumBytes) { throw 'Guest response exceeded its fixed byte limit.' }
    return [Text.Encoding]::UTF8.GetString($bytes)
}
function New-TBoundProcInspectionCommand {
    param([string]$PidText)
    if ($PidText -cnotmatch '^[1-9][0-9]{0,9}$') { throw 'Rejected non-decimal launcher PID before command construction.' }
    $value=0L
    if (-not [long]::TryParse($PidText,[ref]$value) -or $value -gt 2147483647) { throw 'Rejected out-of-range launcher PID before command construction.' }
    return 'set -o pipefail; { awk ''/^Uid:/{print $2}'' /proc/' + $PidText + '/status; readlink -f /proc/' + $PidText + '/cwd; tr ''\0'' ''\n'' < /proc/' + $PidText + '/cmdline; } | head -c 8193 | base64 -w0'
}
function New-TBoundQueueCaptureCommand {
    param([string]$Session)
    if ($Session -cnotmatch '^tbv_[0-9a-f]{32}$') { throw 'Rejected invalid controller tmux session name.' }
    return 'set -o pipefail; tmux capture-pane -p -t ' + $Session + ' -S -400 | head -c 65537 | base64 -w0'
}
function Test-TBoundGuestQueue {
    param($Context,[string]$Address,[string]$Session,[string]$StagePath,[DateTimeOffset]$Deadline)
    if ($Session -cnotmatch '^tbv_[0-9a-f]{32}$') { throw 'Rejected invalid controller tmux session name.' }
    $capture=New-TBoundQueueCaptureCommand $Session
    do {
        $result=Invoke-TBoundSsh $Context $Address $capture -AllowFailure
        if ($result.ExitCode -eq 0 -and $result.Lines.Count -eq 1) {
            $captureText=ConvertFrom-TBoundBoundedText ([string]$result.Lines[0]) 65536
            $captureLines=@($captureText.Split([char]10) | ForEach-Object { ([string]$_).TrimEnd([char]13) })
            $pathLines=@($captureLines | Where-Object { $_ -cmatch '^TBOUND_RUN_PATH=(/home/tboundadmin/\.tbound-offline-queued\.[A-Za-z0-9]{10})$' })
            if ($pathLines.Count -eq 1) {
                $runPath=ConvertTo-TBoundSafeRemotePath ($pathLines[0].Substring('TBOUND_RUN_PATH='.Length))
                $proof=Invoke-TBoundSsh $Context $Address (New-TBoundQueueMetadataCommand $runPath) -AllowFailure
                if ($proof.ExitCode -eq 0 -and $proof.Lines.Count -eq 5 -and
                    $proof.Lines[0] -match '^PID:([A-Za-z0-9+/=]+)$' -and $proof.Lines[1] -match '^PATH:([A-Za-z0-9+/=]+)$' -and
                    $proof.Lines[2] -match '^STATUS:([A-Za-z0-9+/=]+)$' -and $proof.Lines[3] -match '^STAT:([0-9]{1,8}) 700$' -and $proof.Lines[4] -match '^UID:([0-9]{1,8})$') {
                    $pidText=(ConvertFrom-TBoundBoundedText ([regex]::Match([string]$proof.Lines[0],'^PID:([A-Za-z0-9+/=]+)$').Groups[1].Value) 11).TrimEnd([char[]]@(13,10))
                    $recorded=(ConvertFrom-TBoundBoundedText ([regex]::Match([string]$proof.Lines[1],'^PATH:([A-Za-z0-9+/=]+)$').Groups[1].Value) 128).TrimEnd([char[]]@(13,10))
                    $status=(ConvertFrom-TBoundBoundedText ([regex]::Match([string]$proof.Lines[2],'^STATUS:([A-Za-z0-9+/=]+)$').Groups[1].Value) 63).TrimEnd([char[]]@(13,10))
                    $dirOwner=[regex]::Match([string]$proof.Lines[3],'^STAT:([0-9]{1,8}) 700$').Groups[1].Value
                    $expectedUid=[regex]::Match([string]$proof.Lines[4],'^UID:([0-9]{1,8})$').Groups[1].Value
                    if ($pidText -cmatch '^[1-9][0-9]{0,9}$') {
                        $process=Invoke-TBoundSsh $Context $Address (New-TBoundProcInspectionCommand $pidText) -AllowFailure
                        if ($process.ExitCode -eq 0 -and $process.Lines.Count -eq 1) {
                            $processText=ConvertFrom-TBoundBoundedText ([string]$process.Lines[0]) 8192
                            $procLines=@($processText.Split([char]10) | ForEach-Object { ([string]$_).TrimEnd([char]13) } | Where-Object { $_ -ne '' })
                            if ($procLines.Count -lt 5) { Start-Sleep -Seconds 2; continue }
                            $processUid=[string]$procLines[0]; $working=[string]$procLines[1]
                            $argv=@($procLines[2..($procLines.Count-1)])
                            $failure=Get-TBoundQueueProofFailure $pidText $runPath $recorded $status $processUid $expectedUid $dirOwner '700' $working $StagePath $argv
                            if (-not $failure) { return [pscustomobject]@{ RunPath=$runPath; Pid=$pidText } }
                        }
                    }
                }
            }
        }
        Start-Sleep -Seconds 2
    } while ([DateTimeOffset]::UtcNow -lt $Deadline)
    throw 'No exact private WAITING launcher PID proof appeared before the bounded queue deadline.'
}
function Read-TBoundRemoteArtifact {
    param($Context,[string]$Address,[string]$RemotePath,[string]$LocalPath)
    if ($RemotePath -cnotmatch '^/home/tboundadmin/\.tbound-offline-queued\.[A-Za-z0-9]{10}/(?:status|runner\.exit-code|launcher\.exit-code|completed|verification\.json|verification\.stderr|launcher\.log|launcher\.pid|location)$') { throw 'Rejected unsafe artifact path.' }
    $quoted="'" + $RemotePath + "'"
    $remote='set -o pipefail; head -c 5242882 -- ' + $quoted + ' | base64 -w0'
    $result=Invoke-TBoundSsh $Context $Address $remote
    if ($result.Lines.Count -gt 1) { throw 'Bounded artifact stream returned unexpected line framing.' }
    $encoded=''
    if ($result.Lines.Count -eq 1) { $encoded=[string]$result.Lines[0] }
    try { $bytes=[Convert]::FromBase64String($encoded) } catch { throw 'Bounded artifact stream was not valid base64.' }
    if ($bytes.Length -gt 5242880) { throw 'Remote artifact exceeded the five MiB transfer limit.' }
    [IO.File]::WriteAllBytes($LocalPath,$bytes)
    $item=Get-Item -LiteralPath $LocalPath -ErrorAction Stop
    return [pscustomobject]@{ Name=[IO.Path]::GetFileName($RemotePath); Length=$item.Length; Sha256=(Get-FileHash -LiteralPath $LocalPath -Algorithm SHA256).Hash.ToUpperInvariant() }
}
function Get-TBoundTerminalArtifacts {
    param($Context,[string]$Address,[string]$RunPath,[string]$ExpectedPid,[string]$OutputDirectory)
    $artifacts = @{}; $records = @()
    foreach ($name in @('status','runner.exit-code','launcher.exit-code','completed','verification.json','verification.stderr','launcher.log','launcher.pid','location')) {
        $local = Join-Path $OutputDirectory $name
        $records += Read-TBoundRemoteArtifact $Context $Address ($RunPath + '/' + $name) $local
        $artifacts[$name] = [IO.File]::ReadAllText($local)
    }
    if ($artifacts['launcher.pid'].Trim() -cne $ExpectedPid -or $artifacts['location'].Trim() -cne $RunPath) { throw 'Retrieved run markers do not match the exact queued launcher.' }
    $report = $null
    try { $report = $artifacts['verification.json'] | ConvertFrom-Json -ErrorAction Stop } catch { }
    $records | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'artifact-manifest.json') -Encoding UTF8
    return [pscustomobject]@{ Artifacts=$artifacts; Report=$report; Manifest=$records }
}
function Invoke-TBoundTrustedOfflineVerifier {
    if ($RunBudgetMinutes -lt 5 -or $RunBudgetMinutes -gt 180) { throw 'RunBudgetMinutes must be between 5 and 180.' }
    $repo=(Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
    $git=(Get-Command git.exe -ErrorAction Stop).Source
    $gitArgs=@('-c','safe.directory=' + $repo,'-C',$repo)
    $sourceCommit=$script:ReviewedSourceCommit
    $null=@(& $git @gitArgs cat-file -e ($sourceCommit + '^{commit}') 2>$null)
    if ($LASTEXITCODE -ne 0) { throw 'The reviewed verifier source commit is unavailable.' }
    $launcherPath=Join-Path $repo 'infra\guest\run-offline-queued.sh'
    $verifierPath=Join-Path $repo 'infra\guest\verify-offline.py'
    if ((Get-FileHash -LiteralPath $launcherPath -Algorithm SHA256).Hash -cne $script:ReviewedLauncherSha256 -or
        (Get-FileHash -LiteralPath $verifierPath -Algorithm SHA256).Hash -cne $script:ReviewedVerifierSha256) { throw 'Reviewed launcher or native verifier hash mismatch.' }
    $null=@(& $git @gitArgs diff --quiet $sourceCommit -- adapter supervisor infra/guest 2>$null)
    if ($LASTEXITCODE -ne 0) { throw 'Verifier source differs from the reviewed source commit.' }
    $sourceStatus=@(& $git @gitArgs status --porcelain -- adapter supervisor infra/guest 2>$null)
    if ($LASTEXITCODE -ne 0) { throw 'Could not verify clean verifier source scope.' }
    if ($sourceStatus.Count -ne 0) { throw 'Verifier source scope is not clean at the reviewed commit.' }

    $localRoot=Join-Path $env:LOCALAPPDATA 'TBound\TrustedVerifierRuns'
    New-Item -ItemType Directory -Path $localRoot -Force | Out-Null
    $runId=[guid]::NewGuid().ToString('N')
    $localRun=Join-Path $localRoot $runId
    New-Item -ItemType Directory -Path $localRun | Out-Null
    $lockPath=Join-Path $localRoot 'controller.lock'
    $lock=$null; $hostReceipts=@(); $allowSafeDisconnect=$false; $queueStarted=$false; $disconnectRequested=$false
    $vmId=''; $adapterId=''; $mac=''; $profileHash=''
    try {
        try { $lock=[IO.File]::Open($lockPath,[IO.FileMode]::CreateNew,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None) }
        catch { throw 'Another trusted verifier controller run is active.' }
        $allowSafeDisconnect=$true
        $ctx=Get-TBoundSshContext
        $inspect=Invoke-TBoundHostTask 'Inspect' '' '' '' ''
        $vmId=[string]$inspect.vmId; $adapterId=[string]$inspect.adapterId; $mac=[string]$inspect.adapterMacAddress; $profileHash=[string]$inspect.profileSha256
        if ($vmId -notmatch '^[0-9a-fA-F-]{36}$' -or $adapterId -notmatch '^[0-9a-fA-F-]{36}$' -or
            $mac -cnotmatch '^(?:[0-9A-F]{2}:){5}[0-9A-F]{2}$' -or $profileHash -cnotmatch '^[0-9A-F]{64}$') { throw 'Protected Inspect receipt lacks pinned VM, adapter, MAC, or profile identity.' }
        $hostReceipts+=$inspect
        if ([string]$inspect.finalVmState -cne 'Running' -or [string]$inspect.finalSwitchName -cne $script:ExpectedSwitch) { throw 'The fixed VM is not Running on Default Switch at start.' }
        $seed=$InitialGuestIPv4; $parsedSeed=$null
        if (-not [Net.IPAddress]::TryParse($seed,[ref]$parsedSeed) -or $parsedSeed.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork) { throw 'InitialGuestIPv4 must be the currently verified IPv4 address.' }
        $probe=Invoke-TBoundSsh $ctx $seed 'true' -AllowFailure
        if ($probe.ExitCode -ne 0) { throw 'Initial verified SSH address or pinned host key is unavailable.' }

        $archive=Join-Path $localRun 'trusted-source.tar.gz'
        $null=@(& $git @gitArgs archive --format=tar.gz --output=$archive $sourceCommit -- adapter supervisor infra/guest 2>$null)
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $archive -PathType Leaf)) { throw 'Could not create the reviewed tracked verifier source archive.' }
        $archiveHash=(Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToUpperInvariant()
        $stage='/home/tboundadmin/.tbound-trusted-' + $runId
        $remoteArchive=$stage + '.tar.gz'
        $scpOpts=@(Get-TBoundHermeticSshOptions $ctx)
        $scpResult=Invoke-TBoundBoundedProcess $ctx.Scp (@($scpOpts)+@($archive,($script:SshUser + '@' + $seed + ':' + $remoteArchive)))
        if ($scpResult.ExitCode -ne 0) { throw 'Could not stage reviewed source through pinned SSH.' }
        $archiveHashResult=Invoke-TBoundSsh $ctx $seed ("sha256sum '$remoteArchive'")
        if ($archiveHashResult.Lines.Count -ne 1 -or $archiveHashResult.Lines[0] -cne ($archiveHash.ToLowerInvariant() + '  ' + $remoteArchive)) { throw 'Transferred source archive hash failed read-back verification.' }
        $stageCommand="mkdir -m 700 -- '$stage' && tar -xzf '$remoteArchive' -C '$stage' && rm -f -- '$remoteArchive'"
        $null=Invoke-TBoundSsh $ctx $seed $stageCommand
        $launcher=$stage + '/infra/guest/run-offline-queued.sh'
        $hashCheck=Invoke-TBoundSsh $ctx $seed ("sha256sum '$launcher'; sha256sum '$stage/infra/guest/verify-offline.py'")
        $expectedLauncher=$script:ArchivedLauncherSha256.ToLowerInvariant() + '  ' + $launcher
        $expectedVerifier=$script:ReviewedVerifierSha256.ToLowerInvariant() + '  ' + $stage + '/infra/guest/verify-offline.py'
        if ($hashCheck.Lines.Count -ne 2 -or $hashCheck.Lines[0] -cne $expectedLauncher -or $hashCheck.Lines[1] -cne $expectedVerifier) { throw 'Staged launcher or native verifier hash failed read-back pin.' }

        $session='tbv_' + $runId
        $inner='cd ' + "'" + $stage + "'" + ' && exec /bin/bash ' + "'" + $launcher + "'" + ' --host-adapter-disconnected'
        $startCommand='tmux new-session -d -s ' + $session + ' "' + $inner + '"'
        $queueStarted=$true
        $null=Invoke-TBoundSsh $ctx $seed $startCommand
        $queue=Test-TBoundGuestQueue $ctx $seed $session $stage ([DateTimeOffset]::UtcNow.AddMinutes(3))

        $disconnectRequested=$true
        $disconnected=Invoke-TBoundHostTask 'Disconnect' $vmId $adapterId $mac $profileHash
        $hostReceipts+=$disconnected
        $deadline=[DateTimeOffset]::UtcNow.AddMinutes($RunBudgetMinutes)
        while ([DateTimeOffset]::UtcNow -lt $deadline) { Start-Sleep -Seconds 5 }

        $stopped=Invoke-TBoundHostTask 'StopOffline' $vmId $adapterId $mac $profileHash
        $hostReceipts+=$stopped
        $inspectOff=Invoke-TBoundHostTask 'Inspect' $vmId $adapterId $mac $profileHash
        $hostReceipts+=$inspectOff
        $gate=Get-TBoundReconnectGateFailure $stopped $inspectOff $vmId $adapterId $mac $profileHash $null
        if ($gate) { throw ('Reconnect gate rejected: ' + $gate) }
        $connected=Invoke-TBoundHostTask 'ConnectOff' $vmId $adapterId $mac $profileHash
        $hostReceipts+=$connected
        $started=Invoke-TBoundHostTask 'StartTrustedMaintenance' $vmId $adapterId $mac $profileHash
        $hostReceipts+=$started

        $addressDeadline=[DateTimeOffset]::UtcNow.AddMinutes(5); $maintenanceAddress=$null
        do {
            try { $maintenanceAddress=Get-TBoundNeighborAddress $mac } catch { }
            if ($maintenanceAddress) {
                $probe=Invoke-TBoundSsh $ctx $maintenanceAddress 'true' -AllowFailure
                if ($probe.ExitCode -eq 0) { break }
                $maintenanceAddress=$null
            }
            Start-Sleep -Seconds 3
        } while ([DateTimeOffset]::UtcNow -lt $addressDeadline)
        if (-not $maintenanceAddress) { throw 'No MAC-matched pinned SSH endpoint appeared after trusted maintenance boot.' }
        $manifest=Get-TBoundTerminalArtifacts $ctx $maintenanceAddress $queue.RunPath $queue.Pid $localRun
        $testResult=Get-TBoundTestResult $false $manifest.Artifacts $manifest.Report
        $hostReceipts | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $localRun 'host-receipts.json') -Encoding UTF8
        [pscustomobject]@{ RunId=$runId; TestResult=$testResult; InitialAddress=$seed; MaintenanceAddress=$maintenanceAddress; RunPath=$queue.RunPath; Pid=$queue.Pid; SourceArchiveSha256=$archiveHash; Artifacts=$manifest.Manifest; HostReceipts=$hostReceipts; OutputDirectory=$localRun } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $localRun 'result.json') -Encoding UTF8
        Write-Output ('TestResult=' + $testResult)
        Write-Output ('RunId=' + $runId)
        Write-Output ('Evidence=' + $localRun)
    }
    catch {
        $primaryFailure=$_.Exception.Message
        $cleanupResult='NOT_ATTEMPTED'
        if (Test-TBoundCleanupDisconnectRequired $allowSafeDisconnect $true $queueStarted $disconnectRequested) {
            try {
                $cleanupReceipt=Invoke-TBoundHostTask 'Disconnect' $vmId $adapterId $mac $profileHash
                $hostReceipts+=$cleanupReceipt
                $cleanupResult='PASS'
            } catch { $cleanupResult='UNKNOWN' }
        }
        $hostReceipts | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $localRun 'host-receipts.json') -Encoding UTF8
        [pscustomobject]@{ RunId=$runId; TestResult='ABORTED_UNKNOWN'; Stage='attention-required'; Reason=$primaryFailure; CleanupDisconnect=$cleanupResult; HostReceipts=$hostReceipts; OutputDirectory=$localRun } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $localRun 'result.json') -Encoding UTF8
        Write-Error ('TestResult=ABORTED_UNKNOWN; attention required: ' + $primaryFailure + '; safe disconnect=' + $cleanupResult)
        throw
    }
    finally {
        if ($lock) { $lock.Dispose(); Remove-Item -LiteralPath $lockPath -Force -ErrorAction SilentlyContinue }
        # Deliberately no reconnect or recovery here. Only the fixed Off-gated path may reconnect.
    }
}
if (-not $PolicyOnly) { Invoke-TBoundTrustedOfflineVerifier }
