package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// Local sidecar change detection: an edited NFO or a replaced poster reaches a
// matched item without a manual refresh.
//
// Each item, season and episode stores, per library, a fingerprint of the
// sidecar files the local provider applied to it (path, size and modification
// time). Items also store a digest of every sidecar-type file in their
// folders. A scan compares that digest with the directory listing it already
// read, so an unchanged item costs no provider lookup. When the digest
// differs, the provider's lookups run again; when the applied files'
// fingerprint differs, the local metadata is re-applied.
//
// An item seen for the first time is only recorded: detection reacts to edits
// made after Silo first saw an item's files. A season or episode that appears
// later in an item already recorded is new, so its local metadata applies. A
// fingerprint advances only after its re-apply succeeds, so a failure is
// retried by the next check.

// LocalSidecarProvider is implemented by a provider that reads local sidecar
// files (the builtin NFO provider). Each method returns the files that the
// provider's GetMetadata and GetImages, GetSeasons, or GetEpisodes apply for
// the same request.
type LocalSidecarProvider interface {
	ItemSidecarPaths(ctx context.Context, req MetadataRequest) LocalSidecars
	SeasonSidecarPaths(ctx context.Context, req SeasonsRequest) map[int]LocalSidecars
	EpisodeSidecarPaths(ctx context.Context, req EpisodesRequest) map[int]LocalSidecars
}

// LocalSidecars are the sidecar files a local provider applies to one target.
type LocalSidecars struct {
	// Metadata are the documents whose values apply (an NFO).
	Metadata []string
	// Artwork are the images that apply.
	Artwork []string
	// MetadataErr reports a metadata document that exists but cannot be
	// read and that may be the one that should apply: until it can be read,
	// the metadata files must not read as changed or removed.
	MetadataErr error
}

// localSidecarSource is a chain provider that reads local sidecar files.
type localSidecarSource interface {
	Provider
	LocalSidecarProvider
}

// localSidecarUpsertBatch bounds how many records a scan check holds before
// storing them.
const localSidecarUpsertBatch = 500

// emptySidecarFingerprint is the fingerprint of a target with no sidecars.
var emptySidecarFingerprint = hashSidecarEntries(nil)

// errLocalSidecarBusy reports that another check, on this node or another,
// held an item's lock for longer than LockItem waits. Nothing is stored for
// the item; the next check looks again.
var errLocalSidecarBusy = errors.New("another check is applying this item's local metadata")

// SetLocalSidecarStore enables local sidecar change detection.
func (s *MetadataService) SetLocalSidecarStore(store *LocalSidecarRepository) {
	if s == nil || store == nil {
		return
	}
	s.localSidecars = store
}

// CheckLocalSidecars re-applies the local metadata of matched items with files
// under scopePath in a library when their sidecar files changed since it was
// last applied. It returns the content IDs of the items whose local metadata
// (their own, or their seasons' and episodes') it re-applied.
//
// listing is what the scan read, nil when it read no directories. An item
// whose folders the listing covers and whose sidecars match the stored digest
// is skipped. One item's failure is logged and does not stop the others.
func (s *MetadataService) CheckLocalSidecars(ctx context.Context, folderID int, scopePath string, listing scanner.SidecarListing) ([]string, error) {
	if s == nil || s.localSidecars == nil || folderID <= 0 {
		return nil, nil
	}
	if hasProviders, err := s.libraryHasLocalSidecarProviders(ctx, folderID); err != nil || !hasProviders {
		return nil, err
	}
	contents, err := s.localSidecars.ListScopeContents(ctx, folderID, scopePath)
	if err != nil || len(contents) == 0 {
		return nil, err
	}
	contentIDs := make([]string, 0, len(contents))
	for _, content := range contents {
		contentIDs = append(contentIDs, content.ContentID)
	}
	stored, err := s.localSidecars.Get(ctx, folderID, RefreshTargetItem, contentIDs)
	if err != nil {
		return nil, err
	}
	check := &localSidecarCheck{s: s, listing: listing, digests: make(map[string]string)}
	lookedUp := 0
	for _, content := range contents {
		if err := ctx.Err(); err != nil {
			return check.reapplied, err
		}
		digest, covered := check.folderDigest(content.Dirs)
		record, recorded := stored[content.ContentID]
		if covered && recorded && record.FolderDigest == digest {
			continue
		}
		var previous *localSidecarRecord
		if recorded {
			previous = &record
		}
		// A scan narrower than a recorded item (one season's folder) checks
		// only the seasons and episodes it read, so it keeps the stored folder
		// digest: the next full scan still sees what changed in the folders it
		// skipped. An item seen for the first time is recorded whole, reading
		// the folders the scan did not, so no season or episode outside the
		// scan later reads as new.
		inScope := func(string) bool { return true }
		switch {
		case covered:
		case listing != nil && previous != nil:
			inScope = func(dir string) bool { _, ok := listing[dir]; return ok }
			digest = previous.FolderDigest
		default:
			digest = check.diskFolderDigest(content.Dirs)
		}
		lookedUp++
		if err := check.content(ctx, content, digest, previous, inScope); errors.Is(err, errLocalSidecarBusy) {
			slog.DebugContext(ctx, "metadata: local sidecar check skipped an item another check is applying", "component", "metadata",
				"folder_id", folderID, "content_id", content.ContentID)
		} else if err != nil {
			slog.WarnContext(ctx, "metadata: local sidecar check failed", "component", "metadata",
				"folder_id", folderID, "content_id", content.ContentID, "error", err)
		}
		if len(check.pending) >= localSidecarUpsertBatch {
			if err := check.flush(ctx); err != nil {
				return check.reapplied, err
			}
		}
	}
	if err := check.flush(ctx); err != nil {
		return check.reapplied, err
	}
	if lookedUp > 0 {
		slog.DebugContext(ctx, "metadata: checked local sidecars", "component", "metadata",
			"folder_id", folderID, "scope", scopePath, "items", len(contents), "looked_up", lookedUp, "reapplied", len(check.reapplied))
	}
	return check.reapplied, nil
}

