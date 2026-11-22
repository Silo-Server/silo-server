// Package mediatrack provides pure audio track selection, matching, and probe
// decodability helpers shared by catalog and playback without pulling in
// FFmpeg, HLS, or session machinery.
package mediatrack

import (
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// OriginalLanguageSentinel is the value stored in audio language preference
// columns to mean "use the media item's original language." It is resolved
// to a concrete language code in the playback handler before reaching
// SelectAudioTrack.
const OriginalLanguageSentinel = "original"

// OriginalLanguageTag is the settings-contract spelling of the same choice for
// playback.audio_language. The contract only stores language tags, so it uses
// the private-use tag catalog.metadata_language already defines for "each
// item's original language".
const OriginalLanguageTag = "x-silo-original"

// IsOriginalLanguagePreference reports whether a stored audio language
// preference means the media item's original language.
func IsOriginalLanguagePreference(preference string) bool {
	preference = strings.TrimSpace(preference)
	return strings.EqualFold(preference, OriginalLanguageSentinel) || strings.EqualFold(preference, OriginalLanguageTag)
}

// AudioTrackPreference holds a per-series audio track preference.
type AudioTrackPreference struct {
	AudioTrackIndex int
	AudioLanguage   string
	TrackSignature  *userstore.AudioTrackSignature
}

// LangMatch accepts compatible languages for previously saved track selections.
func LangMatch(a, b string) bool { return langMatchRank(a, b) >= 0 }

// langMatchRank prefers an exact BCP-47 tag, then a bare language tag, and
// finally another regional/script variant of the same language.
func langMatchRank(candidate, preferred string) int {
	candidate = lang.CompatibleTag(candidate)
	preferred = lang.CompatibleTag(preferred)
	if candidate == "" || preferred == "" {
		return -1
	}
	if candidate == preferred {
		return 0
	}
	candidateBase := lang.PrimaryLanguage(candidate)
	preferredBase := lang.PrimaryLanguage(preferred)
	if candidateBase == "" || candidateBase != preferredBase {
		return -1
	}
	if !strings.Contains(candidate, "-") {
		return 1
	}
	return 2
}

// SelectAudioTrack determines which audio track to use based on preferences.
//
// Priority:
// 1. Series preference exact track signature
// 2. Series preference index (if track exists at that index with matching language)
// 3. Series preference language (best language match)
// 4. Profile preferred language (best language match)
// 5. File's default track (first track with Default: true)
// 6. First track (index 0)
//
// Language matches rank exact tag > bare language > another variant of the same
// language. Track order breaks ties within a language rank. Saved signatures
// and compatible saved indices take precedence over language-only preferences.
//
// Every step considers only tracks FFmpeg can decode (AudioTrackDecodable)
// while the file has one: a server route that maps any other track fails to
// start. When no track is decodable the ordinary choice stands and the planner
// refuses it. The file-level codec is not needed here: it only matters when no
// track names a codec, and then every track gets the same verdict.
func SelectAudioTrack(tracks []models.AudioTrack, preferredLang string, seriesPref *AudioTrackPreference) int {
	var decodable []int
	var candidates []models.AudioTrack
	for i, track := range tracks {
		if AudioTrackDecodable(tracks, i, "") {
			decodable = append(decodable, i)
			candidates = append(candidates, track)
		}
	}
	if len(decodable) == 0 || len(decodable) == len(tracks) {
		return selectAudioTrack(tracks, preferredLang, seriesPref)
	}
	if seriesPref != nil {
		pref := *seriesPref
		pref.AudioTrackIndex = slices.Index(decodable, seriesPref.AudioTrackIndex)
		seriesPref = &pref
	}
	return decodable[selectAudioTrack(candidates, preferredLang, seriesPref)]
}

// SavedAudioTrackChosen reports whether SelectAudioTrack chose the track by
// the viewer's saved series identity (its first two steps: the exact
// signature, or the saved index still in the same language) rather than by
// language or the file's default flag.
func SavedAudioTrackChosen(tracks []models.AudioTrack, pref *AudioTrackPreference, chosen int) bool {
	if pref == nil || chosen < 0 || chosen >= len(tracks) {
		return false
	}
	if findExactAudioTrack(tracks, pref.TrackSignature) == chosen {
		return true
	}
	return pref.AudioTrackIndex == chosen && LangMatch(tracks[chosen].Language, pref.AudioLanguage)
}

// AudioTrackDecodable reports whether FFmpeg can decode tracks[index] of a
// probed file whose file-level audio codec is fileCodec. ffprobe names no codec
// for a stream FFmpeg has no codec ID for, such as AVS3 audio (av3a) in MP4:
// its JSON output omits codec_name, and other writers print "unknown". FFmpeg
// then has no decoder or muxer tag for the stream either, so converting or
// copying it fails before the first segment.
//
// An empty track codec means that only when the probe named another track's
// codec, or named no audio codec for the file at all. A file whose tracks all
// lack a codec while the file names one carries older metadata that never
// recorded per-track codecs; it is not refused for missing metadata.
func AudioTrackDecodable(tracks []models.AudioTrack, index int, fileCodec string) bool {
	switch probedAudioCodec(tracks[index].Codec) {
	case ffprobeUnknownCodec:
		return false
	case "":
		if slices.ContainsFunc(tracks, func(track models.AudioTrack) bool { return probedAudioCodec(track.Codec) != "" }) {
			return false
		}
		codec := probedAudioCodec(fileCodec)
		return codec != "" && codec != ffprobeUnknownCodec
	}
	return true
}

// AnyAudioTrackDecodable reports whether any of a file's audio tracks passes
// AudioTrackDecodable.
func AnyAudioTrackDecodable(tracks []models.AudioTrack, fileCodec string) bool {
	for i := range tracks {
		if AudioTrackDecodable(tracks, i, fileCodec) {
			return true
		}
	}
	return false
}

func probedAudioCodec(codec string) string {
	return strings.ToLower(strings.TrimSpace(codec))
}

// ffprobeUnknownCodec is the codec name ffprobe's text writers print for a
// stream FFmpeg has no codec ID for.
const ffprobeUnknownCodec = "unknown"

func selectAudioTrack(tracks []models.AudioTrack, preferredLang string, seriesPref *AudioTrackPreference) int {
	if len(tracks) == 0 {
		return 0
	}

	if idx := selectSeriesPreferredAudioTrack(tracks, seriesPref); idx >= 0 {
		return idx
	}

	// 4. Profile language preference.
	if preferredLang != "" {
		if idx := bestLanguageTrack(tracks, preferredLang); idx >= 0 {
			return idx
		}
	}

	// 5. File's default track.
	for i, t := range tracks {
		if t.Default {
			return i
		}
	}

	// 6. First track.
	return 0
}

func selectSeriesPreferredAudioTrack(tracks []models.AudioTrack, seriesPref *AudioTrackPreference) int {
	if seriesPref == nil {
		return -1
	}

	// 1. Series preference: try exact signature match first.
	if idx := findExactAudioTrack(tracks, seriesPref.TrackSignature); idx >= 0 {
		return idx
	}

	// 2. Series preference: honor the saved index when its track is still
	// the same language. Saved preferences may carry a bare tag while the
	// scanner now preserves regional subtags, so any compatible match keeps
	// the index rather than falling through to a different track.
	if seriesPref.AudioTrackIndex >= 0 && seriesPref.AudioTrackIndex < len(tracks) &&
		LangMatch(tracks[seriesPref.AudioTrackIndex].Language, seriesPref.AudioLanguage) {
		return seriesPref.AudioTrackIndex
	}

	// 3. Series preference: fall back to language match.
	if seriesPref.AudioLanguage != "" {
		return bestLanguageTrack(tracks, seriesPref.AudioLanguage)
	}
	return -1
}

func bestLanguageTrack(tracks []models.AudioTrack, preferred string) int {
	best, bestRank := -1, 3
	for i, track := range tracks {
		if rank := langMatchRank(track.Language, preferred); rank >= 0 && rank < bestRank {
			best, bestRank = i, rank
		}
	}
	return best
}

// MatchAudioTrackAcrossVersions maps a selection made against one file's
// audio inventory onto another version of the same content. Track ordering,
// codecs and channel layouts are not stable across encodes, so carrying the
// raw ordinal can select a different language: the stable signature wins,
// then the closest track in the selected language (exact tag before another
// regional variant, then the same commentary/main role, title, channels,
// codec and layout), and only then the ordinal or the effective file's
// default track.
func MatchAudioTrackAcrossVersions(
	requestedTracks []models.AudioTrack,
	effectiveTracks []models.AudioTrack,
	requestedIndex int,
) int {
	if len(effectiveTracks) == 0 {
		return 0
	}
	if len(requestedTracks) == 0 {
		return SelectAudioTrack(effectiveTracks, "", nil)
	}
	if requestedIndex < 0 || requestedIndex >= len(requestedTracks) {
		requestedIndex = SelectAudioTrack(requestedTracks, "", nil)
	}
	return MatchAudioTrack(effectiveTracks, requestedTracks[requestedIndex], requestedIndex)
}

// MatchAudioTrack returns the track in tracks closest to wanted, a track the
// viewer selected on another file (at ordinal there). A track FFmpeg cannot
// decode is never chosen while a decodable one exists.
func MatchAudioTrack(tracks []models.AudioTrack, wanted models.AudioTrack, ordinal int) int {
	if len(tracks) == 0 {
		return 0
	}
	anyDecodable := AnyAudioTrackDecodable(tracks, "")
	decodable := func(i int) bool {
		return !anyDecodable || AudioTrackDecodable(tracks, i, "")
	}
	if idx := findExactAudioTrack(tracks, AudioTrackSignatureFromTrack(wanted)); idx >= 0 && decodable(idx) {
		return idx
	}
	wantedUnknown := lang.PrimaryLanguage(wanted.Language) == ""
	best, bestScore := -1, audioMatchScore{}
	for i, track := range tracks {
		if !decodable(i) {
			continue
		}
		score := audioMatchScore{langRank: langMatchRank(track.Language, wanted.Language)}
		if wantedUnknown {
			// An untagged selection can only be recognized among other
			// untagged tracks; a tagged one says nothing about it.
			if lang.PrimaryLanguage(track.Language) != "" {
				continue
			}
			score.langRank = 0
		} else if score.langRank < 0 {
			continue
		}
		score.sameRole = IsCommentaryTrack(track) == IsCommentaryTrack(wanted)
		score.sameTitle = trackStringEqual(track.Title, wanted.Title) && trackStringEqual(track.EmbeddedTitle, wanted.EmbeddedTitle)
		score.sameChannels = track.Channels == wanted.Channels
		score.sameCodec = trackStringEqual(track.Codec, wanted.Codec)
		score.sameLayout = trackStringEqual(track.Layout, wanted.Layout)
		score.sameOrdinal = i == ordinal
		score.isDefault = track.Default
		if best < 0 || score.better(bestScore) {
			best, bestScore = i, score
		}
	}
	if best >= 0 {
		return best
	}
	if wantedUnknown && ordinal >= 0 && ordinal < len(tracks) && decodable(ordinal) {
		return ordinal
	}
	return SelectAudioTrack(tracks, "", nil)
}

type audioMatchScore struct {
	langRank                                                                         int
	sameRole, sameTitle, sameChannels, sameCodec, sameLayout, sameOrdinal, isDefault bool
}

// better orders candidates lexicographically: language closeness first, then
// each trait in turn.
func (s audioMatchScore) better(other audioMatchScore) bool {
	if s.langRank != other.langRank {
		return s.langRank < other.langRank
	}
	traits := [...][2]bool{
		{s.sameRole, other.sameRole},
		{s.sameTitle, other.sameTitle},
		{s.sameChannels, other.sameChannels},
		{s.sameCodec, other.sameCodec},
		{s.sameLayout, other.sameLayout},
		{s.sameOrdinal, other.sameOrdinal},
		{s.isDefault, other.isDefault},
	}
	for _, pair := range traits {
		if pair[0] != pair[1] {
			return pair[0]
		}
	}
	return false
}

// IsCommentaryTrack reports whether the track's title names a commentary track.
func IsCommentaryTrack(track models.AudioTrack) bool {
	return strings.Contains(strings.ToLower(track.Title+" "+track.EmbeddedTitle), "comment")
}

// AtmosAudioTrack reports whether the track's profile or layout names Dolby
// Atmos (JOC is the E-AC-3 carriage).
func AtmosAudioTrack(track models.AudioTrack) bool {
	detail := strings.ToLower(track.Profile + " " + track.Layout)
	return strings.Contains(detail, "atmos") || strings.Contains(detail, "joc")
}
