package invitations

import (
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
)

func TestComposeInvitationEmailEscapesNote(t *testing.T) {
	now := time.Now()
	content := composeInvitationEmail(
		mail.Brand{}, "quick", "Silo", "marco@example.com",
		"https://silo.example.com/invite/tok",
		`<script>alert("hi")</script>`,
		now.Add(7*24*time.Hour), now,
	)
	if strings.Contains(content.HTML, "<script>") {
		t.Error("note not escaped in HTML body")
	}
	if !strings.Contains(content.HTML, "&lt;script&gt;") {
		t.Error("escaped note missing from HTML body")
	}
	if !strings.Contains(content.Text, "marco@example.com") {
		t.Error("text body missing sign-in address")
	}
	if content.Subject != "quick invited you to Silo" {
		t.Errorf("subject = %q", content.Subject)
	}
}
