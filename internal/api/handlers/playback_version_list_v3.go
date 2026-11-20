package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

// errEffectiveFileNotInVersionList marks a start whose resolved effective row
// is not one of the item's selectable version rows. It is not a verdict about
// the requested release: the provider may be perfectly healthy and the row it
// resolved to is simply not a row the client can see, so it must never indict
// the requested row (see virtualCandidateConfirmedDead).
var errEffectiveFileNotInVersionList = errors.New("the resolved effective media file is not one of the item's version rows")

// errSelectedReleaseNotServableV3 marks an explicit pick whose own release could
// not be resolved: the provider answered with a different release's candidate, or
// with a catalog row the item's version list does not carry, and re-binding to the
// row the viewer selected could not be proven. It is deliberately not a verdict
// about any file: the selected release was never disproven, so the start answers a
// retryable failure that names the selected version instead of planning (or
// terminalising on) a sibling the viewer never picked.
var errSelectedReleaseNotServableV3 = errors.New("the selected version could not be resolved from its own release")

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
// Everything else is refused, and the caller re-binds to the requested row (see
// confineStartEffectiveVersionV3).
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

// resolvedSourceIsRequestedReleaseV3 reports whether the resolve provably served
// the requested row's own release, even when it bound a different catalog row to
// the candidate. The requested row's own candidate URI, a provider-renumbered
// rematch, or matching durable provider identity all prove it; anything else is
// a different release and must not be planned under the viewer's pick.
func resolvedSourceIsRequestedReleaseV3(requested *models.MediaFile, resolved resolvedVirtualPlaybackSource) bool {
	if requested == nil {
		return false
	}
	if resolved.IdentityRematched {
		return true
	}
	if sameVirtualReleaseIdentity(requested.FilePath, resolved.URI) {
		return true
	}
	requestedTiers := resolver.NewPersistedIdentityTiers(
		requested.ProviderVideoHash, requested.ProviderGUID, requested.ProviderReleaseName, requested.ProviderReleaseSize,
	)
	resolvedTiers := resolver.NewPersistedIdentityTiers(
		resolved.ProviderVideoHash, resolved.ProviderGUID, resolved.ProviderReleaseName, resolved.ProviderReleaseSize,
	)
	_, shared := resolver.PersistedIdentitiesMatch(requestedTiers, resolvedTiers)
	return shared
}

// rebindResolvedSourceToRequestedRowV3 serves the resolved candidate under the
// requested catalog row's identity. The candidate and its evidence are the same
// release the viewer picked; only the catalog row that owns the candidate URI was
// a duplicate the item's version list does not carry. Keeping the requested id
// means the plan, the attempt record, and the client's progress all name the row
// the viewer can see.
//
// The deferred probe is re-bound with it: it was armed against the row that owned
// the candidate URI, and evidence for the selected release belongs on the selected
// row (the probe's own identity fence still decides whether the write is admitted).
func rebindResolvedSourceToRequestedRowV3(requested *models.MediaFile, resolved resolvedVirtualPlaybackSource) resolvedVirtualPlaybackSource {
	if requested == nil || resolved.File == nil || resolved.File.ID == requested.ID {
		return resolved
	}
	rebound := *resolved.File
	rebound.ID = requested.ID
	rebound.ContentID = requested.ContentID
	rebound.EpisodeID = requested.EpisodeID
	resolved.File = &rebound
	if probe := resolved.DeferredProbe; probe != nil {
		if probe.file != nil {
			file := *probe.file
			file.ID = requested.ID
			file.ContentID = requested.ContentID
			file.EpisodeID = requested.EpisodeID
			probe.file = &file
		}
		if probe.probeTransient != nil {
			transient := *probe.probeTransient
			transient.ID = requested.ID
			transient.ContentID = requested.ContentID
			transient.EpisodeID = requested.EpisodeID
			probe.probeTransient = &transient
		}
	}
	return resolved
}

// explicitPickRequestedV3 reports whether the request is an explicit version
// pick: either the client declared file_selection=explicit or it named an offered
// candidate. Both mean "this release, not whichever one resolves best".
func explicitPickRequestedV3(req playback.StartRequestV3) bool {
	return req.FileSelection == playback.FileSelectionExplicitV3 || strings.TrimSpace(req.OfferedVirtualURI) != ""
}

