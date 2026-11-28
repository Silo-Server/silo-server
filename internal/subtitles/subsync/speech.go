package subsync

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// Window plan. Long files are sampled, so a 60 GB remux is not read end to
// end: ffmpeg's input seek reads only each window's stretch of the file.
const (
	windowSeconds = 120
	// sampledWindows are spread over files of at least sampleFromSeconds;
	// shorter files are read whole, window by window.
	sampledWindows    = 12
	sampleFromSeconds = 45 * 60
	// windowTimeoutSeconds bounds one window's decode.
	windowTimeoutSeconds = 300
)

// Artifact cache of speech levels, one row per file and audio plan.
const (
	speechArtifactKind    = "subtitle_speech"
	speechAlgorithm       = 1
	speechPayloadFormat   = "speech-levels:v1"
	speechWindowHeaderLen = 12 // float64 start + uint32 level count
)

// speechPlan is what to decode for one file.
type speechPlan struct {
	AudioStream   int
	CenterChannel bool
	Windows       []mediasample.Window
	Runtime       float64
}

// maxFallbackStreams bounds how many other audio streams a sync tries when
// the planned stream does not match. Each costs a decode of its own (about
// 45 CPU-seconds, once per file and stream, then cached), and only a sync
// that would otherwise end no_match pays it.
const maxFallbackStreams = 3

// planSpeech picks the audio stream and windows for a file. The stream is
// the first decodable one in the subtitle's language, else the file's default
// audio track as playback.SelectAudioTrack chooses it.
func planSpeech(file *models.MediaFile, subtitleLanguage string) (speechPlan, error) {
	return planSpeechStream(file, audioStreamFor(file.AudioTracks, subtitleLanguage))
}

// planSpeechStream plans the windows to decode from one audio stream.
func planSpeechStream(file *models.MediaFile, stream int) (speechPlan, error) {
	runtime := float64(file.Duration)
	if runtime <= 0 {
		return speechPlan{}, errors.New("media file has no known duration")
	}
	plan := speechPlan{AudioStream: stream, Runtime: runtime}
	if stream < len(file.AudioTracks) {
		plan.CenterChannel = hasCenterChannel(file.AudioTracks[stream])
	}
	if runtime < sampleFromSeconds {
		for start := 0.0; start < runtime; start += windowSeconds {
			plan.Windows = append(plan.Windows, mediasample.Window{StartSeconds: start, DurationSeconds: min(windowSeconds, runtime-start)})
		}
		return plan, nil
	}
	for i := range sampledWindows {
		start := (runtime - windowSeconds) * (float64(i) + 0.5) / sampledWindows
		plan.Windows = append(plan.Windows, mediasample.Window{StartSeconds: math.Round(start), DurationSeconds: windowSeconds})
	}
	return plan, nil
}

func audioStreamFor(tracks []models.AudioTrack, language string) int {
	if want, err := subtitles.NormalizeLanguageCode(language); err == nil && want != "" {
		anyDecodable := playback.AnyAudioTrackDecodable(tracks, "")
		for i, t := range tracks {
			if anyDecodable && !playback.AudioTrackDecodable(tracks, i, "") {
				continue
			}
			if got, err := subtitles.NormalizeLanguageCode(t.Language); err == nil && got == want {
				return i
			}
		}
	}
	return playback.SelectAudioTrack(tracks, "", nil)
}

// fallbackAudioStreams lists the decodable audio streams other than used, in
// file order, at most maxFallbackStreams of them. The planned stream can be
// the wrong one to listen to: the default track, or the one tagged with the
// subtitle's language, is often a dub, while a translated subtitle follows
// the original dialog. Untagged tracks give no way to tell them apart.
func fallbackAudioStreams(tracks []models.AudioTrack, used int) []int {
	anyDecodable := playback.AnyAudioTrackDecodable(tracks, "")
	var streams []int
	for i := range tracks {
		if i == used || (anyDecodable && !playback.AudioTrackDecodable(tracks, i, "")) {
			continue
		}
		streams = append(streams, i)
		if len(streams) == maxFallbackStreams {
			break
		}
	}
	return streams
}

// centerLayouts are channel layouts with a front-center channel.
var centerLayouts = []string{"3.0", "3.1", "4.0", "4.1", "5.0", "5.1", "6.0", "6.1", "7.0", "7.1"}

func hasCenterChannel(track models.AudioTrack) bool {
	layout := strings.ToLower(strings.TrimSpace(track.Layout))
	if layout == "" {
		return track.Channels >= 6
	}
	for _, prefix := range centerLayouts {
		if strings.HasPrefix(layout, prefix) {
			return true
		}
	}
	return false
}

func (p speechPlan) requests(input string, background bool) []mediasample.Request {
	reqs := make([]mediasample.Request, len(p.Windows))
	for i, w := range p.Windows {
		window := w
		reqs[i] = mediasample.Request{
			Input:      input,
			Window:     &window,
			Audio:      &mediasample.AudioOutput{Speech: &mediasample.SpeechParams{AudioStream: p.AudioStream, CenterChannel: p.CenterChannel}},
			Attempts:   []mediasample.Attempt{{TimeoutSeconds: windowTimeoutSeconds}},
			Background: background,
		}
	}
	return reqs
}

// key and identity locate the plan's cached levels.
func (p speechPlan) key() mediaartifact.Key {
	params := fmt.Sprintf("stream=%d;center=%t;windows=%d;length=%d;from=%d",
		p.AudioStream, p.CenterChannel, sampledWindows, windowSeconds, sampleFromSeconds)
	return mediaartifact.Key{Kind: speechArtifactKind, AlgorithmVersion: speechAlgorithm, ConfigHash: mediaartifact.ConfigHash(speechArtifactKind, params)}
}

func (p speechPlan) identity(file *models.MediaFile) mediaartifact.Identity {
	return mediaartifact.Identity{FileHash: file.FileHash, FileSize: file.FileSize, DurationSeconds: p.Runtime, WindowEndSeconds: p.Runtime}
}

// encodeSpeech packs windows as [start float64][count uint32][levels]...
func encodeSpeech(windows []mediasample.SpeechLevels) []byte {
	size := 0
	for _, w := range windows {
		size += speechWindowHeaderLen + len(w.Levels)
	}
	out := make([]byte, 0, size)
	for _, w := range windows {
		out = binary.LittleEndian.AppendUint64(out, math.Float64bits(w.StartSeconds))
		out = binary.LittleEndian.AppendUint32(out, uint32(len(w.Levels)))
		out = append(out, w.Levels...)
	}
	return out
}

func decodeSpeech(data []byte) ([]mediasample.SpeechLevels, error) {
	var windows []mediasample.SpeechLevels
	for len(data) > 0 {
		if len(data) < speechWindowHeaderLen {
			return nil, errors.New("truncated speech window header")
		}
		start := math.Float64frombits(binary.LittleEndian.Uint64(data))
		count := int(binary.LittleEndian.Uint32(data[8:]))
		data = data[speechWindowHeaderLen:]
		if count > len(data) {
			return nil, errors.New("truncated speech window levels")
		}
		windows = append(windows, mediasample.SpeechLevels{
			StartSeconds: start, FrameSeconds: mediasample.SpeechFrameSeconds, Levels: data[:count:count],
		})
		data = data[count:]
	}
	return windows, nil
}
