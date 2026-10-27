package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/autoscan/arrwebhook"
)

// maxWebhookBodyBytes caps public webhook request bodies. Real Sonarr/Radarr
// payloads are a few KiB; season packs with many episode files stay well under
// this.
const maxWebhookBodyBytes = 256 * 1024

// --- Admin webhook endpoint management ---

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
	// disabled autoscan all acknowledge without enqueueing.
	//
	// last_received_at backs the admin "Last delivery" line, so it counts only
	// deliveries the source took: a provider Test event in any state (setup
	// tells the operator to press Test, and that wiring check must work before
	// the source is enabled), and any delivery while the source and Autoscan
	// are both on. A real delivery dropped because either is off is not
	// stamped; otherwise the row reports a delivery that was thrown away.
	// A delivery with paths is stamped by IngestChanges when it is durably
	// accepted, so one answered with an error is not stamped.
	settings, err := h.repo.GetSettings(r.Context())
	if err != nil {
		return autoscanDeliveryFailure(err)
	}
	accepting := settings.Enabled && source.Enabled
	if !parsed.Test && !accepting {
		return nil
	}
	if parsed.Test || len(parsed.Changes) == 0 {
		if terr := h.repo.TouchWebhookReceived(r.Context(), source.ID); terr != nil {
			slog.WarnContext(r.Context(), "autoscan: touch webhook received failed", "component", "api", "source_id", source.ID, "err", terr)
		}
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