// CheckLocalSidecarTarget re-applies an item's local metadata when its
// sidecar files changed since it was last applied. The scheduled refresh
// calls it for each item target it visits, before its own refresh; season and
// episode targets, and items no scan has recorded yet, are left to scans.
// It returns the target's content ID after the check: contentID, the new one
// when a changed identity ran a refresh that moved the item, or "" when the
// item moved and its new ID is unknown, leaving nothing to refresh.
func (s *MetadataService) CheckLocalSidecarTarget(ctx context.Context, targetType, contentID string) (currentID string, err error) {
	currentID = contentID
	if s == nil || s.localSidecars == nil || NormalizeRefreshTargetType(targetType) != RefreshTargetItem || strings.TrimSpace(contentID) == "" {
		return currentID, nil
	}
	contents, err := s.localSidecars.ListContents(ctx, contentID)
	if err != nil {
		return currentID, err
	}
	for _, content := range contents {
		hasProviders, err := s.libraryHasLocalSidecarProviders(ctx, content.FolderID)
		if err != nil {
			return currentID, err
		}
		if !hasProviders {
			continue
		}
		stored, err := s.localSidecars.Get(ctx, content.FolderID, RefreshTargetItem, []string{contentID})
		if err != nil {
			return currentID, err
		}
		previous, ok := stored[contentID]
		if !ok || !localMediaReachable(content) {
			continue
		}
		item, err := s.itemRepo.GetByID(ctx, contentID)
		if err != nil {
			return currentID, err
		}
		// No listing here: the folder digest stays what the last scan stored.
		// A changed item's record is stored under its lock; an unchanged one
		// has nothing new to store.
		record, outcome, err := s.checkLocalItemSidecars(ctx, content.FolderID, item, previous.FolderDigest, &previous)
		switch {
		case outcome == localReapplyRefreshed:
			if err != nil || record.ContentID != contentID {
				// The refresh moved the item, and its records in every
				// library with it, or whether it did is unknown.
				return record.ContentID, err
			}
			// The refresh kept the content ID; the other libraries' files
			// are checked as usual.
		case errors.Is(err, errLocalSidecarBusy):
			exists, err := s.itemExists(ctx, contentID)
			if err != nil {
				return currentID, err
			}
			if !exists {
				// Another check re-anchored the item meanwhile.
				return "", nil
			}
		case err != nil:
			return currentID, err
		}
	}
	return currentID, nil
}

// localSidecarCheck is one scan check of a library's items: the scan's
// listing, the directory digests computed from it, the records waiting to be
// stored, and the items re-applied.
type localSidecarCheck struct {
	s         *MetadataService
	listing   scanner.SidecarListing
	digests   map[string]string
	pending   []localSidecarRecord
	reapplied []string
}

