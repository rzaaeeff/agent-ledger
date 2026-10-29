package ledger

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Injection budget: about 4k tokens (16000 chars, bytes/4 estimate). Pins are
// never trimmed: past 2k tokens of pins a warning is added and all are kept.
const (
	Budget        = 16000
	PinWarnChars  = 8000
	RecentPrompts = 5
	RecentDecs    = 10
	PromptMax     = 2000
	PruneDays     = 30
)

// HookInput is the hook JSON on stdin; unknown or mistyped fields are ignored.
type HookInput struct {
	raw       map[string]any
	Prompt    string
	Cwd       string
	SessionID string
	Source    string
	Reason    string
}

// ReadHookInput parses the hook JSON. Invalid input yields an empty HookInput.
func ReadHookInput(r io.Reader) HookInput {
	var h HookInput
	data, _ := io.ReadAll(r)
	if json.Unmarshal(data, &h.raw) != nil || h.raw == nil {
		h.raw = map[string]any{}
	}
	str := func(k string) string { s, _ := h.raw[k].(string); return s }
	h.Prompt, h.Cwd, h.SessionID, h.Source, h.Reason = str("prompt"), str("cwd"), str("session_id"), str("source"), str("reason")
	if h.Cwd == "" {
		h.Cwd, _ = os.Getwd()
	}
	return h
}

func emit(w io.Writer, msg string) {
	fmt.Fprintf(w, "%s\n", Marshal(map[string]string{"systemMessage": msg}))
}

// debug logs one line per hook event to <root>/hooks.debug.log when
// AGENT_LEDGER_DEBUG is set, to check session ids and hook order on a real /clear.
func debug(root, event string, h HookInput) {
	if os.Getenv("AGENT_LEDGER_DEBUG") == "" || MkdirPrivate(root) != nil {
		return
	}
	line := Marshal(map[string]any{"t": Now().Format("2006-01-02T15:04:05.000000000-07:00"), "e": event,
		"session_id": h.raw["session_id"], "source": h.raw["source"], "reason": h.raw["reason"], "cwd": h.raw["cwd"]})
	_ = appendRaw(filepath.Join(root, "hooks.debug.log"), append(line, '\n'))
}

