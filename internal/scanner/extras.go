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

// classifyExtraPath reports whether the walked path is a local extra.
//
// Directory names win over filename suffixes. For non-movie libraries a file
// carrying a parseable SxxExx episode token is never an extra: series
// "Extras/SxxExx" files keep their documented season-0 mapping.
func classifyExtraPath(path, folderType string) (extraCandidate, bool) {
	candidate := extraCandidate{Path: path}

	dir := filepath.Dir(path)
	for depth := 0; depth < extrasDirAncestorDepth; depth++ {
		label := normalizeScannerDirLabel(filepath.Base(dir))
		if kind, ok := extrasDirKinds[label]; ok {
			candidate.Kind = kind
			candidate.SupplementalDir = dir
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
	if !librarykind.IsMovie(folderType) {
		if hints := naming.ParseFilename(path, folderType); hints != nil &&
			hints.Type == "series" && hints.EpisodeNum > 0 {
			return extraCandidate{}, false
		}
	}

	return candidate, true
}

// partitionExtraPaths splits walked paths into primary content and extras.
// Primary paths feed the existing root/group inference and matching pipeline
// untouched; extras are processed separately and never influence identity.
func partitionExtraPaths(paths []string, folderType string) ([]string, []extraCandidate) {
	primary := paths[:0:0]
	var extras []extraCandidate
	for _, p := range paths {
		if candidate, ok := classifyExtraPath(p, folderType); ok {
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
	walkRoots []string,
	extras []extraCandidate,
	existingByPath map[string]*scanStateFile,
) extrasScanStats {
	var stats extrasScanStats
	if len(extras) == 0 || s.extraRepo == nil {
		return stats
	}

	rootSet := make(map[string]bool, len(walkRoots))
	for _, root := range walkRoots {
		rootSet[filepath.Clean(root)] = true
	}

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

	parentDir := filepath.Dir(candidate.SupplementalDir)
	// Walk supplemental nesting ("Extras/Behind The Scenes/") up to the first
	// non-supplemental ancestor.
	for extrasDirKinds[normalizeScannerDirLabel(filepath.Base(parentDir))] != "" {
		next := filepath.Dir(parentDir)
		if next == parentDir {
			break
		}
		parentDir = next
	}
	if rootSet[filepath.Clean(parentDir)] {
		// Supplemental dir sits at the library root — no single owner.
		return "", nil
	}
	return s.fileRepo.FindUnambiguousParentContentIDForDir(ctx, folderID, parentDir)
}
