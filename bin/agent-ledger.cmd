@echo off
rem Windows launcher. Codex runs hooks as `cmd.exe /C "<command line>"`, which cannot
rem run the extensionless bash script next to this file; cmd applies PATHEXT to a path
rem with no extension, so it finds this instead. Claude Code on Windows goes through
rem Git Bash and keeps using the bash script.
rem A hook must never fail the session, so a missing PowerShell exits 0 and says nothing.
setlocal
set "AL_PS=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%AL_PS%" set "AL_PS=powershell.exe"
"%AL_PS%" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%~dp0agent-ledger.ps1" %*
set "AL_RC=%ERRORLEVEL%"
if not "%AL_RC%"=="0" if "%~1"=="hook" exit /b 0
exit /b %AL_RC%
