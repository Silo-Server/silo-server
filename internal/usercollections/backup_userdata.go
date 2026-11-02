package usercollections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// BackupData is the personal collections a profile created
// (userdata.Collections), with their items, the profiles they are shared
// with, and their uploaded posters.
//
// A collection matches one of the profile's own collections by name: none
// creates it with a new id (its poster copied under the new id first),
// exactly one gains the items it lacks, several leave it out. Restored
// collections are ungrouped; collection groups are account-wide shelf layout
// and are not carried. Audiobookshelf rows that share the table are not
// carried either.
type BackupData struct{}

func (BackupData) ID() string { return userdata.Collections }

func (BackupData) Tables() []string {
	return []string{"user_personal_collections", "user_personal_collection_items", "user_personal_collection_profiles", "user_collection_groups"}
}

// posterPrefix is where uploaded personal collection images live, one
// directory per collection id.
const posterPrefix = "user-collection-images/"

type collectionsDocument struct {
	Collections []personalCollection `json:"collections"`
}

type personalCollection struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	IsShared    bool     `json:"is_shared"`
	SharedWith  []string `json:"shared_with"`
	// Group is the account's shelf group the collection sits in, or nil.
	Group                      *collectionGroup `json:"group"`
	IncludeInServerCollections bool             `json:"include_in_server_collections"`
	QueryDefinition            json.RawMessage  `json:"query_definition"`
	DisplayQueryDefinition     json.RawMessage  `json:"display_query_definition"`
	SortConfig                 json.RawMessage  `json:"sort_config"`
	SourceURL                  string           `json:"source_url"`
	SourceConfig               json.RawMessage  `json:"source_config"`
	SyncSchedule               *string          `json:"sync_schedule"`
	WatchFilter                string           `json:"watch_filter"`
	MediaFilter                string           `json:"media_filter"`
	// WatchFilter and MediaFilter are checked by the table's constraints.
	// Poster is as stored: an uploaded image's key under
	// user-collection-images/<id>/, or a URL or provider path.
	Poster          string   `json:"poster"`
	PosterThumbhash string   `json:"poster_thumbhash"`
	Items           []string `json:"items"`
}

