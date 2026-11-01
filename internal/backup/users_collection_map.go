package backup

// Mapping the collection ids a profile's page layouts name.

import (
	"context"
	"encoding/json"

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

// lookup is Target.Collection for target.
func (m *collectionMap) lookup(target userdata.Target) func(ctx context.Context, field, id string) (string, bool, error) {
	return func(ctx context.Context, field, id string) (string, bool, error) {
		switch field {
		case userdata.LibraryCollectionField:
			return m.adminCollection(ctx, id)
		case userdata.UserCollectionField:
			return usercollections.MapCarried(ctx, m.tx, target, m.carried, id)
		}
		return "", false, nil
	}
}

// adminCollection maps an admin collection id as the restore's collections
// and sections match it: a carried collection or template collection, a
// section-managed collection through its section, and on the server that
// wrote the backup any collection still here by its id.
func (m *collectionMap) adminCollection(ctx context.Context, id string) (string, bool, error) {
	if local, ok := m.admin[id]; ok {
		return local, local != "", nil
	}
	local, _, err := m.p.resolveCollection(ctx, id, m.libMap, m.sameServer)
	if err == nil && local == "" {
		local, err = m.sectionCollection(ctx, id)
	}
	if err != nil {
		return "", false, err
	}
	m.admin[id] = local
	return local, local != "", nil
}

// sectionCollection maps the collection a carried section manages to the
// one its section here shows.
func (m *collectionMap) sectionCollection(ctx context.Context, id string) (string, error) {
	for _, e := range m.p.rs.HomeSections.Sections {
		if e.Collection == nil || e.Collection.ID != id {
			continue
		}
		library := mappedLibrary(e.LibraryID, m.libMap)
		if e.LibraryID != nil && library == nil {
			return "", nil
		}
		local, _, err := m.p.matchSection(ctx, e, library, m.libMap, m.sameServer)
		if err != nil || local == nil {
			return "", err
		}
		return sectionCollectionID(local.Config), nil
	}
	return "", nil
}
