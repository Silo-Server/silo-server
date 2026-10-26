package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

func appendSeparateAudioHLSArgs(args []string, opts TranscodeOpts) []string {
	index := max(0, opts.AudioTrackIndex)
	args = append(args, "-map", fmt.Sprintf("0:a:%d", index), "-vn", "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1")
	args = appendAudioArgs(args, opts)
	if opts.separateAudioTimestampOffset > 0 {
		args = append(args, "-output_ts_offset", strconv.FormatFloat(opts.separateAudioTimestampOffset, 'f', 6, 64))
	}
	args = append(args, "-avoid_negative_ts", "disabled", "-muxdelay", "0", "-muxpreload", "0", "-f", "hls", "-hls_time", strconv.Itoa(opts.SegmentDuration),
		"-hls_list_size", strconv.Itoa(maxSyntheticManifestSegments), "-hls_segment_type", "mpegts",
		"-hls_flags", "independent_segments+temp_file", "-hls_segment_filename", filepath.Join(opts.OutputDir, "audio_%05d.ts"))
	if opts.StartSegmentNumber > 0 {
		args = append(args, "-start_number", strconv.Itoa(opts.StartSegmentNumber))
	}
	return append(args, filepath.Join(opts.OutputDir, hlsAudioManifestFilename))
}

// buildSeparateAudioManifest serves both renditions under the existing manifest
// route. Each child retains the same authorization and transport identity.
func (s *TranscodeSession) buildSeparateAudioManifest(segPrefix, rawQuery string) ([]byte, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid HLS query: %w", err)
	}
	rendition := query.Get("rendition")
	query.Del("rendition")
	opts := s.Opts()
	switch rendition {
	case "":
		query.Set("rendition", hlsAudioRendition)
		audioURI := "master.m3u8?" + query.Encode()
		query.Set("rendition", hlsVideoRendition)
		videoURI := "master.m3u8?" + query.Encode()
		attributes := ""
		if _, audioCodec, ok := strings.Cut(opts.HLSCodecs, ","); ok {
			if init, err := readBoundedHLSFile(filepath.Join(s.outputDir, hlsInitFilename), 2<<20); err == nil {
				if videoCodec := nativeHLSVideoCodec(init); videoCodec != "" {
					attributes += fmt.Sprintf(",CODECS=%q", videoCodec+","+audioCodec)
				}
			}
		}
		if opts.HLSVideoRange != "" {
			attributes += ",VIDEO-RANGE=" + opts.HLSVideoRange
		}
		return []byte(fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"Selected audio\",DEFAULT=YES,AUTOSELECT=YES,URI=\"%s\"\n#EXT-X-STREAM-INF:BANDWIDTH=%d,AUDIO=\"audio\"%s\n%s\n", audioURI, max(1, opts.TargetBitrateKbps*1000), attributes, videoURI)), nil
	case hlsVideoRendition, hlsAudioRendition:
		name := hlsVideoManifestFilename
		if rendition == hlsAudioRendition {
			name = hlsAudioManifestFilename
		}
		var manifest []byte
		var err error
		if rendition == hlsVideoRendition {
			manifest, err = s.GetManifest()
		} else {
			manifest, err = os.ReadFile(filepath.Join(s.outputDir, name))
		}
		if err != nil {
			return nil, ErrManifestNotReady
		}
		if !startupFilesReady(manifest, s.outputDir, 1) {
			return nil, ErrManifestNotReady
		}
		manifest = stabilizeCopyHLSRemountTimeline(manifest, opts)
		return RewriteManifestPaths(manifest, segPrefix, query.Encode())
	default:
		return nil, errors.New("unsupported HLS rendition")
	}
}

// CopyHLSRecipeVersion makes separate renditions fail closed on older workers.
func CopyHLSRecipeVersion(separateAudio bool, convertDV7 bool) string {
	if convertDV7 {
		return "4"
	}
	if separateAudio {
		return "3"
	}
	return CopyFMP4RecipeVersion
}

func validateSeparateAudioHLS(opts TranscodeOpts) error {
	if !opts.SeparateAudioHLS {
		return nil
	}
	if !strings.EqualFold(opts.TargetCodecVideo, "copy") || opts.CopyVideoMPEGTS ||
		strings.ContainsFunc(opts.HLSCodecs, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != ',' && r != '-'
		}) ||
		opts.HLSVideoRange != "" && opts.HLSVideoRange != hlsVideoRangeSDR && opts.HLSVideoRange != hlsVideoRangePQ && opts.HLSVideoRange != hlsVideoRangeHLG {
		return errors.New("unsupported separate-audio HLS recipe")
	}
	return nil
}

