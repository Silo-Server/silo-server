package queryattr

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkOp(b *testing.B) {
	old := enabled.Load()
	b.Cleanup(func() { enabled.Store(old) })
	ctx := context.Background()
	for _, name := range []string{"off", "cached", "first_seen", "parallel", "largest_miss"} {
		b.Run(name, func(b *testing.B) {
			enabled.Store(name != "off")
			sites.Clear()
			pcs.Clear()
			shapeCache.Clear()
			shapeCacheSize.Store(0)
			warm := Begin(ctx, KindQuery, "SELECT 1", nil, 1)
			warm.End(ctx, nil)
			b.ReportAllocs()
			b.ResetTimer()
			if name == "parallel" {
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						op := Begin(ctx, KindQuery, "SELECT 1", nil, 1)
						op.End(ctx, nil)
					}
				})
				return
			}
			for i := 0; i < b.N; i++ {
				sql := "SELECT 1"
				if name == "first_seen" {
					sql = fmt.Sprintf("SELECT %d", i)
				}
				if name == "largest_miss" {
					shapeCache.Clear()
					shapeCacheSize.Store(0)
					sql = largestSQL
				}
				op := Begin(ctx, KindQuery, sql, nil, 1)
				op.End(ctx, nil)
			}
		})
	}
}