// content checks one item in one library: the item itself and, for a series,
// its seasons and episodes in scope. folderDigest is the item's current
// folder digest; previous is its stored record, nil when it has none.
func (c *localSidecarCheck) content(
	ctx context.Context,
	content localSidecarContent,
	folderDigest string,
	previous *localSidecarRecord,
	inScope func(dir string) bool,
) error {
	s := c.s
	item, err := s.itemRepo.GetByID(ctx, content.ContentID)
	if err != nil {
		return err
	}
	// An unreadable media file or folder means an offline mount or a deleted
	// file: its sidecars would read as removed. Record nothing until it is back.
	if !strings.EqualFold(strings.TrimSpace(item.Status), "matched") || !localMediaReachable(content) {
		return nil
	}
	record, outcome, err := s.checkLocalItemSidecars(ctx, content.FolderID, item, folderDigest, previous)
	if outcome == localReapplyRefreshed {
		// A full refresh rewrote the seasons and episodes too, and the record
		// was stored under the item's content ID after it, the new one when
		// the refresh re-anchored the item.
		if record.ContentID != "" {
			c.reapplied = append(c.reapplied, record.ContentID)
		}
		return err
	}
	if err != nil {
		return err
	}
	// A merged or newly recorded item's record was stored under its lock and
	// is not stored again here, outside it, where it could overwrite a newer
	// one. A series was stored there without its folder digest, which
	// advances once its seasons and episodes were checked.
	isSeries := strings.EqualFold(item.Type, matchContentTypeSeries)
	merged := outcome == localReapplyMerged
	storedUnderLock := merged || outcome == localReapplyRecorded
	if merged {
		c.reapplied = append(c.reapplied, item.ContentID)
	}
	// Written outside the lock below: a first record only where none exists,
	// any other only while the stored record is still the one read here.
	record.Baseline = previous == nil
	if previous != nil {
		read := previous.Fingerprint
		record.ifFingerprint = &read
	}
	if isSeries {
		children, reapplied, err := s.checkLocalSeriesChildSidecars(ctx, content.FolderID, item, record.ProviderIDs, previous == nil, inScope)
		if err != nil && previous == nil {
			// Recorded without all its children, the series would see the
			// rest as new on the next check: leave it all to that check's
			// first sight.
			return err
		}
		c.pending = append(c.pending, children...)
		if reapplied > 0 && !merged {
			c.reapplied = append(c.reapplied, item.ContentID)
		}
		if err != nil {
			if !storedUnderLock {
				// Without the new digest the next scan checks the children
				// again.
				record.FolderDigest = ""
				c.pending = append(c.pending, record)
			}
			return err
		}
	}
	switch {
	case storedUnderLock && isSeries:
		return s.localSidecars.AdvanceFolderDigest(ctx, record.FolderID, record.ContentID, record.Fingerprint, record.FolderDigest)
	case storedUnderLock:
		return nil
	}
	c.pending = append(c.pending, record)
	return nil
}

// seriesLocalIdentityConflicts reports whether the IDs the series' local
// files declare name a different title than the identity the series
// keeps, by the rule a refresh of one season or episode follows
// (seriesChildSkipsLocal): a stored ID they contradict, or an admin's
// Identify choice they do not confirm. Its season and episode files then
// describe that other title, so none of them apply.
func (s *MetadataService) seriesLocalIdentityConflicts(ctx context.Context, declared map[string]string, series *models.MediaItem) (bool, error) {
	if len(declared) == 0 {
		return false, nil
	}
	return s.seriesIdentityConflict(ctx, series, []localIdentityHint{{ids: declared}})
}

func (c *localSidecarCheck) flush(ctx context.Context) error {
	if len(c.pending) == 0 {
		return nil
	}
	err := c.s.localSidecars.Upsert(ctx, c.pending)
	c.pending = c.pending[:0]
	return err
}

// folderDigest hashes the sidecars the scan listed in dirs, or reports false
// when the scan did not read one of them.
func (c *localSidecarCheck) folderDigest(dirs []string) (string, bool) {
	if c.listing == nil || len(dirs) == 0 {
		return "", false
	}
	entries := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		names, ok := c.listing[dir]
		if !ok {
			return "", false
		}
		entries = append(entries, dir+"\x00"+c.dirDigest(dir, names))
	}
	return hashSidecarEntries(entries), true
}

// diskFolderDigest is folderDigest for an item the scan did not fully cover:
// the folders the scan did not read are listed now. A folder that cannot be
// read counts as empty.
func (c *localSidecarCheck) diskFolderDigest(dirs []string) string {
	entries := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		names, ok := c.listing[dir]
		if !ok {
			names = scanner.ReadSidecarListing([]string{dir})[dir]
		}
		entries = append(entries, dir+"\x00"+c.dirDigest(dir, names))
	}
	return hashSidecarEntries(entries)
}

// dirDigest hashes each named sidecar in dir, once per check: in a flat folder
// every item shares the digest. An entry holds whether the name is a symbolic
// link and the size and modification time of what it names, so editing a
// linked file changes the digest, and so does replacing a link with a copy
// (artwork discovery skips linked images but applies the copy).
func (c *localSidecarCheck) dirDigest(dir string, names []string) string {
	if digest, ok := c.digests[dir]; ok {
		return digest
	}
	entries := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		leaf, err := os.Lstat(path)
		if err != nil {
			continue
		}
		info, linked := leaf, leaf.Mode()&fs.ModeSymlink != 0
		if linked {
			if target, err := os.Stat(path); err == nil {
				info = target
			}
		}
		entries = append(entries, sidecarEntryKey(name, info.Size(), info.ModTime().UnixNano())+"\x00"+strconv.FormatBool(linked))
	}
	digest := hashSidecarEntries(entries)
	c.digests[dir] = digest
	return digest
}

