package matroska

import "strings"

// FFmpeg codec names of the subtitle codecs FFmpeg's Matroska demuxer
// recognizes.
const (
	FFmpegCodecWebVTT = "webvtt"
	FFmpegCodecSubRip = "subrip"
	FFmpegCodecASS    = "ass"
)

// ffmpegSubtitleCodecs mirrors the subtitle rows of FFmpeg's
// ff_mkv_codec_tags. FFmpeg matches a CodecID by prefix and takes the first
// row that matches, so order matters.
var ffmpegSubtitleCodecs = []struct{ prefix, codec string }{
	{"D_WEBVTT/SUBTITLES", FFmpegCodecWebVTT},
	{"D_WEBVTT/CAPTIONS", FFmpegCodecWebVTT},
	{"D_WEBVTT/DESCRIPTIONS", FFmpegCodecWebVTT},
	{"D_WEBVTT/METADATA", FFmpegCodecWebVTT},
	{"S_TEXT/UTF8", FFmpegCodecSubRip},
	{"S_TEXT/ASCII", "text"},
	{"S_TEXT/ASS", FFmpegCodecASS},
	{"S_TEXT/SSA", FFmpegCodecASS},
	{"S_ASS", FFmpegCodecASS},
	{"S_SSA", FFmpegCodecASS},
	{"S_VOBSUB", "dvd_subtitle"},
	{"S_DVBSUB", "dvb_subtitle"},
	{"S_HDMV/PGS", "hdmv_pgs_subtitle"},
	{"S_HDMV/TEXTST", "hdmv_text_subtitle"},
	{"S_ARIBSUB", "arib_caption"},
}

// FFmpegSubtitleCodec returns the codec name FFmpeg's Matroska demuxer gives
// a subtitle track with this CodecID, or "" for one it doesn't recognize.
func FFmpegSubtitleCodec(codecID string) string {
	for _, row := range ffmpegSubtitleCodecs {
		if strings.HasPrefix(codecID, row.prefix) {
			return row.codec
		}
	}
	return ""
}
