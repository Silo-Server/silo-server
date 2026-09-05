package jellycompat

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	compatSubtitleVTT    = "vtt"
	compatSubtitleASS    = "ass"
	compatSubtitleSRT    = "srt"
	compatSubtitleEncode = "Encode"
)

// deliverSubtitle preserves raw ASS styling when possible and applies the
// Jellyfin text timing contract to VTT/SRT. Unsupported conversions are explicit.
func (h *PlaybackHandler) deliverSubtitle(w http.ResponseWriter, r *http.Request, format string, data []byte) {
	requested := strings.ToLower(chiURLParam(r, "routeFormat"))
	if requested == "" {
		requested = compatSubtitleVTT
	}
	if requested != compatSubtitleVTT && requested != compatSubtitleSRT && requested != compatSubtitleASS {
		writeError(w, 400, "BadRequest", "Supported subtitle formats are vtt, srt and ass")
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
	}
	result, err := windowSubtitleVTT(data, requested, start, end, copyTimestamps, timeMap)
	if err != nil {
		writeError(w, 500, "ServerError", "Invalid subtitle timing")
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

func windowSubtitleVTT(data []byte, format string, start, end int64, copyTimestamps, timeMap bool) ([]byte, error) {
	var out strings.Builder
	if format == compatSubtitleVTT {
		out.WriteString("WEBVTT\n")
		if timeMap {
			// LOCAL is the emitted cue clock; MPEGTS remains the original media clock.
			mediaTime := start * 90
			if copyTimestamps {
				mediaTime = 0
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
			out.WriteString(strings.Join(lines[i+1:], "\n"))
			out.WriteString("\n\n")
			break
		}
	}
	return []byte(out.String()), nil
}
