package scanner

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// relinkMovedFile gives a file that appeared at filePath the row of the file it
// was before a rename or move, so the state keyed to the row's id (markers,
// subtitles and their timing, trickplay, downloads) follows the file instead
// of staying on a missing row until the trash sweep deletes it.
//
// A row qualifies when it is in the same library, recorded the same size,
// OSHash and modification time, would be matched to the same title from its
// new path, and its old path is gone from a root that is reachable and not
// empty. Exactly one row must qualify. Everything else, including a copy whose
// original is still in place, a rename to another title, and a file under an
// offline root, is a new file as before. The bytes are compared by OSHash,
// which samples the head and tail only, so this is a strong heuristic rather
// than proof.
//
// It reports whether the file now has a row. A failure here only costs the
// relink, so it is logged and the caller ingests the file as new.
func (s *Scanner) relinkMovedFile(
	ctx context.Context,
	folder *models.MediaFolder,
	filePath string,
	fileSize int64,
	fileModifiedAt time.Time,
	fileHash string,
	assignment fileRootAssignment,
	groupAssignment fileGroupAssignment,
	loadExternalSubs func() []ExternalSubtitleInfo,
) bool {
	if fileHash == "" {
		return false
	}
	relinked, err := s.tryRelinkMovedFile(ctx, folder, filePath, fileSize, fileModifiedAt, fileHash, assignment, groupAssignment, loadExternalSubs)
	if err != nil {
		slog.WarnContext(ctx, "scanner: could not check whether a new file was moved", "component", "scanner",
			"folder_id", folder.ID,
			"path", filePath,
			"error", err,
		)
	}
	return relinked
}

func (s *Scanner) tryRelinkMovedFile(
	ctx context.Context,
	folder *models.MediaFolder,
	filePath string,
	fileSize int64,
	fileModifiedAt time.Time,
	fileHash string,
	assignment fileRootAssignment,
	groupAssignment fileGroupAssignment,
	loadExternalSubs func() []ExternalSubtitleInfo,
) (bool, error) {
	candidates, err := s.fileRepo.MovedFileCandidates(ctx, folder.ID, filePath, fileSize, fileHash)
	if err != nil {
		return false, err
	}
	candidates = slices.DeleteFunc(candidates, func(candidate *scanStateFile) bool {
		return !sameFileModifiedAt(candidate.FileModifiedAt, fileModifiedAt) ||
			candidate.ContentGroupKey != groupAssignment.ContentGroupKey ||
			candidate.GroupKeyVersion != groupAssignment.GroupKeyVersion ||
			candidate.BaseType != groupAssignment.BaseType
	})
	if len(candidates) == 0 {
		return false, nil
	}
	configuredPaths, err := s.configuredFolderPaths(ctx, folder)
	if err != nil {
		return false, err
	}
	newIdentity := pathMatchIdentityOf(filePath, folder.Type, assignment.LibraryRootPath)
	var moved *scanStateFile
	var mf models.MediaFile
	for _, candidate := range candidates {
		libraryRoot := scopeLibraryRoot(candidate.FilePath, configuredPaths)
		if libraryRoot == "" || pathMatchIdentityOf(candidate.FilePath, folder.Type, libraryRoot) != newIdentity {
			continue
		}
		// A changed episode range (S01E01 renamed to S01E01-E02) links to
		// other episodes, so it is matched afresh like any other new title.
		relinked := models.MediaFile{MediaFolderID: folder.ID, FilePath: filePath}
		populateScanIdentity(&relinked, filePath, folder.Type, assignment, groupAssignment, candidate)
		if relinked.MultiEpisodeStart != candidate.MultiEpisodeStart || relinked.MultiEpisodeEnd != candidate.MultiEpisodeEnd {
			continue
		}
		if !fileGoneFromHealthyRoot(ctx, candidate.FilePath, libraryRoot) {
			continue
		}
		if moved != nil {
			// Two vanished files look like this one; picking either could
			// hand one file's state to the other.
			return false, nil
		}
		moved, mf = candidate, relinked
	}
	if moved == nil {
		return false, nil
	}

	mf.ExternalSubtitles = externalSubtitleModels(loadExternalSubs())
	relinked, err := s.fileRepo.RelinkMovedFile(ctx, moved, mf)
	if err != nil {
		return false, err
	}
	if !relinked {
		// Another scan of this path may have taken the row first. Writing
		// the file as new would then overwrite that row's chapters and
		// edition with a fresh probe.
		_, err := s.fileRepo.GetByPath(ctx, filePath)
		if errors.Is(err, ErrFileNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	mf.ID = moved.ID
	slog.InfoContext(ctx, "scanner: moved file kept its row", "component", "scanner",
		"folder_id", folder.ID,
		"file_id", moved.ID,
		"from", moved.FilePath,
		"to", filePath,
	)
	if err := s.enqueueMetadataWork(ctx, folder, &mf); err != nil {
		// The row has moved; a failed enqueue must not make the caller
		// insert the file a second time. The scan's queue sync covers it.
		slog.WarnContext(ctx, "scanner: enqueueing metadata work for moved file failed", "component", "scanner",
			"folder_id", folder.ID,
			"path", filePath,
			"error", err,
		)
	}
	return true, nil
}

// pathMatchIdentity is what the metadata matchers read from a file's path
// besides its content group: the parsed filename hints and the provider ID
// tags on the path below the library root. Two paths with the same value match
// to the same title, so a file moving between them can keep its links.
type pathMatchIdentity struct {
	hints naming.FilenameHints
	ids   string
}

func pathMatchIdentityOf(filePath, folderType, libraryRoot string) pathMatchIdentity {
	var identity pathMatchIdentity
	if hints := naming.ParseFilename(filePath, folderType, libraryRoot); hints != nil {
		identity.hints = *hints
	}
	rel, err := filepath.Rel(libraryRoot, filePath)
	if err != nil {
		rel = filePath
	}
	rel = strings.TrimSuffix(rel, filepath.Ext(rel))
	var ids []string
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if hints := naming.ParseFolderIDs(part); hints != nil {
			ids = append(ids, "tmdb:"+hints.TmdbID, "imdb:"+hints.ImdbID, "tvdb:"+hints.TvdbID)
		}
	}
	slices.Sort(ids)
	identity.ids = strings.Join(slices.Compact(ids), ",")
	return identity
}

// fileGoneFromHealthyRoot reports whether filePath no longer exists under a
// library root that answers and still holds entries. An unreachable root, or
// an empty one (the signature of a lost mount), says nothing about whether the
// file is still there, so it never counts as gone. The root is probed first
// so the stat of a path on a dead mount cannot hang the scan.
func fileGoneFromHealthyRoot(ctx context.Context, filePath, libraryRoot string) bool {
	probe := rootcheck.ProbeWithTimeout(ctx, libraryRoot, rootcheck.DefaultProbeTimeout)
	if !probe.Reachable || probe.Empty {
		return false
	}
	_, err := os.Lstat(filePath)
	return errors.Is(err, os.ErrNotExist)
}
