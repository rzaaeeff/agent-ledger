package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rzaaeeff/agent-ledger/internal/ledger"
)

type env struct {
	t                 *testing.T
	repo, root, state string
}

func setup(t *testing.T) *env {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	// Run from inside Claude Code these would select its session; the tests set their own.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(state, "config"))
	for _, k := range []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "CLAUDE_ENV_FILE", "AGENT_LEDGER_DEBUG",
		"AGENT_LEDGER_KEY", "AGENT_LEDGER_CLEAR", "AGENT_LEDGER_PRUNE_DAYS", "AGENT_LEDGER_RECENT_PROMPTS",
		"AGENT_LEDGER_DECISIONS", "AGENT_LEDGER_BUDGET_CHARS"} {
		t.Setenv(k, "")
	}
	repo := filepath.Join(t.TempDir(), "proj")
	e := &env{t: t, repo: repo, state: state}
	e.git("init", "-q", "-b", "main", repo)
	root, err := ledger.Root(repo)
	if err != nil || !strings.HasPrefix(root, filepath.Join(state, "agent-ledger", "proj-")) {
		t.Fatalf("repo ledger root %q (%v)", root, err)
	}
	e.root = root
	e.git("-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	t.Chdir(repo)
	return e
}

func (e *env) git(args ...string) {
	e.t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		e.t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func (e *env) checkout(args ...string) {
	e.git(append([]string{"-C", e.repo, "checkout", "-q"}, args...)...)
}

// cli runs the command in-process; in is the stdin (a string, or a value encoded as JSON).
func cli(in any, args ...string) (stdout, stderr string, rc int) {
	var raw string
	switch v := in.(type) {
	case string:
		raw = v
	case nil:
	default:
		b, _ := json.Marshal(v)
		raw = string(b)
	}
	var o, e bytes.Buffer
	rc = run(args, strings.NewReader(raw), &o, &e)
	return o.String(), e.String(), rc
}

type m = map[string]any

func (e *env) prompt(text, sid string) m { return m{"prompt": text, "cwd": e.repo, "session_id": sid} }
func (e *env) start(src, sid string) m   { return m{"cwd": e.repo, "source": src, "session_id": sid} }
func (e *env) end(reason, sid string) m  { return m{"cwd": e.repo, "reason": reason, "session_id": sid} }

func (e *env) say(text, sid string) string {
	out, _, _ := cli(e.prompt(text, sid), "hook", "user-prompt-submit")
	return out
}

func (e *env) inject(src, sid string) string {
	out, _, _ := cli(e.start(src, sid), "hook", "session-start")
	return out
}

func (e *env) dir(key string) string { return filepath.Join(e.root, key) }

func lines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func lastField(t *testing.T, path, field string) string {
	t.Helper()
	ls := lines(t, path)
	if len(ls) == 0 {
		t.Fatalf("%s is empty", path)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(ls[len(ls)-1]), &v); err != nil {
		t.Fatalf("%s: last line is not JSON: %v", path, err)
	}
	return fmt.Sprint(v[field])
}

func sysMsg(t *testing.T, out string) string {
	t.Helper()
	var v map[string]string
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v) != 1 {
		t.Fatalf("want exactly one {systemMessage} JSON, got %q", out)
	}
	msg, ok := v["systemMessage"]
	if !ok {
		t.Fatalf("want a systemMessage key, got %q", out)
	}
	return msg
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestHooksSurviveBadInput(t *testing.T) {
	setup(t)
	for _, in := range []string{"", "not json", "{}", `{"prompt":""}`, `{"prompt":"x","cwd":"/nonexistent"}`, `{"prompt":123}`, `[1,2]`, `{"cwd":5,"session_id":{}}`} {
		for _, h := range []string{"user-prompt-submit", "session-start", "session-end", "unknown"} {
			out, errs, rc := cli(in, "hook", h)
			if rc != 0 || errs != "" || (h != "session-start" && out != "") {
				t.Errorf("hook %s with %q: rc=%d out=%q err=%q", h, in, rc, out, errs)
			}
		}
	}
	if _, _, rc := cli(nil, "hook"); rc != 0 {
		t.Error("bare hook must exit 0")
	}
}

func TestLoggingAndPins(t *testing.T) {
	e := setup(t)
	d := e.dir("s1")
	if out := e.say("hello", "s1"); out != "" {
		t.Errorf("ordinary prompt must print nothing, got %q", out)
	}
	if got := lastField(t, filepath.Join(d, "prompts.jsonl"), "prompt"); got != "hello" {
		t.Errorf("prompt logged = %q", got)
	}
	odd := "multi\nline \"quoted\" $(rm -rf /) `x` <&>"
	e.say(odd, "s1")
	if got := lastField(t, filepath.Join(d, "prompts.jsonl"), "prompt"); got != odd {
		t.Errorf("verbatim prompt = %q", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(d, "prompts.jsonl")); fi.Mode().Perm() != 0o600 {
			t.Errorf("prompts.jsonl mode %v, want 0600", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
			t.Errorf("ledger dir mode %v, want 0700", fi.Mode().Perm())
		}
	}

	msg := sysMsg(t, e.say("PIN: never commit to main", "s1"))
	if !regexp.MustCompile(`^pinned \[[0-9a-f]{6}\]\. ledger: 1 pins`).MatchString(msg) {
		t.Errorf("PIN message = %q", msg)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s1")
	if out, _, _ := cli(nil, "status"); out != "ledger: 1 pins (~5 tokens) | 3 prompts | 0 decisions | 0 errors\n" {
		t.Errorf("status = %q", out)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	e.say("PIN:   keep the critique format  ", "s1")
	if got := lastField(t, filepath.Join(d, "pinned.jsonl"), "text"); got != "keep the critique format" {
		t.Errorf("trimmed pin = %q", got)
	}
	sysMsg(t, e.say("UNPIN: the critique", "s1"))
	if n := len(lines(t, filepath.Join(d, "pinned.jsonl"))); n != 1 {
		t.Errorf("UNPIN by unique substring: %d pins left, want 1", n)
	}
	if msg := sysMsg(t, e.say("UNPIN: zzz-no-match", "s1")); !strings.Contains(msg, "UNPIN did nothing") {
		t.Errorf("no-match UNPIN message = %q", msg)
	}
	e.say("PIN: second rule about main", "s1")
	if msg := sysMsg(t, e.say("UNPIN: main", "s1")); !strings.Contains(msg, "matched 2 pins") {
		t.Errorf("ambiguous UNPIN message = %q", msg)
	}
	if n := len(lines(t, filepath.Join(d, "pinned.jsonl"))); n != 2 {
		t.Errorf("ambiguous UNPIN removed something: %d pins", n)
	}
	if data, _ := os.ReadFile(filepath.Join(d, "errors.log")); !strings.Contains(string(data), "matched 2") {
		t.Errorf("ambiguous UNPIN not logged: %q", data)
	}
	if msg := sysMsg(t, e.say("UNPIN: x", "fresh")); !strings.Contains(msg, "no pins") {
		t.Errorf("UNPIN without pins = %q", msg)
	}
}

func TestKeyedBySession(t *testing.T) {
	e := setup(t)
	d := e.dir("s1")
	e.say("PIN: never commit to main", "s1")
	e.checkout("-b", "feat/x;$(touch-pwned)")
	e.say("PIN: s1 rule on another branch", "s1")
	e.checkout("--detach")
	e.say("detached", "s1")
	if n := len(lines(t, filepath.Join(d, "pinned.jsonl"))); n != 2 {
		t.Errorf("same session across branches: %d pins in one ledger, want 2", n)
	}
	if got := lastField(t, filepath.Join(d, "prompts.jsonl"), "prompt"); got != "detached" {
		t.Errorf("same session on detached HEAD logged elsewhere: %q", got)
	}
	e.checkout("main")

	e.say("PIN: s2 own rule", "s2")
	out := e.inject("compact", "s2")
	if !strings.Contains(out, "s2 own rule") || strings.Contains(out, "never commit to main") {
		t.Errorf("other session must see only its own pins:\n%s", out)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s2")
	want := "ledger: 1 pins (~3 tokens) | 1 prompts | 0 decisions | 0 errors\n"
	if out, _, _ := cli(nil, "status"); out != want {
		t.Errorf("CLAUDE_CODE_SESSION_ID=s2 status = %q", out)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s1")
	t.Setenv("CLAUDE_SESSION_ID", "s2")
	if out, _, _ := cli(nil, "status"); out != want {
		t.Errorf("CLAUDE_SESSION_ID must win over a stale CLAUDE_CODE_SESSION_ID: %q", out)
	}
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	e.say("hostile", "../../../x;$(touch pwned)/..")
	if !exists(filepath.Join(e.root, "_.._.._x___touch_pwned__..", "prompts.jsonl")) {
		t.Error("hostile session id not sanitised into one directory")
	}
	for _, evil := range []string{"..", ".", "..."} {
		e.say("dots "+evil, evil)
		if got := lastField(t, filepath.Join(e.root, "unnamed", "prompts.jsonl"), "prompt"); got != "dots "+evil {
			t.Errorf("session id %q escaped: %q", evil, got)
		}
	}
	filepath.WalkDir(e.state, func(p string, de os.DirEntry, _ error) error {
		if de != nil && de.Name() == "prompts.jsonl" {
			rel, _ := filepath.Rel(e.root, p)
			if strings.Count(filepath.ToSlash(rel), "/") != 1 || strings.HasPrefix(rel, "..") {
				t.Errorf("ledger outside <repo>/<key>/: %s", p)
			}
		}
		return nil
	})
	if exists(filepath.Join(e.repo, "pwned")) || exists("pwned") {
		t.Error("something executed a session id or branch name")
	}

	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	if out := e.inject("startup", "a b;$(x)"); out != "" {
		t.Errorf("startup of a new session printed %q", out)
	}
	if ls := lines(t, envFile); len(ls) == 0 || ls[len(ls)-1] != "export CLAUDE_SESSION_ID=a_b___x_" {
		t.Errorf("CLAUDE_ENV_FILE = %q", ls)
	}
}

func TestBranchFallback(t *testing.T) {
	e := setup(t)
	e.checkout("-b", "feat/y;$(touch-pwned)")
	e.say("on odd branch", "")
	d2 := e.dir("feat_y___touch-pwned_")
	if !exists(filepath.Join(d2, "prompts.jsonl")) {
		t.Error("no session id: branch ledger not used / not sanitised")
	}
	out, errs, rc := cli(nil, "decide", "fb", "fb", "fb")
	if rc != 0 || !strings.Contains(errs, "no session id") || !exists(filepath.Join(d2, "decisions.jsonl")) {
		t.Errorf("decide fallback: rc=%d out=%q err=%q", rc, out, errs)
	}
	e.checkout("--detach")
	e.say("detached", "")
	if !exists(filepath.Join(e.root, "detached", "prompts.jsonl")) {
		t.Error("detached HEAD has no ledger of its own")
	}
	e.checkout("main")
	t.Chdir(t.TempDir())
	if out, _, rc := cli(nil, "decide", "a", "b", "c"); rc != 1 || out != "" {
		t.Errorf("decide outside a repo: rc=%d out=%q", rc, out)
	}
	if out, _, _ := cli(nil, "status"); out != "ledger: not in a git repository\n" {
		t.Errorf("status outside a repo = %q", out)
	}
	if out, _, rc := cli(nil, "inject"); out != "" || rc != 0 {
		t.Errorf("inject outside a repo = %q", out)
	}
}

func TestConcurrency(t *testing.T) {
	e := setup(t)
	d := e.dir("s1")
	big := strings.Repeat("x", 6000)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); e.say(fmt.Sprintf("parallel %d %s", i, big), "s1") }(i)
	}
	wg.Wait()
	ls := lines(t, filepath.Join(d, "prompts.jsonl"))
	for _, l := range ls {
		if !json.Valid([]byte(l)) {
			t.Fatal("interleaved prompt lines")
		}
	}
	if len(ls) != 30 {
		t.Errorf("%d of 30 parallel prompts logged", len(ls))
	}

	e.say("PIN: race-target-rule", "s1")
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); e.say(fmt.Sprintf("PIN: racer %d", i), "s1") }(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); e.say("UNPIN: race-target-rule", "s1") }()
	wg.Wait()
	data, _ := os.ReadFile(filepath.Join(d, "pinned.jsonl"))
	if strings.Contains(string(data), "race-target-rule") || strings.Count(string(data), `"racer `) != 25 {
		t.Errorf("UNPIN raced with PIN:\n%s", data)
	}
}

