// Command agent-ledger keeps the user's pinned rules, and recent prompts
// across context compaction. Run as Claude Code hooks, or by hand:
//
//	agent-ledger hook user-prompt-submit|session-start|session-end   (hook JSON on stdin)
//	agent-ledger inject                                              (print the ledger, e.g. in Codex)
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
  agent-ledger status
  agent-ledger config
  agent-ledger preview [compact|clear|startup|resume]
  agent-ledger version`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func warnFallback(stderr io.Writer, dir string) {
	if ledger.LoadConfig("").Key == ledger.KeySession && ledger.SessionFromEnv() == "" {
		// Outside a repository there is no branch to fall back to, so the folder's
		// sessionless chats share one ledger instead.
		which := "the branch ledger"
		if ledger.IsFolder(filepath.Dir(dir)) {
			which = "the ledger shared by this folder"
		}
		fmt.Fprintln(stderr, "ledger: no session id (CLAUDE_CODE_SESSION_ID, CLAUDE_SESSION_ID and CODEX_THREAD_ID unset); using "+which)
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
		case "user-prompt-expansion":
			ledger.HookUserPromptExpansion(h, stdout)
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
	case "status":
		dir, err := ledger.Dir("", "")
		if err != nil {
			// A folder with no repository has its own ledger, so this is git
			// refusing a folder that does look like one.
			fmt.Fprintln(stdout, "ledger: unavailable here: "+err.Error())
			return 0
		}
		warnFallback(stderr, dir)
		cwd, _ := os.Getwd()
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			fmt.Fprintln(stdout, "ledger: empty")
		} else {
			fmt.Fprintln(stdout, ledger.Status(dir))
		}
		if s := ledger.ScopeStatus(cwd, dir); s != "" {
			fmt.Fprintln(stdout, s)
		}
		// Which folder, always: it is how you tell two ledgers apart, and the only
		// way to see that a repo fell back to being known by its path.
		fmt.Fprintln(stdout, "folder: "+dir)
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
		pdir, _ := ledger.Dir(cwd, "")
		warnFallback(stderr, pdir)
		fmt.Fprintln(stdout, text)
		return 0
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "agent-ledger", version)
		return 0
	}
	fmt.Fprintln(stderr, usage)
	return 2
}
