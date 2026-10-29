package ledger

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Preview describes what SessionStart with source would do for the ledger of cwd
// and sid under cfg, without doing it: no prune, no clear, no env file.
func Preview(cfg Config, cwd, sid, source string) (string, error) {
	dir, err := DirFor(cfg, cwd, sid)
	if err != nil {
		return "", err
	}
	root := filepath.Dir(dir)
	var b strings.Builder
	fmt.Fprintf(&b, "# preview of SessionStart(%s): key=%s clear=%s ledger=%s\n", source, cfg.Key, cfg.Clear, dir)
	switch source {
	case "resume":
		b.WriteString("Nothing is injected on resume: the transcript is already loaded.")
		return b.String(), nil
	case "clear":
		if cfg.Clear == "drop" {
			pins := ReadJSONL[Pin](filepath.Join(dir, PinnedFile))
			if len(pins) == 0 {
				b.WriteString("Nothing is injected and no pins would be dropped.")
				return b.String(), nil
			}
			fmt.Fprintf(&b, "Nothing is injected. You would see that /clear dropped %d pin(s):\n", len(pins))
			for _, p := range pins {
				fmt.Fprintf(&b, "- [%s] %s\n", p.ID, p.Text)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		}
		cfg.RecentPrompts = 0
	case "startup":
		if c := PruneCandidates(root, dir, cfg.PruneDays); len(c) > 0 {
			fmt.Fprintf(&b, "Would prune %d ledger(s) idle for %d+ days:\n", len(c), cfg.PruneDays)
			for _, p := range c {
				fmt.Fprintf(&b, "- %s\n", filepath.Base(p))
			}
		}
	case "compact":
	default:
		return "", fmt.Errorf("unknown source %q (want compact|clear|startup|resume)", source)
	}
	text := Compose(dir, cfg)
	if text == "" {
		b.WriteString("Nothing would be injected (empty ledger).")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "Injected as context (~%d tokens):\n%s", len(text)/4, text)
	return b.String(), nil
}
