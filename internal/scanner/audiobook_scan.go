package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

type filesystemRootContentFinder interface {
	FindContentIDByRootPath(ctx context.Context, folderID int, rootPath, preferredType string) (string, error)
}

type filesystemMediaItemWriter interface {
	Upsert(ctx context.Context, item *models.MediaItem) error
}

type audiobookPosterExec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type audiobookPosterPathReader interface {
	GetPosterPath(ctx context.Context, contentID string) (string, error)
}

const audiobookMultipartKind = "multipart"

// audiobookDuplicateCandidateSQL runs once per new book, so its plan must not
// depend on planner statistics. On a server with a large catalog, the first
// audiobook scan runs while the statistics still say there are no audiobooks
// and no author or narrator credits; a freely reordered join then compares
// every author credit with every audiobook, and each lookup gets slower as
// the library fills. So the names are resolved first, the items credited to
// both are materialized, and media_items is read by primary key. The type is
// tested on the materialized rows: tested on media_items itself, it lets the
// planner scan the partial books index once per candidate.
const audiobookDuplicateCandidateSQL = `
	WITH cand AS MATERIALIZED (
		SELECT ipa.content_id
		FROM item_people ipa
		WHERE ipa.kind = 7
		  AND ipa.person_id = ANY (ARRAY(SELECT id FROM people WHERE LOWER(name) = LOWER($1)))
		INTERSECT
		SELECT ipn.content_id
		FROM item_people ipn
		WHERE ipn.kind = 8
		  AND ipn.person_id = ANY (ARRAY(SELECT id FROM people WHERE LOWER(name) = LOWER($2)))
	), items AS MATERIALIZED (
		SELECT i.content_id, i.title, i.type, i.year
		FROM cand
		JOIN media_items i ON i.content_id = cand.content_id
	)
	SELECT mi.content_id, mi.title
	FROM items mi
	JOIN LATERAL (
		SELECT COALESCE(SUM(mf.duration), 0) AS dur
		FROM media_files mf WHERE mf.content_id = mi.content_id
	) f ON TRUE
	JOIN LATERAL (
		SELECT btrim(regexp_replace(
			regexp_replace(lower(mi.title), '[^a-z0-9]+', ' ', 'g'),
			'[[:space:]]+', ' ', 'g'
		)) AS normalized_title
	) t ON TRUE
	WHERE mi.type = 'audiobook'
	  -- Compare the tag year the files were scanned with: a provider match
	  -- owns mi.year, and a renamed folder of a matched book must still find
	  -- its item. Files from before base_year existed (migration 078) hold 0
	  -- until their folder is rewritten; for those, fall back to mi.year.
	  AND (
		  EXISTS (
			  SELECT 1 FROM media_files yf
			  WHERE yf.content_id = mi.content_id AND yf.base_year = $3
		  )
		  OR (
			  mi.year = $3
			  AND NOT EXISTS (
				  SELECT 1 FROM media_files yf
				  WHERE yf.content_id = mi.content_id AND yf.base_year <> 0
			  )
		  )
	  )
	  AND f.dur > 0
	  AND ABS(f.dur - $4) <= $5
	  AND (
		  t.normalized_title = $6
	   OR t.normalized_title LIKE $6 || ' %'
	   OR $6 LIKE t.normalized_title || ' %'
	  )
	ORDER BY ABS(f.dur - $4), LENGTH(mi.title), mi.content_id
	LIMIT 25
`

// audiobookDiskFile is the on-disk projection used by audiobookFolderUnchanged.
// Path is the absolute file path; Size and ModTime come from os.Stat.
type audiobookDiskFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// audiobookFolderUnchanged reports whether the audio files on disk match the
// existing media_files rows for the same folder one-for-one on path, size,
// and mtime. A new file, removed file, or any byte-level / mtime drift returns
// false so the caller falls through to a full reconcile.
//
// Comparison uses sameFileModifiedAt for mtime to absorb sub-second precision
// differences between filesystem reads.
//
// Every part of a multi-file book carries the chapter the scanner synthesized
// for it. Earlier builds let playback's probe repair erase those, so a part
// without chapters is reprocessed to restore them.
func audiobookFolderUnchanged(existing []*models.MediaFile, onDisk []audiobookDiskFile) bool {
	if len(existing) != len(onDisk) {
		return false
	}
	byPath := make(map[string]*models.MediaFile, len(existing))
	for _, mf := range existing {
		if mf == nil || mf.HasLegacyAttachedPictureVideo() {
			return false
		}
		if len(existing) > 1 && len(mf.Chapters) == 0 {
			return false
		}
		byPath[mf.FilePath] = mf
	}
	for _, d := range onDisk {
		mf, ok := byPath[d.Path]
		if !ok {
			return false
		}
		if mf.FileSize != d.Size {
			return false
		}
		if mf.FileModifiedAt == nil || !sameFileModifiedAt(mf.FileModifiedAt, d.ModTime) {
			return false
		}
	}
	return true
}

// audiobookFolderShouldSkip returns true when every part of the book rooted at
// folderPath (see audiobookBookParts) matches an existing media_files row by size + mtime AND the
// linked media_items row is in a non-unmatched status. False under any
// drift, missing row, or unmatched status — the caller then falls through
// to the full reconcile path.
//
// Errors are returned but the worker loop treats them as "do not skip".
//
// Audio files ffprobe already rejected (rejectedAudiobookParts) have no row
// and are left out of the comparison; unreadable counts them.
func (s *Scanner) audiobookFolderShouldSkip(ctx context.Context, folder *models.MediaFolder, folderPath, physical string, allowedPaths map[string]bool, walked *audiobookWalkedBook) (unreadable int, skip bool, err error) {
	if s.fileRepo == nil || s.itemRepo == nil {
		return 0, false, nil
	}

	// A book with no rows cannot be unchanged, so its files are not read.
	existing, err := s.fileRepo.ListByObservedRootPath(ctx, folder.ID, folderPath)
	if err != nil {
		return 0, false, fmt.Errorf("list existing files: %w", err)
	}
	if len(existing) == 0 {
		return 0, false, nil
	}
	for _, file := range existing {
		if file == nil || file.CanonicalRootPath != physical {
			return 0, false, nil
		}
	}
	var onDisk []audiobookDiskFile
	err = walked.partsOrRelist(folderPath, allowedPaths, func(parts []string) error {
		onDisk, unreadable, err = statAudiobookDiskFiles(parts)
		return err
	})
	if errors.Is(err, errFolderHasNoMedia) {
		return 0, false, nil
	}
	if err != nil || len(onDisk) == 0 {
		return 0, false, err
	}
	if !audiobookFolderUnchanged(existing, onDisk) {
		return 0, false, nil
	}
	// A previous scan may have retired an ignored part without rebuilding the
	// remaining files' presentation. Matching sizes and mtimes do not make that
	// incomplete multipart book safe to skip. A single-file book carries no
	// presentation or edition; earlier builds' filename variant pass could
	// leave both behind, so rebuild such a book once.
	for _, file := range existing {
		if len(existing) == 1 {
			if file.PresentationKind != "" || file.EditionKey != "" {
				return 0, false, nil
			}
		} else if file.PresentationKind != audiobookMultipartKind || file.PresentationPartTotal != len(existing) {
			return 0, false, nil
		}
	}

	contentID := existing[0].ContentID
	if contentID == "" {
		return 0, false, nil
	}
	// All files must share the same content_id; fragmented folders need reconcile.
	for _, mf := range existing[1:] {
		if mf.ContentID != contentID {
			return 0, false, nil
		}
	}
	if audiobookTagRereadRequested(ctx, contentID) {
		return 0, false, nil
	}
	items, err := s.itemRepo.GetByIDs(ctx, []string{contentID})
	if err != nil {
		return 0, false, fmt.Errorf("get item for skip check: %w", err)
	}
	if len(items) == 0 || items[0] == nil {
		return 0, false, nil
	}
	if strings.TrimSpace(items[0].Title) == "" {
		return 0, false, nil
	}
	if strings.EqualFold(strings.TrimSpace(items[0].Status), "unmatched") {
		return 0, false, nil
	}
	paths := make([]string, len(existing))
	for i, file := range existing {
		paths[i] = file.FilePath
	}
	if !s.confirmUnchangedFiles(ctx, folder.ID, paths) {
		return 0, false, nil
	}
	return unreadable, true, nil
}

// statAudiobookDiskFiles stats the parts of a book (audiobookBookParts) for
// the unchanged check, leaving out files ffprobe already rejected and counting
// them in unreadable.
func statAudiobookDiskFiles(parts []string) (onDisk []audiobookDiskFile, unreadable int, err error) {
	for _, full := range parts {
		info, statErr := fspath.Stat(full)
		if statErr != nil {
			return nil, 0, fmt.Errorf("stat %s: %w", full, statErr)
		}
		modTime := normalizeFileModifiedAt(info.ModTime())
		if audiobookPartRejected(full, info.Size(), modTime) {
			unreadable++
			continue
		}
		onDisk = append(onDisk, audiobookDiskFile{Path: full, Size: info.Size(), ModTime: modTime})
	}
	return onDisk, unreadable, nil
}

// audiobookScanProgressInterval is how often an audiobook scan reports how
// many books it has finished.
const audiobookScanProgressInterval = 3 * time.Second

