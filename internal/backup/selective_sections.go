package backup

// Admin page sections: the home page's and each library page's, as they
// were. A section is matched by id on the server that wrote it, else by
// type, title, and page with one candidate; a chosen one replaces the
// section's configuration here (it is admin configuration, like a settings
// page) or is created. The collection a section manages travels with it;
// one it only names is mapped through the collections section.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// PreviewHomeSection describes a page section against this server.
type PreviewHomeSection struct {
	Scope       string `json:"scope"`
	LibraryID   *int   `json:"library_id,omitempty"`
	SectionType string `json:"section_type"`
	// LocalID is the matching section here; Changes the fields a restore
	// replaces in it.
	LocalID string   `json:"local_id,omitempty"`
	Changes []string `json:"changes,omitempty"`
}

// sectionPlan is one backup section against this server.
type sectionPlan struct {
	item  PreviewItem
	entry HomeSectionEntry
	// library is the mapped library of a library page.
	library *int
	local   *sections.PageSection
}

// sectionConfigLibraryKeys are the configuration keys naming libraries.
var sectionConfigLibraryKeys = []string{"library_ids", "filter_library_ids", "filter_library_id", "generated_library_id", "library_id"}

// sectionConfigLibraries lists the library ids a configuration names.
func sectionConfigLibraries(config json.RawMessage) []int {
	var doc map[string]any
	if len(config) == 0 || json.Unmarshal(config, &doc) != nil {
		return nil
	}
	var out []int
	add := func(v any) {
		if n, ok := v.(float64); ok && n > 0 {
			out = append(out, int(n))
		}
	}
	for _, key := range sectionConfigLibraryKeys {
		switch v := doc[key].(type) {
		case []any:
			for _, x := range v {
				add(x)
			}
		default:
			add(v)
		}
	}
	return out
}

