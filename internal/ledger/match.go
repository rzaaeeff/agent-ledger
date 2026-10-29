package ledger

import (
	"encoding/json"
	"fmt"
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

// pinMatches is how UNPIN:/PROMOTE: find a pin: its id, or text in it ignoring
// case, spacing and punctuation, or all the query's words in any order.
func pinMatches(p Pin, q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return false
	}
	if strings.EqualFold(p.ID, strings.Trim(q, "[]")) {
		return true
	}
	nq := normText(q)
	if nq == "" {
		return false
	}
	if strings.Contains(normText(p.Text), nq) {
		return true
	}
	pw := map[string]bool{}
	for _, w := range normWords(p.Text) {
		pw[w] = true
	}
	for w := range contentWords(q, 1) {
		if !pw[w] {
			return false
		}
	}
	return true
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
		out = append(out, located{p, f, w})
	}
	for _, s := range PinScopes(cwd, dir) {
		f := filepath.Join(s.Dir, PinnedFile)
		for _, p := range ReadJSONL[Pin](f) {
			out = append(out, located{p, f, s.Short})
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

// matching is the pins q selects; one entry per store and id.
func matching(q string, pins []located) []located {
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

// PinsList is the text PINS? shows the user (never the model).
func PinsList(pins []located) string {
	if len(pins) == 0 {
		return "No pins here. Start a prompt with PIN: to add one."
	}
	var b strings.Builder
	b.WriteString("Pins (shown to you only; the agent did not see this prompt):")
	for i, p := range pins {
		fmt.Fprintf(&b, "\n%d. [%s] %s (%s)", i+1, p.ID, p.Text, p.Where)
	}
	b.WriteString("\nUNPIN: <number, id, words or last> removes one.")
	return b.String()
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
