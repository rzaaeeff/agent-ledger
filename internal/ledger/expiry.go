package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ExpiredGraceDays is how long an expired pin stays in its file, listed by PINS?
// as expired, before it is deleted. Pinning the same text again brings it back.
const ExpiredGraceDays = 3

// StaleDays is the default age after which a pin with no end date is named at
// startup, once a day, as worth dropping, promoting or dating.
const StaleDays = 14

// ExpiredSeenFile in a ledger lists the expired pins this chat was told about,
// or never had in its context, so each expiry is announced once.
const ExpiredSeenFile = "expired_seen"

// NudgedFile in a repo's ledger root holds the day the old-pin nudge last ran.
const NudgedFile = "nudged"

var (
	durRe  = regexp.MustCompile(`^([0-9]{1,4})([hdw])$`)
	dateRe = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})$`)
)

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "weds": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

// UntilHelp is how an end date is written, for refusals and the cheat sheet.
const UntilHelp = "a weekday (mon), tomorrow, a date (2026-11-10), a length (12h, 3d, 2w), or never"

var errNever = errors.New("never")

// ParseUntil reads the end date of "PIN until <when>:" in local time. A day
// (weekday, tomorrow, date) means the start of that day: "until mon" stops
// applying when Monday begins. A weekday is the next one after today. "never"
// returns errNever: the pin has no end date.
func ParseUntil(s string, now time.Time) (time.Time, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, now.Location()) }
	y, mo, d := now.Date()
	var t time.Time
	switch wd, isDay := weekdays[s]; {
	case s == "never":
		return time.Time{}, errNever
	case s == "tomorrow":
		t = day(y, mo, d+1)
	case isDay:
		ahead := (int(wd) - int(now.Weekday()) + 7) % 7
		if ahead == 0 {
			ahead = 7
		}
		t = day(y, mo, d+ahead)
	case durRe.MatchString(s):
		g := durRe.FindStringSubmatch(s)
		n, _ := strconv.Atoi(g[1])
		unit := map[string]time.Duration{"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[g[2]]
		if n < 1 || time.Duration(n)*unit > 366*24*time.Hour {
			return time.Time{}, fmt.Errorf("%q is out of range: use 1h to a year", s)
		}
		t = now.Add(time.Duration(n) * unit)
	case dateRe.MatchString(s):
		g := dateRe.FindStringSubmatch(s)
		yy, _ := strconv.Atoi(g[1])
		mm, _ := strconv.Atoi(g[2])
		dd, _ := strconv.Atoi(g[3])
		t = day(yy, time.Month(mm), dd)
		if t.Year() != yy || int(t.Month()) != mm || t.Day() != dd {
			return time.Time{}, fmt.Errorf("%s is not a real date", s)
		}
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("%s has already begun, so the pin would never apply", s)
		}
		if t.After(now.AddDate(5, 0, 0)) {
			return time.Time{}, fmt.Errorf("%s is more than five years away", s)
		}
	default:
		return time.Time{}, fmt.Errorf("%q is not an end date I can read; use %s", s, UntilHelp)
	}
	return t, nil
}

// until is the pin's end date; ok is false when it has none (or an unreadable one,
// which is treated as none rather than as already expired).
func (p Pin) until() (time.Time, bool) {
	if p.Until == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, p.Until)
	return t, err == nil
}

// expired reports whether the pin's end date has come.
func (p Pin) expired(now time.Time) bool {
	t, ok := p.until()
	return ok && !now.Before(t)
}

// active drops the pins whose end date has come.
func active(pins []Pin) []Pin {
	now := Now()
	out := pins[:0:0]
	for _, p := range pins {
		if !p.expired(now) {
			out = append(out, p)
		}
	}
	return out
}

// readPins is the pins in path that still apply.
func readPins(path string) []Pin { return active(ReadJSONL[Pin](path)) }

// untilStamp is how an end date is stored.
func untilStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// whenUser is an end date for the user: "Mon 10 Nov 00:00", with the year when
// it is not this one.
func whenUser(t time.Time) string {
	t = t.In(Now().Location())
	if t.Year() != Now().Year() {
		return t.Format("Mon 2 Jan 2006 15:04")
	}
	return t.Format("Mon 2 Jan 15:04")
}

// whenAgent is an end date for the model, unambiguous.
func whenAgent(t time.Time) string { return t.In(Now().Location()).Format("2006-01-02 15:04 (Mon)") }

// untilUser is " until Mon 10 Nov 00:00", or "" for a pin with no end date.
func untilUser(p Pin) string {
	if t, ok := p.until(); ok {
		return ", until " + whenUser(t)
	}
	return ""
}

// pinAge is ", N days" for PINS?: how long ago a pin was made; "" for pins from
// before timestamps were kept.
func pinAge(p Pin) string {
	ts, err := time.Parse(time.RFC3339Nano, p.TS)
	if err != nil {
		return ""
	}
	switch d := int(Now().Sub(ts).Hours() / 24); {
	case d < 1:
		return ", today"
	case d == 1:
		return ", 1 day"
	default:
		return fmt.Sprintf(", %d days", d)
	}
}

// listAllPins is listPins including expired pins.
func listAllPins(cwd, dir, here string) []located {
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

// expiredPins is the expired pins still kept in their files, oldest end first
// as stored.
func expiredPins(cwd, dir, here string) []located {
	now := Now()
	var out []located
	for _, p := range listAllPins(cwd, dir, here) {
		if p.expired(now) {
			out = append(out, p)
		}
	}
	return out
}

// seenKey names one expiry: a pin given a new end date expires anew.
func seenKey(p Pin) string { return p.ID + "@" + p.Until }

func readSeen(dir string) map[string]bool {
	seen := map[string]bool{}
	for _, l := range readLines(filepath.Join(dir, ExpiredSeenFile)) {
		if l = strings.TrimSpace(l); l != "" {
			seen[l] = true
		}
	}
	return seen
}

func addSeen(dir string, keys []string) {
	if len(keys) == 0 || MkdirPrivate(dir) != nil {
		return
	}
	_ = Append(filepath.Join(dir, ExpiredSeenFile), []byte(strings.Join(keys, "\n")))
}

// markExpiredSeen records the pins already expired when a chat starts (or is
// cleared): they were never in its context, so there is nothing to tell it.
func markExpiredSeen(cwd, dir string) {
	seen := readSeen(dir)
	var keys []string
	for _, p := range expiredPins(cwd, dir, "") {
		if k := seenKey(p.Pin); !seen[k] {
			keys = append(keys, k)
		}
	}
	addSeen(dir, keys)
}

// takeExpired deletes pins expired more than ExpiredGraceDays ago and returns,
// once per ledger, the ones that expired since this chat last heard: a line for
// the user and context for the model. Both are "" when nothing expired.
func takeExpired(cwd, dir, here string) (user, agent string) {
	if !anyDated(dir) {
		return "", ""
	}
	exp := expiredPins(cwd, dir, here)
	if len(exp) == 0 {
		return "", ""
	}
	purgeExpired(exp)
	seen := readSeen(dir)
	var fresh []located
	var keys []string
	for _, p := range exp {
		if k := seenKey(p.Pin); !seen[k] {
			seen[k] = true
			fresh, keys = append(fresh, p), append(keys, k)
		}
	}
	if len(fresh) == 0 {
		return "", ""
	}
	addSeen(dir, keys)
	var u, a []string
	for _, p := range fresh {
		t, _ := p.until()
		u = append(u, fmt.Sprintf("[%s] %q (%s, until %s)", p.ID, short(p.Text, 60), p.Where, whenUser(t)))
		a = append(a, fmt.Sprintf("- [%s] %q (end date %s)", p.ID, p.Text, whenAgent(t)))
	}
	noun := "pin"
	if len(fresh) > 1 {
		noun = "pins"
	}
	user = fmt.Sprintf("ledger: %s expired: %s. The agent was told. To bring one back, pin the same text again.", noun, strings.Join(u, "; "))
	agent = capBytes("agent-ledger: these pins reached the end date the user gave them and no longer apply. This is the user's own setting, not an instruction from anyone else:\n"+strings.Join(a, "\n"), 4000)
	return user, agent
}

// anyDated reports whether any pin store this ledger can see might hold a pin
// with an end date, without asking git for the branch: most prompts come from
// ledgers with no dated pins, and they should not pay for a git call.
func anyDated(dir string) bool {
	files := []string{filepath.Join(dir, PinnedFile), filepath.Join(GlobalDir(), PinnedFile)}
	if more, err := filepath.Glob(filepath.Join(filepath.Dir(dir), "@*", PinnedFile)); err == nil {
		files = append(files, more...)
	}
	for _, f := range files {
		if data, err := os.ReadFile(f); err == nil && strings.Contains(string(data), `"until":`) {
			return true
		}
	}
	return false
}

// purgeExpired deletes the pins in exp expired more than ExpiredGraceDays ago.
func purgeExpired(exp []located) {
	cutoff := Now().AddDate(0, 0, -ExpiredGraceDays)
	files := map[string]bool{}
	for _, p := range exp {
		if t, _ := p.until(); t.Before(cutoff) {
			files[p.File] = true
		}
	}
	for f := range files {
		_ = dropExpired(f, func(p Pin) bool { return p.expired(cutoff) })
	}
}

// dropExpired removes, under the lock, the expired pins in path that gone
// selects. Lines are matched one by one, not by id: a pin pinned again after it
// expired has the same id as its expired copy.
func dropExpired(path string, gone func(Pin) bool) error {
	return withLock(path, func() error {
		var keep []string
		changed := false
		for _, l := range readLines(path) {
			var p Pin
			if json.Unmarshal([]byte(l), &p) == nil && p.expired(Now()) && gone(p) {
				changed = true
				continue
			}
			keep = append(keep, l)
		}
		if !changed {
			return nil
		}
		return writeLines(path, keep)
	})
}

// stalePins is the pins that apply here, have no end date and were pinned more
// than days ago. CARRY items are not rules, and pins from before timestamps
// were kept are left alone.
func stalePins(cwd, dir string, days int) []located {
	if days <= 0 {
		return nil
	}
	cutoff := Now().AddDate(0, 0, -days)
	var out []located
	for _, p := range listPins(cwd, dir, "this chat") {
		if p.Life == LifeCarry || p.Until != "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, p.TS); err == nil && ts.Before(cutoff) {
			out = append(out, p)
		}
	}
	return out
}

// staleNudge is the startup line naming old pins with no end date, at most once
// a day per repo (or folder); "" when there is nothing to say.
func staleNudge(cwd, root, dir string, days int) string {
	old := stalePins(cwd, dir, days)
	if len(old) == 0 {
		return ""
	}
	today := Now().Format("2006-01-02")
	mark := filepath.Join(root, NudgedFile)
	if data, err := os.ReadFile(mark); err == nil && strings.TrimSpace(string(data)) == today {
		return ""
	}
	if MkdirPrivate(root) != nil || os.WriteFile(mark, []byte(today+"\n"), 0o600) != nil {
		return ""
	}
	shown := old
	if len(shown) > 3 {
		shown = shown[:3]
	}
	more := ""
	if len(old) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(old)-len(shown))
	}
	return fmt.Sprintf("ledger: %s pinned over %d days ago with no end date: %s%s. Still true? UNPIN: <id> drops one, PROMOTE: <id> makes it a project rule, PIN [<id>] until <when>: <same text> gives it an end date.",
		plural(len(old), "pin"), days, refList(shown), more)
}
