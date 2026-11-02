package watchstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// BackupData is a profile's watch history, resume progress, and history
// hides as backups and profile exports carry them (userdata.WatchHistory).
//
// Merge rules: a progress row is taken when this server has none for the
// item or the backup's is newer; a play is added unless the profile already
// has a play of the item at the same second; a history hide keeps the later
// cut-off. Nothing is deleted.
type BackupData struct{}

func (BackupData) ID() string { return userdata.WatchHistory }

func (BackupData) Tables() []string {
	return []string{"user_watch_history", "user_watch_progress", "user_history_hidden_items"}
}

type watchHistoryDocument struct {
	Progress []watchProgressEntry `json:"progress"`
	History  []watchPlayEntry     `json:"history"`
	Hidden   []watchHiddenEntry   `json:"hidden"`
}

type watchProgressEntry struct {
	Item             string     `json:"item"`
	Position         float64    `json:"position_seconds"`
	Duration         float64    `json:"duration_seconds"`
	Completed        bool       `json:"completed"`
	UpdatedAt        time.Time  `json:"updated_at"`
	EventAt          *time.Time `json:"event_at"`
	HideFromContinue bool       `json:"hide_from_continue"`
	// LastFileID is a file id of the source server; it is restored only
	// there.
	LastFileID     *int    `json:"last_file_id"`
	LastResolution *string `json:"last_resolution"`
	LastHDR        *bool   `json:"last_hdr"`
	LastCodecVideo *string `json:"last_codec_video"`
	LastEditionKey *string `json:"last_edition_key"`
}

type watchPlayEntry struct {
	Item      string                  `json:"item"`
	WatchedAt time.Time               `json:"watched_at"`
	Duration  *float64                `json:"duration_seconds"`
	Completed bool                    `json:"completed"`
	Source    string                  `json:"source"`
	Identity  userstore.WatchIdentity `json:"identity"`
}

// watchHiddenEntry is one history hide. Its cutoffs are timestamps as
// Postgres stores them, which includes -infinity and infinity: a mark
// unwatched stores removed_before -infinity ("nothing removed from the
// listing"). JSON spells those "-infinity" and "infinity". An export always
// writes removed_before; a legacy NULL, which means "the same as
// hidden_before", is written as that value. null on import means the same.
type watchHiddenEntry struct {
	Item          string             `json:"item"`
	HiddenBefore  pgtype.Timestamptz `json:"hidden_before"`
	UpdatedAt     time.Time          `json:"updated_at"`
	RemovedBefore pgtype.Timestamptz `json:"removed_before"`
}

// removedBefore is the hide's removed_before, reading null as the
// hidden_before it stands for.
func (e watchHiddenEntry) removedBefore() pgtype.Timestamptz {
	if !e.RemovedBefore.Valid {
		return e.HiddenBefore
	}
	return e.RemovedBefore
}