// largestSQL is the expanded PostgreSQL statement from
// metadata.(*ImageCacheJobRepository).enqueueArtworkRepair in
// internal/metadata/image_cache_job_repo.go. Its 6,845-byte literal is the
// longest pgx-bound SQL literal under internal/; the item-only branches and
// non-provider schemes are expanded as at the production Query call.
const largestSQL = `
		WITH all_candidates AS (
			SELECT
				'poster'::text AS image_type,
				'item'::text AS target_type,
				mi.content_id AS target_content_id,
				''::text AS target_language,
				mi.content_id AS series_id,
				mi.poster_source_path AS source_path,
				mi.type AS content_type,
				NULL::integer AS season_number,
				NULL::integer AS episode_number,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.poster_source_path LIKE '%://%'
			  AND lower(mi.poster_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (mi.poster_path LIKE '%://%' OR coalesce(mi.poster_path, '') = '')) OR mi.poster_path = ANY($2))
			UNION ALL
			SELECT
				'backdrop'::text,
				'item'::text,
				mi.content_id,
				''::text,
				mi.content_id,
				mi.backdrop_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.backdrop_source_path LIKE '%://%'
			  AND lower(mi.backdrop_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (mi.backdrop_path LIKE '%://%' OR coalesce(mi.backdrop_path, '') = '')) OR mi.backdrop_path = ANY($2))
			UNION ALL
			SELECT
				'logo'::text,
				'item'::text,
				mi.content_id,
				''::text,
				mi.content_id,
				mi.logo_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.logo_source_path LIKE '%://%'
			  AND lower(mi.logo_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (mi.logo_path LIKE '%://%' OR coalesce(mi.logo_path, '') = '')) OR mi.logo_path = ANY($2))
			UNION ALL
			SELECT
				'poster'::text,
				'item_localization'::text,
				loc.content_id,
				loc.language,
				loc.content_id,
				loc.poster_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_item_localizations loc
			JOIN media_items mi ON mi.content_id = loc.content_id
			WHERE loc.poster_source_path LIKE '%://%'
			  AND lower(loc.poster_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (loc.poster_path LIKE '%://%' OR coalesce(loc.poster_path, '') = '')) OR loc.poster_path = ANY($2))
			UNION ALL
			SELECT
				'backdrop'::text,
				'item_localization'::text,
				loc.content_id,
				loc.language,
				loc.content_id,
				loc.backdrop_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_item_localizations loc
			JOIN media_items mi ON mi.content_id = loc.content_id
			WHERE loc.backdrop_source_path LIKE '%://%'
			  AND lower(loc.backdrop_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (loc.backdrop_path LIKE '%://%' OR coalesce(loc.backdrop_path, '') = '')) OR loc.backdrop_path = ANY($2))
			UNION ALL
			SELECT
				'logo'::text,
				'item_localization'::text,
				loc.content_id,
				loc.language,
				loc.content_id,
				loc.logo_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_item_localizations loc
			JOIN media_items mi ON mi.content_id = loc.content_id
			WHERE loc.logo_source_path LIKE '%://%'
			  AND lower(loc.logo_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (loc.logo_path LIKE '%://%' OR coalesce(loc.logo_path, '') = '')) OR loc.logo_path = ANY($2))
			UNION ALL
			SELECT
				'poster'::text,
				'season'::text,
				s.content_id AS target_content_id,
				''::text AS target_language,
				s.series_id,
				s.poster_source_path AS source_path,
				'series'::text AS content_type,
				s.season_number,
				NULL::integer AS episode_number,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM seasons s
			JOIN media_items mi ON mi.content_id = s.series_id
			WHERE s.poster_source_path LIKE '%://%'
			  AND lower(s.poster_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (s.poster_path LIKE '%://%' OR coalesce(s.poster_path, '') = '')) OR s.poster_path = ANY($2))
			UNION ALL
			SELECT
				'poster'::text,
				'season_localization'::text,
				s.content_id,
				loc.language,
				s.series_id,
				loc.poster_source_path,
				'series'::text,
				s.season_number,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM season_localizations loc
			JOIN seasons s ON s.content_id = loc.season_content_id
			JOIN media_items mi ON mi.content_id = s.series_id
			WHERE loc.poster_source_path LIKE '%://%'
			  AND lower(loc.poster_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (loc.poster_path LIKE '%://%' OR coalesce(loc.poster_path, '') = '')) OR loc.poster_path = ANY($2))
			UNION ALL
			SELECT
				'still'::text,
				'episode'::text,
				e.content_id,
				''::text,
				e.series_id,
				e.still_source_path,
				'series'::text,
				e.season_number,
				e.episode_number,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM episodes e
			JOIN media_items mi ON mi.content_id = e.series_id
			WHERE e.still_source_path LIKE '%://%'
			  AND lower(e.still_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (e.still_path LIKE '%://%' OR coalesce(e.still_path, '') = '')) OR e.still_path = ANY($2))
			UNION ALL
			SELECT
				'profile'::text,
				'person'::text,
				p.id::text,
				''::text,
				''::text,
				p.photo_source_path,
				'people'::text,
				NULL::integer,
				NULL::integer,
				p.tmdb_id,
				p.tvdb_id,
				p.imdb_id
			FROM people p
			WHERE p.photo_source_path LIKE '%://%'
			  AND lower(p.photo_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (p.photo_path LIKE '%://%' OR coalesce(p.photo_path, '') = '')) OR p.photo_path = ANY($2))
			UNION ALL
			SELECT
				'banner'::text,
				'item'::text,
				mi.content_id,
				''::text,
				mi.content_id,
				mi.banner_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.banner_source_path LIKE '%://%'
			  AND lower(mi.banner_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (mi.banner_path LIKE '%://%' OR mi.banner_path = '')) OR (mi.banner_path = ANY($2) AND mi.banner_path <> ''))
			UNION ALL
			SELECT
				'thumb'::text,
				'item'::text,
				mi.content_id,
				''::text,
				mi.content_id,
				mi.thumb_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.thumb_source_path LIKE '%://%'
			  AND lower(mi.thumb_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (mi.thumb_path LIKE '%://%' OR mi.thumb_path = '')) OR (mi.thumb_path = ANY($2) AND mi.thumb_path <> ''))
			UNION ALL
			SELECT
				'clearart'::text,
				'item'::text,
				mi.content_id,
				''::text,
				mi.content_id,
				mi.clearart_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.clearart_source_path LIKE '%://%'
			  AND lower(mi.clearart_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (mi.clearart_path LIKE '%://%' OR mi.clearart_path = '')) OR (mi.clearart_path = ANY($2) AND mi.clearart_path <> ''))
			UNION ALL
			SELECT
				'disc'::text,
				'item'::text,
				mi.content_id,
				''::text,
				mi.content_id,
				mi.disc_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.disc_source_path LIKE '%://%'
			  AND lower(mi.disc_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
			  AND ((cardinality($2::text[]) = 0 AND (mi.disc_path LIKE '%://%' OR mi.disc_path = '')) OR (mi.disc_path = ANY($2) AND mi.disc_path <> ''))
		),
		candidates AS (
			SELECT ac.*
			FROM all_candidates ac
			LEFT JOIN metadata_image_cache_jobs j
			  ON j.target_type = ac.target_type
			 AND j.target_content_id = ac.target_content_id
			 AND j.image_type = ac.image_type
			 AND j.target_language = ac.target_language
			WHERE j.id IS NULL
			   OR j.source_path IS DISTINCT FROM ac.source_path
			   OR j.status = 'succeeded'
			   OR (
				   j.status = 'failed'
				   AND j.next_attempt_at <= NOW()
			   )
			ORDER BY ac.target_type, ac.target_content_id, ac.target_language, ac.image_type
			LIMIT $1
		)
		SELECT image_type, target_type, target_content_id, target_language, series_id, source_path,
		       content_type, season_number, episode_number,
		       COALESCE(tmdb_id, '') AS tmdb_id,
		       COALESCE(tvdb_id, '') AS tvdb_id,
		       COALESCE(imdb_id, '') AS imdb_id
		FROM candidates
	`
