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

`AGENT_LEDGER_BIN=/path/to/binary` makes an installed plugin use your build. `AGENT_LEDGER_DEBUG=1` logs every hook input to `hooks.debug.log` in the repo's ledger folder.

## Docs

The README describes what was tested, not what should work. Please keep it that way: no claims a test or a live run does not back.
