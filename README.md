# agent-ledger

Pins, decisions and recent prompts that survive context compaction in Claude Code, scoped to **one session**.

- `PIN: <text>` at the start of a prompt stores the text verbatim. `UNPIN: <id or text>` removes it (from any scope).
- After `/compact` (or auto-compact), pins, the last 5 prompts and the last 10 decisions are re-injected.
- After `/clear`, nothing is re-injected. You see a short list of the pins that were dropped.
- Session ledgers idle for 30 days are pruned at startup.
- No dependencies: one static Go binary per OS, files under `~/.local/state/agent-ledger/<repo>-<hash>/<session>/` (0600). The hash of the repo path keeps same-named repos and submodules apart.

## Pins: scope and lifetime

| Prompt starts with | Seen by | On `/clear` |
|---|---|---|
| `PIN:` | this session | dropped, and you see which |
| `PIN!:` | this session | kept (and moved if `/clear` starts a new session id) |
| `CARRY:` | the chat after the next `/clear`, once | injected, then gone; a handoff note |
| `PIN@branch:` | every session on this branch; refused on the default branch (`main`, `master`, origin's HEAD) and detached HEAD; pruned only once the branch is deleted | kept |
| `PIN@repo:` | every session and branch of this repo; never pruned | kept |
| `PIN@global:` | every repo | kept |

`PROMOTE: <id or text>` moves a pin into the repo's `AGENTS.md` (else `CLAUDE.md`, or `promote_to`) under "Pinned rules (promoted from agent-ledger)" and unpins it. Nothing is committed; review with `git diff`.

After `/clear`, kept pins, carried notes and scoped pins reach the model as `additionalContext`; the dropped list goes only to you. `agent-ledger status` counts the scoped pins and `agent-ledger preview clear` shows what a `/clear` would drop, keep and carry.

## Install (Claude Code)

```
/plugin marketplace add rzaaeeff/agent-ledger
/plugin install agent-ledger@agent-ledger
```

On the first hook, `bin/agent-ledger` fetches the binary for your OS/arch from the matching GitHub release in the background and checks its SHA-256; the ledger starts working from the next event. If that fails, hooks skip silently and log to `~/.local/state/agent-ledger/skips.log`. To use your own build, set `AGENT_LEDGER_BIN=/path/to/agent-ledger`.

## Commands

| Command | What it does |
|---|---|
| `agent-ledger decide "<decision>" "<because>" "<rejected>"` | Append a decision (the `record-decision` skill tells the agent how) |
| `agent-ledger status` | Show the ledger dir, pin count and token estimate |
| `agent-ledger config` | Effective settings and where each came from |
| `agent-ledger preview [source]` | Dry run of SessionStart (default `compact`) |
| `agent-ledger inject` | Print the ledger as context (for CLIs without hooks, e.g. run at Codex session start) |
| `agent-ledger hook <user-prompt-submit\|session-start\|session-end>` | Hook entry points; read the hook JSON on stdin, always exit 0 |

## Configuration

Layers, later wins: defaults < user `~/.config/agent-ledger/config.json` (`%APPDATA%` on Windows) < repo `.agent-ledger.json` (commit it to share with the team) < env `AGENT_LEDGER_<KEY>`.

```json
{ "key": "session", "clear": "drop", "prune_days": 30, "recent_prompts": 5, "decisions": 10, "budget_chars": 16000,
  "logging": "all", "redact": "on" }
```

| Key | Values | Meaning |
|---|---|---|
| `key` | `session` (default), `branch`, `repo` | Who shares a ledger: one chat, everyone on a branch, or the whole repo |
| `clear` | `drop`, `keep` | On `/clear`: drop pins and show them, or re-inject pins and decisions. Default `drop` for `session`, `keep` otherwise |
| `prune_days` | 0-36500 | Delete ledgers idle this long at startup; 0 = never |
| `recent_prompts`, `decisions` | 0-100, 0-200 | How many are re-injected after compact |
| `budget_chars` | 1000-400000 | Injection budget; pins are never cut |
| `logging` | `all` (default), `pins-only`, `off` | Which prompts are stored; pins are always kept |
| `redact` | `on` (default), `off` | Mask API keys, tokens, private keys, JWTs, URL passwords and `password=`-style values before storing |
| `promote_to` | a file name like `RULES.md` | Target of `PROMOTE:` (top of the work tree; no paths) |

Bad files, unknown keys and invalid values are ignored; `agent-ledger config` shows each effective value, where it came from and any problem. `agent-ledger preview [compact|clear|startup|resume]` shows what the next SessionStart would inject, drop or prune, without doing it.

`clear: keep` with `key: session` only helps if `/clear` keeps the session id (unverified).

## Keying

With `key: session` (default):

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