// collectionGroup is one of the account's collection groups.
type collectionGroup struct {
	Label           string `json:"label"`
	Title           string `json:"title"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	SortOrder       int    `json:"sort_order"`
	DefaultSortMode string `json:"default_sort_mode"`
}

var groupSortModes = func() map[string]bool {
	out := map[string]bool{}
	for _, mode := range strings.Fields("manual name_asc name_desc recent most_items") {
		out[mode] = true
	}
	return out
}()

var collectionTypes = func() map[string]bool {
	out := map[string]bool{}
	for _, kind := range strings.Fields("manual smart mdblist tmdb synced playlist") {
		out[kind] = true
	}
	return out
}()

func (BackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner) (userdata.Export, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.name, c.description, c.collection_type, c.is_shared, c.include_in_server_collections, c.query_definition::text,
			COALESCE(c.display_query_definition::text, ''), c.sort_config::text, c.source_url, c.source_config::text, c.sync_schedule,
			c.watch_filter, c.media_filter, c.poster_url, c.poster_thumbhash,
			g.label, g.title, g.name, g.slug, g.sort_order, g.default_sort_mode
		FROM user_personal_collections c
		LEFT JOIN user_collection_groups g ON g.user_id = c.user_id AND g.id = c.group_id
		WHERE c.user_id = $1 AND c.creator_profile_id = $2 AND c.native
		ORDER BY c.sort_order, c.created_at, c.id`, owner.UserID, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading personal collections: %w", err)
	}
	doc := collectionsDocument{Collections: []personalCollection{}}
	index := map[string]int{}
	for rows.Next() {
		var c personalCollection
		var query, display, sortConfig, source string
		var label, title, name, slug, sortMode *string
		var groupOrder *int
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Type, &c.IsShared, &c.IncludeInServerCollections, &query, &display,
			&sortConfig, &c.SourceURL, &source, &c.SyncSchedule, &c.WatchFilter, &c.MediaFilter, &c.Poster, &c.PosterThumbhash,
			&label, &title, &name, &slug, &groupOrder, &sortMode); err != nil {
			rows.Close()
			return userdata.Export{}, err
		}
		if slug != nil {
			c.Group = &collectionGroup{Label: *label, Title: *title, Name: *name, Slug: *slug, SortOrder: *groupOrder, DefaultSortMode: *sortMode}
		}
		c.QueryDefinition, c.SortConfig, c.SourceConfig = json.RawMessage(query), json.RawMessage(sortConfig), json.RawMessage(source)
		if display != "" {
			c.DisplayQueryDefinition = json.RawMessage(display)
		}
		c.SharedWith, c.Items = []string{}, []string{}
		index[c.ID] = len(doc.Collections)
		doc.Collections = append(doc.Collections, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	if len(doc.Collections) == 0 {
		data, err := json.Marshal(doc)
		return userdata.Export{Data: data}, err
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	rows, err = tx.Query(ctx, `
		SELECT collection_id, media_item_id FROM user_personal_collection_items
		WHERE user_id = $1 AND collection_id = ANY($2) AND sub_item_id = ''
		ORDER BY collection_id, position, media_item_id`, owner.UserID, ids)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading personal collection items: %w", err)
	}
	var items []string
	for rows.Next() {
		var id, item string
		if err := rows.Scan(&id, &item); err != nil {
			rows.Close()
			return userdata.Export{}, err
		}
		doc.Collections[index[id]].Items = append(doc.Collections[index[id]].Items, item)
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	rows, err = tx.Query(ctx, `
		SELECT collection_id, profile_id FROM user_personal_collection_profiles
		WHERE user_id = $1 AND collection_id = ANY($2) AND profile_id <> $3 ORDER BY collection_id, profile_id`, owner.UserID, ids, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading personal collection sharing: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, profile string
		if err := rows.Scan(&id, &profile); err != nil {
			return userdata.Export{}, err
		}
		doc.Collections[index[id]].SharedWith = append(doc.Collections[index[id]].SharedWith, profile)
	}
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	out := userdata.Export{Count: len(doc.Collections), Items: items}
	for _, c := range doc.Collections {
		if uploadedPoster(c) {
			out.Blobs = append(out.Blobs, userdata.Blob{Store: userdata.StoreAssets, Key: c.Poster})
		}
	}
	out.Data, err = json.Marshal(doc)
	return out, err
}

// uploadedPoster reports whether the collection's poster is an image
// uploaded for it, stored under its own id.
func uploadedPoster(c personalCollection) bool {
	return strings.HasPrefix(c.Poster, posterPrefix+c.ID+"/")
}

func decodeCollections(data json.RawMessage) (collectionsDocument, userdata.Summary, error) {
	var doc collectionsDocument
	var sum userdata.Summary
	if err := userdata.DecodeStrict(data, &doc); err != nil {
		return doc, sum, err
	}
	total := len(doc.Collections)
	for _, c := range doc.Collections {
		total += len(c.Items)
	}
	if err := userdata.CheckEntries("personal collections", total); err != nil {
		return doc, sum, err
	}
	seen := map[string]bool{}
	for _, c := range doc.Collections {
		if !userdata.ValidContentID(c.ID) || seen[c.ID] || strings.TrimSpace(c.Name) == "" || !userdata.ValidText(c.Name, 512) ||
			!userdata.ValidText(c.Description, 8192) || !collectionTypes[c.Type] {
			return doc, sum, fmt.Errorf("personal collection %q is not valid", c.ID)
		}
		seen[c.ID] = true
		for _, raw := range []json.RawMessage{c.QueryDefinition, c.DisplayQueryDefinition, c.SortConfig, c.SourceConfig} {
			if len(raw) > 0 && !json.Valid(raw) {
				return doc, sum, fmt.Errorf("personal collection %q carries invalid JSON", c.ID)
			}
		}
		if g := c.Group; g != nil && (strings.TrimSpace(g.Slug) == "" || strings.TrimSpace(g.Label) == "" || !groupSortModes[g.DefaultSortMode] ||
			!userdata.ValidText(g.Label, 512) || !userdata.ValidText(g.Title, 512) || !userdata.ValidText(g.Name, 512) || !userdata.ValidText(g.Slug, 512)) {
			return doc, sum, fmt.Errorf("personal collection %q names an invalid group", c.ID)
		}
		for _, item := range c.Items {
			if !userdata.ValidContentID(item) {
				return doc, sum, fmt.Errorf("personal collection %q names an invalid item", c.ID)
			}
			sum.Items = append(sum.Items, item)
		}
		for _, p := range c.SharedWith {
			if !userdata.ValidContentID(p) {
				return doc, sum, fmt.Errorf("personal collection %q names an invalid profile", c.ID)
			}
		}
		if strings.HasPrefix(c.Poster, posterPrefix) {
			if !uploadedPoster(c) {
				return doc, sum, fmt.Errorf("personal collection %q names another collection's poster", c.ID)
			}
			sum.Blobs = append(sum.Blobs, userdata.Blob{Store: userdata.StoreAssets, Key: c.Poster})
		}
	}
	sum.Count = len(doc.Collections)
	return doc, sum, nil
}

func (BackupData) Validate(data json.RawMessage) (userdata.Summary, error) {
	_, sum, err := decodeCollections(data)
	return sum, err
}

func (BackupData) Import(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeCollections(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	unmatched, missingPosters := 0, 0
	for _, c := range doc.Collections {
		var items []string
		for _, item := range c.Items {
			local, ok, err := target.Items.Resolve(ctx, item)
			if err != nil {
				return res, err
			}
			if ok {
				items = append(items, local)
			} else {
				unmatched++
			}
		}
		existing, err := collectionsNamed(ctx, tx, target.Owner, c.Name)
		if err != nil {
			return res, err
		}
		switch len(existing) {
		case 0:
			created, err := createCollection(ctx, tx, target, c, items, &missingPosters)
			if err != nil {
				return res, err
			}
			if !created {
				res.LeftOut++
				res.Notes = append(res.Notes, fmt.Sprintf("The collection %q was left out: its rules or sync source name only libraries that are not on this server.", c.Name))
				continue
			}
			res.Added++
		case 1:
			added, err := addMissingItems(ctx, tx, target.UserID, existing[0], items)
			if err != nil {
				return res, err
			}
			shared, err := shareWith(ctx, tx, target, existing[0], c.SharedWith)
			if err != nil {
				return res, err
			}
			grouped, err := regroup(ctx, tx, target.UserID, existing[0], c.Group)
			if err != nil {
				return res, err
			}
			if added > 0 || shared > 0 || grouped {
				res.Updated++
			} else {
				res.Unchanged++
			}
		default:
			res.LeftOut++
			res.Notes = append(res.Notes, fmt.Sprintf("%d of this profile's collections are named %q; that collection was left out.", len(existing), c.Name))
		}
	}
	if unmatched > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d collection items are not in this server's catalog and were left out.", unmatched))
	}
	if missingPosters > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d collection posters were not restored.", missingPosters))
	}
	return res, nil
}

