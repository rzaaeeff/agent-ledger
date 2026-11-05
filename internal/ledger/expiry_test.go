package ledger

import (
	"strings"
	"testing"
	"time"
)

func TestParseUntil(t *testing.T) {
	// Monday 2 November 2026, 10:30 local.
	now := time.Date(2026, 11, 2, 10, 30, 0, 0, time.Local)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.Local) }
	for in, want := range map[string]time.Time{
		"tomorrow":   day(11, 3),
		"fri":        day(11, 6),
		"Friday":     day(11, 6),
		"mon":        day(11, 9), // today is Monday: the next one
		"sun":        day(11, 8),
		"2026-11-10": day(11, 10),
		"12h":        now.Add(12 * time.Hour),
		"3d":         now.Add(72 * time.Hour),
		"2W":         now.Add(14 * 24 * time.Hour),
	} {
		got, err := ParseUntil(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseUntil(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseUntil("never", now); err != errNever {
		t.Errorf("never: %v", err)
	}
	for in, want := range map[string]string{
		"someday":    "not an end date",
		"2026-11-02": "already begun",
		"2020-01-01": "already begun",
		"2026-02-30": "not a real date",
		"2040-01-01": "five years",
		"0d":         "out of range",
		"400d":       "out of range",
	} {
		if _, err := ParseUntil(in, now); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseUntil(%q) error = %v, want %q", in, err, want)
		}
	}
}

func TestSlashUntil(t *testing.T) {
	old := Now
	Now = func() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local) }
	defer func() { Now = old }()
	for args, want := range map[string]string{
		"until fri Serhii is off":                 "PIN until fri: Serhii is off",
		"@repo until 3d CI is down":               "PIN@repo until 3d: CI is down",
		"! until tomorrow x":                      "PIN! until tomorrow: x",
		"until the migration lands, leave schema": "PIN: until the migration lands, leave schema",
		"until 2026-13-01 x":                      "PIN until 2026-13-01: x", // refused later, not pinned as text
	} {
		if got, _ := slashPrompt("pin", args); got != want {
			t.Errorf("slashPrompt(pin, %q) = %q, want %q", args, got, want)
		}
	}
}
