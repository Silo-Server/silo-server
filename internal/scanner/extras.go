package scanner

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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

// showEvidenceDepth bounds how far below a folder a series classifier looks
// for an episode whose series root is that folder: three path components
// cover "Show/Season 01/Disc 1/episode.mkv".
const showEvidenceDepth = 3

// extrasClassifier classifies walked paths as local extras using the
// library's structure. A convention-named directory ("Other", "Trailers",
// "Extras", ...) only counts as an extras dir when it is owned by a title
// folder: a directory that holds media of its own (the movie file beside the
// extras dir) or, outside movie libraries, a show folder (see
// walkedShowFolder). Convention names used as content-scope folders at any
// depth ("movies/other/<Movie>/...", "tv/Kids/Shorts/<files>") own no media
// directly and are no show's folder, so the titles beneath them stay primary.
type extrasClassifier struct {
	folderType   string
	libraryRoots []string
	rootSet      map[string]bool
	// walkRoots are the roots the walked path list covers. A title folder
	// outside them (a subtree scan of the extras dir itself) has unknown
	// contents, so its ownership is probed from the filesystem instead.
	walkRoots []string
	// probedOwners caches filesystem ownership probes by owner directory.
	probedOwners map[string]bool
	// dirFiles marks directories that directly contain a walked media file.
	dirFiles map[string]bool
	// sortedPaths holds the walked paths in sorted order, so a series
	// classifier can list the files below a candidate show folder.
	sortedPaths []string
	// showFolders caches walkedShowFolder answers by directory.
	showFolders map[string]bool
	// probeFS switches ownership checks to bounded os.ReadDir probes for
	// single-file (watch event) scans, which have no walked path list.
	probeFS bool
}

// newExtrasClassifier builds a classifier from the paths a scan walked under
// walkRoots.
func newExtrasClassifier(folderType string, libraryRoots, walkRoots, walkedPaths []string) *extrasClassifier {
	c := &extrasClassifier{
		folderType:   folderType,
		libraryRoots: libraryRoots,
		rootSet:      walkRootSet(libraryRoots),
		walkRoots:    walkRoots,
		probedOwners: make(map[string]bool),
		dirFiles:     make(map[string]bool, len(walkedPaths)),
		showFolders:  make(map[string]bool),
	}
	for _, p := range walkedPaths {
		c.dirFiles[filepath.Dir(p)] = true
	}
	if !librarykind.IsMovie(folderType) {
		c.sortedPaths = append([]string(nil), walkedPaths...)
		sort.Strings(c.sortedPaths)
	}
	return c
}

// newWatchExtrasClassifier builds a classifier for single-file scans; title
// ownership is probed from the filesystem instead of a walked path list.
func newWatchExtrasClassifier(folderType string, libraryRoots []string) *extrasClassifier {
	return &extrasClassifier{
		folderType:   folderType,
		libraryRoots: libraryRoots,
		rootSet:      walkRootSet(libraryRoots),
		probeFS:      true,
	}
}

// classify reports whether the walked path is a local extra.
//
// Directory names win over filename suffixes. Outside movie libraries, a file
// carrying a parseable SxxExx episode token directly under a title's Extras/
// folder, or classified only by its filename suffix, is not an extra: series
// "Extras/SxxExx" files keep their documented episode mapping. In any other
// extras folder ("other/", "Trailers/", "Extras/Behind The Scenes/") the
// episode token is part of the extra's name.
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
	// a title's Extras/ is an episode (S00 a special), not an extra.
	if !librarykind.IsMovie(c.folderType) && keepsEpisodeToken(candidate) {
		if hints := naming.ParseFilename(path, c.folderType); hints != nil &&
			hints.Type == "series" && hints.EpisodeNum > 0 {
			return extraCandidate{}, false
		}
	}

	return candidate, true
}

// keepsEpisodeToken reports whether an episode token in the candidate's file
// name still makes it an episode: the file sits under an Extras/ folder that
// its title folder holds directly, or only its filename suffix marked it.
func keepsEpisodeToken(candidate extraCandidate) bool {
	if candidate.SupplementalDir == "" {
		return true
	}
	switch normalizeScannerDirLabel(filepath.Base(candidate.SupplementalDir)) {
	case "extra", "extras":
		return firstNonSupplementalAncestor(candidate.SupplementalDir) == filepath.Dir(candidate.SupplementalDir)
	}
	return false
}

