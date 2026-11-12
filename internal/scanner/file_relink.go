package scanner

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// relinkMovedFile gives a file that appeared at filePath the row of the file it
// was before a rename or move, so the state keyed to the row's id (markers,
// subtitles and their timing, trickplay, chapter images, downloads) follows the
// file instead of staying on a missing row until the trash sweep deletes it.
//
// A row qualifies when it is in the same library, recorded the same size,
// OSHash and modification time, and its old path is gone from a root that is
// reachable and not empty. When several rows qualify, the one whose old path
// parses to the same title as the new path wins; if that does not single one
// out, nothing is relinked. A copy whose original is still in place and a file
// under an offline root are new files as before. The bytes are compared by
// OSHash, which samples the head and tail only, so this is a strong heuristic
// rather than proof.
//
// What the new path says about the title decides the row's links. They stay
// when it parses to the same title, or when its provider ID tags agree with
// the IDs of the title the row is linked to and the episode it names is
// unchanged. Otherwise the row is retitled: it drops its links and goes back
// through matching like a new file, and a missing stand-in row left at the old
// path keeps the old title's links, so that title is held and cleaned up
// exactly as when its file goes missing (see FileRepository.RetitleMovedFile).
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
	relinked, err := s.tryRelinkMovedFile(ctx, folder, filePath, fileSize, fileModifiedAt, fileHash, assignment, groupAssignment, loadExternalSubs, nil)
	if err != nil {
		slog.WarnContext(ctx, "scanner: could not check whether a new file was moved", "component", "scanner",
			"folder_id", folder.ID,
			"path", filePath,
			"error", err,
		)
	}
	return relinked
}

// reclaimStandIn handles a file that appeared at a path held by a retitle's
// stand-in row. When the file is the one the stand-in stands for, back from a
// rename that is now undone, that row takes the path again with the stand-in's
// links (FileRepository.ReclaimStandIn), so nothing derived from its bytes is
// lost. If the missing row the scan read at the path is gone by then, the row
// takes the path the ordinary way. Any other file reports false and is written
// onto the stand-in like any file appearing at a missing row's path.
func (s *Scanner) reclaimStandIn(
	ctx context.Context,
	folder *models.MediaFolder,
	filePath string,
	standIn *scanStateFile,
	fileSize int64,
	fileModifiedAt time.Time,
	assignment fileRootAssignment,
	groupAssignment fileGroupAssignment,
	loadExternalSubs func() []ExternalSubtitleInfo,
) bool {
	standInFor, exists, err := s.fileRepo.StandInFor(ctx, standIn.ID)
	switch {
	case err != nil:
	case !exists:
		// The missing row this scan read is gone, perhaps a stand-in the
		// trash sweep took as its file came back; the path is free, so the
		// file looks for its row as any new file does.
		return s.relinkMovedFile(ctx, folder, filePath, fileSize, fileModifiedAt, s.gatherHints(filePath).FileHash, assignment, groupAssignment, loadExternalSubs)
	case standInFor <= 0:
		return false
	default:
		fileHash := s.gatherHints(filePath).FileHash
		if fileHash == "" {
			return false
		}
		claim := &standInClaim{row: standIn, forFileID: standInFor}
		// A match can link the row between the candidate read and the
		// reclaim's fence; the second attempt reads the row as it is now.
		for range 2 {
			var reclaimed bool
			reclaimed, err = s.tryRelinkMovedFile(ctx, folder, filePath, fileSize, fileModifiedAt, fileHash, assignment, groupAssignment, loadExternalSubs, claim)
			if err == nil {
				return reclaimed
			}
			if !errors.Is(err, errStandInFenceLost) {
				break
			}
		}
	}
	slog.WarnContext(ctx, "scanner: could not check whether a file returned to its stand-in", "component", "scanner",
		"folder_id", folder.ID,
		"path", filePath,
		"error", err,
	)
	return false
}

// errStandInFenceLost reports a reclaim whose row changed after it was read
// while the stand-in still holds the path.
var errStandInFenceLost = errors.New("moved row changed before it could take its stand-in's path")

// standInClaim is the stand-in row at the path a file appeared at, and the row
// it stands in for.
type standInClaim struct {
	row       *scanStateFile
	forFileID int
}

