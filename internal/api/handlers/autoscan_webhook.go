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

const (
	autoscanDeliveryAccepted      = "accepted"
	autoscanDeliveryNotFound      = "not_found"
	autoscanDeliveryInternalError = "internal_error"
	autoscanDeliveryBadRequest    = "bad_request"
)

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
	if err := h.DeliverAutoscanWebhook(w, r, chi.URLParam(r, "token")); err != nil {
		failure := autoscanDeliveryFailure(err)
		writeError(w, failure.Status, failure.Code, failure.Message)
		return
	}
	writeJSON(w, http.StatusAccepted, webhookAccepted{Status: autoscanDeliveryAccepted})
}

// AutoscanDeliveryFailure carries safe delivery failure details to each transport.
type AutoscanDeliveryFailure struct {
	Status  int
	Code    string
	Message string
}

func (e *AutoscanDeliveryFailure) Error() string { return e.Message }
func autoscanDeliveryFailure(err error) *AutoscanDeliveryFailure {
	if failure, ok := errors.AsType[*AutoscanDeliveryFailure](err); ok {
		return failure
	}
	if errors.Is(err, autoscan.ErrNotFound) {
		return &AutoscanDeliveryFailure{Status: http.StatusNotFound, Code: autoscanDeliveryNotFound, Message: "Autoscan resource not found"}
	}
	return &AutoscanDeliveryFailure{Status: http.StatusInternalServerError, Code: autoscanDeliveryInternalError, Message: "Autoscan operation failed"}
}

// DeliverAutoscanWebhook validates the capability before reading bounded provider
// bytes and delegates durable acceptance to the existing ingest service. It does
// not encode a response or configure schedules/callback URLs.
func (h *AutoscanHandler) DeliverAutoscanWebhook(w http.ResponseWriter, r *http.Request, token string) error {
	receivedAt := time.Now()

	source, _, err := h.repo.ResolveWebhookToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, autoscan.ErrNotFound) {
			return &AutoscanDeliveryFailure{Status: http.StatusNotFound, Code: autoscanDeliveryNotFound, Message: "Not found"}
		}
		return autoscanDeliveryFailure(err)
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return &AutoscanDeliveryFailure{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Message: "Request body exceeds the webhook payload cap"}
		}
		return &AutoscanDeliveryFailure{Status: http.StatusBadRequest, Code: autoscanDeliveryBadRequest, Message: "Could not read request body"}
	}

	parsed, err := arrwebhook.Parse(source.SourceConfig["webhook_provider"], body)
	if err != nil {
		// Parse errors carry no payload content; recording and returning them
		// is safe. The token stays out of every message.
		_ = h.repo.RecordWebhookError(r.Context(), source.ID, err.Error())
		return &AutoscanDeliveryFailure{Status: http.StatusBadRequest, Code: autoscanDeliveryBadRequest, Message: err.Error()}
	}

	// Test events, unsupported event types, disabled sources, and globally
	// disabled autoscan all acknowledge without enqueueing. Stamping
	// last_received_at on every valid delivery keeps the admin "webhook is
	// wired up" signal honest even while disabled.
	settings, err := h.repo.GetSettings(r.Context())
	if err != nil {
		return autoscanDeliveryFailure(err)
	}
	if terr := h.repo.TouchWebhookReceived(r.Context(), source.ID); terr != nil {
		slog.WarnContext(r.Context(), "autoscan: touch webhook received failed", "component", "api", "source_id", source.ID, "err", terr)
	}
	if parsed.Test || len(parsed.Changes) == 0 || !settings.Enabled || !source.Enabled {
		return nil
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
		return &AutoscanDeliveryFailure{Status: http.StatusInternalServerError, Code: autoscanDeliveryInternalError, Message: "Could not durably accept delivery"}
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
	return nil
}