func collectionsNamed(ctx context.Context, tx pgx.Tx, owner userdata.Owner, name string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM user_personal_collections
		WHERE user_id = $1 AND creator_profile_id = $2 AND native AND lower(name) = lower($3) ORDER BY id`, owner.UserID, owner.ProfileID, name)
	if err != nil {
		return nil, fmt.Errorf("matching personal collections: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// createCollection creates the collection under a new id, in its group. Its
// smart rules, display filter, and sync source are remapped to this
// server's libraries; one that would lose its whole library scope leaves
// the collection out (false) rather than widening it to every library. An
// uploaded poster is copied under the new id before the row that references
// it (a rollback leaves an orphan for the orphan sweeps); with no file to
// copy it is dropped. Any other poster reference is kept as it is.
func createCollection(ctx context.Context, tx pgx.Tx, target userdata.Target, c personalCollection, items []string, missingPosters *int) (bool, error) {
	query, display, source, ok := remapScopes(c, target)
	if !ok {
		return false, nil
	}
	group, err := ensureGroup(ctx, tx, target.UserID, c.Group)
	if err != nil {
		return false, err
	}
	id := uuid.NewString()
	poster, err := copyPoster(ctx, target, c, id, missingPosters)
	if err != nil {
		return false, err
	}
	thumb := c.PosterThumbhash
	if poster == "" {
		thumb = ""
	}
	orEmpty := func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return "{}"
		}
		return string(raw)
	}
	var displayArg any
	if len(display) > 0 {
		displayArg = string(display)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_personal_collections (
			id, user_id, profile_id, creator_profile_id, name, description, collection_type, is_shared, include_in_server_collections,
			query_definition, display_query_definition, sort_config, source_url, source_config, sync_schedule, next_sync_at,
			watch_filter, media_filter, poster_url, poster_thumbhash, group_id, sort_order, native)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12, $13::jsonb, $14, $15, $16, $17, $18, $19, $20,
			COALESCE((SELECT MAX(sort_order) + 1 FROM user_personal_collections WHERE user_id = $2 AND creator_profile_id = $3 AND native), 0), TRUE)`,
		id, target.UserID, target.ProfileID, c.Name, c.Description, c.Type, c.IsShared, c.IncludeInServerCollections,
		orEmpty(query), displayArg, orEmpty(c.SortConfig), c.SourceURL, orEmpty(source), c.SyncSchedule,
		InitialNextSyncAt(c.SyncSchedule), c.WatchFilter, c.MediaFilter, poster, thumb, group); err != nil {
		return false, fmt.Errorf("creating personal collection %q: %w", c.Name, err)
	}
	if _, err := addMissingItems(ctx, tx, target.UserID, id, items); err != nil {
		return false, err
	}
	_, err = shareWith(ctx, tx, target, id, c.SharedWith)
	return err == nil, err
}

