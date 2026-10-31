# Runs one hook the way Codex on Windows does, for the windows-launcher CI job.
# Codex takes the hook's commandWindows line from hooks.json and hands it to the
# session's shell: `powershell.exe -NoProfile -Command <line>`, or `cmd.exe /C
# "<line>"` when it found no shell (codex-rs/hooks/src/engine/command_runner.rs).
# The payload goes in on stdin as UTF-8 and the reply comes back on stdout.
#
#   . .github/codex-hook.ps1
#   $r = Invoke-CodexHook -Root <plugin root> -Event user-prompt-submit -Json $json [-Shell cmd]
#   $r.Code; $r.Out

function Invoke-CodexHook([string]$Root, [string]$Event, [string]$Json, [string]$Shell = 'powershell') {
    $hooks = Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\hooks\hooks.json') -Raw | ConvertFrom-Json
    $names = @{ 'user-prompt-submit' = 'UserPromptSubmit'; 'session-start' = 'SessionStart'; 'session-end' = 'SessionEnd' }
    $line = $hooks.hooks.($names[$Event])[0].hooks[0].commandWindows
    if (-not $line) { throw "hooks.json has no commandWindows for $Event" }
    $line = $line.Replace('${CLAUDE_PLUGIN_ROOT}', $Root)

    $psi = New-Object System.Diagnostics.ProcessStartInfo
    if ($Shell -eq 'cmd') {
        $psi.FileName = "$env:SystemRoot\System32\cmd.exe"
        $psi.Arguments = '/C "' + $line + '"'
    } else {
        $psi.FileName = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
        $psi.Arguments = '-NoProfile -Command "' + $line.Replace('"', '\"') + '"'
    }
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $p = [System.Diagnostics.Process]::Start($psi)
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($Json)
    $p.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
    $p.StandardInput.Close()
    $ms = New-Object System.IO.MemoryStream
    $p.StandardOutput.BaseStream.CopyTo($ms)
    $err = $p.StandardError.ReadToEnd()
    $p.WaitForExit()
    [pscustomobject]@{
        Code = $p.ExitCode
        Out  = [System.Text.Encoding]::UTF8.GetString($ms.ToArray())
        Err  = $err
    }
}
