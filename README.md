# agent-ledger

You tell the agent "don't touch the migrations". An hour later the context compacts, and that line is gone.

agent-ledger keeps the lines you mark and puts them back after every compaction. It is a small local hook for Claude Code and Codex: no model calls, no server, no database.

```
PIN: don't touch the migrations, the schema is frozen until Friday
```

After `/compact`, the agent sees that pin again, word for word. You get a one-line note saying what was restored.

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

Codex (macOS and Linux; experimental):

```
codex plugin marketplace add rzaaeeff/agent-ledger
```

then install `agent-ledger` from `/plugins` and approve its hooks.

On first use the plugin downloads one binary (about 3.5 MB) for your OS from the GitHub release into `~/.local/share/agent-ledger/`, and checks its SHA-256 against the release's checksum file (this catches a broken download, not a compromised release). The download runs in the background; until it finishes you see "still installing" and prompts are not recorded. Needs `bash`, `curl`, `awk`, `git` and `sha256sum` or `shasum`, and only works inside a git repository (elsewhere a `PIN:` tells you so).

Your normal prompts are never blocked. `PINS?`, `LEDGER?`, `UNPIN:`, `PROMOTE:` and rewording are answered without a model turn; the agent hears about removed or changed pins with your next prompt. If the download fails, the reason goes to `~/.local/state/agent-ledger/skips.log`. If saving a pin fails, you see "NOT SAVED" and the error goes to `errors.log` in the ledger folder.

## Using it

Start a prompt with one of these. Forgot them? Send `LEDGER?` for a cheat sheet.

| Prefix | Who sees it | After `/clear` |
|---|---|---|
| `PIN:` | this chat | dropped; you get a list of what was dropped (kept if `key` is `branch` or `repo`) |
| `PIN!:` | this chat | kept |
| `CARRY:` | the next chat after `/clear`, once | handed over, then deleted |
| `PIN@branch:` | every chat on this branch (refused on `main`, `master`, the default branch and a detached HEAD) | kept |
| `PIN@repo:` | every chat in this repo | kept |
| `PIN@global:` | every chat in every repo | kept |
| `PINS?` | lists your pins with numbers; only you see it, the agent gets nothing | |
| `LEDGER?` | the cheat sheet and what's stored; only you see it | |
| `PIN [ref]: <new text>` | rewords a pin; it keeps its scope and lifetime | |
| `UNPIN: <ref>` | removes a pin | |
| `PROMOTE: <ref>` | moves a pin into `CLAUDE.md` or `AGENTS.md` (see `promote_to`), uncommitted | |

A `<ref>` is a number from `PINS?`, a pin's id, `last`, or words from the pin. Prefixes work in any case (`pin:` = `PIN:`) but only at the start of the prompt; "pin:" in the middle of a sentence is ignored. A pin is the first line only: the rest of the prompt goes to the agent as usual. Near misses like `PIN; x` or `PIN x` save nothing and tell you so (the prompt still goes to the agent). A lower-case `pin:` saves and offers `UNPIN: last` in case you didn't mean it.

You don't have to remember a pin's exact wording:

- `UNPIN: friday migrations` finds "Don't touch the migrations until Friday": case, punctuation and word order don't matter, and a word of 4+ letters also matches longer words starting with it (`migration` finds "migrations"). Words match whole, so `all` does not find "called"; refs under 3 letters only match ids. `UNPIN: last` takes the newest pin, `UNPIN: 2` the second one in `PINS?`.
- If nothing or more than one pin matches, nothing is removed and you get the closest pins with their ids.
- Pinning the same text again does nothing (`PIN!:` on a `PIN:` makes it survive `/clear`); if only case or punctuation differ, you're shown the stored wording so you can reword it. Pinning something close to an existing pin saves it and names the old one, so you can `UNPIN` it if the new one replaces it. The tool never decides that for you.

Decisions (advanced): `agent-ledger decide "<decision>" "<because>" "<rejected>"` records one, and the last 10 come back after compaction, labelled as context the agent recorded, not as instructions. Nothing calls it for you: ask the agent to run it, or run it yourself.

To check what is stored or what the next compaction would inject:

```
agent-ledger status
agent-ledger preview          # or: preview clear
```

Run these from the agent's shell (e.g. `!agent-ledger status` in Claude Code): Claude Code puts plugin `bin/` folders on its shell's PATH, and from a normal terminal there is no session id, so you'd see the branch ledger, not your chat's. In Codex, call the plugin's `bin/agent-ledger` by full path.

## Privacy

