package metadata

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// SetLocalArtworkCopier wires the copier that copies local sidecar artwork a
// refresh discovers into the artwork store from this process. Without it,
// local artwork is only queued, for whichever server drains the queue first.
func (s *MetadataService) SetLocalArtworkCopier(copier LocalArtworkCopier) {
	s.localArtwork = copier
}

// copyLocalArtwork hands the local sidecar inputs to the copier and returns
// the inputs still to queue the usual way: the remote ones, or all of them
// when no copier is wired or it failed.
func (s *MetadataService) copyLocalArtwork(ctx context.Context, targetKind, targetID string, inputs []EnqueueImageCacheJobInput) []EnqueueImageCacheJobInput {
	if s.localArtwork == nil {
		return inputs
	}
	local, remote := splitLocalImageCacheInputs(inputs)
	if len(local) == 0 {
		return inputs
	}
	if err := s.localArtwork.CopyLocalArtwork(ctx, local); err != nil {
		slog.WarnContext(ctx, "metadata: failed to start copying local artwork, leaving it to the queue", "component", "metadata",
			"target_kind", targetKind, "target_id", targetID, "count", len(local), "error", err)
		return inputs
	}
	return remote
}

// A slot whose local copy is not cached yet keeps serving the copy it already
// has (preserveCachedArtwork). With nothing cached, it serves the best remote
// image the refresh found until the local copy lands, so a new item does not
// show an empty slot meanwhile. The copy replaces it through the
// source-matched update, because the slot's source stays the local file.

// applyLocalArtworkFallback fills each item slot waiting on its local copy,
// with nothing cached to show, from the best remote candidate. Locked artwork
// gets no stand-in the admin did not choose.
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
