package markers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FetchClaim struct {
	FileID   int
	Provider string
	Identity string
	Token    string
	Failures int
}

type FetchCompletion struct {
	Outcome string
	RetryAt time.Time
	Error   string
	Result  *Result
}

// PopulationStore coordinates provider requests across API replicas and caches
// stored-mode responses. On-demand responses never enter this store.
type PopulationStore interface {
	Eligible(context.Context, int) (bool, error)
	Claim(context.Context, int, string, string, string, bool) (FetchClaim, bool, error)
	Complete(context.Context, FetchClaim, FetchCompletion) error
	Cooldown(context.Context, string, string, time.Time) error
	Cached(context.Context, int, string) (map[string]Result, error)
	Candidates(context.Context, map[string]string, int, int, bool) ([]int, error)
}

type DBPopulationStore struct{ pool *pgxpool.Pool }

func NewPopulationStore(pool *pgxpool.Pool) *DBPopulationStore {
	return &DBPopulationStore{pool: pool}
}

func (s *DBPopulationStore) Eligible(ctx context.Context, fileID int) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM media_files f JOIN media_folders library ON library.id=f.media_folder_id
		WHERE f.id=$1 AND library.enabled AND lower(btrim(library.type)) IN ('movie','movies','tv','series','show','tvshows','mixed')
		AND f.missing_since IS NULL AND f.extra_id IS NULL AND COALESCE(f.duration,0)>0
		AND COALESCE(f.multi_episode_start,0)=0 AND COALESCE(f.multi_episode_end,0)=0
		AND COALESCE(f.presentation_part_total,1)<=1
	)`, fileID).Scan(&ok)
	return ok, err
}

// Claim uses an expiring lease rather than keeping a transaction open over the
// provider call. The identity includes both the file generation and external IDs.
func (s *DBPopulationStore) Claim(ctx context.Context, fileID int, provider, identity, revision string, force bool) (FetchClaim, bool, error) {
	claim := FetchClaim{FileID: fileID, Provider: provider, Identity: identity}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO marker_fetch_state(media_file_id,provider,identity_key,provider_revision,lease_token,lease_until)
		SELECT $1,$2,$3,$5,gen_random_uuid(),now()+interval '2 minutes'
		WHERE NOT EXISTS (SELECT 1 FROM marker_provider_cooldowns WHERE provider=$2 AND provider_revision=$5 AND retry_at>now())
		ON CONFLICT(media_file_id,provider) DO UPDATE SET
			identity_key=EXCLUDED.identity_key,
			provider_revision=EXCLUDED.provider_revision,
			lease_token=EXCLUDED.lease_token,
			lease_until=EXCLUDED.lease_until,
			result=CASE WHEN marker_fetch_state.identity_key<>EXCLUDED.identity_key OR marker_fetch_state.provider_revision<>EXCLUDED.provider_revision THEN NULL ELSE marker_fetch_state.result END,
			fetched_at=CASE WHEN marker_fetch_state.identity_key<>EXCLUDED.identity_key OR marker_fetch_state.provider_revision<>EXCLUDED.provider_revision THEN NULL ELSE marker_fetch_state.fetched_at END,
			failures=CASE WHEN marker_fetch_state.identity_key<>EXCLUDED.identity_key OR marker_fetch_state.provider_revision<>EXCLUDED.provider_revision THEN 0 ELSE marker_fetch_state.failures END
		WHERE (marker_fetch_state.lease_until IS NULL OR marker_fetch_state.lease_until<=now())
		AND (marker_fetch_state.identity_key<>EXCLUDED.identity_key OR marker_fetch_state.provider_revision<>EXCLUDED.provider_revision OR marker_fetch_state.retry_at<=now()
			OR marker_fetch_state.outcome='on_demand' OR ($4 AND marker_fetch_state.outcome IN ('hit','miss')))
		RETURNING lease_token::text,failures`, fileID, provider, identity, force, revision).Scan(&claim.Token, &claim.Failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return FetchClaim{}, false, nil
	}
	if err != nil {
		return FetchClaim{}, false, fmt.Errorf("claim marker fetch: %w", err)
	}
	return claim, true, nil
}

