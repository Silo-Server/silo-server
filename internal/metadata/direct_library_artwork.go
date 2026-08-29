package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
)

const directLibraryFingerprintCacheEntries = 256

// directLibraryFingerprintTTL bounds how long a cached fingerprint is trusted.
// The cache key is metadata only (path + mtime + size), which cannot detect a
// byte-for-byte replacement that preserves all three — a restored backup or a
// rsync --times copy. Without an expiry such a file would serve a stale ETag,
// answer 304 forever, and never let healReference rotate the reference, because
// the hash is never recomputed. Expiring entries forces a periodic re-hash;
// inode-based keys would be cheaper but are not portable.
const directLibraryFingerprintTTL = time.Hour

type directLibraryFingerprintEntry struct {
	fingerprint string
	usedAt      time.Time
	hashedAt    time.Time
}

type DirectLibraryArtworkFile struct {
	File        *os.File
	Fingerprint string
	MediaType   string
	ModTime     time.Time
	Size        int64
	NotModified bool
}

type DirectLibraryArtworkResolver struct {
	pool         *pgxpool.Pool
	mu           sync.Mutex
	fingerprints map[string]directLibraryFingerprintEntry
	hashFlight   singleflight.Group
	healFlight   singleflight.Group
}

func NewDirectLibraryArtworkResolver(pool *pgxpool.Pool) *DirectLibraryArtworkResolver {
	if pool == nil {
		return nil
	}
	return &DirectLibraryArtworkResolver{pool: pool, fingerprints: make(map[string]directLibraryFingerprintEntry)}
}

func (r *DirectLibraryArtworkResolver) ResolveFile(ctx context.Context, reference string, identity artworkurl.LibraryIdentity, ifNoneMatch string) (DirectLibraryArtworkFile, error) {
	surface, ok := artworkSweepSurfaceByName(identity.Surface)
	if !ok || surface.sourceCol == "" || len(identity.Keys) != len(surface.keyCols) {
		return DirectLibraryArtworkFile{}, fmt.Errorf("direct-library artwork target is invalid")
	}
	values, err := surface.parseKeys(identity.Keys)
	if err != nil {
		return DirectLibraryArtworkFile{}, err
	}
	where := make([]string, len(surface.keyCols))
	for i, key := range surface.keyCols {
		where[i] = fmt.Sprintf("%s = $%d", key.column, i+1)
	}
	query := fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s", surface.pathCol, surface.sourceCol, surface.table, strings.Join(where, " AND "))
	var currentPath, source string
	if err := r.pool.QueryRow(ctx, query, values...).Scan(&currentPath, &source); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DirectLibraryArtworkFile{}, fmt.Errorf("direct-library artwork target no longer exists")
		}
		return DirectLibraryArtworkFile{}, fmt.Errorf("load direct-library artwork target: %w", err)
	}
	if currentPath != reference || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), "file://") {
		return DirectLibraryArtworkFile{}, fmt.Errorf("direct-library artwork source changed")
	}
	roots, err := r.roots(ctx, identity.Surface, identity.Keys)
	if err != nil {
		return DirectLibraryArtworkFile{}, err
	}
	stat, err := StatConfinedLocalArtwork(source, roots)
	if err != nil {
		return DirectLibraryArtworkFile{}, err
	}
	cacheKey := directLibraryFingerprintCacheKey(stat)
	if fingerprint, ok := r.cachedFingerprint(cacheKey); ok {
		if fingerprint != identity.Fingerprint {
			r.healReference(surface, identity, reference, source, fingerprint)
		}
		if directLibraryETagMatches(ifNoneMatch, `"`+fingerprint+`"`) {
			return DirectLibraryArtworkFile{Fingerprint: fingerprint, ModTime: stat.Info.ModTime(), Size: stat.Info.Size(), NotModified: true}, nil
		}
	}
	opened, err := OpenConfinedLocalArtwork(source, roots)
	if err != nil {
		return DirectLibraryArtworkFile{}, err
	}
	cacheKey = directLibraryFingerprintCacheKey(opened)
	fingerprint, err := r.fingerprint(cacheKey, opened)
	if err != nil {
		_ = opened.File.Close()
		return DirectLibraryArtworkFile{}, err
	}
	if fingerprint != identity.Fingerprint {
		r.healReference(surface, identity, reference, source, fingerprint)
	}
	if directLibraryETagMatches(ifNoneMatch, `"`+fingerprint+`"`) {
		_ = opened.File.Close()
		return DirectLibraryArtworkFile{Fingerprint: fingerprint, ModTime: opened.Info.ModTime(), Size: opened.Info.Size(), NotModified: true}, nil
	}
	if _, err := opened.File.Seek(0, io.SeekStart); err != nil {
		_ = opened.File.Close()
		return DirectLibraryArtworkFile{}, err
	}
	return DirectLibraryArtworkFile{File: opened.File, Fingerprint: fingerprint, MediaType: opened.MediaType, ModTime: opened.Info.ModTime(), Size: opened.Info.Size()}, nil
}

