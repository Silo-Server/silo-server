// internal/subtitles/releaseinfo.go
package subtitles

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ReleaseInfo contains metadata parsed from a media filename.
type ReleaseInfo struct {
	ReleaseGroup string
	Source       string // BluRay, WEB-DL, WEBRip, HDTV, etc.
	Resolution   string // 720p, 1080p, 2160p
	VideoCodec   string // x264, x265, H.264, H.265, HEVC
	AudioCodec   string // AAC, DTS, DDP5.1, etc.
}

var (
	groupRe      = regexp.MustCompile(`-([A-Za-z0-9]+)(?:\.[a-z]{2,4})?$`)
	sourceRe     = regexp.MustCompile(`(?i)\b(BluRay|BDRip|WEB-DL|WEBRip|HDTV|DVDRip|BRRip|REMUX)\b`)
	resolutionRe = regexp.MustCompile(`\b(720|1080|2160|4320)p\b`)
	videoCodecRe = regexp.MustCompile(`(?i)\b(x264|x265|H\.?264|H\.?265|HEVC|AVC|VP9|AV1)\b`)
	audioCodecRe = regexp.MustCompile(`(?i)\b(AAC|AC3|DTS(?:-HD)?|DDP?\d?\.?\d?|FLAC|TrueHD|Atmos)\b`)
)

// ParseReleaseInfo extracts release metadata from a media filename or path.
func ParseReleaseInfo(filePathOrName string) ReleaseInfo {
	nameNoExt := releaseName(filePathOrName)

	var info ReleaseInfo

	if m := groupRe.FindStringSubmatch(nameNoExt); len(m) > 1 {
		info.ReleaseGroup = m[1]
	}
	if m := sourceRe.FindString(nameNoExt); m != "" {
		info.Source = m
	}
	if m := resolutionRe.FindString(nameNoExt); m != "" {
		info.Resolution = m
	}
	if m := videoCodecRe.FindString(nameNoExt); m != "" {
		info.VideoCodec = m
	}
	if m := audioCodecRe.FindString(nameNoExt); m != "" {
		info.AudioCodec = m
	}

	return info
}

// ParseReleaseSource returns ParseReleaseInfo(filePathOrName).Source without
// running the other patterns, for callers that render every card's badge.
func ParseReleaseSource(filePathOrName string) string {
	return sourceRe.FindString(releaseName(filePathOrName))
}

// releaseName is a path's file name without its extension.
func releaseName(filePathOrName string) string {
	name := filepath.Base(filePathOrName)
	return strings.TrimSuffix(name, filepath.Ext(name))
}