func (s *DBPopulationStore) Complete(ctx context.Context, claim FetchClaim, result FetchCompletion) error {
	var payload []byte
	if result.Result != nil {
		var err error
		payload, err = json.Marshal(result.Result)
		if err != nil {
			return fmt.Errorf("encode cached marker result: %w", err)
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE marker_fetch_state SET
		outcome=$5,retry_at=$6,last_error=NULLIF($7,''),lease_token=NULL,lease_until=NULL,
		fetched_at=CASE WHEN $5 IN ('hit','miss','on_demand') THEN now() ELSE fetched_at END,
		result=CASE WHEN $5 IN ('hit','miss') THEN $8::jsonb WHEN $5='on_demand' THEN NULL ELSE result END,
		failures=CASE WHEN $5='error' THEN failures+1 ELSE 0 END
		WHERE media_file_id=$1 AND provider=$2 AND identity_key=$3 AND lease_token=$4::uuid`,
		claim.FileID, claim.Provider, claim.Identity, claim.Token, result.Outcome, result.RetryAt, result.Error, payload)
	if err != nil {
		return fmt.Errorf("complete marker fetch: %w", err)
	}
	return nil
}

func (s *DBPopulationStore) Cached(ctx context.Context, fileID int, identity string) (map[string]Result, error) {
	rows, err := s.pool.Query(ctx, `SELECT provider,result FROM marker_fetch_state
		WHERE media_file_id=$1 AND identity_key=$2 AND result IS NOT NULL`, fileID, identity)
	if err != nil {
		return nil, fmt.Errorf("load cached marker results: %w", err)
	}
	defer rows.Close()
	results := make(map[string]Result)
	for rows.Next() {
		var provider string
		var payload []byte
		if err := rows.Scan(&provider, &payload); err != nil {
			return nil, err
		}
		var result Result
		if err := json.Unmarshal(payload, &result); err != nil {
			return nil, fmt.Errorf("decode cached marker results: %w", err)
		}
		results[provider] = result
	}
	return results, rows.Err()
}

func (s *DBPopulationStore) Cooldown(ctx context.Context, provider, revision string, until time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO marker_provider_cooldowns(provider,provider_revision,retry_at) VALUES($1,$2,$3)
		ON CONFLICT(provider,provider_revision) DO UPDATE SET
		retry_at=GREATEST(marker_provider_cooldowns.retry_at,EXCLUDED.retry_at)`, provider, revision, until)
	if err != nil {
		return fmt.Errorf("record marker provider cooldown: %w", err)
	}
	return nil
}

// Candidates pages new/unqueried files separately so backfill gets quota before
// periodic refresh. Metadata changes make a file eligible for identity checking;
// Claim still suppresses the request when those changes did not alter its IDs.
func (s *DBPopulationStore) Candidates(ctx context.Context, providers map[string]string, afterID, limit int, unqueried bool) ([]int, error) {
	providerIDs := make([]string, 0, len(providers))
	for id := range providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	revisions := make([]string, 0, len(providers))
	for _, id := range providerIDs {
		revisions = append(revisions, providers[id])
	}
	rows, err := s.pool.Query(ctx, `SELECT f.id FROM media_files f
		JOIN media_folders library ON library.id=f.media_folder_id
		LEFT JOIN episodes episode ON episode.content_id=f.episode_id
		LEFT JOIN media_items item ON item.content_id=COALESCE(episode.series_id,f.content_id)
		WHERE f.id>$1 AND library.enabled AND lower(btrim(library.type)) IN ('movie','movies','tv','series','show','tvshows','mixed')
		AND f.missing_since IS NULL AND f.extra_id IS NULL AND COALESCE(f.duration,0)>0
		AND COALESCE(f.multi_episode_start,0)=0 AND COALESCE(f.multi_episode_end,0)=0
		AND COALESCE(f.presentation_part_total,1)<=1
		AND (episode.content_id IS NOT NULL OR item.type='movie')
		AND (COALESCE(item.tmdb_id,'')<>'' OR COALESCE(item.imdb_id,'')<>'' OR COALESCE(item.tvdb_id,'')<>'')
		AND EXISTS (
			SELECT 1 FROM unnest($3::text[],$5::text[]) requested(provider,revision)
			LEFT JOIN marker_fetch_state state ON state.media_file_id=f.id AND state.provider=requested.provider
			WHERE NOT EXISTS (SELECT 1 FROM marker_provider_cooldowns c WHERE c.provider=requested.provider AND c.provider_revision=requested.revision AND c.retry_at>now())
			AND (state.lease_until IS NULL OR state.lease_until<=now())
			AND (state.media_file_id IS NULL OR state.retry_at<=now() OR state.outcome='on_demand' OR state.provider_revision<>requested.revision
				OR item.updated_at>state.fetched_at OR episode.updated_at>state.fetched_at)
			AND (($4 AND (state.fetched_at IS NULL OR state.outcome='on_demand'))
				OR (NOT $4 AND state.fetched_at IS NOT NULL AND state.outcome<>'on_demand'))
		)
		ORDER BY f.id LIMIT $2`, afterID, limit, providerIDs, unqueried, revisions)
	if err != nil {
		return nil, fmt.Errorf("list marker sync candidates: %w", err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
