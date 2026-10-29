package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names inside a session ledger.
const (
	PromptsFile   = "prompts.jsonl"
	PinnedFile    = "pinned.jsonl"
	DecisionsFile = "decisions.jsonl"
	ErrorsFile    = "errors.log"
	ClearedFile   = "pinned.cleared.jsonl"
)

// Pin is one pinned rule, stored verbatim.
type Pin struct {
	ID   string `json:"id"`
	TS   string `json:"ts,omitempty"`
	Text string `json:"text"`
	Life string `json:"life,omitempty"` // "" | keep (PIN!) | carry (CARRY)
}

// Prompt is one logged user prompt.
type Prompt struct {
	TS      string `json:"ts"`
	Session string `json:"session"`
	Prompt  string `json:"prompt"`
}

// Decision is one recorded session decision.
type Decision struct {
	TS       string `json:"ts"`
	Decision string `json:"decision"`
	Because  string `json:"because"`
	Rejected string `json:"rejected"`
}

// Now is the clock; tests replace it.
var Now = time.Now

func stamp() string { return Now().Format("2006-01-02T15:04:05-07:00") }

// Marshal encodes v as one compact JSON line, without HTML escaping.
func Marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// MkdirPrivate creates dir (and parents) readable by the owner only.
func MkdirPrivate(dir string) error { return os.MkdirAll(dir, 0o700) }

// withLock runs fn holding an exclusive lock on path+".lock", so parallel hooks
// never interleave or lose writes.
func withLock(path string, fn func() error) error {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)
	return fn()
}

func appendRaw(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Append adds one line to path under the lock.
func Append(path string, line []byte) error {
	return withLock(path, func() error { return appendRaw(path, append(line, '\n')) })
}

// writeReplace replaces path with data via a temp file and rename. Call under the lock.
func writeReplace(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// readLines returns the non-empty lines of path (none if it does not exist).
func readLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return bytes.Count(data, []byte("\n"))
}

// ReadJSONL decodes every valid line of path into T; invalid lines are skipped.
func ReadJSONL[T any](path string) []T {
	var out []T
	for _, l := range readLines(path) {
		var v T
		if json.Unmarshal([]byte(l), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// LogError appends a timestamped line to the ledger's errors.log.
func LogError(dir, msg string) {
	_ = appendRaw(filepath.Join(dir, ErrorsFile), []byte(stamp()+" "+msg+"\n"))
}

// Status is one line: what the ledger in dir holds. Pin size is chars/4, an estimate.
func Status(dir string) string {
	chars := 0
	for _, p := range ReadJSONL[Pin](filepath.Join(dir, PinnedFile)) {
		chars += len(p.Text) + 1
	}
	return fmt.Sprintf("ledger: %d pins (~%d tokens) | %d prompts | %d decisions | %d errors",
		countLines(filepath.Join(dir, PinnedFile)), chars/4,
		countLines(filepath.Join(dir, PromptsFile)), countLines(filepath.Join(dir, DecisionsFile)),
		countLines(filepath.Join(dir, ErrorsFile)))
}

// Unpin removes the pin matching q (an id, or text contained in exactly one pin)
// from the pins file. The read, the count and the rewrite all happen under the
// lock, so a PIN arriving meanwhile is never lost. It returns the number of
// matching pins; nothing is removed unless it is 1.
func Unpin(path, q string) (int, error) {
	hits := 0
	err := withLock(path, func() error {
		lines := readLines(path)
		ids := map[string]bool{}
		var keep []string
		for _, l := range lines {
			var p Pin
			if json.Unmarshal([]byte(l), &p) == nil && (p.ID == q || strings.Contains(p.Text, q)) {
				ids[p.ID] = true
				continue
			}
			keep = append(keep, l)
		}
		hits = len(ids)
		if hits != 1 {
			return nil
		}
		data := strings.Join(keep, "\n")
		if data != "" {
			data += "\n"
		}
		return writeReplace(path, []byte(data))
	})
	return hits, err
}
