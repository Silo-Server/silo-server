package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// restoreSetBuild is a restore set read from the backup's snapshot, ready to
// write. The documents and plugin digests come from the snapshot; plugin
// archive bytes are read from it while writing; blobs are copied after it
// closes, as blobs.tar's are.
type restoreSetBuild struct {
	index   RestoreSetIndex
	docs    []builtDocument
	plugins []builtPlugin
	// blobs are the section blobs to copy, by section, in index order.
	blobs []blobRef
	// collections caches the admin collections two sections read.
	collections []collectionRow
}

type builtDocument struct {
	path string
	data []byte
}

type builtPlugin struct {
	id             string
	installationID int
	archive        FileDigest
}

// restoreSetMeta is what the index records about the source.
type restoreSetMeta struct {
	serverID  string
	keyCheck  string
	createdAt time.Time
	version   string
}

// buildRestoreSet reads every section from the snapshot transaction that
// exported the dump's snapshot, so the restore set and the dump describe the
// same database state.
func buildRestoreSet(ctx context.Context, tx pgx.Tx, meta restoreSetMeta) (*restoreSetBuild, error) {
	b := &restoreSetBuild{index: RestoreSetIndex{
		Format: restoreSetFormat, Version: restoreSetVersion,
		ServerID: meta.serverID, SiloVersion: meta.version, CreatedAt: meta.createdAt.UTC(), KeyCheck: meta.keyCheck,
	}}
	settings, err := readSettingRows(ctx, tx)
	if err != nil {
		return nil, err
	}
	b.index.ServerName = settings[branding.KeyServerName]
	for _, spec := range restoreSections {
		var err error
		switch spec.id {
		case SectionSettings:
			err = b.addSettings(spec, settings)
		case SectionLibraries:
			err = b.addLibraries(ctx, tx, spec)
		case SectionPlugins:
			err = b.addPlugins(ctx, tx, spec)
		case SectionUsers:
			err = b.addUsers(ctx, tx, spec)
		case SectionCollections:
			err = b.addCollections(ctx, tx, spec, settings)
		case SectionHomeSections:
			err = b.addHomeSections(ctx, tx, spec)
		default:
			err = fmt.Errorf("no writer for section %q", spec.id)
		}
		if err != nil {
			return nil, fmt.Errorf("restore set %s: %w", spec.id, err)
		}
	}
	return b, nil
}

// summary is the manifest's record of this restore set.
func (b *restoreSetBuild) summary() *RestoreSetSummary {
	out := &RestoreSetSummary{FormatVersion: restoreSetVersion, Sections: map[string]int{}, Omitted: b.index.Omitted}
	for _, s := range b.index.Sections {
		out.Sections[s.ID] = s.Count
	}
	return out
}

func (b *restoreSetBuild) addDocument(spec sectionSpec, name string, doc any, count int, blobs []blobRef) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	si := SectionIndex{ID: spec.id, Count: count, Files: []FileDigest{b.addFile(name, data)}, Blobs: []string{}}
	si.Blobs, b.blobs = appendSectionBlobs(spec, si.Blobs, b.blobs, blobs)
	b.index.Sections = append(b.index.Sections, si)
	return nil
}

