package usercollections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// Carried is a personal collection a backup carries: the source profile
// whose collections document has it (its creator; "" for the profile being
// imported, as a profile export names no profile id) and its name, which
// is how an import matches it.
type Carried struct {
	Profile string
	Name    string
}

// CarriedCollections indexes the personal collections of an account's
// collections documents, by source profile id, by collection id. A
// document that does not decode carries none.
func CarriedCollections(docs map[string]json.RawMessage) map[string]Carried {
	out := map[string]Carried{}
	for profile, data := range docs {
		doc, _, err := decodeCollections(data)
		if err != nil {
			continue
		}
		for _, c := range doc.Collections {
			out[c.ID] = Carried{Profile: profile, Name: c.Name}
		}
	}
	return out
}

// MapCarried finds the personal collection here that a source collection
// id names: on the server that wrote it, the same collection when the
// account still has it; otherwise the one native collection with its name
// of its creator's profile here, the collection an import of it creates or
// merges into. false when there is none or several.
func MapCarried(ctx context.Context, tx pgx.Tx, target userdata.Target, carried map[string]Carried, id string) (string, bool, error) {
	if target.SameServer {
		var found string
		err := tx.QueryRow(ctx, `SELECT id FROM user_personal_collections WHERE id = $1 AND user_id = $2`, id, target.UserID).Scan(&found)
		if err == nil {
			return found, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, fmt.Errorf("reading personal collections: %w", err)
		}
	}
	c, ok := carried[id]
	if !ok {
		return "", false, nil
	}
	creator, ok := target.ProfileID, true
	if c.Profile != "" {
		creator, ok = target.MapProfile(c.Profile)
	}
	if !ok {
		return "", false, nil
	}
	ids, err := collectionsNamed(ctx, tx, userdata.Owner{UserID: target.UserID, ProfileID: creator}, c.Name)
	if err != nil || len(ids) != 1 {
		return "", false, err
	}
	return ids[0], true, nil
}
