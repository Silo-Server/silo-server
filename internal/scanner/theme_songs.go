package scanner

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

func (s *Scanner) scanThemeSongs(ctx context.Context, folder *models.MediaFolder, scope string, exact bool) error {
	if !supportsThemeSongs(folder.Type) {
		return nil
	}
	discovery := themeDiscovery{roots: folder.Paths, ffprobe: s.ffprobePath, ancestors: make(map[themeIgnoreKey]themeIgnoreState), readDir: os.ReadDir}
	return discovery.scan(ctx, themesongs.NewRepository(s.fileRepo.Pool()), folder.ID, scope, exact)
}

func supportsThemeSongs(folderType string) bool {
	kind := librarykind.Of(folderType)
	return kind.Movie || kind.TV || kind.Mixed
}

// Theme discovery follows a completed media scan. A theme database failure
// must not turn that scan into a failed ingest, but cancellation still stops it.
func (s *Scanner) scanOptionalThemeSongs(ctx context.Context, folder *models.MediaFolder, scope string, exact bool) error {
	err := s.scanThemeSongs(ctx, folder, scope, exact)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		slog.WarnContext(ctx, "scanner: optional theme scan failed", "component", "scanner", "folder_id", folder.ID, "scope", scope, "error", err)
	}
	return nil
}

func (d *themeDiscovery) scan(ctx context.Context, repo *themesongs.Repository, folderID int, scope string, exact bool) error {
	directories := []string{scope}
	if !exact {
		var err error
		directories, err = repo.Directories(ctx, folderID, scope)
		if err != nil {
			return err
		}
	}
	cachedDirectories, err := repo.ScanFiles(ctx, folderID, scope, exact)
	if err != nil {
		return err
	}
	for _, directory := range directories {
		if scope != "" && !pathWithinAnyRoot(directory, []string{scope}) && !pathWithinAnyRoot(scope, []string{directory}) {
			continue
		}
		root := scopeLibraryRoot(directory, d.roots)
		if root == "" {
			continue
		}
		cached := cachedDirectories[directory]
		files, err := d.discover(ctx, directory, cached)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.WarnContext(ctx, "scanner: theme directory could not be read; preserving its themes", "component", "scanner", "directory", directory, "error", err)
			continue
		}
		observed := make(map[string]themesongs.File, len(files))
		for _, file := range files {
			observed[file.Path] = file
		}
		if maps.EqualFunc(cached, observed, sameThemeFile) {
			continue
		}
		if err := repo.Replace(ctx, folderID, directory, files); err != nil {
			return err
		}
	}
	return repo.PruneOrphans(ctx, folderID, scope, exact)
}

func sameThemeFile(a, b themesongs.File) bool {
	a.ID, b.ID = "", ""
	a.FolderID, b.FolderID = 0, 0
	a.Modified, b.Modified = a.Modified.UTC(), b.Modified.UTC()
	return a == b
}

type themeIgnoreKey struct{ directory, root string }

type themeIgnoreState struct {
	rules   []ignoreRules
	ignored bool
	err     error
}

type themeDiscovery struct {
	roots     []string
	ffprobe   string
	ancestors map[themeIgnoreKey]themeIgnoreState
	readDir   func(string) ([]os.DirEntry, error)
}

// ancestorRules reads each ancestor once per pass. Retaining only the rules
// avoids keeping a large library root listing alive for the entire scan.
func (d *themeDiscovery) ancestorRules(directory, root string) themeIgnoreState {
	if !pathWithinAnyRoot(directory, []string{root}) {
		return themeIgnoreState{err: themesongs.ErrUnavailable}
	}
	if state, ok := d.ancestors[themeIgnoreKey{directory, root}]; ok {
		return state
	}
	var state themeIgnoreState
	if directory != root {
		state = d.ancestorRules(filepath.Dir(directory), root)
	}
	if state.err == nil && !state.ignored {
		if ignoreRulesMatch(state.rules, directory, true) {
			state.ignored = true
		} else {
			var entries []os.DirEntry
			entries, state.err = d.readDir(directory)
			if state.err == nil {
				inherited := append([]ignoreRules(nil), state.rules...)
				state.rules, state.ignored = dirIgnoreRules(inherited, directory, directory, entries)
			}
		}
	}
	d.ancestors[themeIgnoreKey{directory, root}] = state
	return state
}

