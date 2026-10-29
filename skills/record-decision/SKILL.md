---
name: record-decision
description: Record a design or process decision in the agent ledger so it survives compaction. Use when you and the user settle a non-obvious choice, especially one with rejected alternatives.
allowed-tools: Bash(agent-ledger decide:*)
---

Run, from the project directory, with literal arguments:

```
agent-ledger decide "<decision>" "<because>" "<rejected alternatives>"
```

If `agent-ledger` is not on the PATH (Claude Code before 2.1.91, or Codex), use `"<this skill's base directory>/../../bin/agent-ledger"` instead.

- Keep each argument to one sentence.
- Use `-` for "rejected" when nothing was rejected.
- Record only settled decisions, not proposals.
- After compaction the last 10 decisions are re-injected, so don't repeat them in prose.