func TestDecideAndInject(t *testing.T) {
	e := setup(t)
	e.say("hello", "s1")
	e.say("PIN: never commit to main", "s1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s1")
	if out, errs, rc := cli(nil, "decide", "use X", "because Y", "Z rejected"); rc != 0 || errs != "" || !strings.HasPrefix(out, "decide: recorded in") {
		t.Errorf("decide: rc=%d out=%q err=%q", rc, out, errs)
	}
	if _, _, rc := cli(nil, "decide", "a"); rc != 2 {
		t.Errorf("decide with wrong arg count: rc=%d, want 2", rc)
	}
	if got := lastField(t, filepath.Join(e.dir("s1"), "decisions.jsonl"), "because"); got != "because Y" {
		t.Errorf("decision = %q", got)
	}
	for i := 0; i < 8; i++ {
		e.say(fmt.Sprintf("recent %d", i), "s1")
	}
	out := e.inject("compact", "s1")
	for _, want := range []string{"Pinned by the user", "never commit to main", "Last prompts", "DECISION: use X", "Session decisions"} {
		if !strings.Contains(out, want) {
			t.Errorf("compact output lacks %q:\n%s", want, out)
		}
	}
	if n := regexp.MustCompile(`(?m)^- 20`).FindAllStringIndex(out, -1); len(n) > 5 {
		t.Errorf("%d recent prompts, want at most 5", len(n))
	}
	if out := e.inject("resume", "s1"); out != "" {
		t.Errorf("resume printed %q", out)
	}
	if out, _, _ := cli(nil, "inject"); !strings.Contains(out, "Last prompts") || !strings.Contains(out, "Pinned by the user") {
		t.Errorf("manual inject (env session) =\n%s", out)
	}
	if out, _, _ := cli(m{"cwd": e.repo, "session_id": "s1"}, "hook", "session-start"); !strings.Contains(out, "Last prompts") {
		t.Errorf("no source must print everything:\n%s", out)
	}
}

