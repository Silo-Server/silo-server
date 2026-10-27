package handlers

import (
	"context"
)

// RecipeHandler exposes recipe-registry metadata over HTTP.
type RecipeHandler struct{}

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
