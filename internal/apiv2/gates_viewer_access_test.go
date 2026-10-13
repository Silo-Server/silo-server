package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type failingViewerResolver struct{ err error }

func (r failingViewerResolver) Resolve(context.Context, access.ResolveInput) (access.Scope, error) {
	return access.Scope{}, r.err
}

// A viewer access policy that runs out of time reaches a v2 client as
// dependency_unavailable with Retry-After, worded for viewer access rather
// than sign-in, instead of internal_error.
func TestViewerAccessPolicyTimeoutProblem(t *testing.T) {
	timeout := fmt.Errorf("resolve viewer scope policy: %w",
		fmt.Errorf("%w: %w after 100ms: %w", policy.ErrPolicyEvalFailed, policy.ErrPolicyEvalTimeout, context.DeadlineExceeded))
	r := httptest.NewRequest(http.MethodGet, Prefix+"/playback/media", nil)
	r.Header.Set("X-Profile-Id", "p1")
	r = r.WithContext(apimw.SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user", SessionID: "s"}))
	d := &denialWriter{header: make(http.Header)}
	apimw.NewViewerAccessMiddleware(failingViewerResolver{err: timeout}).
		RequireViewerAccess(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("refused request reached the operation") })).
		ServeHTTP(d, r)
	p := d.problem()
	if p.Type != TypeDependencyUnavailable.URI() || p.Status != http.StatusServiceUnavailable || !strings.Contains(p.Detail, "viewer's access") {
		t.Fatalf("problem = %s %d %q", p.Type, p.Status, p.Detail)
	}
	if got := p.GetHeaders().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}