// audiobookScanWorkers returns the configured number of parallel workers
// for audiobook reconciliation. Defaults to 8 — high enough to keep all
// cores busy on the ffprobe step (which dominates per-book wall time)
// without overwhelming a small server. Override with SILO_AUDIOBOOK_SCAN_WORKERS.
func audiobookScanWorkers() int {
	if v := os.Getenv("SILO_AUDIOBOOK_SCAN_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8
}

type audiobookRootScan struct {
	root         string
	libraryRoots map[string]bool                 // configured library roots, whose loose files are books of their own
	candidates   []string                        // book roots: directories, or the files of loose single-file books
	seenPaths    map[string]bool                 // audio files the walk accepted, and the disc folders it took them from
	books        map[string]*audiobookWalkedBook // what the walk read of each book folder it found
	// admit is a file an admin asked to scan by its exact path, which the
	// walk keeps past the ignore files; "" for none.
	admit        string
	rootErr      error
	walkFailures []string // logical paths the walk could not read or resolve
}

// admits reports whether path is the admitted file or a folder on its path.
func (r *audiobookRootScan) admits(path string) bool {
	return r.admit != "" && pathWithinAnyRoot(r.admit, []string{path})
}

func (r *audiobookRootScan) failed() bool {
	return r.rootErr != nil || len(r.walkFailures) > 0
}

// walkAudiobookDirectories keeps catalog paths under the configured root while
// following directory symlinks. Only ancestors are tracked: aliases must retain
// their own seen paths so missing-file reconciliation does not retire them.
// Callers filter inherited patterns before resolving or entering a directory.
//
// Each directory is classified by what it holds (see audiobookBookParts for
// the parts of a book rooted at a directory):
//   - A folder's own audio files and the audio in its disc subfolders (Disc 1,
//     CD2, Part 3) are one book, whatever else the folder holds.
//   - Its other subfolders are walked, and each that holds audio is a book of
//     its own. Below a folder that holds a book, extras folders (Bonus, Extras,
//     Sample) are not walked.
//   - A library root is never a book: its loose audio files are one book each,
//     rooted at the file's path, and its disc-named folders ordinary folders.
//
// A folder whose own parts are uncertain, because an entry directly inside it
// would not resolve or one of its disc folders could not be fully read, is
// held: it is recorded as a walk failure, which protects its whole subtree
// from missing-file reconciliation, it adds no candidates, and everything
// indexed below it stays as it is until it reads cleanly. A library root is
// never held.
//
// onlyAdmitted walks a folder the ignore files exclude, entered only on the way
// to the file an admin asked for (audiobookRootScan.admit).
func walkAudiobookDirectories(ctx context.Context, path string, scan *audiobookRootScan, ancestors map[string]bool, ignoreRulesStack []ignoreRules, honorIgnores, onlyAdmitted bool, readDir func(string) ([]os.DirEntry, error)) error {
	// Checkpoints for a held folder: the failures recorded and the candidates
	// found from here on, including those inside its disc folders, are its own.
	failures, candidates := len(scan.walkFailures), len(scan.candidates)
	dir, ok, err := readAudiobookDirectory(ctx, path, scan, ancestors, ignoreRulesStack, honorIgnores, onlyAdmitted, readDir)
	if err != nil || !ok {
		return err
	}
	ancestors[dir.canonical] = true
	defer delete(ancestors, dir.canonical)
	inLibraryRoot := scan.libraryRoots[path]
	uncertain := len(scan.walkFailures) > failures

	hasDiscs := false
	var others []string
	var discs []audiobookWalkedDisc
	discsWhole := true
	for _, directory := range dir.directories {
		outcome := audiobookNotDisc
		if !inLibraryRoot && !scan.libraryRoots[directory] && isAudiobookDiscDirName(filepath.Base(directory)) {
			var err error
			var disc *audiobookDirFiles
			if outcome, disc, err = takeAudiobookDisc(ctx, directory, scan, ancestors, dir.childRules, honorIgnores, dir.admittedOnly[directory], readDir); err != nil {
				return err
			}
			if outcome == audiobookDiscTaken {
				if disc == nil {
					discsWhole = false
				} else {
					discs = append(discs, audiobookWalkedDisc{dir: directory, files: disc.files})
				}
			}
		}
		switch outcome {
		case audiobookDiscTaken:
			hasDiscs = true
		case audiobookDiscUnreadable:
			uncertain = true
		case audiobookNotDisc:
			others = append(others, directory)
		}
	}
	if uncertain && !inLibraryRoot {
		scan.candidates = scan.candidates[:candidates]
		recordWalkFailure(&scan.walkFailures, path)
		slog.WarnContext(ctx, "audiobook scan: folder held until its contents can be read", "component", "scanner", "path", path)
		return nil
	}
	holdsBook := !inLibraryRoot && (hasDiscs || len(dir.audio) > 0)
	for _, directory := range others {
		// An extras folder that is a configured root is walked as its own root.
		if holdsBook && !scan.libraryRoots[directory] && isAudiobookExtrasDirName(filepath.Base(directory)) {
			continue
		}
		if err := walkAudiobookDirectories(ctx, directory, scan, ancestors, dir.childRules, honorIgnores, dir.admittedOnly[directory], readDir); err != nil {
			return err
		}
	}
	switch {
	case holdsBook:
		scan.candidates = append(scan.candidates, path)
		if scan.books == nil {
			scan.books = make(map[string]*audiobookWalkedBook)
		}
		scan.books[path] = newAudiobookWalkedBook(dir.canonical, path, dir.files, discs, discsWhole)
	case inLibraryRoot:
		scan.candidates = append(scan.candidates, dir.audio...)
	}
	return nil
}

type audiobookDiscOutcome int

const (
	audiobookNotDisc        audiobookDiscOutcome = iota // walk it like any other folder
	audiobookDiscTaken                                  // a disc of the parent's book
	audiobookDiscSkipped                                // excluded by an ignore file, or an ancestor
	audiobookDiscUnreadable                             // not fully read; recorded as a walk failure, and its parent is held
)

// takeAudiobookDisc reads a disc-named folder below a book folder. A folder
// holding audio is a disc: it is recorded in the inventory, since
// audiobookBookParts reads only the disc folders recorded there, and its
// subfolders are walked so books stored below a disc are still found. Extras
// below the disc stay unwalked, as below any book.
func takeAudiobookDisc(ctx context.Context, path string, scan *audiobookRootScan, ancestors map[string]bool, ignoreRulesStack []ignoreRules, honorIgnores, onlyAdmitted bool, readDir func(string) ([]os.DirEntry, error)) (audiobookDiscOutcome, *audiobookDirFiles, error) {
	failures := len(scan.walkFailures)
	disc, ok, err := readAudiobookDirectory(ctx, path, scan, ancestors, ignoreRulesStack, honorIgnores, onlyAdmitted, readDir)
	switch {
	case err != nil:
		return audiobookDiscSkipped, nil, err
	case len(scan.walkFailures) > failures:
		// The folder, or an entry in it, could not be read.
		return audiobookDiscUnreadable, nil, nil
	case !ok:
		return audiobookDiscSkipped, nil, nil
	case len(disc.audio) == 0:
		return audiobookNotDisc, nil, nil
	}
	scan.seenPaths[path] = true
	ancestors[disc.canonical] = true
	defer delete(ancestors, disc.canonical)
	for _, directory := range disc.directories {
		if isAudiobookExtrasDirName(filepath.Base(directory)) {
			continue
		}
		if err := walkAudiobookDirectories(ctx, directory, scan, ancestors, disc.childRules, honorIgnores, disc.admittedOnly[directory], readDir); err != nil {
			return audiobookDiscSkipped, nil, err
		}
	}
	return audiobookDiscTaken, disc.files, nil
}

// audiobookDirListing is one directory as the walk sees it.
type audiobookDirListing struct {
	canonical   string
	audio       []string
	directories []string
	childRules  []ignoreRules
	// admittedOnly marks subdirectories the ignore files exclude, entered only
	// on the way to the admitted file.
	admittedOnly map[string]bool
	// files is the directory as audiobookBookParts reads it, ignored entries
	// included; nil when an ignored link that could be a part or a disc folder
	// was never resolved.
	files *audiobookDirFiles
}

// readAudiobookDirectory lists one directory for the walk: the audio files it
// holds, which it records as seen, and its subdirectories, each filtered by
// the ignore rules. ok is false when the directory is skipped, already an
// ancestor, or unreadable; an unreadable directory is recorded as a walk
// failure. Only a canceled context returns an error.
//
// When onlyAdmitted is set, or the ignore files skip the folder or one of its
// entries, only the admitted file and the folders on its path are kept.
func readAudiobookDirectory(ctx context.Context, path string, scan *audiobookRootScan, ancestors map[string]bool, ignoreRulesStack []ignoreRules, honorIgnores, onlyAdmitted bool, readDir func(string) ([]os.DirEntry, error)) (dir audiobookDirListing, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return dir, false, err
	}
	recordFailure := func(err error) {
		recordWalkFailure(&scan.walkFailures, path)
		slog.WarnContext(ctx, "audiobook scan: walk error", "component", "scanner", "path", path, "error", err)
	}
	dir.canonical, err = canonicalWalkPath(path)
	if err != nil {
		recordFailure(err)
		return dir, false, nil
	}
	if ancestors[dir.canonical] {
		return dir, false, nil
	}
	entries, err := readDirectoryWithRetry(ctx, path, readDir)
	if err != nil {
		if ctx.Err() != nil {
			return dir, false, ctx.Err()
		}
		recordFailure(err)
		return dir, false, nil
	}
	dir.childRules = ignoreRulesStack
	if honorIgnores {
		var skip bool
		dir.childRules, skip, err = dirIgnoreRules(ignoreRulesStack, path, path, entries)
		if err != nil {
			recordFailure(err)
			return dir, false, nil
		}
		if skip && !scan.admits(path) {
			return dir, false, nil
		}
		onlyAdmitted = onlyAdmitted || skip
	}
	dir.files = &audiobookDirFiles{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return dir, false, err
		}
		child := filepath.Join(path, entry.Name())
		excluded := onlyAdmitted || ignoreRulesMatch(dir.childRules, child, entry.IsDir())
		if excluded && !scan.admits(child) {
			// An unresolved link could be a part or a disc folder; any
			// other name is never read for parts.
			if entry.Type()&os.ModeSymlink != 0 {
				if SupportsAudioFile(entry.Name()) || isAudiobookDiscDirName(entry.Name()) {
					dir.files = nil
				}
			} else {
				dir.files.add(child, entry.IsDir())
			}
			continue
		}
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := fspath.Stat(child)
			if err != nil {
				recordWalkFailure(&scan.walkFailures, child)
				slog.WarnContext(ctx, "audiobook scan: symlink stat failed", "component", "scanner", "path", child, "error", err)
				continue
			}
			isDir = info.IsDir()
			// Directory-only patterns apply once the link resolves to a directory.
			if isDir && ignoreRulesMatch(dir.childRules, child, true) {
				if !scan.admits(child) {
					dir.files.add(child, true)
					continue
				}
				excluded = true
			}
		}
		dir.files.add(child, isDir)
		if isDir {
			if !SkippedDirName(entry.Name()) {
				dir.directories = append(dir.directories, child)
				if excluded {
					if dir.admittedOnly == nil {
						dir.admittedOnly = make(map[string]bool)
					}
					dir.admittedOnly[child] = true
				}
			}
		} else if SupportsAudioFile(entry.Name()) {
			scan.seenPaths[child] = true
			dir.audio = append(dir.audio, child)
		}
	}
	return dir, true, nil
}

// cleanAudiobookRoot returns root as a walk compares it, or "" for no root.
func cleanAudiobookRoot(root string) string {
	clean := filepath.Clean(strings.TrimSpace(root))
	if clean == "." {
		return ""
	}
	return clean
}