// remapScopes maps the libraries of the collection's smart rules, display
// filter, and sync source; false when one would lose its whole library scope.
func remapScopes(c personalCollection, target userdata.Target) (query, display, source json.RawMessage, ok bool) {
	query, queryOK := userdata.RemapQueryLibraries(c.QueryDefinition, target.MapLibrary)
	display, displayOK := userdata.RemapQueryLibraries(c.DisplayQueryDefinition, target.MapLibrary)
	source, sourceOK := userdata.RemapQueryLibraries(c.SourceConfig, target.MapLibrary)
	return query, display, source, queryOK && displayOK && sourceOK
}

// ensureGroup finds the account's group of the backup group's slug, or
// else its label, creating it when neither exists; nil for no group.
func ensureGroup(ctx context.Context, tx pgx.Tx, userID int, g *collectionGroup) (*string, error) {
	if g == nil {
		return nil, nil
	}
	find := func() (*string, error) {
		var id string
		err := tx.QueryRow(ctx, `
			SELECT id FROM user_collection_groups WHERE user_id = $1 AND (slug = $2 OR label = $3)
			ORDER BY (slug = $2) DESC LIMIT 1`, userID, g.Slug, g.Label).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading collection groups: %w", err)
		}
		return &id, nil
	}
	if id, err := find(); err != nil || id != nil {
		return id, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_collection_groups (user_id, id, label, title, name, slug, sort_order, default_sort_mode)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
		userID, uuid.NewString(), g.Label, g.Title, g.Name, g.Slug, g.SortOrder, g.DefaultSortMode); err != nil {
		return nil, fmt.Errorf("creating a collection group: %w", err)
	}
	return find()
}

// regroup puts an ungrouped collection here into the backup's group; a
// group chosen here since stays.
func regroup(ctx context.Context, tx pgx.Tx, userID int, collectionID string, g *collectionGroup) (bool, error) {
	if g == nil {
		return false, nil
	}
	var current *string
	if err := tx.QueryRow(ctx, `SELECT group_id FROM user_personal_collections WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, collectionID).Scan(&current); err != nil {
		return false, fmt.Errorf("reading the collection's group: %w", err)
	}
	if current != nil {
		return false, nil
	}
	group, err := ensureGroup(ctx, tx, userID, g)
	if err != nil || group == nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_personal_collections SET group_id = $3 WHERE user_id = $1 AND id = $2`, userID, collectionID, *group); err != nil {
		return false, fmt.Errorf("grouping the collection: %w", err)
	}
	return true, nil
}

// copyPoster returns the poster a new collection stores: an uploaded poster
// copied under the new id, or dropped (and counted) when there is no file to
// copy; an image URL or a bundled path as it is. Any other value is dropped
// and counted:
// listings sign a bare key as a stored object (presignUserCollectionPoster),
// so an imported document must not name one outside the collection images.
func copyPoster(ctx context.Context, target userdata.Target, c personalCollection, id string, missing *int) (string, error) {
	switch {
	case strings.HasPrefix(c.Poster, posterPrefix):
	case strings.HasPrefix(c.Poster, "https://"), strings.HasPrefix(c.Poster, "http://"), strings.HasPrefix(c.Poster, "/"):
		return c.Poster, nil
	default:
		*missing++
		return "", nil
	}
	if target.WriteBlob == nil || target.HasBlob == nil || !target.HasBlob(c.Poster) {
		*missing++
		return "", nil
	}
	to := posterPrefix + id + "/" + strings.TrimPrefix(c.Poster, posterPrefix+c.ID+"/")
	if err := target.WriteBlob(ctx, userdata.StoreAssets, c.Poster, to); err != nil {
		return "", err
	}
	return to, nil
}

