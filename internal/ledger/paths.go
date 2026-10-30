// Package ledger keeps a per-session store of the user's pinned rules and prompts
// outside git, and turns it into hook output for agent CLIs.
package ledger

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
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

// MarkerFile, in the repo's git common dir, names that repo's ledger directory.
// It is what makes the ledger survive a move: the name used to be derived from
// the repo's path, so moving the repo silently started an empty ledger. Git does
// not show the common dir in git status, and git clone does not copy it, so a
// clone gets its own ledger.
const MarkerFile = "agent-ledger-id"

// newName is a fresh ledger name: the repo name, so the store stays readable, and
// 6 random bytes, so two repos never collide however they are named or moved.
func newName(common string) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return Safe(repoName(common), 60, false) + "-" + hex.EncodeToString(b[:]), nil
}

// fallbackName is used when the marker cannot be written: the repo name plus a
// hash of its path, which is stable without anything stored in the repo. It is
// the pre-marker scheme, so it moves with the repo's path rather than the repo.
func fallbackName(common string) string {
	key := common
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(key)
	}
	sum := sha1.Sum([]byte(key))
	return Safe(repoName(common), 60, false) + "-" + hex.EncodeToString(sum[:])[:6]
}

// repoName is the repo's directory name, or the common dir's own name for a
// submodule, whose common dir is .git/modules/<name>.
func repoName(common string) string {
	if filepath.Base(common) != ".git" {
		return filepath.Base(common)
	}
	return filepath.Base(filepath.Dir(common))
}

// FolderPrefix starts the ledger name of a folder that is not in a git
// repository. Safe never produces "@", so a repo can never take such a name.
const FolderPrefix = "@dir-"

// IsFolder reports whether root is the ledger of a plain folder rather than of a
// git repository. Branch pins and PROMOTE: have nothing to work with there.
func IsFolder(root string) bool {
	return strings.HasPrefix(filepath.Base(root), FolderPrefix)
}

// folderRoot is the ledger of a folder with no repository: the folder's own name
// plus a hash of its path. There is no marker, because the only place to put one
// is the folder itself, where it would be a file the user did not ask for and
// git would offer to commit. Moving such a folder starts a new ledger.
func folderRoot(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	key := abs
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(key)
	}
	sum := sha1.Sum([]byte(key))
	name := Safe(filepath.Base(abs), 60, true)
	if name == "" {
		name = "folder"
	}
	return filepath.Join(StateHome(), FolderPrefix+name+"-"+hex.EncodeToString(sum[:])[:6])
}

// readMarker is the ledger name recorded for this repo, or "" if there is none.
// Safe keeps a hand-edited or hostile marker to one directory inside the store.
func readMarker(common string) string {
	b, err := os.ReadFile(filepath.Join(common, MarkerFile))
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return Safe(strings.TrimSpace(line), 100, true)
}

// Root is the store of the repo containing cwd, shared by all its worktrees: keyed
// by the repo name so two repos never mix.
func Root(cwd string) (string, error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	common, err := git(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		// Keep git's reason (e.g. "detected dubious ownership"): it is not always "no repo".
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if line, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n"); line != "" && !strings.Contains(line, "not a git repository") {
				return "", fmt.Errorf("%w; git said: %s", ErrNoRepo, line)
			}
		}
		return folderRoot(cwd), nil
	}
	common = filepath.Clean(common)
	if real, err := filepath.EvalSymlinks(common); err == nil {
		common = real
	}
	// The marker written on first use is what the ledger is named after, so it
	// follows the repo when it moves.
	if name := readMarker(common); name != "" {
		return filepath.Join(StateHome(), name), nil
	}
	// A repo whose .git was read-only has been pinning to its fallback ledger. It
	// must keep those pins now that the marker can be written, so the marker adopts
	// that ledger rather than minting a name nothing points at.
	name := fallbackName(common)
	if _, serr := os.Stat(filepath.Join(StateHome(), name)); serr != nil {
		var nerr error
		if name, nerr = newName(common); nerr != nil {
			// No randomness. Identity must never be the reason a pin is refused, so
			// use the name that needs nothing stored in the repo.
			return filepath.Join(StateHome(), fallbackName(common)), nil
		}
	}
	if err := os.WriteFile(filepath.Join(common, MarkerFile), []byte(name+"\n"), 0o600); err != nil {
		// A read-only .git. The fallback needs nothing written down, and is the same
		// name the marker would have adopted, so the pins land in one place either way.
		name = fallbackName(common)
	}
	return filepath.Join(StateHome(), name), nil
}

// SessionFromEnv is the session id for callers without hook JSON (status, preview):
// Claude Code's $CLAUDE_CODE_SESSION_ID (set for its Bash tool since 2.1.132), then
// $CLAUDE_SESSION_ID, which the SessionStart hook exports through $CLAUDE_ENV_FILE
// (older versions could leave it stale), then Codex's $CODEX_THREAD_ID (the same id
// its hooks get as session_id).
func SessionFromEnv() string {
	for _, k := range []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "CODEX_THREAD_ID"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// SessionKey is the dir name of session sid's ledger: the id itself when it is
// already safe (UUIDs are), else the sanitised id plus a hash of the raw one, so
// two ids never share a dir.
func SessionKey(sid string) string {
	key := Safe(sid, 100, true)
	if key == sid {
		return key
	}
	sum := sha1.Sum([]byte(sid))
	return "id-" + Safe(key, 88, true) + "-" + hex.EncodeToString(sum[:])[:6]
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
	if sid == "" && IsFolder(root) {
		// No branch to fall back to outside a repository, so all of the folder's
		// sessionless chats share one ledger.
		return filepath.Join(root, RepoKey), nil
	}
	if sid == "" {
		b, _ := git(cwd, "branch", "--show-current")
		if b == "" {
			b = "detached"
		}
		return filepath.Join(root, BranchLedgerKey(b)), nil
	}
	return filepath.Join(root, SessionKey(sid)), nil
}