// collectAudiobookRootScans walks each root, plus every configured library root
// inside one, since a library scan walks every configured root. A scoped root
// is first widened to the folder a library scan would classify it in
// (audiobookScopeRoot). Each walk classifies folders as
// walkAudiobookDirectories describes, knowing which folders are configured
// library roots.
//
// admit is a file an admin asked to scan by its exact path: the walks keep it
// past the ignore files that exclude it, and nothing else they exclude.
func collectAudiobookRootScans(ctx context.Context, folderID int, roots, libraryRoots []string, honorIgnores bool, admit string) ([]audiobookRootScan, error) {
	var configured []string
	for _, root := range libraryRoots {
		if cleanRoot := cleanAudiobookRoot(root); cleanRoot != "" {
			configured = append(configured, cleanRoot)
		}
	}
	walkRoots := make([]string, 0, len(roots))
	scopes := make(map[string]audiobookScope)
	libraryRootSet := make(map[string]bool, len(configured))
	for _, root := range configured {
		libraryRootSet[root] = true
	}
	for _, root := range roots {
		cleanRoot := cleanAudiobookRoot(root)
		if cleanRoot == "" {
			continue
		}
		if honorIgnores && !slices.Contains(configured, cleanRoot) {
			var scope audiobookScope
			cleanRoot, scope = audiobookScopeRoot(cleanRoot, libraryRoots)
			scopes[cleanRoot] = scope
		}
		walkRoots = append(walkRoots, cleanRoot)
		for _, nested := range configured {
			if nested != cleanRoot && pathWithinAnyRoot(nested, []string{cleanRoot}) {
				walkRoots = append(walkRoots, nested)
			}
		}
	}
	scans := make([]audiobookRootScan, 0, len(walkRoots))
	walked := make(map[string]bool, len(walkRoots))
	for _, cleanRoot := range walkRoots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if walked[cleanRoot] {
			continue
		}
		walked[cleanRoot] = true
		scan := audiobookRootScan{
			root:         cleanRoot,
			libraryRoots: libraryRootSet,
			seenPaths:    make(map[string]bool),
			admit:        admit,
		}
		info, statErr := fspath.Stat(cleanRoot)
		switch {
		case statErr != nil && scopeRootVanished(cleanRoot, libraryRoots):
			// A deleted book folder is an empty scope, as for movies and
			// series: its files are reconciled instead of protected.
			scans = append(scans, scan)
			continue
		case statErr != nil:
			scan.rootErr = fmt.Errorf("stat root: %w", statErr)
		case !info.IsDir():
			scan.rootErr = fmt.Errorf("root is not a directory after symlink resolution")
		}
		var rules []ignoreRules
		var ignored bool
		if scan.rootErr == nil && honorIgnores {
			if scope, ok := scopes[cleanRoot]; ok {
				rules, ignored, scan.rootErr = scope.rules, scope.ignored, scope.err
			} else {
				rules, ignored, scan.rootErr = scanRootIgnoreRules(cleanRoot, libraryRoots)
			}
		}
		// An excluded scope is still entered toward the admitted file.
		if scan.rootErr == nil && (!ignored || scan.admits(cleanRoot)) {
			walkErr := walkAudiobookDirectories(ctx, cleanRoot, &scan, make(map[string]bool), rules, honorIgnores, ignored, fspath.ReadDir)
			if walkErr != nil {
				if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
					return nil, walkErr
				}
				scan.rootErr = fmt.Errorf("walk root: %w", walkErr)
			}
			sort.Strings(scan.candidates)
		}
		if scan.failed() {
			slog.WarnContext(ctx, "audiobook scan: walk incomplete; unreadable paths protected from missing-file reconciliation", "component", "scanner",
				"folder_id", folderID,
				"root", cleanRoot,
				"walk_failures", len(scan.walkFailures),
				"error", scan.rootErr,
			)
		}
		scans = append(scans, scan)
	}
	return scans, nil
}

// Keep readable portions of partial walks eligible for cleanup. Failed logical
// paths protect only their own files/subtrees, including during the trash sweep.
func splitAudiobookReconcileRoots(scans []audiobookRootScan) (roots []string, seenPaths map[string]bool, protectedPaths []string) {
	roots = make([]string, 0, len(scans))
	seenPaths = make(map[string]bool)
	seenRoots := make(map[string]struct{}, len(scans))
	for i := range scans {
		scan := &scans[i]
		protectedPaths = append(protectedPaths, scan.walkFailures...)
		if scan.rootErr != nil {
			protectedPaths = append(protectedPaths, scan.root)
			continue
		}
		if pathWithinAnyRoot(scan.root, scan.walkFailures) {
			continue
		}
		if _, seen := seenRoots[scan.root]; !seen {
			roots = append(roots, scan.root)
			seenRoots[scan.root] = struct{}{}
		}
		if len(scan.seenPaths) > 0 {
			for path := range scan.seenPaths {
				seenPaths[path] = true
			}
		}
	}
	return roots, seenPaths, protectedPaths
}

// groupAudiobookCandidates dispatches each physical book once, by the
// canonical path the walk resolved for a book folder. Retain logical aliases
// for retry if the preferred path disappears after discovery.
func groupAudiobookCandidates(scans []audiobookRootScan) [][]string {
	var groups [][]string
	byPhysical := make(map[string]int)
	seen := make(map[string]bool)
	for _, scan := range scans {
		for _, path := range scan.candidates {
			if seen[path] {
				continue
			}
			seen[path] = true
			var physical string
			if book := scan.books[path]; book != nil {
				physical = book.canonical
			} else if resolved, err := canonicalWalkPath(path); err == nil {
				physical = resolved
			} else {
				// Let reconciliation report the failure rather than silently
				// dropping a book that vanished after the walk.
				physical = path
			}
			if index, ok := byPhysical[physical]; ok {
				groups[index] = append(groups[index], path)
			} else {
				byPhysical[physical] = len(groups)
				groups = append(groups, []string{path})
			}
		}
	}
	return groups
}

func (s *Scanner) reconcileAudiobookAliases(ctx context.Context, folder *models.MediaFolder, aliases []string, inventory audiobookInventory, inventoryAt time.Time, skipped, unreadable *int64) error {
	// Prefer the already indexed location. Adding an alias should not churn
	// file IDs or overwrite a filesystem-derived title on every scan.
	if s.fileRepo != nil && len(aliases) > 1 {
		for i, path := range aliases {
			id, err := s.fileRepo.FindContentIDByObservedRootPath(ctx, folder.ID, path, "audiobook")
			if err != nil {
				return err
			}
			if id != "" {
				aliases[0], aliases[i] = aliases[i], aliases[0]
				break
			}
		}
	}
	var failures []error
	for _, path := range aliases {
		if err := ctx.Err(); err != nil {
			return err
		}
		walked := inventory.book(path)
		err := s.reconcileAudiobookFolder(ctx, folder, path, inventory.seen, walked, inventoryAt, skipped, unreadable)
		walked.release()
		if err != nil {
			if !errors.Is(err, errFolderHasNoMedia) {
				failures = append(failures, err)
			}
			continue
		}
		return nil
	}
	return errors.Join(failures...)
}

// ScanAudiobookFolder walks an audiobooks-typed media folder and writes
// one media_items row per book it can parse, plus the corresponding
// media_files rows and author/narrator links in item_people.
//
// walkAudiobookDirectories decides which folders and loose files are books;
// discovery follows directory symlinks.
//
// This bypasses the per-file movie/TV pipeline because audiobooks are
// inherently folder-scoped (one book = one item, possibly multi-file).
func (s *Scanner) ScanAudiobookFolder(ctx context.Context, folder *models.MediaFolder, fullScan bool) error {
	if folder == nil {
		return fmt.Errorf("ScanAudiobookFolder: nil folder")
	}
	return s.scanAudiobookPaths(ctx, folder, folder.Paths, fullScan, true, "")
}