// checkLocalItemSidecars compares an item's applied sidecar files with its
// stored record (previous, nil on first sight) and re-applies its local
// metadata when they changed. It returns the record to store.
func (s *MetadataService) checkLocalItemSidecars(
	ctx context.Context,
	folderID int,
	item *models.MediaItem,
	folderDigest string,
	previous *localSidecarRecord,
) (localSidecarRecord, localReapplyOutcome, error) {
	record := localSidecarRecord{
		FolderID:     folderID,
		TargetType:   RefreshTargetItem,
		ContentID:    item.ContentID,
		ItemID:       item.ContentID,
		Fingerprint:  emptySidecarFingerprint,
		FolderDigest: folderDigest,
	}
	sources, err := s.localSidecarProviders(ctx, folderID, providerChainContentLevel(item.Type))
	if err != nil {
		return record, localReapplyNone, err
	}
	if len(sources) == 0 {
		// Only the seasons or episodes take local metadata; the record keeps
		// the folder digest, and no fingerprint or identity baseline, so a
		// local provider added to this level later starts from first sight.
		record.Fingerprint = ""
		return record, localReapplyNone, nil
	}
	req, imageReq, err := s.localItemRequests(ctx, item, folderID)
	if err != nil {
		return record, localReapplyNone, err
	}
	if record.Fingerprint, err = itemSidecarFingerprint(ctx, sources, req); err != nil {
		return record, localReapplyNone, err
	}
	switch {
	case previous == nil:
		record.ProviderIDs, err = s.localDeclaredProviderIDs(ctx, sources, req)
		return record, localReapplyNone, err
	case previous.Fingerprint == "":
		// A record made while this level had no local provider: the first
		// baseline replaces it under the item's lock, so concurrent checks
		// store one, and the scheduled check stores it too.
		return s.reapplyChangedLocalItem(ctx, item, sources, req, imageReq, record)
	case previous.Fingerprint == record.Fingerprint:
		record.ProviderIDs = previous.ProviderIDs
		return record, localReapplyNone, nil
	}
	return s.reapplyChangedLocalItem(ctx, item, sources, req, imageReq, record)
}

// reapplyChangedLocalItem re-applies an item whose sidecars changed and stores
// its record, both under the item's lock. Under it the record and the files
// are read again: another scan or the scheduled refresh, on any node, may
// have applied the same or a newer change meanwhile.
func (s *MetadataService) reapplyChangedLocalItem(
	ctx context.Context,
	item *models.MediaItem,
	sources localSources,
	req MetadataRequest,
	imageReq ImageRequest,
	record localSidecarRecord,
) (localSidecarRecord, localReapplyOutcome, error) {
	unlock, locked, err := s.localSidecars.LockItem(ctx, record.FolderID, item.ContentID)
	if err != nil {
		return record, localReapplyNone, err
	}
	if !locked {
		return record, localReapplyNone, errLocalSidecarBusy
	}
	defer unlock()
	stored, err := s.localSidecars.Get(ctx, record.FolderID, RefreshTargetItem, []string{item.ContentID})
	if err != nil {
		return record, localReapplyNone, err
	}
	if record.Fingerprint, err = itemSidecarFingerprint(ctx, sources, req); err != nil {
		return record, localReapplyNone, err
	}
	previous, ok := stored[item.ContentID]
	if !ok || previous.Fingerprint == "" {
		if !ok {
			exists, err := s.itemExists(ctx, item.ContentID)
			if err != nil {
				return record, localReapplyNone, err
			}
			if !exists {
				// Another check re-anchored or removed the item while this
				// one waited for the lock; it stored what it applied.
				return record, localReapplyNone, errLocalSidecarBusy
			}
		}
		// The record went away meanwhile (its library row was replaced), or
		// was made while this level had no local provider: record a
		// baseline, as on first sight.
		if record.ProviderIDs, err = s.localDeclaredProviderIDs(ctx, sources, req); err != nil {
			return record, localReapplyNone, err
		}
		return record, localReapplyRecorded, s.storeLockedItemRecord(ctx, item, record, localReapplyRecorded)
	}
	if previous.Fingerprint == record.Fingerprint {
		record.ProviderIDs = previous.ProviderIDs
		return record, localReapplyNone, nil
	}
	outcome, declared, err := s.reapplyLocalItem(ctx, record.FolderID, item, sources, req, imageReq, previous.ProviderIDs)
	if err != nil {
		return record, localReapplyNone, err
	}
	record.ProviderIDs = declared
	if outcome == localReapplyRefreshed {
		exists, err := s.itemExists(ctx, item.ContentID)
		if err != nil {
			// Whether the refresh moved the item is unknown.
			record.ContentID, record.ItemID = "", ""
			return record, outcome, err
		}
		if !exists {
			// The refresh re-anchored the item under another content ID,
			// which its media file now names: the record belongs there.
			newID, err := s.localSidecars.ContentIDOfFile(ctx, record.FolderID, req.RepresentativeFilePath)
			if err != nil || newID == "" {
				record.ContentID, record.ItemID = "", ""
				return record, outcome, err
			}
			record.ContentID, record.ItemID = newID, newID
		}
	}
	return record, outcome, s.storeLockedItemRecord(ctx, item, record, outcome)
}

// storeLockedItemRecord stores an item's record under its lock. A series
// whose seasons and episodes are still to be checked keeps a cleared folder
// digest until they are, so a check stopped in between (a canceled scan, a
// node going down) leaves the next scan to look at them again.
func (s *MetadataService) storeLockedItemRecord(ctx context.Context, item *models.MediaItem, record localSidecarRecord, outcome localReapplyOutcome) error {
	if outcome != localReapplyRefreshed && strings.EqualFold(item.Type, matchContentTypeSeries) {
		record.FolderDigest = ""
	}
	return s.localSidecars.Upsert(ctx, []localSidecarRecord{record})
}

