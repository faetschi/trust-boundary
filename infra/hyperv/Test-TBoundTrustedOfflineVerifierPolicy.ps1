[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$controller=Join-Path $PSScriptRoot 'Invoke-TBoundTrustedOfflineVerifier.ps1'
. $controller -PolicyOnly

function Assert-Policy {
    param([bool]$Condition,[string]$Name)
    if (-not $Condition) { throw ('Policy test failed: ' + $Name) }
}
function New-Receipt {
    param([string]$Operation,[string]$InitialState,[string]$InitialSwitch,[string]$FinalState,[string]$FinalSwitch,[DateTimeOffset]$Now)
    return [pscustomobject]@{
        schema=1; task=('\TBound\' + $Operation); operation=$Operation; invocationId=[guid]::NewGuid().ToString()
        startedUtc=$Now.AddSeconds(-10).ToString('o'); completedUtc=$Now.AddSeconds(-5).ToString('o'); result='PASS'
        vmId='11111111-2222-3333-4444-555555555555'; adapterId='aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'
        adapterMacAddress='00:15:5D:01:02:03'; initialVmState=$InitialState; finalVmState=$FinalState
        initialSwitchName=$InitialSwitch; finalSwitchName=$FinalSwitch; message=''; profileSha256=('A' * 64)
    }
}
$now=[DateTimeOffset]::UtcNow
$vm='11111111-2222-3333-4444-555555555555'; $nic='aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'; $mac='00:15:5D:01:02:03'; $profile=('A' * 64)
$off=New-Receipt 'StopOffline' 'Running' '' 'Off' '' $now
$offObserved=New-Receipt 'Inspect' 'Off' '' 'Off' '' $now
$runningObserved=New-Receipt 'Inspect' 'Running' 'Default Switch' 'Running' 'Default Switch' $now

Assert-Policy (-not (Get-TBoundReceiptPolicyFailure $off 'StopOffline' $vm $nic $mac $profile '' $now)) 'fresh StopOffline receipt'
$cross=New-Receipt 'ConnectOff' 'Off' '' 'Off' 'Default Switch' $now
Assert-Policy ((Get-TBoundReceiptPolicyFailure $cross 'StopOffline' $vm $nic $mac $profile '' $now) -eq 'cross-operation receipt') 'cross-operation receipt is rejected'
Assert-Policy ((Get-TBoundReceiptPolicyFailure $off 'StopOffline' $vm $nic $mac $profile $off.invocationId $now) -eq 'stale invocation id') 'reused invocation id is rejected'
$old=New-Receipt 'StopOffline' 'Running' '' 'Off' '' $now.AddMinutes(-30)
Assert-Policy ((Get-TBoundReceiptPolicyFailure $old 'StopOffline' $vm $nic $mac $profile '' $now) -eq 'stale or future receipt') 'stale receipt is rejected'

Assert-Policy (-not (Get-TBoundReconnectGateFailure $off $offObserved $vm $nic $mac $profile '')) 'fresh host Off plus disconnected permits reconnect'
Assert-Policy ((Get-TBoundReconnectGateFailure $off $runningObserved $vm $nic $mac $profile '') -ne $null) 'running host state blocks reconnect'
$guestPass='PASS'
Assert-Policy ((Get-TBoundReconnectGateFailure $null $offObserved $vm $nic $mac $profile '') -ne $null) 'guest PASS cannot replace a host Off receipt'
$cleanupOff=New-Receipt 'Disconnect' 'Off' '' 'Off' '' $now
Assert-Policy (-not (Get-TBoundReceiptPolicyFailure $cleanupOff 'Disconnect' $vm $nic $mac $profile '' $now)) 'cleanup Disconnect is idempotent while Off/disconnected'
$wrongSwitch=New-Receipt 'Disconnect' 'Running' 'Other Switch' 'Running' '' $now
Assert-Policy ((Get-TBoundReceiptPolicyFailure $wrongSwitch 'Disconnect' $vm $nic $mac $profile '' $now) -ne $null) 'cleanup refuses an unexpected initial switch'
Assert-Policy (Test-TBoundCleanupDisconnectRequired $true $true $true $true) 'failed first Disconnect retries cleanup after queue start'

$stage='/home/tboundadmin/.tbound-trusted-0123456789abcdef0123456789abcdef'
$run='/home/tboundadmin/.tbound-offline-queued.AbC123xYz0'
$argv=@('bash',($stage + '/infra/guest/run-offline-queued.sh'),'--host-adapter-disconnected')
Assert-Policy (-not (Get-TBoundQueueProofFailure '4321' $run $run 'WAITING_FOR_OFFLINE' '1000' '1000' '1000' '700' $stage $stage $argv)) 'exact private run path, PID, owner and argv are accepted'
$badPath='/home/tboundadmin/.tbound-offline-queued.aaaa;touch /tmp/pwn'
Assert-Policy ((Get-TBoundQueueProofFailure '4321' $badPath $badPath 'WAITING_FOR_OFFLINE' '1000' '1000' '1000' '700' $stage $stage $argv) -ne $null) 'malicious run path is rejected'
$badPidRejected=$false
try { $null=New-TBoundProcInspectionCommand '123;touch /tmp/pwn' } catch { $badPidRejected=$true }
Assert-Policy $badPidRejected 'malicious PID is rejected before remote command construction'
$pathRejected=$false
try { $null=New-TBoundQueueMetadataCommand $badPath } catch { $pathRejected=$true }
Assert-Policy $pathRejected 'malicious path is rejected before remote command construction'
$processCommand=New-TBoundProcInspectionCommand '4321'
Assert-Policy ($processCommand -match 'head -c 8193.*base64 -w0') 'process UID, cwd and argv proof is capped before SSH output'
$captureCommand=New-TBoundQueueCaptureCommand 'tbv_0123456789abcdef0123456789abcdef'
Assert-Policy ($captureCommand -match 'head -c 65537.*base64 -w0') 'tmux queue proof is capped before SSH output'
$sessionRejected=$false
try { $null=New-TBoundQueueCaptureCommand 'tbv_x;touch /tmp/pwn' } catch { $sessionRejected=$true }
Assert-Policy $sessionRejected 'untrusted tmux session text is rejected before command construction'
$boundedText=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('WAITING'))
Assert-Policy ((ConvertFrom-TBoundBoundedText $boundedText 7) -ceq 'WAITING') 'bounded marker decoder accepts a within-limit response'
$oversizedRejected=$false
try { $null=ConvertFrom-TBoundBoundedText ([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('123456789'))) 8 } catch { $oversizedRejected=$true }
Assert-Policy $oversizedRejected 'bounded marker decoder rejects an oversized response before parsing'
$localProcess=[TBoundBoundedProcessRunner]::Run($env:ComSpec,@('/c','echo bounded-output'),1024,5000)
Assert-Policy (-not $localProcess.OutputLimitExceeded -and -not $localProcess.TimedOut -and [Text.Encoding]::UTF8.GetString($localProcess.Stdout) -match 'bounded-output') 'bounded native runner captures a small local response'
$overflowProcess=[TBoundBoundedProcessRunner]::Run($env:ComSpec,@('/c','for /L %i in (1,1,1000) do @echo 1234567890'),64,5000)
Assert-Policy $overflowProcess.OutputLimitExceeded 'bounded native runner terminates a response that exceeds its cap'

$artifacts=@{
    status='PASS'; 'runner.exit-code'='0'; 'launcher.exit-code'='0'; completed=$now.ToString('o');
    'verification.json'='{}'; 'verification.stderr'=''; 'launcher.log'='queued'; 'launcher.pid'='4321'; location=$run
}
$report=[pscustomobject]@{
    schema='tbound.guest-verification/v2'; status='PASS'; failures=@()
    cleanup=[pscustomobject]@{status='PASS'}
    guest=[pscustomobject]@{offline_prerequisite=[pscustomobject]@{host_adapter_disconnected_attested=$true}}
    claims=[pscustomobject]@{live_provider_exchange=$false;live_effect_execution=$false;containment_established=$false;g1_durability_established=$false}
    checks=[pscustomobject]@{native=[pscustomobject]@{status='PASS'}}
}
Assert-Policy ((Get-TBoundTestResult $false $artifacts $report) -eq 'PASS') 'complete exact-run report can classify PASS'
Assert-Policy ((Get-TBoundTestResult $true $artifacts $report) -eq 'ABORTED_UNKNOWN') 'bounded wait/timeout never classifies PASS'
$artifacts.Remove('completed')
Assert-Policy ((Get-TBoundTestResult $false $artifacts $report) -eq 'ABORTED_UNKNOWN') 'missing terminal artifact remains unknown'

Write-Output 'TBound trusted offline verifier policy tests: PASS'
