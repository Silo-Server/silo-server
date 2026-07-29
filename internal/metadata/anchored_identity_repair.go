package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog/filesplit"
	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/pathscope"
)

const (
	anchoredIdentityRepairBatchSize = 100
	anchoredItemTypeMovie           = "movie"
	anchoredItemTypeSeries          = "series"
)

type anchoredGroupIdentity struct {
	Version       int
	ItemType      string
	Provider      string
	ProviderID    string
	TargetContent string
}

type anchoredIdentityRepairCandidate struct {
	FolderID         int
	ObservedRootPath string
	SourceContentID  string
	GroupKeyVersion  int
	ContentGroupKey  string
	ItemType         string
	FileCount        int
}

type anchoredIdentityRepairGroupCursor struct {
	SourceContentID string
	GroupKeyVersion int
	ContentGroupKey string
	ItemType        string
}

// repairAnchoredIdentityMismatchesByFolderAndPathPrefix repairs historical
// wrong-version merges after a scan has persisted provider-anchored group
// keys. It deliberately requires a complete root, an existing unique matched
// provider owner, and a proper source subset. Anything less stays manual.
func (s *MetadataService) repairAnchoredIdentityMismatchesByFolderAndPathPrefix(
	ctx context.Context,
	folderID int,
	pathPrefix string,
) (int, error) {
	return s.repairAnchoredIdentityMismatchesByFolderAndPathPrefixPageSize(
		ctx,
		folderID,
		pathPrefix,
		anchoredIdentityRepairBatchSize,
	)
}

func (s *MetadataService) repairAnchoredIdentityMismatchesByFolderAndPathPrefixPageSize(
	ctx context.Context,
	folderID int,
	pathPrefix string,
	pageSize int,
) (int, error) {
	if s == nil || s.dbPool == nil || folderID <= 0 || strings.TrimSpace(pathPrefix) == "" {
		return 0, nil
	}
	if pageSize <= 0 {
		pageSize = anchoredIdentityRepairBatchSize
	}

	pathPrefix = filepath.Clean(pathPrefix)
	var afterGroup anchoredIdentityRepairGroupCursor
	repaired := 0
	for {
		candidateGroups, err := s.loadAnchoredIdentityRepairCandidateGroups(
			ctx,
			folderID,
			pathPrefix,
			afterGroup,
			pageSize,
		)
		if err != nil {
			return repaired, err
		}
		if len(candidateGroups) == 0 {
			if err := s.ensureAnchoredIdentityRepairEpisodeLinks(ctx, folderID, pathPrefix); err != nil {
				return repaired, err
			}
			return repaired, nil
		}

		for _, candidates := range candidateGroups {
			didRepair, repairErr := s.repairAnchoredIdentityMismatchGroup(ctx, candidates)
			if repairErr != nil {
				return repaired, repairErr
			}
			if didRepair {
				repaired += len(candidates)
				if candidates[0].ItemType == anchoredItemTypeSeries {
					anchor, valid := parseAnchoredGroupIdentity(
						candidates[0].GroupKeyVersion,
						candidates[0].ContentGroupKey,
					)
					if valid {
						if followUpErr := s.ensureAnchoredIdentityRepairTargetEpisodeLinks(
							ctx,
							anchor.TargetContent,
						); followUpErr != nil {
							return repaired, followUpErr
						}
					}
				}
			}
		}
		lastGroup := candidateGroups[len(candidateGroups)-1]
		afterGroup = anchoredIdentityRepairCursor(lastGroup[0])
	}
}

