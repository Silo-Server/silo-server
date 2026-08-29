package templates

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

// validate sanity-checks a template definition before it joins the registry.
// Failures here indicate a programming mistake in the curated catalog, so the
// registry panics on them at startup — easier to catch in tests than to debug
// from a confused admin UI.
func validate(t Template) error {
	switch {
	case strings.TrimSpace(t.ID) == "":
		return errors.New("id is required")
	case strings.TrimSpace(t.Title) == "":
		return errors.New("title is required")
	case t.Category == "":
		return errors.New("category is required")
	case t.MediaKind == "":
		return errors.New("media_kind is required")
	}

	switch t.Source {
	case SourceTMDB:
		if t.TMDB == nil {
			return errors.New("tmdb spec is required for tmdb source")
		}
		if t.Trakt != nil || t.MDBList != nil || t.TMDBDiscover != nil || t.TMDBCollection != nil {
			return errors.New("only one source spec may be set")
		}
		return validateTMDB(*t.TMDB)
	case SourceTrakt:
		if t.Trakt == nil {
			return errors.New("trakt spec is required for trakt source")
		}
		if t.TMDB != nil || t.MDBList != nil || t.TMDBDiscover != nil || t.TMDBCollection != nil {
			return errors.New("only one source spec may be set")
		}
		return validateTrakt(*t.Trakt, t.RequiresProfile)
	case SourceMDBList:
		if t.MDBList == nil {
			return errors.New("mdblist spec is required for mdblist source")
		}
		if t.TMDB != nil || t.Trakt != nil || t.TMDBDiscover != nil || t.TMDBCollection != nil {
			return errors.New("only one source spec may be set")
		}
		return validateMDBList(*t.MDBList)
	case SourceTMDBDiscover:
		if t.TMDBDiscover == nil {
			return errors.New("tmdb_discover spec is required for tmdb_discover source")
		}
		if t.TMDB != nil || t.Trakt != nil || t.MDBList != nil || t.TMDBCollection != nil {
			return errors.New("only one source spec may be set")
		}
		return validateTMDBDiscover(*t.TMDBDiscover)
	case SourceTMDBCollection:
		if t.TMDBCollection == nil {
			return errors.New("tmdb_collection spec is required for tmdb_collection source")
		}
		if t.TMDB != nil || t.Trakt != nil || t.MDBList != nil || t.TMDBDiscover != nil {
			return errors.New("only one source spec may be set")
		}
		return validateTMDBCollection(*t.TMDBCollection)
	default:
		return fmt.Errorf("unknown source %q", t.Source)
	}
}

func (r *Registry) validateBundleLocked(b Bundle) error {
	switch {
	case strings.TrimSpace(b.ID) == "":
		return errors.New("id is required")
	case strings.TrimSpace(b.Title) == "":
		return errors.New("title is required")
	case len(b.TemplateIDs) == 0:
		return errors.New("template_ids is required")
	}
	seen := make(map[string]struct{}, len(b.TemplateIDs))
	for _, id := range b.TemplateIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return errors.New("template_ids cannot contain empty IDs")
		}
		if _, exists := seen[trimmed]; exists {
			return fmt.Errorf("duplicate template ID %q", trimmed)
		}
		seen[trimmed] = struct{}{}
		idx, ok := r.byID[trimmed]
		if !ok {
			return fmt.Errorf("unknown template ID %q", trimmed)
		}
		if r.templates[idx].RequiresProfile {
			return fmt.Errorf("template %q requires a profile", trimmed)
		}
	}
	return nil
}

func validateTMDB(spec TMDBSpec) error {
	switch spec.Preset {
	case "trending":
		switch spec.MediaType {
		case "movie", "tv", "all":
		default:
			return fmt.Errorf("tmdb trending: media_type must be movie, tv, or all (got %q)", spec.MediaType)
		}
		switch spec.TimeWindow {
		case "day", "week":
		default:
			return fmt.Errorf("tmdb trending: time_window must be day or week (got %q)", spec.TimeWindow)
		}
	case "popular", "top_rated":
		switch spec.MediaType {
		case "movie", "tv":
		default:
			return fmt.Errorf("tmdb %s: media_type must be movie or tv", spec.Preset)
		}
		if spec.TimeWindow != "" {
			return fmt.Errorf("tmdb %s: time_window is not allowed", spec.Preset)
		}
	case "now_playing", "upcoming":
		if spec.MediaType != "movie" {
			return fmt.Errorf("tmdb %s requires media_type=movie", spec.Preset)
		}
	case "airing_today", "on_the_air":
		if spec.MediaType != "tv" {
			return fmt.Errorf("tmdb %s requires media_type=tv", spec.Preset)
		}
	default:
		return fmt.Errorf("tmdb: unsupported preset %q", spec.Preset)
	}
	return nil
}

