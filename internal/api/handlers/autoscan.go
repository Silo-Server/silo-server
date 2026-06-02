package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// autoscanStore is the subset of *autoscan.Repository the handler needs.
type autoscanStore interface {
	GetSettings(ctx context.Context) (autoscan.Settings, error)
	UpdateSettings(ctx context.Context, s autoscan.Settings) (autoscan.Settings, error)
	ListAllSources(ctx context.Context) ([]autoscan.Source, error)
	UpsertSource(ctx context.Context, integrationID string, u autoscan.SourceUpdate) (*autoscan.Source, error)
}

// autoscanTriggerer is the subset of *autoscan.Service the handler needs.
type autoscanTriggerer interface {
	PollOnce(ctx context.Context) error
	SuggestRewrites(ctx context.Context, integrationID string) (autoscan.RewriteSuggestions, error)
}

// triggerUpdater reconfigures a task's schedule (satisfied by *taskmanager.TaskManager).
type triggerUpdater interface {
	UpdateTriggers(key string, triggerConfigs []taskmanager.TriggerConfig) error
}

// AutoscanHandler serves the admin-only autoscan settings/sources/trigger API.
type AutoscanHandler struct {
	repo     autoscanStore
	svc      autoscanTriggerer
	triggers triggerUpdater
}

func NewAutoscanHandler(repo autoscanStore, svc autoscanTriggerer, triggers triggerUpdater) *AutoscanHandler {
	return &AutoscanHandler{repo: repo, svc: svc, triggers: triggers}
}

func (h *AutoscanHandler) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.repo.GetSettings(r.Context())
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *AutoscanHandler) HandleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var s autoscan.Settings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if s.PollIntervalMinutes <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "poll_interval_minutes must be greater than 0")
		return
	}
	if s.DebounceSeconds < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "debounce_seconds must be 0 or greater")
		return
	}
	updated, err := h.repo.UpdateSettings(r.Context(), s)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	if h.triggers != nil {
		intervalMs := int64(updated.PollIntervalMinutes) * 60 * 1000
		if terr := h.triggers.UpdateTriggers("autoscan_poll", []taskmanager.TriggerConfig{
			{Type: taskmanager.TriggerTypeInterval, IntervalMs: intervalMs},
		}); terr != nil {
			slog.WarnContext(r.Context(), "autoscan: update trigger failed", "err", terr)
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *AutoscanHandler) HandleListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := h.repo.ListAllSources(r.Context())
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	if sources == nil {
		sources = []autoscan.Source{}
	}
	writeJSON(w, http.StatusOK, struct {
		Sources []autoscan.Source `json:"sources"`
	}{Sources: sources})
}

func (h *AutoscanHandler) HandleUpsertSource(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	var update autoscan.SourceUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	source, err := h.repo.UpsertSource(r.Context(), id, update)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

func (h *AutoscanHandler) HandleTrigger(w http.ResponseWriter, r *http.Request) {
	// Run the poll detached: don't block the request on a full cycle, and don't
	// tie the poll to the request context (which is cancelled once we respond).
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := h.svc.PollOnce(ctx); err != nil {
			slog.WarnContext(ctx, "autoscan: manual poll failed", "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, struct {
		Status string `json:"status"`
	}{Status: "ok"})
}

func (h *AutoscanHandler) HandleRewriteSuggestions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	suggestions, err := h.svc.SuggestRewrites(r.Context(), id)
	if err != nil {
		if errors.Is(err, autoscan.ErrIntegrationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Autoscan source not found")
			return
		}
		writeError(w, http.StatusBadGateway, "arr_unreachable", "Could not load root folders from the arr instance")
		return
	}
	writeJSON(w, http.StatusOK, suggestions)
}

type autoscanStatusSource struct {
	IntegrationID string     `json:"integration_id"`
	Name          string     `json:"name"`
	LastPollAt    *time.Time `json:"last_poll_at,omitempty"`
}

type autoscanStatusResponse struct {
	Enabled bool                   `json:"enabled"`
	Sources []autoscanStatusSource `json:"sources"`
}

func (h *AutoscanHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := h.repo.GetSettings(ctx)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	sources, err := h.repo.ListAllSources(ctx)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	trimmed := make([]autoscanStatusSource, 0, len(sources))
	for _, src := range sources {
		trimmed = append(trimmed, autoscanStatusSource{
			IntegrationID: src.IntegrationID,
			Name:          src.Name,
			LastPollAt:    src.LastPollAt,
		})
	}
	writeJSON(w, http.StatusOK, autoscanStatusResponse{
		Enabled: s.Enabled,
		Sources: trimmed,
	})
}

// writeAutoscanError maps autoscan repository/service errors to HTTP status
// codes: a missing integration on upsert maps to 404, everything else to 500.
func writeAutoscanError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, autoscan.ErrIntegrationNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Autoscan source not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Autoscan operation failed")
}
