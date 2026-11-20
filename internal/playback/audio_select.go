package playback

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

// langMatch accepts compatible languages for previously saved track selections,
// rejecting explicitly conflicting scripts.
func langMatch(a, b string) bool {
	rank := langMatchRank(a, b)
	return rank >= 0 && rank < lang.RankScriptConflict
}

// trackLanguageRank returns the best language rank for a track against the
// preferred language, considering both the primary code and the MULTi
// language list. -1 when nothing matches.
//
// Bare MULTI/DUAL membership sentinels carry no language information: they
// are skipped before matching so they never boost a track above real language
// evidence, but the track still falls through to the default/first-track
// selection below (neutral, not disqualifying).
func trackLanguageRank(track models.AudioTrack, preferred string) int {
	best := -1
	if rank := rankedLanguageMatch(track.Language, preferred); rank >= 0 {
		best = rank
		if best == 0 {
			return best
		}
	}
	for _, code := range track.Languages {
		if rank := rankedLanguageMatch(code, preferred); rank >= 0 && (best < 0 || rank < best) {
			best = rank
			if best == 0 {
				break
			}
		}
	}
	return best
}

// rankedLanguageMatch ranks one language token against the preference, or -1
// when there is no match. Tokens that only declare MULTI/DUAL/unknown
// membership are neutral: never a match (no -1 from the membership test
// unless every token is neutral), and never a positive rank that would push
// the track above real language evidence in bestLanguageTrack.
func rankedLanguageMatch(candidate, preferred string) int {
	if unknownLanguageMembership(candidate) {
		return -1
	}
	return langMatchRank(candidate, preferred)
}

// Language sentinels that carry no concrete language evidence. Release and
// intake vocabulary uses these to mean "several" or "not stated", which must
// stay neutral in preference matching rather than rank as a match.
const (
	langUndetermined = "und"
	langMulti        = "mul"

	langLongMulti     = "multi"
	langLongMultiple  = "multiple"
	langLongDual      = "dual"
	langLongUnknown   = "unknown"
	langLongUndefined = "undefined"
)

// unknownLanguageMembership reports whether a language token declares only
// MULTI/DUAL/undetermined membership (or nothing at all) instead of a
// concrete language. Such tokens are release/intake vocabulary, not language
// evidence, and must stay neutral in language preference matching.
func unknownLanguageMembership(token string) bool {
	switch lang.Canonical(strings.TrimSpace(token)) {
	case "", langUndetermined, langMulti:
		return true
	}
	switch strings.ToLower(strings.TrimSpace(token)) {
	case langLongMulti, langLongMultiple, langLongDual, langLongUnknown, langLongUndefined:
		return true
	}
	return false
}

// trackHasLanguage reports whether the track carries the preferred language,
// either as its primary code or anywhere in its MULTi language list,
// without an explicit script conflict. A bare MULTI/DUAL primary with no
// member list is not a match: unknown membership stays neutral. It is the
// shared membership authority for reconciliations and cross-version remaps;
// SelectAudioTrack keeps its own ranking rules on top of this predicate.
func trackHasLanguage(track models.AudioTrack, preferred string) bool {
	rank := trackLanguageRank(track, preferred)
	return rank >= 0 && rank < lang.RankScriptConflict
}

// TrackCarriesLanguage is the exported membership authority: it reports
// whether the track carries the preferred language via its primary code or
// its MULTi member list. Bare MULTI/DUAL tokens (unknown membership) never
// count as a concrete match but do not fail closed either.
func TrackCarriesLanguage(track models.AudioTrack, preferred string) bool {
	return trackHasLanguage(track, preferred)
}

