# Picks the prebuilt Windows binary, downloading it from the release once if missing.
# This is the Windows half of bin/agent-ledger: Codex runs hooks through cmd.exe, which
# cannot run the bash script. Claude Code on Windows uses Git Bash and never comes here.
# Hook calls never fail the session: on any problem they log to skips.log and exit 0.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$here = Split-Path -Parent $PSCommandPath
$root = Split-Path -Parent $here
$repo = 'rzaaeeff/agent-ledger'
$argv = @($args | ForEach-Object { [string]$_ })
$mode = if ($argv.Count -ge 1) { $argv[0] } else { '' }
$event = if ($argv.Count -ge 2) { $argv[1] } else { '' }

# Windows PowerShell reads a redirected stdin into $input before the script runs, which
# leaves [Console]::OpenStandardInput() empty, so take it from there instead. $input
# belongs to the script scope, so it has to be read here rather than in a function, and
# it is read through Get-Variable: writing $input anywhere in the file makes the host
# hold the stream open at exit, which hangs `agent-ledger status` in a terminal.
$stdinLines = @()
if ($mode -eq 'hook') { $stdinLines = @(Get-Variable -Name input -ValueOnly) }

# Everything this writes is JSON for the agent, so hold stdout to UTF-8 rather than
# the console's code page. A redirected handle refuses the change on some hosts,
# which is harmless: the binary writes its own output on an inherited handle.
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch {}

function Get-StateDir {
    if ($env:XDG_STATE_HOME) { return (Join-Path $env:XDG_STATE_HOME 'agent-ledger') }
    if ($env:LOCALAPPDATA) { return (Join-Path $env:LOCALAPPDATA 'agent-ledger') }
    if ($env:USERPROFILE) { return (Join-Path $env:USERPROFILE '.local\state\agent-ledger') }
    return ''
}

function Write-Skip([string]$message) {
    $state = Get-StateDir
    if (-not $state) { return }
    try {
        New-Item -ItemType Directory -Force -Path $state | Out-Null
        $stamp = Get-Date -Format 'yyyy-MM-ddTHH:mm:sszzz'
        Add-Content -LiteralPath (Join-Path $state 'skips.log') -Value "$stamp $message"
    } catch {}
}

# Fail logs why the hook did nothing and still exits 0, so the session goes on.
function Fail([string]$message) {
    if ($script:mode -eq 'hook') {
        Write-Skip $message
        exit 0
    }
    [Console]::Error.WriteLine("agent-ledger: $message")
    exit 1
}

# Run-Binary hands the hook's stdin to the binary untouched and exits with its code.
# Stdout is inherited, so the agent reads the binary's JSON directly. Outside a hook
# stdin is left alone: `agent-ledger status` from a terminal must not wait for EOF.
function Run-Binary([string]$path, $stdin) {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $path
    # The subcommands are single words, so they need no quoting.
    $psi.Arguments = ($script:argv -join ' ')
    $psi.UseShellExecute = $false
    $psi.RedirectStandardInput = ($null -ne $stdin)
    $p = [System.Diagnostics.Process]::Start($psi)
    if ($null -ne $stdin) {
        $s = $p.StandardInput.BaseStream
        if ($stdin.Length -gt 0) { $s.Write($stdin, 0, $stdin.Length) }
        $s.Flush()
        $s.Close()
    }
    $p.WaitForExit()
    exit $p.ExitCode
}

function Read-Stdin {
    if ($script:stdinLines.Count -gt 0) {
        # The host has already decoded these bytes with its input encoding, so encoding
        # the text again the same way gives the original bytes back. Both code pages
        # Windows uses here map every byte to a character, so nothing is lost.
        $enc = $null
        try { $enc = [Console]::InputEncoding } catch {}
        if (-not $enc) { $enc = [System.Text.Encoding]::Default }
        # A hook payload is one line of JSON; a newline inside it would not be valid
        # JSON anyway, so joining with one is safe.
        try { return $enc.GetBytes((($script:stdinLines | ForEach-Object { [string]$_ }) -join "`n")) } catch { return [byte[]]@() }
    }
    try {
        $ms = New-Object System.IO.MemoryStream
        [Console]::OpenStandardInput().CopyTo($ms)
        return $ms.ToArray()
    } catch { return [byte[]]@() }
}

