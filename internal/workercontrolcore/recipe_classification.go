package workercontrolcore

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

// RequiresRecipe distinguishes enrolled standalone workers from legacy
// executors using the same registered authority as command dispatch. Enrolled
// workers never reconstruct from the client's stream token.
func (s *Service) RequiresRecipe(ctx context.Context, nodeURL string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, nodeURL, nil)
	if err != nil {
		return false, ErrDenied
	}
	node, registered, err := s.nodeForRequest(request)
	if err != nil || !registered {
		return false, err
	}
	if _, err := workeridentity.ValidateOrigin(nodeURL, node.AllowHTTP); err != nil {
		return false, ErrDenied
	}
	return true, nil
}
