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
		name, path, method string
		outcome            notifications.EmailVerifyOutcome
		ok                 bool
		err                error
		status             int
		calls              int
	}{
		{"verify", "verify", http.MethodGet, notifications.EmailVerifyOK, false, nil, 200, 1},
		{"used", "verify", http.MethodGet, notifications.EmailVerifyInvalid, false, nil, 400, 1},
		{"conflict", "verify", http.MethodGet, notifications.EmailVerifyConflict, false, nil, 409, 1},
		{"verify-error", "verify", http.MethodGet, notifications.EmailVerifyInvalid, false, errors.New("private database detail"), 500, 1},
		{"unsubscribe-confirm", "unsubscribe", http.MethodGet, 0, false, nil, 200, 0},
		{"one-click", "unsubscribe", http.MethodPost, 0, true, nil, 200, 1},
		{"invalid", "unsubscribe", http.MethodPost, 0, false, nil, 400, 1},
		{"unsubscribe-error", "unsubscribe", http.MethodPost, 0, false, errors.New("private database detail"), 500, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*fake = fakeNotificationEmailLinks{outcome: tc.outcome, unsubscribed: tc.ok, err: tc.err}
			req := httptest.NewRequest(tc.method, "/api/v2/notifications/email/"+tc.path+"?token=synthetic-proof", strings.NewReader("List-Unsubscribe=One-Click"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "text/html")
			// This token capability is independent of an ambient browser login.
			req.Header.Set("Authorization", "Bearer unrelated-expired-session")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status || fake.calls != tc.calls || (tc.calls > 0 && fake.token != "synthetic-proof") {
				t.Fatalf("%d %s calls=%d token=%s", rec.Code, rec.Body.String(), fake.calls, fake.token)
			}
			bridge := httptest.NewRecorder()
			if tc.path == "verify" {
				links.HandleVerify(bridge, req)
			} else {
				links.HandleUnsubscribe(bridge, req)
			}
			if rec.Body.String() != bridge.Body.String() || rec.Code != bridge.Code {
				t.Fatal("bridge HTML result changed")
			}
			if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatalf("headers: %v", rec.Header())
			}
			if strings.Contains(rec.Body.String(), "synthetic-proof") || strings.Contains(rec.Body.String(), "private database detail") {
				t.Fatal("proof or internal error reflected")
			}
		})
	}
}