func (s *Scanner) scanAudiobookPaths(ctx context.Context, folder *models.MediaFolder, roots []string, fullScan, honorIgnores bool, admit string) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanAudiobookFolder: nil scanner or folder")
	}
	ctx = withScopedScan(ctx, fullScan)

	warning, err := s.scanWarningBeforeWalk(ctx, folder.ID, fullScan)
	if err != nil {
		return err
	}

	// Another scan can index a book's new parts while this one runs. This walk
	// never listed them, so rows written after this time are left to that
	// scan: this one neither marks them missing nor overwrites their book.
	inventoryAt, err := s.scanInventoryTime(ctx)
	if err != nil {
		return fmt.Errorf("audiobook scan: %w", err)
	}

	// Phase 1: walk the tree to collect candidate book folders. This is
	// I/O-light (no ffprobe), and avoids holding the worker pool open
	// for the duration of a 240k-folder scan.
	scans, err := collectAudiobookRootScans(ctx, folder.ID, roots, folder.Paths, honorIgnores, admit)
	if err != nil {
		return err
	}
	walkFailures := 0
	for _, scan := range scans {
		walkFailures += len(scan.walkFailures)
		if scan.rootErr != nil {
			walkFailures++
		}
	}
	// Cleanup may replace this with a stronger empty/dead-root warning.
	if err := s.setPartialWalkWarning(ctx, folder.ID, walkFailures, !fullScan); err != nil {
		return err
	}
	candidates := groupAudiobookCandidates(scans)
	reconcileRoots, seenPaths, protectedPaths := splitAudiobookReconcileRoots(scans)
	inventory := newAudiobookInventory(seenPaths, scans)
	reportProgress(ctx, audiobookScanProgress(folder.ID, len(candidates), 0, 0, 0))

	if len(candidates) == 0 {
		// No books on disk. Still reconcile (guarded) so a legitimately-emptied
		// library converges — but only when a root was readable, and the
		// empty-walk guard requires operator confirmation before deleting.
		if len(reconcileRoots) > 0 {
			if err := s.reconcileAudiobookMissingFiles(ctx, folder, reconcileRoots, inventoryAt, seenPaths, protectedPaths, fullScan); err != nil {
				slog.WarnContext(ctx, "audiobook scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
			}
		} else if len(scans) > 0 {
			slog.WarnContext(ctx, "audiobook scan: every root walk failed; skipping missing-file reconciliation", "component", "scanner",
				"folder_id", folder.ID)
		}
		if walkFailures > 0 {
			return s.setPartialWalkWarning(ctx, folder.ID, walkFailures, true)
		}
		if fullScan {
			return s.clearPartialWalkWarning(ctx, folder.ID, warning)
		}
		return nil
	}

	if err := s.reconcileAudiobookCandidates(ctx, folder, candidates, inventory, inventoryAt); err != nil {
		return err
	}

	// Reconcile files that vanished from disk now that the full walk's
	// seenPaths is known and the scan completed without cancellation.
	if err := s.reconcileAudiobookMissingFiles(ctx, folder, reconcileRoots, inventoryAt, seenPaths, protectedPaths, fullScan); err != nil {
		slog.WarnContext(ctx, "audiobook scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
	}
	if walkFailures > 0 {
		return s.setPartialWalkWarning(ctx, folder.ID, walkFailures, true)
	}
	if fullScan {
		return s.clearPartialWalkWarning(ctx, folder.ID, warning)
	}
	return nil
}

// reconcileAudiobookCandidates rebuilds or skips each discovered book in
// parallel. ffprobe dominates per-book wall time (hundreds of ms each), so
// single-threaded scans of a large library take days; the worker pool brings
// this to hours. It fails when the scan is canceled, or when every attempted
// book failed.
func (s *Scanner) reconcileAudiobookCandidates(ctx context.Context, folder *models.MediaFolder, candidates [][]string, inventory audiobookInventory, inventoryAt time.Time) error {
	workers := audiobookScanWorkers()
	slog.InfoContext(ctx, "audiobook scan: starting", "component", "scanner",
		"folder_id", folder.ID,
		"candidates", len(candidates),
		"workers", workers,
	)

	ch := make(chan []string, workers*2)
	var (
		wg        sync.WaitGroup
		processed int64
		failed    int64
		skipped   int64
		// unreadable counts audio files ffprobe rejected; their books are
		// indexed without them.
		unreadable int64
		cancelMu   sync.Mutex
		failures   scanFailures
		cancelErr  error
	)
	start := time.Now()
	for range workers {
		wg.Go(func() {
			for aliases := range ch {
				path := aliases[0]
				if ctx.Err() != nil {
					return
				}
				if err := s.reconcileAudiobookAliases(ctx, folder, aliases, inventory, inventoryAt, &skipped, &unreadable); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						cancelMu.Lock()
						if cancelErr == nil {
							cancelErr = err
						}
						cancelMu.Unlock()
						return
					}
					atomic.AddInt64(&failed, 1)
					failures.addf("%s: %w", path, err)
					slog.WarnContext(ctx, "audiobook scan: folder failed", "component", "scanner",
						"folder_id", folder.ID,
						"path", path,
						"error", err,
					)
				}
				atomic.AddInt64(&processed, 1)
			}
		})
	}
	// Books can each take seconds, so progress follows the clock: workers
	// only count, and one goroutine writes the counts every interval. The
	// last counts are written however the pool ends.
	progress := audiobookProgress(ctx, folder.ID, len(candidates), start, &processed, &failed, &skipped)
	stopProgress := reportProgressEvery(ctx, audiobookScanProgressInterval, progress)
	defer func() {
		stopProgress()
		reportProgress(ctx, progress())
	}()

	for _, p := range candidates {
		select {
		case ch <- p:
		case <-ctx.Done():
			close(ch)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(ch)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if cancelErr != nil {
		return cancelErr
	}

	slog.InfoContext(ctx, "audiobook scan: completed", "component", "scanner",
		"folder_id", folder.ID,
		"processed", atomic.LoadInt64(&processed),
		"failed", atomic.LoadInt64(&failed),
		"skipped", atomic.LoadInt64(&skipped),
		"unreadable_files", atomic.LoadInt64(&unreadable),
		"elapsed_sec", int(time.Since(start).Seconds()),
	)
	if processedCount := atomic.LoadInt64(&processed); processedCount > 0 {
		failedCount := atomic.LoadInt64(&failed)
		skippedCount := atomic.LoadInt64(&skipped)
		if failedCount > 0 && skippedCount == 0 && failedCount == processedCount {
			return fmt.Errorf("audiobook scan failed for every attempted folder_id=%d: %w", folder.ID, failures.join())
		}
	}

	return nil
}

// reconcileAudiobookMissingFiles marks missing the cataloged files under the
// scanned roots that the walk did not see, except rows written since
// inventoryAt, which belong to a scan that walked later. It optionally empties
// the folder trash and reconciles library memberships so books with no
// remaining files are removed (a rename therefore converges on the newly
// indexed path instead of leaving a stale duplicate). A scan that saw zero
// files while the DB still has rows only reconciles when the operator has
// confirmed cleanup, so an unmounted source can't wipe the catalog.
func (s *Scanner) reconcileAudiobookMissingFiles(ctx context.Context, folder *models.MediaFolder, roots []string, inventoryAt time.Time, seenPaths map[string]bool, protectedPaths []string, fullScan bool) error {
	if s.fileRepo == nil || s.libraryRepo == nil || len(roots) == 0 {
		return nil
	}

	confirmedCleanup, blockAll, err := s.emptyCleanupDecision(
		ctx, folder, roots, seenPaths, fullScan, "audiobook",
	)
	if err != nil {
		return err
	}
	if blockAll {
		return nil
	}
	// Protect configured roots that are unreachable or suspect-empty (a lost
	// mount leaving its mountpoint) before marking, as the movie/series
	// pipeline does. Marking first would let membership reconciliation remove
	// those books even though the sweep below keeps their files.
	protectedPaths, err = s.sweepProtectedRoots(ctx, folder, confirmedCleanup, protectedPaths)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	missing := 0
	for _, root := range roots {
		existing, err := s.fileRepo.ListScanPresenceWrittenBefore(ctx, folder.ID, root, inventoryAt)
		if err != nil {
			return fmt.Errorf("listing existing audiobook files for %q: %w", root, err)
		}
		for _, mf := range existing {
			if seenPaths[mf.FilePath] || pathWithinAnyRoot(mf.FilePath, protectedPaths) {
				continue
			}
			if mf.MissingSince == nil {
				err := s.fileRepo.MarkMissingWrittenBefore(ctx, mf.ID, mf.FilePath, now, inventoryAt)
				if errors.Is(err, ErrFileNotFound) {
					// Another scan wrote, relinked, or deleted the row since it
					// was listed. It is no longer this scan's to mark.
					continue
				}
				if err != nil {
					slog.ErrorContext(ctx, "audiobook scan: failed to mark file missing", "component", "scanner",
						"folder_id", folder.ID, "path", mf.FilePath, "error", err)
					continue
				}
			}
			missing++
		}
	}

	trashed, removedMemberships, deletedItems, err := s.sweepMissingWithProtectedRoots(ctx, folder, protectedPaths)
	if trashed > 0 {
		slog.InfoContext(ctx, "audiobook scan: emptied trash", "component", "scanner", "folder_id", folder.ID, "deleted", trashed)
	}
	if err != nil {
		return err
	}
	if missing > 0 || removedMemberships > 0 || deletedItems > 0 {
		slog.InfoContext(ctx, "audiobook scan: reconciled missing files", "component", "scanner",
			"folder_id", folder.ID, "missing", missing,
			"memberships_removed", removedMemberships, "items_deleted", deletedItems)
	}
	return nil
}

// audiobookProgress returns the counts of an audiobook scan as a progress
// update, logging them when the processed count moved since the last call.
// Calls must not overlap.
func audiobookProgress(ctx context.Context, folderID, total int, start time.Time, processed, failed, skipped *int64) func() ProgressUpdate {
	logged := int64(-1)
	return func() ProgressUpdate {
		n, failedCount, skippedCount := atomic.LoadInt64(processed), atomic.LoadInt64(failed), atomic.LoadInt64(skipped)
		if n != logged {
			logged = n
			slog.InfoContext(ctx, "audiobook scan: progress", "component", "scanner",
				"folder_id", folderID,
				"processed", n,
				"failed", failedCount,
				"skipped", skippedCount,
				"total", total,
				"elapsed_sec", int(time.Since(start).Seconds()),
			)
		}
		return audiobookScanProgress(folderID, total, int(n), int(failedCount), int(skippedCount))
	}
}

func audiobookScanProgress(folderID int, total, processed, failed, skipped int) ProgressUpdate {
	return ProgressUpdate{
		Phase:           "audiobook_scan",
		Message:         fmt.Sprintf("Scanning audiobooks in folder %d", folderID),
		CurrentScope:    strconv.Itoa(folderID),
		TotalFiles:      total,
		FilesDiscovered: total,
		FilesProcessed:  processed,
		Errors:          failed,
		Unchanged:       skipped,
	}
}

// audiobookFolderUnchangedOnDisk runs audiobookFolderShouldSkip for the book
// at physical, logging a failed check, which then counts as changed.
func (s *Scanner) audiobookFolderUnchangedOnDisk(ctx context.Context, folder *models.MediaFolder, folderPath, physical string, physicalErr error, allowedPaths map[string]bool, walked *audiobookWalkedBook) (rejected int, unchanged bool) {
	err := physicalErr
	if err == nil {
		rejected, unchanged, err = s.audiobookFolderShouldSkip(ctx, folder, folderPath, physical, allowedPaths, walked)
	}
	// A folder gone since the walk is reported by the parse that follows.
	if err != nil && !errors.Is(physicalErr, os.ErrNotExist) {
		slog.WarnContext(ctx, "audiobook scan: skip-check failed, falling through", "component", "scanner",
			"folder_id", folder.ID,
			"path", folderPath,
			"error", err,
		)
	}
	return rejected, unchanged && err == nil
}

// audiobookRootUnmoved checks, after the parse, that folderPath still resolves
// to physical, the root resolved before it. The parse read the files through
// folderPath; a link that moved while it ran pointed them at another book, and
// writing them under physical would give that book's metadata to this one, so
// this scan leaves the book alone.
func audiobookRootUnmoved(folderPath, physical string, physicalErr error) error {
	if physicalErr == nil {
		var now string
		if now, physicalErr = canonicalWalkPath(folderPath); physicalErr == nil && now != physical {
			return fmt.Errorf("audiobook root %s moved from %s to %s while it was read; will rescan", folderPath, physical, now)
		}
	}
	if physicalErr != nil {
		return fmt.Errorf("upsert audiobook item: resolve audiobook root: %w", physicalErr)
	}
	return nil
}

// unreadable, when set, counts audio files ffprobe rejected that the book was
// indexed without.
func (s *Scanner) reconcileAudiobookFolder(ctx context.Context, folder *models.MediaFolder, folderPath string, allowedPaths map[string]bool, walked *audiobookWalkedBook, inventoryAt time.Time, skipped, unreadable *int64) error {
	// Resolved once per book, so the skip check, the identity lookup and the
	// rows it writes agree on the physical root.
	physical, physicalErr := canonicalWalkPath(folderPath)
	if rejected, unchanged := s.audiobookFolderUnchangedOnDisk(ctx, folder, folderPath, physical, physicalErr, allowedPaths, walked); unchanged {
		// Linking runs when either format is added or its metadata changes.
		// An unchanged, unlinked book must not repeat the candidate search.
		atomic.AddInt64(skipped, 1)
		if unreadable != nil {
			atomic.AddInt64(unreadable, int64(rejected))
		}
		return nil
	}
	parsed, err := parseAudiobookFolder(ctx, s.ffprobePath, folderPath, allowedPaths, walked)
	if err != nil {
		if errors.Is(err, errFolderHasNoMedia) {
			return err
		}
		var unreadableBook *noReadableAudiobookPartError
		if errors.As(err, &unreadableBook) {
			s.retireUnreadableAudiobookFiles(ctx, folder.ID, unreadableBook.paths)
		}
		return fmt.Errorf("parse audiobook folder %s: %w", folderPath, err)
	}
	if err := audiobookRootUnmoved(folderPath, physical, physicalErr); err != nil {
		return err
	}
	if unreadable != nil {
		atomic.AddInt64(unreadable, int64(len(parsed.UnreadableParts)))
	}

	contentID, err := s.upsertAudiobookMediaItem(ctx, folder.ID, folderPath, physical, parsed)
	if err != nil {
		return fmt.Errorf("upsert audiobook item: %w", err)
	}
	// Keep item metadata, file rows, and folder-level audiobook indexes in one
	// transaction. A multi-part book must not become visible with only some of
	// its parts after a worker or database failure.
	tx, err := s.fileRepo.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audiobook folder transaction: %w", err)
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelCleanup()
	defer func() { _ = tx.Rollback(cleanupCtx) }()
	if err := s.upsertAudiobookPresentationTx(ctx, tx, folder, contentID, folderPath, physical, parsed, inventoryAt); errors.Is(err, errAudiobookInventoryStale) {
		slog.InfoContext(ctx, "audiobook scan: book changed after this scan listed it; leaving it to the scan that saw the change", "component", "scanner", "folder_id", folder.ID, "path", folderPath)
		atomic.AddInt64(skipped, 1)
		return nil
	} else if err != nil {
		return fmt.Errorf("upsert audiobook presentation: %w", err)
	}
	if err := s.upsertAudiobookSeriesTx(ctx, tx, contentID, parsed); err != nil {
		return fmt.Errorf("upsert audiobook series: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, contentID, folder.ID); err != nil {
		return fmt.Errorf("upsert audiobook library membership: %w", err)
	}
	if parsed.ASIN != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
			VALUES ($1, 'asin', $2, 'audiobook')
			ON CONFLICT DO NOTHING
		`, contentID, parsed.ASIN); err != nil {
			return fmt.Errorf("upsert audiobook ASIN provider id: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audiobook folder transaction: %w", err)
	}
	s.applyAudiobookCovers(ctx, folder.ID, contentID, folderPath, parsed)
	if err := s.upsertAudiobookPeople(ctx, contentID, parsed); err != nil {
		return fmt.Errorf("upsert audiobook people: %w", err)
	}
	s.autoLinkLiteraryWork(ctx, contentID)
	noteAudiobookTagReread(ctx, contentID)
	slog.InfoContext(ctx, "audiobook scan: indexed", "component", "scanner",
		"folder_id", folder.ID,
		"content_id", contentID,
		"title", parsed.Title,
		"author", parsed.Author,
		"files", len(parsed.Files),
		"unreadable_files", len(parsed.UnreadableParts),
	)
	return nil
}

// applyAudiobookCovers sets a book's poster from a sidecar image in its folder
// or from the art embedded in its first file. Failures are logged.
func (s *Scanner) applyAudiobookCovers(ctx context.Context, folderID int, contentID, folderPath string, parsed *parsedAudiobook) {
	// A loose single-file book shares its directory, and so any cover image
	// there, with other books; only a book folder's cover belongs to the book.
	rootedAtFile := len(parsed.Files) == 1 && parsed.Files[0].Path == folderPath
	if !rootedAtFile {
		if err := applyAudiobookSidecarCover(ctx, s.itemRepo, s.imageCacher, contentID, folderPath); err != nil {
			slog.WarnContext(ctx, "audiobook scan: sidecar cover upload failed", "component", "scanner",
				"folder_id", folderID,
				"content_id", contentID,
				"path", folderPath,
				"error", err,
			)
		}
	}
	if len(parsed.Files) > 0 && s.fileRepo != nil {
		if _, err := applyAudiobookEmbeddedCover(ctx, s.itemRepo, s.fileRepo.Pool(), FFmpegPathFromFFprobe(s.ffprobePath), s.imageCacher, parsed.Files[0].Path, contentID); err != nil {
			slog.WarnContext(ctx, "audiobook scan: embedded cover failed", "component", "scanner",
				"folder_id", folderID,
				"content_id", contentID,
				"path", parsed.Files[0].Path,
				"error", err,
			)
		}
	}
}

// retireUnreadableAudiobookFiles marks missing the rows of a book whose every
// file ffprobe now rejects. The files are still on disk, so the missing-file
// sweep would otherwise keep offering a book that cannot play. Failure only
// delays this to the next scan, so it is logged rather than returned.
func (s *Scanner) retireUnreadableAudiobookFiles(ctx context.Context, folderID int, paths []string) {
	if s.fileRepo == nil || len(paths) == 0 {
		return
	}
	if _, err := s.fileRepo.Pool().Exec(ctx, `
		UPDATE media_files SET missing_since = COALESCE(missing_since, NOW())
		WHERE media_folder_id = $1 AND file_path = ANY($2) AND missing_since IS NULL
	`, folderID, paths); err != nil {
		slog.WarnContext(ctx, "audiobook scan: could not retire unreadable files", "component", "scanner",
			"folder_id", folderID, "error", err)
	}
}

func applyAudiobookEmbeddedCover(
	ctx context.Context,
	reader audiobookPosterPathReader,
	exec audiobookPosterExec,
	ffmpegPath string,
	cacher audiobookCoverCacher,
	audioFilePath string,
	contentID string,
) (bool, error) {
	if exec == nil {
		return false, nil
	}
	if reader != nil {
		existingPosterPath, err := reader.GetPosterPath(ctx, contentID)
		if err != nil {
			return false, fmt.Errorf("get audiobook poster path for embedded cover: %w", err)
		}
		if strings.TrimSpace(existingPosterPath) != "" {
			return false, nil
		}
	}
	poster, thumb := ExtractAndUploadAudiobookCover(ctx, ffmpegPath, cacher, audioFilePath, contentID)
	if poster == "" {
		return false, nil
	}
	_, err := exec.Exec(ctx, `
		UPDATE media_items
		SET poster_path = $1, poster_thumbhash = $2, updated_at = NOW()
		WHERE content_id = $3 AND (poster_path IS NULL OR poster_path = '')
	`, poster, thumb, contentID)
	if err != nil {
		return false, fmt.Errorf("update audiobook embedded cover: %w", err)
	}
	return true, nil
}

// upsertAudiobookMediaItem reuses an item already linked to the same filesystem
// root, then falls back to the stricter ABS duplicate rule, or creates a new row.
// It intentionally avoids title/year-only dedupe because audiobooks often share
// titles, unknown years, or edition-specific credits. Returns the content_id used.
func (s *Scanner) upsertAudiobookMediaItem(ctx context.Context, folderID int, folderPath, physical string, book *parsedAudiobook) (string, error) {
	if s.itemRepo == nil {
		return "", fmt.Errorf("itemRepo not configured on Scanner")
	}
	if s.fileRepo == nil {
		return "", fmt.Errorf("fileRepo not configured on Scanner")
	}

	// Include missing rows so a surviving alias restores the same identity.
	var existingID string
	err := s.fileRepo.Pool().QueryRow(ctx, `
		SELECT mf.content_id FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1 AND mf.canonical_root_path = $2
		  AND mi.type = 'audiobook'
		ORDER BY mf.missing_since NULLS FIRST, mf.id
		LIMIT 1
	`, folderID, physical).Scan(&existingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("find audiobook by physical root: %w", err)
	}
	if existingID == "" {
		// Upgrade books indexed before canonical roots followed symlinks.
		existingID, err = s.fileRepo.FindContentIDByRootPath(ctx, folderID, folderPath, "audiobook")
	} else {
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("find audiobook by root path: %w", err)
	}
	cleanTitle := stripNarratorSuffix(book.Title)
	if cleanTitle == "" {
		cleanTitle = book.Title
	}
	if existingID == "" {
		if existing := s.findAudiobookByFilePath(ctx, folderID, book); existing != nil {
			existingID = existing.ContentID
		} else if existing := s.findAudiobookDuplicate(ctx, folderID, book, cleanTitle); existing != nil {
			existingID = existing.ContentID
		}
	}
	contentID, err := s.claimAudiobookIdentity(ctx, folderID, physical, existingID, book, cleanTitle)
	if err != nil {
		return "", err
	}
	return contentID, nil
}

// Use the existing root-claim table so concurrent scoped scans through different
// aliases agree on identity before either has written its media files.
func (s *Scanner) claimAudiobookIdentity(ctx context.Context, folderID int, physical, existingID string, book *parsedAudiobook, title string) (string, error) {
	tx, err := s.fileRepo.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	defer func() { _ = tx.Rollback(cleanupCtx) }()
	if err := lockAudiobookRoot(ctx, tx, folderID, physical); err != nil {
		return "", err
	}
	var claimedID string
	err = tx.QueryRow(ctx, `SELECT content_id FROM media_item_roots
		WHERE media_folder_id = $1 AND canonical_root_path = $2`, folderID, physical).Scan(&claimedID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if claimedID == "" {
		claimedID = existingID
		if claimedID == "" {
			claimedID, err = idgen.NextID()
			if err != nil {
				return "", err
			}
			item := &models.MediaItem{ContentID: claimedID, SortTitle: titleutil.DeriveDefaultSortTitle(title)}
			applyBookToMediaItem(item, book, false)
			if err := s.itemRepo.UpsertTx(ctx, tx, item); err != nil {
				return "", err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id)
			VALUES ($1, $2, $3) ON CONFLICT (media_folder_id, canonical_root_path)
			DO UPDATE SET last_seen_at = NOW() RETURNING content_id`, folderID, physical, claimedID).Scan(&claimedID); err != nil {
			return "", err
		}
	} else if err := catalog.NewRootClaimRepository(s.fileRepo.Pool()).Claim(ctx, tx, folderID, physical, claimedID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return claimedID, nil
}

func lockAudiobookRoot(ctx context.Context, tx pgx.Tx, folderID int, physical string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("audiobook:%d:%s", folderID, physical))
	return err
}