// movedFileCandidate is a row whose file left its path with the same bytes as
// the new file. sameTitle reports whether its old path parses to the title the
// new path does.
type movedFileCandidate struct {
	file      *scanStateFile
	identity  pathMatchIdentity
	sameTitle bool
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
	standIn *standInClaim,
) (bool, error) {
	candidates, err := s.fileRepo.MovedFileCandidates(ctx, folder.ID, filePath, fileSize, fileHash)
	if err != nil {
		return false, err
	}
	isStandIn := func(candidate *scanStateFile) bool { return standIn != nil && candidate.ID == standIn.row.ID }
	if slices.ContainsFunc(candidates, func(candidate *scanStateFile) bool {
		return candidate.FilePath == filePath && !isStandIn(candidate)
	}) {
		// Another scan wrote this path after this scan read its snapshot,
		// possibly by relinking a moved file here. Inserting it as new would
		// overwrite that row's chapters and edition with a fresh probe.
		return true, nil
	}
	candidates = slices.DeleteFunc(candidates, func(candidate *scanStateFile) bool {
		return isStandIn(candidate) || !sameFileModifiedAt(candidate.FileModifiedAt, fileModifiedAt)
	})
	if len(candidates) == 0 {
		return false, nil
	}
	configuredPaths, err := s.configuredFolderPaths(ctx, folder)
	if err != nil {
		return false, err
	}
	newIdentity := pathMatchIdentityOf(filePath, folder.Type, assignment.LibraryRootPath)
	vanished := make([]movedFileCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		libraryRoot := scopeLibraryRoot(candidate.FilePath, configuredPaths)
		if libraryRoot == "" || !fileGoneFromHealthyRoot(ctx, candidate.FilePath, libraryRoot) {
			continue
		}
		identity := pathMatchIdentityOf(candidate.FilePath, folder.Type, libraryRoot)
		vanished = append(vanished, movedFileCandidate{
			file:      candidate,
			identity:  identity,
			sameTitle: sameTitle(candidate, identity, groupAssignment, newIdentity),
		})
	}
	var picked movedFileCandidate
	var ok bool
	if standIn != nil {
		// Only the row the stand-in stands for may take its path back; any
		// other file there is new to the stand-in's title.
		i := slices.IndexFunc(vanished, func(candidate movedFileCandidate) bool { return candidate.file.ID == standIn.forFileID })
		if i >= 0 {
			picked, ok = vanished[i], true
		}
	} else {
		picked, ok = pickMovedFile(vanished)
	}
	if !ok {
		return false, nil
	}
	moved := picked.file

	mf := models.MediaFile{MediaFolderID: folder.ID, FilePath: filePath}
	mf.ExternalSubtitles = externalSubtitleModels(loadExternalSubs())
	var relinked, keepLinks, reclaimed bool
	if standIn != nil {
		populateScanIdentity(&mf, filePath, folder.Type, assignment, groupAssignment, nil)
		var standInGone bool
		reclaimed, standInGone, err = s.fileRepo.ReclaimStandIn(ctx, standIn.row.ID, moved, mf)
		switch {
		case err != nil:
			return false, err
		case reclaimed:
			relinked = true
		case standInGone:
			// The trash sweep took the stand-in as its file came back, so
			// the row takes the free path the ordinary way.
			standIn = nil
			mf = models.MediaFile{MediaFolderID: folder.ID, FilePath: filePath, ExternalSubtitles: mf.ExternalSubtitles}
		default:
			return false, errStandInFenceLost
		}
	}
	if !reclaimed {
		relinked, keepLinks, err = s.relinkOrRetitle(ctx, folder, &mf, picked, newIdentity, assignment, groupAssignment)
		if err != nil {
			return false, err
		}
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
		"rematch", !reclaimed && !keepLinks,
		"reclaimed", reclaimed,
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

// relinkOrRetitle moves picked's row to mf.FilePath, keeping its links when
// the new path names its title and retitling it otherwise.
func (s *Scanner) relinkOrRetitle(
	ctx context.Context,
	folder *models.MediaFolder,
	mf *models.MediaFile,
	picked movedFileCandidate,
	newIdentity pathMatchIdentity,
	assignment fileRootAssignment,
	groupAssignment fileGroupAssignment,
) (relinked, keepLinks bool, err error) {
	moved := picked.file
	keepLinks = picked.sameTitle
	if !keepLinks {
		keepLinks, err = s.tagsNameLinkedTitle(ctx, moved, picked.identity, newIdentity, groupAssignment.BaseType, mf.FilePath, assignment.LibraryRootPath)
		if err != nil {
			return false, false, err
		}
	}
	if keepLinks {
		populateScanIdentity(mf, mf.FilePath, folder.Type, assignment, groupAssignment, moved)
		relinked, err = s.fileRepo.RelinkMovedFile(ctx, moved, *mf)
		return relinked, true, err
	}
	// The variant hints describe the new title, so none come from the old
	// row's import.
	populateScanIdentity(mf, mf.FilePath, folder.Type, assignment, groupAssignment, nil)
	relinked, err = s.fileRepo.RetitleMovedFile(ctx, moved, *mf)
	return relinked, false, err
}

// sameTitle reports whether a vanished row's old path names the title the new
// path does. Rows stored under an older group key scheme cannot be compared by
// key with the current scan's, which would read every rename in the first scan
// after a scheme change as a title change, so for them the path identity
// decides alone.
func sameTitle(candidate *scanStateFile, identity pathMatchIdentity, assignment fileGroupAssignment, newIdentity pathMatchIdentity) bool {
	if candidate.BaseType != assignment.BaseType || identity != newIdentity {
		return false
	}
	return candidate.GroupKeyVersion != assignment.GroupKeyVersion || candidate.ContentGroupKey == assignment.ContentGroupKey
}

// pickMovedFile chooses the row a new file takes over among the vanished rows
// with its bytes. One row is the file. Among several, a row whose old path
// names the same title is the better claim, but only when exactly one does:
// picking between equal claims could hand one file's state to the other.
func pickMovedFile(vanished []movedFileCandidate) (movedFileCandidate, bool) {
	if len(vanished) == 1 {
		return vanished[0], true
	}
	var picked movedFileCandidate
	found := false
	for _, candidate := range vanished {
		if !candidate.sameTitle {
			continue
		}
		if found {
			return movedFileCandidate{}, false
		}
		picked, found = candidate, true
	}
	return picked, found
}

// tagsNameLinkedTitle reports whether a rename that changed the parsed title
// still leaves the row's links right: the provider ID tags on the new path
// agree with the provider IDs stored on the title the row is linked to (at
// least one tagged provider equals the title's ID for it and none differs),
// the tags on the path agree with each other, and the path names the same
// episode as before (or none, for a movie). Adding {tmdb-…}, {imdb-…}, or
// {tvdb-…} tags to a correctly matched library is the common case, and keeping
// its links spares a rematch that would only find the same title and, on the
// way, clear the file's detected markers.
func (s *Scanner) tagsNameLinkedTitle(ctx context.Context, moved *scanStateFile, oldIdentity, newIdentity pathMatchIdentity, baseType, filePath, libraryRoot string) (bool, error) {
	if moved.ContentID == "" || moved.BaseType != baseType || !sameEpisodeIdentity(oldIdentity, newIdentity) {
		return false, nil
	}
	tagged, ok := pathProviderIDs(filePath, libraryRoot)
	if !ok {
		return false, nil
	}
	stored, found, err := s.fileRepo.ItemProviderIDs(ctx, moved.ContentID)
	if err != nil || !found {
		return false, err
	}
	return providerIDsAgree(tagged, stored), nil
}

// providerIDsAgree reports whether at least one provider tagged on a path
// carries the title's stored ID and no tagged provider carries another one.
// A provider the title has no ID for neither agrees nor conflicts.
func providerIDsAgree(tagged, stored contentid.ProviderIDs) bool {
	agreed := false
	for _, pair := range [][2]string{{tagged.Tmdb, stored.Tmdb}, {tagged.Imdb, stored.Imdb}, {tagged.Tvdb, stored.Tvdb}} {
		tag, have := strings.TrimSpace(pair[0]), strings.TrimSpace(pair[1])
		if tag == "" || have == "" {
			continue
		}
		if !strings.EqualFold(tag, have) {
			return false
		}
		agreed = true
	}
	return agreed
}

// sameEpisodeIdentity compares the parts of two path identities that pick an
// episode within a title.
func sameEpisodeIdentity(a, b pathMatchIdentity) bool {
	return a.hints.SeasonNum == b.hints.SeasonNum &&
		a.hints.SeasonKnown == b.hints.SeasonKnown &&
		a.hints.EpisodeNum == b.hints.EpisodeNum &&
		a.hints.AirDate == b.hints.AirDate &&
		a.episodeStart == b.episodeStart &&
		a.episodeEnd == b.episodeEnd
}

// pathProviderIDs merges the provider ID tags on every path component below
// the library root. It reports false when two components tag the same
// provider with different IDs, since the path then names no single title.
func pathProviderIDs(filePath, libraryRoot string) (contentid.ProviderIDs, bool) {
	var ids contentid.ProviderIDs
	for _, hints := range pathFolderIDs(filePath, libraryRoot) {
		for _, tag := range []struct {
			into  *string
			value string
		}{{&ids.Tmdb, hints.TmdbID}, {&ids.Imdb, hints.ImdbID}, {&ids.Tvdb, hints.TvdbID}} {
			if tag.value == "" {
				continue
			}
			if *tag.into != "" && *tag.into != tag.value {
				return contentid.ProviderIDs{}, false
			}
			*tag.into = tag.value
		}
	}
	return ids, true
}

// pathFolderIDs returns the provider ID tags parsed from each component of the
// path below the library root, the file name without its extension included.
func pathFolderIDs(filePath, libraryRoot string) []*naming.FolderIDHints {
	rel, err := filepath.Rel(libraryRoot, filePath)
	if err != nil {
		rel = filePath
	}
	rel = strings.TrimSuffix(rel, filepath.Ext(rel))
	var out []*naming.FolderIDHints
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if hints := naming.ParseFolderIDs(part); hints != nil {
			out = append(out, hints)
		}
	}
	return out
}

// pathMatchIdentity is what the metadata matchers read from a file's path
// besides its content group: the parsed filename hints, the episode range, and
// the provider ID tags on the path below the library root. Two paths with the
// same value match to the same title, so a file moving between them can keep
// its links. The range is parsed from the path alone: a range an import
// recorded on the old row says nothing about what the new name declares.
type pathMatchIdentity struct {
	hints                    naming.FilenameHints
	episodeStart, episodeEnd int
	ids                      string
}

func pathMatchIdentityOf(filePath, folderType, libraryRoot string) pathMatchIdentity {
	var identity pathMatchIdentity
	if hints := naming.ParseFilename(filePath, folderType, libraryRoot); hints != nil {
		identity.hints = *hints
	}
	if variant := naming.ParseVariantHints(filePath, folderType, libraryRoot); variant != nil {
		identity.episodeStart, identity.episodeEnd = variant.MultiEpisodeStart, variant.MultiEpisodeEnd
	}
	var ids []string
	for _, hints := range pathFolderIDs(filePath, libraryRoot) {
		ids = append(ids, "tmdb:"+hints.TmdbID, "imdb:"+hints.ImdbID, "tvdb:"+hints.TvdbID)
	}
	slices.Sort(ids)
	identity.ids = strings.Join(slices.Compact(ids), ",")
	return identity
}

// fileGoneFromHealthyRoot reports whether filePath no longer exists under a
// library root that answers and still holds entries. An unreachable root, or
// an empty one (the signature of a lost mount), says nothing about whether the
// file is still there, so it never counts as gone. Both checks go through
// rootcheck's bounded, per-path coalesced probe, so a dead mount anywhere on
// the path cannot hang a scan worker; a probe that times out does not count
// as gone.
func fileGoneFromHealthyRoot(ctx context.Context, filePath, libraryRoot string) bool {
	root := rootcheck.ProbeWithTimeout(ctx, libraryRoot, rootcheck.DefaultProbeTimeout)
	if !root.Reachable || root.Empty {
		return false
	}
	file := rootcheck.ProbeWithTimeout(ctx, filePath, rootcheck.DefaultProbeTimeout)
	return file.ErrorCode == rootcheck.ErrCodeNotFound
}
