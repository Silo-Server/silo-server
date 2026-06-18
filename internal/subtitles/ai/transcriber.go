package ai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	aitranslate "github.com/Silo-Server/silo-server/internal/ai/translate"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const (
	// Default/maximum audio chunk length sent per transcription request.
	// 10 minutes of 16 kHz mono WAV is ~19 MB — under typical 25 MB API
	// limits. Shorter chunks bound Whisper's within-chunk timestamp drift at
	// the cost of more requests and more boundary word-clips; the operator
	// tunes this via subtitle_ai.asr_chunk_seconds. Live jobs may use smaller
	// chunks through subtitle_ai.live_asr_chunk_seconds to reduce time to first
	// cue; the 15s floor bounds request count and boundary word-clips.
	defaultASRChunkSeconds = 600
	minASRChunkSeconds     = 15
	// Per-chunk request timeout: 3× realtime accommodates local Whisper
	// servers on modest hardware.
	asrChunkTimeoutFactor = 3
	// Cue text wrapping: standard subtitle conventions.
	cueMaxLineLength = 42
	cueMaxLines      = 2
	// Cue timing conventions. When word timings are available a cue closes
	// at a speech pause, at text capacity, at the maximum duration, or after
	// a sentence ends; without them a segment becomes one duration-capped
	// cue. Sub-second cues are stretched to the minimum so short
	// interjections ("What?") stay readable.
	maxCueSeconds        = 7.0
	minCueSeconds        = 1.0
	cueSplitPauseSeconds = 1.0
)

// TranscribeJobRequest is the input to a Transcriber.
type TranscribeJobRequest struct {
	FilePath        string
	AudioTrackIndex int     // resolved 0-based audio stream index
	LanguageHint    string  // ISO 639-1; "" lets the model detect
	StartPosition   float64 // seconds; chunks are processed playhead-first
	DurationSeconds float64 // optional media duration for incremental progress
	ChunkSeconds    int     // optional per-job override; 0 uses configured default
	Incremental     bool    // stream extraction chunks instead of extracting whole file first
}

// Transcriber converts an audio track into subtitle cues. The built-in
// implementation is WhisperTranscriber; the interface is the seam for tests
// and future engines. onChunk, when non-nil, receives each chunk's cues as
// they land (chronological within a chunk, playhead-first across chunks) so
// callers can report progress and stream cues live.
type Transcriber interface {
	Transcribe(ctx context.Context, req TranscribeJobRequest,
		onChunk TranscribeChunkCallback) ([]SubtitleCue, string, error)
}

// TranscribeChunkCallback receives each chunk's cues, the detected language for
// that chunk when the ASR endpoint reports one, and chunk progress. A total of
// 0 means the total is unknown/indeterminate.
type TranscribeChunkCallback func(cues []SubtitleCue, language string, done, total int)

// audioTranscriber is the slice of llm.Client the transcriber needs.
type audioTranscriber interface {
	Transcribe(ctx context.Context, req llm.TranscribeRequest) (*llm.Transcription, error)
}

// WhisperTranscriber generates subtitles from audio via an OpenAI-compatible
// transcription endpoint: extract the track to 16 kHz mono WAV chunks, send
// each chunk for verbose_json transcription, offset the segment timestamps by
// the chunk start, and build wrapped cues. Chunk boundaries are fixed-length;
// a word straddling a boundary can be clipped — accepted v1 limitation, noted
// for a silence-aligned follow-up.
type WhisperTranscriber struct {
	client audioTranscriber
	// ffmpegPath/chunkSeconds are atomics so admin settings changes apply to
	// jobs started afterwards; each job snapshots them once at start.
	ffmpegPath   atomic.Pointer[string]
	chunkSeconds atomic.Int32
	// extract and probeOffset are playback helpers, injectable for tests.
	extract            func(ctx context.Context, filePath string, audioTrackIndex int, dir, ffmpegPath string, chunkSeconds int) ([]playback.AudioChunk, error)
	incrementalExtract func(ctx context.Context, filePath string, audioTrackIndex int, dir, ffmpegPath string, startSec float64, chunkSeconds int, onSegment func(playback.AudioChunk) error) error
	probeOffset        func(ctx context.Context, filePath string, audioTrackIndex int, ffmpegPath string) float64
}

// NewWhisperTranscriber builds a transcriber backed by the shared AI client.
// chunkSeconds outside [minASRChunkSeconds, defaultASRChunkSeconds] clamps
// to the nearest bound (longer chunks would exceed upload limits).
func NewWhisperTranscriber(client *llm.Client, ffmpegPath string, chunkSeconds int) *WhisperTranscriber {
	t := &WhisperTranscriber{
		client:             client,
		extract:            playback.ExtractAudioChunks,
		incrementalExtract: playback.ExtractAudioChunksFrom,
		probeOffset:        playback.ProbeAudioStartOffset,
	}
	t.SetExtraction(ffmpegPath, chunkSeconds)
	return t
}

