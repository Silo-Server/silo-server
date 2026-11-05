package backup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// PreviewItem is one item of a section, as it compares with this server.
type PreviewItem struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Status is how the item compares with this server; Selectable whether
	// it can be chosen; Reason and Message why not, or what restoring it
	// will and will not do.
	Status     string `json:"status"`
	Selectable bool   `json:"selectable"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	// Requires lists items ("libraries:3") that must be restored with this
	// one: a collection whose library this server does not have yet.
	Requires        []string            `json:"requires,omitempty"`
	RestartRequired bool                `json:"restart_required"`
	ExternalService bool                `json:"external_service"`
	MissingBlobs    []string            `json:"missing_blobs,omitempty"`
	Settings        []PreviewSetting    `json:"settings,omitempty"`
	Plugin          *PreviewPlugin      `json:"plugin,omitempty"`
	Library         *PreviewLibrary     `json:"library,omitempty"`
	Collection      *PreviewCollected   `json:"collection,omitempty"`
	HomeSection     *PreviewHomeSection `json:"home_section,omitempty"`
}

// PreviewSetting is one setting on a settings page. Values of encrypted and
// sensitive settings are never returned.
type PreviewSetting struct {
	Key             string       `json:"key"`
	Class           SettingClass `json:"class"`
	Status          string       `json:"status"`
	Masked          bool         `json:"masked"`
	BackupValue     string       `json:"backup_value"`
	CurrentValue    string       `json:"current_value"`
	RestartRequired bool         `json:"restart_required"`
}

type PreviewPlugin struct {
	Version          string   `json:"version"`
	InstalledVersion string   `json:"installed_version,omitempty"`
	Enabled          bool     `json:"enabled"`
	ConfigKeys       []string `json:"config_keys"`
	// ConfigRestorable is false when the configuration was encrypted with
	// another SECRET_KEY: the plugin installs without it.
	ConfigRestorable bool `json:"config_restorable"`
}

type PreviewLibrary struct {
	Type         string   `json:"type"`
	Paths        []string `json:"paths"`
	LocalID      *int     `json:"local_id,omitempty"`
	MissingPaths []string `json:"missing_paths,omitempty"`
}

// PreviewCollected describes a collection or artwork item.
type PreviewCollected struct {
	LibraryIDs []int `json:"library_ids,omitempty"`
	// Items is how many items the backup's collection holds, MissingItems
	// how many of those this server's collection lacks, and UnknownItems
	// how many are not in this server's catalog (left out).
	Items        int    `json:"items"`
	MissingItems int    `json:"missing_items"`
	UnknownItems int    `json:"unknown_items"`
	LocalID      string `json:"local_id,omitempty"`
	// TemplateID and BundleID name a template collection's template and
	// bundle; Changes lists the admin's changes a restore writes.
	TemplateID string   `json:"template_id,omitempty"`
	BundleID   string   `json:"bundle_id,omitempty"`
	Changes    []string `json:"changes,omitempty"`
}

// planner compares a restore set with this server, through q: the pool for
// a preview, the item's transaction while restoring.
type planner struct {
	s          *Service
	rs         *RestoreSet
	q          queryer
	keyMatches bool
	// authenticated is set when the backup's manifest verified here.
	authenticated bool
	// claimed maps "kind:local id" to the item of this restore that wrote
	// it; nil for a preview.
	claimed map[string]string
}

func (s *Service) newPlanner(rs *RestoreSet, q queryer) *planner {
	return &planner{s: s, rs: rs, q: q, keyMatches: s.keyMatches(rs), authenticated: !rs.unverified}
}

// keyMatches reports whether rs was written under this server's SECRET_KEY,
// so its encrypted values can be read here.
func (s *Service) keyMatches(rs *RestoreSet) bool {
	return rs.Index.KeyCheck != "" && rs.Index.KeyCheck == s.opts.KeyCheck
}

func libraryRef(id int) string { return SectionLibraries + ":" + strconv.Itoa(id) }

// ---- Libraries ----

type localLibrary struct {
	id    int
	name  string
	paths []string
}

func readLocalLibraries(ctx context.Context, q queryer) ([]localLibrary, error) {
	rows, err := q.Query(ctx, `
		SELECT f.id, f.name, COALESCE(array_agg(p.path ORDER BY p.path) FILTER (WHERE p.path IS NOT NULL), '{}')
		FROM media_folders f LEFT JOIN media_folder_paths p ON p.media_folder_id = f.id
		GROUP BY f.id ORDER BY f.id`)
	if err != nil {
		return nil, fmt.Errorf("reading libraries: %w", err)
	}
	defer rows.Close()
	var out []localLibrary
	for rows.Next() {
		var l localLibrary
		if err := rows.Scan(&l.id, &l.name, &l.paths); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func pathSetKey(paths []string) string {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}

// libraryPlan is one backup library against this server.
type libraryPlan struct {
	entry   LibraryEntry
	item    PreviewItem
	localID int
}

// planLibraries matches each backup library by identical path set only: a
// library with the same name and other folders is not this one. A library
// sharing a folder with another library here, or with a folder this server
// cannot open, cannot be restored.
func (p *planner) planLibraries(ctx context.Context) ([]libraryPlan, error) {
	locals, err := readLocalLibraries(ctx, p.q)
	if err != nil {
		return nil, err
	}
	bySet := map[string]localLibrary{}
	byPath := map[string]localLibrary{}
	for _, l := range locals {
		bySet[pathSetKey(l.paths)] = l
		for _, path := range l.paths {
			byPath[path] = l
		}
	}
	var out []libraryPlan
	for _, e := range p.rs.Libraries.Libraries {
		plan := libraryPlan{entry: e, item: PreviewItem{ID: strconv.Itoa(e.ID), Kind: KindLibrary, Label: e.Name,
			Library: &PreviewLibrary{Type: e.Type, Paths: e.Paths}}}
		if e.Poster != "" && !p.rs.HasBlob(e.Poster) {
			plan.item.MissingBlobs = []string{e.Poster}
		}
		if local, ok := bySet[pathSetKey(e.Paths)]; ok {
			plan.localID = local.id
			plan.item.Library.LocalID = &local.id
			plan.item.Status, plan.item.Reason = ItemOnThisServer, ReasonSame
			out = append(out, plan)
			continue
		}
		plan.item.Status = ItemNotOnThisServer
		if check := p.s.targets.CheckLibrary; check != nil {
			if err := check(libraryInput(e)); err != nil {
				plan.item.Reason, plan.item.Message = ReasonUnreadable, refusedMessage(err)
				out = append(out, plan)
				continue
			}
		}
		var inUse string
		for _, path := range e.Paths {
			if local, ok := byPath[path]; ok {
				inUse = local.name
				break
			}
		}
		if inUse != "" {
			plan.item.Reason = ReasonPathInUse
			plan.item.Message = fmt.Sprintf("A folder of this library belongs to the library %q on this server.", inUse)
			out = append(out, plan)
			continue
		}
		for _, path := range e.Paths {
			if p.s.targets.CheckFolder == nil || p.s.targets.CheckFolder(ctx, path) != nil {
				plan.item.Library.MissingPaths = append(plan.item.Library.MissingPaths, path)
			}
		}
		if len(plan.item.Library.MissingPaths) > 0 {
			plan.item.Status, plan.item.Reason = ItemFolderMissing, ReasonFolderMissing
			plan.item.Message = "This server cannot open " + strings.Join(plan.item.Library.MissingPaths, ", ") + "."
			out = append(out, plan)
			continue
		}
		plan.item.Selectable = true
		if costly := costlyLibraryOptions(e); len(costly) > 0 {
			if p.authenticated {
				plan.item.Message = "Restoring it turns on " + joinLabels(costly) + "."
			} else {
				plan.item.Message = "Restored with " + joinLabels(costly) + " off: a backup this server cannot verify does not turn on work that sends library data to outside services or runs for hours. Turn them on in the library's settings."
			}
		}
		out = append(out, plan)
	}
	return out, nil
}

// joinLabels joins labels as a list in a sentence: "a", "a and b",
// "a, b, and c".
func joinLabels(labels []string) string {
	switch len(labels) {
	case 0:
		return ""
	case 1:
		return labels[0]
	case 2:
		return labels[0] + " and " + labels[1]
	}
	return strings.Join(labels[:len(labels)-1], ", ") + ", and " + labels[len(labels)-1]
}

// ---- Plugins ----

type localPlugin struct {
	id      int
	version string
	enabled bool
	configs map[string][]byte
}

func readLocalPlugins(ctx context.Context, q queryer) (map[string]localPlugin, error) {
	rows, err := q.Query(ctx, `SELECT id, plugin_id, version, enabled FROM plugin_installations WHERE kind = 'plugin' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("reading plugins: %w", err)
	}
	out := map[string]localPlugin{}
	byID := map[int]string{}
	for rows.Next() {
		var l localPlugin
		var pluginID string
		if err := rows.Scan(&l.id, &pluginID, &l.version, &l.enabled); err != nil {
			rows.Close()
			return nil, err
		}
		if _, dup := out[pluginID]; dup {
			continue
		}
		l.configs = map[string][]byte{}
		out[pluginID], byID[l.id] = l, pluginID
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT plugin_installation_id, config_key, config_value::text FROM plugin_runtime_configs`)
	if err != nil {
		return nil, fmt.Errorf("reading plugin configuration: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var key, value string
		if err := rows.Scan(&id, &key, &value); err != nil {
			return nil, err
		}
		if pluginID, ok := byID[id]; ok {
			out[pluginID].configs[key] = []byte(value)
		}
	}
	return out, rows.Err()
}

func (p *planner) planPlugins(ctx context.Context) ([]PreviewItem, error) {
	locals, err := readLocalPlugins(ctx, p.q)
	if err != nil {
		return nil, err
	}
	var out []PreviewItem
	for _, doc := range p.rs.Plugins {
		out = append(out, p.planPlugin(doc, locals))
	}
	return out, nil
}

func (p *planner) planPlugin(doc PluginDocument, locals map[string]localPlugin) PreviewItem {
	item := PreviewItem{ID: doc.PluginID, Kind: KindPlugin, Label: doc.PluginID + " " + doc.Version,
		Plugin: &PreviewPlugin{Version: doc.Version, Enabled: doc.Enabled, ConfigKeys: []string{}, ConfigRestorable: p.keyMatches}}
	for _, c := range doc.Configs {
		item.Plugin.ConfigKeys = append(item.Plugin.ConfigKeys, c.Key)
	}
	local, installed := locals[doc.PluginID]
	if !installed {
		item.Status, item.Reason, item.Message = ItemNotOnThisServer, ReasonNeedsInstall, needsInstallMessage(doc)
		return item
	}
	item.Plugin.InstalledVersion = local.version
	if local.version != doc.Version {
		item.Reason = ReasonOtherVersion
		item.Message = fmt.Sprintf("Version %s is installed here. Only its settings are restored, and values that version rejects are left out.", local.version)
	}
	if len(doc.Configs) == 0 {
		item.Status, item.Reason, item.Message = ItemOnThisServer, ReasonSame, ""
		return item
	}
	// A plugin's configuration can name services and the tokens it sends
	// them: it comes only from a backup this server verified.
	if !p.authenticated {
		item.Status, item.Reason, item.Message = ItemDiffers, ReasonUnverifiedBackup,
			"Its configuration can connect to outside services, so it is not restored from a backup this server cannot verify."
		return item
	}
	if !p.keyMatches || p.s.targets.Plugins == nil {
		item.Status, item.Reason = ItemNeedsAnotherKey, ReasonNeedsAnotherKey
		item.Message = "Its configuration was encrypted with another SECRET_KEY and cannot be read here."
		return item
	}
	same := true
	for _, c := range doc.Configs {
		backupValue, err := p.s.targets.Plugins.DecodeStoredConfig(doc.InstallationID, c.Key, c.Value)
		if err != nil {
			item.Status, item.Reason = ItemNeedsAnotherKey, ReasonNeedsAnotherKey
			item.Message = "Its configuration cannot be decrypted with this server's key."
			return item
		}
		stored, ok := local.configs[c.Key]
		if !ok {
			same = false
			continue
		}
		localValue, err := p.s.targets.Plugins.DecodeStoredConfig(local.id, c.Key, stored)
		if err != nil || !plugins.SameConfig(localValue, backupValue) {
			same = false
		}
	}
	if same {
		item.Status, item.Reason, item.Message = ItemOnThisServer, ReasonSame, ""
		return item
	}
	item.Status, item.Selectable = ItemDiffers, true
	return item
}

// ---- Collections and artwork ----

// collectionInput is the fields of c an editor's create command checks.
func collectionInput(c CollectionEntry) catalog.CreateLibraryCollectionInput {
	return catalog.CreateLibraryCollectionInput{
		LibraryID: c.LibraryID, Slug: c.Slug, Title: c.Title, Description: c.Description, CollectionType: c.CollectionType,
		Visibility: c.Visibility, SortOrder: c.SortOrder, Featured: c.Featured, SourceURL: c.SourceURL,
		QueryDefinition: c.QueryDefinition, SortConfig: c.SortConfig, SourceConfig: c.SourceConfig, SyncSchedule: c.SyncSchedule,
	}
}

// checkCollection checks a collection the restore would write: its
// source as the editors canonicalize one (collectionutil
// .CanonicalCollectionSource), and CheckCollection when this server has it.
func (p *planner) checkCollection(in catalog.CreateLibraryCollectionInput) error {
	if _, _, err := collectionutil.CanonicalCollectionSource(in.CollectionType, in.SourceURL, in.SourceConfig); err != nil {
		return err
	}
	if p.s.targets.CheckCollection == nil {
		return nil
	}
	return p.s.targets.CheckCollection(in)
}

// truncateText cuts s to at most limit bytes on a rune boundary.
func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "")
}

// refusedMessage says why an editor refuses an entry.
func refusedMessage(err error) string {
	return truncateText("It cannot be restored: "+err.Error(), 500)
}

// collectionPlan is one backup collection against this server.
type collectionPlan struct {
	item    PreviewItem
	scope   []int
	localID string
	missing []string
	// ungrouped counts memberships of the collection here that have no
	// group while the backup's membership in that library had one.
	ungrouped int
}

// planCollection maps a collection's libraries, then matches it by name
// within its mapped library. restorable lists backup libraries a restore
// could still create, which a collection may require.
func (p *planner) planCollection(ctx context.Context, c CollectionEntry, libMap map[int]int, restorable map[int]bool) (collectionPlan, error) {
	plan := collectionPlan{item: PreviewItem{ID: collectionItemID(c.ID), Kind: KindCollection, Label: c.Title,
		Collection: &PreviewCollected{Items: len(c.Items)}}}
	for _, key := range []string{c.Poster, c.Backdrop} {
		if strings.HasPrefix(key, "collection-images/") && !p.rs.HasBlob(key) {
			plan.item.MissingBlobs = append(plan.item.MissingBlobs, key)
		}
	}
	if err := p.checkCollection(collectionInput(c)); err != nil {
		plan.item.Status, plan.item.Reason, plan.item.Message = ItemNotOnThisServer, ReasonUnreadable, refusedMessage(err)
		return plan, nil
	}
	libraries := []int{c.LibraryID}
	for _, l := range c.Libraries {
		if l.LibraryID != c.LibraryID {
			libraries = append(libraries, l.LibraryID)
		}
	}
	for _, id := range libraries {
		if local, ok := libMap[id]; ok {
			plan.scope = append(plan.scope, local)
			continue
		}
		if restorable[id] {
			plan.item.Requires = append(plan.item.Requires, libraryRef(id))
			continue
		}
		plan.item.Status, plan.item.Reason = ItemNotOnThisServer, ReasonLibraryNotRestorable
		plan.item.Message = "Its library is not on this server and cannot be restored."
		return plan, nil
	}
	plan.item.Collection.LibraryIDs = plan.scope
	known, err := existingItems(ctx, p.q, c.Items)
	if err != nil {
		return plan, err
	}
	plan.item.Collection.UnknownItems = len(c.Items) - len(known)
	if len(plan.item.Requires) > 0 {
		plan.item.Status, plan.item.Selectable = ItemNotOnThisServer, true
		plan.item.Message = "Restored together with its library."
		return plan, nil
	}
	match, ambiguous, err := p.matchCollection(ctx, c, plan.scope[0], libMap)
	if err != nil {
		return plan, err
	}
	switch {
	case ambiguous != "":
		plan.item.Status, plan.item.Reason, plan.item.Message = ItemAmbiguous, ReasonAmbiguousName, ambiguous
		return plan, nil
	case match == "":
		plan.item.Status, plan.item.Selectable = ItemNotOnThisServer, true
		plan.item.Message = p.scheduleNote("It is restored with", c.SyncSchedule)
		for _, id := range c.Items {
			if known[id] {
				plan.missing = append(plan.missing, id)
			}
		}
	default:
		plan.localID = match
		plan.item.Collection.LocalID = match
		have, err := collectionItems(ctx, p.q, match)
		if err != nil {
			return plan, err
		}
		for _, id := range c.Items {
			if known[id] && !have[id] {
				plan.missing = append(plan.missing, id)
			}
		}
		if plan.ungrouped, err = ungroupedMemberships(ctx, p.q, match, c, libMap); err != nil {
			return plan, err
		}
		if len(plan.missing) == 0 && plan.ungrouped == 0 {
			plan.item.Status, plan.item.Reason = ItemOnThisServer, ReasonSame
		} else {
			plan.item.Status, plan.item.Selectable = ItemDiffers, true
			plan.item.Message = fmt.Sprintf("Adds %d items the collection here lacks and puts it back in %d groups.", len(plan.missing), plan.ungrouped)
		}
	}
	plan.item.Collection.MissingItems = len(plan.missing)
	return plan, nil
}

// ungroupedMemberships counts the local collection's memberships that are
// ungrouped where the backup's membership in the mapped library had a group.
func ungroupedMemberships(ctx context.Context, q queryer, collectionID string, c CollectionEntry, libMap map[int]int) (int, error) {
	rows, err := q.Query(ctx, `SELECT library_id FROM library_collection_libraries WHERE collection_id = $1 AND group_id IS NULL`, collectionID)
	if err != nil {
		return 0, fmt.Errorf("reading collection groups: %w", err)
	}
	defer rows.Close()
	ungrouped := map[int]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ungrouped[id] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, l := range c.Libraries {
		if local, ok := libMap[l.LibraryID]; ok && l.GroupSlug != "" && ungrouped[local] {
			n++
		}
	}
	return n, nil
}

func existingItems(ctx context.Context, q queryer, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT content_id FROM media_items WHERE content_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("reading catalog items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func collectionsNamed(ctx context.Context, q queryer, libraryID int, title string) ([]string, error) {
	return collectStringsArgs(ctx, q, `
		SELECT c.id FROM library_collections c
		JOIN library_collection_libraries l ON l.collection_id = c.id AND l.library_id = $1
		WHERE lower(c.title) = lower($2) ORDER BY c.id`, libraryID, title)
}

func collectionItems(ctx context.Context, q queryer, collectionID string) (map[string]bool, error) {
	ids, err := collectStringsArgs(ctx, q, `SELECT media_item_id FROM library_collection_items WHERE collection_id = $1`, collectionID)
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

func collectStringsArgs(ctx context.Context, q queryer, sql string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// artworkColumns names an artwork slot's path and source columns.
func artworkColumns(a ItemArtworkEntry) (table, path, source string, ok bool) {
	if catalog.ValidateArtworkSelectionTarget(a.TargetType, a.ImageType) != nil {
		return "", "", "", false
	}
	switch a.TargetType {
	case "season":
		table = "seasons"
	case "episode":
		return "", "", "", false
	default:
		table = "media_items"
	}
	switch a.ImageType {
	case "poster", "backdrop", "logo":
		return table, a.ImageType + "_path", a.ImageType + "_source_path", true
	}
	return "", "", "", false
}

func (p *planner) planArtwork(ctx context.Context, a ItemArtworkEntry) (PreviewItem, error) {
	item := PreviewItem{ID: a.itemID(), Kind: KindItemArtwork, Label: strings.TrimSpace(a.Title + " " + a.ImageType)}
	if !p.rs.HasBlob(a.Path) {
		item.Status, item.Reason, item.MissingBlobs = ItemNotOnThisServer, ReasonFileMissing, []string{a.Path}
		item.Message = "The image was missing when the backup was taken."
		return item, nil
	}
	table, pathColumn, sourceColumn, ok := artworkColumns(a)
	if !ok {
		item.Status, item.Reason = ItemNotOnThisServer, ReasonItemNotOnThisServer
		item.Message = "This server cannot show this image on that item."
		return item, nil
	}
	var path, source string
	err := p.q.QueryRow(ctx, fmt.Sprintf(`SELECT COALESCE(%s, ''), COALESCE(%s, '') FROM %s WHERE content_id = $1`, pathColumn, sourceColumn, table), a.ContentID).Scan(&path, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		item.Status, item.Reason = ItemNotOnThisServer, ReasonItemNotOnThisServer
		item.Message = "The item is not in this server's catalog."
		return item, nil
	}
	if err != nil {
		return item, fmt.Errorf("reading artwork of %s: %w", a.ContentID, err)
	}
	if path == a.Path && source == a.Source {
		return p.sameUnlessMissing(ctx, item, p.rs.blobFamily(a.Path))
	}
	item.Status, item.Selectable = ItemDiffers, true
	return item, nil
}

// sameUnlessMissing is item shown here as it is in the backup: the same,
// unless one of keys, the files the backup carries for it, is missing from
// this server's assets storage. Then restoring it writes them back
// (applier.writeBlob, which never replaces an object) and changes nothing
// else.
func (p *planner) sameUnlessMissing(ctx context.Context, item PreviewItem, keys []string) (PreviewItem, error) {
	missing, err := p.s.missingHere(ctx, storeAssets, keys)
	if err != nil {
		return item, err
	}
	if len(missing) == 0 {
		item.Status, item.Reason = ItemOnThisServer, ReasonSame
		return item, nil
	}
	item.Status, item.Selectable, item.Reason = ItemDiffers, true, ReasonFilesMissingHere
	item.Message = fmt.Sprintf("%d of its files are missing on this server. Restoring writes them back.", len(missing))
	return item, nil
}

// missingHereConcurrency bounds the objects missingHere checks at once.
const missingHereConcurrency = 8

// missingHere lists the keys missing from the named store, checking at
// most missingHereConcurrency at a time. A server without that store has
// none of them.
func (s *Service) missingHere(ctx context.Context, storeName string, keys []string) ([]string, error) {
	store := s.stores[storeName]
	if store == nil {
		return keys, nil
	}
	gone := make([]bool, len(keys))
	errs := make([]error, len(keys))
	sem := make(chan struct{}, missingHereConcurrency)
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			_, err := store.Stat(ctx, key)
			gone[i] = errors.Is(err, blobstore.ErrNotFound)
			if err != nil && !gone[i] {
				errs[i] = fmt.Errorf("checking %s here: %w", key, err)
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var missing []string
	for i, key := range keys {
		if gone[i] {
			missing = append(missing, key)
		}
	}
	return missing, nil
}

func (p *planner) planBranding(ctx context.Context, stored map[string]string, b BrandingEntry) (PreviewItem, error) {
	item := PreviewItem{ID: b.itemID(), Kind: KindBranding, Label: b.Key}
	if !p.rs.HasBlob(b.Blob) {
		item.Status, item.Reason, item.MissingBlobs = ItemNotOnThisServer, ReasonFileMissing, []string{b.Blob}
		item.Message = "The image was missing when the backup was taken."
		return item, nil
	}
	current := strings.TrimSpace(stored[b.Key])
	switch current {
	case b.Ref:
		return p.sameUnlessMissing(ctx, item, []string{b.Blob})
	case "":
		item.Status, item.Selectable = ItemNotOnThisServer, true
	default:
		item.Status, item.Selectable = ItemDiffers, true
	}
	return item, nil
}

// ---- Preview ----

// PreviewSection is one section of a restore set.
type PreviewSection struct {
	ID    string        `json:"id"`
	Count int           `json:"count"`
	Items []PreviewItem `json:"items"`
}

// LibraryMapping is one backup library and the library it is here.
type LibraryMapping struct {
	BackupID   int    `json:"backup_id"`
	Name       string `json:"name"`
	LocalID    *int   `json:"local_id,omitempty"`
	Status     string `json:"status"`
	Selectable bool   `json:"selectable"`
}

// RestorePreview is a backup's restore set compared with this server.
type RestorePreview struct {
	BackupID      string    `json:"backup_id"`
	Digest        string    `json:"digest"`
	FormatVersion int       `json:"format_version"`
	ServerID      string    `json:"server_id"`
	ServerName    string    `json:"server_name"`
	SiloVersion   string    `json:"silo_version"`
	CreatedAt     time.Time `json:"created_at"`
	SameServer    bool      `json:"same_server"`
	KeyMatches    bool      `json:"key_matches"`
	// Authenticated is set when the backup's manifest verified under this
	// server's SECRET_KEY; otherwise a restore needs the admin's
	// confirmation (RestoreRequest.AcceptUnauthenticated).
	Authenticated bool     `json:"authenticated"`
	MissingBlobs  []string `json:"missing_blobs"`
	// NeverRestoredSettings are the server-specific keys and prefixes.
	NeverRestoredSettings []string         `json:"never_restored_settings"`
	Libraries             []LibraryMapping `json:"library_map"`
	Sections              []PreviewSection `json:"sections"`
}

// Preview reads a backup's restore set and compares every item with this
// server. It writes nothing and takes no lock. A backup this server cannot
// authenticate is read only when acceptUnauthenticated confirms it
// (ErrUnauthenticated).
func (s *Service) Preview(ctx context.Context, id string, acceptUnauthenticated bool) (*RestorePreview, error) {
	return s.preview(ctx, restoreSource{backupID: id}, acceptUnauthenticated)
}

func (s *Service) preview(ctx context.Context, src restoreSource, acceptUnauthenticated bool) (*RestorePreview, error) {
	rs, authenticated, err := s.previewSet(ctx, src, acceptUnauthenticated)
	if err != nil {
		return nil, err
	}
	serverID, _ := s.serverID(ctx)
	p := s.newPlanner(rs, s.opts.Pool)
	p.authenticated = authenticated
	out := &RestorePreview{BackupID: src.backupID, Digest: rs.Digest, FormatVersion: rs.Index.Version, ServerID: rs.Index.ServerID,
		ServerName: rs.Index.ServerName, SiloVersion: rs.Index.SiloVersion, CreatedAt: rs.Index.CreatedAt,
		SameServer: rs.Index.ServerID == serverID, KeyMatches: p.keyMatches, Authenticated: authenticated, MissingBlobs: append([]string{}, rs.MissingBlobs...),
		NeverRestoredSettings: ServerSpecificSettings(),
		Libraries:             []LibraryMapping{}, Sections: []PreviewSection{}}
	libraries, err := p.planLibraries(ctx)
	if err != nil {
		return nil, err
	}
	libMap, restorable := map[int]int{}, map[int]bool{}
	for _, l := range libraries {
		m := LibraryMapping{BackupID: l.entry.ID, Name: l.entry.Name, Status: l.item.Status, Selectable: l.item.Selectable}
		if l.localID > 0 {
			libMap[l.entry.ID] = l.localID
			m.LocalID = new(l.localID)
		}
		restorable[l.entry.ID] = l.item.Selectable
		out.Libraries = append(out.Libraries, m)
	}
	counts := map[string]int{}
	for _, si := range rs.Index.Sections {
		counts[si.ID] = si.Count
	}
	for _, spec := range restoreSections {
		if _, ok := counts[spec.id]; !ok {
			continue
		}
		section := PreviewSection{ID: spec.id, Count: counts[spec.id], Items: []PreviewItem{}}
		switch spec.id {
		case SectionLibraries:
			for _, l := range libraries {
				section.Items = append(section.Items, l.item)
			}
		case SectionPlugins:
			items, err := p.planPlugins(ctx)
			if err != nil {
				return nil, err
			}
			section.Items = append(section.Items, items...)
		case SectionSettings:
			plans, err := p.planSettings(ctx)
			if err != nil {
				return nil, err
			}
			section.Items = append(section.Items, settingsPageItems(plans)...)
		case SectionCollections:
			items, err := p.planCollectionsSection(ctx, libMap, restorable)
			if err != nil {
				return nil, err
			}
			section.Items = append(section.Items, items...)
		case SectionHomeSections:
			items, err := p.planHomeSections(ctx, libMap, restorable, out.SameServer)
			if err != nil {
				return nil, err
			}
			section.Items = append(section.Items, items...)
		}
		for _, e := range rs.Unusable {
			if e.Section == spec.id {
				section.Items = append(section.Items, PreviewItem{ID: e.ID, Kind: e.Kind, Label: e.Label, Status: ItemNotOnThisServer,
					Reason: ReasonUnreadable, Message: e.Message, Requires: []string{}, MissingBlobs: []string{}})
			}
		}
		out.Sections = append(out.Sections, section)
	}
	return out, nil
}

func (p *planner) planCollectionsSection(ctx context.Context, libMap map[int]int, restorable map[int]bool) ([]PreviewItem, error) {
	var out []PreviewItem
	for _, c := range p.rs.Collections.Collections {
		plan, err := p.planCollection(ctx, c, libMap, restorable)
		if err != nil {
			return nil, err
		}
		out = append(out, plan.item)
	}
	for _, t := range p.rs.Collections.TemplateCollections {
		plan, err := p.planTemplateCollection(ctx, t, libMap, restorable)
		if err != nil {
			return nil, err
		}
		out = append(out, plan.item)
	}
	for _, a := range p.rs.Collections.ItemArtwork {
		item, err := p.planArtwork(ctx, a)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	stored, err := readStoredSettings(ctx, p.q)
	if err != nil {
		return nil, err
	}
	for _, b := range p.rs.Collections.Branding {
		item, err := p.planBranding(ctx, stored, b)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}
