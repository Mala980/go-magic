package gateway

import (
	"errors"
	"strings"
	"testing"
)

// fakeToken is a synthetic credential used only to prove redaction; it
// is not a real token.
const fakeToken = "123456:FAKE-token_for-redaction-tests"

func TestRedactBotTokenInURL(t *testing.T) {
	in := `Post "https://api.telegram.org/bot` + fakeToken + `/getMe": dial tcp: lookup api.telegram.org on [::1]:53: connection refused`
	out := RedactBotToken(in)
	if strings.Contains(out, fakeToken) {
		t.Fatalf("token survived redaction: %q", out)
	}
	if !strings.Contains(out, "/bot<redacted>/getMe") {
		t.Fatalf("redacted form missing: %q", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Fatalf("rest of the error must be preserved: %q", out)
	}
}

func TestRedactSecretsLiteral(t *testing.T) {
	in := "auth failed for token " + fakeToken + " on host"
	out := RedactSecrets(in, fakeToken)
	if strings.Contains(out, fakeToken) {
		t.Fatalf("literal secret survived: %q", out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Fatalf("expected redaction marker: %q", out)
	}
}

func TestRedactErrorTextNilAndPlain(t *testing.T) {
	if got := RedactErrorText(nil); got != "" {
		t.Fatalf("nil error must yield empty string, got %q", got)
	}
	err := errors.New("plain failure without secrets")
	if got := RedactErrorText(err); got != err.Error() {
		t.Fatalf("secret-free error changed: %q", got)
	}
}