func TestSizeCap(t *testing.T) {
	e := setup(t)
	e.say("PIN: never commit to main", "s1")
	big := strings.Repeat("y", 5000)
	for i := 0; i < 6; i++ {
		e.say(fmt.Sprintf("big %d %s", i, big), "s1")
	}
	out := e.inject("compact", "s1")
	if len([]rune(out)) > 16200 || !strings.Contains(out, "never commit to main") || !strings.Contains(out, "[cut; full text in prompts.jsonl]") {
		t.Errorf("size cap: %d chars", len([]rune(out)))
	}
	e.say("PIN: "+strings.Repeat("p", 9000), "s1")
	out = e.inject("compact", "s1")
	if !strings.Contains(out, "WARNING: pinned items exceed") || !strings.Contains(out, strings.Repeat("p", 9000)) || !strings.Contains(out, "never commit to main") {
		t.Error("pins over 2k tokens must be kept whole with a warning")
	}
}

func TestClear(t *testing.T) {
	e := setup(t)
	d, notes := e.dir("s1"), filepath.Join(e.root, ".cleared.jsonl")
	e.say("PIN: never commit to main", "s1")
	e.say("PIN: keep the critique format", "s1")
	for _, r := range []string{"logout", "prompt_input_exit", "other", "resume"} {
		if out, errs, rc := cli(e.end(r, "s1"), "hook", "session-end"); out+errs != "" || rc != 0 {
			t.Errorf("SessionEnd %s printed %q %q", r, out, errs)
		}
	}
	if len(lines(t, filepath.Join(d, "pinned.jsonl"))) != 2 || exists(notes) {
		t.Error("SessionEnd other than clear must keep the pins and leave no note")
	}
	if out, errs, rc := cli(e.end("clear", "s1"), "hook", "session-end"); out+errs != "" || rc != 0 {
		t.Errorf("SessionEnd clear printed %q %q", out, errs)
	}
	if exists(filepath.Join(d, "pinned.jsonl")) || len(lines(t, filepath.Join(d, "pinned.cleared.jsonl"))) != 2 || !exists(notes) {
		t.Error("SessionEnd clear must archive and remove the pins and leave a note")
	}
	out := e.inject("clear", "s1")
	if msg := sysMsg(t, out); !strings.Contains(msg, "dropped 2 pin") || !strings.Contains(msg, "never commit to main") {
		t.Errorf("clear message = %q", msg)
	}
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 || exists(notes) {
		t.Error("clear must print one line and consume the note")
	}
	if out := e.inject("clear", "s1"); out != "" {
		t.Errorf("second clear start printed %q", out)
	}
	e.say("after the clear", "s1")
	if out := e.inject("compact", "s1"); strings.Contains(out, "never commit to main") || strings.Contains(out, "keep the critique") || !strings.Contains(out, "after the clear") {
		t.Errorf("compact after clear must show only what came after it (no pins, no earlier prompts):\n%s", out)
	}

	e.say("PIN: rule before a clear with a new id", "c1")
	cli(e.end("clear", "c1"), "hook", "session-end")
	if msg := sysMsg(t, e.inject("clear", "c2")); !strings.Contains(msg, "dropped 1 pin") || !strings.Contains(msg, "new id") {
		t.Errorf("new id after clear: %q", msg)
	}

	e.say("PIN: start before end", "c3")
	out = e.inject("clear", "c3")
	if msg := sysMsg(t, out); !strings.Contains(msg, "start before end") {
		t.Errorf("start before end: %q", msg)
	}
	if out, _, _ := cli(e.end("clear", "c3"), "hook", "session-end"); out != "" || exists(filepath.Join(e.dir("c3"), "pinned.jsonl")) || exists(notes) {
		t.Error("SessionEnd after SessionStart(clear) must find nothing left")
	}

	cli(e.end("clear", "none"), "hook", "session-end")
	if out := e.inject("clear", "none"); out != "" || exists(notes) {
		t.Errorf("nothing dropped must be silent, got %q", out)
	}
	if out, errs, rc := cli(e.start("clear", ""), "hook", "session-start"); out+errs != "" || rc != 0 {
		t.Error("clear with an empty session id must be silent")
	}

	e.say("PIN: other session pin", "p1")
	cli(e.end("clear", "p1"), "hook", "session-end")
	wt := filepath.Join(t.TempDir(), "b")
	e.git("-C", e.repo, "worktree", "add", "-q", wt, "-b", "wtb")
	if out, _, _ := cli(m{"cwd": wt, "source": "clear", "session_id": "p3"}, "hook", "session-start"); out != "" || !exists(notes) {
		t.Error("a clear in another worktree took this session's note")
	}
	e.say("B has history, no pins", "pB")
	cli(e.end("clear", "pB"), "hook", "session-end")
	if out := e.inject("clear", "pB"); out != "" || !exists(notes) {
		t.Error("another existing session clearing in the same cwd took the note")
	}
	if msg := sysMsg(t, e.inject("clear", "p1")); !strings.Contains(msg, "other session pin") {
		t.Errorf("owning session lost its note: %q", msg)
	}

	e.say("PIN: "+strings.Repeat("q", 500), "long")
	cli(e.end("clear", "long"), "hook", "session-end")
	if msg := sysMsg(t, e.inject("clear", "long")); strings.Contains(msg, strings.Repeat("q", 301)) || !strings.Contains(msg, "...") {
		t.Error("long pins must be cut to 300 chars in the clear message")
	}
}

