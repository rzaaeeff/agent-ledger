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
	"time"
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
	Short string // where a pin lives, in PINS? and UNPIN replies
}

// PinScopes lists the branch, repo and global pin stores for cwd, skipping any
// that is the current ledger dir itself (key=branch or key=repo).
func PinScopes(cwd, dir string) []Scope {
	var out []Scope
	root := filepath.Dir(dir)
	if b, _ := git(cwd, "branch", "--show-current"); b != "" {
		out = append(out, Scope{ScopeBranch, "Pinned for branch " + b + " (all sessions)", filepath.Join(root, branchKey(b)), "branch " + b})
	}
	out = append(out,
		Scope{ScopeRepo, "Pinned for this repo (all sessions and branches)", filepath.Join(root, RepoKey), "this repo"},
		Scope{ScopeGlobal, "Pinned for every repo", GlobalDir(), "every repo"})
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
// The hash of the raw name keeps branches that sanitise alike (feat/x, feat_x) apart.
func branchKey(b string) string {
	sum := sha1.Sum([]byte(b))
	return "@branch-" + Safe(b, 80, true) + "-" + hex.EncodeToString(sum[:])[:6]
}

// BranchLedgerKey is the dir of a branch-keyed ledger (key=branch, or no session
// id): "@b-" keeps it apart from session ids (Safe never produces "@") and from
// PIN@branch stores; the hash keeps feat/x and feat_x apart. Idle-pruned as usual.
func BranchLedgerKey(b string) string {
	sum := sha1.Sum([]byte(b))
	return "@b-" + Safe(b, 80, true) + "-" + hex.EncodeToString(sum[:])[:6]
}

// BranchNameFile in a @branch-* store holds the raw branch name, so Prune can
// tell whether the branch still exists.
const BranchNameFile = "branch"

// HandledFile in a ledger marks that SessionStart(clear) handled a /clear there;
// a SessionEnd(clear) arriving just after then does nothing (no orphan note).
const HandledFile = "clear_handled"

func markHandled(dir string) {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		_ = os.WriteFile(filepath.Join(dir, HandledFile), nil, 0o600)
		now := Now()
		_ = os.Chtimes(filepath.Join(dir, HandledFile), now, now)
	}
}

func handledRecently(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, HandledFile))
	return err == nil && Now().Sub(fi.ModTime()) < 10*time.Second && Now().Sub(fi.ModTime()) > -10*time.Second
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
	scope string // "" (session) | branch | repo | global, or an unknown word
	ref   string // PIN [ref]: rewords the pin ref selects
	text  string
	typed string // the verb as typed, e.g. "pin" (the scope may be lower case anyway)
}

// The ! may come before or after the scope, and a full-width colon counts too.
var pinRe = regexp.MustCompile(`(?i)^(PIN|CARRY|UNPIN|PROMOTE)(!)?(?:[ \t]*@([a-z]+))?(!)?(?:[ \t]*\[([^\]\n]*)\])?[ \t]*[:：]`)

// Look-alikes of a command that are not one: a wrong separator ("PIN; x",
// "pin = x"), or the keyword in capitals without the colon ("PIN x").
var (
	nearRe = regexp.MustCompile(`(?i)^(PIN|CARRY|UNPIN|PROMOTE)(!)?(@[a-z]+)?[ \t]*[;=]`)
	capsRe = regexp.MustCompile(`^(PIN|CARRY|UNPIN|PROMOTE)(!)?(@[a-z]+)?[ \t]+[^\s@]`)
)

// nearMiss is the hint for a prompt that looks like a command but is not one, or "".
func nearMiss(prompt string) string {
	p := strings.TrimLeft(prompt, " \t\r\n")
	m := nearRe.FindStringSubmatch(p)
	if m == nil {
		m = capsRe.FindStringSubmatch(p)
	}
	if m == nil {
		return ""
	}
	want := strings.ToUpper(m[1]) + m[2] + strings.ToLower(m[3]) + ":"
	return fmt.Sprintf("ledger: nothing saved; this went to the agent as a normal prompt. Commands need the colon: %s ... (LEDGER? lists them)", want)
}

// parsePin recognises PIN:, PIN!:, PIN@<scope>:, PIN [ref]:, CARRY:, UNPIN: and
// PROMOTE: at the start of a prompt, after any spaces, in any case. Only the start
// counts, so prose mentioning "pin:" is left alone. The command's text is its
// first line; the rest of the prompt is a normal prompt for the agent.
func parsePin(prompt string) (pinCmd, bool) {
	prompt = strings.TrimLeft(prompt, " \t\r\n")
	m := pinRe.FindStringSubmatch(prompt)
	if m == nil {
		return pinCmd{}, false
	}
	text := prompt[len(m[0]):]
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	return pinCmd{verb: strings.ToUpper(m[1]), keep: m[2] != "" || m[4] != "", scope: strings.ToLower(m[3]), ref: strings.TrimSpace(m[5]),
		text: strings.TrimSpace(text), typed: m[1]}, true
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

// uniqueID is id, or id plus 6 more hex digits (from id and text) when
// another text in file already has id: 6 hex digits collide now and then.
func uniqueID(file, id, text string) string {
	for _, p := range ReadJSONL[Pin](file) {
		if p.ID == id && normText(p.Text) != normText(text) {
			sum := sha1.Sum([]byte(id + "\x00" + text))
			return id + hex.EncodeToString(sum[:])[:6]
		}
	}
	return id
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
// "scoped pins: branch feat 1 | repo 2 | global 0", or "" when there are none.
func ScopeStatus(cwd, dir string) string {
	var parts []string
	total := 0
	for _, s := range PinScopes(cwd, dir) {
		name := s.Name
		if s.Name == ScopeBranch {
			b, _ := git(cwd, "branch", "--show-current")
			name += " " + b
		}
		n := len(ReadJSONL[Pin](filepath.Join(s.Dir, PinnedFile)))
		total += n
		parts = append(parts, fmt.Sprintf("%s %d", name, n))
	}
	if total == 0 {
		return ""
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
		if err := retryBusy(func() error { return os.Remove(path) }); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeReplace(path, []byte(strings.Join(lines, "\n")+"\n"))
}
