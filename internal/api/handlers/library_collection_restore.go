package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// TemplateCollectionDefaults is the collection a bundle apply creates from
// templateID for a library of libraryType, with every default the apply
// sets, and the template's title. ok is false when this server's catalog
// has no such template or the template does not suit the library. A
// selective restore creates a missing template collection from this input,
// so a restored collection and an applied one validate and default alike.
func (h *LibraryCollectionHandler) TemplateCollectionDefaults(bundleID, templateID string, libraryID int, libraryType string) (title string, input catalog.CreateLibraryCollectionInput, needsSetup, ok bool, err error) {
	tmpl, found := h.templateRegistry().Get(templateID)
	if !found || !templateEligibleForLibrary(tmpl, &models.MediaFolder{Type: libraryType}) {
		return "", catalog.CreateLibraryCollectionInput{}, false, false, nil
	}
	input, err = templateCollectionInput(bundleID, tmpl, libraryID, templateBundleManagementKey(bundleID, templateID, libraryID))
	if err != nil {
		return "", catalog.CreateLibraryCollectionInput{}, false, false, err
	}
	return tmpl.Title, input, tmpl.NeedsSetup(), true, nil
}

// TemplateTitle is the title of templateID in this server's catalog.
func (h *LibraryCollectionHandler) TemplateTitle(templateID string) (string, bool) {
	tmpl, ok := h.templateRegistry().Get(templateID)
	return tmpl.Title, ok
}

// TemplateCollectionRestored finishes a template collection a restore
// created, as a bundle apply does after creating one: it stores the
// template's poster when the collection still uses it, syncs the items, and
// schedules the next sync. It may run again after a crash or a failed sync.
func (h *LibraryCollectionHandler) TemplateCollectionRestored(ctx context.Context, collectionID string) error {
	collection, err := h.repo.GetByID(ctx, collectionID)
	if errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	templateID := templateIDOfManagementKey(collection.ManagementKey)
	tmpl, ok := h.templateRegistry().Get(templateID)
	if !ok {
		return nil
	}
	if poster := strings.TrimSpace(tmpl.PosterPath); poster != "" && collection.PosterFromTemplate && collection.PosterURL == poster {
		if _, _, _, err := h.storeBundledTemplatePoster(ctx, collectionID, poster, true); err != nil {
			return err
		}
	}
	// A restored source can carry the TMDB collection an administrator set
	// on a placeholder template before the backup.
	if tmpl.NeedsSetup() && catalog.TMDBCollectionSourceUnset(collection.SourceConfig) {
		return nil
	}
	if h.service == nil {
		return errors.New("collection sync is not available on this server")
	}
	if _, err := h.service.SyncCollectionWithOptions(ctx, collectionID, catalog.SyncCollectionOptions{}); err != nil {
		return fmt.Errorf("syncing the restored collection: %w", err)
	}
	if collection.SyncSchedule != nil && strings.TrimSpace(*collection.SyncSchedule) != "" {
		next := catalog.ComputeNextSyncAtFrom(*collection.SyncSchedule, time.Now())
		if err := h.repo.UpdateNextSyncAt(ctx, collectionID, next); err != nil {
			return err
		}
	}
	return nil
}

// templateIDOfManagementKey reads the template id from a template bundle
// management key, "<bundle>:<template>:library:<id>".
func templateIDOfManagementKey(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) != 4 || parts[2] != adminSectionScopeLibrary {
		return ""
	}
	if _, err := strconv.Atoi(parts[3]); err != nil {
		return ""
	}
	return parts[1]
}

// CheckRestoredSection validates a section a restore writes the way the
// admin section editor validates one: its type, scope, configuration, and
// the recipe check bulk create runs. A new row of a type that no longer
// takes new rows is refused.
func (h *SectionHandler) CheckRestoredSection(sectionType, scope string, libraryID *int, config json.RawMessage, create bool) error {
	if !sections.ValidSectionTypes[sections.SectionType(sectionType)] {
		return fmt.Errorf("section type %q is not one this server has", sectionType)
	}
	if create && refusesNewRow(sectionType) {
		return newRowRefusedError(sectionType)
	}
	if msg, ok := validateSectionScope(scope, libraryID); !ok {
		return errors.New(msg)
	}
	if msg, ok := validateSectionConfig(sections.SectionType(sectionType), config, false); !ok {
		return errors.New(msg)
	}
	if msg, ok := validateRecipeConfig(sections.SectionType(sectionType), config); !ok {
		return errors.New(msg)
	}
	return nil
}