func (s *Scanner) updateAudiobookMediaItemTx(ctx context.Context, tx pgx.Tx, contentID string, book *parsedAudiobook) error {
	// Lock the row before reading it and its match: the enricher writes a
	// match under the same lock, so this write cannot replace one that
	// commits in between with values from the tags.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, contentID); err != nil {
		return fmt.Errorf("lock audiobook media item %s: %w", contentID, err)
	}
	if err := runAudiobookTagRereadReset(ctx, tx, contentID); err != nil {
		return fmt.Errorf("reset audiobook match %s: %w", contentID, err)
	}
	item, err := s.itemRepo.GetByIDTx(ctx, tx, contentID)
	if err != nil {
		return fmt.Errorf("get audiobook media item %s: %w", contentID, err)
	}
	matched, err := audiobookProviderMatched(ctx, tx, contentID)
	if err != nil {
		return err
	}
	applyBookToMediaItem(item, book, matched)
	return s.itemRepo.UpsertTx(ctx, tx, item)
}

func resolveAudiobookMediaItem(
	ctx context.Context,
	rootFinder filesystemRootContentFinder,
	itemWriter filesystemMediaItemWriter,
	folderID int,
	folderPath string,
	book *parsedAudiobook,
) (string, error) {
	if rootFinder == nil {
		return "", fmt.Errorf("root content finder not configured")
	}
	if itemWriter == nil {
		return "", fmt.Errorf("media item writer not configured")
	}
	existingID, err := rootFinder.FindContentIDByRootPath(ctx, folderID, folderPath, "audiobook")
	if err != nil {
		return "", fmt.Errorf("find audiobook by root path: %w", err)
	}
	if existingID != "" {
		return existingID, nil
	}

	cleanTitle := stripNarratorSuffix(book.Title)
	if cleanTitle == "" {
		cleanTitle = book.Title
	}
	return createAudiobookMediaItem(ctx, itemWriter, book, cleanTitle)
}

