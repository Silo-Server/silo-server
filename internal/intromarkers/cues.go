package intromarkers

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// A file's subtitle cues, as hints for the markers its audio and picture
// place.
//
// Two things in a subtitle are evidence. A "Previously On" cue says where the
// recap is, and the recap ends where an intro may start. Dialog stops when
// the credits roll, so the last cue before the credits says where they begin —
// on the files TheIntroDB reports as starting late, that cue ends at the black
// screen and the placement is what was late.
//
// The same reading of cues is deliberately not used to move an intro's start:
// the Chromaprint path tried it (`chromaprint:dialog:v4`) and moved the start
// a median 13 to 15 seconds later, agreeing with TheIntroDB and Plex far less
// often, because most cues in an opening are the opening's own lyrics or a
// title-sequence narration. Only the recap text is used, which is what it says
// it is.
const (
	// ArtifactKindIntroCues is a cached set of a file's subtitle cues.
	ArtifactKindIntroCues = "intro_cues"
	// CuesFormat is the payload of an intro cues artifact.
	CuesFormat = "cues:v1"
	// cuesPreferredLanguages are tried in order when a file has several text
	// tracks; the first is the one its hint text is expected in.
	cuesPreferredLanguages = "eng,en"
	// cueExtractionParams shape a cues payload. Changing them discards every
	// cached cue list.
	cueExtractionParams = "lang=" + cuesPreferredLanguages + ";format=srt;whole-file"
)

// cuesDetailNoTrack marks a file with no text subtitle track to read.
const cuesDetailNoTrack = "no_text_track"

// subtitleCue is one cue's bounds and text.
type subtitleCue struct {
	Start float64
	End   float64
	Text  string
}

// CueSource reads a file's subtitle cues.
type CueSource interface {
	// FileCues returns the cues of a file's English text track, and whether
	// it has one to read.
	FileCues(ctx context.Context, candidate Candidate) ([]subtitleCue, bool, error)
}

// SubtitleCueExtractor reads cues from a file's own text subtitle tracks.
type SubtitleCueExtractor struct {
	config Config
}

func NewSubtitleCueExtractor(config Config) *SubtitleCueExtractor {
	return &SubtitleCueExtractor{config: config.normalized()}
}

// introCuesKey keys a file's cached cues.
func introCuesKey() mediaartifact.Key {
	return mediaartifact.Key{
		Kind:             ArtifactKindIntroCues,
		AlgorithmVersion: AlgorithmVersion,
		ConfigHash:       mediaartifact.ConfigHash(ArtifactKindIntroCues, cueExtractionParams),
	}
}

// cuesIdentity is the artifact identity of a file's cues: the whole file.
func cuesIdentity(candidate Candidate) mediaartifact.Identity {
	return mediaartifact.Identity{
		FileHash:           candidate.FileHash,
		FileSize:           candidate.FileSize,
		DurationSeconds:    candidate.DurationSeconds,
		WindowStartSeconds: 0,
		WindowEndSeconds:   candidate.DurationSeconds,
	}
}

// textSubtitleCodecs are the subtitle codecs whose cues carry text.
var textSubtitleCodecs = map[string]struct{}{
	"subrip": {}, "srt": {}, "ass": {}, "ssa": {}, "webvtt": {}, "vtt": {},
	"mov_text": {}, "text": {}, "tx3g": {}, "microdvd": {}, "sami": {}, "jacosub": {},
}

// cueTrackIndex returns the position of the track cueTrack chooses among the
// file's subtitle streams: the ordinal `-map 0:s:N` takes.
//
// A track's Index is its index in the container, and that is not this ordinal
// once the file carries more than one subtitle stream — a file whose subtitle
// streams are 2, 3, 4 has ordinals 0, 1, 2, so asking for `0:s:2` reads its
// third subtitle track, in whatever language that happens to be, and a file
// with two subtitle streams fails the map outright.
func cueTrackIndex(tracks []models.SubtitleTrack) (int, bool) {
	preferred := strings.Split(cuesPreferredLanguages, ",")
	for _, language := range preferred {
		for i, track := range tracks {
			if !isCueTrack(track) || track.Forced || track.External {
				continue
			}
			if languageMatches(track.Language, language) {
				return i, true
			}
		}
	}
	for i, track := range tracks {
		if isCueTrack(track) && !track.Forced && !track.External {
			return i, true
		}
	}
	return 0, false
}

