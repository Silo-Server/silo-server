package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
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
}

// NewProfileTransfer serves profile export and import over pool.
func NewProfileTransfer(pool *pgxpool.Pool, serverID func(context.Context) (string, error)) *ProfileTransfer {
	return &ProfileTransfer{pool: pool, serverID: serverID, now: time.Now}
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

// Export writes the profile's data, read in one snapshot.
func (t *ProfileTransfer) Export(ctx context.Context, userID int, profileID string) ([]byte, error) {
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
	var items []string
	for _, dt := range userDataTypes {
		exp, err := dt.Export(ctx, tx, userdata.Owner{UserID: userID, ProfileID: profileID})
		if err != nil {
			return nil, fmt.Errorf("exporting %s: %w", dt.ID(), err)
		}
		doc.Data[dt.ID()] = exp.Data
		items = append(items, exp.Items...)
	}
	if doc.Items, err = readItemRefs(ctx, tx, items); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

// ProfileImportScope is the signed-in profile an import writes to, from the
// session, and what it may see.
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
	// Unmatched lists, by title, items this catalog does not hold; Hidden
	// those the profile may not see here. Both were left out.
	Unmatched []string
	Hidden    []string
	// SameServer is true when the export came from this server.
	SameServer bool
}

// ParseProfileExport checks an untrusted document: its size, nesting,
// format and version, that it carries no account or access field, strict
// decoding, and each data type's own checks.
func ParseProfileExport(data []byte) (*ProfileExport, error) {
	if len(data) > MaxProfileImportBytes {
		return nil, ErrProfileImportTooLarge
	}
	if err := checkJSONDepth(data); err != nil {
		return nil, &InvalidProfileExportError{Err: err}
	}
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, &InvalidProfileExportError{Err: err}
	}
	if head.Format != ProfileExportFormat {
		return nil, &InvalidProfileExportError{Err: fmt.Errorf("format %q is not %s", head.Format, ProfileExportFormat)}
	}
	if head.Version > ProfileExportVersion {
		return nil, ErrProfileExportTooNew
	}
	if head.Version < 1 {
		return nil, &InvalidProfileExportError{Err: fmt.Errorf("version %d is not valid", head.Version)}
	}
	if err := refuseAccessFields(data); err != nil {
		return nil, err
	}
	var doc ProfileExport
	if err := decodeStrict(data, &doc); err != nil {
		return nil, &InvalidProfileExportError{Err: err}
	}
	if len(doc.ProfileName) > maxAccountField || (doc.ServerID != "" && !validServerID(doc.ServerID)) {
		return nil, &InvalidProfileExportError{Err: errors.New("the source is not valid")}
	}
	for id, raw := range doc.Data {
		dt, ok := userDataType(id)
		if !ok {
			return nil, &InvalidProfileExportError{Err: fmt.Errorf("unknown data %q", id)}
		}
		if _, err := dt.Validate(raw); err != nil {
			return nil, &InvalidProfileExportError{Err: fmt.Errorf("%s: %w", id, err)}
		}
	}
	if len(doc.Items) > userdata.MaxEntries {
		return nil, &InvalidProfileExportError{Err: errors.New("too many items")}
	}
	seen := map[string]bool{}
	for _, r := range doc.Items {
		if !userdata.ValidContentID(r.ContentID) || seen[r.ContentID] {
			return nil, &InvalidProfileExportError{Err: fmt.Errorf("item %q is not valid or is listed twice", r.ContentID)}
		}
		seen[r.ContentID] = true
	}
	return &doc, nil
}

// refuseAccessFields looks for account and access fields where a profile
// record would hold them: the top level and the profile preferences.
func refuseAccessFields(data []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return &InvalidProfileExportError{Err: err}
	}
	check := func(fields map[string]json.RawMessage) error {
		for key := range fields {
			if accessFieldNames[key] {
				return fmt.Errorf("%w (%s)", ErrProfileImportAccessFields, key)
			}
		}
		return nil
	}
	if err := check(top); err != nil {
		return err
	}
	var payload struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	var prefs struct {
		Profile map[string]json.RawMessage `json:"profile"`
	}
	// A data member that does not decode here fails strict decoding later.
	if json.Unmarshal(data, &payload) == nil {
		if raw, ok := payload.Data[userdata.Preferences]; ok && json.Unmarshal(raw, &prefs) == nil {
			return check(prefs.Profile)
		}
	}
	return nil
}

// Import merges a profile export into the signed-in profile, in one
// transaction, with the restore's merge rules.
func (t *ProfileTransfer) Import(ctx context.Context, scope ProfileImportScope, data []byte) (*ProfileImportResult, error) {
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
		prepared[id] = []userdata.Prepared{{Owner: userdata.Owner{UserID: scope.UserID, ProfileID: scope.ProfileID}, Data: raw}}
	}
	if err := prepareImport(ctx, tx, prepared); err != nil {
		return nil, err
	}
	result := &ProfileImportResult{SameServer: doc.ServerID == serverID, Data: []ReportData{}}
	var extra []string
	for id, raw := range doc.Data {
		dt, _ := userDataType(id)
		sum, _ := dt.Validate(raw)
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
	target := userdata.Target{
		Owner:      userdata.Owner{UserID: scope.UserID, ProfileID: scope.ProfileID},
		SameServer: result.SameServer, Items: visible, Now: t.now().UTC(),
		Library: func(id int) (int, bool) { return id, libraries[id] },
	}
	target.Collection = transferCollections(tx, target, doc.Data[userdata.Collections])
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
	result.Unmatched, result.Hidden = items.unmatchedTitles(), items.hiddenTitles()
	return result, nil
}

// transferCollections maps the collection ids a profile export names: its
// own personal collections as their import matches them, and an admin
// collection only on the server that wrote it, while it is still there.
func transferCollections(tx pgx.Tx, target userdata.Target, collections json.RawMessage) func(ctx context.Context, field, id string) (string, bool, error) {
	carried := usercollections.CarriedCollections(map[string]json.RawMessage{"": collections})
	return func(ctx context.Context, field, id string) (string, bool, error) {
		switch field {
		case userdata.UserCollectionField:
			return usercollections.MapCarried(ctx, tx, target, carried, id)
		case userdata.LibraryCollectionField:
			if !target.SameServer {
				return "", false, nil
			}
			var found bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM library_collections WHERE id = $1)`, id).Scan(&found); err != nil {
				return "", false, err
			}
			return id, found, nil
		}
		return "", false, nil
	}
}
