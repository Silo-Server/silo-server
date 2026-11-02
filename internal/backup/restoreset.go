package backup

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// The restore set is a tar of versioned JSON sections plus the blobs they
// reference, stored beside db.dump. Selective restore reads it instead of the
// dump, so it works across Silo versions and on another server.
//
//	restore-set.json                  the index, always the first entry
//	settings.json
//	plugins/<plugin-id>/plugin.json
//	plugins/<plugin-id>/archive.bin
//	libraries.json
//	collections.json
//	home-sections.json                version 3 and later
//	users/<account id>.json           version 2 and later
//	blobs/<object key>
//
// Readers accept every format version up to restoreSetVersion and refuse a
// newer one with ErrRestoreSetTooNew. Version 2 added the users section;
// version 3 the home_sections section, template-bundle collections in
// collections.json, and an omitted item's cause and error. A reader refuses
// an unknown section and an unknown field, so any of those is a new version,
// and an older set never holds them.
const (
	restoreSetFormat  = "silo-restore-set"
	restoreSetVersion = 3

	restoreSetIndexName = "restore-set.json"
	restoreSetBlobDir   = "blobs/"
	restoreSetPluginDir = "plugins/"
	restoreSetUsersDir  = "users/"

	pluginDocumentName = "plugin.json"
	pluginArchiveName  = "archive.bin"

	settingsDocumentName     = "settings.json"
	librariesDocumentName    = "libraries.json"
	collectionsDocumentName  = "collections.json"
	homeSectionsDocumentName = "home-sections.json"
)

// Section ids. They name sections in the index, the manifest, preview, the
// restore selection, and the report.
const (
	SectionSettings    = "settings"
	SectionPlugins     = "plugins"
	SectionLibraries   = "libraries"
	SectionUsers       = "users"
	SectionCollections = "collections"
	// SectionHomeSections holds the admin home and library page sections.
	SectionHomeSections = "home_sections"
)

// sectionSpec describes one section: where its files live in the archive and
// which blob keys it may carry. Each section owns disjoint blob prefixes, so a
// blob's section is known from its key.
type sectionSpec struct {
	id string
	// document is the section's one JSON file; empty for a section stored
	// one item per directory.
	document string
	// itemDir is the per-item directory prefix ("plugins/"), whose entries
	// are <itemDir><item id>/<name> with name in itemFiles, or, with
	// itemExt set, one file <itemDir><item id><itemExt> per item.
	itemDir   string
	itemFiles []string
	itemExt   string
	// minVersion is the first format version that may hold the section.
	minVersion int
	// blobPrefixes are the key prefixes this section's blobs may use, and
	// the store each lands in.
	blobPrefixes map[string]string
}

// restoreSections lists every section in the order a restore applies them:
// libraries first, so later sections can map library ids, then plugins,
// settings, collections (which home sections name), home sections (which
// carry the collections they manage), and users last, so a profile's page
// layout resolves against the sections restored before it. Nothing else in
// the reader or writer lists sections.
var restoreSections = []sectionSpec{
	{id: SectionLibraries, document: librariesDocumentName,
		blobPrefixes: map[string]string{"library-posters/": storeAssets}},
	{id: SectionPlugins, itemDir: restoreSetPluginDir, itemFiles: []string{pluginDocumentName, pluginArchiveName}},
	{id: SectionSettings, document: settingsDocumentName},
	{id: SectionCollections, document: collectionsDocumentName,
		blobPrefixes: map[string]string{"collection-images/": storeAssets, "local/": storeAssets, "branding/": storeAssets}},
	{id: SectionHomeSections, document: homeSectionsDocumentName, minVersion: 3},
	{id: SectionUsers, itemDir: restoreSetUsersDir, itemExt: ".json", minVersion: 2,
		blobPrefixes: map[string]string{"profile-avatars/": storeOperational, "user-collection-images/": storeAssets}},
}

func sectionByID(id string) (sectionSpec, bool) {
	for _, s := range restoreSections {
		if s.id == id {
			return s, true
		}
	}
	return sectionSpec{}, false
}

