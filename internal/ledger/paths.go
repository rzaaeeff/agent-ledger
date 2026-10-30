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

// uniqueSuffix is 6 random bytes in hex, or "" if the system has no randomness,
// in which case the caller keeps the name it already had.
func uniqueSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// MarkerFile, in the repo's git common dir, names that repo's ledger directory.
// It is what makes the ledger survive a move or a rename: without it the name is
// derived from the repo's path, so moving the repo silently started a new ledger
// and left the old pins behind. Git ignores the common dir, so the marker never
// shows up in git status and is never carried by a clone.
const MarkerFile = "agent-ledger-id"

// OwnerFile, in the ledger directory, marks the ledger as claimed by a repo that
// carries a marker. A repo with no marker that lands on a claimed name is a
// different repo reusing an old path, not the owner coming back, so it gets its
// own ledger instead of the other repo's pins. The file holds the repo's path at
// the time of the claim, which is a hint for a human reading the store and
// nothing more: the path goes stale the moment the repo moves.
const OwnerFile = "claimed-by"

// pathName is the pre-marker ledger name: the repo name plus a hash of its path.
// Still the name every new ledger starts with, so a build without marker support
// keeps reading the same directory.
func pathName(common string) string {
	name := filepath.Base(filepath.Dir(common))
	if filepath.Base(common) != ".git" {
		name = filepath.Base(common)
	}
	key := common
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(key)
	}
	sum := sha1.Sum([]byte(key))
	return Safe(name, 60, false) + "-" + hex.EncodeToString(sum[:])[:6]
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

// claim records name as this repo's ledger and this repo as the ledger's owner.
// Both writes are best effort: a read-only common dir or store costs the rename
// resistance, which is worth less than the pin the user is trying to save.
func claim(common, name string) {
	_ = os.WriteFile(filepath.Join(common, MarkerFile), []byte(name+"\n"), 0o600)
	dir := filepath.Join(StateHome(), name)
	if err := MkdirPrivate(dir); err == nil {
		owner := filepath.Join(dir, OwnerFile)
		if _, err := os.Stat(owner); err != nil {
			_ = os.WriteFile(owner, []byte(common+"\n"), 0o600)
		}
	}
}

// claimed reports whether some repo with a marker already owns the ledger at
// name. Reached only when this repo has no marker, so the owner is somebody
// else. A ledger with no claim file was written before markers existed and
// belongs to whoever is at this path now, which is the upgrade path.
func claimed(name string) bool {
	_, err := os.Stat(filepath.Join(StateHome(), name, OwnerFile))
	return err == nil
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
		return "", ErrNoRepo
	}
	common = filepath.Clean(common)
	if real, err := filepath.EvalSymlinks(common); err == nil {
		common = real
	}
	// A marker written on first use wins, so the ledger follows the repo when it
	// moves. Without one, fall back to the path-derived name and record it.
	if name := readMarker(common); name != "" {
		return filepath.Join(StateHome(), name), nil
	}
	name := pathName(common)
	if claimed(name) {
		// Another repo's ledger sits at that name, so take a fresh one.
		if u := uniqueSuffix(); u != "" {
			name += "-" + u
		}
	}
	claim(common, name)
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
	if sid == "" {
		b, _ := git(cwd, "branch", "--show-current")
		if b == "" {
			b = "detached"
		}
		return filepath.Join(root, BranchLedgerKey(b)), nil
	}
	return filepath.Join(root, SessionKey(sid)), nil
}
