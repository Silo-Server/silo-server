package apiv2

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// A change to a field the node declares is a validation failure naming each
// locked field with code managed_by_environment, however the store wrapped it.
func TestAdminNodeUpdateRefusesEnvManagedFields(t *testing.T) {
	err := fmt.Errorf("update: %w", &nodepool.EnvManagedFieldsError{Fields: []string{nodepool.FieldGroup, nodepool.FieldMaxJobs}})
	p, ok := errors.AsType[*Problem](adminNodeConfigurationProblem(err))
	if !ok || p.Status != http.StatusUnprocessableEntity || p.Type != TypeValidationFailed.URI() {
		t.Fatalf("problem %+v, want validation_failed", p)
	}
	want := []string{"body.group", "body.max_jobs"}
	if len(p.Errors) != len(want) {
		t.Fatalf("errors %+v, want %v", p.Errors, want)
	}
	for i, e := range p.Errors {
		if e.Location != want[i] || e.Code != codeManagedByEnvironment {
			t.Fatalf("error %d = %+v, want %s %s", i, e, want[i], codeManagedByEnvironment)
		}
	}
}
