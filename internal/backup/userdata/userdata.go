// Package userdata is the contract between internal/backup and the packages
// that own per-profile data. Each owner implements Type for its data, so the
// package that knows a table's invariants is the one that reads and merges
// it; internal/backup registers the types, carries their documents in a
// backup's restore set and a profile export, and chooses which to apply.
//
// It imports nothing from the owners or from internal/backup, so any owner
// can implement it without an import cycle.
package userdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
)

// Data type ids. They name a profile's data in a restore set, a profile
// export, a restore selection, and a report.
const (
	WatchHistory  = "watch_history"
	Ratings       = "ratings"
	Favorites     = "favorites"
	Collections   = "collections"
	Dismissals    = "dismissals"
	Preferences   = "preferences"
	Notifications = "notifications"
)

// Stores a carried blob lives in, as internal/backup names them.
const (
	StoreAssets      = "assets"
	StoreOperational = "operational"
)

// Owner names one profile's rows.
type Owner struct {
	UserID    int
	ProfileID string
}

// Export is one data type's export of one profile.
type Export struct {
	// Data is the type's own JSON document.
	Data json.RawMessage
	// Count is how many entries Data holds, for previews.
	Count int
	// Items are the content ids Data references, so the carrier can record
	// their provider ids for matching on another server.
	Items []string
	// Blobs are the files Data references that must travel with it.
	Blobs []Blob
}

// Blob is a carried file.
type Blob struct {
	Store string
	Key   string
}

// Summary is what validating a document found: its entry count, the content
// ids and blob keys it references.
type Summary struct {
	Count int
	Items []string
	Blobs []Blob
}

// Items resolves a source content id to the catalog item it is here. The
// resolver records items it could not match, by title, for the report.
type Items interface {
	Resolve(ctx context.Context, sourceID string) (localID string, ok bool, err error)
}

// Target is where an import writes, and how references in the document map
// onto this server.
type Target struct {
	Owner
	// SameServer is true when the document comes from this server, so
	// server-local references (file ids, paths) still mean the same thing.
	SameServer bool
	// Items maps content ids.
	Items Items
	// Library maps a source library id; false drops the reference.
	Library func(sourceID int) (int, bool)
	// Profile maps another source profile of the same account; false drops
	// the reference.
	Profile func(sourceID string) (string, bool)
	// WriteBlob writes a carried blob under a key this server owns; nil when
	// the document carries no blobs (a profile export), in which case
	// references to blobs are dropped.
	WriteBlob func(ctx context.Context, store, from, to string) error
	// HasBlob reports whether a carried blob is present; nil means none is.
	HasBlob func(key string) bool
	// PluginInstallation maps a source server's watch-sync plugin
	// installation, for plays a plugin imported; false when no installation
	// here is that plugin's with the capability. Only consulted across
	// servers.
	PluginInstallation func(installationID int, capabilityID string) (int, bool)
	// Collection maps a source collection id named in field
	// (LibraryCollectionField, an admin collection, or UserCollectionField,
	// a personal one) to the collection here; false drops the reference.
	// nil maps none.
	Collection func(ctx context.Context, field, id string) (string, bool, error)
	// Now stamps rows that need a time of their own.
	Now time.Time
	// Untrusted marks a profile's own export, which whoever holds it could
	// have edited. Its import must not write rows other accounts read, such
	// as shared watchlist titles, from the document.
	Untrusted bool
}

// The configuration fields that name a collection.
const (
	LibraryCollectionField = "library_collection_id"
	UserCollectionField    = "user_collection_id"
)

// MapCollection maps a source collection id, dropping it when no mapping
// is set.
func (t Target) MapCollection(ctx context.Context, field, id string) (string, bool, error) {
	if t.Collection == nil {
		return "", false, nil
	}
	return t.Collection(ctx, field, id)
}

// MapPluginInstallation maps a source plugin installation, dropping it when
// no mapping is set.
func (t Target) MapPluginInstallation(installationID int, capabilityID string) (int, bool) {
	if t.PluginInstallation == nil {
		return 0, false
	}
	return t.PluginInstallation(installationID, capabilityID)
}

// MapLibrary maps a library id, dropping it when no mapping is set.
func (t Target) MapLibrary(id int) (int, bool) {
	if t.Library == nil {
		return 0, false
	}
	return t.Library(id)
}

