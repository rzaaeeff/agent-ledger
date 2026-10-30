# Security

## Reporting a vulnerability

Report privately through GitHub: **Security → Report a vulnerability** on
https://github.com/rzaaeeff/agent-ledger. Please do not open a public issue for
anything exploitable.

Expect a first reply within 7 days. This is a one-person project, so a fix may
take longer; you will be told where it stands.

## What this plugin can reach

A hook runs on every prompt you send and on every session start, with your user
account's permissions. It:

- reads the text of each prompt, and stores lines that start with a ledger
  command (plus, only if you set `"logging": "all"`, every prompt);
- writes under `~/.local/state/agent-ledger/` (or `$XDG_STATE_HOME`), with files
  `0600` and directories `0700`;
- writes to `CLAUDE.md` or `AGENTS.md` inside the repo, but only when you send
  `PROMOTE:`;
- deletes ledgers in the current repo that nothing has touched for `prune_days`
  (default 30);
- makes network calls for one purpose only: fetching its own binary and that
  release's `checksums.txt` from this repo's GitHub release on first use,
  retried on failure after 5 minutes and then hourly until it succeeds.
  Nothing is ever sent anywhere.

Secrets are masked by pattern before anything is written. Pattern matching
misses things; treat the ledger files as sensitive.

A repository's `.agent-ledger.json` is read from whatever you have checked out,
so it is treated as untrusted: it may only make `logging`, `redact`, `key` and
`prune_days` stricter than your own settings, and `promote_to` may only be
`AGENTS.md` or `CLAUDE.md`.

## The binary download

The shim downloads a prebuilt binary and checks its SHA-256 against
`checksums.txt` from the same release. That catches a corrupted or truncated
download. It does **not** protect you against a compromised release, because the
checksum file ships beside the binary.

If that matters to you, build it yourself and skip the download:

```
go build -o /usr/local/bin/agent-ledger ./cmd/agent-ledger
export AGENT_LEDGER_BIN=/usr/local/bin/agent-ledger
```

Releases are built by GitHub Actions from a tag on `master`, published with
build provenance attestation, and are immutable. You can verify a downloaded
binary with:

```
gh attestation verify agent-ledger-linux-amd64 --repo rzaaeeff/agent-ledger
```

## Supported versions

Only the latest release gets fixes.