# Invoke-Download fetches one binary and checks it against the release's checksum
# file. That catches a broken download, not a tampered-with release. It throws on
# any problem, leaving nothing behind.
function Invoke-Download([string]$dir, [string]$target, [string]$name, [string]$version) {
    $base = "https://github.com/$script:repo/releases/download/v$version"
    $tmp = Join-Path $dir ('.download.' + $PID)
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    try {
        # Windows PowerShell still defaults to TLS 1.0, which github.com refuses.
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -UseBasicParsing -TimeoutSec 60 -Uri "$base/$name" -OutFile (Join-Path $tmp $name)
        Invoke-WebRequest -UseBasicParsing -TimeoutSec 20 -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')
    } catch {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
        throw "download of $name v$version failed: $($_.Exception.Message)"
    }
    $want = ''
    foreach ($line in Get-Content -LiteralPath (Join-Path $tmp 'checksums.txt')) {
        $f = $line.Trim() -split '\s+'
        if ($f.Count -ge 2 -and ($f[1] -eq $name -or $f[1] -eq "*$name")) { $want = $f[0] }
    }
    $got = (Get-FileHash -LiteralPath (Join-Path $tmp $name) -Algorithm SHA256).Hash
    if (-not $want -or $want -ne $got) {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
        throw "checksum mismatch for $name v$version"
    }
    Move-Item -LiteralPath (Join-Path $tmp $name) -Destination $target -Force
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$version = ''
try { $version = (Get-Content -LiteralPath (Join-Path $root 'VERSION') -TotalCount 1).Trim() } catch {}

# --fetch is this script run again, detached, to download without holding up a prompt.
# It is handed everything it needs, so it repeats none of the work the prompt did.
if ($mode -eq '--fetch') {
    $dir = $argv[1]; $target = $argv[2]; $binName = $argv[3]
    $failed = Join-Path $dir '.download.failed'
    $prev = "$failed.prev"
    try {
        Invoke-Download $dir $target $binName $version
        Remove-Item -LiteralPath $failed -Force -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $prev -Force -ErrorAction SilentlyContinue
    } catch {
        # Two failures in a row back the retry off from 5 minutes to an hour.
        if (Test-Path -LiteralPath $prev) {
            Set-Content -LiteralPath $failed -Value '2' -NoNewline
            Remove-Item -LiteralPath $prev -Force -ErrorAction SilentlyContinue
        } else {
            Set-Content -LiteralPath $failed -Value '' -NoNewline
        }
        Write-Skip $_.Exception.Message
    } finally {
        Remove-Item -LiteralPath (Join-Path $dir '.download.lock') -Recurse -Force -ErrorAction SilentlyContinue
    }
    exit 0
}

# Which agent is running us: PROMOTE: has to know, because Codex never reads CLAUDE.md.
if ($env:CLAUDE_PLUGIN_ROOT -or $env:CLAUDE_PROJECT_DIR -or $env:CLAUDE_PLUGIN_DATA) {
    $env:AGENT_LEDGER_HOST = 'claude'
} elseif ($env:CODEX_THREAD_ID -or $env:CODEX_HOME -or $root -match '[\\/]\.?codex[\\/]') {
    $env:AGENT_LEDGER_HOST = 'codex'
} else {
    $env:AGENT_LEDGER_HOST = ''
}

# A caller-supplied binary wins, before any of the download machinery.
if ($env:AGENT_LEDGER_BIN) {
    if (-not (Test-Path -LiteralPath $env:AGENT_LEDGER_BIN -PathType Leaf)) {
        Fail "AGENT_LEDGER_BIN is set to $env:AGENT_LEDGER_BIN, which is not there"
    }
    if ($mode -eq 'hook') { Run-Binary $env:AGENT_LEDGER_BIN (Read-Stdin) } else { Run-Binary $env:AGENT_LEDGER_BIN $null }
}

switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    'x86' {
        # 32-bit PowerShell on a 64-bit machine: the release has no 386 build.
        if ($env:PROCESSOR_ARCHITEW6432 -eq 'AMD64') { $arch = 'amd64' }
        elseif ($env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { $arch = 'arm64' }
        else { Fail "unsupported arch $env:PROCESSOR_ARCHITECTURE" }
    }
    default { Fail "unsupported arch $env:PROCESSOR_ARCHITECTURE" }
}
$name = "agent-ledger-windows-$arch.exe"

# Where a download goes: the plugin's data folder, which uninstalling removes, else
# the user's own. Never the plugin folder, whose contents Codex fingerprints.
function Get-DataDirs {
    $out = @()
    foreach ($d in @($env:CLAUDE_PLUGIN_DATA, $env:PLUGIN_DATA)) {
        if ($d) { $out += $d }
    }
    if ($env:XDG_DATA_HOME) { $out += (Join-Path $env:XDG_DATA_HOME 'agent-ledger') }
    elseif ($env:LOCALAPPDATA) { $out += (Join-Path $env:LOCALAPPDATA 'agent-ledger\data') }
    elseif ($env:USERPROFILE) { $out += (Join-Path $env:USERPROFILE '.local\share\agent-ledger') }
    return $out
}

$data = ''
$bin = Join-Path $here $name
if (-not (Test-Path -LiteralPath $bin -PathType Leaf)) {
    foreach ($d in Get-DataDirs) {
        $try = Join-Path $d "v$version"
        if (-not $data) { $data = $try }
        if (Test-Path -LiteralPath (Join-Path $try $name) -PathType Leaf) { $data = $try; break }
    }
    if (-not $data) { Fail 'no data folder: LOCALAPPDATA and USERPROFILE are both unset' }
    $bin = Join-Path $data $name
    # Run by hand rather than as a hook, the plugin data folder is unset: reuse the
    # copy the hooks downloaded instead of fetching a second one.
    if (-not (Test-Path -LiteralPath $bin -PathType Leaf)) {
        $homes = @()
        if ($env:CODEX_HOME) { $homes += $env:CODEX_HOME }
        elseif ($env:USERPROFILE) { $homes += (Join-Path $env:USERPROFILE '.codex') }
        if ($env:CLAUDE_CONFIG_DIR) { $homes += $env:CLAUDE_CONFIG_DIR }
        elseif ($env:USERPROFILE) { $homes += (Join-Path $env:USERPROFILE '.claude') }
        foreach ($h in $homes) {
            $found = Get-ChildItem -LiteralPath (Join-Path $h 'plugins\data') -Filter $name -Recurse -File -ErrorAction SilentlyContinue |
                Where-Object { $_.Directory.Name -eq "v$version" } | Select-Object -First 1
            if ($found) { $bin = $found.FullName; break }
        }
    }
}

if (Test-Path -LiteralPath $bin -PathType Leaf) {
    if ($mode -eq 'hook') { Run-Binary $bin (Read-Stdin) } else { Run-Binary $bin $null }
}

# Run by hand, wait for the download and report it plainly rather than in JSON.
if ($mode -ne 'hook') {
    try { Invoke-Download $data $bin $name $version } catch { Fail $_.Exception.Message }
    Run-Binary $bin $null
}

# The binary is missing. Fetch it in the background and wait a few seconds, so the
# first prompt after an install is usually handled; meanwhile say so in valid JSON.
$stdinBytes = Read-Stdin
$inputText = ''
if ($stdinBytes.Length -gt 0) { $inputText = [System.Text.Encoding]::UTF8.GetString($stdinBytes) }

# A ledger command must not reach the model unhandled: block it, so the text stays in
# the input box to send again. Other prompts go on. The <pasted_content> wrapper is
# Claude Code's, kept here so one line works whichever agent ends up running this.
$commandRe = '"prompt"\s*:\s*"(<pasted_content[^>]*>\\n)?\s*(pin|carry|unpin|promote)!?(\s*@[a-z]+)?!?(\s*\[[^\]"]*\])?\s*(:|\uFF1A)' +
'|"prompt"\s*:\s*"\s*(pins|ledger)\?\s*"' +
'|"prompt"\s*:\s*"\s*\$(agent-ledger:)?(pin|pins|unpin|carry|promote|help)(\s|\\n|")'

function Say([string]$text, [string]$suffix) {
    $esc = $text.Replace('\', '\\').Replace('"', '\"')
    if ($script:event -eq 'user-prompt-expansion') {
        # A slash command: block only our own, whose expansion would otherwise reach
        # the model as plain text. Stay quiet for anyone else's.
        if ($script:inputText -match '"command_name"\s*:\s*"agent-ledger:') {
            [Console]::Out.Write('{"decision":"block","reason":"' + $esc + ' This command was not saved; send it again' + $suffix + '.","hookSpecificOutput":{"hookEventName":"UserPromptExpansion","suppressOriginalPrompt":true}}' + "`n")
        }
        return
    }
    if ($script:event -eq 'user-prompt-submit' -and $script:inputText -match $script:commandRe) {
        $extra = ',"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","suppressOriginalPrompt":true}'
        # Codex marks its payload with turn_id and rejects the field.
        if ($script:inputText -match '"turn_id"') { $extra = '' }
        [Console]::Out.Write('{"decision":"block","reason":"' + $esc + ' This command was not saved; send it again' + $suffix + '."' + $extra + '}' + "`n")
        return
    }
    [Console]::Out.Write('{"systemMessage":"' + $esc + '"}' + "`n")
}

$lock = Join-Path $data '.download.lock'
$failed = Join-Path $data '.download.failed'
New-Item -ItemType Directory -Force -Path $data -ErrorAction SilentlyContinue | Out-Null
# A lock older than 5 minutes is stale: the fetch that made it is gone.
$stale = Get-Item -LiteralPath $lock -Force -ErrorAction SilentlyContinue
if ($stale -and $stale.LastWriteTime -lt (Get-Date).AddMinutes(-5)) {
    Remove-Item -LiteralPath $lock -Recurse -Force -ErrorAction SilentlyContinue
}
# After a failed download wait 5 minutes before retrying, an hour after two in a row.
$failedItem = Get-Item -LiteralPath $failed -Force -ErrorAction SilentlyContinue
$waitMin = 5
if ($failedItem -and $failedItem.Length -gt 0) { $waitMin = 60 }
if ($failedItem -and $failedItem.LastWriteTime -lt (Get-Date).AddMinutes(-$waitMin)) {
    Move-Item -LiteralPath $failed -Destination "$failed.prev" -Force -ErrorAction SilentlyContinue
    $failedItem = $null
}

function Say-Failed {
    $state = Get-StateDir
    if ($script:event -eq 'user-prompt-submit' -or $script:event -eq 'user-prompt-expansion' -or $script:event -eq 'session-start') {
        Say "agent-ledger is not working: downloading its binary failed (see $state\skips.log). It retries in $script:waitMin minutes; to retry now, delete $script:failed." ' once agent-ledger works'
    }
    Fail "$script:name v$script:version missing; last download failed, retrying in $script:waitMin minutes"
}

if ($failedItem) { Say-Failed }

# Creating the lock folder is the claim: if it is already there, another prompt is
# fetching and this one only waits.
$haveLock = $false
try {
    New-Item -ItemType Directory -Path $lock -ErrorAction Stop | Out-Null
    $haveLock = $true
} catch {}
if ($haveLock) {
    try {
        $self = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
        Start-Process -FilePath $self -WindowStyle Hidden -ArgumentList @(
            '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $PSCommandPath,
            '--fetch', $data, $bin, $name) | Out-Null
    } catch {
        # Nothing is fetching after all, so drop the claim and let the next prompt try.
        Write-Skip "could not start the background download: $($_.Exception.Message)"
        Remove-Item -LiteralPath $lock -Recurse -Force -ErrorAction SilentlyContinue
    }
}

if ($event -ne 'session-end') {
    for ($i = 0; $i -lt 12; $i++) {
        if (Test-Path -LiteralPath $bin -PathType Leaf) { break }
        if (-not (Test-Path -LiteralPath $lock)) { break }
        Start-Sleep -Milliseconds 500
    }
}
if (Test-Path -LiteralPath $bin -PathType Leaf) { Run-Binary $bin $stdinBytes }

$failedItem = Get-Item -LiteralPath $failed -Force -ErrorAction SilentlyContinue
if ($failedItem) {
    $waitMin = 5
    if ($failedItem.Length -gt 0) { $waitMin = 60 }
    Say-Failed
}
switch ($event) {
    'user-prompt-submit' { Say 'agent-ledger is still installing (one-time download); this prompt was not recorded.' ' in a few seconds' }
    'user-prompt-expansion' { Say 'agent-ledger is still installing (one-time download); this prompt was not recorded.' ' in a few seconds' }
    'session-start' { Say 'agent-ledger: downloading its binary in the background (one time).' '' }
}
Fail "$name missing; fetching v$version in the background"