func TestPrune(t *testing.T) {
	e := setup(t)
	e.say("keep me", "s1")
	old := time.Now().Add(-31 * 24 * time.Hour)
	mk := func(name string, age time.Time) string {
		p := filepath.Join(e.root, name)
		os.MkdirAll(p, 0o700)
		f := filepath.Join(p, "prompts.jsonl")
		os.WriteFile(f, []byte("x\n"), 0o600)
		os.Chtimes(f, age, age)
		os.Chtimes(p, age, age)
		return p
	}
	oldDir := mk("old", old)
	fresh := mk("fresh", time.Now())
	os.Chtimes(fresh, old, old) // dir old, file recent: keep
	note := filepath.Join(e.root, "cleared.json")
	os.WriteFile(note, []byte("{}"), 0o600)
	os.Chtimes(note, old, old)
	target := t.TempDir()
	os.Chtimes(target, old, old)
	link := filepath.Join(e.root, "link")
	symlinked := os.Symlink(target, link) == nil

	e.inject("compact", "s1")
	if !exists(oldDir) {
		t.Error("compact must not prune")
	}
	if out, errs, _ := cli(e.start("startup", "s1"), "hook", "session-start"); errs != "" || strings.Contains(out, "prune") {
		t.Errorf("startup prune printed %q %q", out, errs)
	}
	if exists(oldDir) || !exists(fresh) || !exists(note) || !exists(e.dir("s1")) {
		t.Error("startup must remove idle dirs only, keeping recent dirs and plain files")
	}
	if symlinked && (!exists(link) || !exists(target)) {
		t.Error("prune followed or removed a symlink")
	}
	cur := mk("cur", old)
	e.inject("startup", "cur")
	if !exists(cur) {
		t.Error("startup pruned the current session")
	}
}

