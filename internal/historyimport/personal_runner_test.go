package historyimport

import (
	"testing"
)

func personalEffectRepository(t *testing.T) *Repository {
	t.Helper()
	repo := personalQueueRepository(t)
	for _, statement := range []string{
		`CREATE TABLE media_items(content_id text PRIMARY KEY,type text,title text,year integer,status text,imdb_id text,tmdb_id text,tvdb_id text);INSERT INTO media_items VALUES('movie','movie','Movie',2026,'matched','tt1234567',NULL,NULL)`,
		`CREATE TABLE user_history_hidden_items(user_id integer,profile_id text,media_item_id text,hidden_before timestamptz)`,
		`CREATE TABLE user_watch_progress(user_id integer,profile_id text,media_item_id text,position_seconds double precision,duration_seconds double precision,completed boolean,updated_at timestamptz,event_at timestamptz,last_file_id text,last_resolution text,last_hdr text,last_codec_video text,last_edition_key text,PRIMARY KEY(user_id,profile_id,media_item_id))`,
		`CREATE TABLE user_watch_history(id text PRIMARY KEY,user_id integer,profile_id text,media_item_id text,watched_at timestamptz,duration_seconds double precision,completed boolean,source text,watch_identity jsonb)`,
		`CREATE TABLE user_watchlist(user_id integer,profile_id text,media_item_id text,added_at timestamptz NOT NULL DEFAULT now(),sort_index integer,PRIMARY KEY(user_id,profile_id,media_item_id))`,
	} {
		if _, err := repo.pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}
