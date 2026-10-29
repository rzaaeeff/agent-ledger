// Command agent-ledger keeps the user's pinned rules, prompts and session decisions
// across context compaction. Run as Claude Code hooks, or by hand:
//
//	agent-ledger hook user-prompt-submit|session-start|session-end   (hook JSON on stdin)
//	agent-ledger inject                                              (print the ledger, e.g. in Codex)
//	agent-ledger decide "<decision>" "<because>" "<rejected>"
//	agent-ledger status
//	agent-ledger config                                              (effective settings and their source)
//	agent-ledger preview [compact|clear|startup|resume]              (dry run of SessionStart)
//	agent-ledger version
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rzaaeeff/agent-ledger/internal/ledger"
)

var version = "dev"

const usage = `usage:
  agent-ledger hook user-prompt-submit|session-start|session-end   (hook JSON on stdin)
  agent-ledger inject
  agent-ledger decide "<decision>" "<because>" "<rejected>"
  agent-ledger status
  agent-ledger config
  agent-ledger preview [compact|clear|startup|resume]
  agent-ledger version`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func warnFallback(stderr io.Writer) {
	if ledger.LoadConfig("").Key == ledger.KeySession && ledger.SessionFromEnv() == "" {
		fmt.Fprintln(stderr, "ledger: no session id (CLAUDE_SESSION_ID and CLAUDE_CODE_SESSION_ID unset); using the branch ledger")
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "hook":
		// A hook must never fail or block the agent: always exit 0, never write stderr.
		defer func() { _ = recover() }()
		if len(args) < 2 {
			return 0
		}
		h := ledger.ReadHookInput(stdin)
		switch args[1] {
		case "user-prompt-submit":
			ledger.HookUserPromptSubmit(h, stdout)
		case "session-start":
			ledger.HookSessionStart(h, stdout)
		case "session-end":
			ledger.HookSessionEnd(h)
		}
		return 0
	case "inject":
		defer func() { _ = recover() }()
		ledger.Inject(stdout)
		return 0
	case "decide":
		if len(args) != 4 {
			fmt.Fprintln(stderr, `usage: agent-ledger decide "<decision>" "<because>" "<rejected>"`)
			return 2
		}
		dir, err := ledger.Dir("", "")
		if err != nil {
			fmt.Fprintln(stderr, "decide: not in a git repository")
			return 1
		}
		warnFallback(stderr)
		path := filepath.Join(dir, ledger.DecisionsFile)
		if err := ledger.MkdirPrivate(dir); err == nil {
			err = ledger.Append(path, ledger.Marshal(ledger.Decision{
				TS: ledger.Now().Format("2006-01-02T15:04:05-07:00"), Decision: args[1], Because: args[2], Rejected: args[3]}))
		}
		if err != nil {
			fmt.Fprintln(stderr, "decide:", err)
			return 1
		}
		fmt.Fprintln(stdout, "decide: recorded in", path)
		return 0
	case "status":
		dir, err := ledger.Dir("", "")
		if err != nil {
			fmt.Fprintln(stdout, "ledger: not in a git repository")
			return 0
		}
		warnFallback(stderr)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			fmt.Fprintf(stdout, "ledger: empty (%s)\n", dir)
			return 0
		}
		fmt.Fprintln(stdout, ledger.Status(dir))
		return 0
	case "config":
		cwd, _ := os.Getwd()
		fmt.Fprintln(stdout, ledger.LoadConfig(cwd).Describe())
		fmt.Fprintln(stdout, "files: user", ledger.UserConfigPath()+", repo", ledger.RepoConfigPath(cwd))
		return 0
	case "preview":
		source := "compact"
		if len(args) > 1 {
			source = args[1]
		}
		cwd, _ := os.Getwd()
		cfg := ledger.LoadConfig(cwd)
		text, err := ledger.Preview(cfg, cwd, "", source)
		if err != nil {
			fmt.Fprintln(stderr, "preview:", err)
			return 2
		}
		warnFallback(stderr)
		fmt.Fprintln(stdout, text)
		return 0
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "agent-ledger", version)
		return 0
	}
	fmt.Fprintln(stderr, usage)
	return 2
}
