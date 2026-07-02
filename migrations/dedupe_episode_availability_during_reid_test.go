package migrations

import (
	"strings"
	"testing"
)

func TestDedupeEpisodeAvailabilityDuringReIDMigrationContract(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260625172243_dedupe_episode_availability_during_reid.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	migration := string(migrationBytes)
	parts := strings.Split(migration, "-- +goose Down")
	if len(parts) != 2 {
		t.Fatalf("expected one goose down section")
	}
	up := normalizeSQL(parts[0])
	down := normalizeSQL(parts[1])

	episodeDedupe := strings.Index(up, "IF to_regclass('public.episode_availability') IS NOT NULL THEN")
	if episodeDedupe < 0 {
		t.Fatal("up migration must special-case episode_availability before generic content-id rewrite")
	}
	genericRewrite := strings.Index(up, "FOR c IN SELECT cl.oid::regclass AS rel")
	if genericRewrite < 0 {
		t.Fatal("up migration missing generic content-id rewrite loop")
	}
	if episodeDedupe > genericRewrite {
		t.Fatal("episode_availability dedupe must run before the generic rewrite loop")
	}

	for _, want := range []string{
		"AND dest.series_id = p_to AND dest.episode_key = src.episode_key WHERE src.series_id = p_from",
		"SET available_at = conflicts.available_at, created_at = conflicts.created_at",
		"DELETE FROM public.episode_availability dest USING conflicts",
		"AND u.episode_id = conflicts.source_episode_id",
		"IF to_regclass('public.movie_availability') IS NOT NULL THEN",
		"AND dest.item_id = p_to WHERE src.item_id = p_from",
		"DELETE FROM public.movie_availability dest USING conflicts",
	} {
		if !strings.Contains(up, normalizeSQL(want)) {
			t.Fatalf("up migration missing %q", want)
		}
	}

	if strings.Contains(down, "episode_availability") || strings.Contains(down, "movie_availability") {
		t.Fatal("down migration should restore the pre-dedupe rename helper")
	}
}
