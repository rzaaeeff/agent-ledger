# agent-ledger

You tell the agent "don't touch the migrations". An hour later the context compacts, and that line is gone.

agent-ledger keeps the lines you mark and puts them back after every compaction. It is a small local hook for Claude Code and Codex: no model calls, no server, no database.

```
PIN: don't touch the migrations, the schema is frozen until Friday
```

After `/compact`, the agent sees that pin again, word for word, along with up to your last 5 prompts and its last 10 recorded decisions (fewer if they don't fit the size limit).

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

On first use the plugin downloads one binary (about 3.5 MB) for your OS from the GitHub release and checks its SHA-256. The download runs in the background, so hooks start working a few seconds after first use; anything before that is not recorded. Needs `bash`, `curl`, `awk`, `git` and `sha256sum` or `shasum`, and only works inside a git repository (elsewhere the hooks do nothing).

The hooks never block your prompt (`PINS?` is the one exception, on purpose). If the download fails, the reason goes to `~/.local/state/agent-ledger/skips.log`. If saving a pin fails, you see "NOT SAVED" and the error goes to `errors.log` in the ledger folder.

## Using it

Start a prompt with one of these:

| Prefix | Who sees it | After `/clear` |
|---|---|---|
| `PIN:` | this chat | dropped; you get a list of what was dropped (kept if `key` is `branch` or `repo`) |
| `PIN!:` | this chat | kept |
| `CARRY:` | the next chat after `/clear`, once | handed over, then deleted |
| `PIN@branch:` | every chat on this branch (refused on `main`, `master`, the default branch and a detached HEAD) | kept |
| `PIN@repo:` | every chat in this repo | kept |
| `PIN@global:` | every chat in every repo | kept |
| `PINS?` | lists your pins with numbers; only you see it, the agent gets nothing | |
| `UNPIN: <number, id, words or last>` | removes a pin | |
| `PROMOTE: <number, id or words>` | moves a pin into `AGENTS.md` (or `CLAUDE.md` if only that exists), uncommitted | |

Prefixes are case-sensitive and must start the prompt (spaces before them are fine); "pin:" in the middle of a sentence is ignored. Each pin gets a short id, shown when you pin it, in `PINS?` and in the re-injected list.

You don't have to remember a pin's exact wording:

- `UNPIN: friday migrations` finds "Don't touch the migrations until Friday": case, punctuation and word order don't matter. `UNPIN: last` takes the newest pin, `UNPIN: 2` the second one in `PINS?`.
- If nothing or more than one pin matches, nothing is removed and you get the closest pins with their ids.
- Pinning the same text again does nothing (`PIN!:` on a `PIN:` makes it survive `/clear`). Pinning something close to an existing pin saves it and names the old one, so you can `UNPIN` it if the new one replaces it. The tool never decides that for you.

Decisions: the plugin ships a `record-decision` skill. When you and the agent settle something, it runs `agent-ledger decide "<decision>" "<because>" "<rejected>"`, and the decision comes back after compaction, so you don't rehash rejected options.

To check what is stored or what the next compaction would inject:

```
agent-ledger status
agent-ledger preview          # or: preview clear
```

The binary is in the plugin's `bin/` folder (for Claude Code, somewhere under `~/.claude/plugins/`); add it to your `PATH` or call it by full path. Run these from the agent's shell (e.g. `!agent-ledger status` in Claude Code): from a normal terminal there is no session id, so you see the branch ledger, not your chat's.

## Privacy

- By default every prompt is logged, so recent prompts can come back after compaction. Turn this down with `"logging": "pins-only"` or `"off"`.
- Before anything is stored, common secrets are masked: API keys, GitHub/Slack/AWS tokens, private keys, JWTs, URL passwords and `password=`-style values. This is pattern matching. It will miss some secrets, and it can mask harmless text that looks like one.
- Files stay on your machine, under `~/.local/state/agent-ledger/` and readable only by you. Nothing is sent anywhere; the only network call is the one-time binary download. When a session starts, ledgers in that repo untouched for 30 days are deleted, including their `PIN!:` pins and decisions. `PIN@branch:` pins go once the branch is deleted and idle. Repo and global pins are kept until you `UNPIN` them.
- A repo's `.agent-ledger.json` can turn redaction off or logging up. Check it in repos you don't trust.

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

After `/clear`, Claude Code may start a new session id. Your `PIN!:` pins and `CARRY:` items move to it only if exactly one chat in the same folder was cleared in the last 10 minutes. If several were, nothing moves and you are told. A forked chat starts its own ledger.

Without a hook session id (`decide`, `status`, `preview`), the ledger comes from `CLAUDE_SESSION_ID`, `CLAUDE_CODE_SESSION_ID` or `CODEX_THREAD_ID`, else the current branch (`detached` on a detached HEAD). You get a warning when it falls back to the branch.

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
| `budget_chars` | size limit of what is re-injected. Older prompts, then decisions, are dropped to fit; pins are never cut, but you get a warning when they alone are over. Above ~10,000 the host may truncate it | `9000` |
| `promote_to` | file name `PROMOTE:` writes to | `AGENTS.md`; `CLAUDE.md` if only that exists |

Invalid values are ignored and listed by `agent-ledger config`.

## Cost

On a slow 2-core VM each hook takes 40-75 ms plus about 65 ms for the launcher script, so roughly 0.1 s per prompt. After compaction, prompts and decisions are trimmed to fit `budget_chars` (9000 characters, about 2,000-2,500 tokens); pins are never trimmed, so many pins can push past it.

## Development

```
go test ./...
scripts/release.sh   # builds all 6 targets + checksums.txt into dist/
```

`AGENT_LEDGER_BIN=/path/to/binary` makes the plugin use your own build. `AGENT_LEDGER_DEBUG=1` logs every hook input to `hooks.debug.log` in the repo's ledger folder.

## License

MIT
