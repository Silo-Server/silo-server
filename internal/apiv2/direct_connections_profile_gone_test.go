package apiv2

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/directconn"
)

// A profile deleted during its own enrollment answers like any other request
// for a deleted profile (RequireViewerAccess answers 404 not_found), not an
// internal error.
func TestDirectCoordinationProblemMapsAnEnrollmentForADeletedProfileToNotFound(t *testing.T) {
	problem := directCoordinationProblem(fmt.Errorf("enroll: %w", directconn.ErrProfileGone))

	if problem.Status != http.StatusNotFound {
		t.Fatalf("status %d (%s), want %d", problem.Status, problem.Type, http.StatusNotFound)
	}
}
