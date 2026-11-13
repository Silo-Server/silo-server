package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalogseed"
)

func TestCatalogImportSourceProblemNamesDownloadLimit(t *testing.T) {
	apiErr, ok := errors.AsType[*APIError](catalogImportSourceProblem(catalogseed.ErrRemoteBundleTooLarge))
	if !ok || apiErr.Status != http.StatusBadRequest || !strings.Contains(apiErr.Message, "GiB download limit") {
		t.Fatalf("oversize source problem = %+v, want a 400 naming the download limit", apiErr)
	}
}
