package ledger

import "regexp"

// secretRes match common credentials. Each match becomes [REDACTED:<kind>];
// for key=value forms only the value is replaced.
var secretRes = []struct {
	kind string
	re   *regexp.Regexp
	keep int // leading submatch kept verbatim (0: none); 2: also keep the value's quotes
}{
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), 0},
	{"aws-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})\b`), 0},
	{"slack-token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), 0},
	{"api-key", regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`), 0},
	{"stripe-key", regexp.MustCompile(`\b[rs]k_(?:live|test)_[A-Za-z0-9]{16,}`), 0},
	{"slack-webhook", regexp.MustCompile(`(?i)https://hooks\.slack\.com/services/[A-Za-z0-9/_-]+`), 0},
	{"google-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), 0},
	{"url-password", regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^\s:/@]+:)[^\s@/]+@`), 1},
	// A quoted value goes whole ("super secret value"); db_password, aws_secret_access_key count too.
	{"secret", regexp.MustCompile(`(?i)(\b(?:[a-z0-9]+[_-])*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|client[_-]?secret)["']?\s*[:=]\s*)(?:"[^"\n]{3,}"|'[^'\n]{3,}')`), 2},
	{"secret", regexp.MustCompile(`(?i)(\b(?:[a-z0-9]+[_-])*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|client[_-]?secret)["']?\s*[:=]\s*["']?)[^\s"',;\[][^\s"',;]{3,}`), 1},
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
			if r.keep == 2 {
				q := m[len(sub[1]) : len(sub[1])+1]
				return sub[1] + q + tag + q
			}
			return sub[1] + tag
		})
	}
	return s, n
}
