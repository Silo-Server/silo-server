package catalog

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/ctxerr"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// audioPreferenceBatchReader is the optional user-store capability that reads
// a profile's stored audio preferences for several items in one query, each
// identical to what GetAudioPreference returns for it. The Postgres store
// implements it.
type audioPreferenceBatchReader interface {
	ListAudioPreferences(ctx context.Context, profileID string, seriesIDs []string) (map[string]*userstore.AudioPreference, error)
}

// prefetchAudioResolvers resolves, for a page of items, the lookups each
// item's audioPrefResolver would otherwise make one item at a time: the
// item's stored audio preference, the audio language for each library its
// files are in, and its original language. It returns one resolver per item
// with files, keyed by content ID, built from one batched preference read and
// one settings read for the whole page.
//
// Every value is the one the lazy resolver would compute for the same item,
// so versions built with these resolvers match the per-item path. A lookup
// that cannot be batched (a store without audioPreferenceBatchReader, or a
// failed batch) is left unresolved and the resolver falls back to its own
// lazy query, as it does for a library no file named here.
func (s *DetailService) prefetchAudioResolvers(
	ctx context.Context,
	items []*models.MediaItem,
	filesByID map[string][]*models.MediaFile,
	filter AccessFilter,
) map[string]*audioPrefResolver {
	type pageItem struct {
		item       *models.MediaItem
		libraryIDs []int
	}
	page := make([]pageItem, 0, len(items))
	for _, item := range items {
		if item.Type == playableTypeSeries || strings.TrimSpace(item.ContentID) == "" {
			continue
		}
		var libraryIDs []int
		seen := map[int]bool{}
		for _, file := range filesByID[item.ContentID] {
			if file != nil && !seen[file.MediaFolderID] {
				seen[file.MediaFolderID] = true
				libraryIDs = append(libraryIDs, file.MediaFolderID)
			}
		}
		if len(libraryIDs) > 0 {
			page = append(page, pageItem{item: item, libraryIDs: libraryIDs})
		}
	}
	if len(page) == 0 {
		return nil
	}

	resolvers := make(map[string]*audioPrefResolver, len(page))
	contentIDs := make([]string, 0, len(page))
	for _, entry := range page {
		r := s.newAudioPrefResolver(ctx, filter, entry.item.ContentID)
		if !r.valid {
			// No viewer store: the resolver answers without any lookup.
			return nil
		}
		if s.originalLangFn == nil {
			r.originalDone = true
			r.originalLang = strings.TrimSpace(entry.item.OriginalLanguage)
		}
		resolvers[entry.item.ContentID] = r
		contentIDs = append(contentIDs, entry.item.ContentID)
	}
	store := resolvers[contentIDs[0]].store
	prefillAudioPreferences(ctx, store, filter.ProfileID, contentIDs, resolvers)

	// One settings read for every (item, library) context, ranked per context
	// exactly as resolver.audioLanguage would rank it alone.
	type langKey struct {
		contentID string
		libraryID int
	}
	var keys []langKey
	var contexts []settingsresolve.Context
	for _, entry := range page {
		for _, libraryID := range entry.libraryIDs {
			keys = append(keys, langKey{entry.item.ContentID, libraryID})
			contexts = append(contexts, settingsresolve.Context{
				ProfileID:  filter.ProfileID,
				DeviceID:   filter.DeviceID,
				LibraryIDs: []int{libraryID},
				SeriesIDs:  []string{entry.item.ContentID},
			})
		}
	}
	resolved, err := s.settingsResolver().ResolveContexts(ctx, store, contexts, []string{settingskeys.PlaybackAudioLanguage}, nil)
	if err != nil {
		slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "batched audio language resolution failed; resolving per item", "component", "catalog", "error", err)
		return resolvers
	}
	for i, key := range keys {
		resolvers[key.contentID].resolvedLang[key.libraryID] = audioLanguageFromResolved(resolved[i])
	}
	return resolvers
}

// prefillAudioPreferences hands each resolver its stored audio preference
// from one batched read. Without a batch reader, or when the read fails, the
// resolvers keep looking theirs up lazily.
func prefillAudioPreferences(ctx context.Context, store userstore.UserStore, profileID string, contentIDs []string, resolvers map[string]*audioPrefResolver) {
	reader, ok := store.(audioPreferenceBatchReader)
	if !ok {
		return
	}
	prefs, err := reader.ListAudioPreferences(ctx, profileID, contentIDs)
	if err != nil {
		slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "batched audio preference read failed; resolving per item", "component", "catalog", "error", err)
		return
	}
	for id, r := range resolvers {
		r.audioDone = true
		if pref := prefs[id]; pref != nil {
			r.audioPref = &playback.AudioTrackPreference{
				AudioTrackIndex: pref.AudioTrackIndex,
				AudioLanguage:   pref.AudioLanguage,
				TrackSignature:  pref.TrackSignature,
			}
		}
	}
}
