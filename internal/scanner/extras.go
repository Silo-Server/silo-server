package scanner

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// extraCandidate is a walked file classified as a local extra rather than
// primary content. Extras bypass root/group inference and matching entirely:
// they bind to their parent item purely by directory structure.
type extraCandidate struct {
	Path string
	Kind models.ExtraKind
	// SupplementalDir is the classified ancestor directory (Trailers/,
	// Featurettes/, ...); empty when the file was classified by its filename
	// suffix (-trailer, -behindthescenes, ...).
	SupplementalDir string
}

// extrasDirAncestorDepth bounds how far above a file the walk looks for a
// supplemental directory name. Two levels covers "Movie/Extras/file.mkv" and
// "Movie/Extras/Subdir/file.mkv" without letting a library that happens to
// live inside a directory named "Extras" classify everything beneath it.
const extrasDirAncestorDepth = 2

// extrasClassifier classifies walked paths as local extras using the
// library's structure. A convention-named directory ("Other", "Trailers",
// "Extras", ...) only counts as an extras dir when it is owned by a title
// folder — a directory that holds media of its own (the movie file beside the
// extras dir, or episodes in season folders beside it). Convention names used
// as content-scope folders at any depth ("movies/other/<Movie>/...",
// "movies/4K/shorts/<Movie>/...") own no media directly and never classify,
// so the titles beneath them stay primary.
type extrasClassifier struct {
	folderType string
	rootSet    map[string]bool
	// dirFiles marks directories that directly contain a walked media file.
	dirFiles map[string]bool
	// dirFilesBelow marks directories with a walked media file exactly two
	// levels down through a non-convention child (a show folder above its
	// season folders — but not a folder whose only media hides inside its
	// own extras dirs).
	dirFilesBelow map[string]bool
	// probeFS switches ownership checks to bounded os.ReadDir probes for
	// single-file (watch event) scans, which have no walked path list.
	probeFS bool
}

// newExtrasClassifier builds a classifier from a scan's walked paths.
func newExtrasClassifier(folderType string, libraryRoots []string, walkedPaths []string) *extrasClassifier {
	c := &extrasClassifier{
		folderType:    folderType,
		rootSet:       walkRootSet(libraryRoots),
		dirFiles:      make(map[string]bool, len(walkedPaths)),
		dirFilesBelow: make(map[string]bool, len(walkedPaths)),
	}
	for _, p := range walkedPaths {
		dir := filepath.Dir(p)
		c.dirFiles[dir] = true
		if extrasDirKinds[normalizeScannerDirLabel(filepath.Base(dir))] == "" {
			c.dirFilesBelow[filepath.Dir(dir)] = true
		}
	}
	return c
}

// newWatchExtrasClassifier builds a classifier for single-file scans; title
// ownership is probed from the filesystem instead of a walked path list.
func newWatchExtrasClassifier(folderType string, libraryRoots []string) *extrasClassifier {
	return &extrasClassifier{
		folderType: folderType,
		rootSet:    walkRootSet(libraryRoots),
		probeFS:    true,
	}
}

