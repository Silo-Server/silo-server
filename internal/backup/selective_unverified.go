package backup

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// What a restored item can set off on its own once it is written: requests
// to outside services, provider or AI charges, or long background work.
// From a backup this server cannot verify (RestoreSet.unverified) each is
// restored inactive and named in the preview; an admin turns it on.
// TestUnverifiedWorkIsClassified walks this list.
type unverifiedWork struct {
	kind, field string
	// label names it in the preview and the report.
	label string
	// sets is where the restore leaves it inactive.
	sets string
}

// Where the restore leaves a field inactive, named more than once.
const (
	setsLibrary    = "withoutCostlyOptions"
	setsCollection = "applier.createCollection"
	setsTemplate   = "customizeTemplate"
	fieldSource    = "source"
	fieldSchedule  = "sync_schedule"
)

var unverifiedWorkFields = []unverifiedWork{
	{KindLibrary, "auto_translate_metadata", "AI translation of metadata", setsLibrary},
	{KindLibrary, "trickplay_enabled", "trickplay previews", setsLibrary},
	{KindLibrary, "intro_detection_enabled", "intro detection", setsLibrary},
	{KindLibrary, "chapter_thumbnails_enabled", "chapter thumbnails", setsLibrary},
	{KindLibrary, "box_sets_enabled", "box sets", setsLibrary},
	// Every collection a restore creates goes through createCollection: an
	// admin collection, one a section carries, a template collection.
	{KindCollection, fieldSchedule, "its sync schedule", setsCollection},
	{KindHomeSection, "collection.sync_schedule", "its collection's sync schedule", setsCollection},
	{KindTemplateCollection, fieldSource, "its source", setsTemplate},
	{KindTemplateCollection, fieldSchedule, "its sync schedule", setsTemplate},
	// A seasonal section's themes name MDBList lists the seasonal task
	// fetches for every enabled one.
	{KindHomeSection, "enabled (" + string(sections.SectionSeasonalThemed) + ")", "the section", "restoredSectionEnabled"},
	{KindPlugin, "configuration", "its configuration", "applier.applyPlugin"},
	{KindSettingsPage, "external_service settings", "settings that connect to outside services", "planner.planSettingsPage"},
}

// libraryWork reads and clears each library field of unverifiedWorkFields.
var libraryWork = map[string]struct {
	on  func(LibraryEntry) bool
	off func(*catalog.CreateFolderInput)
}{
	"auto_translate_metadata": {func(e LibraryEntry) bool { return e.AutoTranslateMetadata }, func(in *catalog.CreateFolderInput) { in.AutoTranslateMetadata = false }},
	"trickplay_enabled":       {func(e LibraryEntry) bool { return e.TrickplayEnabled }, func(in *catalog.CreateFolderInput) { in.TrickplayEnabled = false }},
	"intro_detection_enabled": {func(e LibraryEntry) bool { return e.IntroDetectionEnabled }, func(in *catalog.CreateFolderInput) { in.IntroDetectionEnabled = false }},
	"chapter_thumbnails_enabled": {func(e LibraryEntry) bool { return e.ChapterThumbnailsEnabled },
		func(in *catalog.CreateFolderInput) { in.ChapterThumbnailsEnabled = false }},
	"box_sets_enabled": {func(e LibraryEntry) bool { return e.BoxSetsEnabled }, func(in *catalog.CreateFolderInput) { in.BoxSetsEnabled = false }},
}

// costlyLibraryOptions names the options of e that unverifiedWorkFields
// lists and e turns on: what a restore from an unverified set leaves off
// (withoutCostlyOptions).
func costlyLibraryOptions(e LibraryEntry) []string {
	var out []string
	for _, w := range unverifiedWorkFields {
		if w.kind == KindLibrary && libraryWork[w.field].on(e) {
			out = append(out, w.label)
		}
	}
	return out
}

// withoutCostlyOptions is in with every library option unverifiedWorkFields
// lists off.
func withoutCostlyOptions(in catalog.CreateFolderInput) catalog.CreateFolderInput {
	for _, w := range unverifiedWorkFields {
		if w.kind == KindLibrary {
			libraryWork[w.field].off(&in)
		}
	}
	return in
}

// maxShownSeasonalLists bounds the lists a preview names for one section,
// and maxRestoredSeasonal the seasonal sections one restore writes: each
// names lists the seasonal task fetches.
const (
	maxShownSeasonalLists = 12
	maxRestoredSeasonal   = 50
)

// seasonalLists are the MDBList lists a seasonal section's config names
// over the next year, as the seasonal task would fetch them.
func seasonalLists(e HomeSectionEntry, now time.Time) []string {
	if e.SectionType != string(sections.SectionSeasonalThemed) {
		return nil
	}
	var p recipes.SeasonalThemedParams
	if json.Unmarshal(e.Config, &p) != nil {
		return nil
	}
	return recipes.SeasonalListURLsAround(p, now, 366*24*time.Hour)
}

// restoredSectionEnabled is whether a restored section is enabled, and a
// note when the restore turned it off: a seasonal section of a backup this
// server cannot verify comes back disabled, since the seasonal task
// fetches the lists of every enabled one.
func restoredSectionEnabled(e HomeSectionEntry, unverified bool, now time.Time) (bool, string) {
	lists := seasonalLists(e, now)
	if !unverified || !e.Enabled || e.SectionType != string(sections.SectionSeasonalThemed) {
		return e.Enabled, ""
	}
	return false, "Restored disabled: a seasonal section of a backup this server cannot verify fetches lists from MDBList" + listedURLs(lists) + "."
}

// seasonalNote is the preview's word on a seasonal section: the lists it
// fetches, and, from a backup this server cannot verify, that it comes
// back disabled.
func (p *planner) seasonalNote(e HomeSectionEntry, now time.Time) string {
	lists := seasonalLists(e, now)
	switch {
	case e.SectionType != string(sections.SectionSeasonalThemed) || !e.Enabled:
		return ""
	case !p.authenticated:
		_, note := restoredSectionEnabled(e, true, now)
		return note
	case len(lists) > 0:
		return "It fetches lists from MDBList" + listedURLs(lists) + "."
	}
	return ""
}

// listedURLs is ": a, b, and 3 more", or "".
func listedURLs(urls []string) string {
	if len(urls) == 0 {
		return ""
	}
	shown := urls[:min(len(urls), maxShownSeasonalLists)]
	out := ": " + strings.Join(shown, ", ")
	if more := len(urls) - len(shown); more > 0 {
		out += fmt.Sprintf(", and %d more", more)
	}
	return out
}
