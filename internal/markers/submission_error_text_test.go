package markers

import (
	"errors"
	"strings"
	"testing"
)

func TestSubmissionErrorTextMasksURLCredentials(t *testing.T) {
	err := &SubmissionInvalidError{Message: "rpc error: POST https://user:pass@api.example.test/v1/markers?token=s3cr3t#frag rejected"}
	got := submissionErrorText(err)
	for _, secret := range []string{"user:pass", "token", "s3cr3t", "frag"} {
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
		{"dial postgres://svc:pgpass@db.example.test:5432/silo failed", "pgpass", "db.example.test"},
		{"GET HTTPS://u:uppass@api.example.test/x?sig=upsig: 403", "upsig", "api.example.test"},
		{"rpc error: code = Unauthenticated desc = invalid api_key=k3y123 for provider", "k3y123", "for provider"},
		{`rpc error: desc = rejected {"x-api-key": "hdrkey9"}`, "hdrkey9", "rejected"},
		{"upstream said: Authorization: Bearer eyJtok.en.sig was expired", "eyJtok", "was expired"},
		{"token expired, please retry", "", "token expired, please retry"},
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
