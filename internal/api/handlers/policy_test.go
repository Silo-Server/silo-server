package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/policy"
)

func TestPolicyEditorDisabledGatesEditorEndpoints(t *testing.T) {
	handler := NewPolicyHandler(policy.NewSystem(nil, nil, nil), nil, nil, func() bool { return false })

	rec := httptest.NewRecorder()
	req := newPolicyHandlerRequest(http.MethodPost, "/admin/policy/validate", map[string]any{
		"domain": policy.DomainScope,
		"source": validHandlerPolicySource(),
	}, nil)
	handler.HandleValidate(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s, want forbidden", rec.Code, rec.Body.String())
	}
}

func newPolicyHandlerRequest(method, target string, payload any, params map[string]string) *http.Request {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		body = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, target, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{
		UserID:    42,
		Role:      "admin",
		SessionID: "session-42",
		TokenType: auth.TokenTypeAccess,
	})
	return req.WithContext(ctx)
}

func validHandlerPolicySource() string {
	return `package silo_custom.scope

import rego.v1

override(base, _) := base`
}