// remapSectionConfig maps a configuration's libraries and its collection.
// false when a library it names is not mapped.
func remapSectionConfig(config json.RawMessage, libMap map[int]int, collectionID string) (json.RawMessage, bool) {
	mapped, ok := userdata.RemapQueryLibraries(config, mapIn(libMap))
	if !ok {
		return nil, false
	}
	var doc map[string]any
	if len(mapped) == 0 || json.Unmarshal(mapped, &doc) != nil || doc == nil {
		return mapped, true
	}
	for _, key := range []string{"generated_library_id", "library_id"} {
		v, present := doc[key]
		if !present || v == nil {
			continue
		}
		n, isNumber := v.(float64)
		local, found := libMap[int(n)]
		if !isNumber || !found {
			return nil, false
		}
		doc[key] = local
	}
	if collectionID != "" {
		doc["library_collection_id"] = collectionID
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	return out, true
}

func (p *planner) sectionLabel(e HomeSectionEntry) string {
	if e.Scope != pageScopeLibrary || e.LibraryID == nil {
		return e.Title + " (home)"
	}
	if name, ok := p.rs.lookup().libraryNames[*e.LibraryID]; ok {
		return e.Title + " (" + name + ")"
	}
	return e.Title
}

func (p *planner) planHomeSection(ctx context.Context, e HomeSectionEntry, libMap map[int]int, restorable map[int]bool, sameServer bool) (sectionPlan, error) {
	plan := sectionPlan{entry: e, item: PreviewItem{ID: e.itemID(), Kind: KindHomeSection, Label: p.sectionLabel(e),
		HomeSection: &PreviewHomeSection{Scope: e.Scope, SectionType: e.SectionType}}}
	if p.s.targets.CheckSection == nil {
		plan.item.Status, plan.item.Message = ItemNotOnThisServer, "This server cannot restore page sections."
		return plan, nil
	}
	if c := e.Collection; c != nil {
		in := collectionInput(c.CollectionEntry)
		in.ManagementMode, in.ManagementSource, in.ManagementKey = managementSection, c.ManagementSource, c.ManagementKey
		if err := p.checkCollection(in); err != nil {
			plan.item.Status, plan.item.Reason, plan.item.Message = ItemNotOnThisServer, ReasonUnreadable, refusedMessage(err)
			return plan, nil
		}
	}
	libraries := sectionConfigLibraries(e.Config)
	if e.LibraryID != nil {
		libraries = append([]int{*e.LibraryID}, libraries...)
	}
	if c := e.Collection; c != nil {
		// A carried collection is created in its own libraries
		// (restoreManagedCollection), which its config may not name.
		libraries = append(libraries, c.LibraryID)
		libraries = append(libraries, libraryIDsOf(c.Libraries)...)
	}
	required := map[int]bool{}
	for _, id := range libraries {
		if _, ok := libMap[id]; ok {
			continue
		}
		if restorable[id] {
			if !required[id] {
				required[id] = true
				plan.item.Requires = append(plan.item.Requires, libraryRef(id))
			}
			continue
		}
		plan.item.Status, plan.item.Reason = ItemNotOnThisServer, ReasonLibraryNotMapped
		plan.item.Message = "A library it shows is not on this server and cannot be restored."
		return plan, nil
	}
	if e.LibraryID != nil {
		if local, ok := libMap[*e.LibraryID]; ok {
			plan.library = &local
			plan.item.HomeSection.LibraryID = &local
		}
	}
	collectionRef := sectionCollectionID(e.Config)
	if e.Collection == nil && collectionRef != "" {
		local, item, err := p.resolveCollection(ctx, collectionRef, libMap, sameServer)
		if err != nil {
			return plan, err
		}
		switch {
		case local != "":
		case item != "":
			plan.item.Requires = append(plan.item.Requires, SectionCollections+":"+item)
		default:
			plan.item.Status, plan.item.Reason = ItemNotOnThisServer, ReasonCollectionNotMapped
			plan.item.Message = "The collection it shows is neither on this server nor in the backup."
			return plan, nil
		}
	}
	if len(plan.item.Requires) > 0 {
		plan.item.Status, plan.item.Selectable = ItemNotOnThisServer, true
		plan.item.Message = "Restored together with what it shows."
		return plan, nil
	}
	local, ambiguous, err := p.matchSection(ctx, e, plan.library, libMap, sameServer)
	if err != nil {
		return plan, err
	}
	if ambiguous != "" {
		plan.item.Status, plan.item.Reason, plan.item.Message = ItemAmbiguous, ReasonAmbiguousName, ambiguous
		return plan, nil
	}
	plan.local = local
	if local == nil {
		plan.item.Status, plan.item.Selectable, plan.item.Message = ItemNotOnThisServer, true, msgCreated
		return plan, nil
	}
	plan.item.HomeSection.LocalID = local.ID
	changes, err := p.sectionChanges(ctx, plan, libMap, sameServer)
	if err != nil {
		return plan, err
	}
	plan.item.HomeSection.Changes = changes
	if len(changes) == 0 {
		plan.item.Status, plan.item.Reason = ItemOnThisServer, ReasonSame
		return plan, nil
	}
	plan.item.Status, plan.item.Selectable = ItemDiffers, true
	plan.item.Message = "Replaces the section here: " + strings.Join(changes, ", ") + "."
	return plan, nil
}

func readSection(ctx context.Context, q queryer, where string, args ...any) ([]*sections.PageSection, error) {
	rows, err := q.Query(ctx, `SELECT id, scope, library_id, position, section_type, title, featured, item_limit, config::text, enabled
		FROM page_sections WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading page sections: %w", err)
	}
	defer rows.Close()
	var out []*sections.PageSection
	for rows.Next() {
		var s sections.PageSection
		var library *int64
		var config string
		if err := rows.Scan(&s.ID, &s.Scope, &library, &s.Position, &s.SectionType, &s.Title, &s.Featured, &s.ItemLimit, &config, &s.Enabled); err != nil {
			return nil, err
		}
		if library != nil {
			id := int(*library)
			s.LibraryID = &id
		}
		s.Config = json.RawMessage(config)
		out = append(out, &s)
	}
	return out, rows.Err()
}

// sectionChanges lists what replacing the local section would change.
func (p *planner) sectionChanges(ctx context.Context, plan sectionPlan, libMap map[int]int, sameServer bool) ([]string, error) {
	e, local := plan.entry, plan.local
	var changes []string
	if e.SectionType != string(local.SectionType) {
		changes = append(changes, "type")
	}
	if e.Title != local.Title {
		changes = append(changes, "title")
	}
	if e.Position != local.Position {
		changes = append(changes, "position")
	}
	if e.Featured != local.Featured {
		changes = append(changes, "featured")
	}
	if e.ItemLimit != local.ItemLimit {
		changes = append(changes, "item limit")
	}
	if e.Enabled != local.Enabled {
		changes = append(changes, "enabled")
	}
	collection := sectionCollectionID(local.Config)
	if e.Collection == nil && sectionCollectionID(e.Config) != "" {
		resolved, _, err := p.resolveCollection(ctx, sectionCollectionID(e.Config), libMap, sameServer)
		if err != nil {
			return nil, err
		}
		collection = resolved
	}
	if e.Collection != nil && collection != "" {
		// Only a collection the section manages stands for the carried
		// one, as the restore finds it (managedCollectionOf): one the
		// section was pointed at since is replaced by the carried one.
		managed, err := managedCollectionOf(ctx, p.q, local)
		if err != nil {
			return nil, err
		}
		if managed == "" {
			return append(changes, "collection replaced"), nil
		}
	}
	config, ok := remapSectionConfig(e.Config, libMap, collection)
	if !ok || !jsonEqual(config, local.Config) {
		changes = append(changes, "configuration")
	}
	if e.Collection != nil {
		missing, err := p.managedItemsMissing(ctx, e.Collection, collection)
		if err != nil {
			return nil, err
		}
		if missing > 0 {
			changes = append(changes, fmt.Sprintf("%d collection items", missing))
		}
	}
	return changes, nil
}

// managedItemsMissing counts the carried collection's items, in this
// catalog, that the section's collection here lacks.
func (p *planner) managedItemsMissing(ctx context.Context, c *SectionCollectionEntry, localID string) (int, error) {
	known, err := existingItems(ctx, p.q, c.Items)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	if localID != "" {
		if have, err = collectionItems(ctx, p.q, localID); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, id := range c.Items {
		if known[id] && !have[id] {
			n++
		}
	}
	return n, nil
}

// resolveCollection maps a backup collection id to the collection here: a
// manual collection by title in its mapped library, a template collection
// by its remapped management key, and, on the server that wrote the
// backup, a collection the set does not carry by its id. item names the
// collections item that would create it when it is not here.
func (p *planner) resolveCollection(ctx context.Context, backupID string, libMap map[int]int, sameServer bool) (local, item string, err error) {
	if c := indexed(p.rs.Collections.Collections, p.rs.lookup().collectionIDs, backupID); c != nil {
		if mapped, ok := libMap[c.LibraryID]; ok {
			match, _, err := p.matchCollection(ctx, *c, mapped, libMap)
			if err != nil || match != "" {
				return match, "", err
			}
		}
		return "", collectionItemID(c.ID), nil
	}
	if t := indexed(p.rs.Collections.TemplateCollections, p.rs.lookup().templateIDs, backupID); t != nil {
		if mapped, ok := libMap[t.LibraryID]; ok {
			found, err := readTemplateCollection(ctx, p.q, t.BundleID, TemplateManagementKey(t.BundleID, t.TemplateID, mapped))
			if err != nil || found != nil {
				return idOf(found), "", err
			}
		}
		return "", t.itemID(), nil
	}
	if sameServer {
		var id string
		err := p.q.QueryRow(ctx, `SELECT id FROM library_collections WHERE id = $1`, backupID).Scan(&id)
		if err == nil {
			return id, "", nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
	}
	return "", "", nil
}

func idOf(c *localTemplateCollection) string {
	if c == nil {
		return ""
	}
	return c.id
}

func (p *planner) planHomeSections(ctx context.Context, libMap map[int]int, restorable map[int]bool, sameServer bool) ([]PreviewItem, error) {
	var out []PreviewItem
	for _, e := range p.rs.HomeSections.Sections {
		plan, err := p.planHomeSection(ctx, e, libMap, restorable, sameServer)
		if err != nil {
			return nil, err
		}
		if c := e.Collection; c != nil && plan.item.Selectable {
			if note := p.scheduleNote("A collection it creates gets", c.SyncSchedule); note != "" {
				plan.item.Message = strings.TrimSpace(plan.item.Message + " " + note)
			}
		}
		if note := p.seasonalNote(e, p.s.opts.Now()); note != "" && plan.item.Selectable {
			plan.item.Message = strings.TrimSpace(plan.item.Message + " " + note)
		}
		out = append(out, plan.item)
	}
	return out, nil
}

func (a *applier) applyHomeSection(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	return a.withCollectionBudget(id, a.writeHomeSection)(ctx, tx)
}

func (a *applier) writeHomeSection(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	item := ReportItem{Kind: KindHomeSection, Label: id}
	e := indexed(a.rs.HomeSections.Sections, a.rs.lookup().sections, id)
	if e == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such section."), nil
	}
	p := a.planner(tx)
	item.Label = p.sectionLabel(*e)
	libMap := a.libraryMap()
	sameServer := a.rs.Index.ServerID == a.serverID
	plan, err := p.planHomeSection(ctx, *e, libMap, nil, sameServer)
	if err != nil {
		return applied{}, err
	}
	if !plan.item.Selectable {
		return leftOutFor(item, plan.item), nil
	}
	seasonal := e.SectionType == string(sections.SectionSeasonalThemed)
	if seasonal && a.seasonal >= maxRestoredSeasonal {
		return leftOut(item, ReasonOverRestoreBudget, fmt.Sprintf("This restore already wrote %d seasonal sections, as many as one restore may.", maxRestoredSeasonal)), nil
	}
	enabled, note := restoredSectionEnabled(*e, a.rs.unverified, a.s.opts.Now())
	if note != "" {
		item.Notes = append(item.Notes, note)
	}
	section := &sections.PageSection{Scope: e.Scope, LibraryID: plan.library, Position: e.Position, SectionType: sections.SectionType(e.SectionType),
		Title: e.Title, Featured: e.Featured, ItemLimit: e.ItemLimit, Enabled: enabled}
	create := plan.local == nil
	if create {
		if section.ID, err = freeSectionID(ctx, tx, e.ID); err != nil {
			return applied{}, err
		}
	} else {
		section.ID = plan.local.ID
	}
	collection := ""
	switch ref := sectionCollectionID(e.Config); {
	case e.Collection != nil:
		if collection, err = a.restoreManagedCollection(ctx, tx, *e, plan, section.ID, libMap, &item.Notes); err != nil {
			return applied{}, err
		}
		if collection == "" {
			return leftOut(item, ReasonLibraryNotMapped, "Its collection's library is not on this server."), nil
		}
	case ref != "":
		local, _, err := p.resolveCollection(ctx, ref, libMap, sameServer)
		if err != nil {
			return applied{}, err
		}
		if local == "" {
			return leftOut(item, ReasonCollectionNotMapped, "The collection it shows is not on this server."), nil
		}
		collection = local
	}
	config, ok := remapSectionConfig(e.Config, libMap, collection)
	if !ok {
		return leftOut(item, ReasonLibraryNotMapped, "A library it shows is not on this server."), nil
	}
	section.Config = config
	if err := a.s.targets.CheckSection(e.SectionType, e.Scope, plan.library, config, create); err != nil {
		return applied{}, fmt.Errorf("the section is not valid on this server: %w", err)
	}
	if err := sections.WriteSectionInTx(ctx, tx, section, create); err != nil {
		return applied{}, err
	}
	a.claim(claimSection, section.ID, id)
	if seasonal {
		a.seasonal++
	}
	item.Outcome = OutcomeRestored
	if create {
		item.Message = msgCreated
	} else {
		item.Message = plan.item.Message
	}
	out := applied{item: item}
	if e.Collection != nil {
		out.after = a.smartCollectionCreated(e.Collection.CollectionType, collection)
	}
	return out, nil
}

// freeSectionID keeps the backup's section id when no section here has it.
func freeSectionID(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM page_sections WHERE id = $1)`, id).Scan(&taken); err != nil {
		return "", err
	}
	if !taken {
		return id, nil
	}
	return idgen.NextID()
}

// restoreManagedCollection keeps the collection the section here manages,
// adding the backup's items it lacks, or creates the section's collection.
// It returns the collection's id, or "" when its library is not here.
func (a *applier) restoreManagedCollection(ctx context.Context, tx pgx.Tx, e HomeSectionEntry, plan sectionPlan, sectionID string, libMap map[int]int,
	notes *[]string) (string, error) {
	c := e.Collection
	known, err := existingItems(ctx, tx, c.Items)
	if err != nil {
		return "", err
	}
	var items []string
	for _, id := range c.Items {
		if known[id] {
			items = append(items, id)
		}
	}
	var scope []int
	inScope := map[int]bool{}
	for _, id := range append([]int{c.LibraryID}, libraryIDsOf(c.Libraries)...) {
		local, ok := libMap[id]
		if !ok {
			return "", nil
		}
		if !inScope[local] {
			inScope[local] = true
			scope = append(scope, local)
		}
	}
	// The section owns the collection through its management fields, which
	// may name the section's id and libraries: both are this server's.
	rename := func(s string) string { return strings.ReplaceAll(s, e.ID, sectionID) }
	key, keyMapped := remapManagementKey(rename(c.ManagementKey), libMap)
	existing, err := managedCollectionOf(ctx, tx, plan.local)
	if err == nil && existing == "" && keyMapped {
		// The section was pointed elsewhere, and the collection it managed
		// is still here: its management key is unique, so it is the one,
		// when it covers the libraries the backup's maps to.
		existing, err = managedCollectionByKey(ctx, tx, key)
		if err == nil && existing != "" {
			var same bool
			if same, err = collectionScopeIs(ctx, tx, existing, scope); !same {
				existing, keyMapped = "", false
			}
		}
	}
	if err != nil {
		return "", err
	}
	if existing != "" {
		if err := catalog.LockLibraryCollectionInTx(ctx, tx, existing); err != nil {
			return "", err
		}
		_, err = catalog.AddMissingItemsInTx(ctx, tx, existing, items)
		return existing, err
	}
	query, queryOK := userdata.RemapQueryLibraries(c.QueryDefinition, mapIn(libMap))
	source, sourceOK := userdata.RemapQueryLibraries(c.SourceConfig, mapIn(libMap))
	if !queryOK || !sourceOK {
		return "", nil
	}
	newID, err := idgen.NextID()
	if err != nil {
		return "", err
	}
	if !keyMapped {
		// A key this server cannot map, or one another collection here
		// holds for other libraries: the new collection's own, so the two
		// never share one.
		key += ":restored:" + newID
	}
	slug, err := freeSlug(ctx, tx, scope[0], c.Slug)
	if err != nil {
		return "", err
	}
	if err := a.createCollection(ctx, tx, catalog.CreateLibraryCollectionInput{
		ID: newID, LibraryID: scope[0], LibraryIDs: scope, Slug: slug, Title: c.Title, Description: c.Description,
		CollectionType: c.CollectionType, Visibility: c.Visibility, SortOrder: c.SortOrder, Featured: c.Featured,
		PosterURL: c.Poster, BackdropURL: c.Backdrop, PosterThumbhash: thumbFor(c.Poster, c.PosterThumbhash), BackdropThumbhash: thumbFor(c.Backdrop, c.BackdropThumbhash),
		PosterAutoGenerated: c.PosterAutoGenerated, SourceURL: c.SourceURL, QueryDefinition: query, SortConfig: c.SortConfig, SourceConfig: source,
		SyncSchedule: c.SyncSchedule, ManagementMode: managementSection, ManagementSource: rename(c.ManagementSource), ManagementKey: key,
	}, true, notes); err != nil {
		return "", err
	}
	if _, err := catalog.AddMissingItemsInTx(ctx, tx, newID, items); err != nil {
		return "", err
	}
	return newID, nil
}

// remapManagementKey maps the library ids a management key names (each
// "library:<id>" segment, as a recipe writes "trakt:trending:movie:library:1")
// through libMap; false when one is not on this server.
func remapManagementKey(key string, libMap map[int]int) (string, bool) {
	parts := strings.Split(key, ":")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != pageLibrary {
			continue
		}
		id, err := strconv.Atoi(parts[i+1])
		if err != nil {
			continue
		}
		local, ok := libMap[id]
		if !ok {
			return key, false
		}
		parts[i+1] = strconv.Itoa(local)
		i++
	}
	return strings.Join(parts, ":"), true
}