// titleDirOwns reports whether the matched supplemental directory is owned by
// a title folder: the first non-supplemental ancestor must lie strictly inside
// a library root and must hold media of its own or, outside movie libraries,
// be a show folder. A library root that is itself named like an extras dir
// ("/media/shorts") has its owner above the library, which owns nothing.
func (c *extrasClassifier) titleDirOwns(supplementalDir string) bool {
	owner := firstNonSupplementalAncestor(supplementalDir)
	if c.rootSet[owner] || !pathWithinAnyRoot(owner, c.libraryRoots) {
		return false
	}
	if c.probeFS || !pathWithinAnyRoot(owner, c.walkRoots) {
		return c.probeOwner(owner)
	}
	if c.dirFiles[owner] {
		return true
	}
	return !librarykind.IsMovie(c.folderType) && c.walkedShowFolder(owner)
}

// walkedShowFolder reports whether dir, inside the walk roots, is a show
// folder, which owns the extras folders inside it. Outside movie libraries, a
// folder is a show folder when a file within showEvidenceDepth below it
// resolves to it as its series root (episodes in season folders, at any
// supported depth, or loose in the folder) or, in a TV library, when its name
// carries a year or provider IDs ("Show (2021)", "Show [tvdbid-123]"), which
// covers a show whose episodes haven't arrived yet. A category folder such as
// Kids/ holds shows whose episodes resolve to their own folders, so its
// Shorts/ or Clips/ folder stays a content folder. probeOwner applies the same
// test from the filesystem.
func (c *extrasClassifier) walkedShowFolder(dir string) bool {
	if owns, ok := c.showFolders[dir]; ok {
		return owns
	}
	owns := c.namedShowFolder(dir)
	prefix := dir + string(filepath.Separator)
	for i := sort.SearchStrings(c.sortedPaths, prefix); !owns && i < len(c.sortedPaths); i++ {
		if !strings.HasPrefix(c.sortedPaths[i], prefix) {
			break
		}
		owns = c.episodeRootsAt(c.sortedPaths[i], dir)
	}
	c.showFolders[dir] = owns
	return owns
}

// episodeRootsAt reports whether file, below dir, resolves to dir as its
// series root.
func (c *extrasClassifier) episodeRootsAt(file, dir string) bool {
	rel, err := filepath.Rel(dir, file)
	if err != nil || strings.Count(rel, string(filepath.Separator)) >= showEvidenceDepth {
		return false
	}
	ctx := naming.ResolvePathContext(file, c.folderType, c.libraryRoots...)
	return ctx != nil && ctx.RootPath != "" && filepath.Clean(filepath.FromSlash(ctx.RootPath)) == dir
}

// namedShowFolder reports whether a TV library folder's name identifies a
// show on its own: a year or provider IDs.
func (c *extrasClassifier) namedShowFolder(dir string) bool {
	if !librarykind.IsTV(c.folderType) {
		return false
	}
	_, _, identified := naming.ParseInferFolderTitleYear(filepath.Base(dir))
	return identified
}

// probeOwner reports from the filesystem whether owner holds media of its
// own or, outside movie libraries, is a show folder. The probe applies the
// library walk's skip and ignore rules, so it only counts files a full scan
// would have walked.
func (c *extrasClassifier) probeOwner(owner string) bool {
	if owns, ok := c.probedOwners[owner]; ok {
		return owns
	}
	owns := false
	if rules, ignored, err := scanRootIgnoreRules(owner, c.libraryRoots); err == nil && !ignored {
		owns = c.dirHoldsMedia(owner, 1, rules, nil)
		if !owns && !librarykind.IsMovie(c.folderType) {
			owns = c.namedShowFolder(owner) || c.dirHoldsMedia(owner, showEvidenceDepth, rules, func(file string) bool {
				return c.episodeRootsAt(file, owner)
			})
		}
	}
	if c.probedOwners != nil {
		c.probedOwners[owner] = owns
	}
	return owns
}

