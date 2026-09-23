package handlers

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
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

func TestHistoryImportAPIErrorExplainsUnreachableServersAndInvalidInput(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	upstream := &url.Error{Op: "Post", URL: "http://emby.example.test/Users/AuthenticateByName", Err: dial}
	out := historyImportAPIError(fmt.Errorf("%w: %w", historyimport.ErrSourceUnreachable, upstream))
	if out.Status != http.StatusBadGateway || !IsHistoryImportUpstreamError(out) || !strings.HasPrefix(out.Message, "Couldn't reach the source server.") {
		t.Fatalf("unreachable mapping = %+v", out)
	}
	// Silo's own database failing to connect is not a source address problem.
	out = historyImportAPIError(fmt.Errorf("loading source: %w", dial))
	if out.Status != http.StatusInternalServerError || IsHistoryImportUpstreamError(out) {
		t.Fatalf("database dial failure mapping = %+v, want an internal error", out)
	}
	out = historyImportAPIError(fmt.Errorf("%w: choose a server and enter the Emby username", historyimport.ErrInvalidInput))
	if out.Status != http.StatusBadRequest || out.Message != "Choose a server and enter the Emby username." {
		t.Fatalf("invalid input mapping = %+v", out)
	}
}

func TestHistoryImportInputMessageKeepsFieldNames(t *testing.T) {
	if got := historyImportInputMessage(fmt.Errorf("%w: base_url must be an http or https URL", historyimport.ErrInvalidInput)); got != "base_url must be an http or https URL." {
		t.Fatalf("message = %q", got)
	}
}
