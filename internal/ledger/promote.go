package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PromoteHeading is the section PROMOTE: adds rules to.
// It does not say "pinned": agents took that as the place to write pins to.
const PromoteHeading = "## Project rules"

// oldPromoteHeading is the v0.1.0-v0.1.1 heading; rules still go under it if present.
const oldPromoteHeading = "## Pinned rules (promoted from agent-ledger)"

const maxPromoteBytes = 1 << 20

// PromoteTarget is the file PROMOTE: writes in the work tree of cwd: cfg.PromoteTo;
// else the one of AGENTS.md and CLAUDE.md that exists; with both, AGENTS.md if
// CLAUDE.md imports or links to it (both agents then read it) or under Codex,
// else CLAUDE.md, the only one Claude Code reads when both exist; with neither,
// AGENTS.md, which both read.
func PromoteTarget(cfg Config, cwd string) (string, error) {
	top, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", ErrNoRepo
	}
	if cfg.PromoteTo != "" {
		return filepath.Join(top, cfg.PromoteTo), nil
	}
	agents, claude := filepath.Join(top, "AGENTS.md"), filepath.Join(top, "CLAUDE.md")
	_, aErr := os.Lstat(agents)
	_, cErr := os.Lstat(claude)
	switch {
	case cErr != nil:
		return agents, nil
	case aErr != nil:
		return claude, nil
	case importsAgents(claude), os.Getenv("CODEX_THREAD_ID") != "" && os.Getenv("CLAUDE_PROJECT_DIR") == "":
		return agents, nil
	}
	return claude, nil
}

// importsAgents reports whether CLAUDE.md links to AGENTS.md or imports it (@AGENTS.md).
func importsAgents(claude string) bool {
	if t, err := os.Readlink(claude); err == nil {
		return filepath.Base(t) == "AGENTS.md"
	}
	fi, err := os.Stat(claude)
	if err != nil || fi.Size() > maxPromoteBytes {
		return false
	}
	data, _ := os.ReadFile(claude)
	return regexp.MustCompile(`(?m)(^|\s)@(\./)?AGENTS\.md\b`).Match(data)
}

// addRule puts "- text" at the end of the PromoteHeading section of path, adding
// the section at the end if missing. It refuses symlinks and non-regular or large
// files; a rule already present is not added twice (added reports which).
func addRule(path, text string) (added bool, err error) {
	text = strings.Join(strings.Fields(text), " ")
	var data []byte
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() || fi.Size() > maxPromoteBytes {
			return false, errors.New(filepath.Base(path) + " is not a regular file under 1 MiB")
		}
		if data, err = os.ReadFile(path); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	rule := "- " + text
	nl := "\n"
	if strings.Contains(string(data), "\r\n") {
		nl = "\r\n"
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	heads := mdHeadings(lines)
	start := -1
	for i, l := range lines {
		if t := strings.TrimSpace(l); heads[i] && (t == PromoteHeading || t == oldPromoteHeading) {
			start = i
		}
	}
	if start < 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, PromoteHeading, "", rule)
	} else {
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			if heads[i] {
				end = i
				break
			}
		}
		for _, l := range lines[start+1 : end] {
			if strings.TrimSpace(l) == rule {
				return false, nil
			}
		}
		at := end
		for at > start+1 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		ins := []string{rule}
		if at == start+1 {
			ins = []string{"", rule}
		}
		lines = append(lines[:at], append(ins, lines[at:]...)...)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Lstat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	// CreateTemp uses O_EXCL with a random name, so a planted symlink is never followed.
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-ledger-*.tmp")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	_, werr := f.WriteString(strings.Join(lines, nl) + nl)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, mode)
	}
	if werr == nil {
		if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
			werr = errors.New(filepath.Base(path) + " is not a regular file")
		}
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		os.Remove(tmp)
		return false, werr
	}
	return true, nil
}

var headingRe = regexp.MustCompile(`^ {0,3}#{1,6}(\s|$)`)
var fenceRe = regexp.MustCompile("^ {0,3}(```|~~~)")

// mdHeadings marks lines that are Markdown ATX headings outside fenced code blocks.
func mdHeadings(lines []string) []bool {
	out := make([]bool, len(lines))
	fence := ""
	for i, l := range lines {
		if m := fenceRe.FindStringSubmatch(l); m != nil {
			if fence == "" {
				fence = m[1]
			} else if fence == m[1] {
				fence = ""
			}
			continue
		}
		out[i] = fence == "" && headingRe.MatchString(l)
	}
	return out
}