func (BackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner, keep userdata.Keep) (userdata.Export, error) {
	doc := watchHistoryDocument{Progress: []watchProgressEntry{}, History: []watchPlayEntry{}, Hidden: []watchHiddenEntry{}}
	items := map[string]bool{}
	rows, err := tx.Query(ctx, `
		SELECT media_item_id, position_seconds, duration_seconds, completed, updated_at, event_at, hide_from_continue,
			last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
		FROM user_watch_progress WHERE user_id = $1 AND profile_id = $2 ORDER BY media_item_id`, owner.UserID, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading watch progress: %w", err)
	}
	for rows.Next() {
		var e watchProgressEntry
		if err := rows.Scan(&e.Item, &e.Position, &e.Duration, &e.Completed, &e.UpdatedAt, &e.EventAt, &e.HideFromContinue,
			&e.LastFileID, &e.LastResolution, &e.LastHDR, &e.LastCodecVideo, &e.LastEditionKey); err != nil {
			rows.Close()
			return userdata.Export{}, err
		}
		if !keep.Has(e.Item) {
			continue
		}
		doc.Progress = append(doc.Progress, e)
		items[e.Item] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	rows, err = tx.Query(ctx, `
		SELECT media_item_id, watched_at, duration_seconds, completed, source, watch_identity::text
		FROM user_watch_history WHERE user_id = $1 AND profile_id = $2 ORDER BY watched_at, id`, owner.UserID, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading watch history: %w", err)
	}
	for rows.Next() {
		var e watchPlayEntry
		var identity string
		if err := rows.Scan(&e.Item, &e.WatchedAt, &e.Duration, &e.Completed, &e.Source, &identity); err != nil {
			rows.Close()
			return userdata.Export{}, err
		}
		// A stored identity that no longer decodes is left out rather than
		// failing the backup: it is an archive hint, not the play itself.
		_ = json.Unmarshal([]byte(identity), &e.Identity)
		// A source the reader would refuse travels as an import, so one
		// play can never make the whole document unreadable.
		if !validWatchSource(e.Source) {
			e.Source = string(userstore.WatchHistorySourceImport)
		}
		if !keep.Has(e.Item) {
			continue
		}
		doc.History = append(doc.History, e)
		items[e.Item] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	rows, err = tx.Query(ctx, `
		SELECT media_item_id, hidden_before, updated_at, COALESCE(removed_before, hidden_before)
		FROM user_history_hidden_items WHERE user_id = $1 AND profile_id = $2 ORDER BY media_item_id`, owner.UserID, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading history hides: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e watchHiddenEntry
		if err := rows.Scan(&e.Item, &e.HiddenBefore, &e.UpdatedAt, &e.RemovedBefore); err != nil {
			return userdata.Export{}, err
		}
		if !keep.Has(e.Item) {
			continue
		}
		doc.Hidden = append(doc.Hidden, e)
		items[e.Item] = true
	}
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return userdata.Export{}, err
	}
	out := userdata.Export{Data: data, Count: len(doc.Progress) + len(doc.History) + len(doc.Hidden)}
	for id := range items {
		out.Items = append(out.Items, id)
	}
	return out, nil
}

var watchSources = map[string]bool{
	string(userstore.WatchHistorySourceLegacy): true, string(userstore.WatchHistorySourceManual): true,
	string(userstore.WatchHistorySourcePlayback): true, string(userstore.WatchHistorySourceImport): true,
	string(userstore.WatchHistorySourceJellycompat): true, string(userstore.WatchHistorySourceTrakt): true,
	string(userstore.WatchHistorySourceSimkl): true, string(userstore.WatchHistorySourceMDBList): true,
}

// validWatchSource admits the fixed sources and a watch-sync plugin's,
// plugin:<installation id>:<capability id>.
func validWatchSource(source string) bool {
	if watchSources[source] {
		return true
	}
	_, _, ok := userstore.ParsePluginWatchSource(source)
	return ok
}

// importedSource is the source a play keeps here. A plugin's installation
// id is this server's own on the server that wrote the play; elsewhere it
// is mapped to the installation here that is the same plugin with the
// capability, or the play becomes an import, never pointing at another
// installation or none. The source is not part of a play's identity (item
// and second), so a re-run finds the play whatever its source became.
func importedSource(target userdata.Target, source string) string {
	installation, capability, ok := userstore.ParsePluginWatchSource(source)
	if !ok || target.SameServer {
		return source
	}
	if local, mapped := target.MapPluginInstallation(installation, capability); mapped {
		return string(userstore.PluginWatchSource(local, capability))
	}
	return string(userstore.WatchHistorySourceImport)
}

func decodeWatchHistory(data json.RawMessage) (watchHistoryDocument, userdata.Summary, error) {
	var doc watchHistoryDocument
	if err := userdata.DecodeStrict(data, &doc); err != nil {
		return doc, userdata.Summary{}, err
	}
	n := len(doc.Progress) + len(doc.History) + len(doc.Hidden)
	if err := userdata.CheckEntries("watch history", n); err != nil {
		return doc, userdata.Summary{}, err
	}
	sum := userdata.Summary{Count: n}
	add := func(id string) error {
		if !userdata.ValidContentID(id) {
			return fmt.Errorf("watch history names an invalid item %q", id)
		}
		sum.Items = append(sum.Items, id)
		return nil
	}
	for _, e := range doc.Progress {
		if err := add(e.Item); err != nil {
			return doc, sum, err
		}
		if e.Position < 0 || e.Duration < 0 || e.UpdatedAt.IsZero() {
			return doc, sum, fmt.Errorf("progress of %q is not valid", e.Item)
		}
	}
	for _, e := range doc.History {
		if err := add(e.Item); err != nil {
			return doc, sum, err
		}
		if e.WatchedAt.IsZero() || !validWatchSource(e.Source) || (e.Duration != nil && *e.Duration < 0) {
			return doc, sum, fmt.Errorf("a play of %q is not valid", e.Item)
		}
	}
	for _, e := range doc.Hidden {
		if err := add(e.Item); err != nil {
			return doc, sum, err
		}
		if !e.HiddenBefore.Valid {
			return doc, sum, fmt.Errorf("the history hide of %q is not valid", e.Item)
		}
	}
	return doc, sum, nil
}

func (BackupData) Validate(data json.RawMessage) (userdata.Summary, error) {
	_, sum, err := decodeWatchHistory(data)
	return sum, err
}

// Prepare takes every profile's history lock, by profile id.
func (BackupData) Prepare(ctx context.Context, tx pgx.Tx, docs []userdata.Prepared) error {
	owners := make([]userdata.Owner, 0, len(docs))
	for _, d := range docs {
		owners = append(owners, d.Owner)
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].UserID != owners[j].UserID {
			return owners[i].UserID < owners[j].UserID
		}
		return owners[i].ProfileID < owners[j].ProfileID
	})
	for _, o := range owners {
		if err := userstore.LockImportedHistory(ctx, tx, o.UserID, o.ProfileID); err != nil {
			return err
		}
	}
	return nil
}

