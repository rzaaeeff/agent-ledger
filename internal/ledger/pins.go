package ledger

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Pin lifetimes (session pins only; scoped pins always outlive /clear).
const (
	LifeKeep  = "keep"  // PIN!: survives /clear
	LifeCarry = "carry" // CARRY: injected once, after the next /clear
)

// Pin scopes beyond the session.
const (
	ScopeBranch = "branch"
	ScopeRepo   = "repo"
	ScopeGlobal = "global"
)

// GlobalKey is the dir, directly under StateHome, of PIN@global pins; Safe never
// produces "@", so no repo store can collide with it.
const GlobalKey = "@global"

// GlobalDir holds the pins shown in every repo.
func GlobalDir() string { return filepath.Join(StateHome(), GlobalKey) }

// Scope is one pin store besides the current ledger.
type Scope struct {
	Name  string // branch | repo | global
	Label string // section title in the injected context
	Dir   string
}

// PinScopes lists the branch, repo and global pin stores for cwd, skipping any
// that is the current ledger dir itself (key=branch or key=repo).
func PinScopes(cwd, dir string) []Scope {
	var out []Scope
	root := filepath.Dir(dir)
	if b, _ := git(cwd, "branch", "--show-current"); b != "" {
		out = append(out, Scope{ScopeBranch, "Pinned for branch " + b + " (all sessions)", filepath.Join(root, branchKey(b))})
	}
	out = append(out,
		Scope{ScopeRepo, "Pinned for this repo (all sessions and branches)", filepath.Join(root, RepoKey)},
		Scope{ScopeGlobal, "Pinned for every repo", GlobalDir()})
	kept := out[:0]
	for _, s := range out {
		if filepath.Clean(s.Dir) != filepath.Clean(dir) {
			kept = append(kept, s)
		}
	}
	return kept
}

// branchKey is the dir of PIN@branch pins: apart from the branch-keyed ledgers
// (key=branch, or no session id), so those never surface as branch pins. Safe never
// produces "@".
func branchKey(b string) string {
	k := Safe(b, 100, true)
	if k == "" {
		k = "unnamed"
	}
	return "@branch-" + k
}

// DefaultBranches are the branch names PIN@branch refuses: origin's HEAD, main
// and master. Pins there would leak into every feature branch's sessions' view of
// the default branch, which must never accumulate pins.
func DefaultBranches(cwd string) map[string]bool {
	out := map[string]bool{"main": true, "master": true}
	if h, err := git(cwd, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && h != "" {
		out[strings.TrimPrefix(h, "origin/")] = true
	}
	return out
}

// pinCmd is a parsed PIN-family prompt.
type pinCmd struct {
	verb  string // PIN | CARRY | UNPIN
	keep  bool   // PIN!
	scope string // "" (session) | branch | repo | global
	text  string
}

var pinRe = regexp.MustCompile(`^(PIN|CARRY|UNPIN)(!)?(?:@(branch|repo|global))?:`)

// parsePin recognises PIN:, PIN!:, PIN@<scope>:, CARRY: and UNPIN: at the very start
// of a prompt (case-sensitive, so prose mentioning "pin:" is left alone).
func parsePin(prompt string) (pinCmd, bool) {
	m := pinRe.FindStringSubmatch(prompt)
	if m == nil {
		return pinCmd{}, false
	}
	return pinCmd{verb: m[1], keep: m[2] != "", scope: m[3], text: strings.TrimSpace(prompt[len(m[0]):])}, true
}

// pinID is sha1(text)[:6] for session pins (as before scopes existed) and
// sha1(scope NUL text)[:6] for scoped ones, so the same text pinned in two scopes
// still has two ids.
func pinID(scope, text string) string {
	key := text
	if scope != "" {
		key = scope + "\x00" + text
	}
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])[:6]
}

// unpinFiles are the pin files UNPIN searches, current ledger first.
func unpinFiles(cwd, dir string) []string {
	files := []string{filepath.Join(dir, PinnedFile)}
	for _, s := range PinScopes(cwd, dir) {
		files = append(files, filepath.Join(s.Dir, PinnedFile))
	}
	return files
}

func countMatches(path, q string) int {
	ids := map[string]bool{}
	for _, l := range readLines(path) {
		var p Pin
		if json.Unmarshal([]byte(l), &p) == nil && (p.ID == q || strings.Contains(p.Text, q)) {
			ids[p.ID] = true
		}
	}
	return len(ids)
}

// UnpinAny removes the one pin matching q across the current ledger and the
// branch, repo and global stores. It returns the total number of matching pins;
// nothing is removed unless it is 1.
func UnpinAny(cwd, dir, q string) (int, error) {
	total, at := 0, ""
	for _, f := range unpinFiles(cwd, dir) {
		if n := countMatches(f, q); n > 0 {
			total += n
			at = f
		}
	}
	if total != 1 {
		return total, nil
	}
	return Unpin(at, q)
}

// pinLines formats pins for the injected context; tag marks lifetimes.
func pinLines(pins []Pin, tag bool) []string {
	var out []string
	for _, p := range pins {
		line := fmt.Sprintf("- [%s] %s", p.ID, p.Text)
		if tag && p.Life == LifeKeep {
			line += " (kept across /clear)"
		}
		out = append(out, line)
	}
	return out
}

// sessionPins are the pins of dir that apply now: all but the CARRY ones, which
// wait for the next /clear.
func sessionPins(dir string) []Pin {
	var out []Pin
	for _, p := range ReadJSONL[Pin](filepath.Join(dir, PinnedFile)) {
		if p.Life != LifeCarry {
			out = append(out, p)
		}
	}
	return out
}

// scopedSections is the injected text for the branch, repo and global pins.
func scopedSections(cwd, dir string) string {
	var b strings.Builder
	for _, s := range PinScopes(cwd, dir) {
		if ls := pinLines(ReadJSONL[Pin](filepath.Join(s.Dir, PinnedFile)), false); len(ls) > 0 {
			fmt.Fprintf(&b, "\n### %s\n%s", s.Label, strings.Join(ls, "\n"))
		}
	}
	return b.String()
}

// ScopeStatus counts the pins of each scope besides dir, e.g.
// "scoped pins: branch feat 1 | repo 2 | global 0".
func ScopeStatus(cwd, dir string) string {
	var parts []string
	for _, s := range PinScopes(cwd, dir) {
		name := s.Name
		if s.Name == ScopeBranch {
			b, _ := git(cwd, "branch", "--show-current")
			name += " " + b
		}
		parts = append(parts, fmt.Sprintf("%s %d", name, len(ReadJSONL[Pin](filepath.Join(s.Dir, PinnedFile)))))
	}
	return "scoped pins: " + strings.Join(parts, " | ")
}

// takeCarry removes and returns the CARRY pins of dir.
func takeCarry(dir string) []Pin {
	path := filepath.Join(dir, PinnedFile)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	var carry []Pin
	_ = withLock(path, func() error {
		var keep []string
		for _, l := range readLines(path) {
			var p Pin
			if json.Unmarshal([]byte(l), &p) == nil && p.Life == LifeCarry {
				carry = append(carry, p)
				continue
			}
			keep = append(keep, l)
		}
		if len(carry) == 0 {
			return nil
		}
		return writeLines(path, keep)
	})
	return carry
}

// writeLines replaces path with lines, removing it when there are none. Call under the lock.
func writeLines(path string, lines []string) error {
	if len(lines) == 0 {
		return os.Remove(path)
	}
	return writeReplace(path, []byte(strings.Join(lines, "\n")+"\n"))
}
