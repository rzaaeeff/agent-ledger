package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Injection budget, in UTF-8 bytes. Claude Code keeps 10,000 characters of hook
// context (only a 2,000-character preview beyond that) and Codex about 2,500
// tokens counted as bytes/4, so 10,000 bytes. MaxBudget stays under both, header
// included; pins that do not fit are cut, with a warning at the top.
const (
	Budget        = 9000
	MaxBudget     = 9500
	DecisionMax   = 600
	PinWarnBytes  = 6000
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

// block stops the prompt: the user sees msg, the model gets no turn.
func block(w io.Writer, msg string) {
	fmt.Fprintf(w, "%s\n", Marshal(map[string]string{"decision": "block", "reason": msg}))
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
//	UNPIN: <ref>       removes the one pin ref selects, from any of those stores:
//	                   an id, its number in PINS?, "last", or words in it in any
//	                   order (case and punctuation ignored); else lists the closest
//	PROMOTE: <ref>     moves that pin into the repo's AGENTS.md (or CLAUDE.md, or
//	                   promote_to), uncommitted
//	PIN [ref]: <text>  rewords that pin; it keeps its store and lifetime
//	PINS?              blocks the prompt; the user (not the model) sees the pins
//	LEDGER?            the same, with a cheat sheet and the status
//
// Keywords match in any case, only at the start of the prompt. A look-alike
// ("PIN; x", "PIN x") saves nothing and gets a hint; the prompt still goes on.
//
// The same text pinned again in the same store is not added (PIN! upgrades a
// PIN); a pin similar to an existing one is added with a note naming it.
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
		why := "agent-ledger could not open its ledger: " + err.Error()
		if errors.Is(err, ErrNoRepo) {
			why = "agent-ledger works only inside a git repository; this folder is not in one, so nothing is stored here."
		}
		if q := strings.TrimSpace(h.Prompt); strings.EqualFold(q, "PINS?") || strings.EqualFold(q, "LEDGER?") {
			block(out, why)
		} else if _, ok := parsePin(h.Prompt); ok {
			emit(out, "ledger: nothing saved: "+why)
		}
		return
	}
	here := map[string]string{"branch": "this branch", "repo": "this repo"}[cfg.Key]
	if here == "" {
		here = "this chat"
	}
	// Blocking the prompt shows the reason to the user only: no model turn.
	switch q := strings.TrimSpace(h.Prompt); {
	case strings.EqualFold(q, "PINS?"):
		block(out, PinsList(listPins(h.Cwd, dir, here)))
		return
	case strings.EqualFold(q, "LEDGER?"):
		block(out, HelpText(Status(dir)))
		return
	}
	pinsHere := func() string { return brief(listPins(h.Cwd, dir, here)) }
	prompt, masked := h.Prompt, 0
	if cfg.Redact == "on" {
		prompt, masked = Redact(prompt)
	}
	cmd, ok := parsePin(prompt)
	ts := stamp()
	if cfg.Logging == "all" || (cfg.Logging == "pins-only" && ok) {
		if MkdirPrivate(dir) != nil {
			if ok && cmd.text != "" {
				emit(out, cmd.verb+" NOT SAVED: could not create "+dir+" (check permissions and disk space).")
			}
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
	if !ok {
		if cfg.Logging == "off" {
			note = ""
		}
		msg := ""
		if hint := nearMiss(prompt); hint != "" {
			msg = hint + note
		} else if note != "" {
			msg = "ledger:" + note
		}
		v := map[string]any{}
		if msg != "" {
			v["systemMessage"] = msg
		}
		// UNPIN/PROMOTE/reword blocked their prompt, so the model learns of them now.
		if n := takeNotices(dir); n != "" {
			v["hookSpecificOutput"] = map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": n}
		}
		if len(v) > 0 {
			fmt.Fprintf(out, "%s\n", Marshal(v))
		}
		return
	}
	if cmd.scope != "" && cmd.scope != ScopeBranch && cmd.scope != ScopeRepo && cmd.scope != ScopeGlobal {
		emit(out, fmt.Sprintf("ledger: nothing saved: there is no @%s. Use @branch, @repo or @global.", cmd.scope))
		return
	}
	if cmd.text == "" {
		emit(out, "ledger: nothing saved: "+cmd.verb+": needs text after the colon.")
		return
	}
	if MkdirPrivate(dir) != nil {
		emit(out, cmd.verb+" NOT SAVED: could not create "+dir+" (check permissions and disk space).")
		return
	}
	out = &suffixWriter{w: out, suffix: note}
	if cmd.ref != "" {
		rewordPin(h, dir, here, cmd, out)
		return
	}
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
				why := b + " (the default branch never keeps pins)"
				if b == "" {
					why = "a detached HEAD (there is no branch to keep them for)"
				}
				emit(out, fmt.Sprintf("PIN@branch refused on %s. Use PIN: for this chat or PIN@repo: for the repo.", why))
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
		if life == LifeCarry {
			// Apart from a PIN: of the same text, so UNPIN can take either.
			id = pinID(LifeCarry, cmd.text)
		}
		file := filepath.Join(target, PinnedFile)
		all := listPins(h.Cwd, dir, here)
		var similar []located
		for _, p := range all {
			sameStore := filepath.Clean(p.File) == filepath.Clean(file) && (p.Life == LifeCarry) == (life == LifeCarry)
			switch {
			case sameStore && normText(p.Text) == normText(cmd.text):
				msg := fmt.Sprintf("already pinned [%s]", p.ID)
				if !sameText(p.Text, cmd.text) {
					// Case or punctuation differ ("Foo.go" vs "foo.go"): show the stored text.
					msg += fmt.Sprintf(" as %q (if the difference matters: PIN [%s]: <text>)", short(p.Text, 60), p.ID)
				}
				if life == LifeKeep && p.Life == "" {
					if err := setLife(file, p.Pin, LifeKeep); err != nil {
						LogError(dir, "pin upgrade failed")
						emit(out, fmt.Sprintf("already pinned [%s]; could NOT make it survive /clear (see %s).", p.ID, filepath.Join(dir, ErrorsFile)))
						return
					}
					msg += "; it now survives /clear"
				}
				emit(out, msg+". "+pinsHere())
				return
			case p.ID != id && looksSimilar(p.Text, cmd.text):
				similar = append(similar, p)
			}
		}
		if MkdirPrivate(target) != nil || Append(file, Marshal(Pin{id, Now().Format(time.RFC3339Nano), cmd.text, life})) != nil {
			LogError(dir, "pinned append failed")
			emit(out, cmd.verb+" NOT SAVED: could not write "+file+" (check permissions and disk space).")
			return
		}
		msg := fmt.Sprintf("pinned [%s]%s.", id, where)
		if len(similar) > 0 {
			if len(similar) > 2 {
				similar = similar[:2]
			}
			msg += " Similar: " + refList(similar) + ". Both are kept; if this one replaces it, send UNPIN: " + similar[0].ID + "."
		}
		if cmd.typed != strings.ToUpper(cmd.typed) {
			msg += " Not meant as a pin? UNPIN: last."
		}
		if n := pinBytes(listPins(h.Cwd, dir, here)); n > PinWarnBytes {
			msg += fmt.Sprintf(" Warning: pins here now take %d of the %d bytes that come back after compaction; UNPIN the ones that no longer apply.", n, MaxBudget)
		}
		emit(out, msg+" "+pinsHere())
	case "UNPIN", "PROMOTE":
		if cmd.verb == "PROMOTE" && (cmd.scope != "" || cmd.keep) {
			block(out, "PROMOTE takes no scope or '!': PROMOTE: <id or text>.")
			return
		}
		hit, miss := pickPin(cmd.verb, cmd.text, listPins(h.Cwd, dir, here))
		if miss != "" {
			block(out, miss)
			return
		}
		p, from := hit.Pin, hit.File
		if cmd.verb == "UNPIN" {
			switch ok, err := Unpin(from, p); {
			case err != nil:
				LogError(dir, "unpin failed")
				block(out, "UNPIN failed, see "+filepath.Join(dir, ErrorsFile))
			case !ok:
				block(out, fmt.Sprintf("UNPIN did nothing: [%s] was already gone. %s", p.ID, pinsHere()))
			default:
				notice(dir, fmt.Sprintf("The user removed pin [%s] %q; it no longer applies.", p.ID, p.Text))
				block(out, fmt.Sprintf("unpinned [%s] %q. %s", p.ID, short(p.Text, 60), pinsHere()))
			}
			return
		}
		target, err := PromoteTarget(cfg, h.Cwd)
		var added bool
		if err == nil {
			added, err = addRule(target, p.Text)
		}
		if err != nil {
			LogError(dir, "promote failed: "+err.Error())
			block(out, "PROMOTE failed: "+err.Error())
			return
		}
		unpinned := "unpinned it"
		if ok, err := Unpin(from, p); err != nil {
			LogError(dir, "promote: unpin failed")
			unpinned = "could NOT unpin it (see " + filepath.Join(dir, ErrorsFile) + ")"
		} else if !ok {
			unpinned = "it was already unpinned"
		}
		notice(dir, fmt.Sprintf("The user moved pin [%s] %q into %s as a project rule.", p.ID, p.Text, filepath.Base(target)))
		msg := fmt.Sprintf("promoted [%s] to %s; %s. Not committed: review with git diff.", p.ID, filepath.Base(target), unpinned)
		if !added {
			msg = fmt.Sprintf("[%s] was already in %s; %s.", p.ID, filepath.Base(target), unpinned)
		}
		block(out, msg+" "+pinsHere())
	}
}

// pickPin is the one pin ref selects among pins, or the message for the user
// when it selects none or several.
func pickPin(verb, ref string, all []located) (located, string) {
	if n, ok := pinNumber(ref); ok && (n < 1 || n > len(all)) {
		return located{}, fmt.Sprintf("%s did nothing: there is no pin %d (there are %d). Send PINS? to list them.", verb, n, len(all))
	}
	hits := matching(resolveRef(ref, all), all)
	if len(hits) == 1 {
		return hits[0], ""
	}
	if len(all) == 0 {
		return located{}, verb + " did nothing: there are no pins here."
	}
	msg := fmt.Sprintf("%s did nothing: no pin matches %q.", verb, ref)
	if c := closest(ref, all, 3); len(hits) == 0 && len(c) > 0 {
		msg += " Closest: " + refList(c) + "."
	} else if len(hits) > 1 {
		if len(hits) > 5 {
			hits = hits[:5]
		}
		msg = fmt.Sprintf("%s did nothing: %q matches several pins: %s.", verb, ref, refList(hits))
	}
	return located{}, msg + fmt.Sprintf(" Send %s: <id>, or PINS? to list them.", verb)
}

// rewordPin handles PIN [ref]: <text>: the pin keeps its store and lifetime and
// gets the id of its new text.
func rewordPin(h HookInput, dir, here string, cmd pinCmd, out io.Writer) {
	if cmd.verb != "PIN" || cmd.keep || cmd.scope != "" {
		block(out, "ledger: nothing saved. To reword a pin: PIN [id]: new text (it keeps its scope and lifetime).")
		return
	}
	all := listPins(h.Cwd, dir, here)
	hit, miss := pickPin("PIN [ref]", cmd.ref, all)
	if miss != "" {
		if _, isNum := pinNumber(cmd.ref); !isNum && !strings.EqualFold(cmd.ref, "last") && len(matching(cmd.ref, all)) == 0 {
			// "PIN [WIP]: x" with no pin [WIP] is a pin whose text starts with [WIP].
			cmd.ref, cmd.text = "", "["+cmd.ref+"] "+cmd.text
			HookUserPromptSubmit(HookInput{raw: h.raw, Prompt: "PIN: " + cmd.text, Cwd: h.Cwd, SessionID: h.SessionID}, out)
			return
		}
		block(out, strings.Replace(miss, "Send PIN [ref]: <id>", "Send PIN [<id>]: <new text>", 1))
		return
	}
	if hit.Text == cmd.text {
		block(out, fmt.Sprintf("[%s] already says that.", hit.ID))
		return
	}
	for _, o := range all {
		if o.File == hit.File && o.ID != hit.ID && (o.Life == LifeCarry) == (hit.Life == LifeCarry) && normText(o.Text) == normText(cmd.text) {
			block(out, fmt.Sprintf("ledger: nothing changed: [%s] already says that. To drop the old one: UNPIN: %s", o.ID, hit.ID))
			return
		}
	}
	scope := hit.Scope
	if hit.Life == LifeCarry {
		scope = LifeCarry
	}
	p := Pin{pinID(scope, cmd.text), Now().Format(time.RFC3339Nano), cmd.text, hit.Life}
	switch ok, err := replacePin(hit.File, hit.Pin, p); {
	case err != nil:
		LogError(dir, "reword failed")
		block(out, "PIN NOT SAVED: could not reword ["+hit.ID+"], see "+filepath.Join(dir, ErrorsFile))
	case !ok:
		block(out, fmt.Sprintf("PIN NOT SAVED: [%s] was removed meanwhile.", hit.ID))
	default:
		notice(dir, fmt.Sprintf("The user reworded pin [%s] %q; it is now [%s] %q.", hit.ID, hit.Text, p.ID, p.Text))
		block(out, fmt.Sprintf("reworded [%s] -> [%s] (%s). %s", hit.ID, p.ID, hit.Where, brief(listPins(h.Cwd, dir, here))))
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
	if json.Unmarshal(b, &v) == nil {
		for _, k := range []string{"systemMessage", "reason"} {
			if v[k] != "" {
				v[k] += s.suffix
				_, err := fmt.Fprintf(s.w, "%s\n", Marshal(v))
				return len(b), err
			}
		}
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
			_ = appendRaw(f, []byte("export CLAUDE_SESSION_ID="+SessionKey(h.SessionID)+"\n"))
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
	text, n := compose(h.Cwd, dir, cfg)
	msg := ""
	if h.Source == "compact" && n != (counts{}) {
		msg = "ledger: restored " + n.String() + " after compaction."
	} else if n.pins > 0 {
		msg = "ledger: " + pinsApply(n.pins) + " (PINS? lists them)."
	}
	if h.Source == "startup" {
		if hint := filepath.Join(root, HintedFile); !fileExists(hint) && MkdirPrivate(root) == nil &&
			os.WriteFile(hint, nil, 0o600) == nil {
			msg = strings.TrimSpace(msg + " agent-ledger is on: start a prompt with PIN: to keep a rule through compaction. LEDGER? lists the commands.")
		}
	}
	if h.Source == "clear" {
		// clear=keep: no prompts, plus the CARRY handoff, once.
		c := takeCarry(dir)
		_, statErr := os.Stat(dir)
		if note, _ := TakeCleared(root, h.SessionID, h.Cwd, statErr != nil || cfg.Key != KeySession); note != nil {
			c = append(c, note.Carry...)
		}
		if len(c) > 0 {
			text = strings.TrimLeft(text+"\n### Carried over from before /clear (once)\n"+strings.Join(pinLines(c, false), "\n"), "\n")
		}
	}
	startOutput(out, h.Source, text, msg)
}

// HintedFile in a repo's ledger root marks that the one-time hint was shown there.
const HintedFile = "hinted"

// startOutput prints SessionStart output: for a hook (with a source), JSON with
// the context for the model and a line for the user; for a manual run, the plain
// context only.
func startOutput(out io.Writer, source, text, msg string) {
	if source == "" {
		if text != "" {
			fmt.Fprintln(out, text)
		}
		return
	}
	v := map[string]any{}
	if msg != "" {
		v["systemMessage"] = msg
	}
	if text != "" {
		v["hookSpecificOutput"] = map[string]string{"hookEventName": "SessionStart", "additionalContext": text}
	}
	if len(v) > 0 {
		fmt.Fprintf(out, "%s\n", Marshal(v))
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

// Compose builds the context block for the ledger in dir, within cfg.BudgetBytes:
// over it, the oldest recent prompts go first, then the oldest decisions, then
// (with a warning at the top) the pins that do not fit.
func Compose(cwd, dir string, cfg Config) string {
	text, _ := compose(cwd, dir, cfg)
	return text
}

// counts is what a compose put back, for the line shown to the user.
type counts struct{ pins, prompts, decisions int }

func (c counts) String() string {
	var parts []string
	for _, x := range []struct {
		n    int
		name string
	}{{c.pins, "pin"}, {c.prompts, "prompt"}, {c.decisions, "decision"}} {
		if x.n == 1 {
			parts = append(parts, "1 "+x.name)
		} else if x.n > 1 {
			parts = append(parts, fmt.Sprintf("%d %ss", x.n, x.name))
		}
	}
	return strings.Join(parts, ", ")
}

func compose(cwd, dir string, cfg Config) (string, counts) {
	var n counts
	header := fmt.Sprintf("## Agent ledger (kept by the user's agent-ledger hooks; full logs in %s)", dir)
	budget := min(cfg.BudgetBytes, MaxBudget) - len(header) - 300
	pins, kept, dropped := fitPins(cwd, dir, budget)
	n.pins = kept
	mark := readMark(dir)
	var prompts []Prompt
	for _, p := range readJSONLFrom[Prompt](filepath.Join(dir, PromptsFile), mark.Prompts) {
		if !isCommand(p.Prompt) {
			prompts = append(prompts, p)
		}
	}
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
			c := func(s string) string {
				if t, was := cut(s, DecisionMax); was {
					return t + " [cut; full text in decisions.jsonl]"
				}
				return s
			}
			dl = append(dl, fmt.Sprintf("- DECISION: %s\n  BECAUSE: %s\n  REJECTED: %s", c(x.Decision), c(x.Because), c(x.Rejected)))
		}
		recent, decs = strings.Join(rl, "\n"), strings.Join(dl, "\n")
		n.prompts, n.decisions = len(rl), len(dl)
		if len(pins)+len(recent)+len(decs)+60 <= budget {
			break
		}
		if r > 0 {
			r--
		} else if d > 0 {
			d--
		} else {
			break
		}
	}
	if pins+recent+decs == "" && dropped == 0 {
		return "", counts{}
	}
	var b strings.Builder
	b.WriteString(header)
	if dropped > 0 {
		fmt.Fprintf(&b, "\nWARNING: %d pinned item(s) did not fit the size limit and are NOT shown. Tell the user to send PINS? and UNPIN the ones that no longer apply.", dropped)
	} else if len(pins) > PinWarnBytes {
		b.WriteString("\nNOTE: the pins are close to the size limit; newer ones may be cut. The user can UNPIN old ones.")
	}
	b.WriteString(pins)
	if recent != "" {
		b.WriteString("\n### Last prompts from the user\n" + recent)
	}
	if decs != "" {
		b.WriteString("\n### Session decisions\n" + decs)
	}
	return capBytes(b.String(), MaxBudget), n
}

// fitPins is the pin sections (this ledger, then branch, repo and global) within
// budget bytes: whole pins in that order until one does not fit. kept and cut
// count the pins shown and left out.
func fitPins(cwd, dir string, budget int) (text string, kept, dropped int) {
	type section struct {
		title string
		lines []string
	}
	secs := []section{{"Pinned by the user (standing rules and concerns, verbatim)", pinLines(sessionPins(dir), true)}}
	for _, s := range PinScopes(cwd, dir) {
		secs = append(secs, section{s.Label, pinLines(ReadJSONL[Pin](filepath.Join(s.Dir, PinnedFile)), false)})
	}
	var b strings.Builder
	for _, s := range secs {
		titled := false
		for _, l := range s.lines {
			add := "\n" + l
			if !titled {
				add = "\n### " + s.title + add
			}
			if dropped > 0 || b.Len()+len(add) > budget {
				dropped++
				continue
			}
			b.WriteString(add)
			titled = true
			kept++
		}
	}
	return b.String(), kept, dropped
}

// capBytes cuts s to at most n bytes on a line (else rune) boundary.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	if i := strings.LastIndexByte(s, '\n'); i > n/2 {
		s = s[:i]
	}
	return s
}

// isCommand reports whether a logged prompt was a ledger command, which does
// not come back as one of the user's last prompts.
func isCommand(prompt string) bool {
	if _, ok := parsePin(prompt); ok {
		return true
	}
	q := strings.TrimSpace(prompt)
	return strings.EqualFold(q, "PINS?") || strings.EqualFold(q, "LEDGER?")
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
	note, ambiguous := TakeCleared(root, h.SessionID, h.Cwd, isNew)
	var msg, ctx []string
	if ambiguous {
		msg = append(msg, "ledger: several chats were /cleared in this folder in the last 10 minutes, so I can't tell which one this was; no pins were carried over. PIN: again what still applies.")
	}
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
			"additionalContext": capBytes("## Agent ledger (after /clear)\n"+strings.Join(ctx, "\n"), MaxBudget)}
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

// NoticesFile in a ledger holds pin changes the model has not been told about:
// UNPIN, PROMOTE and rewording block their prompt, so the next prompt carries them.
const NoticesFile = "notices.jsonl"

func notice(dir, text string) {
	_ = Append(filepath.Join(dir, NoticesFile), Marshal(map[string]string{"ts": stamp(), "text": text}))
}

// takeNotices removes and returns the pending notices as context for the model.
func takeNotices(dir string) string {
	path := filepath.Join(dir, NoticesFile)
	if !fileExists(path) {
		return ""
	}
	var lines []string
	_ = withLock(path, func() error {
		for _, n := range ReadJSONL[map[string]string](path) {
			lines = append(lines, "- "+n["text"])
		}
		return os.Remove(path)
	})
	if len(lines) == 0 {
		return ""
	}
	return capBytes("agent-ledger: the user changed pinned rules since your last turn:\n"+strings.Join(lines, "\n"), 4000)
}