// prepareSeparateAudioTimestampOffset applies the same negative-DTS lift to
// both outputs. MOV lifts copied B-frame video independently of the TS muxer;
// without this, separate audio drifts by the video's initial reorder delay.
func prepareSeparateAudioTimestampOffset(ctx context.Context, opts TranscodeOpts) (TranscodeOpts, error) {
	if !opts.SeparateAudioHLS {
		return opts, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, ManifestStartupTimeout)
	defer cancel()
	interval := "%+#128"
	if opts.SeekSeconds > 0 {
		interval = fmt.Sprintf("%.6f%%+#128", opts.SeekSeconds)
	}
	// Budget packets per selected stream: dense lossless audio can otherwise
	// exhaust a global packet limit before reordered video exposes its DTS.
	probe := func(stream string) (separateAudioTimestampReport, error) {
		command := exec.CommandContext(probeCtx, ffprobePathFromFFmpeg(ResolveFFmpegPath(opts.FFmpegPath)), //nolint:gosec // G204: server-configured executable and catalog input; no shell.
			"-v", "error", "-select_streams", stream, "-read_intervals", interval,
			"-show_entries", "packet=stream_index,dts_time,duration_time:packet_side_data=skip_samples:stream=index,codec_type,sample_rate:format=start_time", "-of", "json", opts.InputPath)
		output, err := command.Output()
		if err != nil {
			return separateAudioTimestampReport{}, fmt.Errorf("separate-audio timestamp probe failed: %w", err)
		}
		var report separateAudioTimestampReport
		if json.Unmarshal(output, &report) != nil {
			return report, errors.New("separate-audio timestamp evidence is unavailable")
		}
		return report, nil
	}
	videoReport, err := probe("v:0")
	if err != nil {
		return opts, err
	}
	audioReport, err := probe(fmt.Sprintf("a:%d", max(0, opts.AudioTrackIndex)))
	if err != nil {
		return opts, err
	}
	start, err := strconv.ParseFloat(videoReport.Format.Start, 64)
	audioStart, audioStartErr := strconv.ParseFloat(audioReport.Format.Start, 64)
	if err != nil || math.IsNaN(start) || math.IsInf(start, 0) || audioStartErr != nil || audioStart != start {
		return opts, errors.New("unsupported separate-audio timestamp evidence")
	}
	videoIndex, audioIndex := -1, -1
	audioRate := 0.0
	for _, stream := range videoReport.Streams {
		if stream.Kind == hlsVideoRendition && videoIndex < 0 {
			videoIndex = stream.Index
		}
	}
	for _, stream := range audioReport.Streams {
		if stream.Kind == hlsAudioRendition && audioIndex < 0 {
			audioIndex = stream.Index
			audioRate, _ = strconv.ParseFloat(stream.SampleRate, 64)
		}
	}
	videoDTS, videoOK := videoReport.initialDTS(videoIndex)
	audioDTS, audioOK := audioReport.initialDTS(audioIndex)
	if !videoOK || !audioOK {
		return opts, errors.New("unsupported separate-audio timestamp evidence")
	}
	// AAC's native encoder emits one frame of priming before decoded sound.
	// Include it in the common lift so the TS muxer does not shift audio alone.
	if codec := strings.ToLower(strings.TrimSpace(opts.TargetCodecAudio)); codec == audioCodecAACV3 || codec == "" {
		if audioRate < 8000 || audioRate > 192000 || math.IsNaN(audioRate) || math.IsInf(audioRate, 0) {
			return opts, errors.New("unsupported separate-audio sample rate")
		}
		for _, packet := range audioReport.Packets {
			if packet.StreamIndex != audioIndex {
				continue
			}
			for _, sideData := range packet.SideData {
				audioDTS += float64(sideData.SkipSamples) / audioRate
			}
			break
		}
		audioDTS -= 1024 / audioRate
	}
	offset := max(0, start-min(videoDTS, audioDTS))
	if offset > 2 {
		return opts, errors.New("unsupported separate-audio timestamp evidence")
	}
	opts.separateAudioTimestampOffset = offset
	return opts, nil
}

type separateAudioTimestampReport struct {
	Packets []struct {
		StreamIndex int    `json:"stream_index"`
		DTS         string `json:"dts_time"`
		Duration    string `json:"duration_time"`
		SideData    []struct {
			SkipSamples int `json:"skip_samples"`
		} `json:"side_data_list"`
	} `json:"packets"`
	Streams []struct {
		Index      int    `json:"index"`
		Kind       string `json:"codec_type"`
		SampleRate string `json:"sample_rate"`
	} `json:"streams"`
	Format struct {
		Start string `json:"start_time"`
	} `json:"format"`
}

func (r separateAudioTimestampReport) initialDTS(streamIndex int) (float64, bool) {
	if streamIndex < 0 {
		return 0, false
	}
	precedingDuration := 0.0
	firstDuration := 0.0
	for _, packet := range r.Packets {
		if packet.StreamIndex != streamIndex {
			continue
		}
		if dts, err := strconv.ParseFloat(packet.DTS, 64); err == nil && !math.IsNaN(dts) && !math.IsInf(dts, 0) {
			return dts - precedingDuration, true
		}
		duration, err := strconv.ParseFloat(packet.Duration, 64)
		if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 0.25 {
			return 0, false
		}
		if firstDuration == 0 {
			firstDuration = duration
		}
		if math.Abs(firstDuration-duration) > 0.00001 {
			return 0, false
		}
		precedingDuration += duration
		if precedingDuration > 1 {
			return 0, false
		}
	}
	return 0, false
}
