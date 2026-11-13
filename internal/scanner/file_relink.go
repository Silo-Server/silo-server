package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
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
// takes the path the ordinary way.
//
// It reports false only for a file that is certainly not the stand-in's row:
// other bytes, another modification time, or a copy whose original is still in
// place. The caller writes such a file onto the stand-in like any file
// appearing at a missing row's path. When it cannot tell (a database error,
// an old path it cannot probe, or a row that keeps changing under the fence)
// it returns an error, and the caller leaves the file for a later scan:
// writing it onto the stand-in would clear the stand-in and leave the row
// missing for good.
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
) (bool, error) {
	standInFor, exists, err := s.fileRepo.StandInFor(ctx, standIn.ID)
	switch {
	case err != nil:
		return false, fmt.Errorf("checking stand-in at %s: %w", filePath, err)
	case !exists:
		// The missing row this scan read is gone, perhaps a stand-in the
		// trash sweep took as its file came back; the path is free, so the
		// file looks for its row as any new file does.
		return s.relinkMovedFile(ctx, folder, filePath, fileSize, fileModifiedAt, s.gatherHints(filePath).FileHash, assignment, groupAssignment, loadExternalSubs), nil
	case standInFor <= 0 || fileSize != standIn.FileSize:
		// No stand-in, or not the bytes the stand-in's row recorded.
		return false, nil
	}
	fileHash := s.gatherHints(filePath).FileHash
	if fileHash == "" {
		return false, fmt.Errorf("hashing %s for its stand-in: %w", filePath, errStandInUndecided)
	}
	claim := &standInClaim{row: standIn, forFileID: standInFor}
	// A match can link the row between the candidate read and the reclaim's
	// fence; the second attempt reads the row as it is now.
	for range 2 {
		var reclaimed bool
		reclaimed, err = s.tryRelinkMovedFile(ctx, folder, filePath, fileSize, fileModifiedAt, fileHash, assignment, groupAssignment, loadExternalSubs, claim)
		if err == nil {
			return reclaimed, nil
		}
		if !errors.Is(err, errStandInFenceLost) {
			break
		}
	}
	return false, fmt.Errorf("reclaiming stand-in at %s: %w", filePath, err)
}

// seriesBaseType is the base type of a series file.
const seriesBaseType = "series"

// errStandInFenceLost reports a reclaim whose row changed after it was read
// while the stand-in still holds the path.
var errStandInFenceLost = errors.New("moved row changed before it could take its stand-in's path")