// itemSidecarFingerprint fingerprints the sidecar files the local sources
// apply to an item and change detection tracks.
func itemSidecarFingerprint(ctx context.Context, sources localSources, req MetadataRequest) (string, error) {
	var paths []string
	for _, provider := range sources {
		found, err := provider.ItemSidecarPaths(ctx, req).tracked()
		if err != nil {
			return "", fmt.Errorf("%s: %w", provider.Slug(), err)
		}
		paths = append(paths, found...)
	}
	return localSidecarFingerprint(paths)
}

// checkLocalSeriesChildSidecars compares a series' seasons and episodes in
// scope with their stored records and re-applies the local metadata of those
// whose sidecar files changed. A child with no record is recorded when the
// series is seen for the first time, and otherwise is new, so its local
// metadata applies. Nothing is checked while the IDs the series' files
// declare (declaredIDs) name another title. It returns the records to store
// and how many children it re-applied.
func (s *MetadataService) checkLocalSeriesChildSidecars(
	ctx context.Context,
	folderID int,
	series *models.MediaItem,
	declaredIDs map[string]string,
	seriesFirstSeen bool,
	inScope func(dir string) bool,
) ([]localSidecarRecord, int, error) {
	seasonSources, err := s.localSidecarProviders(ctx, folderID, "season")
	if err != nil {
		return nil, 0, err
	}
	episodeSources, err := s.localSidecarProviders(ctx, folderID, "episode")
	if err != nil {
		return nil, 0, err
	}
	if len(seasonSources) == 0 && len(episodeSources) == 0 {
		return nil, 0, nil
	}
	if conflicts, err := s.seriesLocalIdentityConflicts(ctx, declaredIDs, series); err != nil || conflicts {
		return nil, 0, err
	}
	childCtx, err := s.loadSeriesChildLocalContext(ctx, series.ContentID, folderID)
	if err != nil {
		return nil, 0, err
	}
	seasonIDs, episodeIDs, err := s.localSidecars.SeriesChildren(ctx, series.ContentID)
	if err != nil {
		return nil, 0, err
	}
	children, err := localSidecarChildren(ctx, folderID, series.ContentID, seasonSources, episodeSources, childCtx, seasonIDs, episodeIDs, inScope)
	if err != nil || len(children) == 0 {
		return nil, 0, err
	}

	unchanged, changed, err := s.classifyLocalSidecarChildren(ctx, folderID, children, seriesFirstSeen)
	if err != nil || len(changed) == 0 {
		return unchanged, 0, err
	}

	// Re-apply and store under the series' lock, after reading the records
	// and fingerprinting the changed children again, as for an item: another
	// scan or node may have applied the same or a newer change meanwhile.
	unlock, locked, err := s.localSidecars.LockItem(ctx, folderID, series.ContentID)
	if err != nil {
		return unchanged, 0, err
	}
	if !locked {
		return unchanged, 0, errLocalSidecarBusy
	}
	defer unlock()
	exists, err := s.itemExists(ctx, series.ContentID)
	if err != nil {
		return unchanged, 0, err
	}
	if !exists {
		// A refresh re-anchored the series while this check waited for the
		// lock; it rewrote the seasons and episodes too.
		return unchanged, 0, errLocalSidecarBusy
	}
	changedSeasonIDs, changedEpisodeIDs := localSidecarChildIDs(changed)
	if changed, err = localSidecarChildren(ctx, folderID, series.ContentID, seasonSources, episodeSources, childCtx, changedSeasonIDs, changedEpisodeIDs, inScope); err != nil {
		return unchanged, 0, err
	}
	recorded, changed, err := s.classifyLocalSidecarChildren(ctx, folderID, changed, seriesFirstSeen)
	unchanged = append(unchanged, recorded...)
	if err != nil || len(changed) == 0 {
		return unchanged, 0, err
	}
	var changedSeasons []int
	var changedEpisodes []episodeResultKey
	records := make([]localSidecarRecord, 0, len(changed))
	for _, c := range changed {
		records = append(records, c.record)
		if c.record.TargetType == RefreshTargetSeason {
			changedSeasons = append(changedSeasons, c.key.seasonNumber)
		} else {
			changedEpisodes = append(changedEpisodes, c.key)
		}
	}
	if err := s.reapplyLocalSeriesChildren(ctx, folderID, series, childCtx, seasonSources, episodeSources, changedSeasons, changedEpisodes); err != nil {
		return unchanged, 0, err
	}
	return unchanged, len(changed), s.localSidecars.Upsert(ctx, records)
}