// cueTrack returns the text track whose cues are read: the first in a
// preferred language, else the first text track that is not forced. An
// external entry names a sidecar file, which this reader does not open, so it
// is not a cue track.
func cueTrack(tracks []models.SubtitleTrack) (models.SubtitleTrack, bool) {
	i, ok := cueTrackIndex(tracks)
	if !ok {
		return models.SubtitleTrack{}, false
	}
	return tracks[i], true
}

// languageMatches reports whether a track's language is the one wanted,
// treating the two- and three-letter codes as the same language.
func languageMatches(track, wanted string) bool {
	track = strings.ToLower(strings.TrimSpace(track))
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	if track == "" || wanted == "" {
		return false
	}
	return track == wanted || track == languageCode(wanted) || languageCode(track) == wanted
}

// languageCode returns a two-letter language's three-letter code.
func languageCode(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "eng":
		return "eng"
	default:
		return language
	}
}

// isCueTrack reports whether a track holds readable text cues. An image
// subtitle (PGS, VobSub) has no text to read.
func isCueTrack(track models.SubtitleTrack) bool {
	if track.IndexOnly {
		return false
	}
	_, ok := textSubtitleCodecs[strings.ToLower(strings.TrimSpace(track.Codec))]
	return ok
}

// FileCues reads the cues of the candidate's text track.
func (e *SubtitleCueExtractor) FileCues(ctx context.Context, candidate Candidate) ([]subtitleCue, bool, error) {
	ordinal, ok := cueTrackIndex(candidate.SubtitleTracks)
	if !ok || candidate.FilePath == "" {
		return nil, false, nil
	}
	data, err := playback.ExtractSubtitleWithFormat(ctx, candidate.FilePath, ordinal, "srt", e.config.FFmpegPath)
	if err != nil {
		return nil, false, fmt.Errorf("extracting subtitle cues from file %d: %w", candidate.FileID, err)
	}
	parsed, err := subtitles.ParseCues(data)
	if err != nil {
		return nil, false, fmt.Errorf("parsing subtitle cues of file %d: %w", candidate.FileID, err)
	}
	cues := make([]subtitleCue, 0, len(parsed))
	for _, cue := range parsed {
		text := strings.TrimSpace(strings.Join(cue.Lines, " "))
		if text == "" {
			continue
		}
		cues = append(cues, subtitleCue{
			Start: cue.Start.Seconds(),
			End:   cue.End.Seconds(),
			Text:  text,
		})
	}
	if len(cues) == 0 {
		return nil, false, nil
	}
	return cues, true, nil
}

// ensureIntroCues loads the cached cues of the candidates that need them and
// extracts the missing ones. A file with no text track is stored unusable so it
// is not read again until it changes. Extraction is one demux per file, so it
// is asked for the files a hint can act on, not the whole season.
// retryable reports whether a cue read that failed should be retried, as
// opposed to a file that has no text track to read.
func (a *Analyzer) ensureIntroCues(ctx context.Context, candidates []Candidate) (map[int][]subtitleCue, bool, error) {
	if a.cues == nil || len(candidates) == 0 {
		return nil, false, nil
	}
	stored, err := a.loadCreditsArtifacts(ctx, candidates, introCuesKey())
	if err != nil {
		return nil, false, err
	}
	out := map[int][]subtitleCue{}
	var (
		mu       sync.Mutex
		firstErr error
		retry    bool
		wg       sync.WaitGroup
	)
	// A read that failed for a temporary reason is not a file without a text
	// track: the group stays retryable so the hint is not lost for good.
	setRetry := func() {
		mu.Lock()
		retry = true
		mu.Unlock()
	}
	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	for _, candidate := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				setErr(err)
				return
			}
			if artifact, ok := stored[candidate.FileID]; ok {
				switch artifact.State(cuesIdentity(candidate), a.nodeName(), time.Now()) {
				case mediaartifact.Ready:
					if artifact.PayloadFormat == CuesFormat {
						if cues, err := decodeCues(artifact.Payload); err == nil {
							mu.Lock()
							out[candidate.FileID] = cues
							mu.Unlock()
							return
						}
					}
				case mediaartifact.Skipped:
					return
				}
			}
			// Cue extraction runs ffmpeg, so it takes a slot from the same
			// limiter every other extraction in this package takes one from.
			release, err := a.ffmpegAcquirer()(ctx)
			if err != nil {
				setErr(err)
				return
			}
			cues, ok, err := a.cues.FileCues(ctx, candidate)
			release()
			if err != nil {
				if ctx.Err() != nil {
					setErr(ctx.Err())
					return
				}
				a.logger.DebugContext(ctx, "subtitle cue extraction failed", "file_id", candidate.FileID, "error", err)
				setRetry()
				return
			}
			if !ok {
				_ = a.storeCuesUnusable(ctx, candidate)
				return
			}
			if err := a.storeCues(ctx, candidate, cues); err != nil {
				setErr(err)
				return
			}
			mu.Lock()
			out[candidate.FileID] = cues
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, retry, firstErr
}

