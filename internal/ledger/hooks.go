package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	PinWarnBytes  = 6000
	RecentPrompts = 5
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
	bad       bool // not a JSON object, or a session_id that is not a string or number
}

// ReadHookInput parses the hook JSON. Invalid input yields an empty HookInput.
func ReadHookInput(r io.Reader) HookInput {
	var h HookInput
	data, _ := io.ReadAll(r)
	if json.Unmarshal(data, &h.raw) != nil || h.raw == nil {
		h.raw, h.bad = map[string]any{}, true
	}
	str := func(k string) string { s, _ := h.raw[k].(string); return s }
	h.Prompt, h.Cwd, h.SessionID, h.Source, h.Reason = str("prompt"), str("cwd"), str("session_id"), str("source"), str("reason")
	// Claude Code wraps a long or pasted prompt in a <pasted_content> block, which
	// hid the command at the start of it.
	h.Prompt = Unwrap(h.Prompt)
	switch v := h.raw["session_id"].(type) {
	case float64:
		h.SessionID = strconv.FormatFloat(v, 'f', -1, 64)
	case string, nil:
	default:
		// Never fall back to the shared branch ledger for an id we can't read.
		h.bad = true
	}
	if h.Cwd == "" {
		h.Cwd, _ = os.Getwd()
	}
	return h
}

func emit(w io.Writer, msg string) {
	fmt.Fprintf(w, "%s\n", Marshal(map[string]string{"systemMessage": msg}))
}

