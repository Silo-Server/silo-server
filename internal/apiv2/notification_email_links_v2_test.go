package apiv2_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeNotificationEmailLinks struct {
	calls        int
	token        string
	outcome      notifications.EmailVerifyOutcome
	unsubscribed bool
	err          error
}

func (f *fakeNotificationEmailLinks) VerifyEmailToken(_ context.Context, token string) (notifications.EmailVerifyOutcome, error) {
	f.calls++
	f.token = token
	return f.outcome, f.err
}
func (f *fakeNotificationEmailLinks) UnsubscribeEmail(_ context.Context, token string) (bool, error) {
	f.calls++
	f.token = token
	return f.unsubscribed, f.err
}

func TestNotificationEmailLinksV2PreserveHTML(t *testing.T) {
	fake := new(fakeNotificationEmailLinks)
	links := handlers.NewEmailLinkHandler(fake)
	h := apiv2.NewHandler(apiv2.Dependencies{NotificationEmailLinks: links})
	for _, tc := range []struct {
		name, path, method, token string
		outcome                   notifications.EmailVerifyOutcome
		ok                        bool
		err                       error
		status                    int
	}{
		{"verify", "verify", http.MethodGet, "synthetic-proof", notifications.EmailVerifyOK, false, nil, 200},
		{"used", "verify", http.MethodGet, "synthetic-proof", notifications.EmailVerifyInvalid, false, nil, 400},
		{"conflict", "verify", http.MethodGet, "synthetic-proof", notifications.EmailVerifyConflict, false, nil, 409},
		{"verify-error", "verify", http.MethodGet, "synthetic-proof", notifications.EmailVerifyInvalid, false, errors.New("private database detail"), 500},
		{"unsubscribe", "unsubscribe", http.MethodGet, "synthetic-proof", 0, true, nil, 200},
		{"unsubscribe-missing", "unsubscribe", http.MethodGet, "", 0, true, nil, 400},
		{"one-click", "unsubscribe", http.MethodPost, "synthetic-proof", 0, true, nil, 200},
		{"invalid", "unsubscribe", http.MethodPost, "synthetic-proof", 0, false, nil, 400},
		{"unsubscribe-error", "unsubscribe", http.MethodPost, "synthetic-proof", 0, false, errors.New("private database detail"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*fake = fakeNotificationEmailLinks{outcome: tc.outcome, unsubscribed: tc.ok, err: tc.err}
			req := httptest.NewRequest(tc.method, "/api/v2/notifications/email/"+tc.path+"?token="+tc.token, strings.NewReader("List-Unsubscribe=One-Click"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "text/html")
			// The confirmation page is no-referrer, so its form post arrives with Origin: null.
			req.Header.Set("Origin", "null")
			// This token capability is independent of an ambient browser login.
			req.Header.Set("Authorization", "Bearer unrelated-expired-session")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			// Opening the unsubscribe link must not change preferences; only POST does.
			wantCalls, wantToken := 1, tc.token
			if tc.path == "unsubscribe" && tc.method == http.MethodGet {
				wantCalls, wantToken = 0, ""
			}
			if rec.Code != tc.status || fake.calls != wantCalls || fake.token != wantToken {
				t.Fatalf("%d %s calls=%d token=%s", rec.Code, rec.Body.String(), fake.calls, fake.token)
			}
			if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatalf("headers: %v", rec.Header())
			}
			body := rec.Body.String()
			if strings.Contains(body, "synthetic-proof") || strings.Contains(body, "private database detail") {
				t.Fatal("proof or internal error reflected")
			}
			// The confirmation form has no action, so it posts back to the URL it
			// was opened at, token included.
			if wantForm := tc.name == "unsubscribe"; strings.Contains(body, `<form method="post"`) != wantForm || strings.Contains(body, "action=") {
				t.Fatalf("confirmation form: %s", body)
			}
		})
	}
}