func validateTrakt(spec TraktSpec, requiresProfile bool) error {
	switch spec.Preset {
	case "trending", "popular":
		if requiresProfile {
			return errors.New("trakt: trending/popular do not require a profile")
		}
	case "recommended":
		if !requiresProfile {
			return errors.New("trakt: recommended must set requires_profile=true")
		}
	default:
		return fmt.Errorf("trakt: unsupported preset %q", spec.Preset)
	}
	switch spec.MediaType {
	case "movie", "tv":
	default:
		return fmt.Errorf("trakt: media_type must be movie or tv (got %q)", spec.MediaType)
	}
	return nil
}

// tmdbDiscoverSortByValues enumerates the sort_by values TMDB documents for
// /discover/movie and /discover/tv. Validation is intentionally lenient — we
// accept the documented set rather than introspecting per-media-type quirks
// because TMDB silently ignores irrelevant sorts.
var tmdbDiscoverSortByValues = map[string]struct{}{
	"popularity.desc":           {},
	"popularity.asc":            {},
	"vote_average.desc":         {},
	"vote_average.asc":          {},
	"primary_release_date.desc": {},
	"primary_release_date.asc":  {},
	"first_air_date.desc":       {},
	"first_air_date.asc":        {},
	"revenue.desc":              {},
	"revenue.asc":               {},
	"vote_count.desc":           {},
	"vote_count.asc":            {},
}

func validateTMDBDiscover(spec TMDBDiscoverSpec) error {
	switch spec.MediaType {
	case "movie", "tv":
	default:
		return fmt.Errorf("tmdb_discover: media_type must be movie or tv (got %q)", spec.MediaType)
	}

	sortBy := strings.TrimSpace(spec.SortBy)
	if sortBy == "" {
		return errors.New("tmdb_discover: sort_by is required")
	}
	if _, ok := tmdbDiscoverSortByValues[sortBy]; !ok {
		return fmt.Errorf("tmdb_discover: unsupported sort_by %q", sortBy)
	}

	if spec.VoteCountGte < 0 {
		return fmt.Errorf("tmdb_discover: vote_count_gte must be >= 0 (got %d)", spec.VoteCountGte)
	}
	if spec.VoteAverageGte < 0 {
		return fmt.Errorf("tmdb_discover: vote_average_gte must be >= 0 (got %v)", spec.VoteAverageGte)
	}

	if spec.ReleaseDateGte != "" {
		if _, err := time.Parse("2006-01-02", spec.ReleaseDateGte); err != nil {
			return fmt.Errorf("tmdb_discover: release_date_gte must be YYYY-MM-DD (got %q)", spec.ReleaseDateGte)
		}
	}
	if spec.ReleaseDateLte != "" {
		if _, err := time.Parse("2006-01-02", spec.ReleaseDateLte); err != nil {
			return fmt.Errorf("tmdb_discover: release_date_lte must be YYYY-MM-DD (got %q)", spec.ReleaseDateLte)
		}
	}

	for i, cert := range spec.Certifications {
		if strings.TrimSpace(cert) == "" {
			return fmt.Errorf("tmdb_discover: certifications[%d] must not be empty", i)
		}
	}

	if spec.WithRuntimeGte < 0 {
		return fmt.Errorf("tmdb_discover: with_runtime_gte must be >= 0 (got %d)", spec.WithRuntimeGte)
	}
	if spec.WithRuntimeLte < 0 {
		return fmt.Errorf("tmdb_discover: with_runtime_lte must be >= 0 (got %d)", spec.WithRuntimeLte)
	}
	if spec.WithRuntimeGte > 0 && spec.WithRuntimeLte > 0 && spec.WithRuntimeGte > spec.WithRuntimeLte {
		return fmt.Errorf("tmdb_discover: with_runtime_gte (%d) must be <= with_runtime_lte (%d)", spec.WithRuntimeGte, spec.WithRuntimeLte)
	}

	if lang := strings.TrimSpace(spec.OriginalLanguage); lang != "" && len(lang) != 2 {
		return fmt.Errorf("tmdb_discover: original_language must be a 2-letter ISO 639-1 code (got %q)", spec.OriginalLanguage)
	}

	return nil
}

// validateTMDBCollection checks a TMDB collection franchise spec.
//
// CollectionID == 0 is a deliberate placeholder/sentinel: the catalog ships a
// generic "TMDB Franchise" template that admins fill in at apply-time. Such
// placeholder templates land in the gallery but the resulting collection's
// sync will fail loudly until the admin edits the source config and supplies
// a real TMDB collection ID (see syncTMDBFranchiseCollection in the catalog
// package). Validation therefore allows zero, but never negative IDs.
func validateTMDBCollection(spec TMDBCollectionSpec) error {
	if spec.CollectionID < 0 {
		return fmt.Errorf("tmdb_collection: collection_id must be >= 0 (got %d)", spec.CollectionID)
	}
	return nil
}

func validateMDBList(spec MDBListSpec) error {
	trimmed := strings.TrimSpace(spec.URL)
	if trimmed == "" {
		// Empty URL is allowed; it means the template is a "bring your own URL"
		// placeholder that simply opens the standard MDBList import form.
		return nil
	}
	if _, err := collectionutil.CanonicalMDBListURL(trimmed); err != nil {
		return fmt.Errorf("mdblist url: %w", err)
	}
	return nil
}
