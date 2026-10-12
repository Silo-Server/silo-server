package jellycompat

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// AudioPreferenceWriter saves a profile's remembered audio track for a series
// (or movie) the way PUT /api/v2/audio-prefs/{series_id} does.
type AudioPreferenceWriter interface {
	SetAudioPreference(ctx context.Context, userID int, pref userstore.AudioPreference) error
}

// SubtitlePreferenceWriter saves a profile's remembered subtitle choice for a
// series (or movie) the way PUT /api/v2/subtitle-prefs/{series_id} does.
type SubtitlePreferenceWriter interface {
	SetSubtitlePreferenceCanonical(ctx context.Context, userID int, pref userstore.SubtitlePreference) error
}

// compatTrackMemoryTimeout bounds the reads and writes one report makes to
// remember its tracks, which outlive a client that drops the connection.
const compatTrackMemoryTimeout = 10 * time.Second

// compatSubtitleOff is the SubtitleStreamIndex Jellyfin reports for
// subtitles off, and the track index Silo stores for it.
const compatSubtitleOff = -1

// compatTrackPreferenceKey is the id Silo remembers a play's tracks under:
// the series for an episode and the movie itself, as the native clients key
// them. Other items remember nothing.
func compatTrackPreferenceKey(detail *upstreamItemDetail) string {
	switch detail.Type {
	case compatCatalogTypeEpisode:
		return detail.SeriesID
	case compatCatalogTypeMovie:
		return detail.ContentID
	default:
		return ""
	}
}

