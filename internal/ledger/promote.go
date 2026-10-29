package ledger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PromoteHeading is the section PROMOTE: adds rules to.
const PromoteHeading = "## Pinned rules (promoted from agent-ledger)"

const maxPromoteBytes = 1 << 20

// findPin returns the one pin matching q (id, or contained text) across the
// ledger and the scoped stores, its file, and the number of matching pins.
func findPin(cwd, dir, q string) (Pin, string, int) {
	var pick Pin
	var at string
	total := 0
	for _, f := range unpinFiles(cwd, dir) {
		seen := map[string]bool{}
		for _, l := range readLines(f) {
			var p Pin
			if json.Unmarshal([]byte(l), &p) == nil && (p.ID == q || strings.Contains(p.Text, q)) && !seen[p.ID] {
				seen[p.ID] = true
				pick, at = p, f
				total++
			}
		}
	}
	return pick, at, total
}

// PromoteTarget is the file PROMOTE: writes in the work tree of cwd: cfg.PromoteTo,
// else AGENTS.md if it exists, else CLAUDE.md if it exists, else AGENTS.md.
func PromoteTarget(cfg Config, cwd string) (string, error) {
	top, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", ErrNoRepo
	}
	name := cfg.PromoteTo
	if name == "" {
		name = "AGENTS.md"
		if _, err := os.Stat(filepath.Join(top, name)); err != nil {
			if _, err := os.Stat(filepath.Join(top, "CLAUDE.md")); err == nil {
				name = "CLAUDE.md"
			}
		}
	}
	return filepath.Join(top, name), nil
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
		if heads[i] && strings.TrimSpace(l) == PromoteHeading {
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
