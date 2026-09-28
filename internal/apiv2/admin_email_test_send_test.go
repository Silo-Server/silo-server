package apiv2

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

type adminEmailTestStub struct {
	calls int
	to    string
	user  int
	err   error
}

func (s *adminEmailTestStub) SendAdminTestEmail(ctx context.Context, to string) (handlers.AdminEmailTestResult, error) {
	s.calls++
	s.to = to
	s.user = apimw.GetUserID(ctx)
	return handlers.AdminEmailTestResult{OK: true, DurationMS: 3}, s.err
}
func TestAdminEmailTestTransport(t *testing.T) {
	s := &adminEmailTestStub{}
	deps := requestDeps(fixtureRequests())
	deps.AdminEmailTests = s
	h := NewHandler(deps)
	path := Prefix + "/admin/email/test"
	r := do(t, h, "POST", path, `{"to":"recipient@example.test"}`, nil)
	if r.Code != 401 || s.calls != 0 {
		t.Fatal(r.Code, s.calls)
	}
	r = do(t, h, "POST", path, `{"to":null}`, actingRequestAdmin)
	if r.Code != 422 || s.calls != 0 {
		t.Fatal(r.Code, r.Body.String(), s.calls)
	}
	r = do(t, h, "POST", path, `{"to":"recipient@example.test"}`, actingRequestAdmin)
	if r.Code != 200 || s.calls != 1 || s.user == 0 || s.to != "recipient@example.test" {
		t.Fatal(r.Code, r.Body.String(), s)
	}
	s.err = handlers.ErrAdminEmailRecipient
	r = do(t, h, "POST", path, `{"to":"bad"}`, actingRequestAdmin)
	if r.Code != 422 {
		t.Fatal(r.Code, r.Body.String())
	}
	s.err = handlers.ErrAdminEmailUnavailable
	r = do(t, h, "POST", path, `{"to":"recipient@example.test"}`, actingRequestAdmin)
	if r.Code != 503 {
		t.Fatal(r.Code, r.Body.String())
	}
}
