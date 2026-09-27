package handlers_test

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
	for _, tc := range []struct {
		name, path, method string
		outcome            notifications.EmailVerifyOutcome
		ok                 bool
		err                error
		status             int
	}{
		{"verify", "verify", http.MethodGet, notifications.EmailVerifyOK, false, nil, 200},
		{"used", "verify", http.MethodGet, notifications.EmailVerifyInvalid, false, nil, 400},
		{"conflict", "verify", http.MethodGet, notifications.EmailVerifyConflict, false, nil, 409},
		{"verify-error", "verify", http.MethodGet, notifications.EmailVerifyInvalid, false, errors.New("private database detail"), 500},
		{"unsubscribe", "unsubscribe", http.MethodGet, 0, true, nil, 200},
		{"one-click", "unsubscribe", http.MethodPost, 0, true, nil, 200},
		{"invalid", "unsubscribe", http.MethodPost, 0, false, nil, 400},
		{"unsubscribe-error", "unsubscribe", http.MethodPost, 0, false, errors.New("private database detail"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeNotificationEmailLinks{outcome: tc.outcome, unsubscribed: tc.ok, err: tc.err}
			links := handlers.NewEmailLinkHandler(fake)
			h := apiv2.NewHandler(apiv2.Dependencies{NotificationEmailLinks: links})
			req := httptest.NewRequest(tc.method, "/api/v2/notifications/email/"+tc.path+"?token=synthetic-proof", strings.NewReader("List-Unsubscribe=One-Click"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "text/html")
			// This token capability is independent of an ambient browser login.
			req.Header.Set("Authorization", "Bearer unrelated-expired-session")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status || fake.calls != 1 || fake.token != "synthetic-proof" {
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

func TestNotificationEmailLinksV2MissingProofAndService(t *testing.T) {
	fake := new(fakeNotificationEmailLinks)
	h := apiv2.NewHandler(apiv2.Dependencies{NotificationEmailLinks: handlers.NewEmailLinkHandler(fake)})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/notifications/email/verify", nil))
	if rec.Code != 400 || fake.calls != 1 || fake.token != "" {
		t.Fatalf("missing token: %d calls=%d", rec.Code, fake.calls)
	}
	rec = httptest.NewRecorder()
	apiv2.NewHandler(apiv2.Dependencies{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v2/notifications/email/unsubscribe?token=synthetic", nil))
	if rec.Code != 503 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("missing service: %d %v", rec.Code, rec.Header())
	}
}
