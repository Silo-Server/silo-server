package pgstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A profile's home and library page layout (its section overrides) is part
// of its preferences. Overrides name libraries and the admin sections they
// customize, which are this server's ids, so each one carries the section's
// type and title too: on import the library is mapped and the section found
// by id on the same server, or else by type and title within the mapped
// page, with exactly one candidate. An override whose library or section has
// no counterpart here is left out and counted.

// scopeLibrary is a library page's section scope and a library's
// collection-sort kind.
const scopeLibrary = "library"

type sectionOverrideEntry struct {
	Scope string `json:"scope"`
	// Library is the page's library; 0 for the home page.
	Library int `json:"library"`
	// Section is the admin section the override customizes; nil for a
	// section the profile added itself.
	Section         *sectionRef `json:"section"`
	Position        *int        `json:"position"`
	Hidden          bool        `json:"hidden"`
	Removed         bool        `json:"removed"`
	SectionType     string      `json:"section_type"`
	Title           string      `json:"title"`
	Featured        *bool       `json:"featured"`
	ItemLimit       *int        `json:"item_limit"`
	Config          string      `json:"config"`
	IsUserAdded     bool        `json:"is_user_added"`
	UserSectionType string      `json:"user_section_type"`
	UserConfig      string      `json:"user_config"`
	UserTitle       string      `json:"user_title"`
}

type sectionRef struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

