package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/contentid"
)

func TestParseAnchoredGroupIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		version        int
		key            string
		wantOK         bool
		wantTarget     string
		wantProvider   string
		wantProviderID string
	}{
		{
			name:           "series tvdb",
			version:        1,
			key:            "v1|series|anchor|tvdb-415239",
			wantOK:         true,
			wantTarget:     "series-tvdb-415239",
			wantProvider:   contentid.ProviderTVDB,
			wantProviderID: "415239",
		},
		{
			name:           "movie imdb normalizes case",
			version:        1,
			key:            "v1|movie|anchor|imdb-tt33763941",
			wantOK:         true,
			wantTarget:     "movie-imdb-tt33763941",
			wantProvider:   contentid.ProviderIMDB,
			wantProviderID: "tt33763941",
		},
		{name: "version mismatch", version: 2, key: "v1|series|anchor|tvdb-415239"},
		{name: "title group", version: 1, key: "v1|series|diplomat|2023"},
		{name: "unknown provider", version: 1, key: "v1|series|anchor|anidb-123"},
		{name: "invalid numeric id", version: 1, key: "v1|series|anchor|tvdb-nope"},
		{name: "wrong shape", version: 1, key: "v1|series|anchor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseAnchoredGroupIdentity(tt.version, tt.key)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (identity = %+v)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.TargetContent != tt.wantTarget ||
				got.Provider != tt.wantProvider ||
				got.ProviderID != tt.wantProviderID {
				t.Fatalf(
					"identity = %+v, want target=%q provider=%q id=%q",
					got,
					tt.wantTarget,
					tt.wantProvider,
					tt.wantProviderID,
				)
			}
		})
	}
}

