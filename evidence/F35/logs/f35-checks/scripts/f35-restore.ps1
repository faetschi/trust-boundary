$ErrorActionPreference = 'Stop'
$dir = 'F:\trust-boundary\.local\f35-freeze'
$out = Join-Path $dir 'f35-restore.out'
$m = Join-Path $dir 'markers'
$vm = 'TBound-Ubuntu-2404-ABI8'
$keep = 'pre-F35-restore-2026-11-26'
$src = 'pre-F31-2026-11-20'
$pre34 = 'pre-F35-34-2026-11-26'
function L($t) { "$((Get-Date).ToUniversalTime().ToString('o')) $t" | Add-Content $out }
function New-TbCheckpoint($name) { $orig = (Get-VM -Name $vm).CheckpointType; Set-VM -Name $vm -CheckpointType Standard; try { Checkpoint-VM -Name $vm -SnapshotName $name } finally { Set-VM -Name $vm -CheckpointType $orig } }
function Wait-Marker($names, $minutes) {
  $end = (Get-Date).AddMinutes($minutes)
  while ((Get-Date) -lt $end) {
    foreach ($n in $names) { if (Test-Path (Join-Path $m $n)) { return $n } }
    Start-Sleep -Seconds 10
  }
  return 'timeout'
}
try {
  "" | Out-File $out -Encoding utf8
  New-Item -ItemType Directory -Path $m -Force | Out-Null
  $v = Get-VM -Name $vm
  $mac = (Get-VMNetworkAdapter -VMName $vm).MacAddress -join ','
  L "start state=$($v.State) mac=$mac"
  if ($mac -ne '00155D0C4002') { throw "MAC $mac is not the reference guest; nothing done" }
  if (-not (Get-VMSnapshot -VMName $vm -Name $src -ErrorAction SilentlyContinue)) { throw "checkpoint $src not found; nothing done" }
  foreach ($n in @($keep, $pre34)) { if (Get-VMSnapshot -VMName $vm -Name $n -ErrorAction SilentlyContinue) { throw "checkpoint $n already exists; nothing done" } }
  New-TbCheckpoint $keep
  L "checkpoint $keep created (F34 research-pilot state: research config, openrouter.key, genesis ledger bea5ce06, service stopped)"
  if ((Get-VM -Name $vm).State -ne 'Off') { Stop-VM -Name $vm -TurnOff -Force }
  Restore-VMSnapshot -VMName $vm -Name $src -Confirm:$false
  L "restored $src state=$((Get-VM -Name $vm).State)"
  Start-VM -Name $vm
  L "Start-VM returned state=$((Get-VM -Name $vm).State)"
  New-Item -ItemType File -Path (Join-Path $m 'restored-pre-F31') -Force | Out-Null
  L "waiting up to 360 min for markers pre34 or abort"
  $r = Wait-Marker @('pre34', 'abort') 360
  if ($r -ne 'pre34') { L "no pre34 marker ($r): no pre-34 checkpoint taken"; throw "stopped before 34 ($r)" }
  New-TbCheckpoint $pre34
  L "checkpoint $pre34 created (right before 34)"
  New-Item -ItemType File -Path (Join-Path $m 'pre34-done') -Force | Out-Null
  L "waiting up to 180 min for marker post34 (34 and 40 v-stub ended, pass or fail)"
  $r = Wait-Marker @('post34') 180
  L "marker: $r; restoring $pre34 in every case"
  if ((Get-VM -Name $vm).State -ne 'Off') { Stop-VM -Name $vm -TurnOff -Force }
  Restore-VMSnapshot -VMName $vm -Name $pre34 -Confirm:$false
  L "restored $pre34 state=$((Get-VM -Name $vm).State)"
  Start-VM -Name $vm
  L "Start-VM returned state=$((Get-VM -Name $vm).State)"
  New-Item -ItemType File -Path (Join-Path $m 'post34-restored') -Force | Out-Null
} catch { L "ERROR: $($_.Exception.Message)" }
Get-VMSnapshot -VMName $vm | Select-Object Name, CreationTime | Format-Table -AutoSize | Out-String | Add-Content $out
L "DONE"
New-Item -ItemType File -Path (Join-Path $m 'ps-done') -Force | Out-Null
