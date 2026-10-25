package gateway

import (
	"errors"
	"regexp"
	"strings"
)

// Bot APIs such as Telegram embed the credential in the request URL
// (https://api.telegram.org/bot<token>/<method>). The HTTP client
// returns *url.Error values whose message contains that full URL, so
// any error that is logged or returned verbatim leaks the bot token —
// this happened in practice: a Termux gateway log line printed the
// complete token inside a failed getMe URL. The helpers here scrub
// such secrets from error text before it crosses a log/return
// boundary. They never change the request itself, only the string
// representation of failures.

// botTokenURLPattern matches the "/bot<secret>/" path segment used by
// the Telegram Bot API (and compatible clones).
var botTokenURLPattern = regexp.MustCompile(`/bot[^/\s"']+/`)

// RedactBotToken replaces every "/bot<secret>/" URL path segment in s
// with "/bot<redacted>/".
func RedactBotToken(s string) string {
	return botTokenURLPattern.ReplaceAllString(s, "/bot<redacted>/")
}

// RedactSecrets returns s with every literal occurrence of the given
// secrets replaced by "<redacted>", and any bot-token URL segment
// scrubbed as well. Empty secrets are ignored.
func RedactSecrets(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "<redacted>")
		}
	}
	return RedactBotToken(s)
}

// RedactErrorText is RedactSecrets for an error's message. A nil error
// yields an empty string.
func RedactErrorText(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	return RedactSecrets(err.Error(), secrets...)
}

// SanitizedError returns an error whose message is the redacted text
// of err. Use it instead of fmt.Errorf("...: %w", err) at boundaries
// where the error will be logged or shown: wrapping with %w would
// keep printing the original (secret-bearing) message. The unwrapped
// chain is dropped on purpose — redaction beats chain fidelity for
// credential-bearing URLs.
func SanitizedError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return errors.New(RedactErrorText(err, secrets...))
}