// validSectionID accepts a lowercase identifier, the shape every section id
// has, so a newer format's unknown section still names something sensible.
func validSectionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// ownsFile reports whether path is one of the section's files, and the item
// it belongs to for a per-item section.
func (s sectionSpec) ownsFile(name string) (item string, ok bool) {
	if s.document != "" {
		return "", name == s.document
	}
	rest, found := strings.CutPrefix(name, s.itemDir)
	if !found {
		return "", false
	}
	if s.itemExt != "" {
		item, found := strings.CutSuffix(rest, s.itemExt)
		if !found || strings.Contains(item, "/") || !validItemSegment(item) {
			return "", false
		}
		return item, true
	}
	item, file, found := strings.Cut(rest, "/")
	if !found || !validItemSegment(item) || !slices.Contains(s.itemFiles, file) {
		return "", false
	}
	return item, true
}

// blobStore reports the store a key of this section lands in.
func (s sectionSpec) blobStore(key string) (string, bool) {
	if !allowedRestoreSetKey(key) {
		return "", false
	}
	for prefix, store := range s.blobPrefixes {
		if strings.HasPrefix(key, prefix) {
			return store, true
		}
	}
	return "", false
}

// validItemSegment is one safe path segment naming an item, such as a plugin
// id.
func validItemSegment(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !safeIDRune(r) {
			return false
		}
	}
	return true
}

// RestoreSetIndex is restore-set.json.
type RestoreSetIndex struct {
	Format      string         `json:"format"`
	Version     int            `json:"version"`
	ServerID    string         `json:"server_id"`
	ServerName  string         `json:"server_name"`
	SiloVersion string         `json:"silo_version"`
	CreatedAt   time.Time      `json:"created_at"`
	KeyCheck    string         `json:"key_check"`
	Sections    []SectionIndex `json:"sections"`
	// Omitted lists items the backup could not carry (version 2 and
	// later); the dump still holds them.
	Omitted []OmittedItem `json:"omitted,omitempty"`
}

