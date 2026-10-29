# agent-ledger

You tell the agent "don't touch the migrations". An hour later the context compacts, and that line is gone.

agent-ledger keeps the lines you mark and puts them back after every compaction. It is a small local hook for Claude Code and Codex: no model calls, no server, no database.

```
PIN: don't touch the migrations, the schema is frozen until Friday
```

After `/compact`, the agent sees that pin again, word for word, along with your last 5 prompts and its last 10 recorded decisions.

**Status: v0.1, early.** The hooks are covered by tests that feed them the JSON Claude Code and Codex send. They have not yet been run in a live Claude Code or Codex session. Please report what breaks.

## Is it for you?

Use it if your sessions are long enough to compact and you keep repeating the same instructions after they do.

Skip it if:

- The rule should hold forever. Put it in `AGENTS.md` or `CLAUDE.md`; the agent already rereads those. (`PROMOTE:` moves a pin there when it turns out to be permanent.)
- You want searchable memory across projects or automatic notes. This only keeps what you mark, plus recent prompts.

What it does not do: make the model obey. It keeps your text in the context; whether the agent follows it is still up to the model.

## Install

Claude Code:

```
/plugin marketplace add rzaaeeff/agent-ledger
/plugin install agent-ledger@agent-ledger
```

Codex (macOS and Linux):

```
codex plugin marketplace add rzaaeeff/agent-ledger
```

then install `agent-ledger` from `/plugins` and approve its hooks.

On first use the plugin downloads one binary (about 3.5 MB) for your OS from the GitHub release and checks its SHA-256. The download runs in the background, so hooks start working a few seconds after first use; anything before that is not recorded. Needs `bash`, `curl`, `git` and `sha256sum` or `shasum`. If anything fails, the hooks do nothing and write the reason to `~/.local/state/agent-ledger/skips.log`; they never block your prompt.

## Using it

Start a prompt with one of these:

| Prefix | Who sees it | After `/clear` |
|---|---|---|
| `PIN:` | this chat | dropped; you get a list of what was dropped |
| `PIN!:` | this chat | kept |
| `CARRY:` | the next chat after `/clear`, once | handed over, then deleted |
| `PIN@branch:` | every chat on this branch (refused on `main`/`master`) | kept |
| `PIN@repo:` | every chat in this repo | kept |
| `PIN@global:` | every chat in every repo | kept |
| `UNPIN: <id or text>` | removes a pin | |
| `PROMOTE: <id or text>` | moves a pin into `AGENTS.md` (else `CLAUDE.md`), uncommitted | |

Each pin gets a short id, shown when you pin it and in the re-injected list.

Decisions: the plugin ships a `record-decision` skill. When you and the agent settle something, it runs `agent-ledger decide "<decision>" "<because>" "<rejected>"`, and the decision comes back after compaction, so you don't rehash rejected options.

To check what is stored or what the next compaction would inject:

```
agent-ledger status
agent-ledger preview          # or: preview clear
```

These are plain CLI commands. The binary is in the plugin's `bin/` folder (for Claude Code, somewhere under `~/.claude/plugins/`); add that folder to your `PATH` or call it by full path.

## Privacy

- By default every prompt is logged, so recent prompts can come back after compaction. Turn this down with `"logging": "pins-only"` or `"off"`.
- Before anything is stored, common secrets are masked: API keys, GitHub/Slack/AWS tokens, private keys, JWTs, URL passwords and `password=`-style values. This is pattern matching. It will miss some secrets, and it can mask harmless text that looks like one.
- Files stay on your machine, under `~/.local/state/agent-ledger/` and readable only by you. Nothing is sent anywhere; the only network call is the one-time binary download. Chat ledgers untouched for 30 days are deleted; repo and global pins are kept until you `UNPIN` them.

## Where it works

| Tool | Status |
|---|---|
| Claude Code: CLI, VS Code, JetBrains, desktop Code tab | Should work: they share the same hooks and plugins |
| Codex: CLI and desktop app | Should work on macOS/Linux. Not on Windows (Codex runs hooks with `cmd.exe`) |
| Codex IDE extension | Plugins aren't installed there; manual hook setup may work, untested |
| Claude Code on the web, Codex cloud | No. The ledger is a local folder |

Codex `/clear` opens a new thread with no link to the old one, so `PIN:`, `PIN!:` and `CARRY:` don't cross it. Use `PIN@branch:` or `PIN@repo:` there.

## How chats are told apart

By default each chat has its own ledger, keyed by the session id Claude Code or Codex passes to the hooks. Switching branch or worktree mid-chat keeps the same ledger, and a second chat on the same branch never sees the first one's pins.

After `/clear`, if the tool starts a new session id, pins are moved to it only when exactly one chat in that folder was cleared in the last 10 minutes. Otherwise nothing is moved and you are told. A forked chat starts its own ledger.

Without a session id (manual runs, tools without hooks), the ledger for the current branch is used.

## Configuration

Optional. Put it in `~/.config/agent-ledger/config.json` (yours) or `.agent-ledger.json` in the repo (the team's). Env vars `AGENT_LEDGER_<KEY>` override both. `agent-ledger config` shows the effective values and where each came from.

```json
{ "key": "session", "logging": "all", "redact": "on", "prune_days": 30 }
```

| Key | Values | Default |
|---|---|---|
| `key` | `session`, `branch`, `repo`: who shares one ledger | `session` |
| `clear` | `drop`, `keep`: what `/clear` does to `PIN:` pins | `drop` for `session`, else `keep` |
| `logging` | `all`, `pins-only`, `off` | `all` |
| `redact` | `on`, `off` | `on` |
| `prune_days` | days of no activity before a ledger is deleted; `0` = never | `30` |
| `recent_prompts` / `decisions` | how many come back after compaction | `5` / `10` |
| `budget_chars` | size limit of what is re-injected. Older prompts, then decisions, are dropped to fit; pins are never cut, but you get a warning when they alone are over | `9000` |
| `promote_to` | file name `PROMOTE:` writes to | `AGENTS.md`, else `CLAUDE.md` |

Invalid values are ignored and listed by `agent-ledger config`.

## Cost

Each hook takes tens of milliseconds: on a slow 2-core VM, 40-75 ms plus about 65 ms for the launcher script. After compaction it adds at most `budget_chars` (9000 characters, about 2,000-2,500 tokens) to the context.

## Development

```
go test ./...
scripts/release.sh   # builds all 6 targets + checksums.txt into dist/
```

`AGENT_LEDGER_BIN=/path/to/binary` makes the plugin use your own build. `AGENT_LEDGER_DEBUG=1` logs every hook input to `hooks.debug.log` in the repo's ledger folder.

## License

MIT
