package backup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/secret"
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
	// claimed maps "kind:local id" to the item of this restore that wrote
	// it; nil for a preview.
	claimed map[string]string
}

func (s *Service) newPlanner(rs *RestoreSet, q queryer) *planner {
	return &planner{s: s, rs: rs, q: q, keyMatches: s.keyMatches(rs)}
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
		out = append(out, plan)
	}
	return out, nil
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
		item.Status, item.Selectable = ItemNotOnThisServer, true
		item.Message = "Installs the plugin from the backup, disabled until its configuration is applied."
		if len(doc.Configs) > 0 && !p.keyMatches {
			item.Reason = ReasonConfigNeedsKey
			item.Message = "Installs without its configuration, which was encrypted with another SECRET_KEY. The plugin stays disabled."
		}
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

// ---- Settings ----

func readStoredSettings(ctx context.Context, q queryer) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT key, value FROM server_settings`)
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

// settingPlan is one backup setting against this server: its plaintext when
// readable.
type settingPlan struct {
	preview PreviewSetting
	page    string
	value   string
}

// planSettings compares each setting this server's table offers. An
// encrypted value is read only with a matching key; otherwise it needs
// another key and is never written.
func (p *planner) planSettings(ctx context.Context) ([]settingPlan, error) {
	stored, err := readStoredSettings(ctx, p.q)
	if err != nil {
		return nil, err
	}
	cipher := p.s.opts.Cipher
	var out []settingPlan
	for _, e := range p.rs.Settings.Settings {
		rule, ok := RestorableSetting(e.Key)
		if !ok {
			continue
		}
		plan := settingPlan{page: rule.page, preview: PreviewSetting{Key: e.Key, Class: rule.class, RestartRequired: config.RestartRequired(e.Key),
			Masked: e.Encrypted || catalog.SensitiveSettingKeys[e.Key]}}
		value := e.Value
		if e.Encrypted {
			if !p.keyMatches || cipher == nil {
				plan.preview.Status = ItemNeedsAnotherKey
				out = append(out, plan)
				continue
			}
			if value, err = cipher.Decrypt(e.Value, secret.SettingsAAD(e.Key)); err != nil {
				plan.preview.Status = ItemNeedsAnotherKey
				out = append(out, plan)
				continue
			}
		}
		plan.value = value
		current, have := stored[e.Key]
		if have && cipher != nil {
			if plain, err := cipher.DecryptIfEncrypted(current, secret.SettingsAAD(e.Key)); err == nil {
				current = plain
			} else {
				have = false
			}
		}
		if have && current == value {
			plan.preview.Status = ItemOnThisServer
		} else {
			plan.preview.Status = ItemDiffers
		}
		if !plan.preview.Masked {
			plan.preview.BackupValue, plan.preview.CurrentValue = value, current
		}
		out = append(out, plan)
	}
	return out, nil
}

// settingsPageItems groups settings by page in display order.
func settingsPageItems(plans []settingPlan) []PreviewItem {
	byPage := map[string][]settingPlan{}
	for _, plan := range plans {
		byPage[plan.page] = append(byPage[plan.page], plan)
	}
	var out []PreviewItem
	for _, page := range SettingsPages() {
		list := byPage[page.ID]
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].preview.Key < list[j].preview.Key })
		item := PreviewItem{ID: page.ID, Kind: KindSettingsPage, Label: page.Title, Status: ItemOnThisServer, Reason: ReasonSame}
		differs, needsKey := 0, 0
		for _, plan := range list {
			item.Settings = append(item.Settings, plan.preview)
			switch plan.preview.Status {
			case ItemDiffers:
				differs++
				item.RestartRequired = item.RestartRequired || plan.preview.RestartRequired
				item.ExternalService = item.ExternalService || plan.preview.Class == SettingExternalService
			case ItemNeedsAnotherKey:
				needsKey++
			}
		}
		switch {
		case differs > 0:
			item.Status, item.Reason, item.Selectable = ItemDiffers, "", true
			if needsKey > 0 {
				item.Message = fmt.Sprintf("%d encrypted settings on this page were encrypted with another SECRET_KEY and are left out.", needsKey)
			}
		case needsKey > 0:
			item.Status, item.Reason = ItemNeedsAnotherKey, ReasonNeedsAnotherKey
			item.Message = "The settings that differ were encrypted with another SECRET_KEY."
		}
		out = append(out, item)
	}
	return out
}

// ---- Collections and artwork ----

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
		item.Status, item.Reason = ItemOnThisServer, ReasonSame
		return item, nil
	}
	item.Status, item.Selectable = ItemDiffers, true
	return item, nil
}

func (p *planner) planBranding(stored map[string]string, b BrandingEntry) PreviewItem {
	item := PreviewItem{ID: b.itemID(), Kind: KindBranding, Label: b.Key}
	if !p.rs.HasBlob(b.Blob) {
		item.Status, item.Reason, item.MissingBlobs = ItemNotOnThisServer, ReasonFileMissing, []string{b.Blob}
		item.Message = "The image was missing when the backup was taken."
		return item
	}
	current := strings.TrimSpace(stored[b.Key])
	switch current {
	case b.Ref:
		item.Status, item.Reason = ItemOnThisServer, ReasonSame
	case "":
		item.Status, item.Selectable = ItemNotOnThisServer, true
	default:
		item.Status, item.Selectable = ItemDiffers, true
	}
	return item
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
	MissingBlobs  []string  `json:"missing_blobs"`
	// NeverRestoredSettings are the server-specific keys and prefixes.
	NeverRestoredSettings []string         `json:"never_restored_settings"`
	Libraries             []LibraryMapping `json:"library_map"`
	Sections              []PreviewSection `json:"sections"`
}

// Preview reads a backup's restore set and compares every item with this
// server. It writes nothing and takes no lock.
func (s *Service) Preview(ctx context.Context, id string) (*RestorePreview, error) {
	return s.preview(ctx, restoreSource{backupID: id})
}

func (s *Service) preview(ctx context.Context, src restoreSource) (*RestorePreview, error) {
	rs, err := s.previewSet(ctx, src)
	if err != nil {
		return nil, err
	}
	serverID, _ := s.serverID(ctx)
	p := s.newPlanner(rs, s.opts.Pool)
	out := &RestorePreview{BackupID: src.backupID, Digest: rs.Digest, FormatVersion: rs.Index.Version, ServerID: rs.Index.ServerID,
		ServerName: rs.Index.ServerName, SiloVersion: rs.Index.SiloVersion, CreatedAt: rs.Index.CreatedAt,
		SameServer: rs.Index.ServerID == serverID, KeyMatches: p.keyMatches, MissingBlobs: append([]string{}, rs.MissingBlobs...),
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
		out = append(out, p.planBranding(stored, b))
	}
	return out, nil
}