// OmittedItem is an item a restore set left out, with the reason.
type OmittedItem struct {
	Section string `json:"section"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	// Reason is OmittedTooLarge or OmittedExportFailed; Message says why.
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// Cause names what failed, stable across backups so a client can group
	// omitted items: a data type and the step that failed in it
	// ("notifications: reading notification preferences"). Error is the
	// underlying error, without ids. Both are empty for OmittedTooLarge and
	// in sets before version 3.
	Cause string `json:"cause,omitempty"`
	Error string `json:"error,omitempty"`
}

// Reasons a restore set leaves an item out.
const (
	OmittedTooLarge     = "too_large"
	OmittedExportFailed = "export_failed"
)

// SectionIndex lists one section's files with their sizes and digests, and
// the blob keys its documents reference. A referenced blob that was gone when
// the backup copied it is listed but absent from the archive.
type SectionIndex struct {
	ID    string       `json:"id"`
	Count int          `json:"count"`
	Files []FileDigest `json:"files"`
	Blobs []string     `json:"blobs"`
}

// FileDigest is one file's size and SHA-256.
type FileDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SettingsDocument is settings.json: server_settings rows as stored, so an
// encrypted value stays ciphertext bound to its key name.
type SettingsDocument struct {
	Settings []SettingEntry `json:"settings"`
}

type SettingEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Encrypted bool   `json:"encrypted"`
}

// LibrariesDocument is libraries.json.
type LibrariesDocument struct {
	Libraries []LibraryEntry `json:"libraries"`
}

// LibraryEntry is one media_folders row with its paths and options.
type LibraryEntry struct {
	ID                           int      `json:"id"`
	Name                         string   `json:"name"`
	Type                         string   `json:"type"`
	Paths                        []string `json:"paths"`
	Enabled                      bool     `json:"enabled"`
	MetadataLanguage             string   `json:"metadata_language"`
	ChapterThumbnailsEnabled     bool     `json:"chapter_thumbnails_enabled"`
	IntroDetectionEnabled        bool     `json:"intro_detection_enabled"`
	AutoTranslateMetadata        bool     `json:"auto_translate_metadata"`
	RealtimeMonitoring           bool     `json:"realtime_monitoring"`
	TrickplayEnabled             bool     `json:"trickplay_enabled"`
	TrailerKinds                 []string `json:"trailer_kinds"`
	CollectionUngroupedSortOrder int      `json:"collection_ungrouped_sort_order"`
	// The box set settings. A restore set written before box sets has none:
	// box sets off, with the defaults.
	BoxSetsEnabled  bool  `json:"box_sets_enabled"`
	BoxSetMinMovies int   `json:"box_set_min_movies"`
	BoxSetsInSearch *bool `json:"box_sets_in_search"`
	// Poster is the library poster's blob key, or empty.
	Poster string `json:"poster"`
}

// PluginDocument is plugins/<plugin-id>/plugin.json. Configs hold
// plugin_runtime_configs values as stored: each row is encrypted as one
// object under its source installation id.
type PluginDocument struct {
	PluginID       string              `json:"plugin_id"`
	Version        string              `json:"version"`
	Enabled        bool                `json:"enabled"`
	InstallationID int                 `json:"installation_id"`
	Archive        FileDigest          `json:"archive"`
	Configs        []PluginConfigEntry `json:"configs"`
}

type PluginConfigEntry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// CollectionsDocument is collections.json: manually managed admin
// collections, collections a template bundle created (version 3 and later),
// uploaded item artwork, and branding assets. Collections a home section
// manages travel with their section (HomeSectionsDocument).
type CollectionsDocument struct {
	Collections []CollectionEntry `json:"collections"`
	// TemplateCollections are template-bundle collections: which template
	// was applied to which library, and what the admin changed since. Their
	// items are not carried; the restored collection syncs them again.
	TemplateCollections []TemplateCollectionEntry `json:"template_collections,omitempty"`
	ItemArtwork         []ItemArtworkEntry        `json:"item_artwork"`
	Branding            []BrandingEntry           `json:"branding"`
}

// TemplateCollectionEntry is one collection a template bundle created
// (management_mode template_bundle), keyed by its management key
// "<bundle>:<template>:library:<library id>". The template's own definition
// is code: only its id is carried, with the fields an admin can change
// afterwards. Poster and Backdrop are as stored; an uploaded image's blob
// is carried only when the admin set it (not from the template, not a
// generated collage).
type TemplateCollectionEntry struct {
	ID            string          `json:"id"`
	BundleID      string          `json:"bundle_id"`
	TemplateID    string          `json:"template_id"`
	ManagementKey string          `json:"management_key"`
	LibraryID     int             `json:"library_id"`
	Libraries     []CollectionLib `json:"libraries"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	Visibility    string          `json:"visibility"`
	Featured      bool            `json:"featured"`
	SortOrder     int             `json:"sort_order"`
	SortConfig    json.RawMessage `json:"sort_config"`
	SourceURL     string          `json:"source_url"`
	SourceConfig  json.RawMessage `json:"source_config"`
	SyncSchedule  *string         `json:"sync_schedule"`
	Poster        string          `json:"poster"`
	// PosterThumbhash is the poster's, PosterAutoGenerated marks a
	// generated collage, and PosterFromTemplate a poster the template
	// supplied.
	PosterThumbhash     string `json:"poster_thumbhash"`
	PosterAutoGenerated bool   `json:"poster_auto_generated"`
	PosterFromTemplate  bool   `json:"poster_from_template"`
	Backdrop            string `json:"backdrop"`
	BackdropThumbhash   string `json:"backdrop_thumbhash"`
}

func (e TemplateCollectionEntry) itemID() string { return templateCollectionItemID(e.ID) }

func templateCollectionItemID(id string) string { return "template:" + id }

// HomeSectionsDocument is home-sections.json (version 3 and later): the
// admin sections of the home page and of each library page, as they are
// now. Revisions are not carried; writing a section advances them.
type HomeSectionsDocument struct {
	Sections []HomeSectionEntry `json:"sections"`
}

