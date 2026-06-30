package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

type fakeAdminACLAuthorizer struct {
	decision auth.AccessDecision
	request  auth.AccessRequest
	err      error
	called   bool
}

func (f *fakeAdminACLAuthorizer) Authorize(_ context.Context, request auth.AccessRequest) (auth.AccessDecision, error) {
	f.called = true
	f.request = request
	return f.decision, f.err
}

func (f *fakeAdminACLAuthorizer) Explain(_ context.Context, request auth.AccessRequest) (auth.AccessExplanation, error) {
	decision, err := f.Authorize(context.Background(), request)
	return auth.AccessExplanation{Request: request, Decision: decision}, err
}

func runAdminCapabilityMiddleware(role string, action auth.ACLAction, authorizer auth.Authorizer, check PrimaryProfileChecker, profileID string) (int, *fakeAdminACLAuthorizer) {
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	if profileID != "" {
		req.Header.Set("X-Profile-Id", profileID)
	}
	req = req.WithContext(SetClaims(req.Context(), &auth.Claims{UserID: 42, Role: role, TokenType: auth.TokenTypeAccess}))

	next := RequireAdminCapability(authorizer, action, check)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	fake, _ := authorizer.(*fakeAdminACLAuthorizer)
	return rec.Code, fake
}

func TestRequireAdminCapability_AllowsDelegatedUserWithACLGrant(t *testing.T) {
	acl := &fakeAdminACLAuthorizer{decision: auth.AccessDecision{Allowed: true, ReasonCode: "rule_allow"}}
	code, fake := runAdminCapabilityMiddleware("user", auth.ActionUsersManage, acl, nil, "")

	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
	if fake == nil || !fake.called {
		t.Fatalf("ACL authorizer was not called")
	}
	if fake.request.UserID != 42 || fake.request.Action != auth.ActionUsersManage || fake.request.ResourceType != auth.ResourceServer || fake.request.ResourceID != "*" {
		t.Fatalf("ACL request = %#v", fake.request)
	}
}

func TestRequireAdminCapability_RejectsDelegatedUserWithoutACLGrant(t *testing.T) {
	acl := &fakeAdminACLAuthorizer{decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"}}
	code, _ := runAdminCapabilityMiddleware("user", auth.ActionUsersManage, acl, nil, "")

	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireAdminCapability_AllowsPrimaryAdminWithoutACLAuthorizer(t *testing.T) {
	code, _ := runAdminCapabilityMiddleware("admin", auth.ActionUsersManage, nil, primaryChecker(true, true, nil), "primary")

	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireAdminCapability_AdminOnNonPrimaryProfileNeedsACLGrant(t *testing.T) {
	acl := &fakeAdminACLAuthorizer{decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"}}
	code, fake := runAdminCapabilityMiddleware("admin", auth.ActionUsersManage, acl, primaryChecker(false, true, nil), "kid")

	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
	if fake == nil || !fake.called {
		t.Fatalf("ACL authorizer was not called")
	}
	if fake.request.PrimaryProfile {
		t.Fatalf("primary profile flag = true, want false")
	}
}

func TestRequireAdminCapability_AllowsAdminOnNonPrimaryProfileWithExplicitACLGrant(t *testing.T) {
	acl := &fakeAdminACLAuthorizer{decision: auth.AccessDecision{Allowed: true, ReasonCode: "rule_allow"}}
	code, fake := runAdminCapabilityMiddleware("admin", auth.ActionUsersManage, acl, primaryChecker(false, true, nil), "kid")

	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
	if fake == nil || !fake.called {
		t.Fatalf("ACL authorizer was not called")
	}
	if fake.request.PrimaryProfile {
		t.Fatalf("primary profile flag = true, want false")
	}
}

func TestRequireAdminCapability_AuthorizerErrorReturnsInternalError(t *testing.T) {
	acl := &fakeAdminACLAuthorizer{err: errors.New("database offline")}
	code, _ := runAdminCapabilityMiddleware("user", auth.ActionUsersManage, acl, nil, "")

	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", code, http.StatusInternalServerError)
	}
}
