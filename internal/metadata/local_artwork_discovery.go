package metadata

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
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
	visible := visibleLocalSidecars(ctx, local, rootcheck.ProbeManyWithTimeout)
	for i, in := range local {
		if visible[i] {
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

// folderProber probes folders with a timeout; rootcheck.ProbeManyWithTimeout
// in production.
type folderProber func(ctx context.Context, paths []string, timeout time.Duration) []rootcheck.Result

// visibleLocalSidecars reports, for each local input, whether the file it
// names exists on this server. Each folder is probed first, once, through
// probe with rootcheck's coalescing and one deadline for the whole batch, so
// shares that do not answer cost the refresh at most that timeout and count
// as unseen; the file is looked up only in a folder that answered, including
// one that denies listing (the NFO finder reads known names from such a
// folder).
func visibleLocalSidecars(ctx context.Context, local []EnqueueImageCacheJobInput, probe folderProber) []bool {
	paths := make([]string, len(local))
	folderOf := make([]int, len(local)) // index into folders, -1 when not local
	folderIndex := make(map[string]int)
	var folders []string
	for i, in := range local {
		folderOf[i] = -1
		path, ok := localImageSourceFilePath(in.SourcePath)
		if !ok {
			continue
		}
		dir := filepath.Dir(path)
		idx, ok := folderIndex[dir]
		if !ok {
			idx = len(folders)
			folderIndex[dir] = idx
			folders = append(folders, dir)
		}
		paths[i], folderOf[i] = path, idx
	}
	visible := make([]bool, len(local))
	if len(folders) == 0 {
		return visible
	}
	probeCtx, cancel := context.WithTimeout(ctx, rootcheck.DefaultProbeTimeout)
	defer cancel()
	probes := probe(probeCtx, folders, rootcheck.DefaultProbeTimeout)
	for i, path := range paths {
		if folderOf[i] < 0 {
			continue
		}
		if folder := probes[folderOf[i]]; !folder.Reachable && folder.ErrorCode != rootcheck.ErrCodePermissionDenied {
			continue
		}
		_, err := os.Lstat(path)
		visible[i] = err == nil
	}
	return visible
}

// applyLocalArtworkFallback fills each item slot waiting on the copy of its
// sidecar file, with nothing cached to show, from the best remote candidate.
// An image URL an NFO names gets no stand-in: its slot stays empty until the
// image cache has downloaded it. A slot with a
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
		if strings.TrimSpace(*field.path) != "" || !isLocalSidecarSource(*field.source) {
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
// while the copy of its sidecar file is pending.
func isLocalArtworkStandIn(path, source string) bool {
	return isRemoteImageSourcePath(path) && isLocalSidecarSource(source)
}

// localArtworkFallbackPath returns fallback in place of an empty path whose
// source is a sidecar file, and path otherwise.
func localArtworkFallbackPath(path, source, fallback string) string {
	if strings.TrimSpace(path) == "" && isLocalSidecarSource(source) && isRemoteImageSourcePath(fallback) {
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
