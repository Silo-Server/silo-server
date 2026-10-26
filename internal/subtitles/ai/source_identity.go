package ai

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// asRequested returns job as seen from a request for sourceIndex. A job
// matched by its source identity may have been created when that track had
// another combined index; the response names the index the caller used, so
// clients can match it to their request.
func asRequested(job *Job, sourceIndex int) *Job {
	if job.SourceIndex == sourceIndex {
		return job
	}
	out := *job
	out.SourceIndex = sourceIndex
	return &out
}

// findTranslationByIndexOrReuse covers a translate request whose source has
// a stable identity. A job queued under the index key, by a server that
// predates identity keys, is still in flight for this index; its worker loads
// the source by that index, so it is producing this track. A translation that
// already finished, and whose track is still stored, is returned as-is:
// asking again would bill the provider for a duplicate track. Deleting the
// track allows a fresh translation. Only a source named by a stable identity
// qualifies, so a shifted index never returns another track's translation.
func (s *Service) findTranslationByIndexOrReuse(ctx context.Context, req JobRequest, key, jobModel string) (*Job, error) {
	indexKey := idempotencyKey(req.MediaFileID, req.Kind, strconv.Itoa(req.SourceIndex), req.TargetLanguage, jobModel)
	if existing, err := s.repo.GetActiveJobByIdempotencyKey(ctx, indexKey); err != nil || existing != nil {
		return existing, err
	}
	done, err := s.repo.GetReusableJobByIdempotencyKey(ctx, key)
	if err != nil || done == nil {
		return nil, err
	}
	return asRequested(done, req.SourceIndex), nil
}

// translationSource names a translation source track by its content rather
// than its combined index, which shifts when a rescan adds or removes
// sidecars or a deleted download moves later ones: a hash of the sidecar's or
// stored subtitle's bytes as translated (timing correction applied), or the
// container stream together with the media file's fingerprint. Editing a sidecar or replacing the media file
// therefore names a new source. stable is false, and the index is used, when
// the track cannot be resolved.
func (s *Service) translationSource(ctx context.Context, mediaFileID, idx int) (source string, stable bool) {
	source = strconv.Itoa(idx)
	if s.files == nil || idx < 0 {
		return source, false
	}
	file, err := s.files.GetByID(ctx, mediaFileID)
	if err != nil || file == nil {
		return source, false
	}
	externalCount := len(file.ExternalSubtitles)
	switch {
	case idx < externalCount:
		// The bytes loadSource translates: the sidecar with any saved timing
		// correction applied.
		data, err := playback.LoadExternalSubtitle(ctx, s.externalTimings, file.ID, file.ExternalSubtitles[idx])
		if err != nil {
			return source, false
		}
		return "external:" + subtitles.ContentSHA256(data), true
	case idx < externalCount+len(file.SubtitleTracks):
		return fmt.Sprintf("embedded:%s:%d", mediaFileFingerprint(file), file.SubtitleTracks[idx-externalCount].Index), true
	case s.lister != nil && s.store != nil:
		list, err := s.lister.ListDownloadedSubtitles(ctx, file.ID)
		downloaded := idx - externalCount - len(file.SubtitleTracks)
		if err != nil || downloaded >= len(list) {
			return source, false
		}
		// The stored bytes with their timing correction, as loadSource
		// translates them.
		stored, data, err := s.store.GetSubtitleContent(ctx, list[downloaded].ID)
		if err != nil {
			return source, false
		}
		if data, err = subtitles.DeliveryBytes(stored, data); err != nil {
			return source, false
		}
		return fmt.Sprintf("downloaded:%d:%s", list[downloaded].ID, subtitles.ContentSHA256(data)), true
	}
	return source, false
}

// mediaFileFingerprint identifies the content of a media file. FileHash is
// an OpenSubtitles hash, which reads only the file's ends, so size and
// modification time are included to catch a same-size remux.
func mediaFileFingerprint(file *models.MediaFile) string {
	var modified int64
	if file.FileModifiedAt != nil {
		modified = file.FileModifiedAt.UnixNano()
	}
	return fmt.Sprintf("%s-%d-%d", file.FileHash, file.FileSize, modified)
}

// sourceChanged reports whether a queued translation's source index now
// names a different track than the one its key was derived from, for
// example after a rescan added a sidecar while the job waited. Translating
// the track now at that index would publish it under another track's key.
func (s *Service) sourceChanged(ctx context.Context, job *Job) bool {
	if job.Kind != JobKindTranslate || job.IdempotencyKey == "" {
		return false
	}
	source, stable := s.translationSource(ctx, job.MediaFileID, job.SourceIndex)
	if !stable {
		// loadSource reports a track that cannot be read.
		return false
	}
	return job.IdempotencyKey != idempotencyKey(job.MediaFileID, job.Kind, source, job.TargetLanguage, job.Model) &&
		job.IdempotencyKey != idempotencyKey(job.MediaFileID, job.Kind, strconv.Itoa(job.SourceIndex), job.TargetLanguage, job.Model)
}
