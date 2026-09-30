package chapterthumbs

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobgc"
)

// BlobNamespace describes chapter thumbnail storage to blobgc. A file's
// prefix is live while its media_files row exists; media_files ids are never
// reused, so once the row is gone nothing will write there again.
func BlobNamespace(pool *pgxpool.Pool) blobgc.Namespace {
	return blobgc.Namespace{
		Root:  chapterImagesPrefix,
		Group: imagesGroup,
		Live: func(ctx context.Context, prefixes []string) (map[string]bool, error) {
			return liveImagePrefixes(ctx, pool, prefixes)
		},
	}
}

// imagesGroup returns the file prefix of a chapter thumbnail key.
func imagesGroup(key string) (string, bool) {
	id, ok := imagesFileID(key)
	if !ok {
		return "", false
	}
	return chapterImagesPrefix + strconv.Itoa(id) + "/", true
}

// imagesFileID parses the media file id of a key under chapterImagesPrefix.
func imagesFileID(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, chapterImagesPrefix)
	if !ok {
		return 0, false
	}
	digits, _, ok := strings.Cut(rest, "/")
	if !ok || digits == "" || digits[0] == '0' {
		return 0, false
	}
	id, err := strconv.Atoi(digits)
	if err != nil || id <= 0 || strconv.Itoa(id) != digits {
		return 0, false
	}
	return id, true
}

func liveImagePrefixes(ctx context.Context, pool *pgxpool.Pool, prefixes []string) (map[string]bool, error) {
	ids := make([]int, 0, len(prefixes))
	for _, prefix := range prefixes {
		if id, ok := imagesFileID(prefix); ok {
			ids = append(ids, id)
		}
	}
	rows, err := pool.Query(ctx, `SELECT id FROM public.media_files WHERE id = ANY($1::int[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("look up media files: %w", err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[chapterImagesPrefix+strconv.Itoa(id)+"/"] = true
	}
	return live, rows.Err()
}