func (BackupData) Import(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeWatchHistory(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	// Plays and hides change under the lock every history writer of the
	// profile takes, so the play de-duplication and the cutoff merge below
	// cannot interleave with a mark watched or unwatched.
	if err := userstore.LockImportedHistory(ctx, tx, target.UserID, target.ProfileID); err != nil {
		return res, err
	}
	if err := importHidden(ctx, tx, target, doc.Hidden, &res); err != nil {
		return res, err
	}
	if err := importProgress(ctx, tx, target, doc.Progress, &res); err != nil {
		return res, err
	}
	if err := importPlays(ctx, tx, target, doc.History, &res); err != nil {
		return res, err
	}
	return res, nil
}

func importProgress(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []watchProgressEntry, res *userdata.Result) error {
	// The newest entry per local item: two source items can match one here.
	newest := map[string]watchProgressEntry{}
	var order []string
	for _, e := range entries {
		local, ok, err := target.Items.Resolve(ctx, e.Item)
		if err != nil {
			return err
		}
		if !ok {
			res.LeftOut++
			continue
		}
		if !target.SameServer {
			e.LastFileID = nil
		}
		prev, seen := newest[local]
		if !seen {
			order = append(order, local)
		}
		if !seen || e.UpdatedAt.After(prev.UpdatedAt) {
			newest[local] = e
		}
	}
	for _, local := range order {
		e := newest[local]
		// One statement decides newer-wins against the row as committed: a
		// playback heartbeat that lands meanwhile is newer and is kept, and a
		// row created concurrently is merged instead of failing the insert.
		var inserted bool
		err := tx.QueryRow(ctx, `
			INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
				event_at, hide_from_continue, last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET position_seconds = EXCLUDED.position_seconds,
				duration_seconds = EXCLUDED.duration_seconds, completed = EXCLUDED.completed, updated_at = EXCLUDED.updated_at,
				event_at = EXCLUDED.event_at, hide_from_continue = EXCLUDED.hide_from_continue, last_file_id = EXCLUDED.last_file_id,
				last_resolution = EXCLUDED.last_resolution, last_hdr = EXCLUDED.last_hdr, last_codec_video = EXCLUDED.last_codec_video,
				last_edition_key = EXCLUDED.last_edition_key
			WHERE user_watch_progress.updated_at < EXCLUDED.updated_at
			RETURNING (xmax = 0)`,
			target.UserID, target.ProfileID, local, e.Position, e.Duration, e.Completed, e.UpdatedAt, e.EventAt, e.HideFromContinue,
			e.LastFileID, e.LastResolution, e.LastHDR, e.LastCodecVideo, e.LastEditionKey).Scan(&inserted)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			res.Unchanged++
		case err != nil:
			return fmt.Errorf("restoring progress: %w", err)
		case inserted:
			res.Added++
		default:
			res.Updated++
		}
	}
	return nil
}

