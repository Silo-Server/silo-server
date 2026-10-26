package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