func directLibraryFingerprintCacheKey(file ConfinedLocalArtworkFile) string {
	return fmt.Sprintf("%s\x00%d\x00%d", file.Path, file.Info.ModTime().UnixNano(), file.Info.Size())
}

func (r *DirectLibraryArtworkResolver) cachedFingerprint(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.fingerprints[key]
	if !ok {
		return "", false
	}
	now := time.Now()
	if now.Sub(entry.hashedAt) >= directLibraryFingerprintTTL {
		delete(r.fingerprints, key)
		return "", false
	}
	entry.usedAt = now
	r.fingerprints[key] = entry
	return entry.fingerprint, true
}

func directLibraryETagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func (r *DirectLibraryArtworkResolver) fingerprint(key string, opened ConfinedLocalArtworkFile) (string, error) {
	if fingerprint, ok := r.cachedFingerprint(key); ok {
		return fingerprint, nil
	}
	value, err, _ := r.hashFlight.Do(key, func() (any, error) {
		if _, err := opened.File.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, io.LimitReader(opened.File, maxLocalImageSourceBytes+1)); err != nil {
			return "", err
		}
		fingerprint := hex.EncodeToString(hash.Sum(nil))
		r.storeFingerprint(key, fingerprint)
		return fingerprint, nil
	})
	if err != nil {
		return "", err
	}
	fingerprint, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("direct-library artwork fingerprint has unexpected type %T", value)
	}
	return fingerprint, nil
}

func (r *DirectLibraryArtworkResolver) storeFingerprint(key, fingerprint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.fingerprints) >= directLibraryFingerprintCacheEntries {
		oldestKey := ""
		var oldest time.Time
		for candidate, entry := range r.fingerprints {
			if oldestKey == "" || entry.usedAt.Before(oldest) {
				oldestKey, oldest = candidate, entry.usedAt
			}
		}
		delete(r.fingerprints, oldestKey)
	}
	now := time.Now()
	r.fingerprints[key] = directLibraryFingerprintEntry{fingerprint: fingerprint, usedAt: now, hashedAt: now}
}

func (r *DirectLibraryArtworkResolver) healReference(surface artworkSweepSurface, identity artworkurl.LibraryIdentity, oldReference, source, fingerprint string) {
	identity.Fingerprint = fingerprint
	newReference, err := artworkurl.EncodeLibraryReference(identity)
	if err != nil || newReference == oldReference {
		return
	}
	flightKey := identity.Surface + "\x00" + strings.Join(identity.Keys, "\x00")
	go func() {
		_, _, _ = r.healFlight.Do(flightKey, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			values, err := surface.parseKeys(identity.Keys)
			if err != nil {
				return nil, err
			}
			args := []any{newReference, oldReference, source}
			where := []string{surface.pathCol + " = $2", surface.sourceCol + " = $3"}
			for i, key := range surface.keyCols {
				where = append(where, fmt.Sprintf("%s = $%d", key.column, i+4))
				args = append(args, values[i])
			}
			set := surface.pathCol + " = $1"
			if !surface.noUpdatedAt {
				set += ", updated_at = NOW()"
			}
			_, err = r.pool.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE %s", surface.table, set, strings.Join(where, " AND ")), args...)
			if err != nil {
				slog.WarnContext(ctx, "failed to rotate direct-library artwork identity", "surface", surface.name, "error", err)
			}
			return nil, err
		})
	}()
}

