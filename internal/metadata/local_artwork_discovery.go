package metadata

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// SetLocalArtworkCopier wires the copier that copies local sidecar artwork a
// refresh discovers into the artwork store from this process. Without it,
// local artwork is only queued, for whichever server drains the queue first.
// Call it during setup, before refreshes run.
func (s *MetadataService) SetLocalArtworkCopier(copier LocalArtworkCopier) {
	s.localArtwork = copier
}

// copyLocalArtwork hands the local sidecar inputs this process can see to the
// copier and returns the inputs still to queue the usual way: the remote ones
// and the unseen local ones, or all of them when no copier is wired or it
// failed. A refresh finds only files it can read, so a discovered sidecar is
// seen; a local source kept by a refresh on a server without the mount is
// not, and the usual queueing leaves its hold or deferral as it is.
func (s *MetadataService) copyLocalArtwork(ctx context.Context, targetKind, targetID string, inputs []EnqueueImageCacheJobInput) []EnqueueImageCacheJobInput {
	if s.localArtwork == nil {
		return inputs
	}
	local, rest := splitLocalImageCacheInputs(inputs)
	var seen []EnqueueImageCacheJobInput
	for _, in := range local {
		if localSidecarVisible(in.SourcePath) {
			seen = append(seen, in)
		} else {
			rest = append(rest, in)
		}
	}
	if len(seen) == 0 {
		return inputs
	}
	if err := s.localArtwork.CopyLocalArtwork(ctx, seen); err != nil {
		slog.WarnContext(ctx, "metadata: failed to start copying local artwork, leaving it to the queue", "component", "metadata",
			"target_kind", targetKind, "target_id", targetID, "count", len(seen), "error", err)
		return inputs
	}
	return rest
}

// localSidecarVisible reports whether the file a local source names exists
// on this server.
func localSidecarVisible(source string) bool {
	path, ok := localImageSourceFilePath(source)
	if !ok {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil
}

// applyLocalArtworkFallback fills each item slot waiting on its local copy,
// with nothing cached to show, from the best remote candidate. A slot with a
// cached copy keeps serving it (preserveCachedArtwork); an empty one would
// leave a new item without a poster until the copy lands. The copy replaces
// the stand-in through the source-matched update, because the slot's source
// stays local. Locked artwork gets no stand-in the admin did not choose.
func applyLocalArtworkFallback(item *models.MediaItem, images []RemoteImage, preferredLang string) {
	if artworkLocked(item) {
		return
	}
	var remote []itemArtworkField
	for i, field := range itemArtworkFields(item) {
		if strings.TrimSpace(*field.path) != "" || !isLocalImageSourcePath(*field.source) {
			continue
		}
		if remote == nil {
			best := &models.MediaItem{}
			applyBestImages(best, withoutLocalImages(images), MergeReplaceUnlocked, preferredLang)
			remote = itemArtworkFields(best)
		}
		*field.path = localArtworkFallbackPath(*field.path, *field.source, *remote[i].path)
	}
}

// isLocalArtworkStandIn reports whether a stored slot serves a remote stand-in
// while the copy of its local source is pending.
func isLocalArtworkStandIn(path, source string) bool {
	return isRemoteImageSourcePath(path) && isLocalImageSourcePath(source)
}

// localArtworkFallbackPath returns fallback in place of an empty path whose
// source is local, and path otherwise.
func localArtworkFallbackPath(path, source, fallback string) string {
	if strings.TrimSpace(path) == "" && isLocalImageSourcePath(source) && isRemoteImageSourcePath(fallback) {
		return fallback
	}
	return path
}

// recordEpisodeStillFallback remembers the first remote still that a local
// still displaces or outranks while episode results merge.
func recordEpisodeStillFallback(target *EpisodeResult, incoming EpisodeResult) {
	target.stillFallback = firstNonEmpty(target.stillFallback, displacedRemoteImage(target.StillPath, incoming.StillPath))
}

// displacedRemoteImage returns the remote one of an accumulated and an
// incoming image when the other is local.
func displacedRemoteImage(accumulated, incoming string) string {
	switch {
	case isLocalImageSourcePath(accumulated) && isRemoteImageSourcePath(incoming):
		return incoming
	case isLocalImageSourcePath(incoming) && isRemoteImageSourcePath(accumulated):
		return accumulated
	}
	return ""
}