// SetExtraction updates the ffmpeg path and ASR chunk duration used by jobs
// started afterwards. Safe for concurrent use; out-of-range chunk durations
// clamp to the nearest supported bound, matching per-request overrides.
func (t *WhisperTranscriber) SetExtraction(ffmpegPath string, chunkSeconds int) {
	t.ffmpegPath.Store(&ffmpegPath)
	t.chunkSeconds.Store(int32(clampASRChunkSeconds(chunkSeconds)))
}

// Transcribe implements Transcriber. The returned cues are NOT sorted (they
// arrive playhead-first across chunks); the detected language is the
// endpoint's report for the first processed chunk, normalized to an ISO code
// where possible.
func (t *WhisperTranscriber) Transcribe(ctx context.Context, req TranscribeJobRequest,
	onChunk TranscribeChunkCallback) ([]SubtitleCue, string, error) {
	dir, err := os.MkdirTemp("", "silo-asr-*")
	if err != nil {
		return nil, "", fmt.Errorf("create ASR temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// Snapshot once so the whole job extracts and times chunks consistently
	// even if the config reloads mid-job.
	ffmpegPath := *t.ffmpegPath.Load()
	chunkSeconds := int(t.chunkSeconds.Load())
	if req.ChunkSeconds > 0 {
		chunkSeconds = req.ChunkSeconds
	}
	chunkSeconds = clampASRChunkSeconds(chunkSeconds)

	// Audio streams can start after the container's timeline origin (TS
	// remuxes especially); Whisper times are relative to the first audio
	// sample, so the delta is a constant sync error unless added back.
	startOffset := t.probeOffset(ctx, req.FilePath, req.AudioTrackIndex, ffmpegPath)

	if req.Incremental {
		return t.transcribeIncremental(ctx, req, dir, ffmpegPath, chunkSeconds, startOffset, onChunk)
	}

	chunks, err := t.extract(ctx, req.FilePath, req.AudioTrackIndex, dir, ffmpegPath, chunkSeconds)
	if err != nil {
		return nil, "", err
	}

	order := chunkOrderForPosition(chunks, req.StartPosition)
	timeout := time.Duration(chunkSeconds*asrChunkTimeoutFactor) * time.Second

	var all []SubtitleCue
	detected := ""
	for done, idx := range order {
		cues, lang, err := t.transcribeChunk(ctx, chunks[idx], req.LanguageHint, timeout, startOffset)
		if err != nil {
			return nil, "", fmt.Errorf("transcribe chunk %d/%d: %w", idx+1, len(chunks), err)
		}
		if detected == "" {
			detected = lang
		}
		all = append(all, cues...)
		if onChunk != nil {
			onChunk(cues, lang, done+1, len(order))
		}
	}

	if len(all) == 0 {
		return nil, detected, fmt.Errorf("no speech recognized in the audio track")
	}
	return all, detected, nil
}

var errStopIncrementalPass = errors.New("stop incremental ASR pass")

func (t *WhisperTranscriber) transcribeIncremental(
	ctx context.Context,
	req TranscribeJobRequest,
	dir, ffmpegPath string,
	chunkSeconds int,
	startOffset float64,
	onChunk TranscribeChunkCallback,
) ([]SubtitleCue, string, error) {
	timeout := time.Duration(chunkSeconds*asrChunkTimeoutFactor) * time.Second
	pivot := incrementalPivotStart(req.StartPosition, req.DurationSeconds, chunkSeconds)
	total := estimatedChunkTotal(req.DurationSeconds, chunkSeconds)

	var all []SubtitleCue
	detected := ""
	done := 0
	process := func(chunk playback.AudioChunk, chunkOffset float64) error {
		cues, lang, err := t.transcribeChunk(ctx, chunk, req.LanguageHint, timeout, chunkOffset)
		if err != nil {
			return fmt.Errorf("transcribe chunk at %.3fs: %w", chunk.Start, err)
		}
		if detected == "" {
			detected = lang
		}
		all = append(all, cues...)
		done++
		if onChunk != nil {
			onChunk(cues, lang, done, progressTotal(done, total))
		}
		return nil
	}

	firstPassDone := done
	firstPassOffset := audioOffsetForIncrementalPass(pivot, startOffset)
	if err := t.runIncrementalPass(ctx, req, dir, ffmpegPath, pivot, chunkSeconds, func(chunk playback.AudioChunk) error {
		return process(chunk, firstPassOffset)
	}); err != nil {
		if ctx.Err() != nil || pivot == 0 || done != firstPassDone {
			return nil, "", err
		}
	}
	if pivot > 0 {
		wrap := func(chunk playback.AudioChunk) error {
			if chunk.Start >= pivot {
				return errStopIncrementalPass
			}
			return process(chunk, startOffset)
		}
		if err := t.runIncrementalPass(ctx, req, dir, ffmpegPath, 0, chunkSeconds, wrap); err != nil {
			if !errors.Is(err, errStopIncrementalPass) {
				return nil, "", err
			}
		}
	}

	if len(all) == 0 {
		return nil, detected, fmt.Errorf("no speech recognized in the audio track")
	}
	return all, detected, nil
}

func (t *WhisperTranscriber) runIncrementalPass(
	ctx context.Context,
	req TranscribeJobRequest,
	dir, ffmpegPath string,
	startSec float64,
	chunkSeconds int,
	onSegment func(playback.AudioChunk) error,
) error {
	passDir := filepath.Join(dir, fmt.Sprintf("from-%.3f", startSec))
	if err := os.MkdirAll(passDir, 0o755); err != nil {
		return fmt.Errorf("create ASR incremental temp dir: %w", err)
	}
	return t.incrementalExtract(ctx, req.FilePath, req.AudioTrackIndex, passDir, ffmpegPath, startSec, chunkSeconds, onSegment)
}

func (t *WhisperTranscriber) transcribeChunk(
	ctx context.Context,
	chunk playback.AudioChunk,
	languageHint string,
	timeout time.Duration,
	startOffset float64,
) ([]SubtitleCue, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(chunk.Path)
	if err != nil {
		return nil, "", fmt.Errorf("read audio chunk: %w", err)
	}
	tr, err := t.client.Transcribe(ctx, llm.TranscribeRequest{
		Filename: filepath.Base(chunk.Path),
		Audio:    data,
		Language: languageHint,
		Timeout:  timeout,
	})
	if err != nil {
		return nil, "", err
	}
	// Each chunk is read exactly once; deleting it as we go caps disk usage
	// at one extraction rather than extraction + retranscription leftovers.
	_ = os.Remove(chunk.Path)

	return cuesFromSegments(tr.Segments, chunk.Start+startOffset), normalizeDetectedLanguage(tr.Language), nil
}

func clampASRChunkSeconds(chunkSeconds int) int {
	switch {
	case chunkSeconds < minASRChunkSeconds:
		return minASRChunkSeconds
	case chunkSeconds > defaultASRChunkSeconds:
		return defaultASRChunkSeconds
	default:
		return chunkSeconds
	}
}

func incrementalPivotStart(startSeconds, durationSeconds float64, chunkSeconds int) float64 {
	if startSeconds <= 0 || chunkSeconds <= 0 {
		return 0
	}
	if durationSeconds > 0 && startSeconds >= durationSeconds {
		startSeconds = math.Max(0, durationSeconds-0.001)
	}
	pivot := math.Floor(startSeconds/float64(chunkSeconds)) * float64(chunkSeconds)
	if pivot < 0 {
		return 0
	}
	return pivot
}

func estimatedChunkTotal(durationSeconds float64, chunkSeconds int) int {
	if durationSeconds <= 0 || chunkSeconds <= 0 {
		return 0
	}
	return int(math.Ceil(durationSeconds / float64(chunkSeconds)))
}

func progressTotal(done, total int) int {
	if total <= 0 {
		return 0
	}
	if total < done {
		return done
	}
	return total
}

func audioOffsetForIncrementalPass(passStartSec, audioStartOffset float64) float64 {
	if passStartSec <= 0 {
		return audioStartOffset
	}
	if audioStartOffset <= passStartSec {
		return 0
	}
	return audioStartOffset - passStartSec
}

// chunkOrderForPosition orders chunk indexes so the chunk containing
// startSeconds is processed first, then forward, then wrapping to the start —
// the viewer's current region fills first, mirroring translation's
// playhead-first cue order.
func chunkOrderForPosition(chunks []playback.AudioChunk, startSeconds float64) []int {
	n := len(chunks)
	pivot := 0
	if startSeconds > 0 {
		for i := n - 1; i >= 0; i-- {
			if chunks[i].Start <= startSeconds {
				pivot = i
				break
			}
		}
	}
	order := make([]int, 0, n)
	for i := pivot; i < n; i++ {
		order = append(order, i)
	}
	for i := 0; i < pivot; i++ {
		order = append(order, i)
	}
	return order
}

// cuesFromSegments converts transcription segments (timestamps relative to
// their chunk) to absolute-time cues, dropping speech-free segments and
// wrapping text to subtitle conventions. Segments with word timings are
// re-split into readable cues that end when speech stops; segments without
// them become single cues capped at the maximum duration, because Whisper
// segment end times otherwise stretch across silence to the next segment.
func cuesFromSegments(segments []llm.TranscriptionSegment, offsetSeconds float64) []SubtitleCue {
	var out []SubtitleCue
	for _, seg := range segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		if cues := cuesFromWords(seg.Words, offsetSeconds); len(cues) > 0 {
			out = append(out, cues...)
			continue
		}
		start := offsetSeconds + seg.Start
		end := offsetSeconds + seg.End
		if maxEnd := start + maxCueSeconds; end > maxEnd {
			end = maxEnd
		}
		out = append(out, newCue(start, end, text))
	}
	enforceMinCueDurations(out)
	return out
}

// cuesFromWords groups word timings into cues: a cue closes at a speech
// pause, when the text would exceed the wrap capacity, when it would exceed
// the maximum duration, or after a sentence ends once a full line is
// accumulated. Cue end times come from the last word, so cues disappear when
// speech stops instead of lingering through silence.
func cuesFromWords(words []llm.TranscriptionWord, offsetSeconds float64) []SubtitleCue {
	maxRunes := cueMaxLineLength * cueMaxLines
	var out []SubtitleCue
	var texts []string
	var start, end float64
	runes := 0
	flush := func() {
		if len(texts) > 0 {
			out = append(out, newCue(offsetSeconds+start, offsetSeconds+end, strings.Join(texts, " ")))
		}
		texts, runes = nil, 0
	}
	for _, w := range words {
		text := strings.TrimSpace(w.Text)
		if text == "" {
			continue
		}
		n := utf8.RuneCountInString(text)
		if len(texts) > 0 &&
			(w.Start-end >= cueSplitPauseSeconds || runes+1+n > maxRunes || w.End-start > maxCueSeconds) {
			flush()
		}
		if len(texts) == 0 {
			start = w.Start
		}
		texts = append(texts, text)
		runes += n + 1
		end = w.End
		if runes >= cueMaxLineLength && endsSentence(text) {
			flush()
		}
	}
	flush()
	return out
}

// endsSentence reports whether a word closes a sentence, tolerating a
// trailing quote or bracket after the punctuation.
func endsSentence(word string) bool {
	word = strings.TrimRight(word, `"')]”’`)
	return strings.HasSuffix(word, ".") || strings.HasSuffix(word, "?") ||
		strings.HasSuffix(word, "!") || strings.HasSuffix(word, "…")
}

// newCue builds a wrapped cue, guarding degenerate timestamps with a minimal
// visible duration.
func newCue(startSec, endSec float64, text string) SubtitleCue {
	if endSec <= startSec {
		endSec = startSec + 0.5
	}
	return SubtitleCue{
		Start: time.Duration(startSec * float64(time.Second)),
		End:   time.Duration(endSec * float64(time.Second)),
		Lines: wrapCueText(text, cueMaxLineLength, cueMaxLines),
	}
}

// enforceMinCueDurations stretches sub-minimum cues (word-accurate timing can
// produce a 0.2s "What?") up to the readable minimum, without overlapping the
// next cue. Cues are chronological within a chunk.
func enforceMinCueDurations(cues []SubtitleCue) {
	minDur := time.Duration(minCueSeconds * float64(time.Second))
	for i := range cues {
		minEnd := cues[i].Start + minDur
		if i+1 < len(cues) && minEnd > cues[i+1].Start {
			minEnd = cues[i+1].Start
		}
		if cues[i].End < minEnd {
			cues[i].End = minEnd
		}
	}
}

// wrapCueText greedily wraps text into at most maxLines lines of roughly
// maxLen characters (counted in runes, so multi-byte scripts like Arabic or
// Cyrillic wrap at the same visual width as Latin). The last line absorbs any
// overflow — text is never dropped.
func wrapCueText(text string, maxLen, maxLines int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	lines := []string{words[0]}
	for _, w := range words[1:] {
		last := len(lines) - 1
		switch {
		case utf8.RuneCountInString(lines[last])+1+utf8.RuneCountInString(w) <= maxLen:
			lines[last] += " " + w
		case len(lines) < maxLines:
			lines = append(lines, w)
		default:
			lines[last] += " " + w
		}
	}
	return lines
}

// normalizeDetectedLanguage maps a Whisper-reported language to an ISO 639-1
// code: endpoints variously report codes ("en") or English names ("english").
// Returns "" when it cannot be normalized.
func normalizeDetectedLanguage(reported string) string {
	reported = strings.TrimSpace(reported)
	if reported == "" {
		return ""
	}
	if code, err := subtitles.NormalizeLanguageCode(reported); err == nil {
		return code
	}
	return aitranslate.LanguageCodeFromName(reported)
}
