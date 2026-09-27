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
			t.Fatalf("submissionErrorText kept %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "https://api.example.test/v1/markers") || !strings.Contains(got, "rejected") {
		t.Fatalf("submissionErrorText dropped the useful part: %s", got)
	}
	if plain := submissionErrorText(errors.New("connection reset")); plain != "connection reset" {
		t.Fatalf("plain error changed: %q", plain)
	}
}
