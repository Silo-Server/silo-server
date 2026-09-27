package markers

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

// The values these tests mask are obvious placeholders, not credential-shaped
// literals; masking does not depend on what the value looks like.

func TestSubmissionErrorTextMasksURLCredentials(t *testing.T) {
	err := &SubmissionInvalidError{Message: "rpc error: POST https://FAKE_USER:FAKE_PASS@api.example.test/v1/markers?token=FAKE_FIXTURE_1#frag rejected"}
	got := submissionErrorText(err)
	for _, secret := range []string{"FAKE_USER", "FAKE_PASS", "token", "FAKE_FIXTURE_1", "frag"} {
		if strings.Contains(got, secret) {
			// Never echo the value this test protects.
			t.Fatalf("submissionErrorText kept a value that should have been masked (output length %d)", len(got))
		}
	}
	if !strings.Contains(got, "https://api.example.test/v1/markers") || !strings.Contains(got, "rejected") {
		t.Fatalf("submissionErrorText dropped the useful part: %s", got)
	}
	if plain := submissionErrorText(errors.New("connection reset")); plain != "connection reset" {
		t.Fatalf("plain error changed: %q", plain)
	}
}

// runtimeLowercaseToken returns random lowercase letters: the hardest shape to
// tell apart from prose, generated per run so no token-like literal is committed.
func runtimeLowercaseToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = 'a' + b[i]%26
	}
	return string(b)
}

func TestSubmissionErrorTextMasksSecretsOutsideHTTPURLs(t *testing.T) {
	tok := runtimeLowercaseToken(t)
	cases := []struct{ in, secret, keep string }{
		{"dial postgres://svc:FAKE_FIXTURE_2@db.example.test:5432/silo failed", "FAKE_FIXTURE_2", "db.example.test"},
		{"GET HTTPS://u:FAKE_FIXTURE_3@api.example.test/x?sig=FAKE_FIXTURE_4: 403", "FAKE_FIXTURE_", "api.example.test"},
		{"rpc error: code = Unauthenticated desc = invalid api_key=FAKE_FIXTURE_5 for provider", "FAKE_FIXTURE_5", "for provider"},
		{`rpc error: desc = rejected {"x-api-key": "FAKE_FIXTURE_6"}`, "FAKE_FIXTURE_6", "rejected"},
		{"upstream said: Authorization: Bearer FAKE_FIXTURE_7 was expired", "FAKE_FIXTURE_7", "Bearer [REDACTED] was expired"},
		{"token expired, please retry", "", "token expired, please retry"},
		{"session_token=FAKE_FIXTURE_8; retry=3", "FAKE_FIXTURE_8", "retry=3"},
		// Short or unpadded credentials after a scheme word are still masked.
		{"Authorization: Basic " + strings.ToUpper(tok[:4]) + tok[4:], strings.ToUpper(tok[:4]) + tok[4:], "Basic [REDACTED]"},
		{"Authorization: Bearer " + tok[:6] + "-" + tok[6:], tok[:6] + "-" + tok[6:], "Bearer [REDACTED]"},
		{"Authorization: Bearer " + tok, tok, "Bearer [REDACTED]"},
		{`Authorization: Bearer "` + tok + `"`, tok, `Bearer "[REDACTED]"`},
		// Harmless numeric pairs keep their values.
		{"GET /v1/markers?page=1; retry=3 failed", "", "page=1; retry=3 failed"},
		// An auth scheme word followed by prose is not a credential.
		{"code = Unauthenticated desc = basic authentication required", "", "basic authentication required"},
	}
	for _, tc := range cases {
		got := submissionErrorText(&SubmissionInvalidError{Message: tc.in})
		if tc.secret != "" && strings.Contains(got, tc.secret) {
			// Never echo the value this test protects.
			t.Fatalf("case %q kept a value that should have been masked", tc.keep)
		}
		if !strings.Contains(got, tc.keep) {
			t.Fatalf("case %q dropped the useful part", tc.keep)
		}
	}
}
