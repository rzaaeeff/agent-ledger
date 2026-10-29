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
			var drop, kept, carry []Pin
			for _, p := range ReadJSONL[Pin](filepath.Join(dir, PinnedFile)) {
				switch p.Life {
				case LifeKeep:
					kept = append(kept, p)
				case LifeCarry:
					carry = append(carry, p)
				default:
					drop = append(drop, p)
				}
			}
			for _, part := range []struct {
				pins []Pin
				what string
			}{{drop, "You would see that /clear dropped"}, {kept, "Kept across /clear (PIN!)"}, {carry, "Carried into the new chat once (CARRY)"}} {
				if len(part.pins) > 0 {
					fmt.Fprintf(&b, "%s %d pin(s):\n%s\n", part.what, len(part.pins), strings.Join(pinLines(part.pins, false), "\n"))
				}
			}
			if s := strings.TrimLeft(scopedSections(cwd, dir), "\n"); s != "" {
				b.WriteString("Scoped pins injected after /clear:\n" + s + "\n")
			}
			if len(drop)+len(kept)+len(carry) == 0 && !strings.Contains(b.String(), "Scoped") {
				b.WriteString("Nothing is injected and no pins would be dropped.")
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
	text := Compose(cwd, dir, cfg)
	if text == "" {
		b.WriteString("Nothing would be injected (empty ledger).")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "Injected as context (~%d tokens):\n%s", len(text)/4, text)
	return b.String(), nil
}
