$ErrorActionPreference = 'Stop'
$out = 'C:\Users\Admin\AppData\Local\Temp\claude\1bdbf0b7223717e2\300b9e68-97e7-4a01-aa27-ad0df38df07f\scratchpad\f31w\f31-restore.out'
$vm = 'TBound-Ubuntu-2404-ABI8'
$keep = 'post-F30-2026-11-20'
$src = 'pre-F30-2026-11-18'
$new = 'pre-F31-2026-11-20'
function L($m) { "$((Get-Date).ToUniversalTime().ToString('o')) $m" | Add-Content $out }
function New-TbCheckpoint($name) { $orig = (Get-VM -Name $vm).CheckpointType; Set-VM -Name $vm -CheckpointType Standard; try { Checkpoint-VM -Name $vm -SnapshotName $name } finally { Set-VM -Name $vm -CheckpointType $orig } }
try {
  "" | Out-File $out -Encoding utf8
  $v = Get-VM -Name $vm
  $mac = (Get-VMNetworkAdapter -VMName $vm).MacAddress -join ','
  L "start state=$($v.State) mac=$mac checkpointType=$($v.CheckpointType)"
  if ($mac -ne '00155D0C4002') { throw "MAC $mac is not the reference guest 00155D0C4002; nothing done" }
  if (-not (Get-VMSnapshot -VMName $vm -Name $src -ErrorAction SilentlyContinue)) { throw "checkpoint $src not found; nothing done" }
  foreach ($n in @($new, $keep)) { if (Get-VMSnapshot -VMName $vm -Name $n -ErrorAction SilentlyContinue) { throw "checkpoint $n already exists; nothing done" } }
  L "keep the current F30 state as $keep (makes the restore reversible)"
  New-TbCheckpoint $keep
  L "checkpoint $keep created"
  if ((Get-VM -Name $vm).State -ne 'Off') { L "turn off"; Stop-VM -Name $vm -TurnOff -Force }
  L "restore $src"
  Restore-VMSnapshot -VMName $vm -Name $src -Confirm:$false
  L "restored state=$((Get-VM -Name $vm).State)"
  Start-VM -Name $vm
  L "Start-VM returned state=$((Get-VM -Name $vm).State)"
  New-Item -ItemType File -Path ($out + '.started') -Force | Out-Null
  L "waiting up to 45 min for the guest hold ($out.holdok or $out.holdfail)"
  $end = (Get-Date).AddMinutes(45)
  while ((Get-Date) -lt $end -and -not (Test-Path ($out + '.holdok')) -and -not (Test-Path ($out + '.holdfail'))) { Start-Sleep -Seconds 5 }
  if (Test-Path ($out + '.holdok')) {
    New-TbCheckpoint $new
    L "checkpoint $new created (after the hold); checkpointType=$((Get-VM -Name $vm).CheckpointType) state=$((Get-VM -Name $vm).State)"
  } else { L "NO checkpoint ${new}, hold not confirmed" }
  Get-VMSnapshot -VMName $vm | Select-Object Name, CreationTime, SnapshotType | Format-Table -AutoSize | Out-String | Add-Content $out
  L "DONE"
} catch { L "ERROR: $($_.Exception.Message)" }
