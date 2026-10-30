package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Slash commands are aliases of the prefixes: Claude Code's /agent-ledger:pin
// (also typed as /pin) and Codex's $pin. They are always blocked: letting one
// through would hand the skill's text to the model.

var slashVerbs = map[string]string{
	"pin": "PIN", "pins": "PINS?", "unpin": "UNPIN", "carry": "CARRY", "promote": "PROMOTE", "help": "LEDGER?",
}

// slashPrompt turns a slash command into the prefix it stands for.
func slashPrompt(verb, args string) (string, bool) {
	p, ok := slashVerbs[verb]
	if !ok {
		return "", false
	}
	args = strings.TrimSpace(args)
	switch {
	case strings.HasSuffix(p, "?"):
		return p, true
	case verb == "pin" && args == "":
		return "PINS?", true
	case verb == "pin":
		// "! text", "@repo text", "!@repo text", "[ref] text"
		if args == "!" || strings.HasPrefix(args, "! ") || strings.HasPrefix(args, "!\t") || strings.HasPrefix(args, "!@") {
			p += "!"
			args = strings.TrimSpace(args[1:])
		}
		if strings.HasPrefix(args, "@") {
			scope, rest, _ := strings.Cut(args[1:], " ")
			p += "@" + scope
			args = strings.TrimSpace(rest)
		}
		if strings.HasPrefix(args, "[") {
			if ref, rest, ok := strings.Cut(args[1:], "]"); ok {
				p += " [" + ref + "]"
				args = strings.TrimSpace(rest)
			}
		}
	}
	return p + ": " + args, true
}

func slashMultiLine(name string) string {
	return name + " takes one line; nothing was saved. To pin and ask in one message, start the first line with PIN: (or UNPIN:, CARRY:) and put your request on the next lines."
}

// slashOut runs a translated command and blocks with whatever it reported.
// UserPromptExpansion's block also needs suppressOriginalPrompt (Claude Code).
type slashOut struct {
	w     io.Writer
	event string
}

func (s slashOut) block(msg string) {
	v := map[string]any{"decision": "block", "reason": msg}
	if s.event != "" {
		v["hookSpecificOutput"] = map[string]any{"hookEventName": s.event, "suppressOriginalPrompt": true}
	}
	fmt.Fprintf(s.w, "%s\n", Marshal(v))
}

func (s slashOut) run(h HookInput, prompt string) {
	// Only a valid command may reach the handler: anything else would drain the
	// model's pending notices into a prompt that is blocked anyway.
	if !isCommand(prompt) {
		s.block("ledger: nothing saved: a scope is @branch, @repo or @global, followed by a space and the text.")
		return
	}
	h.Prompt = prompt
	var buf bytes.Buffer
	HookUserPromptSubmit(h, &buf)
	msg := ""
	for _, line := range strings.Split(buf.String(), "\n") {
		var v map[string]any
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		for _, k := range []string{"reason", "systemMessage"} {
			if m, _ := v[k].(string); m != "" && msg == "" {
				msg = m
			}
		}
	}
	if msg == "" {
		msg = "agent-ledger: nothing to do."
	}
	s.block(msg)
}

// HookUserPromptExpansion handles /agent-ledger:<verb> in Claude Code. It is
// keyed on command_name, so a user's own /pin skill is left alone.
func HookUserPromptExpansion(h HookInput, out io.Writer) {
	if h.bad {
		return
	}
	name, _ := h.raw["command_name"].(string)
	args, _ := h.raw["command_args"].(string)
	if src, ok := h.raw["command_source"].(string); ok && src != "plugin" {
		return
	}
	verb, ok := strings.CutPrefix(name, "agent-ledger:")
	if !ok {
		return
	}
	prompt, ok := slashPrompt(verb, args)
	if !ok {
		return
	}
	s := slashOut{out, "UserPromptExpansion"}
	if strings.ContainsAny(strings.TrimSpace(args), "\r\n") {
		s.block(slashMultiLine("/agent-ledger:" + verb))
		return
	}
	s.run(h, prompt)
}

// ownSlashRe matches this plugin's slash commands as typed (bare or namespaced).
var ownSlashRe = regexp.MustCompile(`(?i)^/(agent-ledger:)?(pin|pins|unpin|carry|promote|help)(?:\s|$)`)

var dollarRe = regexp.MustCompile(`(?i)^\$(agent-ledger:)?(pin|pins|unpin|carry|promote|help)(?:[ \t]+|$|\r?\n)`)

// codexSlash handles $pin, $agent-ledger:pin and friends at the start of a Codex
// prompt. It reports whether it handled the prompt.
func codexSlash(h HookInput, out io.Writer) bool {
	q := strings.TrimLeft(h.Prompt, " \t\r\n")
	m := dollarRe.FindStringSubmatch(q)
	if m == nil || (strings.EqualFold(m[2], "help") && m[1] == "") {
		return false
	}
	verb := strings.ToLower(m[2])
	prompt, _ := slashPrompt(verb, q[len(m[0]):])
	s := slashOut{out, ""}
	if strings.ContainsAny(strings.TrimSpace(q), "\r\n") {
		s.block(slashMultiLine("$" + verb))
		return true
	}
	s.run(h, prompt)
	return true
}