- By default only ledger commands are logged, not your other prompts. `"logging": "all"` also logs every prompt, and then your last 5 come back after compaction.
- Before anything is stored, common secrets are masked: API keys, GitHub/Slack/AWS tokens, private keys, JWTs, Stripe keys, Slack webhook URLs, URL passwords, and `password=`-style values (also names like `db_password`, quoted or not). This is pattern matching. It will miss some secrets, and it can mask harmless text that looks like one.
- Files stay on your machine, under `~/.local/state/agent-ledger/` and readable only by you. The tool itself makes no network calls apart from the one-time binary download; what it re-injects goes to your model provider like the rest of the chat. When a session starts, ledgers in that repo untouched for 30 days are deleted, including their `PIN!:` pins and decisions. `PIN@branch:` pins go once the branch is deleted and idle. Repo and global pins are kept until you `UNPIN` them.
- A repo's `.agent-ledger.json` can only make `logging`, `redact`, `key` and `prune_days` stricter than your own settings, and its `promote_to` may only be `AGENTS.md` or `CLAUDE.md`. Anything else is ignored; the session-start note says so and `agent-ledger config` says why.
- What comes back after compaction is framed for the agent as your notes: pins are your words; recent prompts and decisions are quoted context, not new instructions. That lowers the risk of old text being read as a fresh order; it does not remove it.

## Where it works

| Tool | Status |
|---|---|
| Claude Code: CLI, VS Code, JetBrains, desktop Code tab | Should work: they share the same hooks and plugins |
| Codex: CLI and desktop app | Experimental, macOS/Linux. Not on Windows (Codex runs hooks with `cmd.exe`). Codex already keeps recent user messages word for word when it compacts, so the gain is smaller there: mostly old rules and `PIN@branch`/`@repo`/`@global` across `/new` |
| Codex IDE extension | Plugins aren't installed there; manual hook setup may work, untested |
| Claude Code on the web, Codex cloud | No. The ledger is a local folder |

Codex `/clear` opens a new thread with no link to the old one, so `PIN:`, `PIN!:` and `CARRY:` don't cross it. Use `PIN@branch:` or `PIN@repo:` there.

## How chats are told apart

By default each chat has its own ledger, keyed by the session id Claude Code or Codex passes to the hooks. Switching branch or worktree mid-chat keeps the same ledger, and a second chat on the same branch never sees the first one's pins.

After `/clear`, Claude Code may start a new session id. Your `PIN!:` pins and `CARRY:` items move to it only if exactly one chat in the same folder was cleared in the last 10 minutes. If several were, nothing moves and you are told. A forked chat starts its own ledger (it sees branch, repo and global pins, not the original chat's).

Without a hook session id (`decide`, `status`, `preview`), the ledger comes from `CLAUDE_SESSION_ID`, `CLAUDE_CODE_SESSION_ID` or `CODEX_THREAD_ID`, else the current branch (`detached` on a detached HEAD). You get a warning when it falls back to the branch.

## Configuration

Optional. Put it in `~/.config/agent-ledger/config.json` (yours) or `.agent-ledger.json` in the repo (the team's). Env vars `AGENT_LEDGER_<KEY>` override both. `agent-ledger config` shows the effective values and where each came from.

```json
{ "key": "session", "logging": "pins-only", "redact": "on", "prune_days": 30 }
```

| Key | Values | Default |
|---|---|---|
| `key` | `session`, `branch`, `repo`: who shares one ledger (a repo file can't widen it) | `session` |
| `clear` | `drop`, `keep`: what `/clear` does to `PIN:` pins | `drop` for `session`, else `keep` |
| `logging` | `all`, `pins-only`, `off` | `pins-only` |
| `redact` | `on`, `off` | `on` |
| `prune_days` | days of no activity before a ledger is deleted; `0` = never | `30` |
| `recent_prompts` / `decisions` | how many come back after compaction | `5` / `10` |
| `budget_bytes` | size limit of what is re-injected, in bytes, at most 9500 (Claude Code keeps 10,000 characters of hook output, Codex about 10,000 bytes). Older prompts, then decisions, are dropped to fit; pins that still don't fit are left out, and the agent is told so at the top | `9000` |
| `promote_to` | file name `PROMOTE:` writes to | the one of `CLAUDE.md`/`AGENTS.md` that exists; with both, `CLAUDE.md` (what Claude Code reads) unless it imports `@AGENTS.md` or you run Codex; with neither, `AGENTS.md` |

Invalid values are ignored and listed by `agent-ledger config`.

## Cost

On a slow 2-core VM each hook takes 40-75 ms plus about 65 ms for the launcher script, so roughly 0.1 s per prompt. After compaction at most 9,000 bytes come back (about 2,000-2,500 tokens). When you pin past 6,000 bytes, the `PIN:` reply warns you.

## Development

```
go test ./...
scripts/release.sh   # builds all 6 targets + checksums.txt into dist/
```

`AGENT_LEDGER_BIN=/path/to/binary` makes the plugin use your own build. `AGENT_LEDGER_DEBUG=1` logs every hook input to `hooks.debug.log` in the repo's ledger folder.

## License

MIT