// dirHoldsMedia is the probeFS counterpart of the walked path list: it
// reports whether dir holds, within depth levels, a file the library walk
// (walkLogicalTree) would collect and that match accepts (any such file when
// match is nil).
func (c *extrasClassifier) dirHoldsMedia(dir string, depth int, inherited []ignoreRules, match func(file string) bool) bool {
	mode := walkModeFor(c.folderType)
	if isIgnoredDirectoryPath(dir) || (mode == walkModeMovie && shouldSkipMovieSupplementalDir(dir)) {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	rules, skip := dirIgnoreRules(inherited, dir, dir, entries)
	if skip {
		return false
	}
	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		if ignoreRulesMatch(rules, child, entry.IsDir()) {
			continue
		}
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			// The library walk follows directory symlinks, so a symlinked
			// season folder holds the show's episodes too.
			info, err := os.Stat(child)
			if err != nil {
				continue
			}
			isDir = info.IsDir()
			if isDir && ignoreRulesMatch(rules, child, true) {
				continue
			}
		}
		if isDir {
			if depth > 1 && c.dirHoldsMedia(child, depth-1, rules, match) {
				return true
			}
			continue
		}
		if mode == walkModeMovie && shouldSkipMovieSupplementalFile(child) {
			continue
		}
		if mode.acceptsPath(child) && (match == nil || match(child)) {
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

// partitionExtraPaths splits the paths walked under walkRoots into primary
// content and extras. Primary paths feed the existing root/group inference and
// matching pipeline untouched; extras are processed separately and never
// influence identity.
func partitionExtraPaths(paths []string, folderType string, libraryRoots, walkRoots []string) ([]string, []extraCandidate) {
	classifier := newExtrasClassifier(folderType, libraryRoots, walkRoots, paths)
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

	// A late extra may already own a primary row from an earlier scan that
	// imported it as its own (unmatched) item. Those rows would make the title
	// folder look ambiguous to the parent lookup and defer the extra forever,
	// so the lookup for each folder leaves out every extra in this batch that
	// lies beneath it.
	batchByLookupDir := make(map[string][]string)
	for _, candidate := range extras {
		batchByLookupDir[extraParentLookupDir(candidate)] = nil
	}
	for _, candidate := range extras {
		for dir := filepath.Dir(candidate.Path); ; {
			if _, ok := batchByLookupDir[dir]; ok {
				batchByLookupDir[dir] = append(batchByLookupDir[dir], candidate.Path)
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	for _, candidate := range extras {
		if ctx.Err() != nil {
			return stats
		}

		info, err := os.Stat(candidate.Path)
		if err != nil {
			slog.WarnContext(ctx, "scanner: extra stat failed", "component", "scanner", "path", candidate.Path, "error", err)
			stats.Errors++
			continue
		}

		extraID := contentid.ForLocal(candidate.Path)
		parentID, err := s.resolveExtraParent(ctx, folder.ID, candidate, rootSet,
			batchByLookupDir[extraParentLookupDir(candidate)])
		if err != nil {
			slog.WarnContext(ctx, "scanner: extra parent lookup failed", "component", "scanner", "path", candidate.Path, "error", err)
			stats.Errors++
			continue
		}
		if parentID == "" {
			slog.DebugContext(ctx, "scanner: extra parent unresolved, deferring", "component", "scanner",
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
			slog.WarnContext(ctx, "scanner: extra upsert failed", "component", "scanner", "path", candidate.Path, "error", err)
			stats.Errors++
			continue
		}

		fileModifiedAt := normalizeFileModifiedAt(info.ModTime())
		existing := existingByPath[candidate.Path]
		if extraFileUnchanged(existing, extraID, info.Size(), fileModifiedAt) {
			stats.Unchanged++
			continue
		}

		hints := s.gatherHints(candidate.Path)
		probe, probeSource, probeRejected := s.probeFile(ctx, candidate.Path)

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
		} else if probeRejected {
			markProbeRejected(&mf)
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
			slog.WarnContext(ctx, "scanner: extra file upsert failed", "component", "scanner", "path", candidate.Path, "error", err)
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
		slog.InfoContext(ctx, "scanner: processed extras", "component", "scanner",
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
// excludePaths are extra files whose rows must not count toward the
// directory's owners (see processExtraFiles).
func (s *Scanner) resolveExtraParent(
	ctx context.Context,
	folderID int,
	candidate extraCandidate,
	rootSet map[string]bool,
	excludePaths []string,
) (string, error) {
	dir := extraParentLookupDir(candidate)
	if candidate.SupplementalDir == "" {
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
	}
	if rootSet[filepath.Clean(dir)] {
		// The extra (or its supplemental dir) sits at the library root — no
		// single owner.
		return "", nil
	}
	return s.fileRepo.FindUnambiguousParentContentIDForDir(ctx, folderID, dir, excludePaths)
}

// extraParentLookupDir is the directory whose single item owns the extra: the
// title folder above a supplemental dir, or the file's own directory for a
// suffix-classified extra.
func extraParentLookupDir(candidate extraCandidate) string {
	if candidate.SupplementalDir == "" {
		return filepath.Dir(candidate.Path)
	}
	return firstNonSupplementalAncestor(candidate.SupplementalDir)
}

// extraFileUnchanged reports whether an extra's row already describes the file
// on disk, so a scan can leave it alone. A row ffprobe rejected for these same
// bytes counts as unchanged too: probing them again would only fail again.
func extraFileUnchanged(existing *scanStateFile, extraID string, fileSize int64, fileModifiedAt time.Time) bool {
	return existing != nil && existing.ExtraID == extraID &&
		existing.FileSize == fileSize &&
		existing.FileModifiedAt != nil && existing.FileModifiedAt.Equal(fileModifiedAt) &&
		(existing.ProbeUpdatedAt != nil || probeRejectionStands(existing, fileSize, fileModifiedAt)) &&
		existing.MissingSince == nil
}
