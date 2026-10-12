package jellycompat

import (
	"context"
	"encoding/json"
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
// from the one the play started with by default (which already comes from
// the remembered tracks) or from the one last saved for this play; a device
// fallback such as AC3 in place of TrueHD is therefore never saved. The
// viewer's RememberAudioSelections and RememberSubtitleSelections settings
// turn either half off, as in Jellyfin.
func (h *PlaybackHandler) rememberCompatTracks(ctx context.Context, session *Session, playSession *PlaybackSession, req sessionReportRequest) {
	if playSession.PreferenceKey == "" || session.ProfileID == "" {
		return
	}
	source := compatReportSource(playSession, req.MediaSourceID)
	if source == nil {
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
		index := max(int(*req.SubtitleStreamIndex), compatSubtitleOff)
		if index != compatRememberedSubtitleStreamIndex(*source) {
			subtitle = &index
		}
	}
	if audio == nil && subtitle == nil {
		return
	}

	ctx = context.WithoutCancel(ctx)
	rememberAudio, rememberSubtitles := h.compatRememberSelections(ctx, session)
	// A choice the viewer's settings do not remember still counts as handled,
	// so later reports of the same track do not look it up again.
	if audio != nil && rememberAudio && !h.saveCompatAudioTrack(ctx, session, playSession.PreferenceKey, *source, *audio) {
		audio = nil
	}
	if subtitle != nil && rememberSubtitles && !h.saveCompatSubtitleTrack(ctx, session, playSession.PreferenceKey, *source, *subtitle) {
		subtitle = nil
	}
	if audio == nil && subtitle == nil {
		return
	}
	err := h.playbackStore.Update(playSession.ID, func(current *PlaybackSession) error {
		for i := range current.MediaSources {
			if current.MediaSources[i].ID != source.ID {
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

// compatReportSource is the source a report names, or the one the play
// streams when the report names none.
func compatReportSource(playSession *PlaybackSession, mediaSourceID string) *PlaybackMediaSource {
	if mediaSourceID != "" {
		return findMediaSource(playSession, mediaSourceID)
	}
	if len(playSession.MediaSources) == 1 {
		source := playSession.MediaSources[0]
		return &source
	}
	for _, source := range playSession.MediaSources {
		if playSession.UpstreamMediaFileID != 0 && source.FileID == playSession.UpstreamMediaFileID {
			return &source
		}
	}
	return nil
}

// compatRememberedAudioStreamIndex is the audio stream the series preference
// names for this play: the last one saved, else the default it started with.
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

// compatRememberSelections reads the viewer's RememberAudioSelections and
// RememberSubtitleSelections; both default to true, as in Jellyfin.
func (h *PlaybackHandler) compatRememberSelections(ctx context.Context, session *Session) (audio, subtitles bool) {
	if h.storeProvider == nil {
		return true, true
	}
	store, err := h.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil || store == nil {
		return true, true
	}
	raw, err := store.GetSetting(ctx, configurationKey(session.ProfileID))
	if err != nil || raw == "" {
		return true, true
	}
	var saved struct {
		RememberAudioSelections    *bool
		RememberSubtitleSelections *bool
	}
	if json.Unmarshal([]byte(raw), &saved) != nil {
		return true, true
	}
	return saved.RememberAudioSelections == nil || *saved.RememberAudioSelections,
		saved.RememberSubtitleSelections == nil || *saved.RememberSubtitleSelections
}

// saveCompatAudioTrack stores the audio stream as the remembered track, with
// the fields native playback records for it. It reports whether the stream
// is settled: saved, or not a track that can be saved.
func (h *PlaybackHandler) saveCompatAudioTrack(ctx context.Context, session *Session, key string, source PlaybackMediaSource, streamIndex int) bool {
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
		return false
	}
	return true
}

// saveCompatSubtitleTrack stores the subtitle stream, or subtitles off, as
// the remembered choice, as a Silo player does on a pick: mode always with
// the track's language and signature, or mode off with no track. It reports
// whether the stream is settled: saved, or not a track that can be saved.
func (h *PlaybackHandler) saveCompatSubtitleTrack(ctx context.Context, session *Session, key string, source PlaybackMediaSource, streamIndex int) bool {
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
				return false
			}
		}
		candidates := compatSubtitleCandidates(source.Version, downloaded)
		position := slices.IndexFunc(candidates, func(c compatSubtitleCandidate) bool { return c.Index == streamIndex })
		if position < 0 {
			return true
		}
		track := candidates[position]
		pref.SubtitleTrackIndex = position
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
		return false
	}
	return true
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
