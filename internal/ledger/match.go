package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// stopWords are ignored when comparing pins, so "the"/"a" don't make two rules
// look alike or stop a match.
var stopWords = map[string]bool{"a": true, "an": true, "the": true, "and": true, "or": true, "to": true,
	"of": true, "in": true, "on": true, "for": true, "is": true, "it": true, "be": true, "this": true,
	"that": true, "with": true, "at": true, "by": true, "as": true, "are": true}

// normWords lowercases s and splits it on anything but letters and digits.
func normWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func normText(s string) string { return strings.Join(normWords(s), " ") }

// contentWords is the set of words of s without stop words (all words if only
// stop words are left). minLen 2 also drops single letters ("t" of "don't"),
// for comparing pins; matching a query keeps them ("alpha x" is not "alpha y").
func contentWords(s string, minLen int) map[string]bool {
	all := normWords(s)
	out := map[string]bool{}
	for _, w := range all {
		if !stopWords[w] && len([]rune(w)) >= minLen {
			out[w] = true
		}
	}
	if len(out) == 0 {
		for _, w := range all {
			out[w] = true
		}
	}
	return out
}

// pinMatches is how UNPIN:/PROMOTE: find a pin: its id, or all the query's
// words in any order, ignoring case and punctuation. Words match whole, or as the
// start of a pin word when 4+ letters long ("migration" finds "migrations"), so
// "all" never matches "called". Queries under 3 letters match only an id.
func pinMatches(p Pin, q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return false
	}
	if strings.EqualFold(p.ID, strings.Trim(q, "[]")) {
		return true
	}
	if nq := normText(q); nq != "" && nq == normText(p.Text) {
		return true
	}
	if len([]rune(strings.ReplaceAll(normText(q), " ", ""))) < 3 {
		return false
	}
	pw := normWords(p.Text)
	for w := range contentWords(q, 1) {
		if !hasWord(pw, w) {
			return false
		}
	}
	return true
}

func hasWord(words []string, w string) bool {
	for _, x := range words {
		if x == w || (len([]rune(w)) >= 4 && strings.HasPrefix(x, w)) {
			return true
		}
	}
	return false
}

// similarity is the word overlap (Jaccard) of two texts' content words, 0..1.
func similarity(a, b string) float64 {
	both, wa, wb := overlap(a, b)
	if wa == 0 || wb == 0 {
		return 0
	}
	return float64(both) / float64(wa+wb-both)
}

func overlap(a, b string) (both, na, nb int) {
	wa, wb := contentWords(a, 2), contentWords(b, 2)
	for w := range wa {
		if wb[w] {
			both++
		}
	}
	return both, len(wa), len(wb)
}

// looksSimilar reports whether a new pin looks like a reworded existing one: at least
// two shared words, and most words in common overall or 80% of the shorter
// one's. "don't touch the migrations, schema frozen until Friday" vs "don't touch
// migrations until Monday" is similar; vs "run migrations first" it is not.
func looksSimilar(a, b string) bool {
	both, na, nb := overlap(a, b)
	if both < 2 {
		return false
	}
	return float64(both)/float64(na+nb-both) >= 0.6 || float64(both)/float64(min(na, nb)) >= 0.8
}

// located is a pin with the store it lives in.
type located struct {
	Pin
	File  string
	Where string
	Scope string // "" for the current ledger, else the store's scope
}

// listPins is every pin that applies in cwd, in the order PINS? numbers them:
// the current ledger, then the branch, repo and global stores. here names the
// current ledger ("this chat" for key=session).
func listPins(cwd, dir, here string) []located {
	var out []located
	f := filepath.Join(dir, PinnedFile)
	for _, p := range ReadJSONL[Pin](f) {
		w := here
		switch p.Life {
		case LifeKeep:
			w += ", kept across /clear"
		case LifeCarry:
			w = "CARRY, for the chat after /clear"
		}
		out = append(out, located{p, f, w, ""})
	}
	for _, s := range PinScopes(cwd, dir) {
		f := filepath.Join(s.Dir, PinnedFile)
		for _, p := range ReadJSONL[Pin](f) {
			out = append(out, located{p, f, s.Short, s.Name})
		}
	}
	return out
}