// MapProfile maps another profile of the account.
func (t Target) MapProfile(id string) (string, bool) {
	if t.Profile == nil {
		return "", false
	}
	return t.Profile(id)
}

// Result counts what an import did. An import that changes nothing reports
// only Unchanged, so running it again is visibly a no-op.
type Result struct {
	Added     int      `json:"added"`
	Updated   int      `json:"updated"`
	Unchanged int      `json:"unchanged"`
	LeftOut   int      `json:"left_out"`
	Notes     []string `json:"notes,omitempty"`
}

// Changed reports whether the import wrote anything.
func (r Result) Changed() bool { return r.Added > 0 || r.Updated > 0 }

// Changes counts what a result changed, or a pending result would change.
func (r Result) Changes() int { return r.Added + r.Updated }

// Type is one kind of per-profile data.
//
// Import merges and never deletes: entries this server already has win
// unless the type's rule says the newer one does, and importing the same
// document twice changes nothing the second time. Import runs in the
// caller's transaction, so a restore commits it with the item's fenced
// outcome.
type Type interface {
	ID() string
	// Tables are the Postgres tables the type reads and writes, for the
	// coverage test that every per-user table is claimed or excluded.
	Tables() []string
	Export(ctx context.Context, tx pgx.Tx, owner Owner) (Export, error)
	// Validate decodes an untrusted document strictly and checks it.
	Validate(data json.RawMessage) (Summary, error)
	Import(ctx context.Context, tx pgx.Tx, target Target, data json.RawMessage) (Result, error)
	// Pending counts what Import would do with data for target, by the same
	// merge rules, without writing anything or taking a lock: Added and
	// Updated are what it would change, Unchanged what it would find the
	// same, LeftOut what it could not map. tx may be read-only. It compares
	// the document's content ids with this server's rows directly and
	// resolves only the entries that would change through target.Items
	// (SettleChanged), which a preview gives without the provider-id
	// fallback an import has: an item only that fallback finds counts as
	// left out.
	Pending(ctx context.Context, tx pgx.Tx, target Target, data json.RawMessage) (Result, error)
}

// PendingFunc compares a decoded document with target as Pending does. A
// Type whose documents are large can split its Pending into decoding, which
// needs no database, and a PendingFunc, with a method
// PreparePending(data json.RawMessage) (PendingFunc, error); a caller
// comparing many documents on one transaction then decodes them ahead,
// concurrently, while it queries.
type PendingFunc func(ctx context.Context, tx pgx.Tx, target Target) (Result, error)

// BatchItems is an Items that resolves many ids at once.
type BatchItems interface {
	ResolveAll(ctx context.Context, sourceIDs []string) (map[string]string, error)
}

// ResolveAll maps each id items resolves to its local id; an id absent from
// the map is not here.
func ResolveAll(ctx context.Context, items Items, ids []string) (map[string]string, error) {
	if b, ok := items.(BatchItems); ok {
		return b.ResolveAll(ctx, ids)
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		local, ok, err := items.Resolve(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out[id] = local
		}
	}
	return out, nil
}

// Prepared is one profile's document of a type, before it is imported.
type Prepared struct {
	Owner Owner
	Data  json.RawMessage
	// Untrusted is as Target.Untrusted.
	Untrusted bool
}

// Preparer is a Type that takes locks before any document of an import is
// written. Every import entrypoint (a selective restore's account, a profile
// import) takes, in this order:
//
//  1. the account locks (pgstore.LockAccountForImport): the account's
//     preference-settings lock, then its section-override lock;
//  2. each Preparer's locks, types in registry order and, within a type, in
//     the type's own fixed order over every profile of the import: the
//     profiles' history locks by profile id, then every watchlist title
//     the import names, created when missing, by media type and TMDB id;
//  3. only then any data row.
//
// Ordinary writers take a subset of these in the same order, so an import
// cannot wait on a lock a writer holds while that writer waits on a row the
// import wrote. Import may take the same locks again; holding them already,
// that is a no-op.
type Preparer interface {
	Prepare(ctx context.Context, tx pgx.Tx, docs []Prepared) error
}