func TestDebugLog(t *testing.T) {
	e := setup(t)
	t.Setenv("AGENT_LEDGER_DEBUG", "1")
	cli(e.end("clear", "s1"), "hook", "session-end")
	e.inject("clear", "s2")
	ls := lines(t, filepath.Join(e.root, "hooks.debug.log"))
	if len(ls) != 2 || !strings.Contains(ls[0], `"e":"SessionEnd"`) || !strings.Contains(ls[1], `"source":"clear"`) {
		t.Errorf("debug log = %q", ls)
	}
}

func TestVersionAndUsage(t *testing.T) {
	if out, _, rc := cli(nil, "version"); rc != 0 || !strings.HasPrefix(out, "agent-ledger ") {
		t.Errorf("version = %q", out)
	}
	if _, errs, rc := cli(nil); rc != 2 || !strings.Contains(errs, "usage") {
		t.Error("no args must print usage and exit 2")
	}
	if _, _, rc := cli(nil, "bogus"); rc != 2 {
		t.Error("unknown command must exit 2")
	}
}

func TestSameNamedReposStayApart(t *testing.T) {
	e := setup(t)
	other := filepath.Join(t.TempDir(), "proj")
	e.git("init", "-q", "-b", "main", other)
	a, errA := ledger.Root(e.repo)
	b, errB := ledger.Root(other)
	if errA != nil || errB != nil || a == b {
		t.Fatalf("two repos named proj share a ledger root: %q %q (%v %v)", a, b, errA, errB)
	}
	sub, _ := ledger.Root(e.repo)
	if sub != a {
		t.Fatalf("root is not stable: %q vs %q", a, sub)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigLayers(t *testing.T) {
	e := setup(t)
	userFile := filepath.Join(e.state, "config", "agent-ledger", "config.json")
	repoFile := filepath.Join(e.repo, ".agent-ledger.json")

	c := ledger.LoadConfig(e.repo)
	if c.Key != "session" || c.Clear != "drop" || c.PruneDays != 30 || c.RecentPrompts != 5 || c.Decisions != 10 || c.BudgetChars != 16000 || len(c.Problems) != 0 {
		t.Fatalf("defaults changed: %+v", c)
	}

	writeFile(t, userFile, `{"key":"branch","recent_prompts":3,"prune_days":7}`)
	writeFile(t, repoFile, `{"recent_prompts":2,"bogus":1,"decisions":"x","budget_chars":5}`)
	t.Setenv("AGENT_LEDGER_PRUNE_DAYS", "0")
	c = ledger.LoadConfig(e.repo)
	if c.Key != "branch" || c.Source["key"] != userFile {
		t.Errorf("user layer: key=%s from %s", c.Key, c.Source["key"])
	}
	if c.RecentPrompts != 2 || c.Source["recent_prompts"] != repoFile {
		t.Errorf("repo layer must beat user: %d from %s", c.RecentPrompts, c.Source["recent_prompts"])
	}
	if c.PruneDays != 0 || c.Source["prune_days"] != "env AGENT_LEDGER_PRUNE_DAYS" {
		t.Errorf("env must beat files: %d from %s", c.PruneDays, c.Source["prune_days"])
	}
	if c.Clear != "keep" {
		t.Errorf("clear must default to keep for a shared ledger, got %s", c.Clear)
	}
	if c.Decisions != 10 || c.BudgetChars != 16000 || len(c.Problems) != 3 {
		t.Errorf("bad values must be ignored and reported: %+v %q", c, c.Problems)
	}
	out, _, rc := cli(nil, "config")
	for _, want := range []string{"key             branch", repoFile, "problem:", "unknown key \"bogus\""} {
		if rc != 0 || !strings.Contains(out, want) {
			t.Errorf("config output lacks %q:\n%s", want, out)
		}
	}

	writeFile(t, repoFile, `not json`)
	if c = ledger.LoadConfig(e.repo); c.RecentPrompts != 3 || len(c.Problems) != 1 {
		t.Errorf("a broken repo file must be ignored: %+v %q", c, c.Problems)
	}
	// Hooks never fail on a broken config.
	if out, errs, rc := cli(e.prompt("PIN: x", "s1"), "hook", "user-prompt-submit"); rc != 0 || errs != "" || !strings.Contains(out, "pinned") {
		t.Errorf("hook with broken config: rc=%d out=%q err=%q", rc, out, errs)
	}
}

func TestKeyModes(t *testing.T) {
	e := setup(t)
	t.Setenv("AGENT_LEDGER_KEY", "branch")
	e.say("PIN: branch rule", "s1")
	if !exists(filepath.Join(e.dir("main"), "pinned.jsonl")) {
		t.Fatal("key=branch must use the branch ledger even with a session id")
	}
	if out := e.inject("startup", "s2"); !strings.Contains(out, "branch rule") {
		t.Errorf("key=branch: a new session on the branch must see its pins:\n%s", out)
	}
	e.checkout("-b", "feat")
	if out := e.inject("compact", "s1"); strings.Contains(out, "branch rule") {
		t.Errorf("key=branch: another branch must not see main's pins:\n%s", out)
	}

	t.Setenv("AGENT_LEDGER_KEY", "repo")
	e.say("PIN: repo rule", "s3")
	e.checkout("main")
	out := e.inject("compact", "s4")
	if !strings.Contains(out, "repo rule") || strings.Contains(out, "branch rule") || !exists(filepath.Join(e.dir("@repo"), "pinned.jsonl")) {
		t.Errorf("key=repo: one ledger across branches and sessions:\n%s", out)
	}
	// Shared ledgers keep pins on /clear by default and re-inject them (no prompts).
	cli(e.end("clear", "s4"), "hook", "session-end")
	out = e.inject("clear", "s4")
	if !strings.Contains(out, "repo rule") || strings.Contains(out, "Last prompts") || strings.Contains(out, "systemMessage") {
		t.Errorf("key=repo clear=keep must re-inject pins without prompts:\n%s", out)
	}
	// clear=drop on a shared ledger drops and reports, even for a new session id.
	t.Setenv("AGENT_LEDGER_CLEAR", "drop")
	cli(e.end("clear", "s4"), "hook", "session-end")
	msg := sysMsg(t, e.inject("clear", "s5"))
	if !strings.Contains(msg, "repo rule") || exists(filepath.Join(e.dir("@repo"), "pinned.jsonl")) {
		t.Errorf("key=repo clear=drop: %q", msg)
	}
}

func TestPreview(t *testing.T) {
	e := setup(t)
	t.Setenv("CLAUDE_SESSION_ID", "s1")
	e.say("PIN: keep it short", "s1")
	e.say("hello", "s1")
	before := lines(t, filepath.Join(e.dir("s1"), "pinned.jsonl"))
	old := e.dir("old")
	writeFile(t, filepath.Join(old, "prompts.jsonl"), "{}\n")
	past := time.Now().Add(-40 * 24 * time.Hour)
	os.Chtimes(filepath.Join(old, "prompts.jsonl"), past, past)
	os.Chtimes(old, past, past)

	for src, want := range map[string][]string{
		"compact": {"keep it short", "hello", "Injected as context"},
		"clear":   {"dropped 1 pin", "keep it short"},
		"startup": {"Would prune 1 ledger", "- old", "keep it short"},
		"resume":  {"Nothing is injected on resume"},
	} {
		out, errs, rc := cli(nil, "preview", src)
		for _, w := range want {
			if rc != 0 || errs != "" || !strings.Contains(out, w) {
				t.Errorf("preview %s lacks %q (rc=%d err=%q):\n%s", src, w, rc, errs, out)
			}
		}
	}
	if !exists(old) || len(lines(t, filepath.Join(e.dir("s1"), "pinned.jsonl"))) != len(before) || exists(filepath.Join(e.root, ".cleared.jsonl")) {
		t.Error("preview must not prune or clear")
	}
	if _, _, rc := cli(nil, "preview", "bogus"); rc != 2 {
		t.Error("preview with an unknown source must exit 2")
	}
	t.Setenv("AGENT_LEDGER_RECENT_PROMPTS", "0")
	if out, _, _ := cli(nil, "preview"); strings.Contains(out, "hello") {
		t.Errorf("recent_prompts=0 must re-inject no prompts:\n%s", out)
	}
}