// classifyLocalSidecarChildren compares children with their stored records.
// It returns the records to store as they are (first sight, or no sidecars)
// and the children whose local metadata must be applied.
func (s *MetadataService) classifyLocalSidecarChildren(
	ctx context.Context,
	folderID int,
	children []localSidecarChild,
	seriesFirstSeen bool,
) ([]localSidecarRecord, []localSidecarChild, error) {
	stored := make(map[string]map[string]localSidecarRecord, 2)
	for _, targetType := range []string{RefreshTargetSeason, RefreshTargetEpisode} {
		var ids []string
		for _, c := range children {
			if c.record.TargetType == targetType {
				ids = append(ids, c.record.ContentID)
			}
		}
		records, err := s.localSidecars.Get(ctx, folderID, targetType, ids)
		if err != nil {
			return nil, nil, err
		}
		stored[targetType] = records
	}
	var unchanged []localSidecarRecord
	var changed []localSidecarChild
	for _, c := range children {
		previous, ok := stored[c.record.TargetType][c.record.ContentID]
		switch {
		case ok && previous.Fingerprint == c.record.Fingerprint:
		case seriesFirstSeen || !ok && c.record.Fingerprint == emptySidecarFingerprint:
			// First sight records only, even over a child stored before;
			// outside the lock, so only over the record read here.
			c.record.Baseline = !ok
			if ok {
				read := previous.Fingerprint
				c.record.ifFingerprint = &read
			}
			unchanged = append(unchanged, c.record)
		default:
			changed = append(changed, c)
		}
	}
	return unchanged, changed, nil
}

// localSidecarChild is a season or episode with its current fingerprint. key
// holds the season number, and for an episode its episode number.
type localSidecarChild struct {
	record localSidecarRecord
	key    episodeResultKey
}

// localSidecarChildren fingerprints the sidecars of a series' seasons and
// episodes in scope that have a content ID in seasonIDs or episodeIDs.
func localSidecarChildren(
	ctx context.Context,
	folderID int,
	seriesID string,
	seasonSources, episodeSources localSources,
	childCtx seriesChildLocalContext,
	seasonIDs map[int]string,
	episodeIDs map[episodeResultKey]string,
	inScope func(dir string) bool,
) ([]localSidecarChild, error) {
	seasons, err := localSeasonSidecarChildren(ctx, folderID, seasonSources, childCtx, seasonIDs, inScope)
	if err != nil {
		return nil, err
	}
	episodes, err := localEpisodeSidecarChildren(ctx, folderID, episodeSources, childCtx, episodeIDs, inScope)
	if err != nil {
		return nil, err
	}
	children := append(seasons, episodes...)
	for i := range children {
		children[i].record.ItemID = seriesID
	}
	return children, nil
}

// localSidecarChildIDs returns the content IDs of children by season number
// and by season and episode number.
func localSidecarChildIDs(children []localSidecarChild) (map[int]string, map[episodeResultKey]string) {
	seasonIDs, episodeIDs := make(map[int]string), make(map[episodeResultKey]string)
	for _, c := range children {
		if c.record.TargetType == RefreshTargetSeason {
			seasonIDs[c.key.seasonNumber] = c.record.ContentID
		} else {
			episodeIDs[c.key] = c.record.ContentID
		}
	}
	return seasonIDs, episodeIDs
}

// localSeasonSidecarChildren fingerprints the sidecars of a series' seasons
// with a content ID whose folders are in scope.
func localSeasonSidecarChildren(
	ctx context.Context,
	folderID int,
	sources localSources,
	childCtx seriesChildLocalContext,
	seasonIDs map[int]string,
	inScope func(dir string) bool,
) ([]localSidecarChild, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	seasonDirs := make(map[int][]string)
	for number, dirs := range childCtx.seasonDirectoryPaths {
		if seasonIDs[number] != "" && slices.ContainsFunc(dirs, inScope) {
			seasonDirs[number] = dirs
		}
	}
	if len(seasonDirs) == 0 {
		return nil, nil
	}
	showFiles := showFilePaths(childCtx.episodeFilePaths)
	paths := make(map[int][]string)
	for _, provider := range sources {
		bySeason := provider.SeasonSidecarPaths(ctx, SeasonsRequest{
			ContentType:          matchContentTypeSeries,
			SeriesRootPaths:      childCtx.seriesRootPaths,
			SeasonDirectoryPaths: seasonDirs,
			ShowFilePaths:        showFiles,
		})
		for number, sidecars := range bySeason {
			found, err := sidecars.tracked()
			if err != nil {
				return nil, fmt.Errorf("%s season %d: %w", provider.Slug(), number, err)
			}
			paths[number] = append(paths[number], found...)
		}
	}
	var children []localSidecarChild
	for number := range seasonDirs {
		fingerprint, err := localSidecarFingerprint(paths[number])
		if err != nil {
			return nil, err
		}
		children = append(children, localSidecarChild{
			record: localSidecarRecord{FolderID: folderID, TargetType: RefreshTargetSeason, ContentID: seasonIDs[number], Fingerprint: fingerprint},
			key:    episodeResultKey{seasonNumber: number},
		})
	}
	return children, nil
}

