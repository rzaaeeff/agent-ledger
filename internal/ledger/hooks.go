package ledger

import (
	"crypto/sha1"
	"encoding/hex"
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
//	PIN: <text>      stores <text> in pinned.jsonl; re-injected on every compact of
//	                 this session, dropped (and shown to the user) on /clear
//	UNPIN: <id|text> removes the pin with that id, or the only one containing the text
//
// Plain stdout would reach the model, so it prints only a systemMessage JSON (shown
// to the user) after PIN:/UNPIN:. Problems go to errors.log.
func HookUserPromptSubmit(h HookInput, out io.Writer) {
	if h.Prompt == "" {
		return
	}
	dir, err := Dir(h.Cwd, h.SessionID)
	if err != nil || MkdirPrivate(dir) != nil {
		return
	}
	ts := stamp()
	if Append(filepath.Join(dir, PromptsFile), Marshal(Prompt{ts, h.SessionID, h.Prompt})) != nil {
		LogError(dir, "prompts append failed")
	}
	pins := filepath.Join(dir, PinnedFile)
	switch {
	case strings.HasPrefix(h.Prompt, "PIN:"):
		text := strings.TrimSpace(strings.TrimPrefix(h.Prompt, "PIN:"))
		if text == "" {
			return
		}
		sum := sha1.Sum([]byte(text))
		id := hex.EncodeToString(sum[:])[:6]
		if Append(pins, Marshal(Pin{id, ts, text})) != nil {
			LogError(dir, "pinned append failed")
		}
		emit(out, fmt.Sprintf("pinned [%s]. %s", id, Status(dir)))
	case strings.HasPrefix(h.Prompt, "UNPIN:"):
		text := strings.TrimSpace(strings.TrimPrefix(h.Prompt, "UNPIN:"))
		if text == "" {
			return
		}
		if _, err := os.Stat(pins); err != nil {
			emit(out, "UNPIN did nothing: no pins. "+Status(dir))
			return
		}
		hits, err := Unpin(pins, text)
		switch {
		case err != nil:
			LogError(dir, "unpin failed")
			emit(out, "UNPIN failed, see "+filepath.Join(dir, ErrorsFile))
		case hits == 1:
			emit(out, "unpinned. "+Status(dir))
		default:
			LogError(dir, fmt.Sprintf("UNPIN '%s' matched %d items; nothing removed", text, hits))
			emit(out, fmt.Sprintf("UNPIN did nothing: '%s' matched %d pins (use the [id] from the pin list). %s", text, hits, Status(dir)))
		}
	}
}

// HookSessionStart prints what must survive a compaction: the session's pinned
// rules, last prompts and last decisions. By the hook's source:
//
//	compact  prints everything.
//	clear    adds no context: the pins were dropped, so it only shows the user
//	         which ones, once, as a systemMessage.
//	resume   prints nothing (the transcript is already loaded).
//	startup  deletes this repo's ledgers idle for 30 days, then prints the (new,
//	         usually empty) session's ledger.
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
	switch h.Source {
	case "resume":
		return
	case "clear":
		if h.SessionID != "" {
			cleared(root, h, out)
		}
		return
	}
	dir, err := Dir(h.Cwd, h.SessionID)
	if err != nil {
		return
	}
	if h.Source == "startup" {
		Prune(root, dir)
	}
	if text := Compose(dir); text != "" {
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

// Compose builds the context block for the ledger in dir, within Budget: over it,
// the oldest recent prompts go first, then the oldest decisions. Never pins.
func Compose(dir string) string {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	var pinLines []string
	for _, p := range ReadJSONL[Pin](filepath.Join(dir, PinnedFile)) {
		pinLines = append(pinLines, fmt.Sprintf("- [%s] %s", p.ID, p.Text))
	}
	pins := strings.Join(pinLines, "\n")
	mark := readMark(dir)
	prompts := readJSONLFrom[Prompt](filepath.Join(dir, PromptsFile), mark.Prompts)
	decisions := readJSONLFrom[Decision](filepath.Join(dir, DecisionsFile), mark.Decisions)

	var recent, decs string
	for r, d := RecentPrompts, RecentDecs; ; {
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
		if runes(pins)+runes(recent)+runes(decs) <= Budget {
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
	if pins+recent+decs == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Agent ledger (re-injected after compact; full logs in %s)", dir)
	if pins != "" {
		b.WriteString("\n### Pinned by the user (standing rules and concerns, verbatim)\n" + pins)
	}
	if runes(pins) > PinWarnChars {
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
func cleared(root string, h HookInput, out io.Writer) {
	dir, err := Dir(h.Cwd, h.SessionID)
	if err != nil {
		return
	}
	_, statErr := os.Stat(dir)
	isNew := statErr != nil
	if !isNew {
		markCleared(dir)
	}
	_ = ClearPins(dir, root, h.SessionID, h.Cwd)
	note := TakeCleared(root, h.SessionID, h.Cwd, isNew)
	if note == nil {
		return
	}
	var lines []string
	for _, p := range note.Pins {
		text, was := cut(p.Text, 300)
		if was {
			text += "..."
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", p.ID, text))
	}
	emit(out, fmt.Sprintf("ledger: /clear dropped %d pin(s); PIN: again any that still apply (archived in %s):\n%s",
		len(note.Pins), filepath.Join(note.Dir, ClearedFile), strings.Join(lines, "\n")))
}

// HookSessionEnd drops the session's pins on /clear (reason "clear"), so a cleared
// chat starts without them; other reasons keep them. Prints nothing and does only
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
	if dir, err := Dir(h.Cwd, h.SessionID); err == nil {
		markCleared(dir)
		_ = ClearPins(dir, root, h.SessionID, h.Cwd)
	}
}
