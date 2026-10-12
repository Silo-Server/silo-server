package jellycompat

import (
	"context"
	"log/slog"
	"slices"

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
func (h *PlaybackHandler) rememberCompatTracks(ctx context.Context, session *Session, playSession *PlaybackSession, req sessionReportRequest) {
	// A terminal play's stop was already handled; a retried or late Stopped
	// report must not replace a choice made since.
	if playSession.PreferenceKey == "" || playSession.Terminal || session.ProfileID == "" {
		return
	}
	source, err := compatPlaybackSource(playSession, req.MediaSourceID)
	if err != nil {
		return
	}
	var audio, subtitle *int
	if req.AudioStreamIndex != nil && h.AudioPreferences != nil {
		index := int(*req.AudioStreamIndex)
		if current := compatRememberedAudioStreamIndex(*source); (current == nil || *current != index) &&
			isValidCompatAudioStreamIndex(source.Version, index) {
			audio = &index
		}
	}
	if req.SubtitleStreamIndex != nil && h.SubtitlePreferences != nil {
		if index := int(*req.SubtitleStreamIndex); index >= compatSubtitleOff && index != compatRememberedSubtitleStreamIndex(*source) {
			subtitle = &index
		}
	}
	if audio == nil && subtitle == nil {
		return
	}

	ctx = context.WithoutCancel(ctx)
	saved := compatSavedConfiguration(ctx, h.storeProvider, session)
	if audio != nil && saved.remembersAudio() {
		h.saveCompatAudioTrack(ctx, session, playSession.PreferenceKey, *source, *audio)
	}
	if subtitle != nil && saved.remembersSubtitles() {
		h.saveCompatSubtitleTrack(ctx, session, playSession.PreferenceKey, *source, *subtitle)
	}
	err = h.playbackStore.Update(playSession.ID, func(current *PlaybackSession) error {
		for i := range current.MediaSources {
			if !mediaSourceIDsEqual(current.MediaSources[i].ID, source.ID) {
				continue
			}
			if audio != nil {
				current.MediaSources[i].RememberedAudioStreamIndex = intPtr(*audio)
			}
			if subtitle != nil {
				current.MediaSources[i].RememberedSubtitleStreamIndex = intPtr(*subtitle)
			}
		}
		return nil
	})
	if err != nil {
		slog.WarnContext(ctx, "jellycompat could not record the remembered tracks on the play", "component", "jellycompat",
			"play_session_id", playSession.ID, "error", err)
	}
}

// compatRememberedAudioStreamIndex is the audio stream the series preference
// names for this play: the last one handled, else the default it started with.
func compatRememberedAudioStreamIndex(source PlaybackMediaSource) *int {
	if source.RememberedAudioStreamIndex != nil {
		return source.RememberedAudioStreamIndex
	}
	return source.DefaultAudioStreamIndex
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

// saveCompatAudioTrack stores the audio stream as the remembered track, with
// the fields native playback records for it.
func (h *PlaybackHandler) saveCompatAudioTrack(ctx context.Context, session *Session, key string, source PlaybackMediaSource, streamIndex int) {
	trackIndex := streamIndex - len(source.Version.VideoTracks)
	track := source.Version.AudioTracks[trackIndex]
	err := h.AudioPreferences.SetAudioPreference(ctx, session.StreamAppUserID, userstore.AudioPreference{
		ProfileID:       session.ProfileID,
		SeriesID:        key,
		AudioTrackIndex: trackIndex,
		AudioLanguage:   compatPreferenceLanguage(track.Language),
		TrackSignature:  playback.AudioTrackSignatureFromTrack(track),
	})
	if err != nil {
		slog.WarnContext(ctx, "jellycompat could not remember the audio track", "component", "jellycompat",
			"series_id", key, "audio_track_index", trackIndex, "error", err)
	}
}

// saveCompatSubtitleTrack stores the subtitle stream, or subtitles off, as
// the remembered choice, as a Silo player does on a pick: mode always with
// the track's language, signature and v3 combined index, or mode off with no
// track. An index that names no track saves nothing.
func (h *PlaybackHandler) saveCompatSubtitleTrack(ctx context.Context, session *Session, key string, source PlaybackMediaSource, streamIndex int) {
	pref := userstore.SubtitlePreference{
		ProfileID:          session.ProfileID,
		SeriesID:           key,
		SubtitleTrackIndex: compatSubtitleOff,
		SubtitleMode:       profileSubtitleOff,
	}
	if streamIndex != compatSubtitleOff {
		var downloaded []subtitles.DownloadedSubtitle
		if streamIndex >= nextDownloadedSubtitleIndex(source.Version) && h.SubtitleRepo != nil {
			var err error
			if downloaded, err = h.SubtitleRepo.ListDownloadedSubtitles(ctx, source.Version.FileID); err != nil {
				slog.WarnContext(ctx, "jellycompat could not look up the reported downloaded subtitle", "component", "jellycompat",
					"file_id", source.Version.FileID, "error", err)
				return
			}
		}
		candidates := compatSubtitleCandidates(source.Version, downloaded)
		position := slices.IndexFunc(candidates, func(c compatSubtitleCandidate) bool { return c.Index == streamIndex })
		if position < 0 {
			return
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
	}
	if err := h.SubtitlePreferences.SetSubtitlePreferenceCanonical(ctx, session.StreamAppUserID, pref); err != nil {
		slog.WarnContext(ctx, "jellycompat could not remember the subtitle track", "component", "jellycompat",
			"series_id", key, "subtitle_track_index", pref.SubtitleTrackIndex, "error", err)
	}
}

// compatCombinedSubtitleIndex is the candidate's index in the playback v3
// combined subtitle space Silo clients store: external files, then embedded
// tracks, then downloaded subtitles, each in catalog order.
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
