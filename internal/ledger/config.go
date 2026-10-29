package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Key modes: which ledger a prompt, pin or decision belongs to.
const (
	KeySession = "session" // one ledger per Claude Code session (default)
	KeyBranch  = "branch"  // one per branch, shared by all sessions on it
	KeyRepo    = "repo"    // one per repo, shared by all sessions and branches
)

// RepoKey is the ledger dir name in repo mode; Safe never produces "@", so no branch
// or session can collide with it.
const RepoKey = "@repo"

// RepoConfigFile is the repo layer, at the top of the work tree (commit it to share).
const RepoConfigFile = ".agent-ledger.json"

// Config is the effective configuration: defaults < user file < repo file < env.
type Config struct {
	Key           string `json:"key"`            // session | branch | repo
	Clear         string `json:"clear"`          // drop | keep (default: drop for session, keep otherwise)
	PruneDays     int    `json:"prune_days"`     // idle days before a ledger is deleted; 0 = never
	RecentPrompts int    `json:"recent_prompts"` // prompts re-injected after compact
	Decisions     int    `json:"decisions"`      // decisions re-injected after compact
	BudgetChars   int    `json:"budget_chars"`   // injection budget (pins are never cut)

	Source   map[string]string `json:"-"` // field -> where its value came from
	Problems []string          `json:"-"` // ignored keys and values
}

type field struct {
	name          string
	str           *string
	num           *int
	allowed       []string
	minimum, maxi int
}

func (c *Config) fields() []field {
	return []field{
		{name: "key", str: &c.Key, allowed: []string{KeySession, KeyBranch, KeyRepo}},
		{name: "clear", str: &c.Clear, allowed: []string{"drop", "keep"}},
		{name: "prune_days", num: &c.PruneDays, minimum: 0, maxi: 36500},
		{name: "recent_prompts", num: &c.RecentPrompts, minimum: 0, maxi: 100},
		{name: "decisions", num: &c.Decisions, minimum: 0, maxi: 200},
		{name: "budget_chars", num: &c.BudgetChars, minimum: 1000, maxi: 400000},
	}
}

// DefaultConfig is today's behaviour.
func DefaultConfig() Config {
	c := Config{Key: KeySession, PruneDays: PruneDays, RecentPrompts: RecentPrompts,
		Decisions: RecentDecs, BudgetChars: Budget, Source: map[string]string{}}
	for _, f := range c.fields() {
		c.Source[f.name] = "default"
	}
	return c
}

// UserConfigPath is $XDG_CONFIG_HOME/agent-ledger/config.json, else
// ~/.config/agent-ledger/config.json (%APPDATA%\agent-ledger\config.json on Windows).
func UserConfigPath() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "agent-ledger", "config.json")
	}
	if runtime.GOOS == "windows" {
		if v := os.Getenv("APPDATA"); v != "" {
			return filepath.Join(v, "agent-ledger", "config.json")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "agent-ledger", "config.json")
}

// RepoConfigPath is the repo layer for cwd, or "" outside a work tree.
func RepoConfigPath(cwd string) string {
	top, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return ""
	}
	return filepath.Join(top, RepoConfigFile)
}

// LoadConfig layers the defaults, the user file, the repo file of cwd and the
// AGENT_LEDGER_<FIELD> env vars. Bad files, unknown keys and invalid values are
// ignored (the lower layer wins) and listed in Problems; it never fails.
func LoadConfig(cwd string) Config {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	c := DefaultConfig()
	c.layerFile(UserConfigPath())
	if p := RepoConfigPath(cwd); p != "" {
		c.layerFile(p)
	}
	for _, f := range c.fields() {
		env := "AGENT_LEDGER_" + strings.ToUpper(f.name)
		if v, ok := os.LookupEnv(env); ok && v != "" {
			c.set(f, v, "env "+env)
		}
	}
	if c.Clear == "" {
		c.Clear = "keep"
		if c.Key == KeySession {
			c.Clear = "drop"
		}
		c.Source["clear"] = "default for key=" + c.Key
	}
	return c
}

func (c *Config) layerFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		c.Problems = append(c.Problems, fmt.Sprintf("%s: not valid JSON, ignored", path))
		return
	}
	known := map[string]field{}
	for _, f := range c.fields() {
		known[f.name] = f
	}
	names := make([]string, 0, len(raw))
	for k := range raw {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		f, ok := known[k]
		if !ok {
			c.Problems = append(c.Problems, fmt.Sprintf("%s: unknown key %q ignored", path, k))
			continue
		}
		switch v := raw[k].(type) {
		case string:
			c.set(f, v, path)
		case float64:
			c.set(f, strconv.FormatFloat(v, 'f', -1, 64), path)
		default:
			c.Problems = append(c.Problems, fmt.Sprintf("%s: %s: unsupported value %v, ignored", path, k, raw[k]))
		}
	}
}

func (c *Config) set(f field, v, from string) {
	v = strings.TrimSpace(v)
	if f.str != nil {
		for _, a := range f.allowed {
			if strings.EqualFold(v, a) {
				*f.str = a
				c.Source[f.name] = from
				return
			}
		}
		c.Problems = append(c.Problems, fmt.Sprintf("%s: %s=%q ignored (want %s)", from, f.name, v, strings.Join(f.allowed, "|")))
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < f.minimum || n > f.maxi {
		c.Problems = append(c.Problems, fmt.Sprintf("%s: %s=%q ignored (want an integer %d..%d)", from, f.name, v, f.minimum, f.maxi))
		return
	}
	*f.num = n
	c.Source[f.name] = from
}

// Describe lists each effective value with its source, then the problems.
func (c Config) Describe() string {
	var b strings.Builder
	for _, f := range c.fields() {
		v := ""
		if f.str != nil {
			v = *f.str
		} else {
			v = strconv.Itoa(*f.num)
		}
		fmt.Fprintf(&b, "%-15s %-8s (%s)\n", f.name, v, c.Source[f.name])
	}
	for _, p := range c.Problems {
		fmt.Fprintf(&b, "problem: %s\n", p)
	}
	return strings.TrimRight(b.String(), "\n")
}