// langMatchRank delegates to lang.MatchRank to rank language closeness:
// exact (0) > matching script/region (1-2) > bare tag (3) >
// regional variant (4) > conflicting script (5).
func langMatchRank(candidate, preferred string) int {
	return lang.MatchRank(candidate, preferred)
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
// Language matches rank exact tag > matching script/region > bare language >
// another variant of the same language > conflicting script. Track order breaks
// ties within a language rank. Saved signatures and compatible saved indices
// take precedence over language-only preferences.
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

// AudioCodecVerdictSupported reports whether f carries measured audio codec
// evidence, i.e. whether a verdict about the file's audio codec may be
// published at all.
//
// AudioTrackDecodable answers false for two different situations: a probe named
// a codec FFmpeg has no decoder for (measured, and the file really cannot
// play), and a row no probe has measured whose tracks and file-level codec are
// all unnamed (unmeasured — a provider listing declares languages and channel
// counts, not codec ids). Only the first is a codec incompatibility. The
// second is an absence of evidence, and publishing it as
// audio_conversion_unsupported told a viewer a file that plays fine "can't be
// converted" with no retry offered, after a delivery timeout that never
// measured its audio at all.
//
// ProbeUpdatedAt is the model's own record that a probe ran and wrote this
// row's stream table, so it is the one field that separates a measured row from
// a declared one. A row with named codecs never reaches this question:
// AudioTrackDecodable answers true for it.
func AudioCodecVerdictSupported(f *models.MediaFile) bool {
	if f == nil {
		return false
	}
	return f.ProbeUpdatedAt != nil
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

	// 1. Series preference: try exact signature match first.
	if seriesPref != nil {
		if idx := findExactAudioTrack(tracks, seriesPref.TrackSignature); idx >= 0 {
			return idx
		}

		// 2. Series preference: honor the saved index when its track is still
		// the same language. Saved preferences may carry a bare tag while the
		// scanner now preserves regional subtags, so any compatible match keeps
		// the index rather than falling through to a different track.
		if seriesPref.AudioTrackIndex >= 0 && seriesPref.AudioTrackIndex < len(tracks) {
			if trackHasLanguage(tracks[seriesPref.AudioTrackIndex], seriesPref.AudioLanguage) {
				return seriesPref.AudioTrackIndex
			}
		}

		// 3. Series preference: fall back to language match.
		if seriesPref.AudioLanguage != "" {
			if idx := bestLanguageTrack(tracks, seriesPref.AudioLanguage); idx >= 0 {
				return idx
			}
		}
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

func bestLanguageTrack(tracks []models.AudioTrack, preferred string) int {
	best, bestRank := -1, lang.RankScriptConflict+1
	for i, track := range tracks {
		if rank := trackLanguageRank(track, preferred); rank >= 0 && rank < bestRank {
			best, bestRank = i, rank
			if bestRank == 0 {
				break
			}
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
	selected := requestedTracks[requestedIndex]
	signature := AudioTrackSignatureFromTrack(selected)
	if idx := findExactAudioTrack(effectiveTracks, signature); idx >= 0 {
		return idx
	}
	for _, code := range crossVersionAudioLanguages(selected) {
		wanted := selected
		wanted.Language = code
		candidate := MatchAudioTrack(effectiveTracks, wanted, requestedIndex)
		if trackHasLanguage(effectiveTracks[candidate], code) {
			return candidate
		}
	}
	return MatchAudioTrack(effectiveTracks, selected, requestedIndex)
}

// crossVersionAudioLanguages returns the concrete languages a track carries,
// primary code first, then its MULTi language list, deduplicated by canonical
// tag form. The "und"/"mul" sentinels are placeholders and are skipped.
func crossVersionAudioLanguages(track models.AudioTrack) []string {
	codes := make([]string, 0, len(track.Languages)+1)
	if primary := track.Language; !unknownLanguageMembership(primary) {
		codes = append(codes, primary)
	}
	for _, code := range track.Languages {
		if unknownLanguageMembership(code) {
			continue
		}
		duplicate := false
		for _, existing := range codes {
			if lang.CompatibleTag(existing) == lang.CompatibleTag(code) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			codes = append(codes, code)
		}
	}
	return codes
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
		score.sameRole = isCommentaryTrack(track) == isCommentaryTrack(wanted)
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

func isCommentaryTrack(track models.AudioTrack) bool {
	return strings.Contains(strings.ToLower(track.Title+" "+track.EmbeddedTitle), "comment")
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
