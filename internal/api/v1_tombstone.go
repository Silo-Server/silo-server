package api

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/apiv2"
)

// v1TombstoneBody keeps the retired v1 error envelope, so a client that
// predates /api/v2 can still show why it stopped working.
const v1TombstoneBody = `{"error":"client_upgrade_required",` +
	`"message":"This server no longer serves /api/v1. Update the app to continue.",` +
	`"docs_url":"` + apiv2.DocsOrigin + `update-to-1.0"}`

// v1Tombstone answers every retired /api/v1 route with 410 Gone. It runs no
// authentication or business logic and never redirects: old URLs may carry
// tokens or secrets.
var v1Tombstone = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	apiv2.RecordV1Tombstone(r.Method)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(v1TombstoneBody))
	}
})
