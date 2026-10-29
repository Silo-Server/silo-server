package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// desiredSubtitleFromFileV3 snapshots the subtitle at combined index on file
// (externals, then embedded tracks, then downloaded subtitles), or returns nil
// when the index names none. It errors only when the downloaded subtitles it
// needs cannot be read.
func (h *PlaybackHandler) desiredSubtitleFromFileV3(ctx context.Context, file *models.MediaFile, index int) (*playback.DesiredSubtitleTrackV3, error) {
	if file == nil || index < 0 {
		return nil, nil
	}
	externals, embedded := len(file.ExternalSubtitles), len(file.SubtitleTracks)
	switch {
	case index < externals:
		track := file.ExternalSubtitles[index]
		return &playback.DesiredSubtitleTrackV3{
			FileID: file.ID, Source: playback.SubtitleSourceExternalV3, ExternalPath: track.Path,
			Language: track.Language, Title: displayedSubtitleTitleV3(track.Title, track.EmbeddedTitle),
			Format: track.Format, Forced: track.Forced, HearingImpaired: track.HearingImpaired,
		}, nil
	case index < externals+embedded:
		track := file.SubtitleTracks[index-externals]
		return &playback.DesiredSubtitleTrackV3{
			FileID: file.ID, Source: playback.SubtitleSourceEmbeddedV3, EmbeddedStreamIndex: track.Index,
			Language: track.Language, Title: displayedSubtitleTitleV3(track.Title, track.EmbeddedTitle),
			Format: track.Codec, Forced: track.Forced, HearingImpaired: track.HearingImpaired,
		}, nil
	}
	if h.SubtitleRepo == nil {
		return nil, nil
	}
	downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
	if err != nil {
		return nil, fmt.Errorf("load downloaded subtitles: %w", wrapSubtitleStoreErrorV3(err))
	}
	position := index - externals - embedded
	if position >= len(downloaded) {
		return nil, nil
	}
	track := downloaded[position]
	// Downloaded subtitles carry no forced or SDH title, so they only ever
	// match another downloaded subtitle exactly.
	return &playback.DesiredSubtitleTrackV3{
		FileID: file.ID, Source: playback.SubtitleSourceDownloadedV3, DownloadedSubtitleID: track.ID,
		Language: track.Language, Format: string(track.Format), ReleaseName: track.ReleaseName,
		HearingImpaired: track.HearingImpaired,
	}, nil
}

// resolveDesiredSubtitleV3 returns the combined index of want on target, or -1
// when target has no equivalent. On the file it was chosen on the track is
// found by its source identity, since combined ordinals shift when sidecar or
// downloaded inventory changes. Elsewhere the same source kind with the same
// language, format and forced/SDH flags wins, then the same language and
// variant in another format.
func (h *PlaybackHandler) resolveDesiredSubtitleV3(ctx context.Context, want playback.DesiredSubtitleTrackV3, target *models.MediaFile) (int, error) {
	if target == nil {
		return -1, nil
	}
	var downloaded []subtitles.DownloadedSubtitle
	if want.Source == playback.SubtitleSourceDownloadedV3 && h.SubtitleRepo != nil {
		var err error
		downloaded, err = h.SubtitleRepo.ListDownloadedSubtitles(ctx, target.ID)
		if err != nil {
			// A failed lookup says nothing about whether the track exists;
			// dropping the selection over it would turn subtitles off.
			return -1, fmt.Errorf("load downloaded subtitles: %w", wrapSubtitleStoreErrorV3(err))
		}
	}
	if target.ID == want.FileID {
		if index := sameFileSubtitleIndexV3(want, target, downloaded); index >= 0 {
			return index, nil
		}
	}
	if index, decided := sameSourceSubtitleIndexV3(want, target, downloaded); decided {
		return index, nil
	}
	if want.Language == "" {
		return -1, nil
	}
	// Editions often carry the same subtitle in different formats (PGS on an
	// HDR remux, SRT on an SDR encode). Keep the viewer's language and
	// forced/SDH variant in whatever format the target has.
	return subtitleVariantIndexV3(target, want.Language, want.Title, want.Forced, want.HearingImpaired), nil
}

