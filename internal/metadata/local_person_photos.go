package metadata

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// localPersonPhotoRepo is the part of the person repository that local
// person photos use.
type localPersonPhotoRepo interface {
	PhotoStates(ctx context.Context, ids []int64) (map[int64]catalog.PersonPhotoState, error)
	ClaimPhotoSources(ctx context.Context, claims []catalog.PersonPhotoSourceClaim) ([]int64, error)
}

// applyLocalPersonPhotos gives the credited people the photos the item's
// local metadata names (an NFO actor <thumb>, carried in LocalPhotoSource)
// and queues them for the image cache. people must carry their person ids.
//
// A person is shared by every title that credits them, so a title's NFO only
// fills a gap: the person takes the photo when they have none (nothing, or
// the "-" no-photo sentinel), or when their photo came from the same NFO,
// which is the same source, or a local file in the same folder (an .actors
// image replaced or renamed). A photo from a provider, cached or still a URL
// waiting for its copy, or from another title's NFO stays.
// Like all local artwork, the photos need the image cache: with it off they
// are not used. The sources are local (file://, or an NFO-named URL as a
// LocalMetadataURLSource), so photo_path never serves them before the copy.
func (s *MetadataService) applyLocalPersonPhotos(ctx context.Context, contentID string, people []models.ItemPerson) {
	if s == nil || s.personRepo == nil || !s.localArtworkUsable() {
		return
	}
	inputs := localPersonPhotoJobs(ctx, s.personRepo, contentID, people)
	s.enqueueImageCacheJobs(ctx, "person", contentID, inputs)
}

// localPersonPhotoJobs records the photo sources people take and returns the
// image cache jobs that copy them.
func localPersonPhotoJobs(ctx context.Context, repo localPersonPhotoRepo, contentID string, people []models.ItemPerson) []EnqueueImageCacheJobInput {
	wanted := make(map[int64]string)
	var ids []int64
	for _, p := range people {
		if p.ID == 0 || !isCacheableImageSourcePath(p.LocalPhotoSource) {
			continue
		}
		if _, dup := wanted[p.ID]; dup {
			continue // the first credit for a person names their photo
		}
		wanted[p.ID] = p.LocalPhotoSource
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	stored, err := repo.PhotoStates(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "metadata: failed to read person photos", "component", "metadata", "content_id", contentID, "error", err)
		return nil
	}
	claims, queue := localPersonPhotoClaims(ids, wanted, stored)
	if len(claims) > 0 {
		claimed, err := repo.ClaimPhotoSources(ctx, claims)
		if err != nil {
			slog.WarnContext(ctx, "metadata: failed to set person photo sources", "component", "metadata", "content_id", contentID, "error", err)
		}
		queue = append(queue, claimed...)
	}
	slices.Sort(queue)
	inputs := make([]EnqueueImageCacheJobInput, 0, len(queue))
	for _, id := range slices.Compact(queue) {
		source := wanted[id]
		inputs = append(inputs, EnqueueImageCacheJobInput{
			TargetType:      ImageCacheTargetPerson,
			TargetContentID: strconv.FormatInt(id, 10),
			// The title's library roots confine a local photo's read.
			SeriesID:          contentID,
			SourcePath:        source,
			ProviderID:        imageCacheProviderIDFromSource(source, ""),
			ProviderContentID: strconv.FormatInt(id, 10),
			ContentType:       personImageContentType,
			ImageType:         ImageCacheImageProfile,
			// A photo still waiting for its copy may have an older job for the
			// same source marked done (one skipped while the source briefly
			// moved), so it runs again.
			RequeueSucceeded: !isCachedImagePath(stored[id].Path),
		})
	}
	return inputs
}

// localPersonPhotoClaims decides, for each person id in order, whether the
// person takes the wanted photo source: a claim to record it, or, when the
// person already holds that source, a queue entry. A person holding the
// source is queued even with a cached photo: that photo may be the file the
// source replaced, if a claim committed and its job was never enqueued. The
// job row keyed by source makes a repeat enqueue of a finished copy a no-op,
// as for item artwork.
func localPersonPhotoClaims(ids []int64, wanted map[int64]string, stored map[int64]catalog.PersonPhotoState) ([]catalog.PersonPhotoSourceClaim, []int64) {
	var claims []catalog.PersonPhotoSourceClaim
	var queue []int64
	for _, id := range ids {
		state, ok := stored[id]
		if !ok {
			continue
		}
		source := wanted[id]
		switch {
		case state.Source == source:
			queue = append(queue, id)
		case hasNoPhoto(state) || sameLocalPhotoFolder(state.Source, source):
			claims = append(claims, catalog.PersonPhotoSourceClaim{ID: id, Expected: state, Source: source})
		}
	}
	return claims, queue
}

// hasNoPhoto reports whether a person has no photo, served or on its way.
func hasNoPhoto(state catalog.PersonPhotoState) bool {
	return isNoPhotoValue(state.Path) && isNoPhotoValue(state.Source)
}

// isNoPhotoValue reports whether a photo column is empty or holds the "-"
// no-photo sentinel.
func isNoPhotoValue(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "-"
}

// sameLocalPhotoFolder reports whether two photo sources are local files in
// one folder.
func sameLocalPhotoFolder(a, b string) bool {
	pathA, okA := localImageSourceFilePath(a)
	pathB, okB := localImageSourceFilePath(b)
	return okA && okB && filepath.Dir(pathA) == filepath.Dir(pathB)
}

// personImageContentType is the image cache's content type for people.
const personImageContentType = "people"
