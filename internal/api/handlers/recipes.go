package handlers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// RecipeHandler exposes recipe-registry metadata over HTTP.
type RecipeHandler struct{}

// HandleList returns all recipe definitions grouped by category.
// GET /api/sections/recipes
func (h *RecipeHandler) HandleList(w http.ResponseWriter, _ *http.Request) {
	groups := map[string][]recipes.RecipeDefinition{}
	for _, cat := range h.Recipes() {
		groups[cat.Category] = cat.Recipes
	}
	resp := map[string]any{"categories": groups}
	writeJSON(w, http.StatusOK, resp)
}

// CandidateSource provides UI-pickable candidates for a parameterized recipe.
// Recipe families register their own sources via RegisterCandidateSource.
type CandidateSource func(ctx context.Context) ([]Candidate, error)

// Candidate is a generic shape: the UI shows DisplayName + optional Subtitle and uses Value as the param.
type Candidate struct {
	Value       string `json:"value"`
	DisplayName string `json:"display_name"`
	Subtitle    string `json:"subtitle,omitempty"`
}

var candidateSources = map[string]CandidateSource{}

// RegisterCandidateSource is called by recipe families that need parameter helpers.
func RegisterCandidateSource(recipeType string, src CandidateSource) {
	candidateSources[recipeType] = src
}

// HandleCandidates is GET /api/sections/recipes/{type}/candidates.
func (h *RecipeHandler) HandleCandidates(w http.ResponseWriter, r *http.Request) {
	cands, err := h.RecipeCandidates(r.Context(), chi.URLParam(r, "type"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": cands})
}
