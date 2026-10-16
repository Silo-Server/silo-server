package notifications

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InterestRepository owns profile_series_interest.
type InterestRepository struct {
	pool *pgxpool.Pool
}

// NewInterestRepository creates an InterestRepository.
func NewInterestRepository(pool *pgxpool.Pool) *InterestRepository {
	return &InterestRepository{pool: pool}
}

// ListActiveBySeries loads candidate recipients for one (library, series).
// This is the hot fanout query; it uses the partial active-interest index.
// Profiles that muted the series are left out in the same query, so a mute
// costs fanout no per-recipient read.
func (r *InterestRepository) ListActiveBySeries(ctx context.Context, tx pgx.Tx, libraryID int, seriesID string) ([]SeriesInterest, error) {
	rows, err := tx.Query(ctx, `
		SELECT psi.user_id, psi.profile_id, psi.library_id, psi.series_id,
		       psi.favorite, psi.watchlist, psi.continue_watching, psi.next_up_candidate,
		       psi.last_completed_episode_key, psi.next_expected_episode_key, psi.last_notified_episode_key,
		       psi.updated_at
		FROM profile_series_interest psi
		WHERE psi.library_id = $1 AND psi.series_id = $2
		  AND (psi.favorite OR psi.watchlist OR psi.continue_watching OR psi.next_up_candidate)
		  AND NOT EXISTS (
			SELECT 1 FROM profile_series_notification_mutes m
			WHERE m.series_id = psi.series_id AND m.user_id = psi.user_id AND m.profile_id = psi.profile_id)`,
		libraryID, seriesID)
	if err != nil {
		return nil, fmt.Errorf("list series interest: %w", err)
	}
	defer rows.Close()

	interests := make([]SeriesInterest, 0, 16)
	for rows.Next() {
		var interest SeriesInterest
		if err := rows.Scan(
			&interest.UserID, &interest.ProfileID, &interest.LibraryID, &interest.SeriesID,
			&interest.Favorite, &interest.Watchlist, &interest.ContinueWatching, &interest.NextUpCandidate,
			&interest.LastCompletedEpisodeKey, &interest.NextExpectedEpisodeKey, &interest.LastNotifiedEpisodeKey,
			&interest.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan series interest: %w", err)
		}
		interests = append(interests, interest)
	}
	return interests, rows.Err()
}

// UpsertRows writes recomputed interest rows. last_notified_episode_key is
// deliberately not touched: it is owned by the fanout worker. The DO UPDATE
// WHERE clause skips no-op writes so hot recompute paths do not churn rows.
func (r *InterestRepository) UpsertRows(ctx context.Context, interests []SeriesInterest) error {
	if len(interests) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`
		INSERT INTO profile_series_interest
			(user_id, profile_id, library_id, series_id,
			 favorite, watchlist, continue_watching, next_up_candidate,
			 last_completed_episode_key, next_expected_episode_key, updated_at)
		VALUES `)
	args := make([]any, 0, len(interests)*10)
	for i, interest := range interests {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := len(args)
		sb.WriteString(fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,now())",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10))
		args = append(args,
			interest.UserID, interest.ProfileID, interest.LibraryID, interest.SeriesID,
			interest.Favorite, interest.Watchlist, interest.ContinueWatching, interest.NextUpCandidate,
			interest.LastCompletedEpisodeKey, interest.NextExpectedEpisodeKey,
		)
	}
	sb.WriteString(`
		ON CONFLICT (user_id, profile_id, library_id, series_id) DO UPDATE SET
			favorite = EXCLUDED.favorite,
			watchlist = EXCLUDED.watchlist,
			continue_watching = EXCLUDED.continue_watching,
			next_up_candidate = EXCLUDED.next_up_candidate,
			last_completed_episode_key = EXCLUDED.last_completed_episode_key,
			next_expected_episode_key = EXCLUDED.next_expected_episode_key,
			updated_at = now()
		WHERE (profile_series_interest.favorite,
		       profile_series_interest.watchlist,
		       profile_series_interest.continue_watching,
		       profile_series_interest.next_up_candidate,
		       profile_series_interest.last_completed_episode_key,
		       profile_series_interest.next_expected_episode_key)
		IS DISTINCT FROM
		      (EXCLUDED.favorite, EXCLUDED.watchlist, EXCLUDED.continue_watching,
		       EXCLUDED.next_up_candidate, EXCLUDED.last_completed_episode_key,
		       EXCLUDED.next_expected_episode_key)`)
	if _, err := r.pool.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("upsert series interest: %w", err)
	}
	return nil
}