// rememberCompatTracks saves the audio and subtitle tracks a Jellyfin client
// reports as the series' remembered tracks, so the next episode starts on
// them in Jellyfin and Silo clients alike. Reports cannot tell a viewer's pick
// from the client's automatic choice, so a track is saved only when it differs
// from the default PlaybackInfo offered (which already comes from the
// remembered tracks) or from the one last handled for this play. Each change
// is written once; a failed write is logged, not retried on every report. The
// viewer's RememberAudioSelections and RememberSubtitleSelections settings
// turn either half off, as in Jellyfin.
//
// A replica's copy of the play can be a few seconds stale, so a Progress report
// it judges unchanged is rechecked on the next report. A Stopped report is the
// last chance, so it is judged against the stored play instead.
func (h *PlaybackHandler) rememberCompatTracks(ctx context.Context, session *Session, playSession *PlaybackSession, req sessionReportRequest, stop bool) {
	// A terminal play's stop was already handled; a retried or late Stopped
	// report must not replace a choice made since.
	if playSession.PreferenceKey == "" || playSession.Terminal || session.ProfileID == "" {
		return
	}
	source := compatReportedSource(playSession, req.MediaSourceID)
	if source == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compatTrackMemoryTimeout)
	defer cancel()
	var audio, subtitle *int
	if req.AudioStreamIndex != nil && h.AudioPreferences != nil {
		if index := int(*req.AudioStreamIndex); (stop || compatAudioStreamChanged(*source, index)) && isValidCompatAudioStreamIndex(source.Version, index) {
			audio = &index
		}
	}
	var subtitlePref userstore.SubtitlePreference
	if req.SubtitleStreamIndex != nil && h.SubtitlePreferences != nil {
		index := int(*req.SubtitleStreamIndex)
		if index >= compatSubtitleOff && (stop || index != compatRememberedSubtitleStreamIndex(*source)) {
			// An index that names no track is not recorded either, so it
			// cannot become the play's baseline.
			var ok bool
			if subtitlePref, ok = h.compatSubtitlePreference(ctx, session, playSession.PreferenceKey, *source, index); ok {
				subtitle = &index
			}
		}
	}
	if audio == nil && subtitle == nil {
		return
	}

	// The viewer's Remember settings are read before claiming: when they cannot
	// be read, nothing is claimed or written, so an opt-out is never ignored
	// and the next report tries again.
	saved, err := compatSavedConfiguration(ctx, h.storeProvider, session)
	if err != nil {
		slog.WarnContext(ctx, "jellycompat could not read the remember-selection settings", "component", "jellycompat",
			"profile_id", session.ProfileID, "error", err)
		return
	}
	// A track the viewer does not remember is not claimed either, so no
	// report's read-back below can write it.
	if !saved.remembersAudio() {
		audio = nil
	}
	if !saved.remembersSubtitles() {
		subtitle = nil
	}
	if audio == nil && subtitle == nil {
		return
	}
	// Claim the change on the play before writing it, so overlapping reports
	// (a retry, a Stopped beside a Progress, another replica) write it once.
	audio, subtitle, err = h.claimCompatTrackChange(playSession.ID, source.ID, audio, subtitle)
	// A play that ended meanwhile is not a failure either.
	if err != nil && !errors.Is(err, ErrSessionNotFound) {
		slog.WarnContext(ctx, "jellycompat could not record the remembered tracks on the play", "component", "jellycompat",
			"play_session_id", playSession.ID, "error", err)
	}
	if err != nil || (audio == nil && subtitle == nil) {
		return
	}
	key := playSession.PreferenceKey
	// A later report can claim another track while this one writes, and its
	// write can land first. After each write the play's latest claim is read
	// back and written again until it matches, so whichever write lands last
	// leaves the latest claimed track. Claims need new reports, so this ends;
	// the report's deadline bounds it if writes stall.
	if audio != nil {
		for index := *audio; ctx.Err() == nil; {
			pref := compatAudioPreference(session, key, *source, index)
			if err := h.AudioPreferences.SetAudioPreference(ctx, session.StreamAppUserID, pref); err != nil {
				slog.WarnContext(ctx, "jellycompat could not remember the audio track", "component", "jellycompat",
					"series_id", key, "audio_track_index", pref.AudioTrackIndex, "error", err)
				break
			}
			latest, ok := h.latestCompatTrackClaim(playSession, source.ID)
			if !ok || latest.RememberedAudioStreamIndex == nil || *latest.RememberedAudioStreamIndex == index {
				break
			}
			index = *latest.RememberedAudioStreamIndex
		}
	}
	if subtitle != nil {
		for index := *subtitle; ctx.Err() == nil; {
			if err := h.SubtitlePreferences.SetSubtitlePreferenceCanonical(ctx, session.StreamAppUserID, subtitlePref); err != nil {
				slog.WarnContext(ctx, "jellycompat could not remember the subtitle track", "component", "jellycompat",
					"series_id", key, "subtitle_track_index", subtitlePref.SubtitleTrackIndex, "error", err)
				break
			}
			latest, ok := h.latestCompatTrackClaim(playSession, source.ID)
			if !ok || latest.RememberedSubtitleStreamIndex == nil || *latest.RememberedSubtitleStreamIndex == index {
				break
			}
			index = *latest.RememberedSubtitleStreamIndex
			if subtitlePref, ok = h.compatSubtitlePreference(ctx, session, key, *latest, index); !ok {
				break
			}
		}
	}
}

// latestCompatTrackClaim is the play's source as the store holds it now, with
// the tracks most recently claimed for it. It reads through Update rather than
// Get: the durable store can serve Get from a replica's cached copy, while
// Update ends on the stored row under its lock. That costs one row write per
// track change. Update refuses a play a later Stopped report has finished
// meanwhile, so that play's final claim is read as a finished play instead.
func (h *PlaybackHandler) latestCompatTrackClaim(playSession *PlaybackSession, sourceID string) (*PlaybackMediaSource, bool) {
	var latest *PlaybackMediaSource
	err := h.playbackStore.Update(playSession.ID, func(current *PlaybackSession) error {
		// Never fail, for the reason claimCompatTrackChange gives.
		latest, _ = compatPlaybackSource(current, sourceID)
		return nil
	})
	if errors.Is(err, ErrSessionNotFound) {
		finished, ok := h.playbackStore.GetFinalizable(playSession.ID, playSession.CompatToken)
		if !ok {
			return nil, false
		}
		latest, err = compatPlaybackSource(finished, sourceID)
	}
	return latest, err == nil && latest != nil
}

