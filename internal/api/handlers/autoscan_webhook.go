package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/autoscan/arrwebhook"
)

// maxWebhookBodyBytes caps public webhook request bodies. Real Sonarr/Radarr
// payloads are a few KiB; season packs with many episode files stay well under
// this.
const maxWebhookBodyBytes = 256 * 1024

// --- Admin webhook endpoint management ---

// HandleCreateSourceWebhook creates the source's webhook endpoint if missing
// and returns the source view including the delivery URL.
// POST /admin/autoscan/sources/{id}/webhook
func (h *AutoscanHandler) HandleCreateSourceWebhook(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	source, err := h.repo.GetSource(r.Context(), id)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	if source.DeliveryMode != autoscan.DeliveryModeWebhook {
		writeError(w, http.StatusBadRequest, "bad_request", "source is not in webhook delivery mode")
		return
	}
	if _, _, err := h.repo.CreateWebhookEndpoint(r.Context(), id); err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.sourceResponseWithWebhook(r.Context(), source))
}

// HandleRotateSourceWebhook replaces the endpoint's token; the old URL stops
// working immediately.
// POST /admin/autoscan/sources/{id}/webhook/rotate
func (h *AutoscanHandler) HandleRotateSourceWebhook(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	source, err := h.repo.GetSource(r.Context(), id)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	if _, _, err := h.repo.RotateWebhookEndpoint(r.Context(), id); err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.sourceResponseWithWebhook(r.Context(), source))
}

// HandleDeleteSourceWebhook removes the endpoint; future deliveries to its URL
// return 404.
// DELETE /admin/autoscan/sources/{id}/webhook
func (h *AutoscanHandler) HandleDeleteSourceWebhook(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := h.repo.DeleteWebhookEndpoint(r.Context(), id); err != nil {
		writeAutoscanError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Public delivery endpoint ---

// webhookAccepted is the 202 body for every accepted delivery (including
// no-ops), so senders can't distinguish disabled sources from active ones.
type webhookAccepted struct {
	Status string `json:"status"`
}

// HandleWebhookDelivery accepts a Sonarr/Radarr webhook POST. The bearer token
// in the path authenticates the delivery. Responses are deliberately coarse:
// 404 for any unknown token (no existence hints), 202 for anything accepted —
// including test events, unsupported event types, and disabled sources — so
// arr never marks a configured webhook unhealthy for a Silo-side state it
// cannot act on. Actionable deliveries are durably queued before the 202, so
// transient ingest failures are retried inside Silo rather than delegated to
// arr (which does not replay failed notification events).
//
// The token, request URL, and body are never logged.
func (h *AutoscanHandler) HandleWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now()
	token := chi.URLParam(r, "token")

	source, _, err := h.repo.ResolveWebhookToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, autoscan.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Not found")
			return
		}
		writeAutoscanError(w, err)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Request body exceeds the webhook payload cap")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "Could not read request body")
		return
	}

	parsed, err := arrwebhook.Parse(source.SourceConfig["webhook_provider"], body)
	if err != nil {
		// Parse errors carry no payload content; recording and returning them
		// is safe. The token stays out of every message.
		_ = h.repo.RecordWebhookError(r.Context(), source.ID, err.Error())
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	// Test events, unsupported event types, disabled sources, and globally
	// disabled autoscan all acknowledge without enqueueing. Stamping
	// last_received_at on every valid delivery keeps the admin "webhook is
	// wired up" signal honest even while disabled.
	settings, err := h.repo.GetSettings(r.Context())
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	if terr := h.repo.TouchWebhookReceived(r.Context(), source.ID); terr != nil {
		slog.WarnContext(r.Context(), "autoscan: touch webhook received failed", "component", "api", "source_id", source.ID, "err", terr)
	}
	if parsed.Test || len(parsed.Changes) == 0 || !settings.Enabled || !source.Enabled {
		writeJSON(w, http.StatusAccepted, webhookAccepted{Status: "accepted"})
		return
	}

	result, err := h.svc.IngestChanges(r.Context(), autoscan.ChangeIngest{
		SourceID:          source.ID,
		ProviderEventType: parsed.EventType,
		Changes:           parsed.Changes,
		ReceivedAt:        receivedAt,
	})
	if err != nil {
		// The only error from IngestChanges is failure to durably accept the
		// delivery. Processing errors are queued internally and return Pending.
		slog.WarnContext(r.Context(), "autoscan: webhook delivery acceptance failed", "component", "api",
			"source_id", source.ID,
			"provider", parsed.Provider,
			"event_type", parsed.EventType,
			"paths", len(parsed.Changes),
			"err", err,
		)
		_ = h.repo.RecordWebhookError(r.Context(), source.ID, err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not durably accept delivery")
		return
	}
	if result.Pending {
		slog.WarnContext(r.Context(), "autoscan: webhook delivery queued for retry", "component", "api",
			"source_id", source.ID,
			"provider", parsed.Provider,
			"event_type", parsed.EventType,
			"paths", len(parsed.Changes),
		)
	}
	slog.DebugContext(r.Context(), "autoscan: webhook delivery ingested", "component", "api",
		"source_id", source.ID,
		"provider", parsed.Provider,
		"event_type", parsed.EventType,
		"paths", len(parsed.Changes),
		"enqueued", result.Enqueued,
		"suppressed", result.Suppressed,
		"unresolved", result.Unresolved,
		"pending", result.Pending,
	)
	writeJSON(w, http.StatusAccepted, webhookAccepted{Status: "accepted"})
}
