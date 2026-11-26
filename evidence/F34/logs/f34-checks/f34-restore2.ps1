$ErrorActionPreference = 'Stop'
$out = 'C:\Users\Admin\AppData\Local\Temp\claude\1bdbf0b7223717e2\300b9e68-97e7-4a01-aa27-ad0df38df07f\scratchpad\f31w\f34-restore2.out'
$vm = 'TBound-Ubuntu-2404-ABI8'
$keep = 'post-F34-chain1-2026-11-24'
$src = 'pre-F31-2026-11-20'
$new = 'post-F34-chain2-2026-11-24'
function L($m) { "$((Get-Date).ToUniversalTime().ToString('o')) $m" | Add-Content $out }
function New-TbCheckpoint($name) { $orig = (Get-VM -Name $vm).CheckpointType; Set-VM -Name $vm -CheckpointType Standard; try { Checkpoint-VM -Name $vm -SnapshotName $name } finally { Set-VM -Name $vm -CheckpointType $orig } }
try {
  "" | Out-File $out -Encoding utf8
  $v = Get-VM -Name $vm
  $mac = (Get-VMNetworkAdapter -VMName $vm).MacAddress -join ','
  L "start state=$($v.State) mac=$mac"
  if ($mac -ne '00155D0C4002') { throw "MAC $mac is not the reference guest; nothing done" }
  if (-not (Get-VMSnapshot -VMName $vm -Name $src -ErrorAction SilentlyContinue)) { throw "checkpoint $src not found; nothing done" }
  foreach ($n in @($new, $keep)) { if (Get-VMSnapshot -VMName $vm -Name $n -ErrorAction SilentlyContinue) { throw "checkpoint $n already exists; nothing done" } }
  New-TbCheckpoint $keep
  L "checkpoint $keep created (F34 chain 1, stopped at the rp model refusal)"
  if ((Get-VM -Name $vm).State -ne 'Off') { Stop-VM -Name $vm -TurnOff -Force }
  Restore-VMSnapshot -VMName $vm -Name $src -Confirm:$false
  L "restored $src state=$((Get-VM -Name $vm).State)"
  Start-VM -Name $vm
  L "Start-VM returned state=$((Get-VM -Name $vm).State)"
  New-Item -ItemType File -Path ($out + '.started') -Force | Out-Null
  L "waiting up to 300 min for the chain marker ($out.chainok or $out.chainfail)"
  $end = (Get-Date).AddMinutes(300)
  while ((Get-Date) -lt $end -and -not (Test-Path ($out + '.chainok')) -and -not (Test-Path ($out + '.chainfail'))) { Start-Sleep -Seconds 10 }
  if (Test-Path ($out + '.chainok')) { New-TbCheckpoint $new; L "checkpoint $new created" } else { L "NO checkpoint ${new}, chain not confirmed" }
  Get-VMSnapshot -VMName $vm | Select-Object Name, CreationTime | Format-Table -AutoSize | Out-String | Add-Content $out
  L "DONE"
} catch { L "ERROR: $($_.Exception.Message)" }