// sameFileSubtitleIndexV3 finds want on the file it was chosen on by its
// source identity, or returns -1 when that file no longer holds it.
func sameFileSubtitleIndexV3(want playback.DesiredSubtitleTrackV3, target *models.MediaFile, downloaded []subtitles.DownloadedSubtitle) int {
	externals, embedded := len(target.ExternalSubtitles), len(target.SubtitleTracks)
	switch want.Source {
	case playback.SubtitleSourceExternalV3:
		for i, track := range target.ExternalSubtitles {
			if track.Path == want.ExternalPath {
				return i
			}
		}
	case playback.SubtitleSourceEmbeddedV3:
		for i, track := range target.SubtitleTracks {
			if track.Index == want.EmbeddedStreamIndex {
				return externals + i
			}
		}
	case playback.SubtitleSourceDownloadedV3:
		for i, track := range downloaded {
			if track.ID == want.DownloadedSubtitleID {
				return externals + embedded + i
			}
		}
	}
	return -1
}

// sameSourceSubtitleIndexV3 finds a track of want's source kind on target
// with the same language, format and forced/SDH flags. decided is true when
// no other match may be tried: downloaded subtitles carry no variant title,
// so they only match another downloaded subtitle.
func sameSourceSubtitleIndexV3(want playback.DesiredSubtitleTrackV3, target *models.MediaFile, downloaded []subtitles.DownloadedSubtitle) (index int, decided bool) {
	externals, embedded := len(target.ExternalSubtitles), len(target.SubtitleTracks)
	switch want.Source {
	case playback.SubtitleSourceExternalV3:
		for i, track := range target.ExternalSubtitles {
			if strings.EqualFold(track.Language, want.Language) && strings.EqualFold(track.Format, want.Format) && track.Forced == want.Forced && track.HearingImpaired == want.HearingImpaired {
				return i, true
			}
		}
	case playback.SubtitleSourceEmbeddedV3:
		for i, track := range target.SubtitleTracks {
			if strings.EqualFold(track.Language, want.Language) && strings.EqualFold(track.Codec, want.Format) && track.Forced == want.Forced && track.HearingImpaired == want.HearingImpaired {
				return externals + i, true
			}
		}
	case playback.SubtitleSourceDownloadedV3:
		for i, track := range downloaded {
			// The release names the cut a downloaded subtitle was timed for;
			// another release in the same language may play out of sync.
			if strings.EqualFold(track.Language, want.Language) && strings.EqualFold(string(track.Format), want.Format) && strings.EqualFold(track.ReleaseName, want.ReleaseName) {
				return externals + embedded + i, true
			}
		}
		return -1, true
	}
	return -1, false
}

// applyDesiredTracksV3 points request's audio and subtitle at desired's
// equivalents on target. It reports whether the desired subtitle had to be
// left out because target has none like it; the desired tracks themselves
// are unchanged, so a later move to a file that has it restores it.
func (h *PlaybackHandler) applyDesiredTracksV3(ctx context.Context, desired *playback.DesiredTracksV3, target *models.MediaFile, request *playback.StartRequestV3) (subtitleDropped bool, err error) {
	if desired == nil || target == nil || request == nil {
		return false, nil
	}
	if desired.Audio != nil && len(target.AudioTracks) > 0 {
		index := desired.Audio.ResolveOn(target)
		request.AudioTrackIndex = &index
		request.AudioTrackID = playback.TrackIDV3(target.ID, "audio", index)
	} else {
		// An identity from another file would be refused on target; with
		// nothing to resolve, target plays its default track.
		request.AudioTrackIndex = nil
		request.AudioTrackID = ""
	}
	request.SubtitleTrackIndex = nil
	request.SubtitleTrackID = ""
	if desired.Subtitle == nil {
		return false, nil
	}
	index, err := h.resolveDesiredSubtitleV3(ctx, *desired.Subtitle, target)
	if err != nil {
		return false, err
	}
	if index < 0 {
		return true, nil
	}
	request.SubtitleTrackIndex = &index
	request.SubtitleTrackID = playback.TrackIDV3(target.ID, "subtitle", index)
	return false, nil
}