// PrepareAll takes every Preparer's locks for the documents, by type id,
// types in the order given (the registry's), after the caller took the
// account locks and before any data row.
func PrepareAll(ctx context.Context, tx pgx.Tx, types []Type, docs map[string][]Prepared) error {
	for _, t := range types {
		p, ok := t.(Preparer)
		if !ok || len(docs[t.ID()]) == 0 {
			continue
		}
		if err := p.Prepare(ctx, tx, docs[t.ID()]); err != nil {
			return err
		}
	}
	return nil
}

// MaxEntries bounds one data type's document.
const MaxEntries = 500_000

// DecodeStrict decodes one JSON value, refusing unknown fields and trailing
// data: documents come from backups and uploads, which are untrusted.
func DecodeStrict(data []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the document")
	}
	return nil
}

// CheckEntries refuses a document with more entries than MaxEntries.
func CheckEntries(kind string, n int) error {
	if n > MaxEntries {
		return fmt.Errorf("%s holds more than %d entries", kind, MaxEntries)
	}
	return nil
}

// ValidContentID accepts a content id: non-empty, bounded, printable ASCII
// without spaces.
func ValidContentID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}

// ValidText bounds a free-text field.
func ValidText(s string, limit int) bool { return len(s) <= limit }

// Find returns the type with the id among types.
func Find(types []Type, id string) (Type, bool) {
	for _, t := range types {
		if t.ID() == id {
			return t, true
		}
	}
	return nil, false
}

// RemapQueryLibraries rewrites the library ids a query or source definition
// names (library_ids, filter_library_ids, filter_library_id) through
// mapLibrary. An id with no library here is dropped from its list, which
// only narrows the definition. A definition that would lose its whole scope
// is refused (false) rather than widened: a list that names libraries but
// none here would read as "every library", and so would a missing
// filter_library_id.
func RemapQueryLibraries(raw json.RawMessage, mapLibrary func(int) (int, bool)) (json.RawMessage, bool) {
	var doc map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return raw, true
	}
	remap := func(v any) (int, bool) {
		n, ok := v.(float64)
		if !ok {
			return 0, false
		}
		return mapLibrary(int(n))
	}
	changed := false
	for _, key := range []string{"library_ids", "filter_library_ids"} {
		list, ok := doc[key].([]any)
		if !ok {
			continue
		}
		out := []any{}
		for _, v := range list {
			if local, ok := remap(v); ok {
				out = append(out, local)
			}
		}
		if len(list) > 0 && len(out) == 0 {
			return nil, false
		}
		doc[key], changed = out, true
	}
	if v, ok := doc["filter_library_id"]; ok && v != nil {
		local, ok := remap(v)
		if !ok {
			return nil, false
		}
		doc["filter_library_id"], changed = local, true
	}
	if !changed {
		return raw, true
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	return data, true
}

// SettleChanged counts the entries a Pending comparison found would change:
// added and updated name each entry's item by its source content id, and
// an entry whose item target.Items does not resolve counts as left out
// instead. Pending compares a document's content ids with this server's
// rows directly (a content id names the same item on every server) and
// asks Items only about the few entries that would change, so a preview
// never resolves a whole document.
func SettleChanged(ctx context.Context, items Items, res *Result, added, updated []string) error {
	if len(added)+len(updated) == 0 {
		return nil
	}
	local, err := ResolveAll(ctx, items, append(append([]string{}, added...), updated...))
	if err != nil {
		return err
	}
	for _, list := range []struct {
		ids   []string
		count *int
	}{{added, &res.Added}, {updated, &res.Updated}} {
		for _, id := range list.ids {
			if _, ok := local[id]; ok {
				*list.count++
			} else {
				res.LeftOut++
			}
		}
	}
	return nil
}

// PendingMissing counts, of ids, the entries the profile has no row for:
// sql takes the user id, the profile id, and the distinct ids as a text
// array, and returns the missing ones. A repeated id counts as unchanged,
// as an import's second insert of it changes nothing; a missing one is
// settled through target.Items (SettleChanged).
func PendingMissing(ctx context.Context, tx pgx.Tx, target Target, res *Result, sql string, ids []string) error {
	seen := map[string]bool{}
	distinct := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			res.Unchanged++
			continue
		}
		seen[id] = true
		distinct = append(distinct, id)
	}
	if len(distinct) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, sql, target.UserID, target.ProfileID, distinct)
	if err != nil {
		return err
	}
	missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	res.Unchanged += len(distinct) - len(missing)
	return SettleChanged(ctx, target.Items, res, missing, nil)
}
