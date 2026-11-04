package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rzaaeeff/agent-ledger/internal/ledger"
)

// clock sets the time the hooks see, until the test ends.
func clock(t *testing.T, at time.Time) {
	t.Helper()
	ledger.Now = func() time.Time { return at }
	t.Cleanup(func() { ledger.Now = time.Now })
}

// monday is Monday 2 November 2026, 10:00 local.
var monday = time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local)

func startMsg(t *testing.T, out string) string {
	t.Helper()
	var v map[string]any
	if out == "" {
		return ""
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("want JSON, got %q", out)
	}
	s, _ := v["systemMessage"].(string)
	return s
}

func TestPinExpires(t *testing.T) {
	e := setup(t)
	clock(t, monday)
	pins := filepath.Join(e.dir("s1"), "pinned.jsonl")

	msg := blockMsg(t, e.say("PIN until fri: Serhii is off, do not suggest asking him", "s1"))
	if !strings.Contains(msg, "until Fri 6 Nov 00:00") || !strings.Contains(msg, "(1 pin applies here)") {
		t.Errorf("PIN until reply = %q", msg)
	}
	if got := lastField(t, pins, "until"); !strings.HasPrefix(got, "2026-11-06T00:00:00") {
		t.Errorf("stored until = %q", got)
	}
	// The model is told the end date up front, so it can reason about it.
	if ctx := e.inject("compact", "s1"); !strings.Contains(ctx, "Serhii is off") || !strings.Contains(ctx, "applies until 2026-11-06 00:00 (Fri), then stops") {
		t.Errorf("injected before expiry: %q", ctx)
	}
	if list := blockMsg(t, e.say("PINS?", "s1")); !strings.Contains(list, "(this chat, today, until Fri 6 Nov 00:00)") {
		t.Errorf("PINS? before expiry: %q", list)
	}

	if ctx := contextOf(e.say("hello", "s1")); !strings.Contains(ctx, "until 2026-11-06 00:00 (Fri), then it stops") {
		t.Errorf("the pin notice must carry the end date: %q", ctx)
	}
	// Thursday: still applies, nothing to say.
	clock(t, monday.AddDate(0, 0, 3))
	if out := e.say("hello", "s1"); out != "" {
		t.Errorf("before the end date, an ordinary prompt must print nothing: %q", out)
	}

	// Friday, the day it ends: the open chat is told once, user and agent both.
	clock(t, time.Date(2026, 11, 6, 0, 0, 1, 0, time.Local))
	out := e.say("hello", "s1")
	if m := sysMsg(t, out); !strings.Contains(m, "pin expired: [") || !strings.Contains(m, "Serhii is off") || !strings.Contains(m, "pin the same text again") {
		t.Errorf("expiry message for the user = %q", m)
	}
	if ctx := contextOf(out); !strings.Contains(ctx, "no longer apply") || !strings.Contains(ctx, "Serhii is off") {
		t.Errorf("expiry context for the agent = %q", ctx)
	}
	if out := e.say("hello again", "s1"); out != "" {
		t.Errorf("an expiry is announced once, got %q", out)
	}
	// Gone from what comes back, from PINS? numbering, and from UNPIN's reach.
	if ctx := e.inject("compact", "s1"); strings.Contains(ctx, "Serhii") {
		t.Errorf("expired pin injected: %q", ctx)
	}
	list := blockMsg(t, e.say("PINS?", "s1"))
	if !strings.HasPrefix(list, "No pins here.") || !strings.Contains(list, "Expired (no longer apply") || !strings.Contains(list, "ended Fri 6 Nov 00:00") {
		t.Errorf("PINS? after expiry: %q", list)
	}
	if m := blockMsg(t, e.say("UNPIN: serhii", "s1")); !strings.Contains(m, "no pins here") {
		t.Errorf("UNPIN must not see an expired pin: %q", m)
	}

	// Pinning it again brings it back, replacing the expired copy.
	if m := blockMsg(t, e.say("PIN until mon: Serhii is off, do not suggest asking him", "s1")); !strings.Contains(m, "until Mon 9 Nov 00:00") {
		t.Errorf("re-pin reply = %q", m)
	}
	if n := len(lines(t, pins)); n != 1 {
		t.Errorf("re-pin left %d lines, want 1", n)
	}
	if list := blockMsg(t, e.say("PINS?", "s1")); strings.Contains(list, "Expired") {
		t.Errorf("re-pinned text still listed as expired: %q", list)
	}
}

