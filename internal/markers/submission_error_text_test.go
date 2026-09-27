package markers

import (
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

func TestSubmissionErrorTextMasksSecretsOutsideHTTPURLs(t *testing.T) {
	cases := []struct{ in, secret, keep string }{
		{"dial postgres://svc:FAKE_FIXTURE_2@db.example.test:5432/silo failed", "FAKE_FIXTURE_2", "db.example.test"},
		{"GET HTTPS://u:FAKE_FIXTURE_3@api.example.test/x?sig=FAKE_FIXTURE_4: 403", "FAKE_FIXTURE_", "api.example.test"},
		{"rpc error: code = Unauthenticated desc = invalid api_key=FAKE_FIXTURE_5 for provider", "FAKE_FIXTURE_5", "for provider"},
		{`rpc error: desc = rejected {"x-api-key": "FAKE_FIXTURE_6"}`, "FAKE_FIXTURE_6", "rejected"},
		{"upstream said: Authorization: Bearer FAKE_FIXTURE_7 was expired", "FAKE_FIXTURE_7", "Bearer [REDACTED] was expired"},
		{"token expired, please retry", "", "token expired, please retry"},
		{"session_token=FAKE_FIXTURE_8; retry=3", "FAKE_FIXTURE_8", "retry=3"},
		// Short or unpadded credentials after a scheme word are still masked.
		{"Authorization: Basic FAKEfixture", "FAKEfixture", "Basic [REDACTED]"},
		{"Authorization: Bearer fake-fixture", "fake-fixture", "Bearer [REDACTED]"},
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