// claimCompatTrackChange records the reported tracks on the play's source
// under the store's lock, rechecking each against the current row, and
// returns the ones this report changed. The durable store runs the callback on
// its cached copy and then on the stored row, and replays it after a failed
// database write, so the callback never fails: it only records what it found,
// and the stored row's run decides. A replay runs after this Update returned,
// possibly over a newer claim another replica saved since, so a replayed run
// changes nothing.
func (h *PlaybackHandler) claimCompatTrackChange(playSessionID, sourceID string, audio, subtitle *int) (claimedAudio, claimedSubtitle *int, err error) {
	ended := false
	var returned atomic.Bool
	err = h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		if returned.Load() {
			return nil
		}
		claimedAudio, claimedSubtitle = nil, nil
		// The stored row can be terminal while a replica's copy is not.
		ended = current.Terminal
		if ended {
			return nil
		}
		for i := range current.MediaSources {
			source := &current.MediaSources[i]
			if !mediaSourceIDsEqual(source.ID, sourceID) {
				continue
			}
			if audio != nil && compatAudioStreamChanged(*source, *audio) {
				source.RememberedAudioStreamIndex = intPtr(*audio)
				claimedAudio = audio
			}
			if subtitle != nil && *subtitle != compatRememberedSubtitleStreamIndex(*source) {
				source.RememberedSubtitleStreamIndex = intPtr(*subtitle)
				claimedSubtitle = subtitle
			}
		}
		return nil
	})
	returned.Store(true)
	if err == nil && ended {
		err = ErrSessionNotFound
	}
	return claimedAudio, claimedSubtitle, err
}

// compatReportedSource is the source a report names. A report without
// MediaSourceId is matched to the version the play streams, not the first
// offered, so a multi-version item never saves another version's track.
func compatReportedSource(playSession *PlaybackSession, mediaSourceID string) *PlaybackMediaSource {
	if mediaSourceID == "" && len(playSession.MediaSources) > 1 {
		if playSession.UpstreamMediaFileID == 0 {
			return nil
		}
		for _, source := range playSession.MediaSources {
			if source.FileID == playSession.UpstreamMediaFileID {
				return &source
			}
		}
		return nil
	}
	source, err := compatPlaybackSource(playSession, mediaSourceID)
	if err != nil {
		return nil
	}
	return source
}

// compatAudioStreamChanged reports whether a reported audio stream differs
// from the last one handled for this play or, before any, from the defaults
// it started with: the track the viewer's preferences select, and the device
// fallback PlaybackInfo offered in its place (AC3 for TrueHD). A client
// playing either made no choice.
func compatAudioStreamChanged(source PlaybackMediaSource, index int) bool {
	if source.RememberedAudioStreamIndex != nil {
		return index != *source.RememberedAudioStreamIndex
	}
	for _, unchosen := range []*int{source.DefaultAudioStreamIndex, defaultAudioStreamIndex(source.Version)} {
		if unchosen != nil && *unchosen == index {
			return false
		}
	}
	return true
}

// compatRememberedSubtitleStreamIndex is the subtitle stream the series
// preference names for this play, or compatSubtitleOff.
func compatRememberedSubtitleStreamIndex(source PlaybackMediaSource) int {
	switch {
	case source.RememberedSubtitleStreamIndex != nil:
		return *source.RememberedSubtitleStreamIndex
	case source.DefaultSubtitleStreamIndex != nil:
		return *source.DefaultSubtitleStreamIndex
	default:
		return compatSubtitleOff
	}
}

