package handlers

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

func TestHistoryImportUpstreamError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "unauthorized",
			status:     http.StatusUnauthorized,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
			wantMsg:    "Couldn't connect to that server. Check the URL, username, and password and try again.",
		},
		{
			name:       "bad request",
			status:     http.StatusBadRequest,
			wantStatus: http.StatusBadRequest,
			wantCode:   "bad_request",
			wantMsg:    "Couldn't start the import with those server settings.",
		},
		{
			name:       "upstream failure",
			status:     http.StatusBadGateway,
			wantStatus: http.StatusBadGateway,
			wantCode:   "bad_gateway",
			wantMsg:    "The source server couldn't complete the import right now. Please try again.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStatus, gotCode, gotMsg := historyImportUpstreamError(tt.status)
			if gotStatus != tt.wantStatus || gotCode != tt.wantCode || gotMsg != tt.wantMsg {
				t.Fatalf("got (%d, %q, %q), want (%d, %q, %q)", gotStatus, gotCode, gotMsg, tt.wantStatus, tt.wantCode, tt.wantMsg)
			}
		})
	}
}

func TestHistoryImportDurableAdmissionErrorsDoNotExposeCauses(t *testing.T) {
	err := errors.Join(historyimport.ErrPersonalAdmissionUncertain, errors.New("private credential detail"))
	out := historyImportAPIError(err)
	if out.Status != http.StatusServiceUnavailable || out.Message != historyimport.ErrPersonalAdmissionUncertain.Error() || !errors.Is(out, historyimport.ErrPersonalAdmissionUncertain) {
		t.Fatalf("unsafe uncertain admission mapping: %+v", out)
	}
}

// A malformed address or missing field is the user's to fix, and a source
// server the HTTP client cannot reach is an upstream failure. A bare sentinel
// or a Silo database connection failure is still an internal error.
func TestHistoryImportAPIErrorMapsInputAndReachability(t *testing.T) {
	t.Parallel()

	invalid := historyImportAPIError(fmt.Errorf("preparing run: %w", fmt.Errorf("%w: enter the Jellyfin address with http:// or https:// at the start", historyimport.ErrInvalidInput)))
	if invalid.Status != http.StatusBadRequest || invalid.Message != "Enter the Jellyfin address with http:// or https:// at the start." {
		t.Fatalf("invalid input = %+v", invalid)
	}
	if bare := historyImportAPIError(historyimport.ErrInvalidInput); bare.Status != http.StatusInternalServerError {
		t.Fatalf("bare invalid input = %+v, want an internal error", bare)
	}

	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	source := &url.Error{Op: "Post", URL: "http://jellyfin.invalid/Users/AuthenticateByName", Err: dial}
	unreachable := historyImportAPIError(fmt.Errorf("authenticating against Jellyfin server: %w", source))
	if unreachable.Status != http.StatusBadGateway || !IsHistoryImportUpstreamError(unreachable) {
		t.Fatalf("unreachable source = %+v", unreachable)
	}
	// pgx reports a refused database connection as a *net.OpError without the
	// *url.Error an HTTP client adds.
	if database := historyImportAPIError(fmt.Errorf("checking profile: %w", dial)); database.Status != http.StatusInternalServerError || IsHistoryImportUpstreamError(database) {
		t.Fatalf("database failure = %+v, want an internal error", database)
	}
}