// addFile queues a document for the archive and returns its index entry.
func (b *restoreSetBuild) addFile(path string, data []byte) FileDigest {
	sum := sha256.Sum256(data)
	b.docs = append(b.docs, builtDocument{path: path, data: data})
	return FileDigest{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

// appendSectionBlobs lists each blob once, keeping only keys the section may
// carry: a reference outside them (provider artwork, for example) can be
// fetched again.
func appendSectionBlobs(spec sectionSpec, keys []string, refs []blobRef, add []blobRef) ([]string, []blobRef) {
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	for _, ref := range add {
		if seen[ref.key] {
			continue
		}
		if _, ok := spec.blobStore(ref.key); !ok {
			continue
		}
		seen[ref.key] = true
		keys = append(keys, ref.key)
		refs = append(refs, ref)
	}
	return keys, refs
}

func readSettingRows(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT key, value FROM server_settings`)
	if err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

// addSettings writes every stored setting the restore table offers, as
// stored. Server-specific and unclassified keys never leave the server.
func (b *restoreSetBuild) addSettings(spec sectionSpec, stored map[string]string) error {
	doc := SettingsDocument{Settings: []SettingEntry{}}
	for key, value := range stored {
		if _, ok := RestorableSetting(key); !ok {
			continue
		}
		doc.Settings = append(doc.Settings, SettingEntry{Key: key, Value: value, Encrypted: secret.IsEncrypted(value)})
	}
	sort.Slice(doc.Settings, func(i, j int) bool { return doc.Settings[i].Key < doc.Settings[j].Key })
	return b.addDocument(spec, spec.document, doc, len(doc.Settings), nil)
}

// storedKey reports whether a stored artwork path is a blob key rather than
// empty, a placeholder, or a URL.
func storedKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "-" && !strings.Contains(value, "://")
}

func (b *restoreSetBuild) addLibraries(ctx context.Context, tx pgx.Tx, spec sectionSpec) error {
	rows, err := tx.Query(ctx, `
		SELECT id, name, type, COALESCE(enabled, true), metadata_language, chapter_thumbnails_enabled, intro_detection_enabled,
			auto_translate_metadata, realtime_monitoring, trickplay_enabled, trailer_kinds, collection_ungrouped_sort_order, poster_path,
			box_sets_enabled, box_set_min_movies, box_sets_in_search,
			COALESCE((SELECT array_agg(path ORDER BY path) FROM media_folder_paths p WHERE p.media_folder_id = f.id), '{}')
		FROM media_folders f ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	doc := LibrariesDocument{Libraries: []LibraryEntry{}}
	var blobs []blobRef
	for rows.Next() {
		var l LibraryEntry
		if err := rows.Scan(&l.ID, &l.Name, &l.Type, &l.Enabled, &l.MetadataLanguage, &l.ChapterThumbnailsEnabled, &l.IntroDetectionEnabled,
			&l.AutoTranslateMetadata, &l.RealtimeMonitoring, &l.TrickplayEnabled, &l.TrailerKinds, &l.CollectionUngroupedSortOrder, &l.Poster,
			&l.BoxSetsEnabled, &l.BoxSetMinMovies, &l.BoxSetsInSearch, &l.Paths); err != nil {
			return err
		}
		if len(l.Paths) == 0 {
			continue // a library with no folders cannot be matched or recreated
		}
		if l.TrailerKinds == nil {
			l.TrailerKinds = []string{}
		}
		if _, ok := spec.blobStore(l.Poster); ok && storedKey(l.Poster) {
			blobs = append(blobs, blobRef{store: storeAssets, key: l.Poster, source: sourceLibraryPoster})
		} else {
			l.Poster = ""
		}
		doc.Libraries = append(doc.Libraries, l)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return b.addDocument(spec, spec.document, doc, len(doc.Libraries), blobs)
}

// addPlugins lists each installed (non-builtin) plugin with the digest of its
// stored archive, computed in the snapshot, so the index can be written
// before the archive bytes are read.
func (b *restoreSetBuild) addPlugins(ctx context.Context, tx pgx.Tx, spec sectionSpec) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (i.plugin_id) i.id, i.plugin_id, i.version, i.enabled, length(a.archive_bytes), encode(sha256(a.archive_bytes), 'hex')
		FROM plugin_installations i JOIN plugin_archives a ON a.plugin_installation_id = i.id
		WHERE i.kind = 'plugin'
		ORDER BY i.plugin_id, i.id`)
	if err != nil {
		return err
	}
	type found struct {
		plugin builtPlugin
		doc    PluginDocument
	}
	var list []found
	for rows.Next() {
		var f found
		if err := rows.Scan(&f.plugin.installationID, &f.doc.PluginID, &f.doc.Version, &f.doc.Enabled, &f.plugin.archive.Size, &f.plugin.archive.SHA256); err != nil {
			rows.Close()
			return err
		}
		if !validItemSegment(f.doc.PluginID) {
			continue
		}
		list = append(list, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	si := SectionIndex{ID: spec.id, Count: len(list), Files: []FileDigest{}, Blobs: []string{}}
	for _, f := range list {
		f.plugin.id = f.doc.PluginID
		f.doc.InstallationID = f.plugin.installationID
		f.plugin.archive.Path = spec.itemDir + f.plugin.id + "/" + pluginArchiveName
		f.doc.Archive = f.plugin.archive
		configs, err := pluginConfigs(ctx, tx, f.plugin.installationID)
		if err != nil {
			return err
		}
		f.doc.Configs = configs
		data, err := json.Marshal(f.doc)
		if err != nil {
			return err
		}
		docPath := spec.itemDir + f.plugin.id + "/" + pluginDocumentName
		si.Files = append(si.Files, b.addFile(docPath, data), f.plugin.archive)
		b.plugins = append(b.plugins, f.plugin)
	}
	b.index.Sections = append(b.index.Sections, si)
	return nil
}

func pluginConfigs(ctx context.Context, tx pgx.Tx, installationID int) ([]PluginConfigEntry, error) {
	rows, err := tx.Query(ctx, `SELECT config_key, config_value::text FROM plugin_runtime_configs WHERE plugin_installation_id = $1 ORDER BY config_key`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PluginConfigEntry{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out = append(out, PluginConfigEntry{Key: key, Value: json.RawMessage(value)})
	}
	return out, rows.Err()
}

func (b *restoreSetBuild) addCollections(ctx context.Context, tx pgx.Tx, spec sectionSpec, settings map[string]string) error {
	doc := CollectionsDocument{Collections: []CollectionEntry{}, TemplateCollections: []TemplateCollectionEntry{}, ItemArtwork: []ItemArtworkEntry{}, Branding: []BrandingEntry{}}
	var blobs []blobRef
	rows, err := b.collectionRows(ctx, tx)
	if err != nil {
		return err
	}
	carry := func(field *string) {
		if !storedKey(*field) {
			return
		}
		if _, ok := spec.blobStore(*field); ok {
			blobs = append(blobs, blobRef{store: storeAssets, key: *field, ladder: true, source: "collection artwork"})
		} else if strings.HasPrefix(*field, "collection-images/") {
			// Not a key the store accepts: nothing to carry or restore.
			*field = ""
		}
	}
	for _, r := range rows {
		switch r.mode {
		case managementManual:
			c := r.entry
			carry(&c.Poster)
			carry(&c.Backdrop)
			doc.Collections = append(doc.Collections, c)
		case managementTemplateBundle:
			t, ok := templateEntryOf(r)
			if !ok {
				continue
			}
			// Only an image the admin set travels: a template poster comes
			// back from the template, and a collage is generated again.
			if !t.PosterFromTemplate && !t.PosterAutoGenerated {
				carry(&t.Poster)
			} else if storedKey(t.Poster) {
				t.Poster, t.PosterThumbhash = "", ""
			}
			carry(&t.Backdrop)
			doc.TemplateCollections = append(doc.TemplateCollections, t)
		}
	}
	artwork, err := readItemArtwork(ctx, tx)
	if err != nil {
		return err
	}
	for _, a := range artwork {
		if _, ok := spec.blobStore(a.Path); !ok {
			continue
		}
		doc.ItemArtwork = append(doc.ItemArtwork, a)
		blobs = append(blobs, blobRef{store: storeAssets, key: a.Path, ladder: true, source: "uploaded artwork"})
	}
	for _, key := range branding.AssetSettingKeys() {
		ref := strings.TrimSpace(settings[key])
		object := branding.AssetObjectKey(key, ref)
		if object == "" {
			continue
		}
		entry := BrandingEntry{Key: key, Ref: ref, Blob: object}
		if _, ok := spec.blobStore(object); !ok || validBrandingEntry(entry) != nil {
			continue
		}
		doc.Branding = append(doc.Branding, entry)
		blobs = append(blobs, blobRef{store: storeAssets, key: object, source: "branding asset"})
	}
	count := len(doc.Collections) + len(doc.TemplateCollections) + len(doc.ItemArtwork) + len(doc.Branding)
	return b.addDocument(spec, spec.document, doc, count, blobs)
}

// Collection management modes (library_collections.management_mode).
const (
	managementManual         = "manual"
	managementSection        = "section"
	managementTemplateBundle = "template_bundle"
)

// collectionRow is one admin collection as the snapshot holds it, with how
// it is managed.
type collectionRow struct {
	entry              CollectionEntry
	mode, source, key  string
	posterFromTemplate bool
}

// collectionRows reads every admin collection once per build: the
// collections section carries the manual and template-bundle ones, and the
// home sections section the ones a section manages.
func (b *restoreSetBuild) collectionRows(ctx context.Context, tx pgx.Tx) ([]collectionRow, error) {
	if b.collections != nil {
		return b.collections, nil
	}
	rows, err := readCollections(ctx, tx)
	if err != nil {
		return nil, err
	}
	b.collections = rows
	if b.collections == nil {
		b.collections = []collectionRow{}
	}
	return b.collections, nil
}

// templateEntryOf reads a template-bundle collection's template and bundle
// from its management key; false for a key that does not parse.
func templateEntryOf(r collectionRow) (TemplateCollectionEntry, bool) {
	bundle, template, _, ok := ParseTemplateManagementKey(r.key)
	if !ok || bundle != r.source {
		return TemplateCollectionEntry{}, false
	}
	c := r.entry
	return TemplateCollectionEntry{
		ID: c.ID, BundleID: bundle, TemplateID: template, ManagementKey: r.key, LibraryID: c.LibraryID, Libraries: c.Libraries,
		Title: c.Title, Description: c.Description, Visibility: c.Visibility, Featured: c.Featured, SortOrder: c.SortOrder,
		SortConfig: c.SortConfig, SourceURL: c.SourceURL, SourceConfig: c.SourceConfig, SyncSchedule: c.SyncSchedule,
		Poster: c.Poster, PosterThumbhash: c.PosterThumbhash, PosterAutoGenerated: c.PosterAutoGenerated, PosterFromTemplate: r.posterFromTemplate,
		Backdrop: c.Backdrop, BackdropThumbhash: c.BackdropThumbhash,
	}, true
}

// TemplateManagementKey is the management key a template bundle gives the
// collection it creates from a template for a library.
func TemplateManagementKey(bundleID, templateID string, libraryID int) string {
	return fmt.Sprintf("%s:%s:%s:%d", bundleID, templateID, pageScopeLibrary, libraryID)
}

// ParseTemplateManagementKey reads TemplateManagementKey.
func ParseTemplateManagementKey(key string) (bundleID, templateID string, libraryID int, ok bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 4 || parts[2] != pageScopeLibrary || parts[0] == "" || parts[1] == "" {
		return "", "", 0, false
	}
	id, err := strconv.Atoi(parts[3])
	if err != nil || id <= 0 {
		return "", "", 0, false
	}
	return parts[0], parts[1], id, true
}

// readCollections reads every admin collection with its scope and groups,
// and its items unless a template bundle created it: those sync their items
// from the template's source again.
func readCollections(ctx context.Context, tx pgx.Tx) ([]collectionRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, library_id, slug, title, description, collection_type, visibility, sort_order, featured,
			poster_url, poster_thumbhash, poster_auto_generated, backdrop_url, backdrop_thumbhash, source_url,
			query_definition::text, sort_config::text, source_config::text, sync_schedule,
			management_mode, management_source, management_key, poster_from_template
		FROM library_collections ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []collectionRow
	index := map[string]int{}
	for rows.Next() {
		var r collectionRow
		c := &r.entry
		var query, sortConfig, source string
		if err := rows.Scan(&c.ID, &c.LibraryID, &c.Slug, &c.Title, &c.Description, &c.CollectionType, &c.Visibility, &c.SortOrder, &c.Featured,
			&c.Poster, &c.PosterThumbhash, &c.PosterAutoGenerated, &c.Backdrop, &c.BackdropThumbhash, &c.SourceURL,
			&query, &sortConfig, &source, &c.SyncSchedule, &r.mode, &r.source, &r.key, &r.posterFromTemplate); err != nil {
			rows.Close()
			return nil, err
		}
		if !validItemSegment(c.ID) {
			continue
		}
		c.QueryDefinition, c.SortConfig, c.SourceConfig = json.RawMessage(query), json.RawMessage(sortConfig), json.RawMessage(source)
		c.Libraries, c.Items = []CollectionLib{}, []string{}
		index[c.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `
		SELECT l.collection_id, l.library_id, COALESCE(g.slug, ''), COALESCE(g.name, ''), COALESCE(g.default_sort_mode, '')
		FROM library_collection_libraries l LEFT JOIN library_collection_groups g ON g.id = l.group_id AND g.kind = 'regular'
		ORDER BY l.collection_id, l.library_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var lib CollectionLib
		if err := rows.Scan(&id, &lib.LibraryID, &lib.GroupSlug, &lib.GroupName, &lib.GroupSort); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].entry.Libraries = append(out[i].entry.Libraries, lib)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `
		SELECT i.collection_id, i.media_item_id FROM library_collection_items i
		JOIN library_collections c ON c.id = i.collection_id
		WHERE c.management_mode <> 'template_bundle'
		ORDER BY i.collection_id, i.position, i.media_item_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, item string
		if err := rows.Scan(&id, &item); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].entry.Items = append(out[i].entry.Items, item)
		}
	}
	return out, rows.Err()
}

// addHomeSections carries every admin page section as it is now, with the
// collection each manages.
func (b *restoreSetBuild) addHomeSections(ctx context.Context, tx pgx.Tx, spec sectionSpec) error {
	collections, err := b.collectionRows(ctx, tx)
	if err != nil {
		return err
	}
	managed := map[string]collectionRow{}
	for _, r := range collections {
		if r.mode == managementSection {
			managed[r.entry.ID] = r
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT id, scope, library_id, position, section_type, title, featured, item_limit, config::text, enabled
		FROM page_sections ORDER BY scope, library_id NULLS FIRST, position, id`)
	if err != nil {
		return err
	}
	doc := HomeSectionsDocument{Sections: []HomeSectionEntry{}}
	for rows.Next() {
		var e HomeSectionEntry
		var library *int64
		var config string
		if err := rows.Scan(&e.ID, &e.Scope, &library, &e.Position, &e.SectionType, &e.Title, &e.Featured, &e.ItemLimit, &config, &e.Enabled); err != nil {
			rows.Close()
			return err
		}
		if !validItemSegment(e.ID) {
			continue
		}
		if library != nil {
			id := int(*library)
			e.LibraryID = &id
		}
		e.Config = json.RawMessage(config)
		if r, ok := managed[sectionCollectionID(e.Config)]; ok {
			c := r.entry
			// Its images are not carried: a section's collection shows a
			// generated collage or a provider image.
			for _, field := range []*string{&c.Poster, &c.Backdrop} {
				if storedKey(*field) {
					*field = ""
				}
			}
			e.Collection = &SectionCollectionEntry{CollectionEntry: c, ManagementSource: r.source, ManagementKey: r.key}
		}
		doc.Sections = append(doc.Sections, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return b.addDocument(spec, spec.document, doc, len(doc.Sections), nil)
}

// sectionCollectionID is the collection a section's configuration names.
func sectionCollectionID(config json.RawMessage) string {
	var c struct {
		ID string `json:"library_collection_id"`
	}
	if len(config) == 0 || json.Unmarshal(config, &c) != nil {
		return ""
	}
	return c.ID
}

// readItemArtwork reads the uploaded images items and seasons show: the
// slots an admin can upload to.
func readItemArtwork(ctx context.Context, tx pgx.Tx) ([]ItemArtworkEntry, error) {
	var out []ItemArtworkEntry
	type slot struct{ image, path, source, thumb string }
	read := func(sql, targetType string, slots []slot, parent bool) error {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var contentID, parentID, title, itemType string
			values := make([]string, len(slots)*3)
			dest := []any{&contentID, &parentID, &title, &itemType}
			for i := range values {
				dest = append(dest, &values[i])
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			for i, s := range slots {
				path, source, thumb := values[i*3], values[i*3+1], values[i*3+2]
				if !strings.HasPrefix(strings.ToLower(source), "upload://") || !storedKey(path) {
					continue
				}
				target := targetType
				if target == "" {
					target = itemType
				}
				entry := ItemArtworkEntry{TargetType: target, ContentID: contentID, Title: title, ImageType: s.image, Path: path, Source: source, Thumbhash: thumb}
				if parent {
					entry.ParentContentID = parentID
				} else {
					entry.ParentContentID = contentID
				}
				out = append(out, entry)
			}
		}
		return rows.Err()
	}
	itemSlots := []slot{{"poster", "poster_path", "poster_source_path", "poster_thumbhash"}, {"backdrop", "backdrop_path", "backdrop_source_path", "backdrop_thumbhash"}, {"logo", "logo_path", "logo_source_path", "''"}}
	seasonSlots := itemSlots[:2]
	columns := func(table string, slots []slot) string {
		var parts []string
		for _, s := range slots {
			thumb := s.thumb
			if thumb != "''" {
				thumb = table + "." + thumb
			}
			parts = append(parts, fmt.Sprintf("COALESCE(%[1]s.%[2]s, ''), COALESCE(%[1]s.%[3]s, ''), COALESCE(%[4]s, '')", table, s.path, s.source, thumb))
		}
		return strings.Join(parts, ", ")
	}
	where := func(table string, slots []slot) string {
		var parts []string
		for _, s := range slots {
			parts = append(parts, fmt.Sprintf("lower(COALESCE(%s.%s, '')) LIKE 'upload://%%'", table, s.source))
		}
		return strings.Join(parts, " OR ")
	}
	if err := read(`SELECT m.content_id, '', COALESCE(m.title, ''), m.type, `+columns("m", itemSlots)+` FROM media_items m WHERE `+where("m", itemSlots)+` ORDER BY m.content_id`, "", itemSlots, false); err != nil {
		return nil, fmt.Errorf("reading uploaded item artwork: %w", err)
	}
	if err := read(`SELECT s.content_id, s.series_id, COALESCE(m.title, '') || ' season ' || s.season_number, 'season', `+columns("s", seasonSlots)+`
		FROM seasons s LEFT JOIN media_items m ON m.content_id = s.series_id WHERE `+where("s", seasonSlots)+` ORDER BY s.content_id`, "season", seasonSlots, true); err != nil {
		return nil, fmt.Errorf("reading uploaded season artwork: %w", err)
	}
	return out, nil
}

// writeSnapshotPart writes the index, the documents, and each plugin archive
// read from the snapshot. A stored archive that no longer matches the digest
// the index recorded fails the backup.
func (b *restoreSetBuild) writeSnapshotPart(ctx context.Context, tw *tar.Writer, tx pgx.Tx) error {
	sort.SliceStable(b.index.Sections, func(i, j int) bool {
		return sectionOrder(b.index.Sections[i].ID) < sectionOrder(b.index.Sections[j].ID)
	})
	index, err := json.Marshal(b.index)
	if err != nil {
		return err
	}
	if err := writeTarFile(tw, restoreSetIndexName, index, b.index.CreatedAt); err != nil {
		return err
	}
	for _, d := range b.docs {
		if err := writeTarFile(tw, d.path, d.data, b.index.CreatedAt); err != nil {
			return err
		}
	}
	for _, p := range b.plugins {
		if err := ctx.Err(); err != nil {
			return err
		}
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT archive_bytes FROM plugin_archives WHERE plugin_installation_id = $1`, p.installationID).Scan(&data); err != nil {
			return fmt.Errorf("reading the %s plugin archive: %w", p.id, err)
		}
		if sum := sha256.Sum256(data); int64(len(data)) != p.archive.Size || hex.EncodeToString(sum[:]) != p.archive.SHA256 {
			return fmt.Errorf("the %s plugin archive changed while it was read", p.id)
		}
		if err := writeTarFile(tw, p.archive.Path, data, b.index.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func sectionOrder(id string) int {
	for i, s := range restoreSections {
		if s.id == id {
			return i
		}
	}
	return len(restoreSections)
}

func writeTarFile(tw *tar.Writer, name string, data []byte, at time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: at, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// writeBlobPart copies the section blobs after the snapshot closed. A key
// gone by then is left out; the reader reports it as missing.
func (b *restoreSetBuild) writeBlobPart(ctx context.Context, tw *tar.Writer, stores blobStores) error {
	written := map[string]bool{}
	var result blobCopyResult
	for _, ref := range b.blobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		store := stores[ref.store]
		if store == nil {
			continue
		}
		found, err := copyBlobAs(ctx, tw, store, restoreSetBlobDir+ref.key, ref.key, written, &result)
		if err != nil {
			return err
		}
		if !found || !ref.ladder {
			continue
		}
		siblings, err := ladderSiblings(ctx, store, ref.key)
		if err != nil {
			return err
		}
		for _, key := range siblings {
			if _, err := copyBlobAs(ctx, tw, store, restoreSetBlobDir+key, key, written, &result); err != nil {
				return err
			}
		}
	}
	return nil
}
