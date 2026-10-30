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
	Pins    []Pin  `json:"pins"`            // dropped
	Kept    []Pin  `json:"kept,omitempty"`  // PIN!: survive /clear
	Carry   []Pin  `json:"carry,omitempty"` // CARRY: injected once after /clear
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

// ClearPins applies /clear to session dir: plain pins are dropped and CARRY pins
// taken (both archived to pinned.cleared.jsonl), PIN! pins stay. It leaves a note
// in root for SessionStart(clear) with all three. Does nothing when there are no
// pins. Calling it again (SessionEnd and SessionStart both do) only re-notes the
// kept pins; TakeCleared merges the notes.
func ClearPins(dir, root, sid, cwd string) error {
	path := filepath.Join(dir, PinnedFile)
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		return nil
	}
	var drop, kept, carry []Pin
	err := withLock(path, func() error {
		var keep, gone []string
		for _, l := range readLines(path) {
			var p Pin
			if json.Unmarshal([]byte(l), &p) != nil {
				gone = append(gone, l)
				continue
			}
			switch p.Life {
			case LifeKeep:
				kept = append(kept, p)
				keep = append(keep, l)
				continue
			case LifeCarry:
				carry = append(carry, p)
			default:
				drop = append(drop, Pin{ID: p.ID, Text: p.Text})
			}
			gone = append(gone, l)
		}
		if len(gone) == 0 {
			return nil
		}
		if err := appendRaw(filepath.Join(dir, ClearedFile), []byte(strings.Join(gone, "\n")+"\n")); err != nil {
			return err
		}
		return writeLines(path, keep)
	})
	if err != nil || len(drop)+len(kept)+len(carry) == 0 {
		return err
	}
	now := Now()
	return Append(filepath.Join(root, NotesFile), Marshal(Note{
		N: fmt.Sprintf("%s.%d.%d", sid, now.UnixNano(), os.Getpid()), Session: sid, Cwd: cwd, Dir: dir,
		Epoch: now.Unix(), Pins: drop, Kept: kept, Carry: carry}))
}

// TakeCleared removes and returns the notes for session sid, merged: the ones
// from the same session id, else, only for a session with no ledger yet (isNew:
// /clear may have started a new id), those of the session that left the newest
// note from the same cwd in the last 10 minutes.
// If notes of more than one other session qualify, it cannot tell which chat was
// cleared and takes none (ambiguous reports it): kept pins reach the model, so a
// guess could hand one chat's rules to another. Notes older than a day are dropped.
func TakeCleared(root, sid, cwd string, isNew bool) (pick *Note, ambiguous bool) {
	path := filepath.Join(root, NotesFile)
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	_ = withLock(path, func() error {
		now := Now().Unix()
		notes := ReadJSONL[Note](path)
		from := ""
		for _, n := range notes {
			if n.Session == sid {
				from = sid
			}
		}
		if from == "" && isNew {
			seen := map[string]bool{}
			for _, n := range notes {
				if n.Cwd != "" && canonDir(n.Cwd) == canonDir(cwd) && n.Epoch > now-600 {
					seen[n.Session] = true
					from = n.Session
				}
			}
			if len(seen) > 1 {
				from, ambiguous = "", true
			}
		}
		var keep []string
		for _, n := range notes {
			if from != "" && n.Session == from {
				pick = mergeNote(pick, n)
			} else if n.Epoch > now-86400 {
				keep = append(keep, string(Marshal(n)))
			}
		}
		if len(keep) == 0 {
			return os.Remove(path)
		}
		return writeReplace(path, []byte(strings.Join(keep, "\n")+"\n"))
	})
	return pick, ambiguous
}

func mergeNote(into *Note, n Note) *Note {
	if into == nil {
		into = &Note{}
	}
	add := func(dst []Pin, src []Pin) []Pin {
		for _, p := range src {
			dup := false
			for _, q := range dst {
				dup = dup || q.ID == p.ID
			}
			if !dup {
				dst = append(dst, p)
			}
		}
		return dst
	}
	into.N, into.Session, into.Cwd, into.Dir, into.Epoch = n.N, n.Session, n.Cwd, n.Dir, n.Epoch
	into.Pins, into.Kept, into.Carry = add(into.Pins, n.Pins), add(into.Kept, n.Kept), add(into.Carry, n.Carry)
	return into
}

// Prune deletes the session (and fallback branch) directories under root in which
// nothing changed for days (0: never), except keep (the current one). It never follows
// symlinks and leaves files directly under root alone.
func Prune(cwd, root, keep string, days int) {
	for _, path := range PruneCandidates(cwd, root, keep, days) {
		_ = os.RemoveAll(path)
	}
}

// PruneCandidates lists what Prune would delete; nothing when days is 0.
// Branch pin stores go only once their branch is gone from the repo of cwd.
func PruneCandidates(cwd, root, keep string, days int) []string {
	entries, err := os.ReadDir(root)
	if err != nil || days <= 0 {
		return nil
	}
	var out []string
	cutoff := Now().Add(-time.Duration(days) * 24 * time.Hour)
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		// The repo-wide pins are standing rules: never idle-pruned.
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 || path == keep || e.Name() == RepoKey {
			continue
		}
		if strings.HasPrefix(e.Name(), "@branch-") {
			data, err := os.ReadFile(filepath.Join(path, BranchNameFile))
			name := strings.TrimSpace(string(data))
			if err != nil || name == "" {
				continue
			}
			if _, err := git(cwd, "show-ref", "--verify", "--quiet", "refs/heads/"+name); err == nil {
				continue
			}
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

// canonDir is cwd made absolute, cleaned and with symlinks resolved, so "$R/",
// "$R" and a symlink to $R count as one folder for the /clear handoff.
func canonDir(cwd string) string {
	if a, err := filepath.Abs(cwd); err == nil {
		cwd = a
	}
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = r
	}
	return filepath.Clean(cwd)
}
