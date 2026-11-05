package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/backup/jsonsize"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// A profile export is one profile's own data, which the profile downloads
// and imports itself: a JSON document of the data types a restore set
// carries for a profile, and nothing else. It holds no account fields, no
// password or PIN hash, no access fields, and no allowed libraries, and an
// import refuses a document that carries any of them. The target account
// and profile always come from the session, never from the document.
const (
	ProfileExportFormat  = "silo-profile-export"
	ProfileExportVersion = 1
	// MaxProfileImportBytes bounds an import's document.
	MaxProfileImportBytes = 64 << 20
	// maxProfileItems bounds the items an export names, its items list and
	// every id its data types name together, once each: a profile's
	// history, ratings, and lists name a few thousand titles, tens of
	// thousands at most.
	maxProfileItems = 100_000
)

// ProfileExport is the silo-profile-export document.
type ProfileExport struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// ServerID is the exporting server. Library references (hidden
	// libraries, per-library preferences) are kept only when importing on
	// the same server.
	ServerID    string                     `json:"server_id"`
	ExportedAt  time.Time                  `json:"exported_at"`
	ProfileName string                     `json:"profile_name"`
	Data        map[string]json.RawMessage `json:"data"`
	Items       []ItemRef                  `json:"items"`
	// summaries are each data type's checks, kept by ParseProfileExport.
	summaries map[string]userdata.Summary
}

// Errors a profile import answers with.
var (
	ErrProfileExportTooNew = errors.New("this profile export is from a newer Silo; upgrade this server first")
	// ErrProfileImportAccessFields refuses a document carrying account or
	// access fields: an import only ever merges a profile's own data.
	ErrProfileImportAccessFields = errors.New("a profile import cannot carry account or access fields")
	ErrProfileImportTooLarge     = fmt.Errorf("a profile import is limited to %d bytes", MaxProfileImportBytes)
	ErrProfileNotFound           = errors.New("profile not found")
)

// InvalidProfileExportError is a document that is not a valid profile
// export.
type InvalidProfileExportError struct{ Err error }

func (e *InvalidProfileExportError) Error() string {
	return "not a valid profile export: " + e.Err.Error()
}
func (e *InvalidProfileExportError) Unwrap() error { return e.Err }

// accessFieldNames are fields of account and profile records that a profile
// export must never carry, at its top level or in its profile preferences.
var accessFieldNames = func() map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Fields(`account accounts profiles profile username email password_hash pin_hash role
		permissions policy group effective_policy library_ids allowed_library_ids library_restrictions_enabled
		max_content_rating max_advisory_age require_advisory_age max_playback_quality is_child is_primary is_owner`) {
		out[name] = true
	}
	return out
}()

// ProfileTransfer exports and imports one profile's data.
type ProfileTransfer struct {
	pool     *pgxpool.Pool
	serverID func(context.Context) (string, error)
	now      func() time.Time
	// slots admits exports and imports before they read a document or take
	// a pool connection: at most maxConcurrentTransfers on this server, and
	// one per account.
	slots        chan struct{}
	mu           sync.Mutex
	transferring map[int]bool
	// itemsChanged hears, after an import commits, which items a profile's
	// data now names (SetItemsChanged).
	itemsChanged ItemsChanged
}

// ItemsChanged tells the rest of the server, after a commit, that a
// profile's data about itemIDs changed outside its normal write paths: the
// notification interest of those series is recomputed, as the user store's
// own writes trigger it (notifications.WrapUserStoreProvider).
type ItemsChanged func(userID int, profileID string, itemIDs []string)

// SetItemsChanged sets the hook an import calls after its commit.
func (t *ProfileTransfer) SetItemsChanged(f ItemsChanged) { t.itemsChanged = f }

// maxConcurrentTransfers bounds the profile exports and imports one server
// runs at once. Each holds a pool connection for its whole transaction and
// a whole document in memory, so transfers arriving together must not take
// the pool or the memory other requests need.
const maxConcurrentTransfers = 2

// ErrProfileTransferBusy refuses an export or import while another runs
// for the same account, or while this server already runs
// maxConcurrentTransfers.
var ErrProfileTransferBusy = errors.New("another profile export or import is running; try again when it finishes")