// compatAudioPreference is the remembered track a valid audio stream stands
// for, with the fields native playback records for it.
func compatAudioPreference(session *Session, key string, source PlaybackMediaSource, streamIndex int) userstore.AudioPreference {
	trackIndex := streamIndex - len(source.Version.VideoTracks)
	track := source.Version.AudioTracks[trackIndex]
	return userstore.AudioPreference{
		ProfileID:       session.ProfileID,
		SeriesID:        key,
		AudioTrackIndex: trackIndex,
		AudioLanguage:   compatPreferenceLanguage(track.Language),
		TrackSignature:  playback.AudioTrackSignatureFromTrack(track),
	}
}

// compatSubtitlePreference is the remembered choice a subtitle stream, or
// subtitles off, stands for, as a Silo player saves it on a pick: mode always
// with the track's language, signature and v3 combined index, or mode off
// with no track. ok is false when the index names no track.
func (h *PlaybackHandler) compatSubtitlePreference(ctx context.Context, session *Session, key string, source PlaybackMediaSource, streamIndex int) (pref userstore.SubtitlePreference, ok bool) {
	pref = userstore.SubtitlePreference{
		ProfileID:          session.ProfileID,
		SeriesID:           key,
		SubtitleTrackIndex: compatSubtitleOff,
		SubtitleMode:       profileSubtitleOff,
	}
	if streamIndex == compatSubtitleOff {
		return pref, true
	}
	var downloaded []subtitles.DownloadedSubtitle
	if streamIndex >= nextDownloadedSubtitleIndex(source.Version) && h.SubtitleRepo != nil {
		var err error
		if downloaded, err = h.SubtitleRepo.ListDownloadedSubtitles(ctx, source.Version.FileID); err != nil {
			slog.WarnContext(ctx, "jellycompat could not look up the reported downloaded subtitle", "component", "jellycompat",
				"file_id", source.Version.FileID, "error", err)
			return pref, false
		}
	}
	candidates := compatSubtitleCandidates(source.Version, downloaded)
	position := slices.IndexFunc(candidates, func(c compatSubtitleCandidate) bool { return c.Index == streamIndex })
	if position < 0 {
		return pref, false
	}
	track := candidates[position]
	pref.SubtitleTrackIndex = compatCombinedSubtitleIndex(candidates, position)
	pref.SubtitleLanguage = compatPreferenceLanguage(track.Language)
	pref.SubtitleMode = profileSubtitleAlways
	pref.TrackSignature = &userstore.SubtitleTrackSignature{
		Source:          track.Source,
		Language:        track.Language,
		Codec:           track.Codec,
		Label:           track.Label,
		Forced:          track.Forced,
		HearingImpaired: track.HearingImpaired,
	}
	return pref, true
}

// compatCombinedSubtitleIndex is the candidate's index in the playback v3
// combined subtitle space Silo clients store: external files, then embedded
// tracks, then downloaded subtitles, each in catalog order, as
// playback.BuildSubtitleInventoryV3 assigns them. Playback matches the stored
// signature first; this index is only its fallback.
func compatCombinedSubtitleIndex(candidates []compatSubtitleCandidate, position int) int {
	rank := func(source string) int {
		switch source {
		case playback.SubtitleSourceExternalV3:
			return 0
		case playback.SubtitleSourceEmbeddedV3:
			return 1
		default:
			return 2
		}
	}
	target := rank(candidates[position].Source)
	index := 0
	for i, candidate := range candidates {
		if r := rank(candidate.Source); r < target || (r == target && i < position) {
			index++
		}
	}
	return index
}

// compatPreferenceLanguage is the language preference a track implies: its
// language, or "" (none) for an undefined tag or one the canonical language
// setting would refuse. The track signature keeps the language as probed.
func compatPreferenceLanguage(language string) string {
	if _, ok := settingscontract.NormalizeLanguageTag(language); !ok || compatLanguageUndefined(language) {
		return ""
	}
	return language
}