// errStandInUndecided reports that a scan could not tell whether a file at a
// stand-in's path is the row the stand-in stands for.
var errStandInUndecided = errors.New("cannot tell whether the file is the one its stand-in stands for")

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
	file        *scanStateFile
	identity    pathMatchIdentity
	libraryRoot string
	sameTitle   bool
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
	same := make([]movedFileCandidate, 0, len(candidates))
	other := make([]movedFileCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		libraryRoot := scopeLibraryRoot(candidate.FilePath, configuredPaths)
		if libraryRoot == "" {
			continue
		}
		identity := pathMatchIdentityOf(candidate.FilePath, folder.Type, libraryRoot)
		c := movedFileCandidate{
			file:        candidate,
			identity:    identity,
			libraryRoot: libraryRoot,
			sameTitle:   sameTitle(candidate, identity, groupAssignment, newIdentity),
		}
		if c.sameTitle {
			same = append(same, c)
		} else {
			other = append(other, c)
		}
	}
	probe := newMovedFileProbe()
	var picked movedFileCandidate
	if standIn != nil {
		// Only the row the stand-in stands for may take its path back. A
		// row whose old path still holds the file makes this one a copy, new
		// to the stand-in's title; a path whose state cannot be read decides
		// nothing.
		i := slices.IndexFunc(append(same, other...), func(c movedFileCandidate) bool { return c.file.ID == standIn.forFileID })
		if i < 0 {
			return false, nil
		}
		picked = append(same, other...)[i]
		switch probe.state(ctx, picked) {
		case fileStillThere:
			return false, nil
		case fileStateUnknown:
			return false, errStandInUndecided
		}
	} else {
		var ok bool
		if picked, ok = pickMovedFile(ctx, same, other, probe); !ok {
			return false, nil
		}
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
		next := *mf
		populateScanIdentity(&next, mf.FilePath, folder.Type, assignment, groupAssignment, nil)
		keepLinks, err = s.matchKeepsLinkedTitle(ctx, moved, picked.identity, newIdentity, groupAssignment, &next)
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

// pickMovedFile chooses the row a new file takes over among the rows with its
// bytes whose old paths are gone (see movedFileProbe). One such row is the
// file. Among several, a row whose old path names the same title is the
// better claim, but only when exactly one does: picking between equal claims
// could hand one file's state to the other. Same-title rows are probed first
// and probing stops once the pick is settled, so rows that cannot change it
// cost no file system probe.
func pickMovedFile(ctx context.Context, same, other []movedFileCandidate, probe *movedFileProbe) (movedFileCandidate, bool) {
	picked, found := movedFileCandidate{}, false
	for _, c := range same {
		if probe.state(ctx, c) != fileGone {
			continue
		}
		if found {
			return movedFileCandidate{}, false
		}
		picked, found = c, true
	}
	if found {
		return picked, true
	}
	for _, c := range other {
		if probe.state(ctx, c) != fileGone {
			continue
		}
		if found {
			return movedFileCandidate{}, false
		}
		picked, found = c, true
	}
	return picked, found
}

// movedFileState is what a probe learned about a candidate's old path.
type movedFileState int

const (
	fileStateUnknown movedFileState = iota
	fileGone
	fileStillThere
)

// movedFileProbe probes candidates' old paths for one moved-file lookup,
// probing each library root once.
type movedFileProbe struct {
	roots map[string]bool
}

func newMovedFileProbe() *movedFileProbe {
	return &movedFileProbe{roots: make(map[string]bool)}
}

// state reports whether a candidate's old path is gone from a library root
// that answers and still holds entries. An unreachable root, or an empty one
// (the signature of a lost mount), says nothing about whether the file is
// still there, and neither does a probe that fails or times out. Both checks
// go through rootcheck's bounded, per-path coalesced probe, so a dead mount
// anywhere on the path cannot hang a scan worker.
func (p *movedFileProbe) state(ctx context.Context, c movedFileCandidate) movedFileState {
	healthy, probed := p.roots[c.libraryRoot]
	if !probed {
		root := rootcheck.ProbeWithTimeout(ctx, c.libraryRoot, rootcheck.DefaultProbeTimeout)
		healthy = root.Reachable && !root.Empty
		p.roots[c.libraryRoot] = healthy
	}
	if !healthy {
		return fileStateUnknown
	}
	switch rootcheck.ProbeWithTimeout(ctx, c.file.FilePath, rootcheck.DefaultProbeTimeout).ErrorCode {
	case rootcheck.ErrCodeNotFound:
		return fileGone
	case rootcheck.ErrCodeNotDirectory:
		// The probe expects a directory, so a file that is still there
		// reports as one that is not.
		return fileStillThere
	default:
		return fileStateUnknown
	}
}

// matchKeepsLinkedTitle reports whether a rename that changed the parsed
// title still leaves the row's links right: matching the new path would link
// it to the title it is linked to now, as far as the catalog can tell without
// asking a provider. It follows createOrFindSkeleton in internal/metadata,
// step for step, with the provider IDs the scan stores on the file's group
// (naming.GroupIdentity, the same evidence matching reads):
//
//  1. a confirmed claim on the new content group, then on the new content
//     root, names the title;
//  2. for a series, a title already linked to another file under the new
//     observed root;
//  3. otherwise the tagged IDs: the first catalog item they name
//     (ItemRepository.GetByExternalID), if matched and not contradicted on
//     the first ID both carry in the type's anchor precedence
//     (catalog.IdentityProviderIDsContradict), else a matched item at the
//     tags' anchored content ID.
//
// Whatever cannot be known locally means a rematch: an operator override on
// the new path or group, a series file directly in the library root, a path
// without tags and claims, or a different season, episode, air date, or
// episode range. Adding {tmdb-…}, {imdb-…}, or {tvdb-…} tags to a correctly
// matched library is the common case, and keeping its links spares a rematch
// that would only find the same title and, on the way, clear the file's
// detected markers.
func (s *Scanner) matchKeepsLinkedTitle(ctx context.Context, moved *scanStateFile, oldIdentity, newIdentity pathMatchIdentity, group fileGroupAssignment, next *models.MediaFile) (bool, error) {
	if moved.ContentID == "" || moved.BaseType != group.BaseType || group.Overridden || !sameEpisodeIdentity(oldIdentity, newIdentity) {
		return false, nil
	}
	if override, err := s.groupOverrideRepo.Get(ctx, next.MediaFolderID, next.GroupKeyVersion, next.ContentGroupKey); err != nil || override != nil {
		return false, err
	}
	resolved, err := s.fileRepo.ClaimedContentID(ctx, next.MediaFolderID, next.GroupKeyVersion, next.ContentGroupKey, next.CanonicalRootPath)
	if err != nil || resolved != "" {
		return resolved == moved.ContentID, err
	}
	itemType := strings.ToLower(strings.TrimSpace(group.BaseType))
	if itemType == seriesBaseType {
		if filepath.Clean(next.ObservedRootPath) == filepath.Clean(next.FilePath) {
			return false, nil
		}
		// Matching runs for the row once it is unlinked, so the row itself
		// is never its own sibling.
		resolved, err = s.fileRepo.FindOtherContentIDByObservedRootPath(ctx, next.MediaFolderID, next.ObservedRootPath, itemType, moved.ID)
		if err != nil || resolved != "" {
			return resolved == moved.ContentID, err
		}
	}
	tagged := map[string]string{contentid.ProviderTMDB: group.TmdbID, contentid.ProviderIMDB: group.ImdbID, contentid.ProviderTVDB: group.TvdbID}
	if group.TmdbID == "" && group.ImdbID == "" && group.TvdbID == "" {
		return false, nil
	}
	existing, err := s.itemRepo.GetByExternalID(ctx, group.TmdbID, group.ImdbID, group.TvdbID, itemType)
	switch {
	case errors.Is(err, catalog.ErrItemNotFound):
	case err != nil:
		return false, err
	default:
		identity, matched, found, err := s.fileRepo.ItemIdentity(ctx, existing.ContentID)
		if err != nil {
			return false, err
		}
		if found && matched && !catalog.IdentityProviderIDsContradict(itemType, tagged, identity) {
			return existing.ContentID == moved.ContentID, nil
		}
	}
	ids := contentid.ProviderIDs{Tmdb: group.TmdbID, Imdb: group.ImdbID, Tvdb: group.TvdbID}
	anchored, ok := contentid.ForMovie(ids)
	if itemType == seriesBaseType {
		anchored, ok = contentid.ForSeries(ids)
	}
	if !ok || anchored != moved.ContentID {
		return false, nil
	}
	_, matched, found, err := s.fileRepo.ItemIdentity(ctx, anchored)
	return found && matched, err
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