// pinNumber reports whether q is a pin number ("1".."999"), as PINS? shows them.
func pinNumber(q string) (int, bool) {
	q = strings.TrimSpace(q)
	n, err := strconv.Atoi(q)
	return n, err == nil && len(q) <= 3 && strings.Trim(q, "0123456789") == ""
}

// resolveRef turns the shortcuts "last" (the newest pin) and "N" (the Nth pin
// of PINS?) into that pin's id; anything else is returned unchanged. A number
// out of range is the caller's to refuse, never matched as text.
func resolveRef(q string, pins []located) string {
	q = strings.TrimSpace(q)
	if strings.EqualFold(q, "last") && len(pins) > 0 {
		best, at := 0, time.Time{}
		for i, p := range pins {
			// RFC3339Nano also parses the older second-resolution stamps.
			if t, err := time.Parse(time.RFC3339Nano, p.TS); err == nil && !t.Before(at) {
				best, at = i, t
			}
		}
		return pins[best].ID
	}
	if n, ok := pinNumber(q); ok && n >= 1 && n <= len(pins) {
		return pins[n-1].ID
	}
	return q
}

// matching is the pins q selects; one entry per store and id. Pins whose whole
// text is q win over pins that merely contain its words.
func matching(q string, pins []located) []located {
	var exact []located
	for _, p := range pins {
		if nq := normText(q); nq != "" && nq == normText(p.Text) {
			exact = append(exact, p)
		}
	}
	if len(exact) > 0 {
		pins = exact
	}
	var out []located
	seen := map[string]bool{}
	for _, p := range pins {
		if k := p.File + "\x00" + p.ID; pinMatches(p.Pin, q) && !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// closest is up to n pins sharing at least half the words of q, best first.
func closest(q string, pins []located, n int) []located {
	qw := contentWords(q, 2)
	type scored struct {
		located
		s float64
	}
	var all []scored
	for _, p := range pins {
		pw := contentWords(p.Text, 2)
		hit := 0
		for w := range qw {
			if pw[w] {
				hit++
			}
		}
		if len(qw) > 0 && float64(hit)/float64(len(qw)) >= 0.5 {
			all = append(all, scored{p, float64(hit)/float64(len(qw)) + similarity(q, p.Text)/10})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].s > all[j].s })
	var out []located
	for i := 0; i < len(all) && i < n; i++ {
		out = append(out, all[i].located)
	}
	return out
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func refLine(p located) string {
	return fmt.Sprintf("[%s] %q (%s)", p.ID, short(p.Text, 60), p.Where)
}

func refList(ps []located) string {
	var out []string
	for _, p := range ps {
		out = append(out, refLine(p))
	}
	return strings.Join(out, "; ")
}

// pinAge is how long ago a pin was made, for PINS?. Empty when the pin has no
// timestamp (0.1.x wrote none) or was made today, where an age tells you nothing.
func pinAge(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	switch d := int(Now().Sub(t).Hours() / 24); {
	case d < 1:
		return ""
	case d == 1:
		return "1 day"
	default:
		return fmt.Sprintf("%d days", d)
	}
}

// pinVerb and pinScope are the pin's own command, as the user would type it. The
// list is deliberately the vocabulary of the commands rather than a description
// of them, so it teaches what to type next. The current ledger is "@chat"; a
// scoped pin outlives /clear by being scoped, so only a chat pin is marked "!".
func pinVerb(p located) string {
	switch {
	case p.Life == LifeCarry:
		return "carry"
	case p.Life == LifeKeep && p.Scope == "":
		return "pin!"
	}
	return "pin"
}

func pinScope(p located) string {
	if p.Scope == "" {
		return "@chat"
	}
	return "@" + p.Scope
}

// shortAge is the age in the smallest form that is still unambiguous next to
// nothing else numeric. It sits with the rest of the bookkeeping rather than
// after the text, where it would read as the user's own words.
func shortAge(ts string) string {
	if a := pinAge(ts); a != "" {
		return strings.TrimSuffix(strings.TrimSuffix(a, " days"), " day") + "d"
	}
	return ""
}

// PinsList is the text PINS? shows the user (never the model).
func PinsList(pins []located, codex bool) string {
	if len(pins) == 0 {
		cmd := "/pin"
		if codex {
			cmd = "$pin"
		}
		return "No pins here. Start a prompt with PIN: (or send " + cmd + " <text>) to add one."
	}
	// What a pin is comes before what it says: you read this list to decide what
	// to remove, and the verb and scope are what you decide on. The id is a handle
	// you type afterwards, so it goes last inside the brackets, and the age with
	// it: both are bookkeeping, and after the text they would read as the user's
	// own words. Every column is padded to the widest value actually present, so
	// a short list stays tight and nothing is padded for a case that is not here.
	// The branch name is in the legend rather than in a tag, because PINS? only
	// ever lists the branch you are on; without that the text would start too far
	// right to survive a narrow screen.
	verbs, scopes, ages := make([]string, len(pins)), make([]string, len(pins)), make([]string, len(pins))
	var vw, sw, aw int
	runes := func(s string) int { return len([]rune(s)) }
	for i, p := range pins {
		verbs[i], scopes[i], ages[i] = pinVerb(p), pinScope(p), shortAge(p.TS)
		vw, sw, aw = max(vw, runes(verbs[i])), max(sw, runes(scopes[i])), max(aw, runes(ages[i]))
	}
	pad := func(s string, w int) string { return s + strings.Repeat(" ", w-runes(s)) }
	var cells []string
	for i, p := range pins {
		c := pad(verbs[i], vw) + " " + pad(scopes[i], sw) + " " + p.ID
		if aw > 0 {
			c = strings.TrimRight(c+" "+pad(ages[i], aw), " ")
		}
		cells = append(cells, "["+c+"]")
	}
	cw := 0
	for _, c := range cells {
		cw = max(cw, runes(c))
	}
	var b strings.Builder
	b.WriteString(pinsHeading(pins) + " (shown to you only; the agent did not see this prompt):")
	for i, p := range pins {
		fmt.Fprintf(&b, "\n%d. %s  %s", i+1, pad(cells[i], cw), p.Text)
	}
	for _, line := range pinLegend(pins, codex, aw > 0) {
		b.WriteString("\n" + line)
	}
	b.WriteString("\nUNPIN: <number, id, words or last> removes one.")
	return b.String()
}

// pinsHeading avoids calling a CARRY a pin: it is a note delivered once into the
// next chat and then deleted, which is the opposite of a rule that keeps coming
// back, and the two are already easy to mix up.
func pinsHeading(pins []located) string {
	for _, p := range pins {
		if p.Life == LifeCarry {
			return "Pins and carries"
		}
	}
	return "Pins"
}

// pinLegend explains the tags that are actually in the list, and nothing else. A
// tag is short because it is the command, so what the command does has to be said
// somewhere; saying it only when it applies keeps a one-pin list to one line.
func pinLegend(pins []located, codex, aged bool) []string {
	var keep, carry bool
	branch := ""
	for _, p := range pins {
		switch {
		case p.Life == LifeCarry:
			carry = true
		case p.Life == LifeKeep && p.Scope == "":
			keep = true
		}
		if p.Scope == ScopeBranch {
			branch = strings.TrimPrefix(p.Where, "branch ")
		}
	}
	var out []string
	if aged {
		out = append(out, "20d is how long ago a pin was made; today's pins have no age.")
	}
	if branch != "" {
		out = append(out, "@branch is "+branch+".")
	}
	switch {
	// Under Codex, /clear starts a chat it cannot link to the one it replaced, so
	// pin! and carry both end there and "survives /clear" would be a lie. Saying so
	// is CodexClearNote's job, which the caller appends; saying it twice is worse
	// than once.
	case codex:
	case keep && carry:
		out = append(out, "pin! survives /clear. carry is delivered once into the next chat, then removed.")
	case keep:
		out = append(out, "pin! survives /clear; a plain chat pin does not.")
	case carry:
		out = append(out, "carry is delivered once into the next chat, then removed.")
	}
	return out
}

// CodexClearNote warns that Codex starts a chat it cannot link to the one it
// replaced, so PIN! and CARRY items never arrive there. Empty when no listed
// pin depends on that handover.
func CodexClearNote(pins []located) string {
	for _, p := range pins {
		if p.Life == LifeKeep || p.Life == LifeCarry {
			return "\nNote: Codex starts a chat it cannot link to this one, so PIN! and CARRY items do not arrive after /clear. Use a scope that does: PIN@repo:, PIN@branch: or PIN@global:."
		}
	}
	return ""
}

// brief is the short state appended to PIN/UNPIN replies; CARRY items wait for
// the next /clear, so they are counted apart.
func brief(pins []located, codex bool) string {
	n, c := 0, 0
	for _, p := range pins {
		if p.Life == LifeCarry {
			c++
		} else {
			n++
		}
	}
	if c > 0 {
		// Under Codex nothing is waiting: /clear starts a chat it cannot link, so
		// the item stays in this ledger until the chat's ledger is pruned.
		if codex {
			return fmt.Sprintf("(%s; %d CARRY that Codex cannot hand over)", pinsApply(n), c)
		}
		return fmt.Sprintf("(%s; %d CARRY waiting for /clear)", pinsApply(n), c)
	}
	return "(" + pinsApply(n) + ")"
}

// pinBytes is the size of the pins that come back after compaction (not CARRY).
func pinBytes(pins []located) int {
	n := 0
	for _, p := range pins {
		if p.Life != LifeCarry {
			n += len(p.ID) + len(p.Text) + 6
		}
	}
	return n
}

func pinsApply(n int) string {
	if n == 1 {
		return "1 pin applies here"
	}
	return fmt.Sprintf("%d pins apply here", n)
}

func fileExists(p string) bool { _, err := os.Lstat(p); return err == nil }

// HelpText is what LEDGER? shows the user (never the model). In a folder with no
// repository it drops the two verbs that need one, rather than offering help that
// only ends in a refusal. Under Codex it says what PIN! and CARRY really do there:
// Codex starts a chat it cannot link, so neither survives /clear.
func HelpText(status string, folder, codex bool) string {
	scopes := "  PIN@branch: / PIN@repo: / PIN@global: <text>   every chat on the branch / in the repo / everywhere\n"
	promote := "  PROMOTE: <ref>          move a pin into CLAUDE.md or AGENTS.md (uncommitted)\n"
	if folder {
		scopes = "  PIN@repo: / PIN@global: <text>   every chat in this folder / everywhere\n"
		promote = ""
	}
	keep := `  PIN: <text>             keep in this chat (dropped on /clear)
  PIN!: <text>            keep in this chat, also across /clear
  CARRY: <text>           where you left off, for the next chat after
                          /clear; delivered once, then deleted (not a pin)
`
	if codex {
		keep = `  PIN: <text>             keep in this chat
  PIN!: <text>            keep in this chat (Codex starts a chat it cannot
                          link, so this does not survive /clear here)
  CARRY: <text>           where you left off, for the next chat; Codex
                          cannot hand it over, so use PIN@repo: instead
`
	}
	return `agent-ledger keeps the lines you mark and puts them back after compaction.
Start a prompt with (any case; only at the start):
` + keep + scopes + `  PIN [ref]: <new text>   reword a pin
  UNPIN: <ref>            remove a pin
` + promote + `  PINS?                   list pins          LEDGER?   this help
<ref> is a number from PINS?, an id, "last", or words from the pin.
Only you see PINS? and LEDGER?; the agent gets nothing.
` + status
}

// replacePin swaps pin old (same id and lifetime) in path for p, under the lock.
func replacePin(path string, old, p Pin) (bool, error) {
	found := false
	err := withLock(path, func() error {
		var out []string
		for _, l := range readLines(path) {
			var q Pin
			if json.Unmarshal([]byte(l), &q) == nil && q.ID == old.ID && q.Life == old.Life {
				if found {
					continue
				}
				found, l = true, string(Marshal(p))
			}
			out = append(out, l)
		}
		if !found {
			return nil
		}
		return writeReplace(path, []byte(strings.Join(out, "\n")+"\n"))
	})
	return found, err
}

// setLife gives pin p (same id and lifetime) in path the lifetime life, under the lock.
func setLife(path string, pin Pin, life string) error {
	return withLock(path, func() error {
		var out []string
		for _, l := range readLines(path) {
			var p Pin
			if json.Unmarshal([]byte(l), &p) == nil && p.ID == pin.ID && p.Life == pin.Life {
				p.Life = life
				l = string(Marshal(p))
			}
			out = append(out, l)
		}
		data := strings.Join(out, "\n")
		if data != "" {
			data += "\n"
		}
		return writeReplace(path, []byte(data))
	})
}

// sameText reports whether a and b are equal apart from spacing; a duplicate that
// differs in case or punctuation is reported with the stored text.
func sameText(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}
