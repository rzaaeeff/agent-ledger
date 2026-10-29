package ledger

import (
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

// ClearedAtFile records the last /clear of a session that keeps its id, so a later
// compact re-injects only the prompts and decisions made after it.
const ClearedAtFile = "cleared_at"

// markCleared stamps the /clear time into an existing session dir.
func markCleared(dir string) {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		_ = writeReplace(filepath.Join(dir, ClearedAtFile), []byte(Now().Format(time.RFC3339Nano)+"\n"))
	}
}

func clearedAt(dir string) time.Time {
	data, _ := os.ReadFile(filepath.Join(dir, ClearedAtFile))
	t, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	return t
}

// after reports whether the entry stamped ts is not older than since; entries
// with an unreadable stamp are kept.
func after(ts string, since time.Time) bool {
	t, err := time.Parse(time.RFC3339, ts)
	return err != nil || since.IsZero() || !t.Before(since)
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
// nothing changed for PruneDays, except keep (the current one). It never follows
// symlinks and leaves files directly under root alone.
func Prune(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := Now().Add(-PruneDays * 24 * time.Hour)
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
			_ = os.RemoveAll(path)
		}
	}
}
