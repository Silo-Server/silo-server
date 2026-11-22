package apiv2

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// Editing or deleting an AI provider the deployment document declares is a
// validation failure with code managed_by_environment, not a server error.
func TestAdminAIProviderRefusesDeclaredProvider(t *testing.T) {
	err := fmt.Errorf("%w: OpenAI is declared by the deployment document in SILO_CONFIG_FILE", handlers.ErrAIProviderManaged)
	p, ok := errors.AsType[*Problem](adminAIProviderProblem(err, "update"))
	if !ok || p.Status != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Code != codeManagedByEnvironment {
		t.Fatalf("problem %+v, want validation_failed with code managed_by_environment", p)
	}
}