func importPlays(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []watchPlayEntry, res *userdata.Result) error {
	seen := map[string]bool{}
	for _, e := range entries {
		local, ok, err := target.Items.Resolve(ctx, e.Item)
		if err != nil {
			return err
		}
		if !ok {
			res.LeftOut++
			continue
		}
		watchedAt := e.WatchedAt.UTC().Truncate(time.Second)
		key := local + "\x00" + watchedAt.Format(time.RFC3339)
		if seen[key] {
			res.Unchanged++
			continue
		}
		seen[key] = true
		identity, _ := json.Marshal(e.Identity) // a struct of strings and ints
		// A play at the same second of the same item is the same play: the
		// witness index keys plays this way.
		tag, err := tx.Exec(ctx, `
			INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, duration_seconds, completed, source, watch_identity)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb
			WHERE NOT EXISTS (
				SELECT 1 FROM user_watch_history h
				WHERE h.user_id = $2 AND h.profile_id = $3 AND h.media_item_id = $4
				  AND date_trunc('second', h.watched_at AT TIME ZONE 'UTC') = date_trunc('second', $5::timestamptz AT TIME ZONE 'UTC'))`,
			uuid.NewString(), target.UserID, target.ProfileID, local, e.WatchedAt, e.Duration, e.Completed, importedSource(target, e.Source), string(identity))
		if err != nil {
			return fmt.Errorf("restoring a play: %w", err)
		}
		if tag.RowsAffected() > 0 {
			res.Added++
		} else {
			res.Unchanged++
		}
	}
	return nil
}

// importHidden merges each history hide's two cutoffs independently, as a
// mark watched or unwatched raises them: the later hidden_before wins, and
// the later removed_before wins, reading a legacy NULL as the
// hidden_before it stands for. A hide changes when either cutoff advances.
func importHidden(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []watchHiddenEntry, res *userdata.Result) error {
	for _, e := range entries {
		local, ok, err := target.Items.Resolve(ctx, e.Item)
		if err != nil {
			return err
		}
		if !ok {
			res.LeftOut++
			continue
		}
		updatedAt := e.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = target.Now
		}
		var inserted bool
		err = tx.QueryRow(ctx, `
			INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, removed_before, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET
				hidden_before = GREATEST(user_history_hidden_items.hidden_before, EXCLUDED.hidden_before),
				removed_before = GREATEST(COALESCE(user_history_hidden_items.removed_before, user_history_hidden_items.hidden_before),
					EXCLUDED.removed_before),
				updated_at = GREATEST(user_history_hidden_items.updated_at, EXCLUDED.updated_at)
			WHERE user_history_hidden_items.hidden_before < EXCLUDED.hidden_before
			   OR COALESCE(user_history_hidden_items.removed_before, user_history_hidden_items.hidden_before) < EXCLUDED.removed_before
			RETURNING (xmax = 0)`,
			target.UserID, target.ProfileID, local, e.HiddenBefore, e.removedBefore(), updatedAt).Scan(&inserted)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			res.Unchanged++
		case err != nil:
			return fmt.Errorf("restoring a history hide: %w", err)
		case inserted:
			res.Added++
		default:
			res.Updated++
		}
	}
	return nil
}

// Pending counts what Import would do, by its rules, without writing: a
// progress row where there is none or the backup's is newer, a play of an
// item at a second the profile has none, a hide that would advance a
// cutoff. The three comparisons go to the server in one round trip, each
// one query over the whole document, and only the entries that would
// change are resolved.
func (b BackupData) Pending(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	compare, err := b.PreparePending(data)
	if err != nil {
		return userdata.Result{}, err
	}
	return compare(ctx, tx, target)
}

// PreparePending decodes a document for Pending; the large histories of a
// preview decode ahead of their comparison.
func (BackupData) PreparePending(data json.RawMessage) (userdata.PendingFunc, error) {
	doc, _, err := decodeWatchHistory(data)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, tx pgx.Tx, target userdata.Target) (userdata.Result, error) {
		return pendingWatchHistory(ctx, tx, target, doc)
	}, nil
}

