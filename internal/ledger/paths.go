// Package ledger keeps a per-session store of the user's prompts, pinned rules and
// session decisions outside git, and turns it into hook output for agent CLIs.
package ledger

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNoRepo means the directory is not inside a git repository.
var ErrNoRepo = errors.New("not in a git repository")

// Safe keeps the bytes [A-Za-z0-9._-] of s, replacing every other byte with '_', so
// the result is always one directory name inside the store. With stripDots it also
// removes leading dots, so "." and ".." can never be produced. Cut to max bytes.
func Safe(s string, max int, stripDots bool) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	out := string(b)
	if stripDots {
		out = strings.TrimLeft(out, ".")
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// StateHome is the base of all ledgers: $XDG_STATE_HOME/agent-ledger, else
// %LOCALAPPDATA%\agent-ledger on Windows, else ~/.local/state/agent-ledger.
func StateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "agent-ledger")
	}
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "agent-ledger")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "agent-ledger")
}

func git(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\r\n"), err
}

// Root is the store of the repo containing cwd, shared by all its worktrees: keyed
// by the repo name so two repos never mix.
func Root(cwd string) (string, error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	common, err := git(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return "", ErrNoRepo
	}
	common = filepath.Clean(common)
	if real, err := filepath.EvalSymlinks(common); err == nil {
		common = real
	}
	// The hash of the repo's path keeps same-named repos (and all submodules, whose
	// common dir is .git/modules/<name>) apart; the name keeps the dir readable.
	name := filepath.Base(filepath.Dir(common))
	if filepath.Base(common) != ".git" {
		name = filepath.Base(common)
	}
	key := common
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(key)
	}
	sum := sha1.Sum([]byte(key))
	repo := Safe(name, 60, false) + "-" + hex.EncodeToString(sum[:])[:6]
	return filepath.Join(StateHome(), repo), nil
}

// SessionFromEnv is the session id for callers without hook JSON (decide, status):
// $CLAUDE_SESSION_ID, which the SessionStart hook exports from the hook JSON through
// $CLAUDE_ENV_FILE, then the undocumented $CLAUDE_CODE_SESSION_ID, then Codex's
// $CODEX_THREAD_ID (the same id its hooks get as session_id).
func SessionFromEnv() string {
	for _, k := range []string{"CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Dir is the ledger for cwd and session sid under the config of cwd; see DirFor.
func Dir(cwd, sid string) (string, error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return DirFor(LoadConfig(cwd), cwd, sid)
}

// DirFor is the ledger in the repo of cwd chosen by cfg.Key:
//
//	session  session sid (default: SessionFromEnv). The branch and worktree do not
//	         matter, so one chat keeps one ledger when it enters a worktree. Without
//	         a session id (Codex, manual runs) it falls back to the branch.
//	branch   the branch of cwd ("detached" for a detached HEAD).
//	repo     one ledger (RepoKey) for the whole repo.
func DirFor(cfg Config, cwd, sid string) (string, error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	root, err := Root(cwd)
	if err != nil {
		return "", err
	}
	switch cfg.Key {
	case KeyRepo:
		return filepath.Join(root, RepoKey), nil
	case KeyBranch:
		sid = ""
	default:
		if sid == "" {
			sid = SessionFromEnv()
		}
	}
	key := sid
	if key == "" {
		key, _ = git(cwd, "branch", "--show-current")
		if key == "" {
			key = "detached"
		}
	}
	key = Safe(key, 100, true)
	if key == "" {
		key = "unnamed"
	}
	return filepath.Join(root, key), nil
}
