#requires -RunAsAdministrator
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$taskPath = '\TBound\'
$taskFolderName = 'TBound'
$installRoot = 'C:\ProgramData\TBoundHostAutomation'
$installedAction = Join-Path $installRoot 'TBoundHostActions.ps1'
$expectedPowerShell = [IO.Path]::GetFullPath((Join-Path $PSHOME 'powershell.exe'))
$leaves = @('Inspect', 'Disconnect', 'StopOffline', 'ConnectOff', 'StartTrustedMaintenance')

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-AccountSid {
    param([Parameter(Mandatory = $true)][string]$Account)
    if ($Account -eq 'S-1-5-18') { return $Account }
    if ($Account -eq 'SYSTEM' -or $Account -eq 'NT AUTHORITY\SYSTEM') { return 'S-1-5-18' }
    try {
        return ([Security.Principal.NTAccount]::new($Account)).Translate([Security.Principal.SecurityIdentifier]).Value
    }
    catch {
        throw ('Could not resolve task principal: ' + $Account)
    }
}

if (-not (Test-Administrator)) { throw 'Run this uninstaller from an elevated Windows PowerShell session.' }
if (-not (Test-Path -LiteralPath $expectedPowerShell -PathType Leaf)) { throw ('Expected Windows PowerShell executable is missing: ' + $expectedPowerShell) }

Import-Module ScheduledTasks -ErrorAction Stop
$scheduler = New-Object -ComObject Schedule.Service
$scheduler.Connect()
$rootFolder = $scheduler.GetFolder('\')
$matchingFolders = @($rootFolder.GetFolders(0) | Where-Object { $_.Name -ceq $taskFolderName })
if ($matchingFolders.Count -gt 1) { throw 'Task Scheduler contains an ambiguous \TBound folder.' }
$folderExists = ($matchingFolders.Count -eq 1)
$registered = @()

if ($folderExists) {
    $folderTasks = @(Get-ScheduledTask -TaskPath $taskPath -ErrorAction Stop)
    $unexpected = @($folderTasks | Where-Object { $_.TaskName -notin $leaves })
    if ($unexpected.Count -gt 0) {
        throw ('Unexpected task exists in \TBound; refusing to alter that folder: ' + (($unexpected | ForEach-Object TaskName) -join ', '))
    }

    foreach ($leaf in $leaves) {
        $task = $folderTasks | Where-Object { $_.TaskName -ceq $leaf } | Select-Object -First 1
        if ($null -eq $task) { continue }
        if ([string]$task.State -ceq 'Running') {
            throw ('Task is running; let it finish before uninstalling: ' + $taskPath + $leaf)
        }

        $actions = @($task.Actions)
        if ($actions.Count -ne 1) { throw ('Task action count changed; refusing to unregister: ' + $taskPath + $leaf) }
        $action = $actions[0]
        $expectedArguments = '-NoLogo -NoProfile -NonInteractive -File "' + $installedAction + '" -Task ' + $leaf
        if ([IO.Path]::GetFullPath([string]$action.Execute) -ine $expectedPowerShell -or
            [string]$action.Arguments -cne $expectedArguments -or
            [string]$action.WorkingDirectory -ine $installRoot) {
            throw ('Task action differs from the fixed installed action; refusing to unregister: ' + $taskPath + $leaf)
        }
        if ((Get-AccountSid ([string]$task.Principal.UserId)) -cne 'S-1-5-18' -or
            [string]$task.Principal.LogonType -cne 'ServiceAccount' -or
            [string]$task.Principal.RunLevel -cne 'Highest') {
            throw ('Task principal differs from fixed SYSTEM/highest policy; refusing to unregister: ' + $taskPath + $leaf)
        }
        if (@($task.Triggers).Count -ne 0) {
            throw ('Unexpected trigger exists; refusing to unregister altered task: ' + $taskPath + $leaf)
        }
        if ([string]$task.Settings.MultipleInstances -cne 'IgnoreNew' -or
            [bool]$task.Settings.AllowDemandStart -ne $true) {
            throw ('Task settings differ from the fixed on-demand policy; refusing to unregister: ' + $taskPath + $leaf)
        }
        $registered += $leaf
    }
}

if ($registered.Count -eq 0) {
    if ($folderExists) {
        Write-Host 'No recognized TBound host automation tasks are registered; preserving the existing task folder.'
    }
    else {
        Write-Host 'No TBound host automation tasks are registered.'
    }
    return
}

$target = if ($registered.Count -gt 0) { $taskPath + ($registered -join ', ') } else { '\' + $taskFolderName }
if (-not $PSCmdlet.ShouldProcess($target, 'Unregister only verified fixed TBound tasks and remove the folder only if empty')) { return }

foreach ($leaf in $registered) {
    Unregister-ScheduledTask -TaskPath $taskPath -TaskName $leaf -Confirm:$false -ErrorAction Stop
}

if ($folderExists) {
    $remaining = @(Get-ScheduledTask -TaskPath $taskPath -ErrorAction SilentlyContinue)
    if ($remaining.Count -eq 0) {
        # Task Scheduler folder removal is nonrecursive; protected files and receipts are preserved.
        $rootFolder.DeleteFolder($taskFolderName, 0)
    }
    else {
        Write-Warning 'TBound folder still contains tasks; it was preserved.'
    }
}

Write-Host 'Fixed TBound task registrations removed where verified.'
Write-Host ('Preserved protected action, profile, and receipts under ' + $installRoot + '.')
