package app

import (
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// NewLogger returns a JSON logger that redacts credentials.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: redact}))
}

const redacted = "[redacted]"

// sensitiveWords mark an attribute key as a credential when one word of the
// key, split at "_", "-" and ".", is in the set.
var sensitiveWords = map[string]bool{
	"authorization": true, "token": true, "secret": true, "password": true,
	"cookie": true, "apikey": true, "credential": true, "ciphertext": true,
}

// credentialPattern matches credentials inside free text, such as an error
// message that quotes a request header or a URL with a password.
var credentialPattern = regexp.MustCompile(`(?i)(bearer|basic|token)\s+[A-Za-z0-9._~+/=-]+|://[^/\s:@]+:[^/\s@]+@`)

func redact(_ []string, a slog.Attr) slog.Attr {
	words := strings.FieldsFunc(strings.ToLower(a.Key), func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	for i, w := range words {
		if sensitiveWords[w] || (w == "key" && i > 0 && words[i-1] == "api") {
			return slog.String(a.Key, redacted)
		}
	}
	if a.Value.Kind() == slog.KindString {
		v := a.Value.String()
		if credentialPattern.MatchString(v) {
			return slog.String(a.Key, RedactText(v))
		}
	}
	return a
}

// RedactText replaces credentials in free text.
func RedactText(s string) string {
	return credentialPattern.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "://") {
			return "://" + redacted + "@"
		}
		scheme, _, _ := strings.Cut(m, " ")
		return scheme + " " + redacted
	})
}