func TestExpiredPinsArePurgedAndNewChatsAreNotTold(t *testing.T) {
	e := setup(t)
	clock(t, monday)
	repoPins := filepath.Join(e.dir("@repo"), "pinned.jsonl")
	blockMsg(t, e.say("PIN@repo until 12h: SEV-1, the reliability MCP may be down", "s1"))
	blockMsg(t, e.say("PIN@repo: run the local test suite before deploying", "s1"))

	clock(t, monday.Add(13*time.Hour))
	// A chat that starts after the end date never had the pin: nothing to tell it.
	e.injectRaw("startup", "s2")
	if out := e.say("hello", "s2"); out != "" {
		t.Errorf("new chat told about a pin it never had: %q", out)
	}
	// The chat that had it is told.
	if m := sysMsg(t, e.say("hello", "s1")); !strings.Contains(m, "SEV-1") {
		t.Errorf("open chat not told: %q", m)
	}
	// Still in the file during the grace period, so PINS? can show it.
	if n := len(lines(t, repoPins)); n != 2 {
		t.Errorf("expired pin removed too early: %d lines", n)
	}

	clock(t, monday.AddDate(0, 0, ledger.ExpiredGraceDays+1))
	e.say("hello", "s1")
	got := lines(t, repoPins)
	if len(got) != 1 || !strings.Contains(got[0], "local test suite") {
		t.Errorf("after the grace period: %v", got)
	}
}

func TestEndDateRefusalsAndChanges(t *testing.T) {
	e := setup(t)
	clock(t, monday)
	pins := filepath.Join(e.dir("s1"), "pinned.jsonl")
	for prompt, want := range map[string]string{
		"PIN until someday: x":    "not an end date I can read",
		"PIN until 2020-01-01: x": "already begun",
		"CARRY until fri: x":      "only PIN takes an end date",
		"UNPIN until fri: x":      "only PIN takes an end date",
		// Several words, in any case, are refused too, not passed on as a prompt.
		"PIN until next friday: x":      "not an end date I can read",
		"pin until next friday: x":      "not an end date I can read",
		"PIN@repo until 10 Nov: x":      "not an end date I can read",
		"PIN until 2026-11-10 18:00: x": "not an end date I can read",
	} {
		if m := blockMsg(t, e.say(prompt, "s1")); !strings.Contains(m, "nothing saved") || !strings.Contains(m, want) {
			t.Errorf("%q -> %q, want %q", prompt, m, want)
		}
	}
	// A sentence that happens to start "pin until" is not a command.
	for _, prompt := range []string{
		"pin until we upgrade, numpy 2 breaks with: ImportError foo",
		"Pin until which version? pip says: conflict",
		"pin until the lockfile is regenerated: is that safe?",
		"pin until we ship: what else breaks?",
		"pin until numpy 2 ships: is that safe?",
		"pin until v2 lands: what do you think?",
		"pin until friday we ship: anything else?",
		"pin until this release ships: ok?",
		"pin until next release: is that sensible?",
		"pin until 2 more fixes: then what?",
	} {
		if out := e.say(prompt, "s1"); out != "" {
			t.Errorf("%q was taken as a command: %q", prompt, out)
		}
	}
	if exists(pins) {
		t.Fatalf("a refused end date stored something: %v", lines(t, pins))
	}
	// A refusal with more lines still lets the prompt reach the agent.
	if m := sysMsg(t, e.say("PIN until someday: x\nand fix the tests", "s1")); !strings.Contains(m, "nothing saved") {
		t.Errorf("multi-line refusal = %q", m)
	}

	blockMsg(t, e.say("PIN: no migrations until the last PR", "s1"))
	id := lastField(t, pins, "id")
	// Same text with an end date: the pin gets one.
	if m := blockMsg(t, e.say("PIN until fri: no migrations until the last PR", "s1")); !strings.Contains(m, "already pinned") || !strings.Contains(m, "now applies until Fri 6 Nov") {
		t.Errorf("dating an existing pin = %q", m)
	}
	// Reword keeps the end date; PIN [ref] until <when> changes it; never removes it.
	if m := blockMsg(t, e.say("PIN ["+id+"]: no migrations until the final PR", "s1")); !strings.Contains(m, "until Fri 6 Nov") {
		t.Errorf("reword lost the end date: %q", m)
	}
	id = lastField(t, pins, "id")
	if m := blockMsg(t, e.say("PIN ["+id+"] until 2w: no migrations until the final PR", "s1")); !strings.Contains(m, "now applies until Mon 16 Nov") {
		t.Errorf("changing the end date = %q", m)
	}
	if got := lastField(t, pins, "id"); got != id {
		t.Errorf("changing only the end date changed the id: %s -> %s", id, got)
	}
	if m := blockMsg(t, e.say("PIN ["+id+"] until never: no migrations until the final PR", "s1")); !strings.Contains(m, "no end date") {
		t.Errorf("until never = %q", m)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(lines(t, pins)[0]), &p)
	if _, has := p["until"]; has || len(lines(t, pins)) != 1 {
		t.Errorf("after until never: %v", lines(t, pins))
	}
	// The open chat hears about the end-date changes with its next prompt.
	if ctx := contextOf(e.say("hello", "s1")); !strings.Contains(ctx, "no end date") {
		t.Errorf("agent not told about the end date change: %q", ctx)
	}
}