func (s *MetadataService) ensureAnchoredIdentityRepairEpisodeLinks(
	ctx context.Context,
	folderID int,
	pathPrefix string,
) error {
	rows, err := s.dbPool.Query(ctx, `
		SELECT DISTINCT mf.content_id
		FROM media_files mf
		JOIN media_items item
		  ON item.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.episode_id IS NULL
		  AND LOWER(TRIM(mf.base_type)) = 'series'
		  AND SPLIT_PART(mf.content_group_key, '|', 3) = 'anchor'
		  AND mf.content_id =
		      LOWER(TRIM(mf.base_type)) || '-' || SPLIT_PART(mf.content_group_key, '|', 4)
		  AND LOWER(TRIM(item.status)) = 'matched'
		  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
		ORDER BY mf.content_id
	`, folderID, pathPrefix, pathscope.PrefixLike(pathPrefix))
	if err != nil {
		return fmt.Errorf("querying anchored repair episode follow-ups: %w", err)
	}

	contentIDs := make([]string, 0)
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			rows.Close()
			return fmt.Errorf("scanning anchored repair episode follow-up: %w", err)
		}
		contentIDs = append(contentIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterating anchored repair episode follow-ups: %w", err)
	}
	rows.Close()

	for _, contentID := range contentIDs {
		if err := s.ensureAnchoredIdentityRepairTargetEpisodeLinks(ctx, contentID); err != nil {
			return err
		}
	}
	return nil
}

func (s *MetadataService) ensureAnchoredIdentityRepairTargetEpisodeLinks(
	ctx context.Context,
	contentID string,
) error {
	if err := s.ensureSeriesEpisodeLinks(ctx, contentID); err != nil {
		queueErr := s.RequestStaleMetadataRefresh(ctx, RefreshTargetItem, contentID)
		slog.WarnContext(ctx, "metadata: anchored repair episode relink failed", "component", "metadata",
			"content_id", contentID,
			"error", err,
			"refresh_queued", queueErr == nil,
		)
		if queueErr != nil {
			return errors.Join(
				fmt.Errorf("ensuring anchored repair episode links for %s: %w", contentID, err),
				fmt.Errorf("queueing anchored repair target refresh for %s: %w", contentID, queueErr),
			)
		}
	}
	return nil
}

