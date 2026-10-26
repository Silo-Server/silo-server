package mediasample

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// FrameStats are one sampled frame's picture statistics, measured after the
// StatsOutput crop, scale, and conversion to 8-bit 4:2:0. Luma and
// saturation are reported by signalstats as the minimum, the 10th
// percentile (low), the average, the 90th percentile (high), and the
// maximum.
type FrameStats struct {
	// Seconds is the frame's time in absolute media seconds.
	Seconds float64 `json:"seconds"`
	// PBlack holds, for each StatsOutput.BlackThresholds entry in order, the
	// percentage of pixels darker than it.
	PBlack  []uint8 `json:"pblack,omitempty"`
	YMin    float32 `json:"y_min"`
	YLow    float32 `json:"y_low"`
	YAvg    float32 `json:"y_avg"`
	YHigh   float32 `json:"y_high"`
	YMax    float32 `json:"y_max"`
	SatLow  float32 `json:"sat_low"`
	SatAvg  float32 `json:"sat_avg"`
	SatHigh float32 `json:"sat_high"`
	SatMax  float32 `json:"sat_max"`
}

// statsKeys are the signalstats values every FrameStats needs, with where
// each one goes.
var statsKeys = []struct {
	key   string
	field func(*FrameStats) *float32
}{
	{"lavfi.signalstats.YMIN", func(f *FrameStats) *float32 { return &f.YMin }},
	{"lavfi.signalstats.YLOW", func(f *FrameStats) *float32 { return &f.YLow }},
	{"lavfi.signalstats.YAVG", func(f *FrameStats) *float32 { return &f.YAvg }},
	{"lavfi.signalstats.YHIGH", func(f *FrameStats) *float32 { return &f.YHigh }},
	{"lavfi.signalstats.YMAX", func(f *FrameStats) *float32 { return &f.YMax }},
	{"lavfi.signalstats.SATLOW", func(f *FrameStats) *float32 { return &f.SatLow }},
	{"lavfi.signalstats.SATAVG", func(f *FrameStats) *float32 { return &f.SatAvg }},
	{"lavfi.signalstats.SATHIGH", func(f *FrameStats) *float32 { return &f.SatHigh }},
	{"lavfi.signalstats.SATMAX", func(f *FrameStats) *float32 { return &f.SatMax }},
}

// The measuring filters of a stats chain.
const (
	filterBlackframe  = "blackframe"
	filterSignalstats = "signalstats"
	filterMetadata    = "metadata"
)

// statsGraph is the video filter chain for a StatsOutput, with the graph
// positions ffmpeg names its filter instances by ("Parsed_<name>_<index>"),
// which the log parsers use to tell instances apart.
type statsGraph struct {
	filter string
	// blackframes holds the blackframe instance of each black threshold, in
	// threshold order.
	blackframes []int
	metadata    int
}

// buildStatsGraph returns the chain
//
//	crop=trunc(iw*CW/2)*2:trunc(ih*CH/2)*2,scale=W:-2:flags=area,format=yuv420p,
//	blackframe=amount=0:threshold=T1,…,signalstats,metadata=print
//
// for frames decoded on accel: in software when accel is empty. Every
// blackframe reports every frame (amount=0), and metadata=print logs each
// frame's time and the statistics the filters before it attached.
//
// VideoToolbox surfaces of a source with bitDepth bits are downloaded first
// (videoToolboxDownloadFilter). VAAPI surfaces, which QSV decodes into too,
// are instead scaled on the GPU as a whole and converted to 8-bit NV12 there,
// which a 10-bit source's P010 surface needs before the download, and the
// crop follows:
//
//	scale_vaapi=w=SW:h=-2:format=nv12,hwdownload,format=nv12,crop=…,format=yuv420p,…
//
// SW is the even width whose crop keeps about W pixels, so the measured
// picture has about the size the software chain gives it.
func buildStatsGraph(stats StatsOutput, accel string, bitDepth int) statsGraph {
	crop := fmt.Sprintf("crop=trunc(iw*%s/2)*2:trunc(ih*%s/2)*2", formatShare(stats.CropWidth), formatShare(stats.CropHeight))
	software := []string{crop, fmt.Sprintf("scale=%d:-2:flags=area", stats.Width), "format=yuv420p"}
	// A download holds two filters, which count as two positions.
	var filters []string
	switch accel {
	case "":
		filters = software
	case hwAccelVideoToolbox:
		filters = append(strings.Split(videoToolboxDownloadFilter(bitDepth), ","), software...)
	default:
		filters = []string{fmt.Sprintf("scale_vaapi=w=%d:h=-2:format=nv12", gpuScaleWidth(stats))}
		filters = append(filters, strings.Split(hwDownloadFilter, ",")...)
		filters = append(filters, crop, "format=yuv420p")
	}
	graph := statsGraph{}
	for _, threshold := range stats.BlackThresholds {
		graph.blackframes = append(graph.blackframes, len(filters))
		filters = append(filters, fmt.Sprintf("%s=amount=0:threshold=%d", filterBlackframe, threshold))
	}
	filters = append(filters, filterSignalstats)
	graph.metadata = len(filters)
	filters = append(filters, filterMetadata+"=print")
	graph.filter = strings.Join(filters, ",")
	return graph
}

