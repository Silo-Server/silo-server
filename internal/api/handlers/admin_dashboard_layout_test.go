package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestParseDashboardLayoutPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		want    string
		wantErr error
	}{
		{
			name: "layout object is stored verbatim",
			body: `{"layout":{"version":1,"entries":[{"id":"libraries","span":7}]}}`,
			want: `{"version":1,"entries":[{"id":"libraries","span":7}]}`,
		},
		{
			name: "empty object is a valid layout",
			body: `{"layout":{}}`,
			want: `{}`,
		},
		{
			name: "unknown widget ids are not the server's business",
			body: `{"layout":{"entries":[{"id":"not-a-widget","span":99}]}}`,
			want: `{"entries":[{"id":"not-a-widget","span":99}]}`,
		},
		{
			name:    "missing layout",
			body:    `{}`,
			wantErr: errDashboardLayoutMissing,
		},
		{
			name:    "explicit null layout",
			body:    `{"layout":null}`,
			wantErr: errDashboardLayoutMissing,
		},
		{
			name:    "array layout",
			body:    `{"layout":[{"id":"libraries","span":7}]}`,
			wantErr: errDashboardLayoutNotObject,
		},
		{
			name:    "string layout",
			body:    `{"layout":"libraries"}`,
			wantErr: errDashboardLayoutNotObject,
		},
		{
			name:    "number layout",
			body:    `{"layout":7}`,
			wantErr: errDashboardLayoutNotObject,
		},
		{
			name:    "malformed json",
			body:    `{"layout":`,
			wantErr: errDashboardLayoutInvalidJSON,
		},
		{
			name:    "empty body",
			body:    ``,
			wantErr: errDashboardLayoutInvalidJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDashboardLayoutPayload([]byte(tt.body))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if got != nil {
					t.Fatalf("layout = %s, want nil on error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("layout = %s, want %s", got, tt.want)
			}
		})
	}
}

// newDashboardLayoutRequest builds an authenticated admin request. The routes
// are admin-gated by the surrounding route group, so the handler only needs a
// user id in the claims.
func newDashboardLayoutRequest(method, body string, userID int) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/admin/dashboard/layout", nil)
	} else {
		req = httptest.NewRequest(method, "/admin/dashboard/layout", strings.NewReader(body))
	}
	if userID == 0 {
		return req
	}
	return req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: userID}))
}

func TestDashboardLayoutHandlersRequireDatabase(t *testing.T) {
	t.Parallel()

	handler := &AdminHandler{}
	for _, tc := range []struct {
		name   string
		method string
		body   string
		serve  func(http.ResponseWriter, *http.Request)
	}{
		{"get", http.MethodGet, "", handler.HandleGetDashboardLayout},
		{"put", http.MethodPut, `{"layout":{}}`, handler.HandlePutDashboardLayout},
		{"delete", http.MethodDelete, "", handler.HandleDeleteDashboardLayout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			tc.serve(rec, newDashboardLayoutRequest(tc.method, tc.body, 7))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// newUnreachableLayoutHandler returns a handler whose pool never connects, so
// every request that reaches SQL fails. Tests using it assert on the checks
// that run *before* the database is touched.
func newUnreachableLayoutHandler(t *testing.T) *AdminHandler {
	t.Helper()

	pool, err := pgxpool.New(context.Background(), "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatalf("create unreachable pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return &AdminHandler{pool: pool}
}

func TestDashboardLayoutHandlersRequireAuthenticatedUser(t *testing.T) {
	t.Parallel()

	handler := newUnreachableLayoutHandler(t)
	for _, tc := range []struct {
		name   string
		method string
		body   string
		serve  func(http.ResponseWriter, *http.Request)
	}{
		{"get", http.MethodGet, "", handler.HandleGetDashboardLayout},
		{"put", http.MethodPut, `{"layout":{}}`, handler.HandlePutDashboardLayout},
		{"delete", http.MethodDelete, "", handler.HandleDeleteDashboardLayout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			tc.serve(rec, newDashboardLayoutRequest(tc.method, tc.body, 0))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPutDashboardLayoutRejectsInvalidBodies(t *testing.T) {
	t.Parallel()

	handler := newUnreachableLayoutHandler(t)
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"not an object", `{"layout":[]}`, errDashboardLayoutNotObject.Error()},
		{"missing layout", `{}`, errDashboardLayoutMissing.Error()},
		{"malformed", `nope`, errDashboardLayoutInvalidJSON.Error()},
		{
			name:        "over the size limit",
			body:        `{"layout":{"pad":"` + strings.Repeat("x", maxDashboardLayoutBytes) + `"}}`,
			wantMessage: "Dashboard layout is too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			handler.HandlePutDashboardLayout(rec, newDashboardLayoutRequest(http.MethodPut, tt.body, 7))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			var resp errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if resp.Message != tt.wantMessage {
				t.Fatalf("message = %q, want %q", resp.Message, tt.wantMessage)
			}
		})
	}
}

// TestDashboardCapabilitiesAdvertisesEverySurface pins the feature-detection
// contract for the dashboard: a client reads this to tell an older deployment
// from a failing request, so a surface may only be dropped from the response
// deliberately.
func TestDashboardCapabilitiesAdvertisesEverySurface(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	(&AdminHandler{}).HandleGetDashboardCapabilities(rr, httptest.NewRequest(http.MethodGet, "/admin/dashboard/capabilities", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
	var resp adminDashboardCapabilitiesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if !resp.ServerLayouts || !resp.Timeseries || !resp.PlaybackActivity ||
		!resp.TopActivity || !resp.Health || !resp.LogLevelList || !resp.DownloadsStats {
		t.Fatalf("capabilities must advertise every dashboard surface: %+v", resp)
	}
}