// discoverThemeSongs follows the local conventions theme.<audio-extension> and
// theme-music/<audio-file>. It never imports these files into media_files.
func discoverThemeSongs(ctx context.Context, directory string, roots []string, ffprobe string, cached map[string]themesongs.File) ([]themesongs.File, error) {
	d := themeDiscovery{roots: roots, ffprobe: ffprobe, ancestors: make(map[themeIgnoreKey]themeIgnoreState), readDir: os.ReadDir}
	return d.discover(ctx, directory, cached)
}

func (d *themeDiscovery) discover(ctx context.Context, directory string, cached map[string]themesongs.File) ([]themesongs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := scopeLibraryRoot(directory, d.roots)
	var state themeIgnoreState
	if root != "" && root != directory {
		state = d.ancestorRules(filepath.Dir(directory), root)
	}
	rules, ignored, err := state.rules, state.ignored, state.err
	if err != nil {
		return nil, err
	}
	if ignored || ignoreRulesMatch(rules, directory, true) {
		return []themesongs.File{}, nil
	}
	entries, err := d.readDir(directory)
	if err != nil {
		return nil, err
	}
	rules, ignored = dirIgnoreRules(append([]ignoreRules(nil), rules...), directory, directory, entries)
	if ignored {
		return []themesongs.File{}, nil
	}
	files := []themesongs.File{}
	add := func(path string, entry os.DirEntry, rules []ignoreRules) error {
		if themesongs.Container(path) == "" || !entry.Type().IsRegular() || ignoreRulesMatch(rules, path, false) {
			return nil
		}
		// A theme.mp3 inside theme-music belongs to its parent directory,
		// even when this directory also contains a scanned video file.
		if owner, ok := themesongs.OwnerDirectory(path); !ok || owner != directory {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			return nil
		}
		if prior, ok := cached[path]; ok && prior.Size == info.Size() && prior.Modified.Equal(info.ModTime().Truncate(time.Microsecond)) {
			files = append(files, prior)
			return nil
		}
		probe, err := ProbeFile(ctx, d.ffprobe, path)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A failed probe cannot make the rest of this directory unobservable.
			// Retain an existing record for this file only; playback rechecks its stat.
			if prior, ok := cached[path]; ok {
				files = append(files, prior)
			}
			slog.WarnContext(ctx, "scanner: theme probe failed", "component", "scanner", "path", path, "error", err)
			return nil
		}
		if len(probe.AudioTracks) == 0 || len(probe.VideoTracks) > 0 {
			return nil
		}
		after, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
			return themesongs.ErrUnavailable
		}
		title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if strings.EqualFold(title, "theme") {
			title = "Theme"
		}
		files = append(files, themesongs.File{Song: themesongs.Song{Title: title, DurationSeconds: probe.Duration, Container: themesongs.Container(path)}, AudioCodec: probe.CodecAudio, AudioChannels: probe.AudioChannels, BitrateKbps: probe.Bitrate, SampleRate: probe.AudioTracks[0].SampleRate, OwnerPath: directory, Path: path, Size: info.Size(), Modified: info.ModTime().Truncate(time.Microsecond)})
		return nil
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(directory, entry.Name())
		if strings.EqualFold(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), "theme") {
			if err := add(path, entry, rules); err != nil {
				return nil, err
			}
		}
		if !entry.IsDir() || !strings.EqualFold(entry.Name(), "theme-music") || ignoreRulesMatch(rules, path, true) {
			continue
		}
		children, err := d.readDir(path)
		if err != nil {
			return nil, err
		}
		childRules, skip := dirIgnoreRules(rules, path, path, children)
		if skip {
			continue
		}
		for _, child := range children {
			if err := add(filepath.Join(path, child.Name()), child, childRules); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}
