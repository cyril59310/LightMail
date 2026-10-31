// Package logging configures structured logging with secret redaction.
//
// Redaction is a safety net: code must never deliberately log secrets, but
// any attribute whose key looks sensitive is replaced by "[REDACTED]".
package logging

import (
	"io"
	"log/slog"
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

var sensitiveKey = regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|authorization|cookie|credential|api[_-]?key|master[_-]?key|private|code_verifier|verifier|^code$|refresh|access)`)

// bearerPattern catches tokens accidentally embedded in error strings,
// e.g. "Authorization: Bearer xyz" or XOAUTH2 initial responses.
var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+|auth=Bearer\s+|access_token=|refresh_token=|code=|password=)[^\s&"',;]+`)

// New returns a slog logger writing to w.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: replaceAttr}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.MessageKey || a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.SourceKey {
		if a.Key == slog.MessageKey {
			a.Value = slog.StringValue(Scrub(a.Value.String()))
		}
		return a
	}
	if sensitiveKey.MatchString(a.Key) && !isAllowedKey(a.Key) {
		return slog.String(a.Key, redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(Scrub(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			a.Value = slog.StringValue(Scrub(err.Error()))
		}
	}
	return a
}

// Keys that match the sensitive pattern but carry no secret.
func isAllowedKey(k string) bool {
	switch strings.ToLower(k) {
	case "access_mode", "token_expiry", "auth_type", "private_networks", "status_code", "http_code", "code_status":
		return true
	}
	return false
}

// Scrub removes token-like substrings from free text.
func Scrub(s string) string {
	if !bearerPattern.MatchString(s) {
		return s
	}
	return bearerPattern.ReplaceAllString(s, "${1}"+redacted)
}

// Secret is a string that never prints its value.
type Secret string

func (Secret) String() string                 { return redacted }
func (Secret) LogValue() slog.Value           { return slog.StringValue(redacted) }
func (Secret) GoString() string               { return redacted }
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (s Secret) Reveal() string               { return string(s) }
