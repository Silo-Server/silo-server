package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/policy"
)

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
