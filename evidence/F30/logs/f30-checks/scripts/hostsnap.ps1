$out = 'C:\Users\Admin\AppData\Local\Temp\claude\1bdbf0b7223717e2\5885a9d4-6837-49d0-8bb9-191b48098c96\scratchpad\f30w\hostsnap-gf30.log'
for ($i=0; $i -lt 40; $i++) {
  if (Test-Path '\\wsl.localhost\tbound-dev\home\tbound\tbound-work\gf30.out.done') { break }
  "=== $(Get-Date -Format o)" | Out-File -Append -Encoding utf8 $out
  Get-CimInstance Win32_Process | Where-Object { $_.Name -match '^(find|rg|grep|du|tree|robocopy|xcopy|everything|git|wsl|MsMpEng|SearchProtocolHost|defrag|vssvc|TiWorker|MpCmdRun|chkdsk)\.exe$' } | ForEach-Object { "{0} {1} {2} {3}" -f $_.ProcessId,$_.Name,$_.CreationDate,($(if($_.CommandLine){$_.CommandLine.Substring(0,[Math]::Min(150,$_.CommandLine.Length))})) } | Out-File -Append -Encoding utf8 $out
  Get-Counter '\PhysicalDisk(*)\% Disk Time','\Processor(_Total)\% Processor Time' -ErrorAction SilentlyContinue | ForEach-Object { $_.CounterSamples | ForEach-Object { "{0} {1:N1}" -f $_.Path,$_.CookedValue } } | Out-File -Append -Encoding utf8 $out
  Start-Sleep -Seconds 300
}
