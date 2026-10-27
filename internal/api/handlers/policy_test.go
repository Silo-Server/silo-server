package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/policy"
)

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

// The editor kill switch closes every editor operation, while the decision
// log, which needs no editor, stays readable.
func TestAdminPolicyReadyGatesEditor(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		handler := NewPolicyHandler(policy.NewSystem(nil, nil, nil), nil, nil, func() bool { return enabled })
		err := handler.AdminPolicyReady(true, false, false)
		var apiErr *APIError
		if refused := errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && apiErr.Code == "policy_editor_disabled"; refused == enabled {
			t.Fatalf("editor enabled=%v: err = %v", enabled, err)
		}
		if err := handler.AdminPolicyReady(false, false, false); err != nil {
			t.Fatalf("editor enabled=%v: a non-editor operation was gated: %v", enabled, err)
		}
	}
}

func validHandlerPolicySource() string {
	return `package silo_custom.scope

import rego.v1

override(base, _) := base`
}