// addMissingItems appends the items the collection lacks, in order.
func addMissingItems(ctx context.Context, tx pgx.Tx, userID int, collectionID string, items []string) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	// The collection row first, as collection edits lock it, so the
	// revision triggers below cannot deadlock with them.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM user_personal_collections WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, collectionID); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		WITH wanted AS (
			SELECT DISTINCT ON (media_item_id) media_item_id, ord
			FROM unnest($3::text[]) WITH ORDINALITY AS w(media_item_id, ord) ORDER BY media_item_id, ord
		), missing AS (
			SELECT w.media_item_id, w.ord FROM wanted w
			WHERE NOT EXISTS (SELECT 1 FROM user_personal_collection_items i
				WHERE i.user_id = $1 AND i.collection_id = $2 AND i.media_item_id = w.media_item_id AND i.sub_item_id = '')
		), base AS (
			SELECT COALESCE(MAX(position) + 1, 0) AS next FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2
		)
		INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, position, added_at)
		SELECT $1, $2, m.media_item_id, base.next + (row_number() OVER (ORDER BY m.ord))::int - 1, now()
		FROM missing m, base
		ON CONFLICT DO NOTHING`, userID, collectionID, items)
	if err != nil {
		return 0, fmt.Errorf("adding personal collection items: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// shareWith adds the collection's visibility to each mapped profile.
func shareWith(ctx context.Context, tx pgx.Tx, target userdata.Target, collectionID string, profiles []string) (int, error) {
	added := 0
	for _, source := range profiles {
		profile, ok := target.MapProfile(source)
		if !ok || profile == target.ProfileID {
			continue
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id)
			SELECT $1, $2, $3 WHERE EXISTS (SELECT 1 FROM user_profiles WHERE user_id = $1 AND id = $3)
			ON CONFLICT DO NOTHING`, target.UserID, collectionID, profile)
		if err != nil {
			return added, fmt.Errorf("sharing personal collection: %w", err)
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}

// Pending counts what Import would do, writing nothing: a collection with
// no namesake here that keeps a library scope is added; one with a single
// namesake is updated when that one lacks an item this server has, a
// share, or its group.
func (BackupData) Pending(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeCollections(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	for _, c := range doc.Collections {
		existing, err := collectionsNamed(ctx, tx, target.Owner, c.Name)
		if err != nil {
			return res, err
		}
		switch len(existing) {
		case 0:
			if _, _, _, ok := remapScopes(c, target); ok {
				res.Added++
			} else {
				res.LeftOut++
			}
		case 1:
			changes, err := pendingCollectionChanges(ctx, tx, target, existing[0], c)
			if err != nil {
				return res, err
			}
			if changes {
				res.Updated++
			} else {
				res.Unchanged++
			}
		default:
			res.LeftOut++
		}
	}
	return res, nil
}

// pendingCollectionChanges reports whether importing c into the collection
// here would add an item this server has, a share, or its group.
func pendingCollectionChanges(ctx context.Context, tx pgx.Tx, target userdata.Target, collectionID string, c personalCollection) (bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT x.item FROM unnest($3::text[]) AS x(item)
		WHERE NOT EXISTS (SELECT 1 FROM user_personal_collection_items i
			WHERE i.user_id = $1 AND i.collection_id = $2 AND i.media_item_id = x.item AND i.sub_item_id = '')`,
		target.UserID, collectionID, c.Items)
	if err != nil {
		return false, fmt.Errorf("comparing personal collection %q: %w", c.Name, err)
	}
	missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, fmt.Errorf("comparing personal collection %q: %w", c.Name, err)
	}
	if len(missing) > 0 {
		here, err := userdata.ResolveAll(ctx, target.Items, missing)
		if err != nil || len(here) > 0 {
			return err == nil, err
		}
	}
	var shares []string
	for _, source := range c.SharedWith {
		if profile, ok := target.MapProfile(source); ok && profile != target.ProfileID {
			shares = append(shares, profile)
		}
	}
	var changes bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM unnest($3::text[]) AS s(profile)
				WHERE EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id = $1 AND p.id = s.profile)
				  AND NOT EXISTS (SELECT 1 FROM user_personal_collection_profiles cp
					WHERE cp.user_id = $1 AND cp.collection_id = $2 AND cp.profile_id = s.profile))
			OR ($4 AND EXISTS (SELECT 1 FROM user_personal_collections WHERE user_id = $1 AND id = $2 AND group_id IS NULL))`,
		target.UserID, collectionID, shares, c.Group != nil).Scan(&changes)
	if err != nil {
		return false, fmt.Errorf("comparing personal collection %q: %w", c.Name, err)
	}
	return changes, nil
}