// tellAgent shows msg to the user and gives the model ctx. Used where the prompt
// still reaches the model after nothing was stored: the refusal is a system
// message the model never sees, so without ctx it confirms a pin that does not
// exist ("Pinned: nonsense"). A blockWriter leaves this alone, so a lone command
// is still blocked and the model gets no turn at all.
func tellAgent(w io.Writer, msg, ctx string) {
	fmt.Fprintf(w, "%s\n", Marshal(map[string]any{"systemMessage": msg,
		"hookSpecificOutput": map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": ctx}}))
}

// clearProofHint names the scopes that really do survive /clear where the user
// is standing: PIN@branch: is refused outside a repo and on a default branch, so
// suggesting it there sent people into a refusal.
func clearProofHint(cwd, dir string) string {
	if IsFolder(filepath.Dir(dir)) {
		return "PIN@global:"
	}
	if b, _ := git(cwd, "branch", "--show-current"); b != "" && !DefaultBranches(cwd)[b] {
		return "PIN@repo: or PIN@branch:"
	}
	return "PIN@repo:"
}

// notSaved names a command that could not be carried out. "NOT SAVED" fits PIN,
// but an UNPIN or a PROMOTE saves nothing in the first place.
func notSaved(verb string) string {
	if verb == "PIN" || verb == "CARRY" {
		return verb + " NOT SAVED"
	}
	return verb + " FAILED"
}

// settingRe is the shape of a setting name, so a problem that names no setting
// (a whole file that would not parse) is not mistaken for one.
var settingRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ignoredList names the settings agent-ledger would not take, so the one-line
// session note is enough on its own for the common case of a single typo.
func ignoredList(problems []string) string {
	var keys []string
	seen := map[string]bool{}
	for _, p := range problems {
		// Each problem reads "<config file or env>: <setting>=... ignored ...", or
		// names no setting at all ("not valid JSON").
		_, rest, ok := strings.Cut(p, ": ")
		if !ok {
			continue
		}
		rest = strings.TrimPrefix(rest, "unknown key ")
		name := strings.Trim(strings.Fields(rest + " ")[0], `":,`)
		if k, _, ok := strings.Cut(name, "="); ok {
			name = strings.Trim(k, `"`)
		}
		if !settingRe.MatchString(name) || seen[name] {
			continue
		}
		seen[name] = true
		keys = append(keys, name)
	}
	switch {
	case len(keys) == 0:
		return "some settings"
	case len(keys) <= 3:
		return "some settings (" + strings.Join(keys, ", ") + ")"
	default:
		return fmt.Sprintf("some settings (%s and %d more)", strings.Join(keys[:3], ", "), len(keys)-3)
	}
}

// PinNote is what the model gets with a stored PIN: without it, agents tend to
// copy the rule into auto memory or AGENTS.md, where UNPIN and /clear can't reach it.
const PinNote = "agent-ledger keeps the user's pins and puts them back after every compaction for as long as they stay pinned; the user removes them with UNPIN. Do not save pins to memory, CLAUDE.md, AGENTS.md or any other file."

// pinned reports a stored PIN to the user. A PIN alone is blocked (no model
// turn) and the model gets news, if any, with the next prompt; a PIN with more
// lines goes on, with news and PinNote as context. CARRY gets neither: it is
// blocked, or its own note says not to act on it yet.
func pinned(w io.Writer, dir, msg, verb string, pass bool, news string) {
	switch {
	case verb != "PIN":
		emit(w, msg)
	case pass:
		ctx := "agent-ledger: the PIN line in this prompt is stored. " + PinNote
		if news != "" {
			ctx = "agent-ledger: " + news + " " + PinNote
		}
		fmt.Fprintf(w, "%s\n", Marshal(map[string]any{"systemMessage": msg,
			"hookSpecificOutput": map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": ctx}}))
	default:
		if news != "" {
			notice(dir, news)
		}
		emit(w, msg)
	}
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
//	PIN@global: <text> everywhere
//	UNPIN: <ref>       removes the one pin ref selects, from any of those stores:
//	                   an id, its number in PINS?, "last", or words in it in any
//	                   order (case and punctuation ignored); else lists the closest
//	PROMOTE: <ref>     moves that pin into the repo's CLAUDE.md or AGENTS.md (or
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
// afterCmdLine returns what follows a ledger command's line, if anything. It
// skips the same leading blank space parsePin does, so a prompt that starts
// with a newline still counts as command-only.
func afterCmdLine(prompt string) string {
	_, rest, _ := strings.Cut(strings.TrimLeft(prompt, " \t\r\n"), "\n")
	return strings.TrimSpace(rest)
}

// cmdLine is the command itself: the first line, without the ordinary prompt that
// may follow it. Under pins-only that is all that may be logged.
func cmdLine(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimLeft(prompt, " \t\r\n"), "\n")
	return strings.TrimRight(line, " \t\r")
}

func HookUserPromptSubmit(h HookInput, out io.Writer) {
	if h.Prompt == "" || h.bad {
		return
	}
	// Our slash commands are handled by UserPromptExpansion, keyed on the
	// command's real name (acting on "/pin" here would hijack a user's own /pin
	// skill), and blocked there: pending notices must wait for the next prompt.
	if ownSlashRe.MatchString(strings.TrimLeft(h.Prompt, " \t\r\n")) {
		return
	}
	_, codex := h.raw["turn_id"]
	if codex && codexSlash(h, out) {
		return
	}
	if !codex {
		out = claudeWriter{out}
	}
	cfg := LoadConfig(h.Cwd)
	dir, err := DirFor(cfg, h.Cwd, h.SessionID)
	if err != nil {
		// A folder with no repository gets its own ledger, so the only way here is
		// git failing on a folder that does look like one.
		why := "agent-ledger could not open its ledger: " + err.Error()
		if errors.Is(err, ErrNoRepo) {
			why = "git could not open this folder as a repository, so nothing is stored here. " + strings.TrimPrefix(err.Error(), ErrNoRepo.Error()+"; ")
		}
		q := strings.TrimSpace(h.Prompt)
		_, isPin := parsePin(h.Prompt)
		rest := afterCmdLine(h.Prompt)
		switch {
		case strings.EqualFold(q, "PINS?"), strings.EqualFold(q, "LEDGER?"):
			block(out, why)
		case isPin && rest == "":
			// The command is the whole prompt: block it, as a working ledger
			// would, so the agent is never asked to act on "PIN: ...".
			block(out, "ledger: nothing saved: "+why)
		case isPin:
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
		pins := listPins(h.Cwd, dir, here)
		msg := PinsList(pins, codex)
		if codex {
			msg += CodexClearNote(pins)
		}
		block(out, msg)
		return
	case strings.EqualFold(q, "LEDGER?"):
		block(out, HelpText(Status(dir), IsFolder(filepath.Dir(dir)), codex))
		return
	}
	pinsHere := func() string { return brief(listPins(h.Cwd, dir, here), codex) }
	prompt, masked := h.Prompt, 0
	if cfg.Redact == "on" {
		prompt, masked = Redact(prompt)
	}
	cmd, ok := parsePin(prompt)
	ts := stamp()
	if cfg.Logging == "all" || (cfg.Logging == "pins-only" && ok) {
		logged := prompt
		if cfg.Logging == "pins-only" {
			// Only the command is a ledger command. The rest is an ordinary prompt,
			// which pins-only promises not to keep.
			logged = cmdLine(prompt)
		}
		if MkdirPrivate(dir) != nil {
			if ok && cmd.text != "" {
				msg := notSaved(cmd.verb) + ": could not create " + dir + " (check permissions and disk space)."
				w := out
				if afterCmdLine(prompt) == "" {
					w = blockWriter{out}
				}
				tellAgent(w, msg, "agent-ledger: the "+cmd.verb+" line in this prompt was NOT stored (the ledger folder could not be created). Do not tell the user it was saved.")
			}
			return
		}
		if Append(filepath.Join(dir, PromptsFile), Marshal(Prompt{ts, h.SessionID, logged})) != nil {
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
		ctx := takeNotices(dir)
		if hint := nearMiss(prompt); hint != "" {
			// The near miss is shown to the user only. Say so, or the model reads the
			// prompt as a command and confirms a pin that was never stored.
			ctx = strings.TrimSpace(ctx + " agent-ledger: this prompt looks like a ledger command but is not one, so nothing was pinned. Treat it as an ordinary prompt and do not confirm a pin.")
		}
		if ctx != "" {
			v["hookSpecificOutput"] = map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": ctx}
		}
		if len(v) > 0 {
			fmt.Fprintf(out, "%s\n", Marshal(v))
		}
		return
	}
	// A command that is the whole prompt never reaches the model, even when it
	// fails: otherwise the agent is handed "PIN: ..." and tries to act on it.
	if afterCmdLine(prompt) == "" {
		out = blockWriter{out}
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
		emit(out, notSaved(cmd.verb)+": could not create "+dir+" (check permissions and disk space).")
		return
	}
	out = &suffixWriter{w: out, suffix: note}
	carry := cmd.verb == "CARRY" && cmd.ref == ""
	pass := false
	if afterCmdLine(prompt) != "" {
		// More than the command line: don't block, the rest is meant for the agent.
		p := passWriter{w: out}
		if carry {
			p.ctx = "agent-ledger: the first line of this prompt (CARRY: ...) is the user's note for the chat after the next /clear. Do not act on it now; the rest of the prompt is for you."
		}
		out, pass = p, true
	} else if cmd.verb == "PIN" || carry {
		// A PIN alone costs no model turn: the model gets it with the next prompt.
		// CARRY is for the chat after /clear: this one must not act on it now.
		out = blockWriter{out}
	}
	if cmd.ref != "" {
		rewordPin(h, dir, here, cmd, codex, out)
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
			if codex {
				where = " for the chat after the next /clear, but Codex starts a chat it cannot link, so it will not arrive there; use PIN@branch: or PIN@repo: instead"
			}
		case cmd.scope == "" && cmd.keep:
			life, where = LifeKeep, " (kept across /clear)"
			if codex {
				where = " (kept across /clear in Claude Code only: Codex starts a chat it cannot link, so use " + clearProofHint(h.Cwd, dir) + " here instead)"
			}
		case cmd.scope == ScopeBranch && IsFolder(filepath.Dir(dir)):
			emit(out, "PIN@branch refused: this folder is not in a git repository, so there is no branch. Use PIN: for this chat or PIN@repo: for this folder.")
			return
		case cmd.scope == ScopeBranch:
			b, _ := git(h.Cwd, "branch", "--show-current")
			if b == "" || DefaultBranches(h.Cwd)[b] {
				why := b + " (a default branch is shared by everyone, so PIN@branch: would follow work that is not yours)"
				if b == "" {
					why = "a detached HEAD (there is no branch to keep them for)"
				}
				emit(out, fmt.Sprintf("PIN@branch refused on %s. Use PIN: for this chat or PIN@repo: for the repo.", why))
				return
			}
			target, where = filepath.Join(filepath.Dir(dir), branchKey(b)), " for branch "+b
		case cmd.scope == ScopeRepo:
			target, where = filepath.Join(filepath.Dir(dir), RepoKey), " for this repo"
			if IsFolder(filepath.Dir(dir)) {
				where = " for this folder"
			}
		case cmd.scope == ScopeGlobal:
			target, where = GlobalDir(), " everywhere"
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
		id = uniqueID(file, id, cmd.text)
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
				pinned(out, dir, msg+". "+pinsHere(), cmd.verb, pass, "")
				return
			case p.ID != id && looksSimilar(p.Text, cmd.text):
				similar = append(similar, p)
			}
		}
		if MkdirPrivate(target) != nil || Append(file, Marshal(Pin{id, Now().Format(time.RFC3339Nano), cmd.text, life})) != nil {
			LogError(dir, "pinned append failed")
			tellAgent(out, notSaved(cmd.verb)+": could not write "+file+" (check permissions and disk space).",
				"agent-ledger: the "+cmd.verb+" line in this prompt was NOT stored (the ledger could not be written). Do not tell the user it was saved.")
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
			budget := min(cfg.BudgetBytes, MaxBudget)
			if n > budget {
				msg += fmt.Sprintf(" Warning: pins here now take %d bytes, more than the %d that come back after compaction, so some will be left out; UNPIN the ones that no longer apply.", n, budget)
			} else {
				msg += fmt.Sprintf(" Warning: pins here now take %d of the %d bytes that come back after compaction; UNPIN the ones that no longer apply.", n, budget)
			}
		}
		pinned(out, dir, msg+" "+pinsHere(), cmd.verb, pass, fmt.Sprintf("The user pinned [%s]%s: %q. It applies from now on.", id, where, cmd.text))
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
		if errors.Is(err, ErrNoRepo) {
			// Not an error: there is simply no work tree to write the file into. Say
			// what to do instead, as every other refusal does, and do not log it.
			block(out, "PROMOTE refused: this folder is not in a git repository, so there is no CLAUDE.md or AGENTS.md to write to. The pin is still here; PIN@repo: keeps it for every chat in this folder.")
			return
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
		msg := fmt.Sprintf("promoted [%s] to %s; %s. Not committed: the file is untracked, so review it with git status and git add -N before git diff.", p.ID, filepath.Base(target), unpinned)
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
	if len(hits) > 1 {
		if len(hits) > 5 {
			hits = hits[:5]
		}
		msg = fmt.Sprintf("%s did nothing: %q matches several pins: %s.", verb, ref, refList(hits))
	} else if c := closest(ref, all, 3); len(c) > 0 {
		msg += " Closest: " + refList(c) + "."
	} else {
		// Nothing shares enough words to be "closest". Name some pins anyway: a
		// refusal with nothing to act on is why you have to send PINS? next.
		some := all
		if len(some) > 3 {
			some = some[:3]
		}
		msg += " Pinned here: " + refList(some) + "."
	}
	return located{}, msg + fmt.Sprintf(" Send %s: <id>, or PINS? to list them.", verb)
}

// rewordPin handles PIN [ref]: <text>: the pin keeps its store and lifetime and
// gets the id of its new text.
func rewordPin(h HookInput, dir, here string, cmd pinCmd, codex bool, out io.Writer) {
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
	p := Pin{uniqueID(hit.File, pinID(scope, cmd.text), cmd.text), Now().Format(time.RFC3339Nano), cmd.text, hit.Life}
	switch ok, err := replacePin(hit.File, hit.Pin, p); {
	case err != nil:
		LogError(dir, "reword failed")
		block(out, "PIN NOT SAVED: could not reword ["+hit.ID+"], see "+filepath.Join(dir, ErrorsFile))
	case !ok:
		block(out, fmt.Sprintf("PIN NOT SAVED: [%s] was removed meanwhile.", hit.ID))
	default:
		notice(dir, fmt.Sprintf("The user reworded pin [%s] %q; it is now [%s] %q.", hit.ID, hit.Text, p.ID, p.Text))
		block(out, fmt.Sprintf("reworded [%s] -> [%s] (%s). %s", hit.ID, p.ID, hit.Where, brief(listPins(h.Cwd, dir, here), codex)))
	}
}

// claudeWriter adds suppressOriginalPrompt to a block, so Claude Code does not
// echo the prompt under the reply. Codex rejects unknown fields there.
type claudeWriter struct{ w io.Writer }

func (c claudeWriter) Write(b []byte) (int, error) {
	var v map[string]any
	if json.Unmarshal(b, &v) == nil && v["decision"] == "block" && v["hookSpecificOutput"] == nil {
		v["hookSpecificOutput"] = map[string]any{"hookEventName": "UserPromptSubmit", "suppressOriginalPrompt": true}
		_, err := fmt.Fprintf(c.w, "%s\n", Marshal(v))
		return len(b), err
	}
	return c.w.Write(b)
}

// blockWriter turns a systemMessage into a block: the user sees it, the model
// gets nothing.
type blockWriter struct{ w io.Writer }

func (k blockWriter) Write(b []byte) (int, error) {
	var v map[string]string
	if json.Unmarshal(b, &v) == nil && v["systemMessage"] != "" && len(v) == 1 {
		block(k.w, v["systemMessage"])
		return len(b), nil
	}
	return k.w.Write(b)
}

// passWriter turns a block into a plain systemMessage, so the prompt still
// reaches the agent.
// With ctx, it also tells the model ctx.
type passWriter struct {
	w   io.Writer
	ctx string
}

func (p passWriter) Write(b []byte) (int, error) {
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		return p.w.Write(b)
	}
	if v["decision"] == "block" {
		v = map[string]any{"systemMessage": v["reason"]}
	}
	if p.ctx != "" && v["systemMessage"] != nil && v["hookSpecificOutput"] == nil {
		v["hookSpecificOutput"] = map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": p.ctx}
	}
	_, err := fmt.Fprintf(p.w, "%s\n", Marshal(v))
	return len(b), err
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
	var v map[string]any
	if json.Unmarshal(b, &v) == nil {
		for _, k := range []string{"systemMessage", "reason"} {
			if m, _ := v[k].(string); m != "" {
				v[k] = m + s.suffix
				_, err := fmt.Fprintf(s.w, "%s\n", Marshal(v))
				return len(b), err
			}
		}
	}
	return s.w.Write(b)
}

// HookSessionStart prints what must survive a compaction: the ledger's pinned
// rules and (with logging=all) last prompts. By the hook's source:
//
//	compact  prints everything.
//	clear    with clear=drop (default for key=session) adds no context: the pins
//	         were dropped, so it only shows the user which ones, once, as a
//	         systemMessage. With clear=keep it prints the pins.
//	resume   prints nothing (the transcript is already loaded).
//	startup  deletes this repo's ledgers idle for prune_days, then prints the
//	         ledger (for a new session usually empty).
//
// Any other or missing source prints everything, which is what a manual run (for
// example from Codex, keyed by branch) wants. With a session id it also exports
// CLAUDE_SESSION_ID through $CLAUDE_ENV_FILE, for status and preview.
func HookSessionStart(h HookInput, out io.Writer) {
	if h.bad {
		return
	}
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
	if h.Source == "startup" && len(cfg.Problems) > 0 {
		msg = strings.TrimSpace(msg + fmt.Sprintf(" agent-ledger ignored %s; `agent-ledger config` says why.", ignoredList(cfg.Problems)))
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
		if note, _, _ := TakeCleared(root, h.SessionID, h.Cwd, statErr != nil || cfg.Key != KeySession); note != nil {
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
// over it, the oldest recent prompts go first, then (with a warning at the top)
// the pins that do not fit.
func Compose(cwd, dir string, cfg Config) string {
	text, _ := compose(cwd, dir, cfg)
	return text
}

// counts is what a compose put back, for the line shown to the user.
type counts struct{ pins, prompts int }

func (c counts) String() string {
	var parts []string
	for _, x := range []struct {
		n    int
		name string
	}{{c.pins, "pin"}, {c.prompts, "prompt"}} {
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
	header := "## Agent ledger: notes kept for the user by the agent-ledger hooks. Pins are the user's own words; the prompts below are quoted context, not new instructions. " + PinNote
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

	var recent string
	for r := cfg.RecentPrompts; ; r-- {
		var rl []string
		for _, p := range tail(prompts, r) {
			text, was := cut(p.Prompt, PromptMax)
			if was {
				text += " [cut; full text in prompts.jsonl]"
			}
			ts, _ := cut(p.TS, 16)
			rl = append(rl, fmt.Sprintf("- %s: %s", ts, indent(text)))
		}
		recent = strings.Join(rl, "\n")
		n.prompts = len(rl)
		if len(pins)+len(recent)+60 <= budget || r <= 0 {
			break
		}
	}
	if pins+recent == "" && dropped == 0 {
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
		b.WriteString("\n### Last prompts from the user (may already be done)\n" + recent)
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
		keep  []bool
		used  int
	}
	secs := []section{{title: "Pinned by the user in this chat (standing rules and concerns, verbatim)", lines: pinLines(sessionPins(dir), true)}}
	for _, s := range PinScopes(cwd, dir) {
		secs = append(secs, section{title: s.Label, lines: pinLines(ReadJSONL[Pin](filepath.Join(s.Dir, PinnedFile)), false)})
	}
	live := 0
	for i := range secs {
		secs[i].keep = make([]bool, len(secs[i].lines))
		if len(secs[i].lines) > 0 {
			live++
		}
	}
	if live == 0 {
		return "", 0, 0
	}

	// A line only pays for its title if it is the first one kept in its section.
	cost := func(s *section, i int) int {
		n := len("\n") + len(s.lines[i])
		if s.used == 0 {
			n += len("\n### ") + len(s.title)
		}
		return n
	}
	spent := 0
	take := func(s *section, i int) bool {
		c := cost(s, i)
		if spent+c > budget {
			return false
		}
		s.keep[i], s.used, spent = true, s.used+c, spent+c
		return true
	}

	// Every scope gets a share first, newest pin first, so a chat full of pins can
	// never push out every repo and global pin. Whatever is left over then goes to
	// the rest, again newest first, so the budget is still spent in full.
	share := budget / live
	for i := range secs {
		s := &secs[i]
		for j := len(s.lines) - 1; j >= 0; j-- {
			if s.used+cost(s, j) > share || !take(s, j) {
				break
			}
		}
	}
	for i := range secs {
		s := &secs[i]
		for j := len(s.lines) - 1; j >= 0; j-- {
			if !s.keep[j] {
				take(s, j)
			}
		}
	}

	var b strings.Builder
	for i := range secs {
		s := &secs[i]
		titled := false
		for j, l := range s.lines {
			if !s.keep[j] {
				dropped++
				continue
			}
			if !titled {
				b.WriteString("\n### " + s.title)
				titled = true
			}
			b.WriteString("\n" + l)
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
	_, _ = ClearPins(dir, root, h.SessionID, h.Cwd)
	note, ambiguous, reported := TakeCleared(root, h.SessionID, h.Cwd, isNew)
	var msg, ctx []string
	if ambiguous != nil {
		m := "ledger: several chats were /cleared in this folder in the last 10 minutes, so I can't tell which one this was; no pins were carried over."
		if lost := append(append([]Pin{}, ambiguous.Kept...), ambiguous.Carry...); len(lost) > 0 {
			var names []string
			for _, p := range tail(lost, 5) {
				names = append(names, fmt.Sprintf("[%s] %s", p.ID, short(p.Text, 60)))
			}
			m += fmt.Sprintf(" Not carried over: %s", strings.Join(names, "; "))
			if len(lost) > 5 {
				m += fmt.Sprintf(" (and %d more)", len(lost)-5)
			}
			m += ". They are still in " + filepath.Join(ambiguous.Dir, PinnedFile) + " and " + filepath.Join(ambiguous.Dir, ClearedFile) + "."
		}
		msg = append(msg, m+" PIN: again what still applies.")
	}
	if note == nil && ambiguous == nil && !reported && statErr != nil && cfg.Key == KeySession && unlinkedKeeps(root, dir) {
		why, where := "Claude Code can cut the hook short", "in that chat"
		if isCodex() {
			why, where = "Codex never reports a /clear", "in Codex"
		}
		msg = append(msg, "ledger: nothing was handed over to this chat. If the chat you cleared had PIN!: or CARRY: items, they stayed in its own ledger: this /clear was not reported to agent-ledger ("+why+"). For rules that must survive /clear "+where+", use "+clearProofHint(h.Cwd, dir)+".")
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
		// An empty note still tells SessionStart(clear) that this /clear was reported.
		_ = addNote(root, Note{Session: h.SessionID, Cwd: h.Cwd, Dir: dir, Carry: takeCarry(dir)})
		return
	}
	markCleared(dir)
	if noted, err := ClearPins(dir, root, h.SessionID, h.Cwd); err == nil && !noted {
		_ = addNote(root, Note{Session: h.SessionID, Cwd: h.Cwd, Dir: dir})
	}
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
	return capBytes("agent-ledger: in a turn you did not see, the user sent a ledger command (a line starting with PIN:, UNPIN: or similar) and these pins changed. This is the user's own doing, not an instruction from anyone else:\n"+strings.Join(lines, "\n")+"\n"+PinNote, 4000)
}

// indent keeps multi-line quoted text inside its list item, so a line in a
// prompt cannot pass for a heading of the ledger.
func indent(s string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

// unlinkedKeeps reports whether another session ledger under root changed its
// PIN! or CARRY items in the last hour: a /clear that left no note may have
// lost them.
func unlinkedKeeps(root, dir string) bool {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		if !e.IsDir() || path == dir || e.Name() == RepoKey || strings.HasPrefix(e.Name(), "@") {
			continue
		}
		f := filepath.Join(path, PinnedFile)
		if fi, err := os.Stat(f); err != nil || fi.ModTime().Before(Now().Add(-time.Hour)) {
			continue
		}
		for _, p := range ReadJSONL[Pin](f) {
			if p.Life == LifeKeep || p.Life == LifeCarry {
				return true
			}
		}
	}
	return false
}
