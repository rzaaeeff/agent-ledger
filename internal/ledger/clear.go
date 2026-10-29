package ledger

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NotesFile holds the /clear handoff notes, one JSON line per clear, in the repo
// store. The leading dot keeps it apart from every session or branch directory
// name, which never start with a dot.
const NotesFile = ".cleared.jsonl"

// Note tells SessionStart(clear) which pins a /clear dropped.
type Note struct {
	N       string `json:"n"`
	Session string `json:"session"`
	Cwd     string `json:"cwd"`
	Dir     string `json:"dir"`
	Epoch   int64  `json:"epoch"`
	Pins    []Pin  `json:"pins"`
}

// ClearMarkFile records where the prompt and decision logs stood at the last /clear
// of a session that keeps its id, so a later compact re-injects only what came after.
// Line offsets, not timestamps: stamps have one-second granularity.
const ClearMarkFile = "clear_mark.json"

type clearMark struct {
	Prompts   int `json:"prompts"`
	Decisions int `json:"decisions"`
}

// markCleared records the current log lengths of an existing session dir.
func markCleared(dir string) {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		m := clearMark{len(readLines(filepath.Join(dir, PromptsFile))), len(readLines(filepath.Join(dir, DecisionsFile)))}
		_ = writeReplace(filepath.Join(dir, ClearMarkFile), append(Marshal(m), '\n'))
	}
}

func readMark(dir string) clearMark {
	var m clearMark
	data, _ := os.ReadFile(filepath.Join(dir, ClearMarkFile))
	_ = json.Unmarshal(data, &m)
	return m
}

// readJSONLFrom decodes the valid lines of path after its first skip lines.
func readJSONLFrom[T any](path string, skip int) []T {
	var out []T
	ls := readLines(path)
	if skip > len(ls) || skip < 0 {
		skip = 0
	}
	for _, l := range ls[skip:] {
		var v T
		if json.Unmarshal([]byte(l), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// ClearPins drops the pins of session dir: moves them to pinned.cleared.jsonl there
// and leaves a note in root for SessionStart(clear) to show the user. Does nothing
// when there are no pins.
func ClearPins(dir, root, sid, cwd string) error {
	path := filepath.Join(dir, PinnedFile)
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		return nil
	}
	var pins []Pin
	err := withLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, p := range ReadJSONL[Pin](path) {
			pins = append(pins, Pin{ID: p.ID, Text: p.Text})
		}
		if err := appendRaw(filepath.Join(dir, ClearedFile), data); err != nil {
			return err
		}
		return os.Remove(path)
	})
	if err != nil || len(pins) == 0 {
		return err
	}
	now := Now()
	return Append(filepath.Join(root, NotesFile), Marshal(Note{
		N: fmt.Sprintf("%s.%d.%d", sid, now.UnixNano(), os.Getpid()), Session: sid, Cwd: cwd, Dir: dir,
		Epoch: now.Unix(), Pins: pins}))
}

// TakeCleared removes and returns the note for session sid: the one from the same
// session id, else, only for a session with no ledger yet (isNew: /clear may have
// started a new id), the newest one from the same cwd in the last 10 minutes.
// Known limit: two brand-new sessions clearing in the same cwd within 10 minutes
// can swap notes; accepted, as a note is only shown to the user. Notes older than
// a day are dropped.
func TakeCleared(root, sid, cwd string, isNew bool) *Note {
	path := filepath.Join(root, NotesFile)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	var pick *Note
	_ = withLock(path, func() error {
		now := Now().Unix()
		notes := ReadJSONL[Note](path)
		for i := range notes {
			if notes[i].Session == sid {
				pick = &notes[i]
			}
		}
		if pick == nil && isNew {
			for i := range notes {
				if notes[i].Cwd == cwd && notes[i].Epoch > now-600 {
					pick = &notes[i]
				}
			}
		}
		var keep []string
		for _, n := range notes {
			if (pick == nil || n.N != pick.N) && n.Epoch > now-86400 {
				keep = append(keep, string(Marshal(n)))
			}
		}
		if len(keep) == 0 {
			return os.Remove(path)
		}
		return writeReplace(path, []byte(strings.Join(keep, "\n")+"\n"))
	})
	return pick
}

// Prune deletes the session (and fallback branch) directories under root in which
// nothing changed for days (0: never), except keep (the current one). It never follows
// symlinks and leaves files directly under root alone.
func Prune(root, keep string, days int) {
	for _, path := range PruneCandidates(root, keep, days) {
		_ = os.RemoveAll(path)
	}
}

// PruneCandidates lists what Prune would delete; nothing when days is 0.
func PruneCandidates(root, keep string, days int) []string {
	entries, err := os.ReadDir(root)
	if err != nil || days <= 0 {
		return nil
	}
	var out []string
	cutoff := Now().Add(-time.Duration(days) * 24 * time.Hour)
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 || path == keep {
			continue
		}
		recent := false
		_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				recent = true // unreadable: keep it
				return fs.SkipAll
			}
			if info, err := d.Info(); err != nil || info.ModTime().After(cutoff) {
				recent = true
				return fs.SkipAll
			}
			return nil
		})
		if !recent {
			out = append(out, path)
		}
	}
	return out
}