// HookUserPromptSubmit logs every prompt and keeps the pinned rules:
//
//	PIN: <text>        stores <text> in this ledger; re-injected on every compact,
//	                   dropped (and shown to the user) on /clear
//	PIN!: <text>       the same, but kept across /clear
//	CARRY: <text>      injected once, right after the next /clear (a handoff)
//	PIN@branch: <text> for every session on this branch (refused on the default branch)
//	PIN@repo: <text>   for every session and branch of this repo
//	PIN@global: <text> for every repo
//	UNPIN: <id|text>   removes the pin with that id, or the only one containing the
//	                   text, from any of those stores
//	PROMOTE: <id|text> moves that pin into the repo's AGENTS.md (or CLAUDE.md, or
//	                   promote_to), uncommitted
//
// Config logging=pins-only logs only these commands, logging=off logs no prompts
// (pins are still kept); redact=on (default) masks credentials before storing.
//
// Plain stdout would reach the model, so it prints only a systemMessage JSON (shown
// to the user) after PIN:/UNPIN:. Problems go to errors.log.
func HookUserPromptSubmit(h HookInput, out io.Writer) {
	if h.Prompt == "" {
		return
	}
	cfg := LoadConfig(h.Cwd)
	dir, err := DirFor(cfg, h.Cwd, h.SessionID)
	if err != nil {
		return
	}
	prompt, masked := h.Prompt, 0
	if cfg.Redact == "on" {
		prompt, masked = Redact(prompt)
	}
	cmd, ok := parsePin(prompt)
	ts := stamp()
	if cfg.Logging == "all" || (cfg.Logging == "pins-only" && ok) {
		if MkdirPrivate(dir) != nil {
			return
		}
		if Append(filepath.Join(dir, PromptsFile), Marshal(Prompt{ts, h.SessionID, prompt})) != nil {
			LogError(dir, "prompts append failed")
		}
	}
	note := ""
	if masked > 0 {
		note = fmt.Sprintf(" (redacted %d secret(s) before storing)", masked)
	}
	if !ok || cmd.text == "" {
		if masked > 0 && cfg.Logging != "off" {
			emit(out, "ledger:"+note)
		}
		return
	}
	if MkdirPrivate(dir) != nil {
		return
	}
	out = &suffixWriter{w: out, suffix: note}
	switch cmd.verb {
	case "PIN", "CARRY":
		target, where := dir, ""
		life := ""
		switch {
		case cmd.verb == "CARRY" && (cmd.scope != "" || cmd.keep):
			emit(out, "CARRY takes no scope or '!': it hands this session's text to the chat after the next /clear.")
			return
		case cmd.verb == "CARRY":
			life, where = LifeCarry, " for the chat after the next /clear"
		case cmd.scope == "" && cmd.keep:
			life, where = LifeKeep, " (kept across /clear)"
		case cmd.scope == ScopeBranch:
			b, _ := git(h.Cwd, "branch", "--show-current")
			if b == "" || DefaultBranches(h.Cwd)[b] {
				name := b
				if name == "" {
					name = "a detached HEAD"
				}
				emit(out, fmt.Sprintf("PIN@branch refused on %s (the default branch never keeps pins). Use PIN: for this session or PIN@repo: for the repo.", name))
				return
			}
			target, where = filepath.Join(filepath.Dir(dir), branchKey(b)), " for branch "+b
		case cmd.scope == ScopeRepo:
			target, where = filepath.Join(filepath.Dir(dir), RepoKey), " for this repo"
		case cmd.scope == ScopeGlobal:
			target, where = GlobalDir(), " for every repo"
		}
		scope := cmd.scope
		if cmd.scope != "" && filepath.Clean(target) == filepath.Clean(dir) {
			// key=repo: the repo pins live in the ledger itself; keep them on /clear.
			scope, life = "", LifeKeep
		}
		if cmd.scope == ScopeBranch && MkdirPrivate(target) == nil {
			b, _ := git(h.Cwd, "branch", "--show-current")
			_ = writeReplace(filepath.Join(target, BranchNameFile), []byte(b+"\n"))
		}
		id := pinID(scope, cmd.text)
		if MkdirPrivate(target) != nil || Append(filepath.Join(target, PinnedFile), Marshal(Pin{id, ts, cmd.text, life})) != nil {
			LogError(dir, "pinned append failed")
		}
		emit(out, fmt.Sprintf("pinned [%s]%s. %s", id, where, Status(dir)))
	case "UNPIN":
		text := cmd.text
		hits, err := UnpinAny(h.Cwd, dir, text)
		switch {
		case err == nil && hits == 0:
			emit(out, "UNPIN did nothing: no pins match. "+Status(dir))
		case err != nil:
			LogError(dir, "unpin failed")
			emit(out, "UNPIN failed, see "+filepath.Join(dir, ErrorsFile))
		case hits == 1:
			emit(out, "unpinned. "+Status(dir))
		default:
			LogError(dir, fmt.Sprintf("UNPIN '%s' matched %d items; nothing removed", text, hits))
			emit(out, fmt.Sprintf("UNPIN did nothing: '%s' matched %d pins (use the [id] from the pin list). %s", text, hits, Status(dir)))
		}
	case "PROMOTE":
		if cmd.scope != "" || cmd.keep {
			emit(out, "PROMOTE takes no scope or '!': PROMOTE: <id or text>.")
			return
		}
		p, from, hits := findPin(h.Cwd, dir, cmd.text)
		if hits != 1 {
			emit(out, fmt.Sprintf("PROMOTE did nothing: '%s' matched %d pins (use the [id] from the pin list).", cmd.text, hits))
			return
		}
		target, err := PromoteTarget(cfg, h.Cwd)
		var added bool
		if err == nil {
			added, err = addRule(target, p.Text)
		}
		if err != nil {
			LogError(dir, "promote failed: "+err.Error())
			emit(out, "PROMOTE failed: "+err.Error())
			return
		}
		if _, err := Unpin(from, p.ID); err != nil {
			LogError(dir, "promote: unpin failed")
		}
		msg := fmt.Sprintf("promoted [%s] to %s and unpinned it. Not committed: review with git diff.", p.ID, filepath.Base(target))
		if !added {
			msg = fmt.Sprintf("[%s] was already in %s; unpinned it.", p.ID, filepath.Base(target))
		}
		emit(out, msg+" "+Status(dir))
	}
}

// suffixWriter adds suffix to the systemMessage emitted through it (the redaction note).
type suffixWriter struct {
	w      io.Writer
	suffix string
}

func (s *suffixWriter) Write(b []byte) (int, error) {
	if s.suffix == "" {
		return s.w.Write(b)
	}
	var v map[string]string
	if json.Unmarshal(b, &v) == nil && v["systemMessage"] != "" {
		v["systemMessage"] += s.suffix
		_, err := fmt.Fprintf(s.w, "%s\n", Marshal(v))
		return len(b), err
	}
	return s.w.Write(b)
}