// collectionScopeIs reports whether the collection's libraries are scope.
func collectionScopeIs(ctx context.Context, q queryer, id string, scope []int) (bool, error) {
	var libraries []int
	if err := q.QueryRow(ctx, `
		SELECT coalesce((SELECT array_agg(DISTINCT l.library_id ORDER BY l.library_id) FROM library_collection_libraries l WHERE l.collection_id = c.id),
			ARRAY[c.library_id])
		FROM library_collections c WHERE c.id = $1`, id).Scan(&libraries); err != nil {
		return false, fmt.Errorf("reading the collection's libraries: %w", err)
	}
	want := slices.Clone(scope)
	slices.Sort(want)
	return slices.Equal(libraries, want), nil
}

// managedCollectionOf is the collection the section here manages, or "".
func managedCollectionOf(ctx context.Context, tx queryer, local *sections.PageSection) (string, error) {
	if local == nil {
		return "", nil
	}
	existing := sectionCollectionID(local.Config)
	if existing == "" {
		return "", nil
	}
	var mode string
	err := tx.QueryRow(ctx, `SELECT management_mode FROM library_collections WHERE id = $1`, existing).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && mode != managementSection) {
		return "", nil
	}
	return existing, err
}

// managedCollectionByKey is the section-managed collection with key, or "".
func managedCollectionByKey(ctx context.Context, tx queryer, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM library_collections WHERE management_mode = $1 AND management_key = $2`, managementSection, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func libraryIDsOf(libs []CollectionLib) []int {
	out := make([]int, 0, len(libs))
	for _, l := range libs {
		out = append(out, l.LibraryID)
	}
	return out
}