// confineStartEffectiveVersionV3 enforces the effective-row rule on a fresh start.
//
// For an explicit pick it never walks to a sibling release: the viewer chose one
// release and must not be silently moved off it, and a plan that names a sibling
// can only be reconciled by an "unknown substitution" notice or, worse, terminalise
// on that sibling's own defect (the measured production case ended in
// hdr_transcode_unsupported for a file the viewer never picked). The outcomes are
// exactly three:
//
//   - The requested row plays: the resolve already named it, or its own release was
//     served under a duplicate row and is re-bound to the requested row.
//   - The requested row is resolved once more, from its own identity, when the first
//     resolve walked off the release.
//   - The requested row cannot be served: a retryable failure that names the selected
//     version. Nothing is planned on another row, and the requested release is never
//     indicted by the sibling's verdict.
//
// An auto selection keeps the old behavior: the unlisted candidate is excluded and
// the walk tries the next one.
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
	explicit := explicitPickRequestedV3(req)
	refusal := h.confineEffectiveVersionV3(r.Context(), requestedFile, resolved.File, filter)
	if refusal == nil {
		if !explicit || resolvedSourceIsRequestedReleaseV3(requestedFile, resolved) {
			return resolved, nil
		}
		// The effective row is listed, but it describes a different release than
		// the one the viewer picked: planning it would silently substitute, and its
		// own route verdict would be published as this version's.
		refusal = fmt.Errorf("%w: effective row %d is a different release than the selected row %d",
			errSelectedReleaseNotServableV3, resolved.File.ID, requestedFile.ID)
		slog.WarnContext(r.Context(), "virtual start refused an effective row of a different release",
			"component", "api", "signal", "effective_row_different_release",
			"requested_file_id", requestedFile.ID,
			"effective_file_id", resolved.File.ID,
			"candidate_uri", resolved.URI,
			"content_id", versionListContentIDV3(requestedFile),
			"reason", refusal)
	} else {
		slog.WarnContext(r.Context(), "virtual start refused an effective row outside the item's version list",
			"component", "api", "signal", "effective_row_not_listed",
			"requested_file_id", requestedFile.ID,
			"effective_file_id", resolved.File.ID,
			"candidate_uri", resolved.URI,
			"content_id", versionListContentIDV3(requestedFile),
			"reason", refusal)
	}
	if explicit {
		return h.rebindExplicitStartToRequestedRowV3(
			r, requestedFile, resolved, req, profileID, allowFailedCandidate, bandwidthCapKbps,
		)
	}
	return h.retryUnlistedEffectiveRowV3(
		r, walkReq, requestedFile, resolved, req, profileID, allowFailedCandidate, bandwidthCapKbps,
	)
}

// rebindExplicitStartToRequestedRowV3 resolves the requested row's own release for
// an explicit pick whose first resolve landed elsewhere. It accepts only the
// requested row (or its own release under a duplicate row); anything else answers
// the selected version's own retryable failure.
func (h *PlaybackHandler) rebindExplicitStartToRequestedRowV3(
	r *http.Request,
	requestedFile *models.MediaFile,
	resolved resolvedVirtualPlaybackSource,
	req playback.StartRequestV3,
	profileID string,
	allowFailedCandidate bool,
	bandwidthCapKbps int,
) (resolvedVirtualPlaybackSource, error) {
	// The first resolve already proved the candidate it served is the requested
	// row's own release; only the row identity was a duplicate.
	if resolvedSourceIsRequestedReleaseV3(requestedFile, resolved) {
		slog.InfoContext(r.Context(), "virtual start re-bound the served candidate to the selected row",
			"component", "api", "signal", "selected_row_rebound",
			"requested_file_id", requestedFile.ID,
			"refused_file_id", resolved.File.ID,
			"candidate_uri", resolved.URI)
		return rebindResolvedSourceToRequestedRowV3(requestedFile, resolved), nil
	}
	slog.WarnContext(r.Context(), "virtual start resolved a different release than the selected version",
		"component", "api", "signal", "selected_release_not_served",
		"requested_file_id", requestedFile.ID,
		"effective_file_id", resolved.File.ID,
		"candidate_uri", resolved.URI,
		"content_id", versionListContentIDV3(requestedFile))
	// Re-resolve the requested row itself, once, with no exclusions and no sibling
	// walk. Its own concrete candidate (or its neutral row) is the only release this
	// attempt may serve; the request context already carries the start's resolve
	// budget, so this second attempt cannot spend past it.
	retried, retryErr := h.resolveVirtualPlaybackSource(
		r, requestedFile, profileID, true, nil, "", req.QualityPreference, bandwidthCapKbps, req.ForceRelink,
		virtualResolveOptionsV3{
			allowFailedCandidate: allowFailedCandidate,
			sessionBound:         false,
			explicitSelection:    true,
		},
	)
	if retryErr == nil && retried.File != nil {
		switch {
		case retried.File.ID == requestedFile.ID:
			return retried, nil
		case resolvedSourceIsRequestedReleaseV3(requestedFile, retried):
			return rebindResolvedSourceToRequestedRowV3(requestedFile, retried), nil
		}
	}
	if retryErr != nil {
		// Log the retry's own cause instead of wrapping it: it belongs to the
		// resolver, and the answer to the viewer must be about the selected release.
		slog.WarnContext(r.Context(), "virtual start could not resolve the selected row's own release",
			"component", "api", "signal", "selected_release_unresolved",
			"requested_file_id", requestedFile.ID, "error", retryErr)
	}
	return resolvedVirtualPlaybackSource{}, fmt.Errorf(
		"%w: row %d did not resolve to its own release", errSelectedReleaseNotServableV3, requestedFile.ID)
}

// retryUnlistedEffectiveRowV3 is the auto-selection path: the candidate that
// resolved to a row outside the item's version list is excluded and the walk tries
// the next one. The retry is bounded to one walk. The refusal is not a verdict about
// the requested release, so neither returned error carries the walk's own cause: the
// candidate loop joins its refusals with "no usable stream", and that text must not
// reach the dead-pin classifier and condemn a row whose only fault was resolving to
// a row the version list does not carry.
func (h *PlaybackHandler) retryUnlistedEffectiveRowV3(
	r *http.Request,
	walkReq *http.Request,
	requestedFile *models.MediaFile,
	resolved resolvedVirtualPlaybackSource,
	req playback.StartRequestV3,
	profileID string,
	allowFailedCandidate bool,
	bandwidthCapKbps int,
) (resolvedVirtualPlaybackSource, error) {
	filter := requestAccessFilter(r)
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