func (s *MetadataService) loadAnchoredIdentityRepairCandidateGroups(
	ctx context.Context,
	folderID int,
	pathPrefix string,
	afterGroup anchoredIdentityRepairGroupCursor,
	pageSize int,
) ([][]anchoredIdentityRepairCandidate, error) {
	rows, err := s.dbPool.Query(ctx, `
		WITH scoped_roots AS (
			SELECT DISTINCT mf.observed_root_path
			FROM media_files mf
			WHERE mf.media_folder_id = $1
			  AND mf.missing_since IS NULL
			  AND COALESCE(mf.observed_root_path, '') <> ''
			  AND SPLIT_PART(mf.content_group_key, '|', 3) = 'anchor'
			  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
		),
		root_summaries AS (
			SELECT
				mf.media_folder_id,
				mf.observed_root_path,
				MIN(mf.content_id) AS source_content_id,
				MIN(mf.group_key_version) AS group_key_version,
				MIN(mf.content_group_key) AS content_group_key,
				MIN(LOWER(TRIM(mf.base_type))) AS item_type,
				COUNT(*)::int AS file_count
			FROM media_files mf
			JOIN scoped_roots scoped
			  ON scoped.observed_root_path = mf.observed_root_path
			WHERE mf.media_folder_id = $1
			  AND mf.missing_since IS NULL
			GROUP BY mf.media_folder_id, mf.observed_root_path
			HAVING COUNT(*) FILTER (
					WHERE mf.content_id IS NULL OR TRIM(mf.content_id) = ''
				) = 0
			   AND COUNT(DISTINCT mf.content_id) = 1
			   AND COUNT(DISTINCT (mf.group_key_version, mf.content_group_key)) = 1
			   AND COUNT(DISTINCT LOWER(TRIM(mf.base_type))) = 1
		),
		candidate_summaries AS (
			SELECT
				summary.media_folder_id,
				summary.observed_root_path,
				summary.source_content_id,
				summary.group_key_version,
				summary.content_group_key,
				summary.item_type,
				summary.file_count
			FROM root_summaries summary
			JOIN media_items source
			  ON source.content_id = summary.source_content_id
			WHERE summary.item_type IN ('movie', 'series')
			  AND source.type = summary.item_type
			  AND LOWER(TRIM(source.status)) = 'matched'
			  AND SPLIT_PART(summary.content_group_key, '|', 2) = summary.item_type
			  AND SPLIT_PART(summary.content_group_key, '|', 3) = 'anchor'
			  AND summary.source_content_id <>
			      summary.item_type || '-' || SPLIT_PART(summary.content_group_key, '|', 4)
			  AND (
				SELECT COUNT(*)
				FROM media_files source_file
				WHERE source_file.content_id = summary.source_content_id
				  AND source_file.missing_since IS NULL
			  ) > summary.file_count
			  AND NOT EXISTS (
				SELECT 1
				FROM media_identity_overrides identity_override
				WHERE identity_override.media_folder_id = summary.media_folder_id
				  AND (
					(identity_override.scope = 'root' AND identity_override.root_path = summary.observed_root_path) OR
					(identity_override.scope = 'file' AND EXISTS (
						SELECT 1
						FROM media_files root_file
						WHERE root_file.media_folder_id = summary.media_folder_id
						  AND root_file.observed_root_path = summary.observed_root_path
						  AND root_file.file_path = identity_override.file_path
						  AND root_file.missing_since IS NULL
					))
				  )
			  )
		),
		candidate_groups AS (
			SELECT
				source_content_id,
				group_key_version,
				content_group_key,
				item_type
			FROM candidate_summaries
			WHERE (
				source_content_id,
				group_key_version,
				content_group_key,
				item_type
			) > ($4::text, $5::int, $6::text, $7::text)
			GROUP BY
				source_content_id,
				group_key_version,
				content_group_key,
				item_type
			ORDER BY
				source_content_id,
				group_key_version,
				content_group_key,
				item_type
			LIMIT $8
		)
		SELECT
			summary.media_folder_id,
			summary.observed_root_path,
			summary.source_content_id,
			summary.group_key_version,
			summary.content_group_key,
			summary.item_type,
			summary.file_count
		FROM candidate_summaries summary
		JOIN candidate_groups candidate_group
		  USING (source_content_id, group_key_version, content_group_key, item_type)
		ORDER BY
			summary.source_content_id,
			summary.group_key_version,
			summary.content_group_key,
			summary.item_type,
			summary.observed_root_path
	`,
		folderID,
		pathPrefix,
		pathscope.PrefixLike(pathPrefix),
		afterGroup.SourceContentID,
		afterGroup.GroupKeyVersion,
		afterGroup.ContentGroupKey,
		afterGroup.ItemType,
		pageSize,
	)
	if err != nil {
		return nil, fmt.Errorf("querying anchored identity repairs: %w", err)
	}
	defer rows.Close()

	candidateGroups := make([][]anchoredIdentityRepairCandidate, 0, pageSize)
	for rows.Next() {
		var candidate anchoredIdentityRepairCandidate
		if err := rows.Scan(
			&candidate.FolderID,
			&candidate.ObservedRootPath,
			&candidate.SourceContentID,
			&candidate.GroupKeyVersion,
			&candidate.ContentGroupKey,
			&candidate.ItemType,
			&candidate.FileCount,
		); err != nil {
			return nil, fmt.Errorf("scanning anchored identity repair: %w", err)
		}
		if len(candidateGroups) == 0 ||
			!sameAnchoredIdentityRepairGroup(candidateGroups[len(candidateGroups)-1][0], candidate) {
			candidateGroups = append(candidateGroups, nil)
		}
		lastIndex := len(candidateGroups) - 1
		candidateGroups[lastIndex] = append(candidateGroups[lastIndex], candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating anchored identity repairs: %w", err)
	}
	return candidateGroups, nil
}

func anchoredIdentityRepairCursor(
	candidate anchoredIdentityRepairCandidate,
) anchoredIdentityRepairGroupCursor {
	return anchoredIdentityRepairGroupCursor{
		SourceContentID: candidate.SourceContentID,
		GroupKeyVersion: candidate.GroupKeyVersion,
		ContentGroupKey: candidate.ContentGroupKey,
		ItemType:        candidate.ItemType,
	}
}

func sameAnchoredIdentityRepairGroup(
	left anchoredIdentityRepairCandidate,
	right anchoredIdentityRepairCandidate,
) bool {
	return left.FolderID == right.FolderID &&
		left.SourceContentID == right.SourceContentID &&
		left.GroupKeyVersion == right.GroupKeyVersion &&
		left.ContentGroupKey == right.ContentGroupKey &&
		left.ItemType == right.ItemType
}

func (s *MetadataService) repairAnchoredIdentityMismatchGroup(
	ctx context.Context,
	candidates []anchoredIdentityRepairCandidate,
) (bool, error) {
	if len(candidates) == 0 {
		return false, nil
	}
	candidates = append([]anchoredIdentityRepairCandidate(nil), candidates...)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ObservedRootPath < candidates[j].ObservedRootPath
	})
	candidate := candidates[0]
	for _, groupedCandidate := range candidates[1:] {
		if !sameAnchoredIdentityRepairGroup(candidate, groupedCandidate) {
			return false, nil
		}
	}

	anchor, ok := parseAnchoredGroupIdentity(
		candidate.GroupKeyVersion,
		candidate.ContentGroupKey,
	)
	if !ok || anchor.ItemType != candidate.ItemType || anchor.TargetContent == candidate.SourceContentID {
		return false, nil
	}

	tx, err := s.dbPool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning anchored identity repair: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	items, err := lockRepairItems(ctx, tx, candidate.SourceContentID, anchor.TargetContent)
	if err != nil {
		return false, err
	}
	if len(items) != 2 ||
		items[candidate.SourceContentID] != candidate.ItemType ||
		items[anchor.TargetContent] != candidate.ItemType {
		return false, nil
	}

	activeSourceFiles, err := lockActiveSourceFiles(ctx, tx, candidate.SourceContentID)
	if err != nil {
		return false, err
	}

	fileCapacity := 0
	for _, groupedCandidate := range candidates {
		fileCapacity += groupedCandidate.FileCount
	}
	files := make([]filesplit.File, 0, fileCapacity)
	for _, groupedCandidate := range candidates {
		rootFiles, lockErr := lockRepairRootFiles(
			ctx,
			tx,
			groupedCandidate.FolderID,
			groupedCandidate.ObservedRootPath,
		)
		if lockErr != nil {
			return false, lockErr
		}
		if len(rootFiles) == 0 || len(rootFiles) != groupedCandidate.FileCount {
			return false, nil
		}
		for _, file := range rootFiles {
			fileAnchor, valid := parseAnchoredGroupIdentity(file.GroupKeyVersion, file.ContentGroupKey)
			if !valid ||
				file.ContentID != groupedCandidate.SourceContentID ||
				file.GroupKeyVersion != groupedCandidate.GroupKeyVersion ||
				file.ContentGroupKey != groupedCandidate.ContentGroupKey ||
				fileAnchor.TargetContent != anchor.TargetContent ||
				file.BaseType != groupedCandidate.ItemType ||
				file.ObservedRootPath != groupedCandidate.ObservedRootPath {
				return false, nil
			}
		}
		hasOverride, overrideErr := rootHasIdentityOverride(
			ctx,
			tx,
			groupedCandidate.FolderID,
			groupedCandidate.ObservedRootPath,
		)
		if overrideErr != nil {
			return false, overrideErr
		}
		if hasOverride {
			return false, nil
		}
		files = append(files, rootFiles...)
	}

	if activeSourceFiles <= len(files) {
		return false, nil
	}

	owners, err := matchedProviderOwners(ctx, tx, anchor)
	if err != nil {
		return false, err
	}
	if len(owners) != 1 || owners[0] != anchor.TargetContent {
		return false, nil
	}

	moveResult, err := filesplit.Move(ctx, tx, filesplit.Options{
		FromContentID: candidate.SourceContentID,
		ToContentID:   anchor.TargetContent,
		ItemType:      candidate.ItemType,
		Files:         files,
		HistoryMode:   reattribute.HistoryModeEvidence,
	})
	if err != nil {
		return false, fmt.Errorf(
			"repairing %d anchored roots from %s to %s: %w",
			len(candidates),
			candidate.SourceContentID,
			anchor.TargetContent,
			err,
		)
	}

	var sourceStillHasActiveFiles bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM media_files
			WHERE content_id = $1
			  AND missing_since IS NULL
		)
	`, candidate.SourceContentID).Scan(&sourceStillHasActiveFiles); err != nil {
		return false, fmt.Errorf("checking anchored repair source remainder: %w", err)
	}
	if !sourceStillHasActiveFiles {
		// The candidate ceased to be a proper subset while this transaction
		// waited for locks. Roll back and let whole-item matching handle it.
		return false, nil
	}

	claimsReconciled, err := reconcileAnchoredRepairClaims(
		ctx,
		tx,
		candidate,
		anchor.TargetContent,
		files,
	)
	if err != nil {
		return false, err
	}
	if !claimsReconciled {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_item_libraries membership
		WHERE membership.content_id = $1
		  AND membership.media_folder_id = $2
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files source_file
			WHERE source_file.content_id = membership.content_id
			  AND source_file.media_folder_id = membership.media_folder_id
			  AND source_file.missing_since IS NULL
		  )
	`, candidate.SourceContentID, candidate.FolderID); err != nil {
		return false, fmt.Errorf("cleaning anchored repair source membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing anchored identity repair: %w", err)
	}

	slog.InfoContext(ctx, "metadata: repaired provider-anchored root ownership",
		"component", "metadata",
		"folder_id", candidate.FolderID,
		"root_count", len(candidates),
		"observed_root_path", candidate.ObservedRootPath,
		"source_content_id", candidate.SourceContentID,
		"target_content_id", anchor.TargetContent,
		"files_moved", len(files),
		"episode_pairs", len(moveResult.EpisodePairs),
		"history_moved", moveResult.Reattribution.HistoryMoved,
		"history_ambiguous", moveResult.Reattribution.HistoryAmbiguous,
	)
	return true, nil
}