func exportSectionOverrides(ctx context.Context, tx pgx.Tx, owner userdata.Owner) ([]sectionOverrideEntry, error) {
	rows, err := tx.Query(ctx, `SELECT value FROM user_settings WHERE user_id = $1 AND starts_with(key, 'section_overrides:') ORDER BY key`, owner.UserID)
	if err != nil {
		return nil, fmt.Errorf("reading section overrides: %w", err)
	}
	var mine []userstore.SectionOverride
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return nil, err
		}
		var overrides []userstore.SectionOverride
		// A value that no longer decodes is skipped, as the page reads do.
		if json.Unmarshal([]byte(value), &overrides) != nil {
			continue
		}
		for _, o := range overrides {
			if o.ProfileID == owner.ProfileID {
				mine = append(mine, o)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []sectionOverrideEntry{}
	for _, o := range mine {
		e, ok, err := overrideEntry(ctx, tx, o)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func validateSectionOverrides(entries []sectionOverrideEntry) error {
	for _, e := range entries {
		if (e.Scope != "home" || e.Library != 0) && (e.Scope != scopeLibrary || e.Library <= 0) {
			return errors.New("a section override names no valid page")
		}
		for _, s := range []string{e.SectionType, e.Title, e.UserSectionType, e.UserTitle} {
			if !userdata.ValidText(s, 512) {
				return errors.New("a section override is too long")
			}
		}
		for _, c := range []string{e.Config, e.UserConfig} {
			if !userdata.ValidText(c, 64<<10) || (c != "" && !json.Valid([]byte(c))) {
				return errors.New("a section override carries invalid configuration")
			}
		}
		if s := e.Section; s != nil && (s.ID == "" || !userdata.ValidText(s.ID, 128) || !userdata.ValidText(s.Type, 128) || !userdata.ValidText(s.Title, 512)) {
			return errors.New("a section override names an invalid section")
		}
	}
	return nil
}

// sectionPage is one page's key in user_settings.
type sectionPage struct {
	scope   string
	library int
}

func (p sectionPage) key() string {
	library := ""
	if p.library > 0 {
		library = strconv.Itoa(p.library)
	}
	return sectionOverridesKey(p.scope, library)
}

// overrideEntry is the entry an export writes for an override; false for
// one it leaves out: an override of a section that is gone customizes
// nothing, one naming a collection that is gone shows nothing, and one the
// reader would refuse (the values are JSON no schema constrains) would make
// the whole document unreadable.
func overrideEntry(ctx context.Context, tx pgx.Tx, o userstore.SectionOverride) (sectionOverrideEntry, bool, error) {
	library, _ := strconv.Atoi(o.LibraryID)
	e := sectionOverrideEntry{Scope: o.Scope, Library: library, Position: o.Position, Hidden: o.Hidden, Removed: o.Removed,
		SectionType: o.SectionType, Title: o.Title, Featured: o.Featured, ItemLimit: o.ItemLimit, Config: o.Config,
		IsUserAdded: o.IsUserAdded, UserSectionType: o.UserSectionType, UserConfig: o.UserConfig, UserTitle: o.UserTitle}
	if o.SectionID != "" {
		ref := sectionRef{ID: o.SectionID}
		err := tx.QueryRow(ctx, `SELECT section_type, title FROM page_sections WHERE id = $1`, o.SectionID).Scan(&ref.Type, &ref.Title)
		if errors.Is(err, pgx.ErrNoRows) {
			return e, false, nil
		}
		if err != nil {
			return e, false, fmt.Errorf("reading page sections: %w", err)
		}
		e.Section = &ref
	}
	for _, config := range []string{o.Config, o.UserConfig} {
		here, err := collectionsHere(ctx, tx, config)
		if err != nil || !here {
			return e, false, err
		}
	}
	return e, validateSectionOverrides([]sectionOverrideEntry{e}) == nil, nil
}

// collectionTables are the tables of the collections a configuration field
// names.
var collectionTables = map[string]string{
	userdata.LibraryCollectionField: "library_collections",
	userdata.UserCollectionField:    "user_personal_collections",
}

// configCollection is the collection id a configuration names in field, or
// "".
func configCollection(config, field string) string {
	var doc map[string]json.RawMessage
	var id string
	if config == "" || json.Unmarshal([]byte(config), &doc) != nil || json.Unmarshal(doc[field], &id) != nil {
		return ""
	}
	return id
}

// collectionsHere reports whether every collection a configuration names
// is on this server.
func collectionsHere(ctx context.Context, tx pgx.Tx, config string) (bool, error) {
	for field, table := range collectionTables {
		id := configCollection(config, field)
		if id == "" {
			continue
		}
		var here bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&here); err != nil {
			return false, fmt.Errorf("reading collections: %w", err)
		}
		if !here {
			return false, nil
		}
	}
	return true, nil
}

// remapCollections maps the collection ids a configuration names to the
// collections here (Target.Collection); false when one has none.
func remapCollections(ctx context.Context, target userdata.Target, config string) (string, bool, error) {
	var doc map[string]json.RawMessage
	if config == "" || json.Unmarshal([]byte(config), &doc) != nil {
		return config, true, nil //nolint:nilerr // not an object: it names no collection
	}
	changed := false
	for _, field := range []string{userdata.LibraryCollectionField, userdata.UserCollectionField} {
		id := configCollection(config, field)
		if id == "" {
			continue
		}
		local, ok, err := target.MapCollection(ctx, field, id)
		if err != nil || !ok {
			return "", false, err
		}
		if local != id {
			doc[field], _ = json.Marshal(local) // a string
			changed = true
		}
	}
	if !changed {
		return config, true, nil
	}
	out, err := json.Marshal(doc)
	return string(out), err == nil, err
}

// carried keeps the overrides an export carries, so a page compares equal
// to the backup's when only overrides the backup could not carry differ.
func carried(ctx context.Context, tx pgx.Tx, list []userstore.SectionOverride) ([]userstore.SectionOverride, error) {
	out := make([]userstore.SectionOverride, 0, len(list))
	for _, o := range list {
		_, ok, err := overrideEntry(ctx, tx, o)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// importSectionOverrides replaces the profile's overrides of each page the
// document has with the mapped ones, under the section-override lock;
// pages it does not carry stay as they are.
func importSectionOverrides(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []sectionOverrideEntry, res *userdata.Result) error {
	if len(entries) == 0 {
		return nil
	}
	if err := lockSectionOverrides(ctx, tx, target.UserID); err != nil {
		return err
	}
	pages, order, err := mapSectionPages(ctx, tx, target, entries, res)
	if err != nil {
		return err
	}
	for _, page := range order {
		if err := replacePageOverrides(ctx, tx, target, page, pages[page], res); err != nil {
			return err
		}
	}
	return nil
}

// pendingSectionOverrides counts what importSectionOverrides would do,
// writing nothing: a page whose overrides here differ from the mapped ones.
func pendingSectionOverrides(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []sectionOverrideEntry, res *userdata.Result) error {
	if len(entries) == 0 {
		return nil
	}
	pages, order, err := mapSectionPages(ctx, tx, target, entries, res)
	if err != nil {
		return err
	}
	for _, page := range order {
		mine, _, err := pageOverrides(ctx, tx, target, page)
		if err != nil {
			return err
		}
		live, err := carried(ctx, tx, mine)
		if err != nil {
			return err
		}
		switch {
		case sameOverrides(live, pages[page]):
			res.Unchanged++
		case len(mine) == 0:
			res.Added++
		default:
			res.Updated++
		}
	}
	return nil
}

// mapSectionPages maps each override, grouped by the page it belongs to
// here; one with no counterpart counts as left out.
func mapSectionPages(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []sectionOverrideEntry, res *userdata.Result) (map[sectionPage][]userstore.SectionOverride, []sectionPage, error) {
	pages := map[sectionPage][]userstore.SectionOverride{}
	var order []sectionPage
	unmapped := 0
	for _, e := range entries {
		o, page, ok, err := mapSectionOverride(ctx, tx, target, e)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			unmapped++
			continue
		}
		if _, seen := pages[page]; !seen {
			order = append(order, page)
		}
		pages[page] = append(pages[page], o)
	}
	if unmapped > 0 {
		res.LeftOut += unmapped
		res.Notes = append(res.Notes, fmt.Sprintf("%d page layout changes name libraries, sections, or collections that are not on this server and were left out.", unmapped))
	}
	return pages, order, nil
}

// mapSectionOverride maps one override's library and section, and the
// library ids its configuration names; false when any has no counterpart.
func mapSectionOverride(ctx context.Context, tx pgx.Tx, target userdata.Target, e sectionOverrideEntry) (userstore.SectionOverride, sectionPage, bool, error) {
	page := sectionPage{scope: e.Scope}
	if e.Library > 0 {
		local, ok := target.MapLibrary(e.Library)
		if !ok {
			return userstore.SectionOverride{}, page, false, nil
		}
		page.library = local
	}
	config, ok, err := remapConfig(ctx, e.Config, target)
	if err != nil || !ok {
		return userstore.SectionOverride{}, page, false, err
	}
	userConfig, ok, err := remapConfig(ctx, e.UserConfig, target)
	if err != nil || !ok {
		return userstore.SectionOverride{}, page, false, err
	}
	o := userstore.SectionOverride{ProfileID: target.ProfileID, Scope: e.Scope, Position: e.Position, Hidden: e.Hidden, Removed: e.Removed,
		SectionType: e.SectionType, Title: e.Title, Featured: e.Featured, ItemLimit: e.ItemLimit, Config: config,
		IsUserAdded: e.IsUserAdded, UserSectionType: e.UserSectionType, UserConfig: userConfig, UserTitle: e.UserTitle}
	if page.library > 0 {
		o.LibraryID = strconv.Itoa(page.library)
	}
	if e.Section != nil {
		id, err := localSection(ctx, tx, target, page, *e.Section)
		if err != nil || id == "" {
			return userstore.SectionOverride{}, page, false, err
		}
		o.SectionID = id
	}
	return o, page, true, nil
}

// remapConfig maps the libraries and collections a configuration names;
// false when one has no counterpart here.
func remapConfig(ctx context.Context, config string, target userdata.Target) (string, bool, error) {
	if config == "" {
		return "", true, nil
	}
	mapped, ok := userdata.RemapQueryLibraries(json.RawMessage(config), target.MapLibrary)
	if !ok {
		return "", false, nil
	}
	return remapCollections(ctx, target, string(mapped))
}

// localSection finds the admin section here: the one of the page with the
// same id (on another server one with the same type too: a restore creates
// a section with its backup id, and a title two sections share there would
// not find it), or else the one section of the page with the type and
// title.
func localSection(ctx context.Context, tx pgx.Tx, target userdata.Target, page sectionPage, ref sectionRef) (string, error) {
	var library *int
	if page.library > 0 {
		library = &page.library
	}
	var id, sectionType string
	err := tx.QueryRow(ctx, `SELECT id, section_type FROM page_sections WHERE id = $1 AND scope = $2 AND library_id IS NOT DISTINCT FROM $3`,
		ref.ID, page.scope, library).Scan(&id, &sectionType)
	switch {
	case err == nil && (target.SameServer || sectionType == ref.Type):
		return id, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("reading page sections: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT id FROM page_sections WHERE scope = $1 AND library_id IS NOT DISTINCT FROM $2 AND section_type = $3 AND title = $4 LIMIT 2`,
		page.scope, library, ref.Type, ref.Title)
	if err != nil {
		return "", fmt.Errorf("matching page sections: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) != 1 {
		return "", err
	}
	return ids[0], nil
}

// replacePageOverrides writes the profile's overrides of one page, keeping
// every other profile's, unless they already match.
func replacePageOverrides(ctx context.Context, tx pgx.Tx, target userdata.Target, page sectionPage, next []userstore.SectionOverride, res *userdata.Result) error {
	mine, others, err := pageOverrides(ctx, tx, target, page)
	if err != nil {
		return err
	}
	live, err := carried(ctx, tx, mine)
	if err != nil {
		return err
	}
	if sameOverrides(live, next) {
		res.Unchanged++
		return nil
	}
	now := target.Now.UTC().Format("2006-01-02T15:04:05Z")
	for i := range next {
		next[i].ID, next[i].CreatedAt, next[i].UpdatedAt = generateUUID(), now, now
	}
	data, _ := json.Marshal(append(others, next...)) // plain structs
	if _, err := tx.Exec(ctx, `INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = excluded.value`, target.UserID, page.key(), string(data)); err != nil {
		return fmt.Errorf("writing section overrides %s: %w", page.key(), err)
	}
	if len(mine) == 0 {
		res.Added++
	} else {
		res.Updated++
	}
	return nil
}

// pageOverrides reads one page's overrides: the profile's own and every
// other profile's.
func pageOverrides(ctx context.Context, tx pgx.Tx, target userdata.Target, page sectionPage) (mine, others []userstore.SectionOverride, err error) {
	key := page.key()
	value, err := getSetting(ctx, tx, target.UserID, key)
	if err != nil {
		return nil, nil, err
	}
	var existing []userstore.SectionOverride
	if value != "" {
		if err := json.Unmarshal([]byte(value), &existing); err != nil {
			return nil, nil, fmt.Errorf("decoding section overrides %s: %w", key, err)
		}
	}
	for _, o := range existing {
		if o.ProfileID == target.ProfileID {
			mine = append(mine, o)
		} else {
			others = append(others, o)
		}
	}
	return mine, others, nil
}

// canonicalJSON re-encodes a JSON document so equal values compare equal;
// anything else is returned as it is.
func canonicalJSON(s string) string {
	var v any
	if s == "" || json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	out, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(out)
}

// sameOverrides compares two override lists ignoring ids and times.
func sameOverrides(a, b []userstore.SectionOverride) bool {
	norm := func(list []userstore.SectionOverride) []userstore.SectionOverride {
		out := make([]userstore.SectionOverride, len(list))
		for i, o := range list {
			o.ID, o.CreatedAt, o.UpdatedAt, o.ProfileID = "", "", "", ""
			// A configuration a restore remapped is re-encoded: compare
			// it by value, not by its bytes.
			o.Config, o.UserConfig = canonicalJSON(o.Config), canonicalJSON(o.UserConfig)
			out[i] = o
		}
		sort.Slice(out, func(i, j int) bool {
			x, _ := json.Marshal(out[i])
			y, _ := json.Marshal(out[j])
			return bytes.Compare(x, y) < 0
		})
		return out
	}
	return len(a) == len(b) && reflect.DeepEqual(norm(a), norm(b))
}
