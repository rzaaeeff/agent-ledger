## What this changes

<!-- The user-visible behaviour, not the diff. For a bug, what someone saw. -->

## How it was checked

<!-- Tests, a real session, a built binary. "Not checked" is a fine answer; say so. -->

## Before merging

- [ ] `go test ./...` passes, and a behaviour change or bug fix has a test in `cmd/agent-ledger/main_test.go` that fails without it
- [ ] CI was run on all three platforms: `gh workflow run ci.yml --ref <branch>` (merging runs it anyway if not, before releasing)
- [ ] `VERSION` and `.claude-plugin/plugin.json` are bumped together, **or** this change is not meant to ship yet
- [ ] The README says only what a test or a run backs, and no section claims more than was verified
