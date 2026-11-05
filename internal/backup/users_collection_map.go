package backup

// Mapping the collection ids a profile's page layouts name.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

// collectionMap maps the collection ids an account's data names: admin
// collections as this restore matches them, personal ones as the
// account's personal collections import matches them.
type collectionMap struct {
	p          *planner
	tx         pgx.Tx
	libMap     map[int]int
	sameServer bool
	// admin caches admin collection ids, "" for none here; the admin
	// collections do not change while accounts are restored or compared,
	// so it is shared across accounts.
	admin   map[string]string
	carried map[string]usercollections.Carried
}

// newCollectionMap maps over tx for one account, with an admin cache
// shared across accounts.
func newCollectionMap(p *planner, tx pgx.Tx, libMap map[int]int, sameServer bool, admin map[string]string, doc *UserDocument) *collectionMap {
	docs := map[string]json.RawMessage{}
	for _, e := range doc.Profiles {
		if data, ok := e.Data[userdata.Collections]; ok {
			docs[e.ID] = data
		}
	}
	return &collectionMap{p: p, tx: tx, libMap: libMap, sameServer: sameServer, admin: admin, carried: usercollections.CarriedCollections(docs)}
}

// lookup is Target.Collections for target.
func (m *collectionMap) lookup(target userdata.Target) func(ctx context.Context, field string, ids []string) (map[string]string, error) {
	return func(ctx context.Context, field string, ids []string) (map[string]string, error) {
		switch field {
		case userdata.LibraryCollectionField:
			return m.adminCollections(ctx, ids)
		case userdata.UserCollectionField:
			return usercollections.MapCarriedEach(ctx, m.tx, target, m.carried, ids)
		}
		return map[string]string{}, nil
	}
}

// previewLookup is lookup for a preview, which writes nothing: a personal
// collection the account's documents carry but this server does not have
// yet maps to a stand-in, because the restore creates it (all profiles'
// collections before any profile's other data) before anything names it.
func (m *collectionMap) previewLookup(target userdata.Target) func(ctx context.Context, field string, ids []string) (map[string]string, error) {
	lookup := m.lookup(target)
	return func(ctx context.Context, field string, ids []string) (map[string]string, error) {
		mapped, err := lookup(ctx, field, ids)
		if err != nil || field != userdata.UserCollectionField {
			return mapped, err
		}
		for _, id := range ids {
			if _, ok := mapped[id]; !ok {
				if _, carried := m.carried[id]; carried {
					mapped[id] = pendingCollectionPrefix + id
				}
			}
		}
		return mapped, nil
	}
}

// pendingCollectionPrefix marks a preview's stand-in for a personal
// collection the restore will create; no stored id has it.
const pendingCollectionPrefix = "restore-pending:"

// adminCollections maps admin collection ids as the restore's collections
// and sections match them: a carried collection or template collection, a
// section-managed collection through its section, and on the server that
// wrote the backup any collection still here by its id. Ids the backup
// does not name are looked up here in one read, not one per id.
func (m *collectionMap) adminCollections(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	index := m.p.rs.lookup()
	var here []string
	for _, id := range ids {
		local, cached := m.admin[id]
		if !cached {
			_, collection := index.collectionIDs[id]
			_, template := index.templateIDs[id]
			_, section := index.sectionCollections[id]
			if !collection && !template && !section {
				here = append(here, id)
				continue
			}
			var err error
			if local, _, err = m.p.resolveCollection(ctx, id, m.libMap, m.sameServer); err == nil && local == "" {
				local, err = m.sectionCollection(ctx, id)
			}
			if err != nil {
				return nil, err
			}
			m.admin[id] = local
		}
		if local != "" {
			out[id] = local
		}
	}
	if len(here) == 0 {
		return out, nil
	}
	var found []string
	if m.sameServer {
		rows, err := m.p.q.Query(ctx, `SELECT id FROM library_collections WHERE id = ANY($1)`, here)
		if err != nil {
			return nil, fmt.Errorf("reading collections: %w", err)
		}
		if found, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return nil, fmt.Errorf("reading collections: %w", err)
		}
	}
	for _, id := range here {
		m.admin[id] = ""
	}
	for _, id := range found {
		m.admin[id], out[id] = id, id
	}
	return out, nil
}

// sectionCollection maps the collection a carried section manages to the
// one its section here shows.
func (m *collectionMap) sectionCollection(ctx context.Context, id string) (string, error) {
	e := indexed(m.p.rs.HomeSections.Sections, m.p.rs.lookup().sectionCollections, id)
	if e == nil {
		return "", nil
	}
	library := mappedLibrary(e.LibraryID, m.libMap)
	if e.LibraryID != nil && library == nil {
		return "", nil
	}
	local, _, err := m.p.matchSection(ctx, *e, library, m.libMap, m.sameServer)
	if err != nil || local == nil {
		return "", err
	}
	return sectionCollectionID(local.Config), nil
}
