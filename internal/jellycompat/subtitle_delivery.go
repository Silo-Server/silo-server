package jellycompat

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const (
	compatSubtitleVTT    = "vtt"
	compatSubtitleASS    = "ass"
	compatSubtitleSRT    = "srt"
	compatSubtitleEncode = "Encode"
)

// compatSubtitleSegmentPTSOffset90k is the 90 kHz PTS shift the source's HLS
// segments add to the source clock, which a WebVTT X-TIMESTAMP-MAP must carry.
// MPEG-TS segments are shifted by the muxer's fixed delay. fMP4 segments
// (copied video without the MPEG-TS remux, and encoded HEVC) keep the source
// clock in tfdt, so their map needs no shift. The container decision is the
// one the transcode itself makes from the same recipe fields.
//
// Direct play and progressive remux have no HLS segments and keep the source
// clock. Subtitle DeliveryUrls can be fetched before any route starts, so until
// one has, a source that can be transcoded is assumed to play over HLS.
func compatSubtitleSegmentPTSOffset90k(playMethod string, source PlaybackMediaSource, file *models.MediaFile) int64 {
	switch playMethod {
	case string(playback.PlayTranscode):
	case "":
		if !source.SupportsTranscoding {
			return 0
		}
	default:
		return 0
	}
	sourceVideoCodec, _, _ := playback.SourceVideoTranscodeFacts(file)
	opts := playback.TranscodeOpts{
		SourceVideoCodec: sourceVideoCodec,
		TargetCodecVideo: compatSourceTargetVideoCodec(source),
		CopyVideoMPEGTS:  source.HLSRemuxMPEGTS,
	}
	if playback.HLSOutputContainer(opts) == playback.OutputContainerMPEGTS {
		return playback.HLSMPEGTSTimestampOffset90k
	}
	return 0
}

// deliverTextSubtitle answers from text subtitle bytes that already carry
// their timing correction: ASS/SSA or SRT as they are when that format was
// requested, anything else as WebVTT.
func (h *PlaybackHandler) deliverTextSubtitle(w http.ResponseWriter, r *http.Request, format string, data []byte, requestedFormat string, segmentPTSOffset90k int64) {
	if requestedFormat == compatSubtitleASS && playback.IsASS(format) {
		h.deliverSubtitle(w, r, compatSubtitleASS, data, segmentPTSOffset90k)
		return
	}
	if requestedFormat == compatSubtitleSRT && subtitleCanServeSRT(format) {
		h.deliverSubtitle(w, r, compatSubtitleSRT, data, segmentPTSOffset90k)
		return
	}
	if subtitles.SubtitleFormat(strings.ToLower(format)) == subtitles.FormatVTT {
		h.deliverSubtitle(w, r, compatSubtitleVTT, data, segmentPTSOffset90k)
		return
	}
	vttData, err := playback.ConvertToVTTWithFFmpeg(r.Context(), data, format, h.FFmpegPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to convert subtitle")
		return
	}
	h.deliverSubtitle(w, r, compatSubtitleVTT, vttData, segmentPTSOffset90k)
}

// deliverSubtitle preserves raw ASS styling when possible and applies the
// Jellyfin text timing contract to VTT/SRT. Unsupported conversions are explicit.
// segmentPTSOffset90k is the source's HLS segment PTS shift for the VTT time
// map (see compatSubtitleSegmentPTSOffset90k).
func (h *PlaybackHandler) deliverSubtitle(w http.ResponseWriter, r *http.Request, format string, data []byte, segmentPTSOffset90k int64) {
	requested := strings.ToLower(chiURLParam(r, "routeFormat"))
	if requested == "" {
		requested = compatSubtitleVTT
	}
	if requested != compatSubtitleVTT && requested != compatSubtitleSRT && requested != compatSubtitleASS && requested != "js" {
		writeError(w, 400, "BadRequest", "Supported subtitle formats are vtt, srt, ass and js")
		return
	}
	startRaw := chiURLParam(r, "routeStartPositionTicks")
	if query := firstQueryValue(r.URL.Query(), "StartPositionTicks"); query != "" {
		startRaw = query
	}
	start, err := subtitleTicks(startRaw)
	if err != nil {
		writeError(w, 400, "BadRequest", "Invalid subtitle start position")
		return
	}
	end, err := subtitleTicks(firstQueryValue(r.URL.Query(), "EndPositionTicks"))
	if err != nil || (end > 0 && end <= start) {
		writeError(w, 400, "BadRequest", "Invalid subtitle end position")
		return
	}
	copyTimestamps, _ := parseOptionalBool(firstQueryValue(r.URL.Query(), "CopyTimestamps"))
	timeMap, _ := parseOptionalBool(firstQueryValue(r.URL.Query(), "AddVttTimeMap"))
	if requested == compatSubtitleASS {
		if !playback.IsASS(format) || start > 0 || end > 0 || timeMap {
			writeError(w, 406, "NotSupported", "ASS requires original timestamps and an ASS source")
			return
		}
		writeSubtitleResponse(w, requested, data)
		return
	}
	// Text written for left-to-right players gets its right-to-left lines
	// marked so the punctuation lands where the author put it.
	if format == compatSubtitleVTT || format == compatSubtitleSRT {
		data = subtitles.MarkLTRAuthoredLines(data)
	}
	if requested == format && start == 0 && end == 0 && !timeMap {
		writeSubtitleResponse(w, requested, data)
		return
	}
	if format != compatSubtitleVTT {
		data, err = playback.ConvertToVTTWithFFmpeg(r.Context(), data, format, h.FFmpegPath)
		if err != nil {
			writeError(w, 500, "ServerError", "Failed to convert subtitle")
			return
		}
		data = subtitles.MarkLTRAuthoredLines(data)
	}
	windowFormat := requested
	if requested == "js" {
		windowFormat = compatSubtitleVTT
	}
	result, err := windowSubtitleVTT(data, windowFormat, start, end, copyTimestamps, timeMap, segmentPTSOffset90k)
	if err != nil {
		writeError(w, 500, "ServerError", "Invalid subtitle timing")
		return
	}
	if requested == "js" {
		if !strings.Contains(string(result), " --> ") {
			writeJSON(w, http.StatusOK, map[string]any{"TrackEvents": []jellyfinSubtitleTrackEvent{}})
			return
		}
		if err := writeJellyfinJSONSubtitleResponse(w, result); err != nil {
			writeError(w, 500, "ServerError", "Failed to encode subtitle")
		}
		return
	}
	writeSubtitleResponse(w, requested, result)
}