func TestSlashPinUntil(t *testing.T) {
	e := setup(t)
	clock(t, monday)
	in := m{"cwd": e.repo, "session_id": "s1", "command_name": "agent-ledger:pin", "command_args": "@repo until tomorrow CI is down, use local tests", "prompt": "/agent-ledger:pin x"}
	out, _, _ := cli(in, "hook", "user-prompt-expansion")
	if !strings.Contains(out, "for this repo until Tue 3 Nov 00:00") {
		t.Errorf("/pin until = %q", out)
	}
	if got := lastField(t, filepath.Join(e.dir("@repo"), "pinned.jsonl"), "text"); got != "CI is down, use local tests" {
		t.Errorf("/pin until text = %q", got)
	}
}

func TestOldPinNudge(t *testing.T) {
	e := setup(t)
	clock(t, monday.AddDate(0, 0, -20))
	blockMsg(t, e.say("PIN@repo: schema is frozen", "s1"))
	blockMsg(t, e.say("PIN@repo until 3w: dated pins are not nagged about", "s1"))
	clock(t, monday.AddDate(0, 0, -1))
	blockMsg(t, e.say("PIN@repo: recent pins are not nagged about either", "s1"))

	clock(t, monday)
	out := e.injectRaw("startup", "s2")
	m := startMsg(t, out)
	if ctx := contextOf(out); strings.Contains(ctx, "no end date") || strings.Contains(ctx, "days ago") {
		t.Errorf("the agent was told a pin is old: %q", ctx)
	}
	if list := blockMsg(t, e.say("PINS?", "s2")); !strings.Contains(list, "schema is frozen (this repo, 20 days)") ||
		!strings.Contains(list, "recent pins are not nagged about either (this repo, 1 day)") {
		t.Errorf("PINS? ages = %q", list)
	}
	if !strings.Contains(m, "1 pin pinned over 14 days ago with no end date") || !strings.Contains(m, "schema is frozen") ||
		strings.Contains(m, "dated pins") || strings.Contains(m, "recent pins") || !strings.Contains(m, "PROMOTE: <id>") {
		t.Errorf("nudge = %q", m)
	}
	// Once a day, not on every new chat.
	if m := startMsg(t, e.injectRaw("startup", "s3")); strings.Contains(m, "no end date") {
		t.Errorf("nudged twice in a day: %q", m)
	}
	clock(t, monday.AddDate(0, 0, 1))
	if m := startMsg(t, e.injectRaw("startup", "s4")); !strings.Contains(m, "no end date") {
		t.Errorf("no nudge the next day: %q", m)
	}
	// Not after compaction, and old pins still come back word for word.
	out = e.injectRaw("compact", "s4")
	if m := startMsg(t, out); strings.Contains(m, "no end date") {
		t.Errorf("nudged after compaction: %q", m)
	}
	if ctx := contextOf(out); !strings.Contains(ctx, "schema is frozen") {
		t.Errorf("old pin not restored after compaction: %q", ctx)
	}
	// Never with stale_days=0.
	t.Setenv("AGENT_LEDGER_STALE_DAYS", "0")
	_ = os.Remove(filepath.Join(e.root, "nudged"))
	if m := startMsg(t, e.injectRaw("startup", "s5")); strings.Contains(m, "no end date") {
		t.Errorf("stale_days=0 still nudged: %q", m)
	}
}

