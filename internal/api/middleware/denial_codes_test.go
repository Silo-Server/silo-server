package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// The v2 listener (internal/apiv2, denialWriter.problem) translates a v1
// denial into a Problem Details document by switching on the body's `error`
// code and on the reason the gate records out of band. Both are contract
// values for that translation: a reworded human message must stay free to
// change, and these must not. This test is where a rename fails. It also pins
// that the reason never reaches the v1 wire body, whose shape the ratified
// scenario catalogs assert is exactly {"error","message"}.

type denialBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// reasonWriter is what internal/apiv2's denialWriter does: collect the reason
// without changing the response.
type reasonWriter struct {
	*httptest.ResponseRecorder
	reason string
}

func (w *reasonWriter) RecordDenialReason(reason string) { w.reason = reason }

func newReasonWriter() *reasonWriter { return &reasonWriter{ResponseRecorder: httptest.NewRecorder()} }

func decodeDenial(t *testing.T, rec *reasonWriter) denialBody {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("denial body is not JSON: %v: %s", err, rec.Body.String())
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"error", "message"}) {
		t.Fatalf("v1 denial body keys = %v, want exactly [error message]: %s", keys, rec.Body.String())
	}
	var got denialBody
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Error == "" {
		t.Fatalf("denial body has no error code: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	return got
}

func TestDenialCodesAreStable(t *testing.T) {
	cases := []struct {
		name       string
		write      func(w http.ResponseWriter)
		status     int
		wantCode   string
		wantReason string
	}{
		{"missing credential", func(w http.ResponseWriter) {
			writeUnauthorized(w, "Missing or malformed authorization header", ReasonAuthenticationRequired)
		}, http.StatusUnauthorized, "unauthorized", ReasonAuthenticationRequired},
		{"invalid credential", func(w http.ResponseWriter) {
			writeUnauthorized(w, "Invalid or expired token", ReasonInvalidCredential)
		}, http.StatusUnauthorized, "unauthorized", ReasonInvalidCredential},
		{"disabled account", func(w http.ResponseWriter) {
			writeUnauthorized(w, "User account is disabled", ReasonAccountDisabled)
		}, http.StatusUnauthorized, "unauthorized", ReasonAccountDisabled},
		{"invalid session", func(w http.ResponseWriter) {
			writeUnauthorized(w, "Session is no longer valid", ReasonSessionInvalid)
		}, http.StatusUnauthorized, "unauthorized", ReasonSessionInvalid},
		{"forbidden", func(w http.ResponseWriter) {
			writeForbidden(w, "Admin access required")
		}, http.StatusForbidden, "forbidden", ""},
		{"internal", func(w http.ResponseWriter) {
			writeInternalError(w, activeProfileVerificationFailedMsg)
		}, http.StatusInternalServerError, "internal_error", ""},
		{"item id required", func(w http.ResponseWriter) {
			writePermissionErrorReason(w, http.StatusBadRequest, "bad_request", itemIDRequiredMsg, ReasonItemIDRequired)
		}, http.StatusBadRequest, "bad_request", ReasonItemIDRequired},
		{"item not found", func(w http.ResponseWriter) {
			writePermissionError(w, http.StatusNotFound, "not_found", "Item not found")
		}, http.StatusNotFound, "not_found", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newReasonWriter()
			tc.write(rec)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			got := decodeDenial(t, rec)
			if got.Error != tc.wantCode || rec.reason != tc.wantReason {
				t.Fatalf("error/reason = %q/%q, want %q/%q", got.Error, rec.reason, tc.wantCode, tc.wantReason)
			}
			if got.Message == "" {
				t.Fatal("denial body lost its human message")
			}
		})
	}
}

// TestGateDenialCodesThroughMiddleware drives the gates apiv2 composes and
// pins the code each writes, so a change in a gate's own branching is caught
// as well as a change in the writers.
func TestGateDenialCodesThroughMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	t.Run("profile header required", func(t *testing.T) {
		rec := newReasonWriter()
		RequireProfile(ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", rec.Code)
		}
		got := decodeDenial(t, rec)
		if got.Error != "bad_request" || rec.reason != ReasonProfileHeaderRequired {
			t.Fatalf("error/reason = %q/%q", got.Error, rec.reason)
		}
	})

	viewer := func(err error) http.Handler {
		return NewViewerAccessMiddleware(stubResolver{err: err}).RequireViewerAccess(ok)
	}
	for name, tc := range map[string]struct {
		err        error
		status     int
		wantCode   string
		wantReason string
	}{
		"unverified profile": {access.ErrProfileUnverified, http.StatusForbidden, "profile_unverified", ""},
		"unknown profile":    {access.ErrProfileNotFound, http.StatusNotFound, "not_found", ""},
		"resolver failure":   {errors.New("boom"), http.StatusInternalServerError, "internal_error", ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.Header.Set("X-Profile-Id", "p1")
			r = r.WithContext(SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user", SessionID: "s"}))
			rec := newReasonWriter()
			viewer(tc.err).ServeHTTP(rec, r)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			got := decodeDenial(t, rec)
			if got.Error != tc.wantCode || rec.reason != tc.wantReason {
				t.Fatalf("error/reason = %q/%q, want %q/%q", got.Error, rec.reason, tc.wantCode, tc.wantReason)
			}
		})
	}

	t.Run("viewer access without claims", func(t *testing.T) {
		rec := newReasonWriter()
		viewer(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		got := decodeDenial(t, rec)
		if rec.Code != http.StatusUnauthorized || got.Error != "unauthorized" || rec.reason != ReasonAuthenticationRequired {
			t.Fatalf("%d %q/%q", rec.Code, got.Error, rec.reason)
		}
	})

	t.Run("acting admin denial", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r = r.WithContext(SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user", SessionID: "s"}))
		rec := newReasonWriter()
		RequireActingAdmin(nil)(ok).ServeHTTP(rec, r)
		got := decodeDenial(t, rec)
		if rec.Code != http.StatusForbidden || got.Error != "forbidden" {
			t.Fatalf("%d %q", rec.Code, got.Error)
		}
	})

	t.Run("demo restricted", func(t *testing.T) {
		rec := newReasonWriter()
		writeDemoBlocked(rec)
		got := decodeDenial(t, rec)
		if rec.Code != http.StatusForbidden || got.Error != "demo_restricted" {
			t.Fatalf("%d %q", rec.Code, got.Error)
		}
	})
}

type stubResolver struct{ err error }

func (s stubResolver) Resolve(_ context.Context, in access.ResolveInput) (access.Scope, error) {
	if s.err != nil {
		return access.Scope{}, s.err
	}
	return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID, ProfileVerified: true}, nil
}
