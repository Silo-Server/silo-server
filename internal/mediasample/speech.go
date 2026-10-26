package mediasample

import (
	"encoding/binary"
	"math"
)

// Speech output: the loudness of the speech band, frame by frame, for
// subtitle alignment. ffmpeg band-limits one audio stream to 200-3400 Hz,
// resamples it to 8 kHz mono, and writes raw 16-bit samples to stdout; the
// runner reduces them to one level per 10 ms frame while they stream, so a
// long window never sits in memory as PCM.
const (
	speechSampleRate = 8000
	// SpeechFrameSeconds is the duration of one level.
	SpeechFrameSeconds = 0.01
	speechFrameSamples = speechSampleRate / 100
	// speechFloorDB is the level a frame of digital silence reports; levels
	// count whole dB above it.
	speechFloorDB = -100
	// maxAudioStream bounds SpeechParams.AudioStream.
	maxAudioStream = 64
	// maxSpeechWindowSeconds bounds a speech window, and with it the levels
	// held in memory (one byte per 10 ms).
	maxSpeechWindowSeconds = 4 * 60 * 60
)

// SpeechParams configures the speech output.
type SpeechParams struct {
	// AudioStream is the ordinal of the audio stream to read (ffmpeg's
	// 0:a:N). Validate accepts 0..64.
	AudioStream int `json:"audio_stream"`
	// CenterChannel reads only the front-center channel, which carries the
	// dialog of 5.1 and 7.1 mixes. The caller sets it from the stream's
	// probed layout; a stream without a center channel fails the run.
	CenterChannel bool `json:"center_channel,omitempty"`
}

// SpeechLevels is the speech output of a window.
type SpeechLevels struct {
	// StartSeconds is the media time of the first level: the window start.
	// Audio that begins later than the window, such as a delayed track,
	// reports the gap as silence, so level i always covers
	// StartSeconds + i*FrameSeconds.
	StartSeconds float64 `json:"start_seconds"`
	FrameSeconds float64 `json:"frame_seconds"`
	// Levels holds each frame's speech-band energy in whole dB above
	// -100 dBFS: 0 is silence, 100 is a full-scale signal.
	Levels []byte `json:"levels"`
}

func (p SpeechParams) validate() error {
	if p.AudioStream < 0 || p.AudioStream > maxAudioStream {
		return errAudioStreamRange
	}
	return nil
}

// speechFilter is the audio chain of a speech run. first_pts=0 makes
// aresample pad a stream that starts after the window start, keeping samples
// on the media clock.
func (p SpeechParams) filter() string {
	chain := ""
	if p.CenterChannel {
		chain = "pan=mono|c0=FC,"
	}
	return chain + "highpass=f=200,lowpass=f=3400,aresample=8000:async=1:first_pts=0"
}

// speechFilters are the filters a speech run needs from ffmpeg.
func (p SpeechParams) filters() []string {
	filters := []string{"highpass", "lowpass", "aresample"}
	if p.CenterChannel {
		filters = append(filters, "pan")
	}
	return filters
}

// speechWriter receives a speech run's raw s16le mono samples and reduces
// them to frame levels as they arrive.
type speechWriter struct {
	levels  []byte
	partial []byte
	sum     float64
	count   int
}

func newSpeechWriter(durationSeconds float64) *speechWriter {
	frames := int(math.Ceil(durationSeconds/SpeechFrameSeconds)) + 1
	return &speechWriter{levels: make([]byte, 0, max(frames, 0))}
}

func (w *speechWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(w.partial) == 1 && len(p) > 0 {
		w.add(int16(binary.LittleEndian.Uint16([]byte{w.partial[0], p[0]})))
		w.partial = w.partial[:0]
		p = p[1:]
	}
	for len(p) >= 2 {
		w.add(int16(binary.LittleEndian.Uint16(p)))
		p = p[2:]
	}
	if len(p) == 1 {
		w.partial = append(w.partial[:0], p[0])
	}
	return n, nil
}

func (w *speechWriter) add(sample int16) {
	v := float64(sample)
	w.sum += v * v
	w.count++
	if w.count == speechFrameSamples {
		w.flush()
	}
}

func (w *speechWriter) flush() {
	if w.count == 0 {
		return
	}
	w.levels = append(w.levels, speechLevel(w.sum/float64(w.count)))
	w.sum, w.count = 0, 0
}

// result closes the last partial frame and returns the levels.
func (w *speechWriter) result(start float64) *SpeechLevels {
	w.flush()
	return &SpeechLevels{StartSeconds: start, FrameSeconds: SpeechFrameSeconds, Levels: w.levels}
}

// speechLevel converts a frame's mean square sample to its level.
func speechLevel(meanSquare float64) byte {
	const fullScale = 32768.0 * 32768.0
	if meanSquare <= 0 {
		return 0
	}
	db := 10*math.Log10(meanSquare/fullScale) - speechFloorDB
	return byte(math.Round(min(max(db, 0), -speechFloorDB)))
}
