package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
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
	Path    string
	Title   string
	Track   int
	Probe   *ProbeData
	Size    int64
	ModTime time.Time
}

// parsePodcastShow walks a single subdirectory of a podcast library and
// returns the show's metadata + episode list. Each audio file inside
// becomes one episode; tags are read from each file individually so
// per-episode titles surface correctly. Each episode keeps its probe and file
// stat so its media_files row records the file's technical facts.
//
// Returns an errFolderHasNoMedia-wrapped error if the folder contains zero
// audible episodes or is skipped by an ignore marker. Every other error
// (including an ffprobe binary that cannot be executed) is a real failure and
// must be reported by the caller.
//
// inheritedRules are the applicable .siloignore rules from the walk ancestors;
// the show folder's own .siloignore, if any, stacks on top of them and both
// sets are evaluated against each episode file.
func parsePodcastShow(ctx context.Context, ffprobePath string, folderPath string, inheritedRules []ignoreRules) (*parsedPodcastShow, error) {
	audioFiles, err := listPodcastShowAudioFiles(folderPath, inheritedRules)
	if err != nil {
		return nil, err
	}

	show := &parsedPodcastShow{}
	var changing error
	for idx, path := range audioFiles {
		probed, info, err := probeStablePodcastFile(ctx, ffprobePath, path)
		if errors.Is(err, errPodcastFileChanging) {
			// A download still being written: leave this episode for the next
			// scan instead of failing the rest of the show. Its row, if any,
			// stays present because the caller recorded the listing first.
			slog.WarnContext(ctx, "podcast scan: episode still changing; skipping it until the next scan", "component", "scanner",
				"path", path,
			)
			changing = err
			continue
		}
		if err != nil {
			return nil, err
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
			Path:    path,
			Title:   firstNonEmpty(tags["title"], stem),
			Track:   track,
			Probe:   probed,
			Size:    info.Size(),
			ModTime: normalizeFileModifiedAt(info.ModTime()),
		})
	}
	if len(show.Episodes) == 0 && changing != nil {
		return nil, changing
	}
	return show, nil
}

// errPodcastFileChanging marks an episode file that kept changing across
// both probes, such as a download still being written.
var errPodcastFileChanging = errors.New("podcast file changed while it was probed")

// probeStablePodcastFile probes path and returns the stat of the file that was
// probed. The file is stat'ed before and after the probe; if it was replaced
// or rewritten in between, even by a file with the same size and mtime, it is
// probed once more, and a file still changing is reported as
// errPodcastFileChanging so the next scan reads it again. Without this, the
// probe of one file could be recorded under another file's size and mtime and
// look current.
func probeStablePodcastFile(ctx context.Context, ffprobePath, path string) (*ProbeData, os.FileInfo, error) {
	for attempt := 1; ; attempt++ {
		before, err := os.Stat(path)
		if err != nil {
			return nil, nil, fmt.Errorf("stat podcast file %s: %w", path, err)
		}
		probed, err := ProbeFile(ctx, ffprobePath, path)
		if err != nil {
			return nil, nil, fmt.Errorf("probe podcast file %s: %w", path, err)
		}
		after, err := os.Stat(path)
		if err != nil {
			return nil, nil, fmt.Errorf("stat podcast file %s: %w", path, err)
		}
		if os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) {
			return probed, before, nil
		}
		if attempt == 2 {
			return nil, nil, fmt.Errorf("%w: %s", errPodcastFileChanging, path)
		}
	}
}

func listPodcastShowAudioFiles(folderPath string, inheritedRules []ignoreRules) ([]string, error) {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Renamed or deleted between the scan walk and this read; treat
			// it as an empty show so the caller skips instead of failing.
			return nil, fmt.Errorf("podcast show %s: %w", folderPath, errFolderHasNoMedia)
		}
		return nil, fmt.Errorf("read podcast folder %s: %w", folderPath, err)
	}
	// basePath is folderPath itself: show-local patterns match episode
	// filenames relative to the show folder, and inherited root rules match
	// nested paths such as Show/file.mp3.
	rules, skip := dirIgnoreRules(inheritedRules, folderPath, folderPath, entries)
	if skip {
		// A skipped show folder (and its contents) is ignored; treat it as an
		// empty show so the caller skips instead of failing.
		return nil, fmt.Errorf("podcast show %s: %w", folderPath, errFolderHasNoMedia)
	}
	var audioFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if SupportsAudioFile(entry.Name()) {
			audioPath := filepath.Join(folderPath, entry.Name())
			if ignoreRulesMatch(rules, audioPath, false) {
				continue
			}
			audioFiles = append(audioFiles, audioPath)
		}
	}
	if len(audioFiles) == 0 {
		return nil, fmt.Errorf("podcast show %s: %w", folderPath, errFolderHasNoMedia)
	}
	sort.Strings(audioFiles)
	return audioFiles, nil
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
