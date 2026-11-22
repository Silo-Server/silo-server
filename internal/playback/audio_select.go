package playback

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/mediatrack"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	// OriginalLanguageSentinel is the value stored in audio language preference
	// columns to mean "use the media item's original language."
	OriginalLanguageSentinel = mediatrack.OriginalLanguageSentinel

	// OriginalLanguageTag is the settings-contract spelling of the same choice
	// for playback.audio_language.
	OriginalLanguageTag = mediatrack.OriginalLanguageTag
)

// AudioTrackPreference holds a per-series audio track preference.
type AudioTrackPreference = mediatrack.AudioTrackPreference

// IsOriginalLanguagePreference reports whether a stored audio language
// preference means the media item's original language.
func IsOriginalLanguagePreference(preference string) bool {
	return mediatrack.IsOriginalLanguagePreference(preference)
}

// SelectAudioTrack determines which audio track to use based on preferences.
func SelectAudioTrack(tracks []models.AudioTrack, preferredLang string, seriesPref *AudioTrackPreference) int {
	return mediatrack.SelectAudioTrack(tracks, preferredLang, seriesPref)
}

// SavedAudioTrackChosen reports whether SelectAudioTrack chose the track by
// the viewer's saved series identity rather than by language or the file's
// default flag.
func SavedAudioTrackChosen(tracks []models.AudioTrack, pref *AudioTrackPreference, chosen int) bool {
	return mediatrack.SavedAudioTrackChosen(tracks, pref, chosen)
}

// AudioTrackDecodable reports whether FFmpeg can decode tracks[index] of a
// probed file whose file-level audio codec is fileCodec.
func AudioTrackDecodable(tracks []models.AudioTrack, index int, fileCodec string) bool {
	return mediatrack.AudioTrackDecodable(tracks, index, fileCodec)
}

// AnyAudioTrackDecodable reports whether any of a file's audio tracks passes
// AudioTrackDecodable.
func AnyAudioTrackDecodable(tracks []models.AudioTrack, fileCodec string) bool {
	return mediatrack.AnyAudioTrackDecodable(tracks, fileCodec)
}

// MatchAudioTrackAcrossVersions maps a selection made against one file's
// audio inventory onto another version of the same content.
func MatchAudioTrackAcrossVersions(
	requestedTracks []models.AudioTrack,
	effectiveTracks []models.AudioTrack,
	requestedIndex int,
) int {
	return mediatrack.MatchAudioTrackAcrossVersions(requestedTracks, effectiveTracks, requestedIndex)
}

// MatchAudioTrack returns the track in tracks closest to wanted.
func MatchAudioTrack(tracks []models.AudioTrack, wanted models.AudioTrack, ordinal int) int {
	return mediatrack.MatchAudioTrack(tracks, wanted, ordinal)
}

// AudioTrackSignatureFromTrack converts a probed audio track into the stable
// signature persisted for series-level sticky audio preferences.
func AudioTrackSignatureFromTrack(track models.AudioTrack) *userstore.AudioTrackSignature {
	return mediatrack.AudioTrackSignatureFromTrack(track)
}

// AtmosAudioTrack reports whether the track's profile or layout names Dolby
// Atmos (JOC is the E-AC-3 carriage).
func AtmosAudioTrack(track models.AudioTrack) bool {
	return mediatrack.AtmosAudioTrack(track)
}

// BrowserSupportsAudioCodec returns true if the given audio codec can be
// played natively by web browsers without transcoding.
func BrowserSupportsAudioCodec(codec string) bool {
	switch strings.ToLower(codec) {
	case "aac", "mp3", "opus", "vorbis", "flac":
		return true
	default:
		return false
	}
}
