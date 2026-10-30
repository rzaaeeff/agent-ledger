# Contributing

agent-ledger has one maintainer, so replies may take a few days.

## Reporting

- Bugs and pins that did not come back: use the issue templates. The output of `agent-ledger status` and `agent-ledger version` answers most first questions.
- Security problems: see [SECURITY.md](SECURITY.md). Please do not open a public issue.

## Changing code

```
go test ./...
go vet ./...
```

The behaviour is specified by `cmd/agent-ledger/main_test.go`, which feeds the hooks the JSON Claude Code and Codex send. A change in behaviour needs a test there; a bug fix needs a test that fails without it.

CI runs the tests on Linux, macOS and Windows. File locking and renames behave differently on Windows, so changes to `internal/ledger/files.go` or the lock files deserve extra care.

It does not run on every push. Start it when you want it:

```
gh workflow run ci.yml --ref <your branch>
gh run watch
```

Nothing can be released untested, though. Merging to `master` publishes whatever version `VERSION` names, and before it does, it looks for a successful `ci` run for that code, on the merge commit or on the pull request's head. If it finds none, it runs the full matrix itself and releases only if that passes. The Actions summary for the merge says which of the two happened, so a bypassed pull request is visible after the fact.

So: bump `VERSION` in a pull request when the change should ship, and merging releases it. Leave `VERSION` alone and the merge publishes nothing.

`AGENT_LEDGER_BIN=/path/to/binary` makes an installed plugin use your build. `AGENT_LEDGER_DEBUG=1` logs every hook input to `hooks.debug.log` in the repo's ledger folder.

## Docs

The README describes what was tested, not what should work. Please keep it that way: no claims a test or a live run does not back.
