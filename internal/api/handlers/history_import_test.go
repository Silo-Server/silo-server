package handlers

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/netguard"
)

func TestHistoryImportDurableAdmissionErrorsDoNotExposeCauses(t *testing.T) {
	err := errors.Join(historyimport.ErrPersonalAdmissionUncertain, errors.New("private credential detail"))
	out := historyImportAPIError(err)
	if out.Status != http.StatusServiceUnavailable || out.Message != historyimport.ErrPersonalAdmissionUncertain.Error() || !errors.Is(out, historyimport.ErrPersonalAdmissionUncertain) {
		t.Fatalf("unsafe uncertain admission mapping: %+v", out)
	}
}

// v1 is frozen: failures the v2 adapter explains still answer 500 here, and
// the cause stays reachable for the v2 mapping.
func TestHistoryImportAPIErrorKeepsV1DecisionForV2Causes(t *testing.T) {
	dial := &url.Error{Op: "Post", URL: "http://emby.example.test/Users/AuthenticateByName", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}}
	for _, err := range []error{
		fmt.Errorf("%w: %w", historyimport.ErrSourceUnreachable, dial),
		fmt.Errorf("%w: choose a server and enter the Emby username", historyimport.ErrInvalidInput),
	} {
		out := historyImportAPIError(err)
		if out.Status != http.StatusInternalServerError || out.Code != policyErrorInternal || !errors.Is(out, err) {
			t.Fatalf("historyImportAPIError(%v) = %+v, want the unchanged v1 500 wrapping its cause", err, out)
		}
	}
}

// A server address the outbound guard refused is the caller's input to fix:
// both APIs answer 400 with the guard's message, never "unreachable".
func TestHistoryImportAPIErrorExplainsRefusedServerAddress(t *testing.T) {
	dial := func(cause error) error {
		return &url.Error{Op: "Post", URL: "http://192.168.1.10:8096/Users/AuthenticateByName", Err: &net.OpError{Op: "dial", Net: "tcp", Err: cause}}
	}
	for cause, message := range map[error]string{
		netguard.ErrPrivateDestination: historyimport.PrivateAddressMessage,
		netguard.ErrBlockedDestination: historyimport.BlockedAddressMessage,
	} {
		for _, err := range []error{cause, dial(cause), fmt.Errorf("authenticating against Jellyfin server: %w", dial(cause))} {
			out := historyImportAPIError(err)
			if out.Status != http.StatusBadRequest || out.Code != policyErrorBadRequest || out.Message != message || !errors.Is(out, cause) {
				t.Fatalf("historyImportAPIError(%v) = %+v", err, out)
			}
		}
	}
}