func (r *DirectLibraryArtworkResolver) ReadSource(ctx context.Context, surfaceName string, keys []string, source string) (ConfinedLocalArtwork, error) {
	roots, err := r.roots(ctx, surfaceName, keys)
	if err != nil {
		return ConfinedLocalArtwork{}, err
	}
	return ReadConfinedLocalArtwork(source, roots)
}

func (r *DirectLibraryArtworkResolver) roots(ctx context.Context, surfaceName string, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("direct-library artwork identity has no keys")
	}
	var query string
	var arg any = keys[0]
	switch surfaceName {
	case artworkSurfaceItemPosters, artworkSurfaceItemBackdrops, artworkSurfaceItemLogos, artworkSurfaceLocalizedItemPosters, artworkSurfaceLocalizedItemBackdrops, artworkSurfaceLocalizedItemLogos:
		query = `SELECT DISTINCT mfp.path FROM media_folder_paths mfp JOIN media_item_libraries mil ON mil.media_folder_id = mfp.media_folder_id WHERE mil.content_id = $1`
	case artworkSurfaceSeasonPosters:
		query = `SELECT DISTINCT mfp.path FROM seasons s JOIN media_item_libraries mil ON mil.content_id = s.series_id JOIN media_folder_paths mfp ON mfp.media_folder_id = mil.media_folder_id WHERE s.content_id = $1`
	case artworkSurfaceLocalizedSeasonPosters:
		query = `SELECT DISTINCT mfp.path FROM season_localizations loc JOIN seasons s ON s.content_id = loc.season_content_id JOIN media_item_libraries mil ON mil.content_id = s.series_id JOIN media_folder_paths mfp ON mfp.media_folder_id = mil.media_folder_id WHERE loc.season_content_id = $1`
	case artworkSurfaceEpisodeStills:
		query = `SELECT DISTINCT mfp.path FROM episodes ep JOIN media_item_libraries mil ON mil.content_id = ep.series_id JOIN media_folder_paths mfp ON mfp.media_folder_id = mil.media_folder_id WHERE ep.content_id = $1`
	case artworkSurfacePersonPhotos:
		personID, err := strconv.ParseInt(keys[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid person artwork identity")
		}
		arg = personID
		query = `SELECT DISTINCT mfp.path FROM item_people ip JOIN media_item_libraries mil ON mil.content_id = ip.content_id JOIN media_folder_paths mfp ON mfp.media_folder_id = mil.media_folder_id WHERE ip.person_id = $1`
	case artworkSurfaceCollectionPosters, artworkSurfaceCollectionBackdrops:
		query = `SELECT DISTINCT mfp.path FROM library_collections lc JOIN media_folder_paths mfp ON mfp.media_folder_id = lc.library_id WHERE lc.id = $1`
	case artworkSurfaceLibraryPosters:
		libraryID, err := strconv.Atoi(keys[0])
		if err != nil {
			return nil, fmt.Errorf("invalid library artwork identity")
		}
		arg = libraryID
		query = `SELECT path FROM media_folder_paths WHERE media_folder_id = $1`
	default:
		return nil, fmt.Errorf("direct-library artwork target %q is not library-owned", surfaceName)
	}
	rows, err := r.pool.Query(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("resolve direct-library artwork roots: %w", err)
	}
	defer rows.Close()
	var roots []string
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("direct-library artwork has no owning library root")
	}
	return roots, nil
}