// localEpisodeSidecarChildren fingerprints the sidecars of a series' episodes
// with a content ID whose files are in scope.
func localEpisodeSidecarChildren(
	ctx context.Context,
	folderID int,
	sources localSources,
	childCtx seriesChildLocalContext,
	episodeIDs map[episodeResultKey]string,
	inScope func(dir string) bool,
) ([]localSidecarChild, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	showFiles := showFilePaths(childCtx.episodeFilePaths)
	var children []localSidecarChild
	for seasonNumber, files := range childCtx.episodeFilePaths {
		episodeFiles := make(map[int][]string)
		for episodeNumber, paths := range files {
			if episodeIDs[episodeResultKey{seasonNumber, episodeNumber}] != "" &&
				slices.ContainsFunc(paths, func(path string) bool { return inScope(filepath.Dir(path)) }) {
				episodeFiles[episodeNumber] = paths
			}
		}
		if len(episodeFiles) == 0 {
			continue
		}
		paths := make(map[int][]string)
		for _, provider := range sources {
			byEpisode := provider.EpisodeSidecarPaths(ctx, EpisodesRequest{
				SeasonNumber:     seasonNumber,
				SeriesRootPaths:  childCtx.seriesRootPaths,
				EpisodeFilePaths: episodeFiles,
				ShowFilePaths:    showFiles,
			})
			for number, sidecars := range byEpisode {
				found, err := sidecars.tracked()
				if err != nil {
					return nil, fmt.Errorf("%s episode %dx%d: %w", provider.Slug(), seasonNumber, number, err)
				}
				paths[number] = append(paths[number], found...)
			}
		}
		for episodeNumber := range episodeFiles {
			key := episodeResultKey{seasonNumber, episodeNumber}
			fingerprint, err := localSidecarFingerprint(paths[episodeNumber])
			if err != nil {
				return nil, err
			}
			children = append(children, localSidecarChild{
				record: localSidecarRecord{FolderID: folderID, TargetType: RefreshTargetEpisode, ContentID: episodeIDs[key], Fingerprint: fingerprint},
				key:    key,
			})
		}
	}
	return children, nil
}

// localItemRequests builds the metadata and image requests a refresh would
// send the local provider for an item in a library. It fails when the item's
// files cannot be loaded, so a database error never reads as an item without
// sidecars.
func (s *MetadataService) localItemRequests(ctx context.Context, item *models.MediaItem, folderID int) (MetadataRequest, ImageRequest, error) {
	localCtx, err := s.loadLocalProviderContext(ctx, item.ContentID, folderID)
	if err != nil {
		return MetadataRequest{}, ImageRequest{}, err
	}
	if localCtx.representativeFilePath == "" {
		return MetadataRequest{}, ImageRequest{}, fmt.Errorf("no files of %s in library %d", item.ContentID, folderID)
	}
	// Only a re-apply needs the library's language for an item without one.
	language := strings.TrimSpace(item.DefaultMetadataLanguage)
	ids := canonicalIdentityProviderIDs(storedIdentityProviderIDs(item))
	req := MetadataRequest{
		ProviderIDs:               ids,
		ContentType:               item.Type,
		Language:                  language,
		FilePath:                  localCtx.filePath,
		RepresentativeFilePath:    localCtx.representativeFilePath,
		ObservedRootPath:          localCtx.observedRootPath,
		AllGroupFilePaths:         localCtx.allGroupFilePaths,
		PrimarySidecarSearchPaths: localCtx.primarySidecarSearchPaths,
	}
	imageReq := ImageRequest{
		ProviderIDs:               ids,
		ContentType:               item.Type,
		Language:                  language,
		RepresentativeFilePath:    localCtx.representativeFilePath,
		AllGroupFilePaths:         localCtx.allGroupFilePaths,
		PrimarySidecarSearchPaths: localCtx.primarySidecarSearchPaths,
	}
	return req, imageReq, nil
}

// loadSeriesChildLocalContext is seriesChildLocalContextForContent that fails
// when the series' files cannot be read.
func (s *MetadataService) loadSeriesChildLocalContext(ctx context.Context, seriesID string, folderID int) (seriesChildLocalContext, error) {
	localCtx, err := s.loadLocalProviderContext(ctx, seriesID, folderID)
	if err != nil {
		return seriesChildLocalContext{}, err
	}
	roots, err := s.configuredNamingRootsForContent(ctx, seriesID, folderID)
	if err != nil {
		return seriesChildLocalContext{}, err
	}
	return buildSeriesChildLocalContext(localCtx.primarySidecarSearchPaths, localCtx.allGroupFilePaths, roots...), nil
}

// localItemLanguage is the language an item's local metadata is written in:
// the item's own, or its library's for an item that has none. Unlike
// resolveFolderLanguage it fails when the library cannot be read, so English
// is never written for a library in another language.
func (s *MetadataService) localItemLanguage(ctx context.Context, item *models.MediaItem, folderID int) (string, error) {
	if language := strings.TrimSpace(item.DefaultMetadataLanguage); language != "" {
		return language, nil
	}
	if folderID <= 0 || s.folderRepo == nil {
		return "en", nil
	}
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		return "", fmt.Errorf("loading library %d: %w", folderID, err)
	}
	if language := strings.TrimSpace(folder.MetadataLanguage); language != "" {
		return language, nil
	}
	return "en", nil
}