// An expired pin in another scope must not hide the PIN! and CARRY handover of
// /clear, and an expired PIN! is neither handed over nor announced in the new chat.
func TestClearWithExpiredPins(t *testing.T) {
	e := setup(t)
	clock(t, monday)
	blockMsg(t, e.say("PIN@repo until 12h: freeze is on", "s1"))
	blockMsg(t, e.say("PIN! until 12h: keep replies short", "s1"))
	blockMsg(t, e.say("PIN!: always run the tests", "s1"))
	blockMsg(t, e.say("CARRY: parser is half done", "s1"))

	clock(t, monday.Add(13*time.Hour))
	cli(e.end("clear", "s1"), "hook", "session-end")
	out := e.injectRaw("clear", "s2")
	if m := startMsg(t, out); !strings.Contains(m, "kept 1 PIN! pin(s) across /clear") {
		t.Errorf("clear report = %q", m)
	}
	if ctx := contextOf(out); !strings.Contains(ctx, "always run the tests") || !strings.Contains(ctx, "parser is half done") || strings.Contains(ctx, "keep replies short") || strings.Contains(ctx, "freeze") {
		t.Errorf("clear context = %q", ctx)
	}
	if out := e.say("hello", "s2"); strings.Contains(out, "expired") {
		t.Errorf("new chat told about pins it never had: %q", out)
	}
}

func TestKeepUntilOnExistingPin(t *testing.T) {
	e := setup(t)
	clock(t, monday)
	pins := filepath.Join(e.dir("s1"), "pinned.jsonl")
	blockMsg(t, e.say("PIN: use tabs", "s1"))
	if m := blockMsg(t, e.say("PIN! until fri: use tabs", "s1")); !strings.Contains(m, "survives /clear") || !strings.Contains(m, "now applies until Fri 6 Nov") {
		t.Errorf("PIN! until on a plain pin = %q", m)
	}
	if l := lines(t, pins); len(l) != 1 || !strings.Contains(l[0], `"life":"keep"`) || !strings.Contains(l[0], `"until":"2026-11-06T00:00:00`) {
		t.Errorf("stored = %v", l)
	}
}

// With key=branch or key=repo, chats share one ledger folder: a new chat must not
// silence the open ones, and each open chat is told, not just the first to speak.
func TestSharedLedgerTellsEveryOpenChat(t *testing.T) {
	for _, key := range []string{"branch", "repo"} {
		t.Run(key, func(t *testing.T) {
			e := setup(t)
			t.Setenv("AGENT_LEDGER_KEY", key)
			clock(t, monday)
			e.injectRaw("startup", "s1")
			e.injectRaw("startup", "s2")
			blockMsg(t, e.say("PIN until 12h: no deploys", "s1"))

			clock(t, monday.Add(13*time.Hour))
			e.injectRaw("startup", "s3")
			if out := e.say("hello", "s3"); strings.Contains(out, "expired") {
				t.Errorf("new chat told about a pin it never had: %q", out)
			}
			for _, sid := range []string{"s1", "s2"} {
				if m := sysMsg(t, e.say("hello", sid)); !strings.Contains(m, "no deploys") {
					t.Errorf("open chat %s not told: %q", sid, m)
				}
				if out := e.say("hello again", sid); strings.Contains(out, "expired") {
					t.Errorf("chat %s told twice: %q", sid, out)
				}
			}
		})
	}
}