func subtitleTicks(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	ticks, err := strconv.ParseInt(value, 10, 64)
	if err != nil || ticks < 0 {
		return 0, fmt.Errorf("invalid ticks")
	}
	return ticks / 10000, nil
}

func subtitleTimestamp(value string) (int64, error) {
	parts := strings.Split(strings.ReplaceAll(value, ",", "."), ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("invalid timestamp")
	}
	seconds := float64(0)
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("invalid timestamp")
		}
		seconds = seconds*60 + value
	}
	return int64(seconds*1000 + 0.5), nil
}

func formatSubtitleTimestamp(ms int64, separator string) string {
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3600000, ms/60000%60, ms/1000%60, separator, ms%1000)
}

func windowSubtitleVTT(data []byte, format string, start, end int64, copyTimestamps, timeMap bool, segmentPTSOffset90k int64) ([]byte, error) {
	var out strings.Builder
	if format == compatSubtitleVTT {
		out.WriteString("WEBVTT\n")
		if timeMap {
			// LOCAL is the emitted cue clock. MPEGTS is the matching segment
			// PTS: the source clock plus the segments' shift, 10 s for MPEG-TS
			// and none for fMP4. Jellyfin writes the same MPEGTS:900000 for
			// copied timestamps on MPEG-TS.
			mediaTime := segmentPTSOffset90k
			if !copyTimestamps {
				mediaTime += start * 90
			}
			fmt.Fprintf(&out, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n", mediaTime%(1<<33))
		}
		out.WriteByte('\n')
	}
	count := 0
	for block := range strings.SplitSeq(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		for i, line := range lines {
			left, right, found := strings.Cut(line, " --> ")
			if !found {
				continue
			}
			fields := strings.Fields(right)
			if len(fields) == 0 {
				return nil, fmt.Errorf("missing cue end")
			}
			from, err := subtitleTimestamp(left)
			if err != nil {
				return nil, err
			}
			to, err := subtitleTimestamp(fields[0])
			if err != nil {
				return nil, err
			}
			if to <= start || (end > 0 && from >= end) {
				break
			}
			if from < start {
				from = start
			}
			if end > 0 {
				to = min(to, end)
			}
			if !copyTimestamps {
				from -= start
				to -= start
			}
			count++
			separator := "."
			if format == compatSubtitleSRT {
				separator = ","
				fmt.Fprintf(&out, "%d\n", count)
			}
			fmt.Fprintf(&out, "%s --> %s", formatSubtitleTimestamp(from, separator), formatSubtitleTimestamp(to, separator))
			if format == compatSubtitleVTT && len(fields) > 1 {
				out.WriteByte(' ')
				out.WriteString(strings.Join(fields[1:], " "))
			}
			out.WriteByte('\n')
			if format == compatSubtitleSRT {
				// SRT has no cue settings; carry the placement the SRT source
				// had as its {\anN} tag again.
				out.WriteString(playback.SRTAlignmentTagForVTTCueSettings(strings.Join(fields[1:], " ")))
			}
			out.WriteString(strings.Join(lines[i+1:], "\n"))
			out.WriteString("\n\n")
			break
		}
	}
	return []byte(out.String()), nil
}

// subtitlePlayed hands a subtitle a client is being served to PlaySync. A
// HEAD request only asks about the subtitle and is not a play.
func (h *PlaybackHandler) subtitlePlayed(r *http.Request, target subtitles.SyncTarget) {
	if h.PlaySync != nil && r.Method != http.MethodHead {
		h.PlaySync.SubtitlePlayed(r.Context(), target)
	}
}
