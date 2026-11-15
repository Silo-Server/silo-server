package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// errEffectiveFileNotInVersionList marks a start whose resolved effective row
// is not one of the item's selectable version rows. It is not a verdict about
// the requested release: the provider may be perfectly healthy and the row it
// resolved to is simply not a row the client can see, so it must never indict
// the requested row (see virtualCandidateConfirmedDead).
var errEffectiveFileNotInVersionList = errors.New("the resolved effective media file is not one of the item's version rows")

// versionListContentIDV3 names the catalog entity whose version list a playback
// row belongs to. An episode file's versions are the episode's, not the
// series'; the detail service resolves either id to the same target.
func versionListContentIDV3(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	if file.EpisodeID != "" {
		return file.EpisodeID
	}
	return file.ContentID
}

// selectableVersionFileIDsV3 returns the item's selectable version row ids, or
// nil when the answer is unknown. Unknown is deliberately not an empty set: an
// unwired seam, a lookup error, and an item with no version rows all answer nil
// so the guard fails open instead of refusing playback it cannot judge. A guard
// that turned its own ignorance into a terminal would block playable releases
// whenever the catalog read hiccuped.
func (h *PlaybackHandler) selectableVersionFileIDsV3(ctx context.Context, file *models.MediaFile, filter catalog.AccessFilter) map[int]struct{} {
	if h == nil || h.VersionList == nil || file == nil {
		return nil
	}
	contentID := versionListContentIDV3(file)
	if contentID == "" {
		return nil
	}
	ids, err := h.VersionList.SelectableVersionFileIDs(ctx, contentID, filter)
	if err != nil {
		slog.WarnContext(ctx, "playback version list unavailable; the effective-row check is skipped",
			"component", "api", "file_id", file.ID, "content_id", contentID, "error", err)
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// confineEffectiveVersionV3 reports whether a plan may name effectiveFile as
// its effective row. A plan's effective row is what the client records as
// "playing" while its requested row is what the viewer picked; when the two
// name rows from different lists the viewer can never reconcile them, and the
// item page dead-ends on a row it cannot show. So the effective row must be one
// of the item's selectable version rows.
//
// Three cases are deliberately outside the rule:
//
//   - The effective row is the requested row. That row is the client's own pick
//     from the list it holds; re-judging it here would refuse a still-valid
//     request whenever the item's list was recomputed between the read and the
//     play.
//   - The requested row is not a viewer selection. A provider-neutral listing
//     row owns no bytes and declares no release, so binding it to the row that
//     owns the served candidate is ordinary auto selection, not a substituted
//     version. Refusing it would break neutral listing rows, whose owner row is
//     legitimately absent from the collapsed list.
//   - The answer is unknown (see selectableVersionFileIDsV3).
//
// Everything else is refused, and the caller tries the next candidate.
func (h *PlaybackHandler) confineEffectiveVersionV3(ctx context.Context, requestedFile, effectiveFile *models.MediaFile, filter catalog.AccessFilter) error {
	if requestedFile == nil || effectiveFile == nil || effectiveFile.ID <= 0 {
		return nil
	}
	if effectiveFile.ID == requestedFile.ID {
		return nil
	}
	if !isVirtualPlaybackFile(requestedFile) || !virtualRequestedRowIsSelection(requestedFile) {
		return nil
	}
	ids := h.selectableVersionFileIDsV3(ctx, requestedFile, filter)
	if ids == nil {
		return nil
	}
	if _, listed := ids[effectiveFile.ID]; listed {
		return nil
	}
	return fmt.Errorf("%w: effective row %d is not among the %d selectable rows of %q",
		errEffectiveFileNotInVersionList, effectiveFile.ID, len(ids), versionListContentIDV3(requestedFile))
}

// confineStartEffectiveVersionV3 enforces confineEffectiveVersionV3 on a fresh
// start: when the resolver landed on an unlisted row, that candidate is refused
// like any other unusable one and the walk tries the next, excluding the
// candidate that produced it. When nothing qualifies, the start answers the
// version-list hint instead of planning a row the client cannot reconcile.
//
// The retry is bounded to one walk. The refusal is not a verdict about the
// requested release, so neither returned error carries the walk's own cause:
// the candidate loop joins its refusals with "no usable stream", and that text
// must not reach the dead-pin classifier and condemn a row whose only fault was
// resolving to a row the version list does not carry.
func (h *PlaybackHandler) confineStartEffectiveVersionV3(
	r *http.Request,
	walkReq *http.Request,
	requestedFile *models.MediaFile,
	resolved resolvedVirtualPlaybackSource,
	req playback.StartRequestV3,
	profileID string,
	allowFailedCandidate bool,
	bandwidthCapKbps int,
) (resolvedVirtualPlaybackSource, error) {
	if resolved.File == nil || resolved.File.ID == requestedFile.ID {
		return resolved, nil
	}
	filter := requestAccessFilter(r)
	refusal := h.confineEffectiveVersionV3(r.Context(), requestedFile, resolved.File, filter)
	if refusal == nil {
		return resolved, nil
	}
	slog.WarnContext(r.Context(), "virtual start refused an effective row outside the item's version list",
		"component", "api", "signal", "effective_row_not_listed",
		"requested_file_id", requestedFile.ID,
		"effective_file_id", resolved.File.ID,
		"candidate_uri", resolved.URI,
		"content_id", versionListContentIDV3(requestedFile),
		"reason", refusal)
	excluded := make([]string, 0, 2)
	for _, uri := range []string{resolved.URI, resolved.File.FilePath} {
		if id := virtualResultCandidateID(uri); id != "" && !slices.Contains(excluded, id) {
			excluded = append(excluded, id)
		}
	}
	retried, retryErr := h.resolveVirtualStartWithVersionFallbackExcluding(
		walkReq, requestedFile, profileID, req, allowFailedCandidate, bandwidthCapKbps, excluded,
	)
	// A retry that lands back on the requested row is the best answer: the plan
	// names the row the viewer picked, which the item lists by construction.
	if retryErr == nil && retried.File != nil &&
		h.confineEffectiveVersionV3(r.Context(), requestedFile, retried.File, filter) == nil {
		return retried, nil
	}
	if retryErr != nil {
		// Log the walk's own cause instead of wrapping it: the candidate loop
		// joins its refusals with "no usable stream", and that text must not
		// reach the dead-pin classifier as a verdict about the requested row.
		slog.WarnContext(r.Context(), "virtual start found no listed candidate on retry",
			"component", "api", "signal", "effective_row_not_listed_retry",
			"requested_file_id", requestedFile.ID, "error", retryErr)
		return resolvedVirtualPlaybackSource{}, fmt.Errorf("%w: the retry found no listed candidate", errEffectiveFileNotInVersionList)
	}
	return resolvedVirtualPlaybackSource{}, fmt.Errorf("%w: refused effective row %d", errEffectiveFileNotInVersionList, resolved.File.ID)
}
