package ledger

import "regexp"

// secretRes match common credentials. Each match becomes [REDACTED:<kind>];
// for key=value forms only the value is replaced.
var secretRes = []struct {
	kind string
	re   *regexp.Regexp
	keep int // leading submatch kept verbatim (0: none)
}{
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), 0},
	{"aws-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})\b`), 0},
	{"slack-token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), 0},
	{"api-key", regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`), 0},
	{"google-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), 0},
	{"url-password", regexp.MustCompile(`([a-z][a-z0-9+.-]*://[^\s:/@]+:)[^\s@/]+@`), 1},
	{"secret", regexp.MustCompile(`(?i)(\b(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|client[_-]?secret)["']?\s*[:=]\s*["']?)[^\s"',;\[][^\s"',;]{3,}`), 1},
}

// Redact masks credentials in s and reports how many it masked.
func Redact(s string) (string, int) {
	n := 0
	for _, r := range secretRes {
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			n++
			tag := "[REDACTED:" + r.kind + "]"
			if r.keep == 0 {
				return tag
			}
			sub := r.re.FindStringSubmatch(m)
			if r.kind == "url-password" {
				return sub[1] + tag + "@"
			}
			return sub[1] + tag
		})
	}
	return s, n
}
