$ErrorActionPreference = 'Stop'
$out = 'C:\Users\Admin\AppData\Local\Temp\claude\1bdbf0b7223717e2\5885a9d4-6837-49d0-8bb9-191b48098c96\scratchpad\f30w\f30-restore.out'
$vm = 'TBound-Ubuntu-2404-ABI8'
$src = 'pre-F29-2026-11-16'
$new = 'pre-F30-2026-11-18'
function L($m) { "$((Get-Date).ToUniversalTime().ToString('o')) $m" | Add-Content $out }
try {
  "" | Out-File $out -Encoding utf8
  $v = Get-VM -Name $vm
  $mac = (Get-VMNetworkAdapter -VMName $vm).MacAddress -join ','
  L "start state=$($v.State) mac=$mac checkpointType=$($v.CheckpointType)"
  if ($mac -ne '00155D0C4002') { throw "MAC $mac is not the reference guest 00155D0C4002; nothing done" }
  $cp = Get-VMSnapshot -VMName $vm -Name $src -ErrorAction SilentlyContinue
  if (-not $cp) { throw "checkpoint $src not found; nothing done" }
  if (Get-VMSnapshot -VMName $vm -Name $new -ErrorAction SilentlyContinue) { throw "checkpoint $new already exists; nothing done" }
  if ((Get-VM -Name $vm).State -ne 'Off') { L "turn off (current F29-chain state is discarded by the restore)"; Stop-VM -Name $vm -TurnOff -Force }
  L "restore $src"
  Restore-VMSnapshot -VMName $vm -Name $src -Confirm:$false
  L "restored state=$((Get-VM -Name $vm).State)"
  New-Item -ItemType File -Path ($out + '.go') -Force | Out-Null
  Start-Sleep -Seconds 3
  L "Start-VM begin"
  Start-VM -Name $vm
  L "Start-VM returned state=$((Get-VM -Name $vm).State)"
  Start-Sleep -Seconds 150
  $orig = (Get-VM -Name $vm).CheckpointType
  Set-VM -Name $vm -CheckpointType Standard
  try { Checkpoint-VM -Name $vm -SnapshotName $new } finally { Set-VM -Name $vm -CheckpointType $orig }
  L "checkpoint $new created; checkpointType=$((Get-VM -Name $vm).CheckpointType) state=$((Get-VM -Name $vm).State)"
  Get-VMSnapshot -VMName $vm | Select-Object Name, CreationTime, SnapshotType | Format-Table -AutoSize | Out-String | Add-Content $out
  L "DONE"
} catch { L "ERROR: $($_.Exception.Message)" }
