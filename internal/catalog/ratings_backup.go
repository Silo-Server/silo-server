package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// RatingsBackupData is a profile's own ratings as backups and profile
// exports carry them (userdata.Ratings). A rating is taken only for an item
// the profile has not rated here.
type RatingsBackupData struct{}

func (RatingsBackupData) ID() string       { return userdata.Ratings }
func (RatingsBackupData) Tables() []string { return []string{"user_ratings"} }

type ratingsDocument struct {
	Ratings []ratingEntry `json:"ratings"`
}

type ratingEntry struct {
	Item    string    `json:"item"`
	Rating  int       `json:"rating"`
	RatedAt time.Time `json:"rated_at"`
}

func (RatingsBackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner, keep userdata.Keep) (userdata.Export, error) {
	rows, err := tx.Query(ctx, `SELECT media_item_id, rating, rated_at FROM user_ratings WHERE user_id = $1 AND profile_id = $2 ORDER BY media_item_id`,
		owner.UserID, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading ratings: %w", err)
	}
	defer rows.Close()
	doc := ratingsDocument{Ratings: []ratingEntry{}}
	var items []string
	for rows.Next() {
		var e ratingEntry
		if err := rows.Scan(&e.Item, &e.Rating, &e.RatedAt); err != nil {
			return userdata.Export{}, err
		}
		if !keep.Has(e.Item) {
			continue
		}
		doc.Ratings = append(doc.Ratings, e)
		items = append(items, e.Item)
	}
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	data, err := json.Marshal(doc)
	return userdata.Export{Data: data, Count: len(doc.Ratings), Items: items}, err
}

func decodeRatings(data json.RawMessage) (ratingsDocument, userdata.Summary, error) {
	var doc ratingsDocument
	if err := userdata.DecodeStrict(data, &doc); err != nil {
		return doc, userdata.Summary{}, err
	}
	if err := userdata.CheckEntries("ratings", len(doc.Ratings)); err != nil {
		return doc, userdata.Summary{}, err
	}
	sum := userdata.Summary{Count: len(doc.Ratings)}
	for _, e := range doc.Ratings {
		if !userdata.ValidContentID(e.Item) || e.Rating < 1 || e.Rating > 5 {
			return doc, sum, fmt.Errorf("rating of %q is not valid", e.Item)
		}
		sum.Items = append(sum.Items, e.Item)
	}
	return doc, sum, nil
}

func (RatingsBackupData) Validate(data json.RawMessage) (userdata.Summary, error) {
	_, sum, err := decodeRatings(data)
	return sum, err
}

func (RatingsBackupData) Import(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeRatings(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	for _, e := range doc.Ratings {
		local, ok, err := target.Items.Resolve(ctx, e.Item)
		if err != nil {
			return res, err
		}
		if !ok {
			res.LeftOut++
			continue
		}
		ratedAt := e.RatedAt
		if ratedAt.IsZero() {
			ratedAt = target.Now
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating, rated_at) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (user_id, profile_id, media_item_id) DO NOTHING`,
			target.UserID, target.ProfileID, local, e.Rating, ratedAt)
		if err != nil {
			return res, fmt.Errorf("restoring a rating: %w", err)
		}
		if tag.RowsAffected() > 0 {
			res.Added++
		} else {
			res.Unchanged++
		}
	}
	return res, nil
}

// Pending counts the ratings Import would add: those of items the profile
// has not rated here.
func (RatingsBackupData) Pending(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeRatings(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	ids := make([]string, 0, len(doc.Ratings))
	for _, e := range doc.Ratings {
		ids = append(ids, e.Item)
	}
	if err := userdata.PendingMissing(ctx, tx, target, &res, `
		SELECT x.item FROM unnest($3::text[]) AS x(item)
		WHERE NOT EXISTS (SELECT 1 FROM user_ratings r WHERE r.user_id = $1 AND r.profile_id = $2 AND r.media_item_id = x.item)`, ids); err != nil {
		return res, fmt.Errorf("comparing ratings: %w", err)
	}
	return res, nil
}