// requestAudioIndexV3 is the audio ordinal request selects on file, after
// applyDesiredTracksV3 pointed it there: its index, or file's default track.
func requestAudioIndexV3(file *models.MediaFile, request playback.StartRequestV3) int {
	if request.AudioTrackIndex == nil {
		return normalizeAudioTrackIndex(file, -1)
	}
	return normalizeAudioTrackIndex(file, *request.AudioTrackIndex)
}

// trackIdentityIndexV3 is the ordinal identity names on file, or -1 when it
// names nothing there.
func trackIdentityIndexV3(identity *playback.TrackIdentityV3, file *models.MediaFile, kind string) int {
	if identity == nil || file == nil {
		return -1
	}
	if identity.ID != "" {
		fileID, parsedKind, ordinal, ok := playback.ParseTrackIDV3(identity.ID)
		if !ok || parsedKind != kind || fileID != file.ID {
			return -1
		}
		return ordinal
	}
	if identity.Index != nil {
		return *identity.Index
	}
	return -1
}

// startIntentV3 is what a start asked for on the requested file, captured
// before any version fallback remaps it.
type startIntentV3 struct {
	// audioIndex is the audio resolved on the requested file, from the
	// request or the viewer's preferences.
	audioIndex int
	// subtitle is the subtitle the request named, or nil when it named none.
	subtitle *playback.TrackIdentityV3
}

// startDesiredTracksV3 is a new attempt's intent: the audio resolved on the
// requested file before any version fallback, and the subtitle the client
// asked for there or, when it asked for none, the one the plan selected.
func (h *PlaybackHandler) startDesiredTracksV3(ctx context.Context, requestedFile *models.MediaFile, intent startIntentV3, effectiveFile *models.MediaFile, plan *playback.PlanV3, recipe playback.ExecutableRecipeV3) *playback.DesiredTracksV3 {
	if plan == nil {
		return nil
	}
	desired := &playback.DesiredTracksV3{PlanID: plan.PlanID, Audio: playback.DesiredAudioFromFile(requestedFile, intent.audioIndex)}
	subtitleFile, subtitleIndex := requestedFile, trackIdentityIndexV3(intent.subtitle, requestedFile, "subtitle")
	if intent.subtitle == nil {
		subtitleFile, subtitleIndex = effectiveFile, trackIdentityIndexV3(plan.SelectedTracks.Subtitle, effectiveFile, "subtitle")
	}
	if subtitle := h.plannedDownloadedSubtitleV3(ctx, subtitleFile, subtitleIndex, effectiveFile, plan, recipe); subtitle != nil {
		desired.Subtitle = subtitle
		return desired
	}
	subtitle, err := h.desiredSubtitleFromFileV3(ctx, subtitleFile, subtitleIndex)
	if err != nil && subtitleFile != effectiveFile {
		// The requested file's downloaded subtitles are unreadable; keep the
		// subtitle the plan selected on the effective file instead.
		slog.WarnContext(ctx, "protocol v3 start: requested subtitle snapshot unavailable", logComponentKey, playbackLogValueV3, "error", err)
		subtitleIndex = trackIdentityIndexV3(plan.SelectedTracks.Subtitle, effectiveFile, "subtitle")
		subtitle = h.plannedDownloadedSubtitleV3(ctx, effectiveFile, subtitleIndex, effectiveFile, plan, recipe)
		if subtitle == nil {
			subtitle, err = h.desiredSubtitleFromFileV3(ctx, effectiveFile, subtitleIndex)
		} else {
			err = nil
		}
	}
	if err != nil {
		// Without a snapshot replans fall back to the plan's own selection.
		slog.WarnContext(ctx, "protocol v3 start: desired subtitle unavailable", logComponentKey, playbackLogValueV3, "error", err)
		return nil
	}
	desired.Subtitle = subtitle
	return desired
}

