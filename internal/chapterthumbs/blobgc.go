package chapterthumbs

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/blobgc"
)

// BlobNamespace describes chapter thumbnail storage to blobgc. A file's
// prefix is live while its media_files row exists; media_files ids are never
// reused, so once the row is gone nothing will write there again.
func BlobNamespace() blobgc.Namespace {
	return blobgc.Namespace{
		Root:  chapterImagesPrefix,
		Group: imagesGroup,
		Live:  liveImagePrefixes,
	}
}

// ImageBlobNamespace describes single chapter thumbnails to blobgc: an image
// a newer one replaced, after a preview width change, is queued by its own
// key and deleted once its file's chapters no longer reference it. It is for
// the Collector only. The sweep lists by file (BlobNamespace): an image
// replaced moments ago is old by its storage time, so a sweep could not give
// it the grace its queue entry does.
func ImageBlobNamespace() blobgc.Namespace {
	return blobgc.Namespace{
		Root:  chapterImagesPrefix,
		Group: imageKeyGroup,
		Live:  referencedImages,
	}
}

// imageKeyGroup accepts a chapter thumbnail key,
// chapter-images/{file_id}/{chapter_index}-{sha256}/w{width}.webp, as its own
// group. Numeric chapter-index directories from older writes remain valid.
func imageKeyGroup(key string) (string, bool) {
	if _, ok := imagesFileID(key); !ok {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(key, chapterImagesPrefix), "/")
	if len(parts) != 3 || !chapterImageIndex(parts[1]) {
		return "", false
	}
	width, ok := strings.CutSuffix(parts[2], ".webp")
	if !ok {
		return "", false
	}
	width, ok = strings.CutPrefix(width, "w")
	if !ok || !canonicalNumber(width, false) {
		return "", false
	}
	return key, true
}

func chapterImageIndex(value string) bool {
	if strings.HasPrefix(value, "marker-") {
		parts := strings.Split(value, "-")
		return len(parts) == 3 && lowerHexDigest(parts[1]) && lowerHexDigest(parts[2])
	}
	index, digest, hashed := strings.Cut(value, "-")
	if !canonicalNumber(index, true) {
		return false
	}
	if !hashed {
		return true
	}
	if len(digest) != sha256HexLength {
		return false
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

const sha256HexLength = 64

// canonicalNumber reports whether s is a decimal number without leading
// zeros; zero itself only when allowZero.
func canonicalNumber(s string, allowZero bool) bool {
	n, err := strconv.Atoi(s)
	return err == nil && strconv.Itoa(n) == s && (n > 0 || (allowZero && n == 0))
}

// referencedImages reports which keys a chapter of their file still points
// at. A key whose file is gone is not referenced.
func referencedImages(ctx context.Context, db blobgc.Querier, keys []string) (map[string]bool, error) {
	ids := make([]int64, 0, len(keys))
	for _, key := range keys {
		if id, ok := imagesFileID(key); ok {
			ids = append(ids, int64(id))
		}
	}
	rows, err := db.Query(ctx, `
		SELECT DISTINCT chapter->>'thumbnail_path'
		FROM public.media_files mf
		CROSS JOIN LATERAL jsonb_array_elements(
			(CASE WHEN jsonb_typeof(mf.chapters) = 'array' THEN mf.chapters ELSE '[]'::jsonb END) || mf.marker_thumbnails
		) AS chapter
		WHERE mf.id = ANY($1::bigint[])
		  AND chapter->>'thumbnail_path' = ANY($2::text[])`, ids, keys)
	if err != nil {
		return nil, fmt.Errorf("look up chapter thumbnails: %w", err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		live[key] = true
	}
	return live, rows.Err()
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

func liveImagePrefixes(ctx context.Context, db blobgc.Querier, prefixes []string) (map[string]bool, error) {
	ids := make([]int64, 0, len(prefixes))
	for _, prefix := range prefixes {
		if id, ok := imagesFileID(prefix); ok {
			ids = append(ids, int64(id))
		}
	}
	rows, err := db.Query(ctx, `SELECT id FROM public.media_files WHERE id = ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("look up media files: %w", err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[chapterImagesPrefix+strconv.FormatInt(id, 10)+"/"] = true
	}
	return live, rows.Err()
}

func lowerHexDigest(value string) bool {
	if len(value) != sha256HexLength {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
