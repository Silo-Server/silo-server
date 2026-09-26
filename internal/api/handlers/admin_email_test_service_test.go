package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	silomail "github.com/Silo-Server/silo-server/internal/mail"
)

type adminTestEmailSender struct {
	calls   int
	message silomail.Message
	err     error
}

func (s *adminTestEmailSender) Enabled(context.Context) bool { return true }
func (s *adminTestEmailSender) Send(_ context.Context, message silomail.Message) error {
	s.calls++
	s.message = message
	return s.err
}
func TestAdminEmailSingleSendAndSafeFailure(t *testing.T) {
	sender := &adminTestEmailSender{}
	h := NewEmailHandler(sender)
	result, err := h.SendAdminTestEmail(t.Context(), "recipient@example.test")
	if err != nil || !result.OK || sender.calls != 1 || len(sender.message.To) != 1 || sender.message.To[0] != "recipient@example.test" || sender.message.Subject != "Silo test email" || sender.message.HTMLBody == "" {
		t.Fatal(result, err, sender)
	}
	_, err = h.SendAdminTestEmail(t.Context(), "invalid recipient")
	if !errors.Is(err, ErrAdminEmailRecipient) || sender.calls != 1 {
		t.Fatal(err, sender.calls)
	}
	sender.err = errors.New("smtp failure with private credential")
	result, err = h.SendAdminTestEmail(t.Context(), "recipient@example.test")
	if err != nil || result.OK || strings.Contains(result.Message, "credential") || sender.calls != 2 {
		t.Fatal(result, err, sender.calls)
	}
	// The frozen bridge retains its old provider-error response and message.
	rec := httptest.NewRecorder()
	h.HandleTest(rec, httptest.NewRequest(http.MethodPost, "/admin/email/test", strings.NewReader(`{"to":"recipient@example.test"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "private credential") || sender.calls != 3 {
		t.Fatal(rec.Code, rec.Body.String(), sender.calls)
	}
	sender.err = silomail.ErrNotConfigured
	result, err = h.SendAdminTestEmail(t.Context(), "recipient@example.test")
	if err != nil || result.OK || !strings.Contains(result.Message, "not configured") {
		t.Fatal(result, err)
	}
}
