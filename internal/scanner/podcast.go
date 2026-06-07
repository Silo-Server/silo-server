package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// parsedPodcastShow is what parsePodcastShow returns. The scanner write
// path converts this into media_items (type='podcast') + episodes +
// media_files rows.
type parsedPodcastShow struct {
	Title    string
	Author   string
	Year     int
	Episodes []parsedPodcastEpisode
}

type parsedPodcastEpisode struct {
	Path  string
	Title string
	Track int
}

// parsePodcastShow walks a single subdirectory of a podcast library and
// returns the show's metadata + episode list. Each audio file inside
// becomes one episode; tags are read from each file individually so
// per-episode titles surface correctly.
//
// Returns an os.ErrNotExist-wrapped error if the folder contains zero
// audio files.
func parsePodcastShow(ctx context.Context, ffprobePath string, folderPath string) (*parsedPodcastShow, error) {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, fmt.Errorf("read podcast folder %s: %w", folderPath, err)
	}
	var audioFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if SupportsAudioFile(entry.Name()) {
			audioFiles = append(audioFiles, filepath.Join(folderPath, entry.Name()))
		}
	}
	if len(audioFiles) == 0 {
		return nil, fmt.Errorf("podcast show %s: %w", folderPath, os.ErrNotExist)
	}
	sort.Strings(audioFiles)

	show := &parsedPodcastShow{}
	for idx, path := range audioFiles {
		probed, err := ProbeFile(ctx, ffprobePath, path)
		if err != nil {
			return nil, fmt.Errorf("probe podcast file %s: %w", path, err)
		}
		tags := probed.FormatTags
		if show.Title == "" {
			show.Title = firstNonEmpty(tags["album"], tags["show"], filepath.Base(folderPath))
			show.Author = firstNonEmpty(tags["artist"], tags["album_artist"])
			if year := firstNonEmpty(tags["date"], tags["year"]); year != "" {
				if y := parseTagYear(year); y > 0 {
					show.Year = y
				}
			}
		}
		stem := filepath.Base(path)
		stem = stem[:len(stem)-len(filepath.Ext(stem))]
		track := idx + 1
		if t := tags["track"]; t != "" {
			if parsed := parseTrackNumber(t); parsed > 0 {
				track = parsed
			}
		}
		show.Episodes = append(show.Episodes, parsedPodcastEpisode{
			Path:  path,
			Title: firstNonEmpty(tags["title"], stem),
			Track: track,
		})
	}
	return show, nil
}

// parseTrackNumber accepts ID3-style track values which may be "5" or
// "5/12". Returns 0 if no leading integer is present.
func parseTrackNumber(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
