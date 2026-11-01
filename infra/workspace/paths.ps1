# Single source of the local (gitignored) TBound root on a Windows host.
# Worktrees are not the primary checkout, so they set TBOUND_LOCAL.
function Get-TBoundLocalRoot {
    if ($env:TBOUND_LOCAL) {
        if (-not [IO.Path]::IsPathRooted($env:TBOUND_LOCAL)) { throw 'TBOUND_LOCAL must be an absolute path.' }
        return $env:TBOUND_LOCAL
    }
    return Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) '.local'
}