// plannedDownloadedSubtitleV3 snapshots a downloaded subtitle the plan itself
// selected from the frozen recipe and the plan's inventory, the snapshot the
// planner used, rather than listing downloaded subtitles again: the list can
// change in between, and its positions with it. The release, which only the
// stored row has, is read by the frozen id.
func (h *PlaybackHandler) plannedDownloadedSubtitleV3(ctx context.Context, file *models.MediaFile, index int, effectiveFile *models.MediaFile, plan *playback.PlanV3, recipe playback.ExecutableRecipeV3) *playback.DesiredSubtitleTrackV3 {
	if file == nil || effectiveFile == nil || file.ID != effectiveFile.ID || index < 0 ||
		recipe.SubtitleSource != playback.SubtitleSourceDownloadedV3 || recipe.DownloadedSubtitleID <= 0 ||
		index != trackIdentityIndexV3(plan.SelectedTracks.Subtitle, effectiveFile, "subtitle") {
		return nil
	}
	for _, item := range plan.Subtitle.Inventory {
		if item.CombinedIndex == index && item.Source == playback.SubtitleSourceDownloadedV3 {
			desired := &playback.DesiredSubtitleTrackV3{
				FileID: file.ID, Source: playback.SubtitleSourceDownloadedV3, DownloadedSubtitleID: recipe.DownloadedSubtitleID,
				Language: item.Language, Format: item.Codec, HearingImpaired: item.HearingImpaired,
			}
			if h.SubtitleRepo != nil {
				// Without the release the subtitle still restores on this
				// file; it just cannot match a download on another version.
				if row, err := h.SubtitleRepo.GetDownloadedSubtitle(ctx, recipe.DownloadedSubtitleID); err == nil && row != nil {
					desired.ReleaseName = row.ReleaseName
				}
			}
			return desired
		}
	}
	return nil
}

// replanDesiredTracksV3 returns the attempt's intent before this replan, or
// derives it from the current plan when the record has none or its intent was
// left behind by a replan that did not maintain it. It returns nil when
// nothing usable exists; replans then remap from the current plan as before.
func (h *PlaybackHandler) replanDesiredTracksV3(ctx context.Context, record *playback.AttemptRecordV3, currentEffectiveFile *models.MediaFile) *playback.DesiredTracksV3 {
	if record.DesiredTracks != nil && record.DesiredTracks.PlanID == record.CurrentPlanID {
		desired := *record.DesiredTracks
		return &desired
	}
	selected := record.CurrentPlan.SelectedTracks
	desired := &playback.DesiredTracksV3{PlanID: record.CurrentPlanID, Audio: playback.DesiredAudioFromFile(currentEffectiveFile, trackIdentityIndexV3(selected.Audio, currentEffectiveFile, "audio"))}
	subtitle, err := h.desiredSubtitleFromFileV3(ctx, currentEffectiveFile, trackIdentityIndexV3(selected.Subtitle, currentEffectiveFile, "subtitle"))
	if err != nil {
		return nil
	}
	desired.Subtitle = subtitle
	return desired
}

// updateDesiredTracksV3 folds a replan's track choices into desired. Clients
// resend the tracks they are playing with every replan, so only a track that
// differs from the current plan's is a new choice; an echo keeps the desired
// track even when the current plan could not deliver it. A track_change that
// omits the subtitle turns subtitles off only when the current plan was
// showing one. It errors when the chosen subtitle cannot be snapshotted
// because downloaded subtitles are unreadable; desired is then unchanged.
func (h *PlaybackHandler) updateDesiredTracksV3(ctx context.Context, desired *playback.DesiredTracksV3, current, requested playback.SelectedTracksV3, trackChange bool, file *models.MediaFile) (*playback.DesiredTracksV3, error) {
	if desired == nil {
		return nil, nil
	}
	next := *desired
	if requested.Audio != nil {
		if index := trackIdentityIndexV3(requested.Audio, file, "audio"); index >= 0 && index != trackIdentityIndexV3(current.Audio, file, "audio") {
			if audio := playback.DesiredAudioFromFile(file, index); audio != nil {
				next.Audio = audio
			}
		}
	}
	currentSubtitle := trackIdentityIndexV3(current.Subtitle, file, "subtitle")
	switch {
	case requested.Subtitle != nil:
		index := trackIdentityIndexV3(requested.Subtitle, file, "subtitle")
		if index < 0 || index == currentSubtitle {
			break
		}
		subtitle, err := h.desiredSubtitleFromFileV3(ctx, file, index)
		if err != nil {
			return desired, err
		}
		if subtitle != nil {
			// An index naming no track is the replan's to reject.
			next.Subtitle = subtitle
		}
	case trackChange && currentSubtitle >= 0:
		next.Subtitle = nil
	}
	return &next, nil
}