// classify reports whether the walked path is a local extra.
//
// Directory names win over filename suffixes. For non-movie libraries a file
// carrying a parseable SxxExx episode token is never an extra: series
// "Extras/SxxExx" files keep their documented season-0 mapping.
func (c *extrasClassifier) classify(path string) (extraCandidate, bool) {
	candidate := extraCandidate{Path: path}

	dir := filepath.Dir(path)
	for depth := 0; depth < extrasDirAncestorDepth; depth++ {
		label := normalizeScannerDirLabel(filepath.Base(dir))
		if kind, ok := extrasDirKinds[label]; ok {
			if c.titleDirOwns(dir) {
				candidate.Kind = kind
				candidate.SupplementalDir = dir
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if candidate.SupplementalDir == "" {
		kind, ok := naming.ParseExtraSuffix(path)
		if !ok {
			return extraCandidate{}, false
		}
		candidate.Kind = models.NormalizeExtraKind(kind)
	}

	// Preserve the documented series behavior: an episode-tokened file under
	// Extras/ is a season-0 special, not an extra.
	if !librarykind.IsMovie(c.folderType) {
		if hints := naming.ParseFilename(path, c.folderType); hints != nil &&
			hints.Type == "series" && hints.EpisodeNum > 0 {
			return extraCandidate{}, false
		}
	}

	return candidate, true
}

// titleDirOwns reports whether the matched supplemental directory is owned by
// a title folder: the first non-supplemental ancestor must not be a library
// root and must hold media of its own — directly for movie folders, or one
// level down for series folders whose episodes live in season subfolders.
func (c *extrasClassifier) titleDirOwns(supplementalDir string) bool {
	owner := firstNonSupplementalAncestor(supplementalDir)
	if c.rootSet[owner] {
		return false
	}
	if c.probeFS {
		depth := 1
		if !librarykind.IsMovie(c.folderType) {
			depth = 2
		}
		return c.dirHoldsMedia(owner, depth)
	}
	if c.dirFiles[owner] {
		return true
	}
	return !librarykind.IsMovie(c.folderType) && c.dirFilesBelow[owner]
}

// dirHoldsMedia is the probeFS counterpart of dirFiles/dirFilesBelow: it
// reports whether dir holds a media file within depth levels, without
// descending into convention-named subdirectories.
func (c *extrasClassifier) dirHoldsMedia(dir string, depth int) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	mode := walkModeFor(c.folderType)
	for _, entry := range entries {
		if entry.IsDir() {
			if depth > 1 && extrasDirKinds[normalizeScannerDirLabel(entry.Name())] == "" &&
				c.dirHoldsMedia(filepath.Join(dir, entry.Name()), depth-1) {
				return true
			}
			continue
		}
		if mode.acceptsExt(strings.ToLower(filepath.Ext(entry.Name()))) {
			return true
		}
	}
	return false
}

// firstNonSupplementalAncestor walks up from a supplemental directory past any
// chained convention names ("Extras/Behind The Scenes/") and returns the
// cleaned directory that owns the supplemental chain.
func firstNonSupplementalAncestor(supplementalDir string) string {
	dir := filepath.Dir(filepath.Clean(supplementalDir))
	for extrasDirKinds[normalizeScannerDirLabel(filepath.Base(dir))] != "" {
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	return dir
}

// walkRootSet builds the cleaned-path set used for scope checks against the
// library's configured roots.
func walkRootSet(roots []string) map[string]bool {
	set := make(map[string]bool, len(roots))
	for _, root := range roots {
		set[filepath.Clean(root)] = true
	}
	return set
}

// partitionExtraPaths splits walked paths into primary content and extras.
// Primary paths feed the existing root/group inference and matching pipeline
// untouched; extras are processed separately and never influence identity.
func partitionExtraPaths(paths []string, folderType string, libraryRoots []string) ([]string, []extraCandidate) {
	classifier := newExtrasClassifier(folderType, libraryRoots, paths)
	primary := paths[:0:0]
	var extras []extraCandidate
	for _, p := range paths {
		if candidate, ok := classifier.classify(p); ok {
			extras = append(extras, candidate)
			continue
		}
		primary = append(primary, p)
	}
	return primary, extras
}

// extrasScanStats aggregates processExtraFiles outcomes for the scan result.
type extrasScanStats struct {
	New       int
	Updated   int
	Unchanged int
	Skipped   int
	Errors    int
}

// processExtraFiles ingests classified extras: bind to a parent item, upsert
// the media_extras entity, and upsert the backing media_files row (probe data
// included) with extra_id set and content/episode ids cleared. Files whose
// parent cannot be resolved yet (parent unmatched or ambiguous) are skipped;
// the next scan retries once the parent has a content id.
func (s *Scanner) processExtraFiles(
	ctx context.Context,
	folder *models.MediaFolder,
	extras []extraCandidate,
	existingByPath map[string]*scanStateFile,
) extrasScanStats {
	var stats extrasScanStats
	if len(extras) == 0 || s.extraRepo == nil {
		return stats
	}

	// Parent binding is scoped by the library's configured roots, not the
	// (possibly narrower) walk roots of a scoped scan: a movie folder targeted
	// directly by a subtree scan must still bind its own extras.
	rootSet := walkRootSet(folder.Paths)

	for _, candidate := range extras {
		if ctx.Err() != nil {
			return stats
		}

		info, err := os.Stat(candidate.Path)
		if err != nil {
			slog.Warn("scanner: extra stat failed", "path", candidate.Path, "error", err)
			stats.Errors++
			continue
		}

		extraID := contentid.ForLocal(candidate.Path)
		parentID, err := s.resolveExtraParent(ctx, folder.ID, candidate, rootSet)
		if err != nil {
			slog.Warn("scanner: extra parent lookup failed", "path", candidate.Path, "error", err)
			stats.Errors++
			continue
		}
		if parentID == "" {
			slog.Debug("scanner: extra parent unresolved, deferring",
				"path", candidate.Path, "kind", candidate.Kind)
			stats.Skipped++
			continue
		}

		// Upsert the entity before the unchanged check so parent/kind/title
		// converge on every scan (a rematched parent or reclassified kind
		// must not be masked by an unchanged file).
		if err := s.extraRepo.Upsert(ctx, models.MediaExtra{
			ContentID: extraID,
			ParentID:  parentID,
			Kind:      candidate.Kind,
			Title:     naming.ExtraTitleFromFile(candidate.Path),
		}); err != nil {
			slog.Warn("scanner: extra upsert failed", "path", candidate.Path, "error", err)
			stats.Errors++
			continue
		}

		fileModifiedAt := normalizeFileModifiedAt(info.ModTime())
		existing := existingByPath[candidate.Path]
		if existing != nil && existing.ExtraID == extraID &&
			existing.FileSize == info.Size() &&
			existing.FileModifiedAt != nil && existing.FileModifiedAt.Equal(fileModifiedAt) &&
			existing.ProbeUpdatedAt != nil && existing.MissingSince == nil {
			stats.Unchanged++
			continue
		}

		hints := s.gatherHints(candidate.Path)
		probe, probeSource := s.probeFile(ctx, candidate.Path)

		mf := models.MediaFile{
			MediaFolderID:  folder.ID,
			FilePath:       candidate.Path,
			FileSize:       info.Size(),
			FileModifiedAt: &fileModifiedAt,
			FileHash:       hints.FileHash,
			ExtraID:        extraID,
		}
		if probe != nil {
			applyProbeData(&mf, probe, probeSource)
		}
		if mf.SubtitleTracks == nil {
			mf.SubtitleTracks = []models.SubtitleTrack{}
		}
		if mf.ExternalSubtitles == nil {
			mf.ExternalSubtitles = []models.ExternalSubtitle{}
		}

		// The upsert clears content/episode linkage atomically when extra_id
		// is set, so a pre-existing primary row (e.g. a "-trailer" file
		// previously scanned as a movie version) converts in one statement.
		if _, err := s.fileRepo.Upsert(ctx, mf); err != nil {
			slog.Warn("scanner: extra file upsert failed", "path", candidate.Path, "error", err)
			stats.Errors++
			continue
		}

		if existing == nil {
			stats.New++
		} else {
			stats.Updated++
		}
	}

	if stats.New+stats.Updated+stats.Skipped+stats.Errors > 0 {
		slog.Info("scanner: processed extras",
			"folder_id", folder.ID,
			"new", stats.New,
			"updated", stats.Updated,
			"unchanged", stats.Unchanged,
			"deferred", stats.Skipped,
			"errors", stats.Errors,
		)
	}
	return stats
}

// resolveExtraParent finds the content id of the item owning an extra.
//
// Directory-classified extras bind to the directory containing the
// supplemental folder ("Movie (2020)/" for "Movie (2020)/Extras/x.mkv"),
// requiring that directory to hold exactly one item. Suffix-classified files
// first try the sibling primary file sharing their stem ("Movie A.mkv" for
// "Movie A-trailer.mkv"), so flat multi-movie folders bind correctly, then
// fall back to the unambiguous-directory rule. Library roots never bind.
func (s *Scanner) resolveExtraParent(
	ctx context.Context,
	folderID int,
	candidate extraCandidate,
	rootSet map[string]bool,
) (string, error) {
	if candidate.SupplementalDir == "" {
		dir := filepath.Dir(candidate.Path)
		stem := strings.TrimSuffix(filepath.Base(candidate.Path), filepath.Ext(candidate.Path))
		if idx := strings.LastIndexAny(stem, "-."); idx > 0 {
			stem = strings.TrimSpace(stem[:idx])
		}
		if stem != "" {
			parentID, err := s.fileRepo.FindParentContentIDForStem(ctx, folderID, dir, stem)
			if err != nil {
				return "", err
			}
			if parentID != "" {
				return parentID, nil
			}
		}
		if rootSet[filepath.Clean(dir)] {
			return "", nil
		}
		return s.fileRepo.FindUnambiguousParentContentIDForDir(ctx, folderID, dir)
	}

	parentDir := firstNonSupplementalAncestor(candidate.SupplementalDir)
	if rootSet[filepath.Clean(parentDir)] {
		// Supplemental dir sits at the library root — no single owner.
		return "", nil
	}
	return s.fileRepo.FindUnambiguousParentContentIDForDir(ctx, folderID, parentDir)
}
