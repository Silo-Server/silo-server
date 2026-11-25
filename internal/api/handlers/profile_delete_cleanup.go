package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileDevices reads the devices a profile registered, before a delete
// takes their registrations, so the playback connections only they used can
// be forgotten after it. The registry is nil when the store keeps none.
func (h *ProfileHandler) profileDevices(ctx context.Context, store userstore.UserStore, userID int, profileID string) (userstore.DeviceRegistry, []string) {
	registry, _ := store.(userstore.DeviceRegistry)
	var devices []string
	if registry != nil && h.Connections != nil {
		entries, err := registry.ListDevices(ctx)
		if err != nil {
			slog.WarnContext(ctx, "listing profile devices before delete failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		}
		for _, entry := range entries {
			if entry.ProfileID == profileID {
				devices = append(devices, entry.DeviceID)
			}
		}
	}
	return registry, devices
}

// afterProfileDeleted runs the clean-ups a committed profile delete needs
// outside the store's transaction: state other services keep for the
// profile, its uploaded avatar, its deleted collections' artwork and its
// devices' playback connections. Each is best-effort and logged, and none
// depends on the client staying connected. Every path that deletes a
// profile runs it once per deleted profile.
func (h *ProfileHandler) afterProfileDeleted(
	ctx context.Context,
	userID int,
	deleted userstore.Profile,
	devices []string,
	collections []string,
	registry userstore.DeviceRegistry,
) {
	profileID := deleted.ID
	purge := func(what string, run func(context.Context) error) {
		purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := run(purgeCtx); err != nil {
			slog.WarnContext(ctx, "profile "+what+" failed after delete", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		}
	}
	if h.JellyfinSessionPurger != nil {
		purge("Jellyfin session purge", func(c context.Context) error { return h.JellyfinSessionPurger.PurgeProfile(c, userID, profileID) })
	}
	if h.WatchlistRequestWithdrawer != nil {
		purge("watchlist request withdrawal", func(c context.Context) error {
			return h.WatchlistRequestWithdrawer.WithdrawProfileWatchlistRequests(c, userID, profileID)
		})
	}
	if isUploadedAvatarRef(deleted.Avatar) {
		if cleanupErr := deleteUploadedAvatarObjects(ctx, h.AvatarStore, userID, profileID); cleanupErr != nil {
			slog.WarnContext(ctx, "profile avatar cleanup failed after delete", "component", "api", "user_id", userID, "profile_id", profileID, "error", cleanupErr)
		}
	}
	removeDeletedCollectionArtwork(ctx, h.ArtworkStore, userCollectionImagePrefix, collections...)
	if h.DeviceLibraryPurger != nil {
		purge("device-library purge", func(c context.Context) error { return h.DeviceLibraryPurger.PurgeProfileDevices(c, userID, profileID) })
	}
	forgetPlaybackConnections(ctx, h.Connections, registry, userID, devices)
	if h.DroppedSeriesPurger != nil {
		purge("dropped-series purge", func(c context.Context) error { return h.DroppedSeriesPurger.PurgeProfile(c, userID, profileID) })
	}
	if h.WatchlistTitlesPurger != nil {
		purge("watchlist-title purge", func(c context.Context) error { return h.WatchlistTitlesPurger.PurgeProfile(c, userID, profileID) })
	}
	if h.EbookReaderPurger != nil {
		purge("ebook reader purge", func(c context.Context) error { return h.EbookReaderPurger.PurgeProfile(c, userID, profileID) })
	}
	if h.RecommendationsPurger != nil {
		purge("recommendation purge", func(c context.Context) error { return h.RecommendationsPurger.PurgeProfile(c, userID, profileID) })
	}
}