func TestRepairAnchoredIdentityMismatch_EndToEnd(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	targetProviderID := fmt.Sprintf("%d", 800_000_000+suffix%90_000_000)
	sourceProviderID := fmt.Sprintf("%d", 900_000_000+suffix%90_000_000)
	missingProviderID := fmt.Sprintf("%d", 700_000_000+suffix%90_000_000)
	targetContentID := "series-tvdb-" + targetProviderID
	sourceContentID := "series-tvdb-" + sourceProviderID
	targetEpisodeID := "episode-tvdb-" + targetProviderID + "-1-1"
	sourceEpisodeID := "episode-tvdb-" + sourceProviderID + "-1-1"
	targetEpisodeID2 := "episode-tvdb-" + targetProviderID + "-1-2"
	sourceEpisodeID2 := "episode-tvdb-" + sourceProviderID + "-1-2"
	rootBase := fmt.Sprintf("/anchored-repair/%d", suffix)
	wrongRoot := rootBase + "/4k/The Diplomat (2023) {tvdb-" + targetProviderID + "}"
	wrongRoot2 := rootBase + "/5k/The Diplomat (2023) {tvdb-" + targetProviderID + "} "
	stayRoot := rootBase + "/20s/The Diplomat (2023) {tvdb-" + sourceProviderID + "}"
	conflictingRoot := rootBase + "/00-ineligible/The Diplomat (2023) {tvdb-" + targetProviderID + "}"
	anchoredGroupKey := "v1|series|anchor|tvdb-" + targetProviderID
	sourceGroupKey := "v1|series|anchor|tvdb-" + sourceProviderID
	unownedGroupKey := "v1|series|anchor|tvdb-" + missingProviderID

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, year, status, tvdb_id)
		VALUES
			($1, 'series', 'The Diplomat', 2023, 'matched', $2),
			($3, 'series', 'The Diplomat', 2023, 'matched', $4)
	`, targetContentID, targetProviderID, sourceContentID, sourceProviderID); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1::text[])`, []string{sourceContentID, targetContentID})
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
		VALUES
			($1, 'series', 'tvdb', $2),
			($3, 'series', 'tvdb', $4)
	`, targetContentID, targetProviderID, sourceContentID, sourceProviderID); err != nil {
		t.Fatalf("seed provider ids: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES
			($1, $2, 1, 1, 'Target Episode'),
			($3, $4, 1, 1, 'Source Episode'),
			($5, $4, 1, 2, 'Source Episode 2')
	`, targetEpisodeID, targetContentID, sourceEpisodeID, sourceContentID, sourceEpisodeID2); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	var folderID, retainedFolderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', $1, true)
		RETURNING id
	`, fmt.Sprintf("anchored-repair-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', $1, true)
		RETURNING id
	`, fmt.Sprintf("anchored-repair-retained-%d", suffix)).Scan(&retainedFolderID); err != nil {
		t.Fatalf("seed retained folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(
			ctx,
			`DELETE FROM media_folders WHERE id = ANY($1::int[])`,
			[]int{folderID, retainedFolderID},
		)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id)
		VALUES ($1, $3), ($2, $3), ($2, $4)
	`, targetContentID, sourceContentID, folderID, retainedFolderID); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	var wrongFileID, wrongFileID2, wrongFileID3, stayFileID, conflictingFileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, media_folder_id, file_path, file_size,
			canonical_root_path, observed_root_path, group_key_version,
			content_group_key, base_type, season_number, episode_number
		)
		VALUES ($1, $2, $3, $4, 1000, $5, $5, 1, $6, 'series', 1, 1)
		RETURNING id
	`, sourceContentID, sourceEpisodeID, folderID, wrongRoot+"/S01E01.mkv", wrongRoot, anchoredGroupKey).Scan(&wrongFileID); err != nil {
		t.Fatalf("seed wrong file: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, media_folder_id, file_path, file_size,
			canonical_root_path, observed_root_path, group_key_version,
			content_group_key, base_type, season_number, episode_number
		)
		VALUES ($1, $2, $3, $4, 1000, $5, $5, 1, $6, 'series', 1, 2)
		RETURNING id
	`, sourceContentID, sourceEpisodeID2, folderID, wrongRoot+"/S01E02.mkv", wrongRoot, anchoredGroupKey).Scan(&wrongFileID2); err != nil {
		t.Fatalf("seed second wrong file: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, media_folder_id, file_path, file_size,
			canonical_root_path, observed_root_path, group_key_version,
			content_group_key, base_type, season_number, episode_number
		)
		VALUES ($1, $2, $3, $4, 1000, $5, $5, 1, $6, 'series', 1, 1)
		RETURNING id
	`, sourceContentID, sourceEpisodeID, folderID, wrongRoot2+"/S01E01.mkv", wrongRoot2, anchoredGroupKey).Scan(&wrongFileID3); err != nil {
		t.Fatalf("seed third wrong file in repeated anchored group: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, media_folder_id, file_path, file_size,
			canonical_root_path, observed_root_path, group_key_version,
			content_group_key, base_type, season_number, episode_number
		)
		VALUES ($1, $2, $3, $4, 1000, $5, $5, 1, $6, 'series', 1, 1)
		RETURNING id
	`, sourceContentID, sourceEpisodeID, retainedFolderID, stayRoot+"/S01E01.mkv", stayRoot, sourceGroupKey).Scan(&stayFileID); err != nil {
		t.Fatalf("seed source file: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, media_folder_id, file_path, file_size,
			canonical_root_path, observed_root_path, group_key_version,
			content_group_key, base_type, season_number, episode_number
		)
			VALUES ($1, NULL, $2, $3, 1000, $4, $4, 1, $5, 'series', 1, 1)
			RETURNING id
		`, sourceContentID, folderID, conflictingRoot+"/S01E01.mkv", conflictingRoot, anchoredGroupKey).Scan(&conflictingFileID); err != nil {
		t.Fatalf("seed conflicting anchored file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episode_libraries (episode_id, media_folder_id)
		VALUES ($1, $3), ($1, $4), ($2, $3)
	`, sourceEpisodeID, sourceEpisodeID2, folderID, retainedFolderID); err != nil {
		t.Fatalf("seed source episode memberships: %v", err)
	}

	var userID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, role)
		VALUES ($1, 'user')
		RETURNING id
	`, fmt.Sprintf("anchored-repair-user-%d", suffix)).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", suffix%1_000_000_000_000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (
			user_id, profile_id, media_item_id, position_seconds,
			duration_seconds, last_file_id
		)
		VALUES
			($1, $2, $3, 120, 3600, $4),
			($1, $2, $5, 240, 3600, $6)
	`, userID, profileID, sourceEpisodeID, stayFileID, sourceEpisodeID2, wrongFileID2); err != nil {
		t.Fatalf("seed source episode progress: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id)
		VALUES ($1, $2, $4), ($1, $3, $4)
	`, folderID, wrongRoot, wrongRoot2, sourceContentID); err != nil {
		t.Fatalf("seed root claim: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_groups (
			media_folder_id, group_key_version, content_group_key, content_id
		)
		VALUES ($1, 1, $2, $3)
	`, folderID, anchoredGroupKey, sourceContentID); err != nil {
		t.Fatalf("seed group claim: %v", err)
	}

	service := &MetadataService{dbPool: pool}
	followUpCalls := 0
	service.hooks.ensureSeriesEpisodeLinks = func(ctx context.Context, seriesID string) error {
		followUpCalls++
		if seriesID != targetContentID {
			return fmt.Errorf("unexpected episode follow-up target %q", seriesID)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, 2, 'Target Episode 2')
			ON CONFLICT (content_id) DO NOTHING
		`, targetEpisodeID2, targetContentID); err != nil {
			return fmt.Errorf("seeding missing target episode in follow-up: %w", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE media_files
			SET episode_id = $1,
				updated_at = NOW()
			WHERE content_id = $2
			  AND season_number = 1
			  AND episode_number = 2
			  AND episode_id IS NULL
		`, targetEpisodeID2, targetContentID); err != nil {
			return fmt.Errorf("relinking missing target episode in follow-up: %w", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
			SELECT episode_id, media_folder_id, MIN(created_at)
			FROM media_files
			WHERE content_id = $1
			  AND episode_id = $2
			GROUP BY episode_id, media_folder_id
			ON CONFLICT (episode_id, media_folder_id) DO NOTHING
		`, targetContentID, targetEpisodeID2); err != nil {
			return fmt.Errorf("adding target episode membership in follow-up: %w", err)
		}
		return nil
	}
	var gotContentID, groupOwner string
	repaired, err := service.repairAnchoredIdentityMismatchesByFolderAndPathPrefix(ctx, folderID, rootBase+"/4k")
	if err != nil {
		t.Fatalf("repair with conflicting claim member: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("repair with conflicting claim member repaired %d roots, want 0", repaired)
	}
	if err := pool.QueryRow(ctx, `
		SELECT content_id
		FROM media_files
		WHERE id = $1
	`, wrongFileID).Scan(&gotContentID); err != nil {
		t.Fatalf("load rolled-back file: %v", err)
	}
	if gotContentID != sourceContentID {
		t.Fatalf("rolled-back file content_id = %q, want %q", gotContentID, sourceContentID)
	}
	if err := pool.QueryRow(ctx, `
		SELECT content_id
		FROM media_item_groups
		WHERE media_folder_id = $1
		  AND group_key_version = 1
		  AND content_group_key = $2
	`, folderID, anchoredGroupKey).Scan(&groupOwner); err != nil {
		t.Fatalf("load retained conflicting group claim: %v", err)
	}
	if groupOwner != sourceContentID {
		t.Fatalf("conflicting group claim owner = %q, want %q", groupOwner, sourceContentID)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE media_files
		SET content_group_key = $2
		WHERE id = $1
	`, conflictingFileID, unownedGroupKey); err != nil {
		t.Fatalf("make leading candidate safely ineligible: %v", err)
	}

	repaired, err = service.repairAnchoredIdentityMismatchesByFolderAndPathPrefixPageSize(
		ctx,
		folderID,
		rootBase,
		1,
	)
	if err != nil {
		t.Fatalf("repair after skipped first page: %v", err)
	}
	if repaired != 2 {
		t.Fatalf("repaired roots after skipped first page = %d, want 2", repaired)
	}
	if followUpCalls != 1 {
		t.Fatalf("episode repair follow-up calls = %d, want 1", followUpCalls)
	}

	var gotEpisodeID string
	if err := pool.QueryRow(ctx, `
		SELECT content_id, episode_id
		FROM media_files
		WHERE id = $1
	`, wrongFileID).Scan(&gotContentID, &gotEpisodeID); err != nil {
		t.Fatalf("load repaired file: %v", err)
	}
	if gotContentID != targetContentID || gotEpisodeID != targetEpisodeID {
		t.Fatalf(
			"repaired file = (%q, %q), want (%q, %q)",
			gotContentID,
			gotEpisodeID,
			targetContentID,
			targetEpisodeID,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT content_id, episode_id
		FROM media_files
		WHERE id = $1
	`, wrongFileID2).Scan(&gotContentID, &gotEpisodeID); err != nil {
		t.Fatalf("load second repaired file: %v", err)
	}
	if gotContentID != targetContentID || gotEpisodeID != targetEpisodeID2 {
		t.Fatalf(
			"second repaired file = (%q, %q), want (%q, %q)",
			gotContentID,
			gotEpisodeID,
			targetContentID,
			targetEpisodeID2,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT content_id, episode_id
		FROM media_files
		WHERE id = $1
	`, wrongFileID3).Scan(&gotContentID, &gotEpisodeID); err != nil {
		t.Fatalf("load repeated-group repaired file: %v", err)
	}
	if gotContentID != targetContentID || gotEpisodeID != targetEpisodeID {
		t.Fatalf(
			"repeated-group repaired file = (%q, %q), want (%q, %q)",
			gotContentID,
			gotEpisodeID,
			targetContentID,
			targetEpisodeID,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT content_id
		FROM media_files
		WHERE id = $1
	`, stayFileID).Scan(&gotContentID); err != nil {
		t.Fatalf("load retained file: %v", err)
	}
	if gotContentID != sourceContentID {
		t.Fatalf("retained file content_id = %q, want %q", gotContentID, sourceContentID)
	}
	rows, err := pool.Query(ctx, `
		SELECT media_item_id
		FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2
		ORDER BY media_item_id
	`, userID, profileID)
	if err != nil {
		t.Fatalf("load source episode progress: %v", err)
	}
	defer rows.Close()
	progressContentIDs := make([]string, 0, 2)
	for rows.Next() {
		var progressContentID string
		if err := rows.Scan(&progressContentID); err != nil {
			t.Fatalf("scan source episode progress: %v", err)
		}
		progressContentIDs = append(progressContentIDs, progressContentID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate source episode progress: %v", err)
	}
	wantProgressContentIDs := []string{targetEpisodeID2, sourceEpisodeID}
	if len(progressContentIDs) != len(wantProgressContentIDs) ||
		progressContentIDs[0] != wantProgressContentIDs[0] ||
		progressContentIDs[1] != wantProgressContentIDs[1] {
		t.Fatalf(
			"episode progress ids = %v, want %v (shared source stays; fully moved episode follows)",
			progressContentIDs,
			wantProgressContentIDs,
		)
	}

	var rootOwner, rootOwner2 string
	if err := pool.QueryRow(ctx, `
		SELECT content_id
		FROM media_item_roots
		WHERE media_folder_id = $1 AND canonical_root_path = $2
	`, folderID, wrongRoot).Scan(&rootOwner); err != nil {
		t.Fatalf("load repaired root claim: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT content_id
		FROM media_item_roots
		WHERE media_folder_id = $1 AND canonical_root_path = $2
	`, folderID, wrongRoot2).Scan(&rootOwner2); err != nil {
		t.Fatalf("load repeated-group repaired root claim: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT content_id
		FROM media_item_groups
		WHERE media_folder_id = $1
		  AND group_key_version = 1
		  AND content_group_key = $2
	`, folderID, anchoredGroupKey).Scan(&groupOwner); err != nil {
		t.Fatalf("load repaired group claim: %v", err)
	}
	if rootOwner != targetContentID || rootOwner2 != targetContentID || groupOwner != targetContentID {
		t.Fatalf(
			"claim owners = (%q, %q, %q), want %q",
			rootOwner,
			rootOwner2,
			groupOwner,
			targetContentID,
		)
	}

	rows, err = pool.Query(ctx, `
		SELECT episode_id
		FROM episode_libraries
		WHERE media_folder_id = $1
		  AND episode_id = ANY($2::text[])
		ORDER BY episode_id
	`, folderID, []string{sourceEpisodeID, sourceEpisodeID2, targetEpisodeID, targetEpisodeID2})
	if err != nil {
		t.Fatalf("load repaired episode memberships: %v", err)
	}
	defer rows.Close()
	gotEpisodeMemberships := make([]string, 0, 2)
	for rows.Next() {
		var episodeID string
		if err := rows.Scan(&episodeID); err != nil {
			t.Fatalf("scan repaired episode membership: %v", err)
		}
		gotEpisodeMemberships = append(gotEpisodeMemberships, episodeID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate repaired episode memberships: %v", err)
	}
	wantEpisodeMemberships := []string{targetEpisodeID, targetEpisodeID2}
	if len(gotEpisodeMemberships) != len(wantEpisodeMemberships) {
		t.Fatalf("episode memberships = %v, want %v", gotEpisodeMemberships, wantEpisodeMemberships)
	}
	for index := range wantEpisodeMemberships {
		if gotEpisodeMemberships[index] != wantEpisodeMemberships[index] {
			t.Fatalf("episode memberships = %v, want %v", gotEpisodeMemberships, wantEpisodeMemberships)
		}
	}
	var retainedSourceMemberships int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM episode_libraries
		WHERE media_folder_id = $1
		  AND episode_id = $2
	`, retainedFolderID, sourceEpisodeID).Scan(&retainedSourceMemberships); err != nil {
		t.Fatalf("load retained source episode membership: %v", err)
	}
	if retainedSourceMemberships != 1 {
		t.Fatalf("retained source episode memberships = %d, want 1", retainedSourceMemberships)
	}

	repaired, err = service.repairAnchoredIdentityMismatchesByFolderAndPathPrefix(ctx, folderID, rootBase+"/4k")
	if err != nil {
		t.Fatalf("repeat anchored identity repair: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("repeat repaired roots = %d, want idempotent 0", repaired)
	}
	if followUpCalls != 1 {
		t.Fatalf("repeat episode repair follow-up calls = %d, want unchanged 1", followUpCalls)
	}
}