// statsGraph returns the stats chain of attempt, whose hardware decodes on
// accel.
func (r Request) statsGraph(attempt Attempt, accel string) statsGraph {
	if !attempt.Hardware {
		accel = ""
	}
	return buildStatsGraph(*r.Stats, accel, r.VideoBitDepth)
}

// gpuScaleWidth is the even width the GPU scales a picture to so that its
// StatsOutput crop is about Width pixels wide: Width / CropWidth, rounded to
// the nearest even number (534 for a 0.9 crop to 480).
func gpuScaleWidth(stats StatsOutput) int {
	return 2 * int(math.Round(float64(stats.Width)/stats.CropWidth/2))
}

// formatShare prints a crop share as ffmpeg expressions take it.
func formatShare(share float64) string {
	return strconv.FormatFloat(share, 'f', -1, 64)
}

// statsParser collects the log lines of one stats output and joins them into
// frames.
type statsParser struct {
	metadata *metadataParser
	black    *blackframeParser
	offset   float64
	// sampled places frames by their sample metadata entry instead of their
	// time; see newSampledStatsParser.
	sampled bool
}

func newStatsParser(graph statsGraph, offset float64) *statsParser {
	return &statsParser{
		metadata: newMetadataParser(graph.metadata),
		black:    newBlackframeParser(graph.blackframes),
		offset:   offset,
	}
}

// newSampledStatsParser parses the stats of a Samples request. A frame's
// time is the sample time its packet was tagged with, since its own
// timestamp follows the concat list. A frame without the tag cannot be
// placed and is dropped, and when one sample decodes a second keyframe, only
// the first frame for that sample is kept.
func newSampledStatsParser(graph statsGraph) *statsParser {
	parser := newStatsParser(graph, 0)
	parser.sampled = true
	return parser
}

func (p *statsParser) line(line string) {
	p.black.line(line)
	p.metadata.line(line)
}

// result joins each metadata frame with its blackframe values by frame
// index. Both filters count the frames passing through one linear chain, so
// the indexes agree. A frame missing any statistic, such as one cut short
// when ffmpeg stopped, is dropped. Times are offset (the window start) plus
// the frame's pts_time, or for samples the frame's sample time.
func (p *statsParser) result() []FrameStats {
	frames := make([]FrameStats, 0, len(p.metadata.frames))
	seen := map[float64]struct{}{}
	for _, meta := range p.metadata.frames {
		seconds := p.offset + meta.PTSTime
		if p.sampled {
			if _, dup := seen[meta.Sample]; !meta.HasSample || dup {
				continue
			}
			seconds = meta.Sample
		}
		pblack, ok := p.black.values(meta.Index)
		if !ok {
			continue
		}
		frame := FrameStats{Seconds: seconds, PBlack: pblack}
		complete := true
		for _, stat := range statsKeys {
			value, ok := meta.Values[stat.key]
			if !ok {
				complete = false
				break
			}
			*stat.field(&frame) = float32(value)
		}
		if complete {
			frames = append(frames, frame)
			if p.sampled {
				seen[meta.Sample] = struct{}{}
			}
		}
	}
	return frames
}
