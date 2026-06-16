package jellycompat

import (
	"io"
	"net/http"
)

// maxClientLogBytes bounds how much of a client log upload we read before
// discarding it, matching Jellyfin's ClientLogController.MaxDocumentSize
// (1,000,000 bytes). Anything larger is rejected with 413.
const maxClientLogBytes = 1_000_000

// clientLogDocumentResponse mirrors Jellyfin's ClientLogDocumentResponseDto.
// Some clients parse FileName from the 200 response, so it must be present.
type clientLogDocumentResponse struct {
	FileName string `json:"FileName"`
}

// HandleClientLogDocument accepts POST /ClientLog/Document. Clients (Jellyfin
// Android TV, Wholphin, Fire TV apps) upload crash/diagnostic bundles here; with
// no route they hit a chi 404 and "upload logs" silently fails. Silo has no
// client-log store, so the body is drained and discarded, but we answer 200 with
// a generated FileName to match Jellyfin's contract (it returns the stored file
// name, never 204). Oversized uploads get 413 like Jellyfin's MaxDocumentSize.
func HandleClientLogDocument(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > maxClientLogBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "PayloadTooLarge", "Client log document is too large")
		return
	}
	// Drain (bounded) and discard: Silo does not persist client logs, but the
	// body must be consumed so the client's upload completes cleanly.
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxClientLogBytes))

	writeJSON(w, http.StatusOK, clientLogDocumentResponse{FileName: uuidNewString() + ".log"})
}
