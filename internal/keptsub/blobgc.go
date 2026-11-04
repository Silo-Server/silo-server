package keptsub

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/blobgc"
)

// StreamBlobNamespace describes kept streams to the blobgc Collector, by
// key: a stream is queued when its row goes (a database trigger, whichever
// path removes it) and is deleted once no row names it. It is for the
// Collector only; the sweep lists by file (FileBlobNamespace).
func StreamBlobNamespace() blobgc.Namespace {
	return blobgc.Namespace{Root: keyRoot, Group: streamKeyGroup, Live: liveStreamKeys}
}

// FileBlobNamespace describes kept streams by file: a file's prefix,
// subtitles/embedded/<media_files.id>/, is live while the media file is.
// The sweep queues the prefix of a deleted file whose streams no row
// queued; media_files ids are never reused.
func FileBlobNamespace() blobgc.Namespace {
	return blobgc.Namespace{Root: keyRoot, Group: fileGroup, Live: liveFilePrefixes}
}

// streamKeyGroup accepts a kept stream key,
// subtitles/embedded/<file id>/<stream index>-<sha256>.<mks|mp4>, as its own
// group.
func streamKeyGroup(key string) (string, bool) {
	if _, ok := keyFileID(key); !ok {
		return "", false
	}
	name := key[strings.LastIndexByte(key, '/')+1:]
	if strings.Count(strings.TrimPrefix(key, keyRoot), "/") != 1 {
		return "", false
	}
	base, ok := strings.CutSuffix(name, ".mks")
	if !ok {
		if base, ok = strings.CutSuffix(name, ".mp4"); !ok {
			return "", false
		}
	}
	stream, digest, ok := strings.Cut(base, "-")
	if !ok || !canonicalNumber(stream, true) || !lowerHexSHA256(digest) {
		return "", false
	}
	return key, true
}

// fileGroup returns the file prefix of a kept stream key.
func fileGroup(key string) (string, bool) {
	id, ok := keyFileID(key)
	if !ok {
		return "", false
	}
	return keyRoot + strconv.Itoa(id) + "/", true
}

// keyFileID parses the media file id a key under keyRoot starts with.
func keyFileID(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, keyRoot)
	if !ok {
		return 0, false
	}
	digits, _, ok := strings.Cut(rest, "/")
	if !ok || !canonicalNumber(digits, false) {
		return 0, false
	}
	id, _ := strconv.Atoi(digits)
	return id, true
}

// canonicalNumber reports whether s is a decimal number without leading
// zeros; zero itself only when allowZero.
func canonicalNumber(s string, allowZero bool) bool {
	n, err := strconv.Atoi(s)
	return err == nil && strconv.Itoa(n) == s && (n > 0 || (allowZero && n == 0))
}

func lowerHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// liveStreamKeys reports which keys a kept track row still names.
func liveStreamKeys(ctx context.Context, db blobgc.Querier, keys []string) (map[string]bool, error) {
	ids := make([]int64, 0, len(keys))
	for _, key := range keys {
		if id, ok := keyFileID(key); ok {
			ids = append(ids, int64(id))
		}
	}
	rows, err := db.Query(ctx, `SELECT blob_key FROM public.embedded_subtitle_tracks
		WHERE media_file_id = ANY($1::bigint[]) AND blob_key = ANY($2::text[])`, ids, keys)
	if err != nil {
		return nil, fmt.Errorf("look up kept subtitle tracks: %w", err)
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

func liveFilePrefixes(ctx context.Context, db blobgc.Querier, prefixes []string) (map[string]bool, error) {
	ids := make([]int64, 0, len(prefixes))
	for _, prefix := range prefixes {
		if id, ok := keyFileID(prefix); ok {
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
		live[keyRoot+strconv.FormatInt(id, 10)+"/"] = true
	}
	return live, rows.Err()
}
