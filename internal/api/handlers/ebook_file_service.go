package handlers

import (
	"context"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ResolveReaderFile applies the catalog/file policy and reader-format whitelist
// shared by legacy and v2 binary delivery.
func (h *EbookReaderHandler) ResolveReaderFile(ctx context.Context, contentID string, fileID int, filter catalog.AccessFilter) (*models.MediaFile, error) {
	if h == nil || h.FileAuthorizer == nil {
		return nil, &APIError{Status: http.StatusServiceUnavailable, Message: "Ebook reader is not configured"}
	}
	file, err := h.FileAuthorizer.AuthorizeContext(ctx, fileID, filter)
	if err != nil {
		return nil, err
	}
	if file == nil || file.ContentID != contentID || !isEbookFile(file) {
		return nil, catalog.ErrItemNotFound
	}
	return file, nil
}

// ServeReaderFile preserves range/HEAD/conversion behavior for an authorized
// file. Call ResolveReaderFile before handing a file to this method.
func (h *EbookReaderHandler) ServeReaderFile(w http.ResponseWriter, r *http.Request, file *models.MediaFile) error {
	attachTransfer(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), file.ID)
	return h.serveEbook(w, r, file)
}
