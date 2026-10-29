package ledger

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAddRuleReviewFixes(t *testing.T) {
	d := t.TempDir()
	h := PromoteHeading

	// CRLF is kept.
	p := filepath.Join(d, "crlf.md")
	os.WriteFile(p, []byte("# T\r\n\r\n"+h+"\r\n\r\n- a\r\n"), 0o600)
	if _, err := addRule(p, "b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "# T\r\n\r\n"+h+"\r\n\r\n- a\r\n- b\r\n" {
		t.Fatalf("crlf: %q", got)
	}
	if fi, _ := os.Stat(p); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// Heading inside a fence is ignored; '# comment' in a fence does not end the section.
	p = filepath.Join(d, "fence.md")
	in := "```\n" + h + "\n```\n\n" + h + "\n\n- a\n```sh\n# c\n```\n- z\n\n## Next\n"
	os.WriteFile(p, []byte(in), 0o644)
	addRule(p, "b")
	want := "```\n" + h + "\n```\n\n" + h + "\n\n- a\n```sh\n# c\n```\n- z\n- b\n\n## Next\n"
	if got, _ := os.ReadFile(p); string(got) != want {
		t.Fatalf("fence:\n%s", got)
	}

	// Indented 4+ spaces is not a heading: a new section is added.
	p = filepath.Join(d, "indent.md")
	os.WriteFile(p, []byte("    "+h+"\n"), 0o644)
	addRule(p, "b")
	if got, _ := os.ReadFile(p); string(got) != "    "+h+"\n\n"+h+"\n\n- b\n" {
		t.Fatalf("indent: %q", got)
	}

	// A planted tmp symlink is never followed; no tmp files left.
	if runtime.GOOS != "windows" {
		sub := t.TempDir()
		victim := filepath.Join(d, "victim")
		os.WriteFile(victim, []byte("safe"), 0o644)
		p = filepath.Join(sub, "AGENTS.md")
		os.Symlink(victim, p+".agent-ledger.tmp")
		if _, err := addRule(p, "x"); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(victim); string(got) != "safe" {
			t.Fatalf("victim overwritten: %q", got)
		}
		ents, _ := os.ReadDir(sub)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".agent-ledger-") {
				t.Fatalf("leftover %s", e.Name())
			}
		}
	}
}

func TestPromoteTarget(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	d := t.TempDir()
	if _, err := git(d, "init", "-q"); err != nil {
		t.Skip("git unavailable")
	}
	top, _ := git(d, "rev-parse", "--show-toplevel")
	want := func(name string) {
		t.Helper()
		got, err := PromoteTarget(Config{}, d)
		if err != nil || filepath.Base(got) != name {
			t.Errorf("got %s %v, want %s", got, err, name)
		}
	}
	a, c := filepath.Join(top, "AGENTS.md"), filepath.Join(top, "CLAUDE.md")
	want("AGENTS.md")
	os.WriteFile(c, []byte("# x\n"), 0o600)
	want("CLAUDE.md")
	os.WriteFile(a, []byte("# y\n"), 0o600)
	want("CLAUDE.md") // Claude Code reads only CLAUDE.md when both exist
	t.Setenv("CODEX_THREAD_ID", "t1")
	want("AGENTS.md")
	t.Setenv("CODEX_THREAD_ID", "")
	os.WriteFile(c, []byte("# x\nSee @AGENTS.md\n"), 0o600)
	want("AGENTS.md")
	os.Remove(c)
	if runtime.GOOS != "windows" {
		os.Symlink("AGENTS.md", c)
		want("AGENTS.md")
	}
	if got, _ := PromoteTarget(Config{PromoteTo: "RULES.md"}, d); filepath.Base(got) != "RULES.md" {
		t.Errorf("promote_to: %s", got)
	}
}