// storeCues caches a file's cues.
func (a *Analyzer) storeCues(ctx context.Context, candidate Candidate, cues []subtitleCue) error {
	return a.repo.UpsertArtifact(ctx, mediaartifact.Artifact{
		MediaFileID:           candidate.FileID,
		Key:                   introCuesKey(),
		Identity:              cuesIdentity(candidate),
		Status:                mediaartifact.StatusComplete,
		PayloadFormat:         CuesFormat,
		SampleDurationSeconds: candidate.DurationSeconds,
		ItemCount:             len(cues),
		Payload:               encodeCues(cues),
		MarkersInvalidatedAt:  candidate.MarkersInvalidatedAt,
	})
}

// storeCuesUnusable records that the file has no text track to read cues from.
func (a *Analyzer) storeCuesUnusable(ctx context.Context, candidate Candidate) error {
	return a.repo.UpsertArtifact(ctx, mediaartifact.Artifact{
		MediaFileID: candidate.FileID,
		Key:         introCuesKey(),
		Identity:    cuesIdentity(candidate),
		Status:      mediaartifact.StatusUnusable,
		Detail:      cuesDetailNoTrack,
	})
}

// encodeCues packs cues as [start float64][end float64][length uint16][text]…
func encodeCues(cues []subtitleCue) []byte {
	size := 0
	for _, cue := range cues {
		size += cuesRecordHeaderBytes + len(cue.Text)
	}
	out := make([]byte, 0, size)
	for _, cue := range cues {
		text := []byte(cue.Text)
		if len(text) > 0xffff {
			text = text[:0xffff]
		}
		out = binary.LittleEndian.AppendUint64(out, math.Float64bits(cue.Start))
		out = binary.LittleEndian.AppendUint64(out, math.Float64bits(cue.End))
		out = binary.BigEndian.AppendUint16(out, uint16(len(text))) //nolint:gosec // text is clamped to 0xffff above.
		out = append(out, text...)
	}
	return out
}

// cuesRecordHeaderBytes is what precedes each cue's text in a payload: two
// float64 times and a uint16 length.
const cuesRecordHeaderBytes = 8 + 8 + 2

// decodeCues reads what encodeCues wrote.
func decodeCues(data []byte) ([]subtitleCue, error) {
	var cues []subtitleCue
	for len(data) > 0 {
		if len(data) < cuesRecordHeaderBytes {
			return nil, fmt.Errorf("cue record is %d bytes, needs %d", len(data), cuesRecordHeaderBytes)
		}
		start := math.Float64frombits(binary.LittleEndian.Uint64(data))
		end := math.Float64frombits(binary.LittleEndian.Uint64(data[8:]))
		length := int(binary.BigEndian.Uint16(data[16:]))
		data = data[cuesRecordHeaderBytes:]
		if len(data) < length {
			return nil, fmt.Errorf("cue text is %d bytes, record holds %d", length, len(data))
		}
		cues = append(cues, subtitleCue{Start: start, End: end, Text: string(data[:length])})
		data = data[length:]
	}
	return cues, nil
}