// HookSessionStart prints what must survive a compaction: the ledger's pinned
// rules, last prompts and last decisions. By the hook's source:
//
//	compact  prints everything.
//	clear    with clear=drop (default for key=session) adds no context: the pins
//	         were dropped, so it only shows the user which ones, once, as a
//	         systemMessage. With clear=keep it prints the pins and decisions.
//	resume   prints nothing (the transcript is already loaded).
//	startup  deletes this repo's ledgers idle for prune_days, then prints the
//	         ledger (for a new session usually empty).
//
// Any other or missing source prints everything, which is what a manual run (for
// example from Codex, keyed by branch) wants. With a session id it also exports
// CLAUDE_SESSION_ID through $CLAUDE_ENV_FILE, for decide and status.
func HookSessionStart(h HookInput, out io.Writer) {
	if h.SessionID != "" {
		if f := os.Getenv("CLAUDE_ENV_FILE"); f != "" {
			_ = appendRaw(f, []byte("export CLAUDE_SESSION_ID="+Safe(h.SessionID, 100, true)+"\n"))
		}
	}
	root, err := Root(h.Cwd)
	if err != nil {
		return
	}
	debug(root, "SessionStart", h)
	cfg := LoadConfig(h.Cwd)
	dir, err := DirFor(cfg, h.Cwd, h.SessionID)
	if err != nil {
		return
	}
	switch h.Source {
	case "resume":
		return
	case "clear":
		if cfg.Clear == "keep" {
			cfg.RecentPrompts = 0
			break
		}
		markHandled(dir)
		if h.SessionID != "" {
			cleared(cfg, root, dir, h, out)
		}
		return
	case "startup":
		Prune(h.Cwd, root, dir, cfg.PruneDays)
	}
	if h.Source == "clear" {
		// clear=keep: no prompts, plus the CARRY handoff, once.
		text := Compose(h.Cwd, dir, cfg)
		c := takeCarry(dir)
		_, statErr := os.Stat(dir)
		if note := TakeCleared(root, h.SessionID, h.Cwd, statErr != nil || cfg.Key != KeySession); note != nil {
			c = append(c, note.Carry...)
		}
		if len(c) > 0 {
			text = strings.TrimLeft(text+"\n### Carried over from before /clear (once)\n"+strings.Join(pinLines(c, false), "\n"), "\n")
		}
		if text != "" {
			fmt.Fprintln(out, text)
		}
		return
	}
	if text := Compose(h.Cwd, dir, cfg); text != "" {
		fmt.Fprintln(out, text)
	}
}

// Inject is a manual run (Codex, or by hand): the same as a SessionStart without a
// source, keyed by the env session id or the branch.
func Inject(out io.Writer) {
	cwd, _ := os.Getwd()
	HookSessionStart(HookInput{raw: map[string]any{}, Cwd: cwd}, out)
}

func runes(s string) int { return utf8.RuneCountInString(s) }

func cut(s string, n int) (string, bool) {
	if runes(s) <= n {
		return s, false
	}
	return string([]rune(s)[:n]), true
}

func tail[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[len(xs)-n:]
	}
	return xs
}

// Compose builds the context block for the ledger in dir, within cfg.BudgetChars:
// over it, the oldest recent prompts go first, then the oldest decisions. Never pins.
func Compose(cwd, dir string, cfg Config) string {
	pins := strings.Join(pinLines(sessionPins(dir), true), "\n")
	scoped := scopedSections(cwd, dir)
	mark := readMark(dir)
	prompts := readJSONLFrom[Prompt](filepath.Join(dir, PromptsFile), mark.Prompts)
	decisions := readJSONLFrom[Decision](filepath.Join(dir, DecisionsFile), mark.Decisions)

	var recent, decs string
	for r, d := cfg.RecentPrompts, cfg.Decisions; ; {
		var rl, dl []string
		for _, p := range tail(prompts, r) {
			text, was := cut(p.Prompt, PromptMax)
			if was {
				text += " [cut; full text in prompts.jsonl]"
			}
			ts, _ := cut(p.TS, 16)
			rl = append(rl, fmt.Sprintf("- %s: %s", ts, text))
		}
		for _, x := range tail(decisions, d) {
			dl = append(dl, fmt.Sprintf("- DECISION: %s\n  BECAUSE: %s\n  REJECTED: %s", x.Decision, x.Because, x.Rejected))
		}
		recent, decs = strings.Join(rl, "\n"), strings.Join(dl, "\n")
		if runes(pins)+runes(scoped)+runes(recent)+runes(decs) <= cfg.BudgetChars {
			break
		}
		if r > 1 {
			r--
		} else if d > 1 {
			d--
		} else {
			break
		}
	}
	if pins+scoped+recent+decs == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Agent ledger (re-injected after compact; full logs in %s)", dir)
	if pins != "" {
		b.WriteString("\n### Pinned by the user (standing rules and concerns, verbatim)\n" + pins)
	}
	b.WriteString(scoped)
	if runes(pins)+runes(scoped) > PinWarnChars {
		b.WriteString("\nWARNING: pinned items exceed 2k tokens; UNPIN: <id> the ones that no longer apply.")
	}
	if recent != "" {
		b.WriteString("\n### Last prompts from the user\n" + recent)
	}
	if decs != "" {
		b.WriteString("\n### Session decisions\n" + decs)
	}
	return b.String()
}