func parseAnchoredGroupIdentity(groupKeyVersion int, groupKey string) (anchoredGroupIdentity, bool) {
	parts := strings.Split(strings.TrimSpace(groupKey), "|")
	if len(parts) != 4 || parts[2] != "anchor" {
		return anchoredGroupIdentity{}, false
	}
	versionText, found := strings.CutPrefix(parts[0], "v")
	if !found {
		return anchoredGroupIdentity{}, false
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version <= 0 || version != groupKeyVersion {
		return anchoredGroupIdentity{}, false
	}
	itemType := strings.TrimSpace(parts[1])
	provider, providerID, found := strings.Cut(strings.TrimSpace(parts[3]), "-")
	if !found || (itemType != anchoredItemTypeMovie && itemType != anchoredItemTypeSeries) {
		return anchoredGroupIdentity{}, false
	}

	ids := contentid.ProviderIDs{}
	switch provider {
	case contentid.ProviderTMDB:
		ids.Tmdb = providerID
	case contentid.ProviderIMDB:
		ids.Imdb = providerID
	case contentid.ProviderTVDB:
		ids.Tvdb = providerID
	default:
		return anchoredGroupIdentity{}, false
	}

	var target string
	var ok bool
	if itemType == anchoredItemTypeSeries {
		target, ok = contentid.ForSeries(ids)
	} else {
		target, ok = contentid.ForMovie(ids)
	}
	if !ok || target != itemType+"-"+provider+"-"+strings.ToLower(strings.TrimSpace(providerID)) {
		return anchoredGroupIdentity{}, false
	}
	return anchoredGroupIdentity{
		Version:       version,
		ItemType:      itemType,
		Provider:      provider,
		ProviderID:    strings.ToLower(strings.TrimSpace(providerID)),
		TargetContent: target,
	}, true
}

func lockActiveSourceFiles(
	ctx context.Context,
	tx pgx.Tx,
	sourceContentID string,
) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT id
		FROM media_files
		WHERE content_id = $1
		  AND missing_since IS NULL
		ORDER BY id
		FOR UPDATE
	`, sourceContentID)
	if err != nil {
		return 0, fmt.Errorf("locking anchored repair source files: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var fileID int
		if err := rows.Scan(&fileID); err != nil {
			return 0, fmt.Errorf("scanning anchored repair source lock: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterating anchored repair source locks: %w", err)
	}
	return count, nil
}

func lockRepairItems(ctx context.Context, tx pgx.Tx, sourceContentID, targetContentID string) (map[string]string, error) {
	contentIDs := []string{sourceContentID, targetContentID}
	sort.Strings(contentIDs)
	rows, err := tx.Query(ctx, `
		SELECT content_id, type
		FROM media_items
		WHERE content_id = ANY($1::text[])
		  AND LOWER(TRIM(status)) = 'matched'
		ORDER BY content_id ASC
		FOR UPDATE
	`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("locking anchored repair items: %w", err)
	}
	defer rows.Close()

	items := make(map[string]string, 2)
	for rows.Next() {
		var contentID, itemType string
		if err := rows.Scan(&contentID, &itemType); err != nil {
			return nil, fmt.Errorf("scanning anchored repair item: %w", err)
		}
		items[contentID] = strings.ToLower(strings.TrimSpace(itemType))
	}
	return items, rows.Err()
}

func lockRepairRootFiles(
	ctx context.Context,
	tx pgx.Tx,
	folderID int,
	observedRootPath string,
) ([]filesplit.File, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			id,
			COALESCE(content_id, ''),
			media_folder_id,
			file_path,
			COALESCE(canonical_root_path, ''),
			COALESCE(observed_root_path, ''),
			COALESCE(group_key_version, 1),
			COALESCE(content_group_key, ''),
			LOWER(TRIM(COALESCE(base_type, ''))),
			COALESCE(season_number, 0),
			COALESCE(episode_number, 0),
			COALESCE(episode_id, '')
		FROM media_files
		WHERE media_folder_id = $1
		  AND observed_root_path = $2
		  AND missing_since IS NULL
		ORDER BY id ASC
		FOR UPDATE
	`, folderID, observedRootPath)
	if err != nil {
		return nil, fmt.Errorf("locking anchored repair files: %w", err)
	}
	defer rows.Close()

	files := make([]filesplit.File, 0)
	for rows.Next() {
		var file filesplit.File
		if err := rows.Scan(
			&file.ID,
			&file.ContentID,
			&file.MediaFolderID,
			&file.FilePath,
			&file.CanonicalRootPath,
			&file.ObservedRootPath,
			&file.GroupKeyVersion,
			&file.ContentGroupKey,
			&file.BaseType,
			&file.SeasonNumber,
			&file.EpisodeNumber,
			&file.EpisodeID,
		); err != nil {
			return nil, fmt.Errorf("scanning anchored repair file: %w", err)
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func rootHasIdentityOverride(ctx context.Context, tx pgx.Tx, folderID int, observedRootPath string) (bool, error) {
	var hasOverride bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM media_identity_overrides identity_override
			WHERE identity_override.media_folder_id = $1
			  AND (
				(identity_override.scope = 'root' AND identity_override.root_path = $2) OR
				(identity_override.scope = 'file' AND EXISTS (
					SELECT 1
					FROM media_files root_file
					WHERE root_file.media_folder_id = $1
					  AND root_file.observed_root_path = $2
					  AND root_file.file_path = identity_override.file_path
					  AND root_file.missing_since IS NULL
				))
			  )
		)
	`, folderID, observedRootPath).Scan(&hasOverride); err != nil {
		return false, fmt.Errorf("checking anchored repair identity overrides: %w", err)
	}
	return hasOverride, nil
}

func matchedProviderOwners(
	ctx context.Context,
	tx pgx.Tx,
	anchor anchoredGroupIdentity,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		WITH owners AS (
			SELECT provider_id.content_id
			FROM media_item_provider_ids provider_id
			JOIN media_items item
			  ON item.content_id = provider_id.content_id
			WHERE provider_id.item_type = $1
			  AND provider_id.provider = $2
			  AND provider_id.provider_id = $3
			  AND LOWER(TRIM(item.status)) = 'matched'
			UNION
			SELECT item.content_id
			FROM media_items item
			WHERE item.type = $1
			  AND LOWER(TRIM(item.status)) = 'matched'
			  AND (
				($2 = 'tmdb' AND item.tmdb_id = $3) OR
				($2 = 'imdb' AND LOWER(item.imdb_id) = $3) OR
				($2 = 'tvdb' AND item.tvdb_id = $3)
			  )
		)
		SELECT content_id
		FROM owners
		ORDER BY content_id ASC
	`, anchor.ItemType, anchor.Provider, anchor.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("querying matched provider owners: %w", err)
	}
	defer rows.Close()

	owners := make([]string, 0, 2)
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning matched provider owner: %w", err)
		}
		owners = append(owners, contentID)
	}
	return owners, rows.Err()
}

