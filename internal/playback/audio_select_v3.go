package playback

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
)

// StartAudioOutcomeV3 is what the plan for one audio track does with it.
type StartAudioOutcomeV3 struct {
	// Playable reports that the track plans at all.
	Playable bool
	// Converted reports that the route re-encodes the audio: an AAC
	// conversion on a remux, a device quirk's recipe, or a video transcode.
	Converted bool
	// Channels is the channel count the client receives.
	Channels int
}

// StartAudioOutcomeFromResultV3 reads a planner result as a StartAudioOutcomeV3.
func StartAudioOutcomeFromResultV3(result PlannerResultV3) StartAudioOutcomeV3 {
	if result.Terminal != nil || result.Plan == nil {
		return StartAudioOutcomeV3{}
	}
	outcome := StartAudioOutcomeV3{Playable: true, Converted: result.TranscodeAudio}
	for _, transformation := range result.Plan.Transformations {
		if transformation.Name == TransformationAudioToAACV3 {
			outcome.Converted = true
		}
	}
	if channels := result.Plan.EffectiveRecipe.AudioChannels; channels != nil {
		outcome.Channels = *channels
	}
	return outcome
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
	return pref.AudioTrackIndex == chosen && langMatch(tracks[chosen].Language, pref.AudioLanguage)
}

// PreferUnconvertedAudioTrackV3 refines a start's preference-based audio
// choice by what the request can play. outcome plans the file with one track
// and reports what the route does with it. When the chosen track is converted
// or does not plan, another main track in the same language that plays
// unconverted with at least the channels the chosen track reaches the client
// with plays instead: a Roku without TrueHD gets a file's AC-3 or E-AC-3 5.1
// track rather than a stereo AAC conversion of its TrueHD default, while a
// client that plays TrueHD keeps it, and a stereo track never replaces a 5.1
// conversion. Among several candidates the most channels win, then Atmos,
// then the higher codec rank, then track order. Commentary and
// audio-description tracks are never substituted in.
//
// Planning is the authority, so device quirks, container support, and video
// routes that re-encode every track all count.
func PreferUnconvertedAudioTrackV3(file *models.MediaFile, chosen int, outcome func(index int) StartAudioOutcomeV3) int {
	if file == nil || chosen < 0 || chosen >= len(file.AudioTracks) {
		return chosen
	}
	tracks := file.AudioTracks
	base := outcome(chosen)
	if base.Playable && !base.Converted {
		return chosen
	}
	best := -1
	var bestOutcome StartAudioOutcomeV3
	for i, track := range tracks {
		if i == chosen || !mainAudioTrack(track) || !sameAudioLanguage(track.Language, tracks[chosen].Language) ||
			!AudioTrackDecodable(tracks, i, file.CodecAudio) {
			continue
		}
		candidate := outcome(i)
		if !candidate.Playable || candidate.Converted || candidate.Channels < base.Channels {
			continue
		}
		if best < 0 || betterUnconvertedAudioTrack(candidate, track, bestOutcome, tracks[best]) {
			best, bestOutcome = i, candidate
		}
	}
	if best < 0 {
		return chosen
	}
	return best
}

// mainAudioTrack excludes commentary and audio-description tracks, which a
// viewer never wants in place of the main mix.
func mainAudioTrack(track models.AudioTrack) bool {
	title := strings.ToLower(track.Title + " " + track.EmbeddedTitle)
	return !isCommentaryTrack(track) && !strings.Contains(title, "descripti")
}

// sameAudioLanguage treats two unlabeled tracks as the same language, and
// otherwise accepts any compatible tag.
func sameAudioLanguage(a, b string) bool {
	if lang.CompatibleTag(a) == lang.CompatibleTag(b) {
		return true
	}
	return langMatch(a, b)
}

func betterUnconvertedAudioTrack(candidate StartAudioOutcomeV3, track models.AudioTrack, current StartAudioOutcomeV3, currentTrack models.AudioTrack) bool {
	if candidate.Channels != current.Channels {
		return candidate.Channels > current.Channels
	}
	if a, b := AtmosAudioTrack(track), AtmosAudioTrack(currentTrack); a != b {
		return a
	}
	return audioCodecQualityRank(track.Codec) > audioCodecQualityRank(currentTrack.Codec)
}

// AtmosAudioTrack reports whether the track's profile or layout names Dolby
// Atmos (JOC is the E-AC-3 carriage).
func AtmosAudioTrack(track models.AudioTrack) bool {
	detail := strings.ToLower(track.Profile + " " + track.Layout)
	return strings.Contains(detail, "atmos") || strings.Contains(detail, "joc")
}

// audioCodecQualityRanks breaks a channel and Atmos tie between unconverted
// tracks: lossless, then DTS, E-AC-3, AC-3, and AAC.
var audioCodecQualityRanks = map[string]int{
	audioCodecTrueHD: 5, audioCodecFLAC: 5, audioCodecALAC: 5, "pcm_s16le": 5, "pcm_s24le": 5,
	audioCodecDTS: 4, audioCodecEAC3: 3, audioCodecAC3: 2, audioCodecAACV3: 1,
}

func audioCodecQualityRank(codec string) int {
	return audioCodecQualityRanks[normalizeCodecV3(codec)]
}