func pendingWatchHistory(ctx context.Context, tx pgx.Tx, target userdata.Target, doc watchHistoryDocument) (userdata.Result, error) {
	var res userdata.Result
	var batch pgx.Batch
	var parts []pendingPart
	for _, part := range []pendingPart{pendingProgress(target, doc.Progress, &res), pendingPlays(target, doc.History, &res), pendingHidden(target, doc.Hidden)} {
		if part.n > 0 {
			batch.Queue(part.sql, part.args...)
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return res, nil
	}
	results := tx.SendBatch(ctx, &batch)
	defer func() { _ = results.Close() }()
	var changed [2][]string
	for _, part := range parts {
		rows, err := results.Query()
		added, updated, err := changedRows(rows, err)
		if err != nil {
			return res, fmt.Errorf("comparing %s: %w", part.what, err)
		}
		res.Unchanged += part.n - len(added) - len(updated)
		changed[0], changed[1] = append(changed[0], added...), append(changed[1], updated...)
	}
	if err := results.Close(); err != nil {
		return res, err
	}
	err := userdata.SettleChanged(ctx, target.Items, &res, changed[0], changed[1])
	return res, err
}

// pendingPart is one comparison query: it returns (item, added) for each
// of its n entries that would change.
type pendingPart struct {
	what string
	sql  string
	args []any
	n    int
}

// changedRows reads (item, added) rows.
func changedRows(rows pgx.Rows, err error) (added, updated []string, _ error) {
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item string
		var isNew bool
		if err := rows.Scan(&item, &isNew); err != nil {
			return nil, nil, err
		}
		if isNew {
			added = append(added, item)
		} else {
			updated = append(updated, item)
		}
	}
	return added, updated, rows.Err()
}

func pendingProgress(target userdata.Target, entries []watchProgressEntry, res *userdata.Result) pendingPart {
	newest := map[string]time.Time{}
	for _, e := range entries {
		if prev, seen := newest[e.Item]; !seen || e.UpdatedAt.After(prev) {
			newest[e.Item] = e.UpdatedAt
		}
	}
	res.Unchanged += len(entries) - len(newest)
	items, times := make([]string, 0, len(newest)), make([]time.Time, 0, len(newest))
	for item, at := range newest {
		items, times = append(items, item), append(times, at)
	}
	return pendingPart{what: "progress", n: len(items), args: []any{target.UserID, target.ProfileID, items, times}, sql: `
		SELECT x.item, p.media_item_id IS NULL
		FROM unnest($3::text[], $4::timestamptz[]) AS x(item, at)
		LEFT JOIN user_watch_progress p ON p.user_id = $1 AND p.profile_id = $2 AND p.media_item_id = x.item
		WHERE p.media_item_id IS NULL OR p.updated_at < x.at`}
}

func pendingPlays(target userdata.Target, entries []watchPlayEntry, res *userdata.Result) pendingPart {
	seen := map[string]bool{}
	var items []string
	var times []time.Time
	for _, e := range entries {
		at := e.WatchedAt.UTC().Truncate(time.Second)
		key := e.Item + "\x00" + at.Format(time.RFC3339)
		if seen[key] {
			res.Unchanged++
			continue
		}
		seen[key] = true
		items, times = append(items, e.Item), append(times, at)
	}
	// The same test Import's insert makes, on the witness index.
	return pendingPart{what: "plays", n: len(items), args: []any{target.UserID, target.ProfileID, items, times}, sql: `
		SELECT x.item, true FROM unnest($3::text[], $4::timestamptz[]) AS x(item, at)
		WHERE NOT EXISTS (SELECT 1 FROM user_watch_history h
			WHERE h.user_id = $1 AND h.profile_id = $2 AND h.media_item_id = x.item
			  AND date_trunc('second', h.watched_at AT TIME ZONE 'UTC') = date_trunc('second', x.at AT TIME ZONE 'UTC'))`}
}

func pendingHidden(target userdata.Target, entries []watchHiddenEntry) pendingPart {
	items := make([]string, 0, len(entries))
	hidden, removed := make([]pgtype.Timestamptz, 0, len(entries)), make([]pgtype.Timestamptz, 0, len(entries))
	for _, e := range entries {
		items, hidden, removed = append(items, e.Item), append(hidden, e.HiddenBefore), append(removed, e.removedBefore())
	}
	return pendingPart{what: "history hides", n: len(items), args: []any{target.UserID, target.ProfileID, items, hidden, removed}, sql: `
		SELECT x.item, h.media_item_id IS NULL
		FROM unnest($3::text[], $4::timestamptz[], $5::timestamptz[]) AS x(item, hidden, removed)
		LEFT JOIN user_history_hidden_items h ON h.user_id = $1 AND h.profile_id = $2 AND h.media_item_id = x.item
		WHERE h.media_item_id IS NULL OR h.hidden_before < x.hidden OR COALESCE(h.removed_before, h.hidden_before) < x.removed`}
}