func reconcileAnchoredRepairClaims(
	ctx context.Context,
	tx pgx.Tx,
	candidate anchoredIdentityRepairCandidate,
	targetContentID string,
	files []filesplit.File,
) (bool, error) {
	canonicalRoots := make([]string, 0)
	seenRoots := make(map[string]bool)
	for _, file := range files {
		root := file.CanonicalRootPath
		if strings.TrimSpace(root) != "" && !seenRoots[root] {
			seenRoots[root] = true
			canonicalRoots = append(canonicalRoots, root)
		}
	}
	sort.Strings(canonicalRoots)

	for _, root := range canonicalRoots {
		tag, err := tx.Exec(ctx, `
			INSERT INTO media_item_roots (
				media_folder_id, canonical_root_path, content_id, last_seen_at
			)
			SELECT $1, $2, $3, NOW()
			WHERE NOT EXISTS (
				SELECT 1
				FROM media_files other_file
				WHERE other_file.media_folder_id = $1
				  AND other_file.canonical_root_path = $2
				  AND other_file.missing_since IS NULL
				  AND other_file.content_id IS DISTINCT FROM $3
			)
			ON CONFLICT (media_folder_id, canonical_root_path) DO UPDATE
			SET content_id = EXCLUDED.content_id,
				last_seen_at = NOW()
			WHERE NOT EXISTS (
				SELECT 1
				FROM media_files other_file
				WHERE other_file.media_folder_id = EXCLUDED.media_folder_id
				  AND other_file.canonical_root_path = EXCLUDED.canonical_root_path
				  AND other_file.missing_since IS NULL
				  AND other_file.content_id IS DISTINCT FROM EXCLUDED.content_id
			)
		`, candidate.FolderID, root, targetContentID)
		if err != nil {
			return false, fmt.Errorf("reconciling anchored repair root claim: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return false, nil
		}
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO media_item_groups (
			media_folder_id, group_key_version, content_group_key, content_id, last_seen_at
		)
		SELECT $1, $2, $3, $4, NOW()
		WHERE NOT EXISTS (
			SELECT 1
			FROM media_files other_file
			WHERE other_file.media_folder_id = $1
			  AND other_file.group_key_version = $2
			  AND other_file.content_group_key = $3
			  AND other_file.missing_since IS NULL
			  AND other_file.content_id IS DISTINCT FROM $4
		)
		ON CONFLICT (media_folder_id, group_key_version, content_group_key) DO UPDATE
		SET content_id = EXCLUDED.content_id,
			last_seen_at = NOW()
		WHERE NOT EXISTS (
			SELECT 1
			FROM media_files other_file
			WHERE other_file.media_folder_id = EXCLUDED.media_folder_id
			  AND other_file.group_key_version = EXCLUDED.group_key_version
			  AND other_file.content_group_key = EXCLUDED.content_group_key
			  AND other_file.missing_since IS NULL
			  AND other_file.content_id IS DISTINCT FROM EXCLUDED.content_id
		)
	`, candidate.FolderID, candidate.GroupKeyVersion, candidate.ContentGroupKey, targetContentID)
	if err != nil {
		return false, fmt.Errorf("reconciling anchored repair group claim: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