func createAudiobookMediaItem(ctx context.Context, itemWriter filesystemMediaItemWriter, book *parsedAudiobook, cleanTitle string) (string, error) {
	id, err := idgen.NextID()
	if err != nil {
		return "", fmt.Errorf("generate content_id: %w", err)
	}
	item := &models.MediaItem{
		ContentID: id,
		Type:      "audiobook",
		Title:     cleanTitle,
		SortTitle: titleutil.DeriveDefaultSortTitle(cleanTitle),
		Year:      book.Year,
	}
	applyBookToMediaItem(item, book, false)
	if err := itemWriter.Upsert(ctx, item); err != nil {
		return "", err
	}
	return id, nil
}

// findAudiobookDuplicate returns an existing audiobook media_items row that
// matches the parsed book on (author, narrator, year, duration ±0.5%/±10s,
// punctuation-normalized title) and that this folder replaces. Used after the
// file-path lookup misses, so a moved or renamed folder keeps its item and
// listening progress. Returns nil when no match exists or any required
// attribute (author, narrator, year, duration) is missing — under-tagged files
// don't qualify for automatic dedup.
//
// The rule, per pickAudiobookDuplicate: a moved or renamed folder keeps its
// item; two copies on disk are two items. A candidate is reused only when it
// carries every tagged credit and none of its indexed files in this library
// is still on disk.
func (s *Scanner) findAudiobookDuplicate(ctx context.Context, folderID int, book *parsedAudiobook, cleanTitle string) *models.MediaItem {
	if s.fileRepo == nil {
		return nil
	}
	if book.Author == "" || book.Narrator == "" || book.Year == 0 {
		return nil
	}
	var totalDuration int
	for _, f := range book.Files {
		totalDuration += f.Duration
	}
	if totalDuration <= 0 {
		return nil
	}
	tolerance := totalDuration / 200 // 0.5%
	if tolerance < 10 {
		tolerance = 10
	}
	titleKey := normalizeAudiobookDedupeTitle(cleanTitle)
	if titleKey == "" {
		return nil
	}
	rows, err := s.fileRepo.Pool().Query(ctx,
		audiobookDuplicateCandidateSQL,
		book.Author,
		book.Narrator,
		book.Year,
		totalDuration,
		tolerance,
		titleKey,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var existingID string
	var candidates []string
	for rows.Next() {
		var candidateID, candidateTitle string
		if err := rows.Scan(&candidateID, &candidateTitle); err != nil {
			return nil
		}
		if audiobookDedupeTitlesMatch(candidateTitle, cleanTitle) {
			candidates = append(candidates, candidateID)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	rows.Close()
	credits, err := s.audiobookCandidateCredits(ctx, candidates)
	if err != nil {
		return nil
	}
	files, err := s.audiobookCandidateFiles(ctx, folderID, candidates)
	if err != nil {
		return nil
	}
	existingID = pickAudiobookDuplicate(candidates, credits, files, book.credits(), fspath.Stat)
	if existingID == "" {
		return nil
	}

	items, err := s.itemRepo.GetByIDs(ctx, []string{existingID})
	if err != nil || len(items) == 0 {
		return nil
	}
	return items[0]
}

// findAudiobookByFilePath returns an existing audiobook media_items row whose
// first part is one of this book's audio files, preferring the earliest part.
// Returns nil when no match exists (a fresh scan of a new folder).
//
// Matching only an item's first part lets a regrouped library keep identities
// without two books claiming one item: when separate disc items merge into one
// book, the book keeps disc 1's item; when one item made of loose files splits
// into one book per file, the first file keeps it and the rest get new items.
func (s *Scanner) findAudiobookByFilePath(ctx context.Context, folderID int, book *parsedAudiobook) *models.MediaItem {
	if s.fileRepo == nil || len(book.Files) == 0 {
		return nil
	}
	paths := audiobookLookupPaths(book.Files)
	if len(paths) == 0 {
		return nil
	}
	// A file matches its own row exactly. When no row has the exact path, a
	// row whose path differs only in case is the same file under other casing
	// (a case-insensitive filesystem, or a case-only rename) only if both
	// paths reach one file or the row's path is gone; on a case-sensitive
	// filesystem "A.mp3" and "a.mp3" are two files and must not claim one item.
	rows, err := s.fileRepo.Pool().Query(ctx, `
		SELECT mf.content_id, mf.file_path, p.path
		FROM unnest($1::text[]) WITH ORDINALITY AS p(path, ord)
		JOIN media_files mf ON mf.media_folder_id = $2 AND (
			mf.file_path = p.path
			OR (lower(mf.file_path) = lower(p.path) AND NOT EXISTS (
				SELECT 1 FROM media_files e WHERE e.media_folder_id = $2 AND e.file_path = p.path)))
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mi.type = 'audiobook'
		  AND COALESCE(mf.presentation_part_index, 0) <= 1
		ORDER BY p.ord, mf.id
	`, paths, folderID)
	if err != nil {
		return nil
	}
	var existingID string
	for rows.Next() {
		var id, rowPath, bookPath string
		if rows.Scan(&id, &rowPath, &bookPath) != nil {
			break
		}
		if rowPath == bookPath || sameAudiobookFileOrGone(bookPath, rowPath) {
			existingID = id
			break
		}
	}
	rows.Close()
	if rows.Err() != nil || existingID == "" {
		return nil
	}
	items, err := s.itemRepo.GetByIDs(ctx, []string{existingID})
	if err != nil || len(items) == 0 {
		return nil
	}
	return items[0]
}

// sameAudiobookFileOrGone reports whether indexed, a path differing from the
// book's file path only in case, still names that file: both reach the same
// file on disk, or indexed no longer exists.
func sameAudiobookFileOrGone(file, indexed string) bool {
	indexedInfo, err := fspath.Stat(indexed)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	fileInfo, err := fspath.Stat(file)
	return err == nil && fspath.SameFile(fileInfo, indexedInfo)
}

func audiobookLookupPaths(files []parsedAudiobookFile) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if path := strings.TrimSpace(f.Path); path != "" && !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths
}

// Lockable metadata fields the audiobook and ebook scans write from file
// metadata. These mirror metadata.MetadataField, which this package cannot
// import (metadata imports scanner). The values are stored in
// media_items.locked_fields, so they cannot change without a data migration.
const (
	lockedFieldName         = 0
	lockedFieldOverview     = 1
	lockedFieldGenres       = 2
	lockedFieldStudios      = 3
	lockedFieldCast         = 4
	lockedFieldCrew         = 5
	lockedFieldReleaseDates = 13
)

// applyBookToMediaItem copies parsed-audiobook tag fields onto the
// MediaItem. Used for both fresh inserts and re-scans of existing rows
// so manual edits to fields not driven by the file (e.g., poster_path
// set by the metadata enricher) survive.
//
// Tags own the title. Year, release date, genres and studios belong to the
// tags only while the book has no provider match; once matched, the provider
// owns them and the tags only fill a field that is still empty, so a rescan
// cannot mix a second source into a matched book.
//
// Audiobook titles are stripped of trailing narrator/edition suffixes
// (`Foo (Read by Bar)`, `Foo - read by Bar`, etc.) since that data is
// already captured as item_people kind=8. The raw tag value is preserved
// in OriginalTitle when it differs so the original is never lost.
func applyBookToMediaItem(item *models.MediaItem, book *parsedAudiobook, providerMatched bool) {
	item.Type = "audiobook"
	if !slices.Contains(item.LockedFields, lockedFieldName) {
		raw := strings.TrimSpace(book.Title)
		cleaned := stripNarratorSuffix(raw)
		if cleaned == "" {
			cleaned = raw
		}
		item.Title = cleaned
		if cleaned != raw && item.OriginalTitle == "" {
			item.OriginalTitle = raw
		}
		// The default sort title follows the title: one derived from an
		// earlier title is derived again, so a book whose title changes does
		// not keep sorting under its old name. Any other sort title is
		// someone's own and stays.
		if item.SortTitle == "" || titleutil.IsDefaultSortTitle(item.SortTitle) {
			item.SortTitle = titleutil.DeriveDefaultSortTitle(item.Title)
		}
	}
	releaseDatesLocked := slices.Contains(item.LockedFields, lockedFieldReleaseDates)
	if !releaseDatesLocked && (!providerMatched || item.Year == 0) {
		item.Year = book.Year
	}
	if book.Overview != "" && item.Overview == "" && !slices.Contains(item.LockedFields, lockedFieldOverview) {
		item.Overview = book.Overview
	}
	if book.Publisher != "" && !slices.Contains(item.LockedFields, lockedFieldStudios) && (!providerMatched || len(item.Studios) == 0) {
		item.Studios = mergeUniqueStrings(item.Studios, []string{book.Publisher})
	}
	if len(book.Genres) > 0 && !slices.Contains(item.LockedFields, lockedFieldGenres) && (!providerMatched || len(item.Genres) == 0) {
		item.Genres = mergeUniqueStrings(item.Genres, book.Genres)
	}
	if rd := normalizeReleaseDateForSQL(book.ReleaseDate); rd != "" && !releaseDatesLocked && (!providerMatched || item.ReleaseDate == nil || *item.ReleaseDate == "") {
		item.ReleaseDate = &rd
	}
	if book.Language != "" && item.OriginalLanguage == "" {
		item.OriginalLanguage = book.Language
	}
}

// normalizeReleaseDateForSQL coerces tag-derived date strings into the
// ISO YYYY-MM-DD shape media_items.release_date expects. A real calendar
// date passes through. A year alone ("2018"), or a year followed by a month
// or day that is not one ("2015-00-00", which some taggers write for an
// unknown date), becomes January 1 of that year. Everything else returns ""
// (skip the column): a date Postgres rejects would fail the whole book.
func normalizeReleaseDateForSQL(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return ""
	}
	year := s[:4]
	if strings.Trim(year, "0123456789") != "" || year == "0000" {
		return ""
	}
	if len(s) >= 10 {
		if _, err := time.Parse(time.DateOnly, s[:10]); err == nil {
			return s[:10]
		}
	}
	return year + "-01-01"
}

func mergeUniqueStrings(existing, additions []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	out := make([]string, 0, len(existing)+len(additions))
	for _, v := range existing {
		k := strings.ToLower(strings.TrimSpace(v))
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	for _, v := range additions {
		k := strings.ToLower(strings.TrimSpace(v))
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	return out
}

// errAudiobookInventoryStale reports that another scan published a part of the
// book after this scan listed the book's files, so this scan's list is older.
var errAudiobookInventoryStale = errors.New("audiobook changed after this scan listed it")

// audiobookInventoryStaleTx reports whether a present file of the physical
// book, written at or after inventoryAt (the database time before this scan
// walked), has a name the book's parsed files lack. A part's name is its path
// relative to the book root ("CD1/01.mp3" for a part in a disc folder, "" for
// a book rooted at its file), which every alias of the book shares. Such a
// file was published by a scan that saw it while this one did not, so writing
// this scan's parts would drop it and retire its alias. The caller holds the
// book's root lock until tx ends. A zero inventoryAt skips the check.
func audiobookInventoryStaleTx(ctx context.Context, tx pgx.Tx, folderID int, physical, folderPath string, book *parsedAudiobook, inventoryAt time.Time) (bool, error) {
	if inventoryAt.IsZero() {
		return false, nil
	}
	names := make([]string, 0, len(book.Files))
	for _, file := range book.Files {
		name := ""
		if file.Path != folderPath {
			rel, err := filepath.Rel(folderPath, file.Path)
			if err != nil {
				return false, err
			}
			name = filepath.ToSlash(rel)
		}
		names = append(names, name)
	}
	var stale bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM media_files
			WHERE media_folder_id = $1 AND canonical_root_path = $2
			  AND missing_since IS NULL AND updated_at >= $3
			  AND CASE WHEN file_path = observed_root_path THEN ''
			      ELSE substr(file_path, length(observed_root_path) + 2) END <> ALL($4::text[])
		)`, folderID, physical, inventoryAt, names).Scan(&stale)
	return stale, err
}

// upsertAudiobookPresentationTx writes item metadata and one row per audio
// file under the physical root lock. File and observed-root paths retain the
// selected logical alias; canonical-root paths identify the physical book.
func (s *Scanner) upsertAudiobookPresentationTx(
	ctx context.Context,
	tx pgx.Tx,
	folder *models.MediaFolder,
	contentID string,
	folderPath string,
	physical string,
	book *parsedAudiobook,
	inventoryAt time.Time,
) error {
	if err := lockAudiobookRoot(ctx, tx, folder.ID, physical); err != nil {
		return err
	}
	if stale, err := audiobookInventoryStaleTx(ctx, tx, folder.ID, physical, folderPath, book, inventoryAt); err != nil {
		return fmt.Errorf("check audiobook inventory: %w", err)
	} else if stale {
		return errAudiobookInventoryStale
	}
	// The selected alias supplies both filesystem-derived metadata and files.
	// Keep them under the same lock and commit so concurrent scans agree.
	if err := s.updateAudiobookMediaItemTx(ctx, tx, contentID, book); err != nil {
		return err
	}
	partTotal := len(book.Files)
	probeUpdatedAt := time.Now().UTC()
	mediaFiles := make([]models.MediaFile, 0, partTotal)
	for idx, af := range book.Files {
		info, err := fspath.Stat(af.Path)
		if err != nil {
			return fmt.Errorf("stat media file %s: %w", af.Path, err)
		}
		// The probe read a specific version of the file. If size or mtime
		// moved while it ran, the probe facts describe bytes this row would
		// not: fail the folder so the next scan reprocesses it instead of
		// persisting a mismatch the unchanged check would then trust.
		if af.Size != info.Size() || !sameFileModifiedAt(&af.ModifiedAt, info.ModTime()) {
			return fmt.Errorf("media file %s changed while it was probed; will rescan", af.Path)
		}
		modifiedAt := normalizeFileModifiedAt(info.ModTime())
		chapters := make([]models.MediaChapter, len(af.Chapters))
		for i, ch := range af.Chapters {
			chapters[i] = models.MediaChapter{
				Index:        ch.Index,
				Title:        ch.Title,
				StartSeconds: ch.StartSeconds,
				EndSeconds:   ch.EndSeconds,
				Source:       ch.Source,
			}
		}

		mf := models.MediaFile{
			ContentID:          contentID,
			MediaFolderID:      folder.ID,
			CanonicalRootPath:  physical,
			ObservedRootPath:   folderPath,
			ContentGroupKey:    contentID,
			GroupKeyVersion:    1,
			BaseTitle:          book.Title,
			BaseYear:           book.Year,
			BaseType:           "audiobook",
			IdentityConfidence: audiobookIdentityConfidence(book, af),
			FilePath:           af.Path,
			FileSize:           info.Size(),
			FileModifiedAt:     &modifiedAt,
			Chapters:           chapters,
			ProbeSource:        "local",
			ProbeUpdatedAt:     &probeUpdatedAt,
			Duration:           af.Duration,
			Bitrate:            af.Bitrate,
			CodecAudio:         af.CodecAudio,
			Container:          af.Container,
			AudioChannels:      af.AudioChannels,
		}
		if partTotal > 1 {
			mf.PresentationKind = audiobookMultipartKind
			mf.PresentationGroupKey = contentID
			mf.PresentationPartIndex = idx + 1
			mf.PresentationPartTotal = partTotal
		}

		mediaFiles = append(mediaFiles, mf)
	}
	if err := s.fileRepo.UpsertBatchTx(ctx, tx, mediaFiles); err != nil {
		return fmt.Errorf("upsert audiobook media files: %w", err)
	}
	// One physical recording has one playable set of parts. Retire the old
	// alias only after its replacement files have been written in this same
	// transaction, so a failed scan cannot take away the working location.
	if _, err := tx.Exec(ctx, `
		UPDATE media_files SET missing_since = COALESCE(missing_since, NOW())
		WHERE media_folder_id = $1 AND content_id = $2
		  AND canonical_root_path = $3 AND observed_root_path <> $4
	`, folder.ID, contentID, physical, folderPath); err != nil {
		return fmt.Errorf("retire alternate audiobook paths: %w", err)
	}
	// A part indexed earlier that ffprobe now rejects is no longer playable.
	// Its file is still on disk, so the missing-file sweep would keep it as a
	// part of a book; retire it here, whichever item its row belonged to.
	if len(book.UnreadableParts) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE media_files SET missing_since = COALESCE(missing_since, NOW())
			WHERE media_folder_id = $1 AND file_path = ANY($2)
		`, folder.ID, book.UnreadableParts); err != nil {
			return fmt.Errorf("retire unreadable audiobook parts: %w", err)
		}
	}
	return nil
}

