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

// MapCarriedEach is MapCarried for each of ids, by id; an id it drops is
// missing from the result.
func MapCarriedEach(ctx context.Context, tx pgx.Tx, target userdata.Target, carried map[string]Carried, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		local, ok, err := MapCarried(ctx, tx, target, carried, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out[id] = local
		}
	}
	return out, nil
}

// MapCarried finds the personal collection here that a source collection
// id names. A collection the document carries maps to the one native
// collection with its name of its creator's profile here, the collection
// its import created or merged into: for a profile import, the importing
// profile's own copy, never the source profile's original. Otherwise, on
// the server that wrote it, the same collection when the importing profile
// can see it (its own, shared with it, or shared with the household).
// false when there is none or several; the reference is then dropped.
func MapCarried(ctx context.Context, tx pgx.Tx, target userdata.Target, carried map[string]Carried, id string) (string, bool, error) {
	if c, ok := carried[id]; ok {
		creator, mapped := target.ProfileID, true
		if c.Profile != "" {
			creator, mapped = target.MapProfile(c.Profile)
		}
		if !mapped {
			return "", false, nil
		}
		ids, err := collectionsNamed(ctx, tx, userdata.Owner{UserID: target.UserID, ProfileID: creator}, c.Name)
		if err != nil || len(ids) != 1 {
			return "", false, err
		}
		return ids[0], true, nil
	}
	if !target.SameServer {
		return "", false, nil
	}
	var found string
	err := tx.QueryRow(ctx, `SELECT c.id FROM user_personal_collections c WHERE c.id = $1 AND c.user_id = $2
		AND (c.creator_profile_id = $3 OR c.is_shared
			OR EXISTS (SELECT 1 FROM user_personal_collection_profiles s WHERE s.collection_id = c.id AND s.profile_id = $3))`,
		id, target.UserID, target.ProfileID).Scan(&found)
	switch {
	case err == nil:
		return found, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	}
	return "", false, fmt.Errorf("reading personal collections: %w", err)
}