// cleared runs on SessionStart after /clear: drops this session's pins if
// SessionEnd has not yet (same session id), then tells the user which pins the
// clear dropped. Nothing goes to the model.
func cleared(cfg Config, root, dir string, h HookInput, out io.Writer) {
	_, statErr := os.Stat(dir)
	// A shared (branch or repo) ledger exists already, so a new session id cannot be
	// told apart by a missing dir: allow the same-cwd note.
	isNew := statErr != nil || cfg.Key != KeySession
	if !isNew {
		markCleared(dir)
	}
	_ = ClearPins(dir, root, h.SessionID, h.Cwd)
	note := TakeCleared(root, h.SessionID, h.Cwd, isNew)
	var msg, ctx []string
	if note != nil {
		// PIN! pins stay in place for the same session; a new id gets a copy.
		if len(note.Kept) > 0 && filepath.Clean(note.Dir) != filepath.Clean(dir) && MkdirPrivate(dir) == nil {
			have := map[string]bool{}
			for _, p := range ReadJSONL[Pin](filepath.Join(dir, PinnedFile)) {
				have[p.ID] = true
			}
			for _, p := range note.Kept {
				if !have[p.ID] {
					_ = Append(filepath.Join(dir, PinnedFile), Marshal(p))
				}
			}
		}
		if len(note.Pins) > 0 {
			var lines []string
			for _, p := range note.Pins {
				text, was := cut(p.Text, 300)
				if was {
					text += "..."
				}
				lines = append(lines, fmt.Sprintf("- [%s] %s", p.ID, text))
			}
			msg = append(msg, fmt.Sprintf("ledger: /clear dropped %d pin(s); PIN: again any that still apply (archived in %s):\n%s",
				len(note.Pins), filepath.Join(note.Dir, ClearedFile), strings.Join(lines, "\n")))
		}
		if len(note.Kept) > 0 {
			msg = append(msg, fmt.Sprintf("ledger: kept %d PIN! pin(s) across /clear.", len(note.Kept)))
		}
		if len(note.Carry) > 0 {
			msg = append(msg, fmt.Sprintf("ledger: carried %d item(s) into this chat.", len(note.Carry)))
			ctx = append(ctx, "### Carried over from before /clear (once)\n"+strings.Join(pinLines(note.Carry, false), "\n"))
		}
	}
	if pins := pinLines(sessionPins(dir), true); len(pins) > 0 {
		ctx = append([]string{"### Pinned by the user (standing rules and concerns, verbatim)\n" + strings.Join(pins, "\n")}, ctx...)
	}
	if s := strings.TrimLeft(scopedSections(h.Cwd, dir), "\n"); s != "" {
		ctx = append(ctx, s)
	}
	if len(msg) == 0 && len(ctx) == 0 {
		return
	}
	v := map[string]any{}
	if len(msg) > 0 {
		v["systemMessage"] = strings.Join(msg, "\n")
	}
	if len(ctx) > 0 {
		v["hookSpecificOutput"] = map[string]string{"hookEventName": "SessionStart",
			"additionalContext": "## Agent ledger (after /clear)\n" + strings.Join(ctx, "\n")}
	}
	fmt.Fprintf(out, "%s\n", Marshal(v))
}

// HookSessionEnd drops the session's pins on /clear (reason "clear") when
// clear=drop, so a cleared chat starts without them; other reasons keep them. Prints nothing and does only
// one small file move: SessionEnd hooks get little time.
func HookSessionEnd(h HookInput) {
	root, err := Root(h.Cwd)
	if err != nil {
		return
	}
	debug(root, "SessionEnd", h)
	if h.Reason != "clear" || h.SessionID == "" {
		return
	}
	cfg := LoadConfig(h.Cwd)
	dir, err := DirFor(cfg, h.Cwd, h.SessionID)
	if err != nil || handledRecently(dir) {
		return
	}
	if cfg.Clear != "drop" {
		// clear=keep: only hand the CARRY items over, in case /clear changes the id.
		if c := takeCarry(dir); len(c) > 0 {
			now := Now()
			_ = Append(filepath.Join(root, NotesFile), Marshal(Note{
				N: fmt.Sprintf("%s.%d.%d", h.SessionID, now.UnixNano(), os.Getpid()), Session: h.SessionID,
				Cwd: h.Cwd, Dir: dir, Epoch: now.Unix(), Carry: c}))
		}
		return
	}
	markCleared(dir)
	_ = ClearPins(dir, root, h.SessionID, h.Cwd)
}