// HomeSectionEntry is one page_sections row. A section whose configuration
// names a collection it manages (management_mode section) carries that
// collection; one naming any other collection names it by its backup id,
// which a restore maps through the collections section.
type HomeSectionEntry struct {
	ID          string          `json:"id"`
	Scope       string          `json:"scope"`
	LibraryID   *int            `json:"library_id"`
	Position    int             `json:"position"`
	SectionType string          `json:"section_type"`
	Title       string          `json:"title"`
	Featured    bool            `json:"featured"`
	ItemLimit   int             `json:"item_limit"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
	// Collection is the section-managed collection, with its items; nil
	// for any other section.
	Collection *SectionCollectionEntry `json:"collection,omitempty"`
}

func (e HomeSectionEntry) itemID() string { return homeSectionItemID(e.ID) }

func homeSectionItemID(id string) string { return "section:" + id }

// Page scopes of a section.
const (
	pageScopeHome    = "home"
	pageScopeLibrary = "library"
)

// msgCreated is the report message of an item a restore created.
const msgCreated = "Created."

// SectionCollectionEntry is a collection a section manages.
type SectionCollectionEntry struct {
	CollectionEntry
	ManagementSource string `json:"management_source"`
	ManagementKey    string `json:"management_key"`
}

// CollectionEntry is one library_collections row with its scope, groups, and
// items. Poster and Backdrop are as stored: a blob key under
// collection-images/ for uploaded artwork, or a URL or provider key.
type CollectionEntry struct {
	ID                  string          `json:"id"`
	LibraryID           int             `json:"library_id"`
	Libraries           []CollectionLib `json:"libraries"`
	Slug                string          `json:"slug"`
	Title               string          `json:"title"`
	Description         string          `json:"description"`
	CollectionType      string          `json:"collection_type"`
	Visibility          string          `json:"visibility"`
	SortOrder           int             `json:"sort_order"`
	Featured            bool            `json:"featured"`
	Poster              string          `json:"poster"`
	PosterThumbhash     string          `json:"poster_thumbhash"`
	PosterAutoGenerated bool            `json:"poster_auto_generated"`
	Backdrop            string          `json:"backdrop"`
	BackdropThumbhash   string          `json:"backdrop_thumbhash"`
	SourceURL           string          `json:"source_url"`
	QueryDefinition     json.RawMessage `json:"query_definition"`
	SortConfig          json.RawMessage `json:"sort_config"`
	SourceConfig        json.RawMessage `json:"source_config"`
	SyncSchedule        *string         `json:"sync_schedule"`
	// Items are content ids in collection order.
	Items []string `json:"items"`
}

// CollectionLib is one library in a collection's scope, with the group the
// collection sits in there.
type CollectionLib struct {
	LibraryID int    `json:"library_id"`
	GroupSlug string `json:"group_slug"`
	GroupName string `json:"group_name"`
	GroupSort string `json:"group_sort"`
}

// ItemArtworkEntry is one uploaded image an item, or a season, shows. Content
// ids are deterministic, so the target is found by its content id, and the
// blob key, which embeds it, is the same on every server.
type ItemArtworkEntry struct {
	TargetType      string `json:"target_type"`
	ContentID       string `json:"content_id"`
	ParentContentID string `json:"parent_content_id"`
	Title           string `json:"title"`
	ImageType       string `json:"image_type"`
	Path            string `json:"path"`
	Source          string `json:"source"`
	Thumbhash       string `json:"thumbhash"`
}

// itemID names the artwork slot.
func (a ItemArtworkEntry) itemID() string { return "artwork:" + a.ContentID + ":" + a.ImageType }

// BrandingEntry is one branding asset setting and its blob.
type BrandingEntry struct {
	Key  string `json:"key"`
	Ref  string `json:"ref"`
	Blob string `json:"blob"`
}

func (b BrandingEntry) itemID() string { return "branding:" + b.Key }

func collectionItemID(id string) string { return "collection:" + id }