// NewProfileTransfer serves profile export and import over pool.
func NewProfileTransfer(pool *pgxpool.Pool, serverID func(context.Context) (string, error)) *ProfileTransfer {
	return &ProfileTransfer{pool: pool, serverID: serverID, now: time.Now,
		slots: make(chan struct{}, maxConcurrentTransfers), transferring: map[int]bool{}}
}

// admit takes a transfer slot for userID without waiting, or refuses with
// ErrProfileTransferBusy. release gives it back.
func (t *ProfileTransfer) admit(userID int) (release func(), err error) {
	select {
	case t.slots <- struct{}{}:
	default:
		return nil, ErrProfileTransferBusy
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.transferring[userID] {
		<-t.slots
		return nil, ErrProfileTransferBusy
	}
	t.transferring[userID] = true
	return func() {
		t.mu.Lock()
		delete(t.transferring, userID)
		t.mu.Unlock()
		<-t.slots
	}, nil
}

// admittedKey marks a context whose caller holds userID's transfer slot.
type admittedKey struct{}

// Admit takes userID's transfer slot before the caller reads an import's
// body, so a refused import buffers nothing. The returned context carries
// it to Import, which then does not take it again; release gives it back
// once Import has returned.
func (t *ProfileTransfer) Admit(ctx context.Context, userID int) (context.Context, func(), error) {
	release, err := t.admit(userID)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, admittedKey{}, userID), release, nil
}

// slotFor takes userID's slot unless ctx already holds it (Admit).
func (t *ProfileTransfer) slotFor(ctx context.Context, userID int) (func(), error) {
	if held, ok := ctx.Value(admittedKey{}).(int); ok && held == userID {
		return func() {}, nil
	}
	return t.admit(userID)
}

// ProfileExportCapability describes profile export and import for clients.
type ProfileExportCapability struct {
	Format         string
	Version        int
	MaxImportBytes int
	DataTypes      []string
}

// Capability reports the format this server writes and reads.
func (t *ProfileTransfer) Capability() ProfileExportCapability {
	return ProfileExportCapability{Format: ProfileExportFormat, Version: ProfileExportVersion, MaxImportBytes: MaxProfileImportBytes, DataTypes: UserDataTypeIDs()}
}

// ErrProfileExportTooLarge refuses an export larger than an import
// accepts: no server could import it.
var ErrProfileExportTooLarge = fmt.Errorf("this profile's data is larger than an import accepts (%d bytes, %d items)", MaxProfileImportBytes, maxProfileItems)