// audiobookCredit pairs a person name with a credit kind. Used to compare
// the desired-from-tags set against the existing item_people set without
// having to materialize a full []models.ItemPerson (which requires
// resolved person IDs).
type audiobookCredit struct {
	Name string
	Kind models.PersonKind
}

// audiobookPeopleCreditsEqual returns true when the existing item_people
// rows for an audiobook match the desired credit set one-for-one on
// (case-insensitive name, kind). Order is irrelevant because the upsert
// path orders by SortOrder.
//
// Case-insensitive comparison: audiobook tag casing drifts between rips
// and we don't want a stylistic re-cap to trigger DELETE+INSERT on every
// scan.
func audiobookPeopleCreditsEqual(existing []models.ItemPerson, desired []audiobookCredit) bool {
	if len(existing) != len(desired) {
		return false
	}
	type key struct {
		name string
		kind models.PersonKind
	}
	have := make(map[key]struct{}, len(existing))
	for _, p := range existing {
		have[key{strings.ToLower(strings.TrimSpace(p.Person.Name)), p.Kind}] = struct{}{}
	}
	for _, d := range desired {
		k := key{strings.ToLower(strings.TrimSpace(d.Name)), d.Kind}
		if _, ok := have[k]; !ok {
			return false
		}
	}
	return true
}

// credits returns the book's authors then narrators, one credit per person.
func (b *parsedAudiobook) credits() []audiobookCredit {
	var out []audiobookCredit
	add := func(names []string, single string, kind models.PersonKind) {
		if len(names) == 0 && single != "" {
			names = []string{single}
		}
		for _, name := range names {
			out = append(out, audiobookCredit{Name: name, Kind: kind})
		}
	}
	add(b.Authors, b.Author, models.PersonKindAuthor)
	add(b.Narrators, b.Narrator, models.PersonKindNarrator)
	return out
}