// DeleteStaleForProfileSeries removes interest rows for libraries the profile
// on userID's account can no longer see (or the series no longer belongs to).
// keepLibraryIDs is the freshly computed target set; an empty set deletes all
// rows for the (profile, series) pair.
func (r *InterestRepository) DeleteStaleForProfileSeries(ctx context.Context, userID int, profileID, seriesID string, keepLibraryIDs []int) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM profile_series_interest
		WHERE user_id = $1 AND profile_id = $2 AND series_id = $3 AND NOT (library_id = ANY($4))`,
		userID, profileID, seriesID, keepLibraryIDs)
	if err != nil {
		return fmt.Errorf("delete stale series interest: %w", err)
	}
	return nil
}

// ListSeriesForProfile returns the distinct series that currently have
// interest rows for the profile on userID's account. The rebuild pass
// recomputes these alongside the series resolved from live sources so rows
// whose sources were removed while the live updater was down get cleaned up
// instead of lingering.
func (r *InterestRepository) ListSeriesForProfile(ctx context.Context, userID int, profileID string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT series_id FROM profile_series_interest WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list profile interest series: %w", err)
	}
	defer rows.Close()
	seriesIDs := make([]string, 0, 16)
	for rows.Next() {
		var seriesID string
		if err := rows.Scan(&seriesID); err != nil {
			return nil, fmt.Errorf("scan profile interest series: %w", err)
		}
		seriesIDs = append(seriesIDs, seriesID)
	}
	return seriesIDs, rows.Err()
}

// GuardedSetLastNotified raises last_notified_episode_key for the given
// account profiles. The < guard makes concurrent workers handling adjacent
// release events safe: the higher key wins regardless of commit order.
func (r *InterestRepository) GuardedSetLastNotified(ctx context.Context, tx pgx.Tx, libraryID int, seriesID string, episodeKey int, profiles []profileKey) error {
	if len(profiles) == 0 {
		return nil
	}
	userIDs, profileIDs := profileKeyArrays(profiles)
	_, err := tx.Exec(ctx, `
		UPDATE profile_series_interest psi
		SET last_notified_episode_key = $1
		FROM unnest($4::integer[], $5::text[]) AS notified(user_id, profile_id)
		WHERE psi.library_id = $2 AND psi.series_id = $3
		  AND psi.user_id = notified.user_id AND psi.profile_id = notified.profile_id
		  AND (psi.last_notified_episode_key IS NULL OR psi.last_notified_episode_key < $1)`,
		episodeKey, libraryID, seriesID, userIDs, profileIDs)
	if err != nil {
		return fmt.Errorf("update last notified episode key: %w", err)
	}
	return nil
}

// DeleteAllForProfile removes every interest row for a deleted profile on
// userID's account. Profiles may live in per-user SQLite stores, so this
// cleanup cannot rely on a Postgres cascade.
func (r *InterestRepository) DeleteAllForProfile(ctx context.Context, userID int, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM profile_series_interest WHERE user_id = $1 AND profile_id = $2`, userID, profileID)
	return err
}

// PruneInert removes rows with no interest flags and no progression cursors;
// they can never produce a notification.
func (r *InterestRepository) PruneInert(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM profile_series_interest
		WHERE NOT (favorite OR watchlist OR continue_watching OR next_up_candidate)
		  AND last_completed_episode_key IS NULL
		  AND next_expected_episode_key IS NULL`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
