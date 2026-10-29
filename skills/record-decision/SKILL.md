---
name: record-decision
description: Record a design or process decision in the agent ledger so it survives compaction. Use when you and the user settle a non-obvious choice, especially one with rejected alternatives.
---

Run, from the project directory, with literal arguments:

```
"<this skill's base directory>/../../bin/agent-ledger" decide "<decision>" "<because>" "<rejected alternatives>"
```

- Keep each argument to one sentence.
- Use `-` for "rejected" when nothing was rejected.
- Record only settled decisions, not proposals.
- After compaction the last 10 decisions are re-injected, so don't repeat them in prose.
