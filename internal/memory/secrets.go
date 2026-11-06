package memory

import (
	"regexp"
	"strings"
)

// secretPatterns find credentials by shape. Each match is replaced by
// redactionMarker, keeping the leading group (the variable name, "Bearer ",
// the URL scheme and user) so the text still says what was removed.
var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?(?:-----END [A-Z0-9 ]+-----|$)`), redactionMarker},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]*`), redactionMarker},
	{regexp.MustCompile(`(\b[A-Za-z][A-Za-z0-9+.\-]*://[^\s/:@]+:)[^\s/@]+@`), `${1}` + redactionMarker + `@`},
	{regexp.MustCompile(`(?i)(\bBearer\s+)[A-Za-z0-9._~+/=\-]{8,}`), `${1}` + redactionMarker},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), redactionMarker},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`), redactionMarker},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), redactionMarker},
	// NAME=value, where NAME holds KEY, PASSWORD, SECRET or TOKEN in any case.
	{regexp.MustCompile("(?i)(\\b[A-Za-z0-9_.\\-]*(?:key|password|passwd|secret|token)[A-Za-z0-9_.\\-]*\\s*=\\s*[\"'`]?)[^\\s\"'`]+"), `${1}` + redactionMarker},
	// NAME: value, only for upper-case names or a quoted value, which is how
	// config and prose differ ("token: the next one" is left alone).
	{regexp.MustCompile(`(\b[A-Z0-9_]*(?:KEY|PASSWORD|PASSWD|SECRET|TOKEN)[A-Z0-9_]*:\s*)[^\s"']{4,}`), `${1}` + redactionMarker},
	{regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.\-]*(?:key|password|passwd|secret|token)[A-Za-z0-9_.\-]*"?\s*:\s*")[^"\n]+"`), `${1}` + redactionMarker + `"`},
}

// BlankAfterRedaction reports whether s holds nothing but white space and
// redaction markers, so saving it would store no information.
func BlankAfterRedaction(s string) bool {
	return strings.TrimSpace(strings.ReplaceAll(s, redactionMarker, "")) == ""
}

// RedactSecrets replaces credentials in s by the redaction marker: values in
// KEY, PASSWORD, SECRET and TOKEN assignments, bearer tokens, GitHub, OpenAI
// style and AWS keys, JWTs, PEM blocks and passwords inside URLs. The save
// path does not call it; commands that ingest untrusted text, such as
// "session ingest", do.
func RedactSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return redact(s)
}
