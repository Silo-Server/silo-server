package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type unsubscribeProcessor struct {
	calls int
}

func (*unsubscribeProcessor) VerifyEmailToken(context.Context, string) (notifications.EmailVerifyOutcome, error) {
	panic("verification is outside unsubscribe")
}

func (p *unsubscribeProcessor) UnsubscribeEmail(context.Context, string) (bool, error) {
	p.calls++
	return true, nil
}

func TestUnsubscribeSafeMethods(t *testing.T) {
	for _, token := range []string{"synthetic-capability", `<script>synthetic</script>"&`, ""} {
		t.Run(token, func(t *testing.T) {
			p := new(unsubscribeProcessor)
			h := NewEmailLinkHandler(p)
			for range 2 {
				rec := httptest.NewRecorder()
				h.HandleUnsubscribe(rec, httptest.NewRequest(http.MethodGet, "/api/v1/notifications/email/unsubscribe?token="+url.QueryEscape(token), nil))
				wantStatus := http.StatusOK
				if token == "" {
					wantStatus = http.StatusBadRequest
				}
				if rec.Code != wantStatus || p.calls != 0 {
					t.Fatalf("status=%d calls=%d", rec.Code, p.calls)
				}
				assertEmailPageHeaders(t, rec)
				body := rec.Body.String()
				if token == "" {
					if !strings.Contains(body, "Link invalid") || strings.Contains(body, "<form") {
						t.Fatalf("missing-token page: %s", body)
					}
					continue
				}
				for _, want := range []string{`<html lang="en">`, "<main ", "<h1 ", `<form method="post"`, `<button type="submit">Unsubscribe</button>`, "button:focus-visible"} {
					if !strings.Contains(body, want) {
						t.Errorf("confirmation missing %q", want)
					}
				}
				for _, forbidden := range []string{token, "action=", "<base", "<script", "<input", "autofocus"} {
					if strings.Contains(body, forbidden) {
						t.Errorf("confirmation contains %q", forbidden)
					}
				}
			}
		})
	}
}

func TestUnsubscribeRejectsUnsupportedMethods(t *testing.T) {
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			p := new(unsubscribeProcessor)
			rec := httptest.NewRecorder()
			NewEmailLinkHandler(p).HandleUnsubscribe(rec, httptest.NewRequest(method, "/?token=synthetic-capability", nil))
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, POST" || p.calls != 0 {
				t.Fatalf("status=%d headers=%v calls=%d", rec.Code, rec.Header(), p.calls)
			}
			assertEmailPageHeaders(t, rec)
		})
	}
}

func TestEmailLinkPageEscapesText(t *testing.T) {
	rec := httptest.NewRecorder()
	writeEmailLinkPage(rec, http.StatusBadRequest, `<title>"&`, `<script>"&`)
	assertEmailPageHeaders(t, rec)
	body := rec.Body.String()
	if strings.Contains(body, `<title>"&`) || strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;&#34;&amp;") {
		t.Fatalf("unescaped page: %s", body)
	}
}

func assertEmailPageHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers: %v", rec.Header())
	}
}
