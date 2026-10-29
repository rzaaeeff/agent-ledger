# agent-ledger

Pins, decisions and recent prompts that survive context compaction in Claude Code, scoped to **one session**.

- `PIN: <text>` in any prompt stores the text verbatim. `UNPIN: <id or text>` removes it.
- After `/compact` (or auto-compact), pins, the last 5 prompts and the last 10 decisions are re-injected.
- After `/clear`, nothing is re-injected. You see a short list of the pins that were dropped.
- Session ledgers idle for 30 days are pruned at startup.
- No dependencies: one static Go binary per OS, files under `~/.local/state/agent-ledger/<repo>/<session>/` (0600).

## Install (Claude Code)

```
/plugin marketplace add rzaaeeff/agent-ledger
/plugin install agent-ledger@agent-ledger
```

On first use, `bin/agent-ledger` downloads the binary for your OS/arch from the matching GitHub release and checks its SHA-256. If that fails, hooks skip silently and log to `~/.local/state/agent-ledger/skips.log`. To use your own build, set `AGENT_LEDGER_BIN=/path/to/agent-ledger`.

## Commands

| Command | What it does |
|---|---|
| `agent-ledger decide "<decision>" "<because>" "<rejected>"` | Append a decision (the `record-decision` skill tells the agent how) |
| `agent-ledger status` | Show the ledger dir, pin count and token estimate |
| `agent-ledger inject` | Print the ledger as context (for CLIs without hooks, e.g. run at Codex session start) |
| `agent-ledger hook <user-prompt-submit\|session-start\|session-end>` | Hook entry points; read the hook JSON on stdin, always exit 0 |

## Keying

1. The hook's `session_id` (hooks), else `CLAUDE_SESSION_ID` (written to `CLAUDE_ENV_FILE` at session start), else `CLAUDE_CODE_SESSION_ID`.
2. With no session id, the current git branch (`detached` for detached HEAD), so manual runs and other CLIs still work.

Set `AGENT_LEDGER_DEBUG=1` to log every hook's input to `hooks.debug.log` in the repo ledger dir.

## Build

```
go test ./...
go build -o bin/agent-ledger-$(go env GOOS)-$(go env GOARCH) ./cmd/agent-ledger
scripts/release.sh   # all 6 targets + checksums.txt into dist/
```

Releases: bump `VERSION` and `.claude-plugin/plugin.json`, then push tag `v<VERSION>`; `release.yml` builds and uploads.
