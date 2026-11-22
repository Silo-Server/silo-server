package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"slices"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

type sectionScopeLister interface {
	ListByScope(ctx context.Context, scope string, libraryID *int) ([]*sections.PageSection, error)
	ListByScopeAll(ctx context.Context, scope string, libraryID *int) ([]*sections.PageSection, error)
}

// ProfileSectionBaseline is one page's server-defined rows before profile
// overrides are applied, used for home layout export snapshots and import
// previews.
type ProfileSectionBaseline struct {
	// SectionIDs holds every server section ID on this page in a library the
	// acting profile is allowed: stored rows (enabled and disabled) plus
	// fallback defaults when no stored row is enabled. Rows tied to libraries
	// the profile isn't allowed are left out, so the list reveals nothing
	// about them; libraries the profile hid from its own browsing still count.
	SectionIDs []string
	// VisibleSections holds the pre-override active server sections permitted
	// by the acting profile's access policy, in display order.
	VisibleSections []sections.ResolvedSection
}

func (h *SectionHandler) lister() sectionScopeLister {
	if h.sectionLister != nil {
		return h.sectionLister
	}
	return h.repo
}

// activeAdminSections returns the enabled admin sections for a page, falling
// back to the same default sections as the Home and library feeds when no
// stored row is enabled.
func (h *SectionHandler) activeAdminSections(ctx context.Context, scope string, libraryID *int) ([]*sections.PageSection, error) {
	enabled, err := h.lister().ListByScope(ctx, scope, libraryID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	return h.fallbackAdminSectionsIfEmpty(ctx, scope, libraryID, enabled)
}

func (h *SectionHandler) fallbackAdminSectionsIfEmpty(ctx context.Context, scope string, libraryID *int, enabled []*sections.PageSection) ([]*sections.PageSection, error) {
	if len(enabled) > 0 {
		return enabled, nil
	}
	if scope == adminSectionScopeHome {
		defaults, err := h.defaultHomeSections(ctx)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
		}
		return defaults, nil
	}
	if scope == adminSectionScopeLibrary && libraryID != nil {
		defaults, err := h.defaultLibrarySections(ctx, *libraryID)
		if err != nil {
			slog.WarnContext(ctx, "loading typed library section defaults", "component", "api", "library_id", *libraryID, "error", err)
			return sections.DefaultLibrarySections(libraryID), nil
		}
		return defaults, nil
	}
	return enabled, nil
}

// GetProfileSectionBaseline returns a page's pre-override server section
// baseline for the acting profile. Access filtering matches the Home feed:
// request scope first, else fail-closed policy lookup.
func (h *SectionHandler) GetProfileSectionBaseline(ctx context.Context, userID int, scope string, libraryID *int) (ProfileSectionBaseline, error) {
	if scope == adminSectionScopeLibrary {
		if libraryID == nil {
			return ProfileSectionBaseline{}, apiError(http.StatusBadRequest, "bad_request", "Library ID is required")
		}
		if err := h.requireViewableLibrary(ctx, *libraryID); err != nil {
			return ProfileSectionBaseline{}, err
		}
	}

	storedAll, err := h.lister().ListByScopeAll(ctx, scope, libraryID)
	if err != nil {
		return ProfileSectionBaseline{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}

	enabled := make([]*sections.PageSection, 0, len(storedAll))
	for _, s := range storedAll {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	active, err := h.fallbackAdminSectionsIfEmpty(ctx, scope, libraryID, enabled)
	if err != nil {
		return ProfileSectionBaseline{}, err
	}

	accessFilter, err := h.sectionBaselineAccessFilter(ctx, userID)
	if err != nil {
		return ProfileSectionBaseline{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve section access")
	}

	tokenFilter := allowedLibrariesFilter(ctx, accessFilter)

	resolved := sections.Resolve(active, nil)
	visible := filterResolvedSectionsByAccess(resolved, accessFilter)
	if visible == nil {
		visible = []sections.ResolvedSection{}
	}

	return ProfileSectionBaseline{
		SectionIDs:      uniqueSectionIDs(allowedPageSections(storedAll, tokenFilter), allowedPageSections(active, tokenFilter)),
		VisibleSections: visible,
	}, nil
}

// allowedLibrariesFilter is filter widened back to every library the profile
// is allowed, including the ones it hid from its own browsing: a row it hid
// that way is still one it knew about, and showing the library again must not
// make that row look new to an import. An unrestricted profile's hidden
// libraries arrive as DisabledLibraryIDs; a restricted one's were taken out
// of AllowedLibraryIDs and kept in the scope's HiddenLibraryIDs.
func allowedLibrariesFilter(ctx context.Context, filter catalog.AccessFilter) catalog.AccessFilter {
	filter.DisabledLibraryIDs = nil
	if scope, ok := access.GetScope(ctx); ok && filter.AllowedLibraryIDs != nil && len(scope.HiddenLibraryIDs) > 0 {
		filter.AllowedLibraryIDs = append(slices.Clone(filter.AllowedLibraryIDs), scope.HiddenLibraryIDs...)
	}
	return filter
}

// allowedPageSections keeps the rows the access filter permits, by the same
// rule the Home feed applies to resolved rows.
func allowedPageSections(rows []*sections.PageSection, filter catalog.AccessFilter) []*sections.PageSection {
	out := make([]*sections.PageSection, 0, len(rows))
	for _, s := range rows {
		if s != nil && sectionAllowedByAccess(sections.ResolvedSection{Config: s.Config}, filter) {
			out = append(out, s)
		}
	}
	return out
}

func (h *SectionHandler) sectionBaselineAccessFilter(ctx context.Context, userID int) (catalog.AccessFilter, error) {
	if scope, ok := access.GetScope(ctx); ok {
		return catalog.AccessFilter{
			AllowedLibraryIDs:  scope.AllowedLibraryIDs,
			DisabledLibraryIDs: scope.DisabledLibraryIDs,
			MaturityLimits:     scope.MaturityLimits,
			MaxPlaybackQuality: scope.MaxPlaybackQuality,
		}, nil
	}
	if h.UserRepo == nil {
		return catalog.AccessFilter{}, nil
	}
	user, err := h.UserRepo.GetByID(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "looking up user for section baseline access", "component", "api", "error", err)
		return catalog.AccessFilter{}, err
	}
	if user == nil {
		return catalog.AccessFilter{}, nil
	}
	effective, err := access.EffectivePolicyForUser(ctx, user, h.AccessGroups)
	if err != nil {
		slog.ErrorContext(ctx, "resolving user policy for section baseline access", "component", "api", "error", err)
		return catalog.AccessFilter{}, err
	}
	return catalog.AccessFilter{AllowedLibraryIDs: effective.LibraryIDs}, nil
}

func uniqueSectionIDs(groups ...[]*sections.PageSection) []string {
	seen := make(map[string]struct{})
	ids := []string{}
	for _, group := range groups {
		for _, s := range group {
			if s == nil || s.ID == "" {
				continue
			}
			if _, ok := seen[s.ID]; ok {
				continue
			}
			seen[s.ID] = struct{}{}
			ids = append(ids, s.ID)
		}
	}
	return ids
}
