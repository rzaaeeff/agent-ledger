# agent-ledger

Pins, decisions and recent prompts that survive context compaction in Claude Code and Codex, scoped to **one session**.

- `PIN: <text>` at the start of a prompt stores the text verbatim. `UNPIN: <id or text>` removes it (from any scope).
- After `/compact` (or auto-compact), pins, the last 5 prompts and the last 10 decisions are re-injected.
- After `/clear`, nothing is re-injected. You see a short list of the pins that were dropped.
- Session ledgers idle for 30 days are pruned at startup.
- A forked chat gets a new session id, so it starts its own ledger; scoped pins (below) still apply.
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

## Install (Codex)

Codex loads the same plugin (it reads `.claude-plugin/` and `hooks/hooks.json`; hooks are on by default):

```
codex plugin marketplace add rzaaeeff/agent-ledger
```

Then install `agent-ledger` from `/plugins` and approve its hooks when Codex asks. `decide` and `status` find the session through `CODEX_THREAD_ID`.

Codex differences:

- `/clear` starts a new thread and nothing links it to the old one, so the new chat starts with an empty session ledger: no "dropped" list, and `PIN!:`/`CARRY:` do not cross it. For pins that must survive, use `PIN@branch:`/`PIN@repo:`, or set `"key": "branch"`.
- Compaction re-injects as in Claude Code.
- macOS and Linux only: on Windows, Codex runs hooks through `cmd.exe`, which does not expand `${CLAUDE_PLUGIN_ROOT}` or run the `sh` shim.

Other CLIs without hooks: run `agent-ledger inject` at session start and paste or pipe its output (keyed by branch).

## Performance

Measured on a 2-core cloud VM, 30 pins and 200 prompts logged, 100 runs each: `user-prompt-submit` 48 ms, `session-start` (compact) 76 ms, `session-end` 37 ms, plus about 65 ms for the `bin/agent-ledger` shell shim. Most of it is `git` subprocesses. Every hook has a 10 s timeout and exits 0 on any error.

## Commands

| Command | What it does |
|---|---|
| `agent-ledger decide "<decision>" "<because>" "<rejected>"` | Append a decision (the `record-decision` skill tells the agent how) |
| `agent-ledger status` | Show the ledger dir, pin count and token estimate |
| `agent-ledger config` | Effective settings and where each came from |
| `agent-ledger preview [source]` | Dry run of SessionStart (default `compact`) |
| `agent-ledger inject` | Print the ledger as context (for CLIs without hooks) |
| `agent-ledger hook <user-prompt-submit\|session-start\|session-end>` | Hook entry points; read the hook JSON on stdin, always exit 0 |

## Configuration

Layers, later wins: defaults < user `~/.config/agent-ledger/config.json` (`%APPDATA%` on Windows) < repo `.agent-ledger.json` (commit it to share with the team) < env `AGENT_LEDGER_<KEY>`.

```json
{ "key": "session", "clear": "drop", "prune_days": 30, "recent_prompts": 5, "decisions": 10, "budget_chars": 9000,
  "logging": "all", "redact": "on" }
```

| Key | Values | Meaning |
|---|---|---|
| `key` | `session` (default), `branch`, `repo` | Who shares a ledger: one chat, everyone on a branch, or the whole repo |
| `clear` | `drop`, `keep` | On `/clear`: drop pins and show them, or re-inject pins and decisions. Default `drop` for `session`, `keep` otherwise |
| `prune_days` | 0-36500 | Delete ledgers idle this long at startup; 0 = never |
| `recent_prompts`, `decisions` | 0-100, 0-200 | How many are re-injected after compact |
| `budget_chars` | 1000-400000 | Injection budget (default 9000, under the ~10k-char hook-context caps of Claude Code and Codex). Prompts, then decisions are dropped to fit; long decisions are cut. Pins are never cut; you get a warning when they alone exceed it |
| `logging` | `all` (default), `pins-only`, `off` | Which prompts are stored; pins are always kept |
| `redact` | `on` (default), `off` | Mask API keys, tokens, private keys, JWTs, URL passwords and `password=`-style values before storing |
| `promote_to` | a file name like `RULES.md` | Target of `PROMOTE:` (top of the work tree; no paths) |

Bad files, unknown keys and invalid values are ignored; `agent-ledger config` shows each effective value, where it came from and any problem. `agent-ledger preview [compact|clear|startup|resume]` shows what the next SessionStart would inject, drop or prune, without doing it.

`clear: keep` with `key: session` only helps if `/clear` keeps the session id (unverified).

## Keying

With `key: session` (default):

1. The hook's `session_id` (hooks), else `CLAUDE_SESSION_ID` (written to `CLAUDE_ENV_FILE` at session start), else `CLAUDE_CODE_SESSION_ID`, else Codex's `CODEX_THREAD_ID`.
2. With no session id, the current git branch (`detached` for detached HEAD), so manual runs and other CLIs still work. Branch ledgers are named `@b-<branch>-<hash>`, so `feat/x` and `feat_x` stay apart.

After `/clear` with a new session id, the pins are handed over only when exactly one chat in this folder was cleared in the last 10 minutes; otherwise you are told, and nothing is guessed.

Set `AGENT_LEDGER_DEBUG=1` to log every hook's input to `hooks.debug.log` in the repo ledger dir.

## Build

```
go test ./...
go build -o bin/agent-ledger-$(go env GOOS)-$(go env GOARCH) ./cmd/agent-ledger
scripts/release.sh   # all 6 targets + checksums.txt into dist/
```

Releases: bump `VERSION` and `.claude-plugin/plugin.json`, then push tag `v<VERSION>`; `release.yml` builds and uploads.

## License

MIT, see [LICENSE](LICENSE).
