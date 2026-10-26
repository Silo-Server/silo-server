package trickplay

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/blobgc"
)

// BlobNamespace describes trickplay storage to blobgc. Sheets are deleted
// per revision, trickplay/<media_files.id>/<revision>/: a revision is live
// while its file's row publishes it, or while a running generation uploads
// under it.
func BlobNamespace() blobgc.Namespace {
	return blobgc.Namespace{
		Root:  keyRoot,
		Group: revisionGroup,
		Live:  liveRevisions,
	}
}

// revisionGroup returns the revision prefix of a sheet key.
func revisionGroup(key string) (string, bool) {
	fileID, revision, ok := parseRevisionKey(key)
	if !ok {
		return "", false
	}
	return revisionPrefix(fileID, revision), true
}

// parseRevisionKey reads the file id and revision a key starts with.
func parseRevisionKey(key string) (int, int64, bool) {
	rest, ok := strings.CutPrefix(key, keyRoot)
	if !ok {
		return 0, 0, false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		return 0, 0, false
	}
	fileID, err := strconv.Atoi(parts[0])
	if err != nil || fileID <= 0 || strconv.Itoa(fileID) != parts[0] {
		return 0, 0, false
	}
	revision, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || revision <= 0 || strconv.FormatInt(revision, 10) != parts[1] {
		return 0, 0, false
	}
	return fileID, revision, true
}

func liveRevisions(ctx context.Context, db blobgc.Querier, prefixes []string) (map[string]bool, error) {
	var fileIDs []int
	for _, prefix := range prefixes {
		if fileID, _, ok := parseRevisionKey(prefix); ok {
			fileIDs = append(fileIDs, fileID)
		}
	}
	rows, err := db.Query(ctx, `
		SELECT media_file_id, revision, CASE WHEN state = 'running' THEN work_revision END
		FROM public.media_file_trickplay WHERE media_file_id = ANY($1)`, fileIDs)
	if err != nil {
		return nil, fmt.Errorf("look up trickplay revisions: %w", err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var fileID int
		var revision, working *int64
		if err := rows.Scan(&fileID, &revision, &working); err != nil {
			return nil, err
		}
		for _, rev := range []*int64{revision, working} {
			if rev != nil {
				live[revisionPrefix(fileID, *rev)] = true
			}
		}
	}
	return live, rows.Err()
}