// pickAudiobookDuplicate returns the first candidate, in query order, that
// this folder replaces, or "" when none does.
//   - Credits: the query matches on the first author and narrator only, and
//     recordings that share those but differ in the rest of the cast are
//     different editions, so every tagged credit must be on the candidate. The
//     candidate may have more, as a provider match stores fuller credits.
//   - Location: the folder replaces the candidate only when none of the
//     candidate's indexed files is still on disk, which tells a move from a
//     second copy. A stat error other than not-exist counts as present, so a
//     storage hiccup cannot merge two copies.
func pickAudiobookDuplicate(
	candidates []string,
	credits map[string][]models.ItemPerson,
	files map[string][]string,
	desired []audiobookCredit,
	stat func(string) (os.FileInfo, error),
) string {
	for _, candidateID := range candidates {
		if !audiobookCreditsContain(credits[candidateID], desired) {
			continue
		}
		if !anyAudiobookFilePresent(files[candidateID], stat) {
			return candidateID
		}
	}
	return ""
}

func anyAudiobookFilePresent(paths []string, stat func(string) (os.FileInfo, error)) bool {
	for _, path := range paths {
		if _, err := stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

// audiobookCandidateFiles loads the duplicate candidates' indexed, not
// missing file paths in this library in one query.
func (s *Scanner) audiobookCandidateFiles(ctx context.Context, folderID int, contentIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(contentIDs))
	if len(contentIDs) == 0 {
		return out, nil
	}
	rows, err := s.fileRepo.Pool().Query(ctx, `
		SELECT content_id, file_path FROM media_files
		WHERE content_id = ANY($1) AND media_folder_id = $2 AND missing_since IS NULL
	`, contentIDs, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var contentID, path string
		if err := rows.Scan(&contentID, &path); err != nil {
			return nil, err
		}
		out[contentID] = append(out[contentID], path)
	}
	return out, rows.Err()
}

// audiobookCreditsContain reports whether every desired credit appears in
// existing with the same kind, comparing names case-insensitively. existing
// may hold more credits.
func audiobookCreditsContain(existing []models.ItemPerson, desired []audiobookCredit) bool {
	type key struct {
		name string
		kind models.PersonKind
	}
	have := make(map[key]struct{}, len(existing))
	for _, p := range existing {
		have[key{strings.ToLower(strings.TrimSpace(p.Name)), p.Kind}] = struct{}{}
	}
	for _, d := range desired {
		if _, ok := have[key{strings.ToLower(strings.TrimSpace(d.Name)), d.Kind}]; !ok {
			return false
		}
	}
	return true
}

// audiobookCandidateCredits loads the author and narrator credits of the
// duplicate candidates in one query.
func (s *Scanner) audiobookCandidateCredits(ctx context.Context, contentIDs []string) (map[string][]models.ItemPerson, error) {
	out := make(map[string][]models.ItemPerson, len(contentIDs))
	if len(contentIDs) == 0 {
		return out, nil
	}
	rows, err := s.fileRepo.Pool().Query(ctx, `
		SELECT ip.content_id, p.name, ip.kind
		FROM item_people ip JOIN people p ON p.id = ip.person_id
		WHERE ip.content_id = ANY($1) AND ip.kind IN ($2, $3)
	`, contentIDs, models.PersonKindAuthor, models.PersonKindNarrator)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var contentID string
		var person models.ItemPerson
		if err := rows.Scan(&contentID, &person.Name, &person.Kind); err != nil {
			return nil, err
		}
		out[contentID] = append(out[contentID], person)
	}
	return out, rows.Err()
}

// upsertAudiobookPeople upserts author and narrator rows into item_people,
// using the PersonRepository to find-or-create each person by name. For a book
// without a provider match, differing parsed credits replace the stored ones.
// Once a provider matched the book, its credits stay, and the tags only add a
// kind (author or narrator) the item has no credit for. An admin's crew lock,
// which holds authors and narrators, keeps the credits from either change.
// Skips the write entirely when the existing credit set already matches the
// desired set (case-insensitive on name, exact on kind).
func (s *Scanner) upsertAudiobookPeople(ctx context.Context, contentID string, book *parsedAudiobook) error {
	if s.personRepo == nil {
		return fmt.Errorf("personRepo not configured on Scanner")
	}

	desired := book.credits()
	if len(desired) == 0 {
		return nil
	}

	existing, err := s.itemRepo.GetPeople(ctx, contentID)
	if err == nil && audiobookPeopleCreditsEqual(existing, desired) {
		return nil
	}

	// A person row nobody is credited with is harmless, so resolve the names
	// before taking the item's row lock.
	people := make([]models.ItemPerson, 0, len(desired))
	for i, c := range desired {
		personID, err := s.personRepo.FindOrCreate(ctx, models.Person{Name: c.Name})
		if err != nil {
			return fmt.Errorf("find-or-create person %q: %w", c.Name, err)
		}
		people = append(people, models.ItemPerson{
			Person:    models.Person{ID: personID},
			Kind:      c.Kind,
			SortOrder: i,
		})
	}

	tx, err := s.fileRepo.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audiobook credits: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Decide under the item's row lock, which the enricher also takes to write
	// a match, so a match committed since the read above is seen here.
	var locked []int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(locked_fields, '{}'::integer[]) FROM media_items WHERE content_id = $1 FOR UPDATE`,
		contentID).Scan(&locked); err != nil {
		return fmt.Errorf("read audiobook field locks: %w", err)
	}
	if slices.Contains(locked, lockedFieldCrew) {
		return nil
	}
	matched, err := audiobookProviderMatched(ctx, tx, contentID)
	if err != nil {
		return err
	}
	if !matched {
		if err := s.itemRepo.ReplacePeopleTx(ctx, tx, contentID, people); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var credited []int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(array_agg(DISTINCT kind), '{}'::integer[]) FROM item_people WHERE content_id = $1`,
		contentID).Scan(&credited); err != nil {
		return fmt.Errorf("read audiobook credit kinds: %w", err)
	}
	missing := slices.DeleteFunc(people, func(p models.ItemPerson) bool { return slices.Contains(credited, int(p.Kind)) })
	if len(missing) == 0 {
		return nil
	}
	if err := s.itemRepo.ReplacePeopleKindsTx(ctx, tx, contentID, catalog.PersonKindsOf(missing), missing); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// audiobookProviderMatched reports whether a metadata provider identified the
// book; see catalog.AudiobookProviderMatchedWhere.
func audiobookProviderMatched(ctx context.Context, q fileQueryer, contentID string) (bool, error) {
	var matched bool
	if err := q.QueryRow(ctx, `SELECT `+catalog.AudiobookProviderMatchedWhere("$1"), contentID).Scan(&matched); err != nil {
		return false, fmt.Errorf("read audiobook provider match: %w", err)
	}
	return matched, nil
}

// upsertAudiobookSeries writes the parsed series_name and series_index into
// the audiobook_series table, overwriting any prior row (e.g. one populated
// by migration 145's title-pattern backfill). A blank book.Series clears
// the row so books explicitly retagged out of a series stop appearing in
// the "In this series" rail on the next scan.
func (s *Scanner) upsertAudiobookSeriesTx(ctx context.Context, tx pgx.Tx, contentID string, book *parsedAudiobook) error {
	if s.fileRepo == nil {
		return fmt.Errorf("fileRepo not configured on Scanner")
	}
	desiredName := strings.TrimSpace(book.Series)
	desiredIdx := parseSeriesIndex(book.SeriesPosition)

	// Read current row.
	var currentName *string
	var currentIdx *float64
	err := tx.QueryRow(ctx, `
		SELECT series_name, series_index FROM audiobook_series WHERE content_id = $1
	`, contentID).Scan(&currentName, &currentIdx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("query audiobook_series: %w", err)
	}

	if desiredName == "" {
		if currentName == nil {
			return nil // already absent
		}
		if _, delErr := tx.Exec(ctx,
			`DELETE FROM audiobook_series WHERE content_id = $1`, contentID); delErr != nil {
			return fmt.Errorf("delete audiobook_series row: %w", delErr)
		}
		return nil
	}

	if currentName != nil && *currentName == desiredName && floatPtrEqual(currentIdx, desiredIdx) {
		return nil // identical row, skip the write
	}

	var idx any
	if desiredIdx != nil {
		idx = *desiredIdx
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audiobook_series (content_id, series_name, series_index, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (content_id) DO UPDATE SET
			series_name  = EXCLUDED.series_name,
			series_index = EXCLUDED.series_index,
			updated_at   = NOW()
	`, contentID, desiredName, idx); err != nil {
		return fmt.Errorf("upsert audiobook_series row: %w", err)
	}
	return nil
}

func audiobookIdentityConfidence(book *parsedAudiobook, file parsedAudiobookFile) string {
	if book == nil {
		return "low"
	}

	score := 0
	if book.Title != "" {
		score++
	}
	if book.Author != "" || book.Narrator != "" {
		score++
	}
	if book.Year > 0 {
		score++
	}
	for _, ch := range file.Chapters {
		if ch.Title != "" && ch.EndSeconds > ch.StartSeconds {
			score++
			break
		}
	}

	switch {
	case score >= 4:
		return "high"
	case score > 0:
		return "medium"
	default:
		return "low"
	}
}

// floatPtrEqual returns true when two *float64 values represent the same
// state — both nil, or both non-nil and equal. Used to skip audiobook_series
// re-upserts when only the series_index needs to be NULL=NULL compared.
func floatPtrEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// parseSeriesIndex extracts the series position from a freeform tag like
// "5", "1.5", "2 of 8", "1a", "Book 3", "#3", "Vol. 3", or "III". Returns nil
// when no position is present so the audiobook_series.series_index column
// stays NULL rather than carrying a misleading zero.
func parseSeriesIndex(raw string) *float64 {
	raw = strings.TrimSpace(raw)
	raw = audiobookSeriesPositionPrefixRE.ReplaceAllString(raw, "")
	if raw == "" {
		return nil
	}
	end := 0
	dot := false
	for end < len(raw) {
		c := raw[end]
		if c >= '0' && c <= '9' {
			end++
			continue
		}
		if c == '.' && !dot {
			dot = true
			end++
			continue
		}
		break
	}
	if end == 0 {
		if n := parseRomanNumeral(strings.Fields(raw)[0]); n > 0 {
			v := float64(n)
			return &v
		}
		return nil
	}
	var v float64
	if _, err := fmt.Sscanf(raw[:end], "%f", &v); err != nil {
		return nil
	}
	return &v
}
