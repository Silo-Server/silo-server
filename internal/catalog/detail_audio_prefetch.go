package catalog

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

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

// prefetchMoviePlaybackDefaults resolves, for one movie's detail, the
// lookups r and effectiveSubtitleDefaults would otherwise make one query at a
// time. One settings read covers the audio language for each library the
// files are in and the subtitle settings, each context ranked as it would be
// alone. One batch reads the stored audio and subtitle preference rows when
// the store keeps them beside the catalog; otherwise r reads its row lazily
// and the subtitle row is read here as effectiveSubtitleDefaults reads it.
// The original language comes from item, the row resolveOriginalLanguage
// would read again. It returns the subtitle defaults; ok is false when r has
// no store or the settings read fails, and the caller resolves them itself.
func (s *DetailService) prefetchMoviePlaybackDefaults(
	ctx context.Context,
	filter AccessFilter,
	item *models.MediaItem,
	files []*models.MediaFile,
	r *audioPrefResolver,
) (defaults subtitleDefaults, ok bool) {
	if r == nil || !r.valid {
		return defaults, false
	}
	var libraryIDs []int
	contexts := []settingsresolve.Context{}
	for _, file := range files {
		if file == nil || slices.Contains(libraryIDs, file.MediaFolderID) {
			continue
		}
		libraryIDs = append(libraryIDs, file.MediaFolderID)
		contexts = append(contexts, r.audioLanguageContext(file.MediaFolderID))
	}
	contexts = append(contexts, subtitleSettingsContext(filter, item.ContentID, files))
	keys := append([]string{settingskeys.PlaybackAudioLanguage}, subtitleSettingKeys...)
	resolved, err := s.settingsResolver().ResolveContexts(ctx, r.store, contexts, keys, nil)
	if err != nil {
		slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "batched playback settings resolution failed; resolving one at a time", "component", "catalog", "error", err)
		return defaults, false
	}
	for i, libraryID := range libraryIDs {
		r.resolvedLang[libraryID] = audioLanguageFromResolved(effectiveForKey(resolved[i], settingskeys.PlaybackAudioLanguage))
	}
	defaults.applySettings(resolved[len(libraryIDs)])

	if audio, subtitle, read := s.readPlaybackPreferences(ctx, r.store, filter.ProfileID, item.ContentID); read {
		r.audioDone = true
		if audio != nil {
			r.audioPref = &playback.AudioTrackPreference{
				AudioTrackIndex: audio.AudioTrackIndex,
				AudioLanguage:   audio.AudioLanguage,
				TrackSignature:  audio.TrackSignature,
			}
		}
		defaults.applyPreference(subtitle)
	} else if pref, err := r.store.GetSubtitlePreference(ctx, filter.ProfileID, item.ContentID); err == nil {
		defaults.applyPreference(pref)
	}

	if s.originalLangFn == nil && s.itemRepo != nil {
		r.originalDone = true
		r.originalLang = strings.TrimSpace(item.OriginalLanguage)
	}
	return defaults, true
}

// readPlaybackPreferences reads the stored audio and subtitle preference rows
// for one movie or series in one round trip on the catalog pool. read is false
// when the store keeps them elsewhere or the batch fails.
func (s *DetailService) readPlaybackPreferences(ctx context.Context, store userstore.UserStore, profileID, contentID string) (*userstore.AudioPreference, *userstore.SubtitlePreference, bool) {
	if s.itemRepo == nil || s.itemRepo.pool == nil {
		return nil, nil, false
	}
	var batch pgx.Batch
	var state userstore.ItemViewerState
	if !userstore.QueueItemViewerState(store, &batch, userstore.ItemViewerStateQuery{
		ProfileID:           profileID,
		MediaItemID:         contentID,
		PlaybackPreferences: true,
	}, &state) {
		return nil, nil, false
	}
	if err := s.itemRepo.pool.SendBatch(ctx, &batch).Close(); err != nil {
		return nil, nil, false
	}
	return state.AudioPreference, state.SubtitlePreference, true
}

// effectiveForKey is the part of one context's resolution that answers key,
// in the shape a single-key resolution of it has.
func effectiveForKey(resolved []settingsresolve.Effective, key string) []settingsresolve.Effective {
	for i := range resolved {
		if resolved[i].Key == key {
			return resolved[i : i+1]
		}
	}
	return nil
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