// Export writes the profile's data, read in one snapshot, as the profile
// may see it now: rows about titles its library access or maturity limits
// hide are left out, as every other read of its data leaves them out. It
// takes the account's transfer slot first, so exports cannot crowd out the
// pool, and stops once the document is larger than an import accepts.
func (t *ProfileTransfer) Export(ctx context.Context, scope ProfileImportScope) ([]byte, error) {
	userID, profileID := scope.UserID, scope.ProfileID
	release, err := t.slotFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer release()
	serverID, err := t.serverID(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := t.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	doc := ProfileExport{Format: ProfileExportFormat, Version: ProfileExportVersion, ServerID: serverID, ExportedAt: t.now().UTC(),
		Data: map[string]json.RawMessage{}}
	err = tx.QueryRow(ctx, `SELECT name FROM user_profiles WHERE user_id = $1 AND id = $2`, userID, profileID).Scan(&doc.ProfileName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProfileNotFound
	}
	if err != nil {
		return nil, err
	}
	owner := userdata.Owner{UserID: userID, ProfileID: profileID}
	keep, err := exportVisibility(ctx, tx, owner, scope)
	if err != nil {
		return nil, err
	}
	var items []string
	size := 0
	for _, dt := range userDataTypes {
		exp, err := dt.Export(ctx, tx, owner, keep)
		if err != nil {
			return nil, fmt.Errorf("exporting %s: %w", dt.ID(), err)
		}
		if size += len(exp.Data); size > MaxProfileImportBytes {
			return nil, ErrProfileExportTooLarge
		}
		doc.Data[dt.ID()] = exp.Data
		items = append(items, exp.Items...)
	}
	if doc.Items, err = readItemRefs(ctx, tx, items); err != nil {
		return nil, err
	}
	// The count an import checks: every item the data names, once.
	slices.Sort(items)
	if len(slices.Compact(items)) > maxProfileItems {
		return nil, ErrProfileExportTooLarge
	}
	out, err := json.Marshal(doc)
	if err == nil && len(out) > MaxProfileImportBytes {
		return nil, ErrProfileExportTooLarge
	}
	return out, err
}

// exportVisibility is the Keep of a profile's export: every item the
// profile's data names, read in the export's snapshot, checked once against
// the catalog visibility the importer applies (contentFilter), and the
// profile's rating ceiling for watchlisted titles outside the library. The
// first pass only records the items each type asks about and keeps no row,
// so the data is held in memory once.
func exportVisibility(ctx context.Context, tx pgx.Tx, owner userdata.Owner, scope ProfileImportScope) (userdata.Keep, error) {
	keep := userdata.Keep{MaxContentRating: scope.Maturity.MaxContentRating}
	var ids []string
	record := userdata.Keep{Item: func(id string) bool { ids = append(ids, id); return false }}
	for _, dt := range userDataTypes {
		if _, err := dt.Export(ctx, tx, owner, record); err != nil {
			return keep, fmt.Errorf("exporting %s: %w", dt.ID(), err)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return keep, nil
	}
	visible, err := catalog.FilterAccessibleContentIDsInTransaction(ctx, tx, ids, scope.AllowedLibraryIDs, nil, scope.Maturity)
	if err != nil {
		return keep, fmt.Errorf("checking access to items: %w", err)
	}
	// An id the catalog no longer holds hides nothing: no read can show it,
	// and the export keeps the profile's own record of it for another server.
	known, err := newItemResolver(ctx, tx, nil, ids)
	if err != nil {
		return keep, err
	}
	keep.Item = func(id string) bool { return visible[id] || !known.present[id] }
	return keep, nil
}

// ProfileImportScope is the signed-in profile an import writes to or an
// export reads, from the session, and what it may see.
type ProfileImportScope struct {
	UserID    int
	ProfileID string
	// AllowedLibraryIDs are the libraries the profile can see; nil when it
	// is unrestricted. Items and library preferences outside them are left
	// out.
	AllowedLibraryIDs []int
	// Maturity is the profile's content-rating and advisory limits (the
	// viewer scope's MaturityLimits): items they hide are left out too.
	Maturity access.MaturityLimits
}

// ProfileImportResult is what an import did, per data type.
type ProfileImportResult struct {
	Data []ReportData
	// Unmatched lists, by title, items left out because this catalog does
	// not hold them or the profile may not see them; Hidden is always empty
	// (see Import).
	Unmatched []string
	Hidden    []string
	// SameServer is true when the export came from this server.
	SameServer bool
}

// ParseProfileExport checks an untrusted document: its size, nesting,
// format and version, that it carries no account or access field, strict
// decoding, and each data type's own checks. It decodes the document once
// into its top-level members and once strictly; each data type's own
// checks read its member once.
func ParseProfileExport(data []byte) (*ProfileExport, error) {
	if len(data) > MaxProfileImportBytes {
		return nil, ErrProfileImportTooLarge
	}
	// Both decodes are bounded before they run (jsonsize): the document's
	// members as a map, then the document strictly.
	var top map[string]json.RawMessage
	if _, err := jsonsize.Check(data, &top, decodeLimits(len(data))); err != nil {
		return nil, &InvalidProfileExportError{Err: err}
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, &InvalidProfileExportError{Err: err}
	}
	var head struct {
		Format  string
		Version int
	}
	// A member of the wrong type fails strict decoding below.
	_ = json.Unmarshal(top["format"], &head.Format)
	_ = json.Unmarshal(top["version"], &head.Version)
	if head.Format != ProfileExportFormat {
		return nil, &InvalidProfileExportError{Err: fmt.Errorf("format %q is not %s", head.Format, ProfileExportFormat)}
	}
	if head.Version > ProfileExportVersion {
		return nil, ErrProfileExportTooNew
	}
	if head.Version < 1 {
		return nil, &InvalidProfileExportError{Err: fmt.Errorf("version %d is not valid", head.Version)}
	}
	if err := refuseAccessFields(top); err != nil {
		return nil, err
	}
	var doc ProfileExport
	if _, err := jsonsize.Check(data, &doc, decodeLimits(len(data))); err != nil {
		return nil, &InvalidProfileExportError{Err: err}
	}
	if err := decodeStrict(data, &doc); err != nil {
		return nil, &InvalidProfileExportError{Err: err}
	}
	if err := refusePreferenceAccessFields(doc.Data[userdata.Preferences]); err != nil {
		return nil, err
	}
	if len(doc.ProfileName) > maxAccountField || (doc.ServerID != "" && !validServerID(doc.ServerID)) {
		return nil, &InvalidProfileExportError{Err: errors.New("the source is not valid")}
	}
	doc.summaries = map[string]userdata.Summary{}
	for id, raw := range doc.Data {
		dt, ok := userDataType(id)
		if !ok {
			return nil, &InvalidProfileExportError{Err: fmt.Errorf("unknown data %q", id)}
		}
		sum, err := dt.Validate(raw)
		if err != nil {
			return nil, &InvalidProfileExportError{Err: fmt.Errorf("%s: %w", id, err)}
		}
		doc.summaries[id] = sum
	}
	seen := map[string]bool{}
	for _, r := range doc.Items {
		if !userdata.ValidContentID(r.ContentID) || seen[r.ContentID] {
			return nil, &InvalidProfileExportError{Err: fmt.Errorf("item %q is not valid or is listed twice", r.ContentID)}
		}
		seen[r.ContentID] = true
	}
	// Every item the import reads about, once each: the refs and every id
	// a data type names, which the import resolves in its transaction.
	named := len(seen)
	for _, sum := range doc.summaries {
		for _, id := range sum.Items {
			if !seen[id] {
				seen[id] = true
				named++
			}
		}
	}
	if named > maxProfileItems {
		return nil, &InvalidProfileExportError{Err: fmt.Errorf("names more than %d items", maxProfileItems)}
	}
	return &doc, nil
}

// refuseAccessFields refuses account and access fields among a document's
// top-level members.
func refuseAccessFields(fields map[string]json.RawMessage) error {
	for key := range fields {
		if accessFieldNames[key] {
			return fmt.Errorf("%w (%s)", ErrProfileImportAccessFields, key)
		}
	}
	return nil
}

// refusePreferenceAccessFields refuses them among the profile preferences,
// where a profile record would hold them. Preferences that do not decode
// here fail their own checks.
func refusePreferenceAccessFields(raw json.RawMessage) error {
	var prefs struct {
		Profile map[string]json.RawMessage `json:"profile"`
	}
	if raw != nil && json.Unmarshal(raw, &prefs) == nil {
		return refuseAccessFields(prefs.Profile)
	}
	return nil
}

// Import merges a profile export into the signed-in profile, in one
// transaction, with the restore's merge rules.
func (t *ProfileTransfer) Import(ctx context.Context, scope ProfileImportScope, data []byte) (*ProfileImportResult, error) {
	release, err := t.slotFor(ctx, scope.UserID)
	if err != nil {
		return nil, err
	}
	defer release()
	doc, err := ParseProfileExport(data)
	if err != nil {
		return nil, err
	}
	serverID, err := t.serverID(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_profiles WHERE user_id = $1 AND id = $2)`, scope.UserID, scope.ProfileID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrProfileNotFound
	}
	// The same lock order as a selective restore (userdata.Preparer): the
	// account locks, then each type's own, then data.
	if err := pgstore.LockAccountForImport(ctx, tx, scope.UserID); err != nil {
		return nil, err
	}
	prepared := map[string][]userdata.Prepared{}
	for id, raw := range doc.Data {
		prepared[id] = []userdata.Prepared{{Owner: userdata.Owner{UserID: scope.UserID, ProfileID: scope.ProfileID}, Data: raw, Untrusted: true}}
	}
	if err := prepareImport(ctx, tx, prepared); err != nil {
		return nil, err
	}
	result := &ProfileImportResult{SameServer: doc.ServerID == serverID, Data: []ReportData{}}
	var extra []string
	for _, sum := range doc.summaries {
		extra = append(extra, sum.Items...)
	}
	items, err := newItemResolver(ctx, tx, doc.Items, extra)
	if err != nil {
		return nil, err
	}
	// Library references survive only on the server that wrote them, and
	// only for libraries the profile can see.
	libraries := map[int]bool{}
	if result.SameServer {
		ids, err := collectIntsArgs(ctx, tx, `SELECT id FROM media_folders`)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			libraries[id] = true
		}
	}
	visible, err := items.visibleTo(ctx, tx, contentFilter{libraries: scope.AllowedLibraryIDs, limits: scope.Maturity})
	if err != nil {
		return nil, err
	}
	if scope.AllowedLibraryIDs != nil {
		allowed := map[int]bool{}
		for _, id := range scope.AllowedLibraryIDs {
			allowed[id] = libraries[id]
		}
		libraries = allowed
	}
	recorded := &userdata.RecordingItems{Inner: visible}
	target := userdata.Target{
		Owner:      userdata.Owner{UserID: scope.UserID, ProfileID: scope.ProfileID},
		SameServer: result.SameServer, Items: recorded, Now: t.now().UTC(),
		Library:   func(id int) (int, bool) { return id, libraries[id] },
		Untrusted: true,
	}
	target.Collections = transferCollections(tx, target, doc.Data[userdata.Collections], scope.AllowedLibraryIDs)
	for _, dt := range userDataTypes {
		raw, ok := doc.Data[dt.ID()]
		if !ok {
			continue
		}
		res, err := dt.Import(ctx, tx, target, raw)
		if err != nil {
			return nil, fmt.Errorf("importing %s: %w", dt.ID(), err)
		}
		result.Data = append(result.Data, ReportData{Type: dt.ID(), Result: res})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if t.itemsChanged != nil {
		t.itemsChanged(scope.UserID, scope.ProfileID, recorded.Resolved())
	}
	// One list for both: telling "not here" from "here but hidden" would let
	// a restricted profile probe which titles its hidden libraries hold.
	// Both lists are titles the document itself names.
	result.Unmatched, result.Hidden = leftOutTitles(items.unmatchedTitles(), items.hiddenTitles()), []string{}
	return result, nil
}

// transferCollections maps the collection ids a profile export names: its
// own personal collections as their import matches them, and an admin
// collection only on the server that wrote it, while it is still there and
// the profile may see it (catalog.CanAccessLibraryCollection, the check a
// sort preference's save makes). A collection it may not see is treated as
// one that is gone, so an import cannot probe which hidden ones exist.
func transferCollections(tx pgx.Tx, target userdata.Target, collections json.RawMessage, allowedLibraries []int) func(ctx context.Context, field string, ids []string) (map[string]string, error) {
	carried := usercollections.CarriedCollections(map[string]json.RawMessage{"": collections})
	return func(ctx context.Context, field string, ids []string) (map[string]string, error) {
		switch field {
		case userdata.UserCollectionField:
			return usercollections.MapCarriedEach(ctx, tx, target, carried, ids)
		case userdata.LibraryCollectionField:
			if !target.SameServer {
				return map[string]string{}, nil
			}
			return accessibleLibraryCollections(ctx, tx, ids, allowedLibraries)
		}
		return map[string]string{}, nil
	}
}

// accessibleLibraryCollections maps each of ids that names an admin
// collection the profile may see to itself.
func accessibleLibraryCollections(ctx context.Context, tx pgx.Tx, ids []string, allowedLibraries []int) (map[string]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT lc.id, lc.visibility, lc.library_id,
			COALESCE(ARRAY_AGG(lcl.library_id) FILTER (WHERE lcl.library_id IS NOT NULL), ARRAY[]::integer[])
		FROM library_collections lc
		LEFT JOIN library_collection_libraries lcl ON lcl.collection_id = lc.id
		WHERE lc.id = ANY($1)
		GROUP BY lc.id`, ids)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.LibraryCollection, error) {
		var c models.LibraryCollection
		var libraryIDs []int32
		err := row.Scan(&c.ID, &c.Visibility, &c.LibraryID, &libraryIDs)
		for _, id := range libraryIDs {
			c.LibraryIDs = append(c.LibraryIDs, int(id))
		}
		if len(c.LibraryIDs) == 0 && c.LibraryID > 0 {
			c.LibraryIDs = []int{c.LibraryID}
		}
		return c, err
	})
	out := map[string]string{}
	access := catalog.AccessFilter{AllowedLibraryIDs: allowedLibraries}
	for i := range found {
		if catalog.CanAccessLibraryCollection(&found[i], access) {
			out[found[i].ID] = found[i].ID
		}
	}
	return out, err
}

// leftOutTitles merges title lists, sorted and without repeats.
func leftOutTitles(lists ...[]string) []string {
	out := []string{}
	for _, list := range lists {
		out = append(out, list...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