// itemExists reports whether an item is still cataloged under contentID.
func (s *MetadataService) itemExists(ctx context.Context, contentID string) (bool, error) {
	_, err := s.itemRepo.GetByID(ctx, contentID)
	if errors.Is(err, catalog.ErrItemNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("loading item %s: %w", contentID, err)
	}
	return true, nil
}

// localMediaReachable reports whether one of an item's media files and every
// folder its files and sidecars sit in can be read. An item spread over
// several roots waits until all of them are reachable: a sidecar on an
// offline root cannot be told from a removed one.
func localMediaReachable(content localSidecarContent) bool {
	for _, path := range append([]string{content.File}, content.Dirs...) {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

// localDeclaredProviderIDs returns the provider IDs the local files declare
// for an item.
func (s *MetadataService) localDeclaredProviderIDs(ctx context.Context, providers localSources, req MetadataRequest) (map[string]string, error) {
	declared := map[string]string{}
	for _, provider := range providers {
		mp, ok := provider.(MetadataProvider)
		if !ok {
			continue
		}
		result, err := mp.GetMetadata(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", provider.Slug(), err)
		}
		addDeclaredProviderIDs(declared, localResultProviderIDs(ctx, provider, req, result))
	}
	return declared, nil
}

// localResultProviderIDs returns the provider IDs a local provider's files
// declare, given its GetMetadata result. A URL-only NFO carries no metadata
// but still names its title, and a refresh reads its IDs as identity hints,
// so they are read the same way here.
func localResultProviderIDs(ctx context.Context, provider Provider, req MetadataRequest, result *MetadataResult) map[string]string {
	if result != nil && result.HasMetadata {
		return result.ProviderIDs
	}
	hinter, ok := provider.(IdentityHintProvider)
	if !ok {
		return nil
	}
	return hinter.IdentityHints(ctx, SearchQuery{
		ContentType:               req.ContentType,
		FilePath:                  req.FilePath,
		RepresentativeFilePath:    req.RepresentativeFilePath,
		AllGroupFilePaths:         req.AllGroupFilePaths,
		PrimarySidecarSearchPaths: req.PrimarySidecarSearchPaths,
	})
}

// addDeclaredProviderIDs adds the valid identity IDs in ids that declared
// does not hold yet; the first local provider wins a key.
func addDeclaredProviderIDs(declared, ids map[string]string) {
	for key, value := range canonicalIdentityProviderIDs(ids) {
		if _, ok := declared[key]; !ok {
			declared[key] = value
		}
	}
}

// libraryHasLocalSidecarProviders reports whether any content level of a
// library takes local metadata. A mixed library has every level.
func (s *MetadataService) libraryHasLocalSidecarProviders(ctx context.Context, folderID int) (bool, error) {
	for _, level := range ContentLevelsForLibraryType("mixed") {
		sources, err := s.localSidecarProviders(ctx, folderID, level)
		if err != nil {
			return false, err
		}
		if len(sources) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// localSources are the enabled local providers in a library's chain for one
// content level. The chain runs them ahead of every remote provider (the
// pinned local tier), so their files always lead the level.
type localSources []localSidecarSource

// tracked returns the sidecar files change detection follows. An NFO that
// may apply but cannot be read fails it.
func (l LocalSidecars) tracked() ([]string, error) {
	if l.MetadataErr != nil {
		return nil, l.MetadataErr
	}
	return append(slices.Clone(l.Metadata), l.Artwork...), nil
}

// localSidecarProviders returns a content level's local sources. A chain that
// cannot be resolved is an error, not a level without local providers, so
// nothing is recorded for it.
func (s *MetadataService) localSidecarProviders(ctx context.Context, folderID int, level string) (localSources, error) {
	chain, err := s.resolveChainCached(ctx, folderID, level)
	if err != nil {
		return nil, fmt.Errorf("resolving %s provider chain of library %d: %w", level, folderID, err)
	}
	var sources localSources
	for _, provider := range chain {
		if source, ok := provider.(localSidecarSource); ok {
			sources = append(sources, source)
		}
	}
	return sources, nil
}

// localSidecarFingerprint hashes the path, size and modification time of each
// file in paths. A file that no longer exists counts as absent; any other
// error is returned, so an unreadable mount never reads as removed files.
func localSidecarFingerprint(paths []string) (string, error) {
	entries := make([]string, 0, len(paths))
	for _, path := range compactUniqueFilePaths(paths) {
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("reading local sidecar %s: %w", path, err)
		}
		entries = append(entries, sidecarEntryKey(path, info.Size(), info.ModTime().UnixNano()))
	}
	return hashSidecarEntries(entries), nil
}

func sidecarEntryKey(name string, size, modTime int64) string {
	return name + "\x00" + strconv.FormatInt(size, 10) + "\x00" + strconv.FormatInt(modTime, 10)
}

func hashSidecarEntries(entries []string) string {
	entries = slices.Clone(entries)
	slices.Sort(entries)
	entries = slices.Compact(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}
