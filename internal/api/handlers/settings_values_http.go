package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
)

// The v1 HTTP surface of the settings values API. Each handler parses the
// request, runs the shared core in settings_values_seam.go, and renders the
// core's decision with the same status, code and message v1 always wrote.

type effectiveContextRequest struct {
	ContextID string          `json:"context_id"`
	LibraryID json.RawMessage `json:"library_id,omitempty"`
	SeriesID  string          `json:"series_id,omitempty"`
}

type effectiveContextResponse struct {
	ContextID string                          `json:"context_id"`
	Settings  []effectiveSettingValueResponse `json:"settings"`
}

// constraintsFor gathers the policy inputs that narrow this viewer's settings.
//
// The routes are mounted inside RequireViewerAccess, so the resolved access
// scope is already on the context. Scope.MaxPlaybackQuality holds a literal
// member of the contract's quality enum ("1080p", "2160p"), so it feeds the
// ceiling on playback.preferred_quality directly — no translation table. An
// empty value means the policy does not cap this viewer, expressed by omitting
// the key so the resolver leaves the preference alone.
//
// catalog.metadata_language deliberately gets no constraint: the manifest notes
// record that the allowlist draft was circular, because the policy input it
// would bind to is populated from the very preference it would narrow.
func (h *SettingValuesHandler) constraintsFor(ctx context.Context) settingsresolve.Constraints {
	scope, ok := access.GetScope(ctx)
	if !ok {
		return nil
	}
	quality := strings.TrimSpace(scope.MaxPlaybackQuality)
	if quality == "" {
		return nil
	}
	limit, err := json.Marshal(quality)
	if err != nil {
		return nil
	}
	return settingsresolve.Constraints{policyInputMaxPlaybackQuality: limit}
}

func (h *SettingValuesHandler) effectiveResponses(
	ctx context.Context,
	resolved []settingsresolve.Effective,
) []effectiveSettingValueResponse {
	observed := h.observedLanguageSuggestions(ctx, resolved)
	return h.effectiveResponsesWithObserved(resolved, observed)
}

func (h *SettingValuesHandler) observedLanguageSuggestions(
	ctx context.Context,
	resolved []settingsresolve.Effective,
) map[string][]string {
	result := make(map[string][]string)
	if h.languageSource == nil {
		return result
	}
	found := false
	for _, eff := range resolved {
		if eff.Key == settingskeys.CatalogMetadataLanguage {
			found = true
			break
		}
	}
	if !found {
		return result
	}

	filters := catalog.BrowseFilters{}
	if scope, ok := access.GetScope(ctx); ok {
		filters.LibraryIDs = scope.AllowedLibraryIDs
		filters.DisabledLibraryIDs = scope.DisabledLibraryIDs
		filters.MaturityLimits = scope.MaturityLimits
	}
	values, err := h.languageSource.ListOriginalLanguages(ctx, filters)
	if err != nil {
		slog.WarnContext(ctx, "settings: listing metadata language suggestions",
			"component", "settings", "error", err)
		return result
	}
	result[settingskeys.CatalogMetadataLanguage] = values
	return result
}
