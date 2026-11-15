package scanner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// audiobookDiscDirRE matches the folders one book's audio is split across:
// "Disc 1", "Disk 02", "CD1", "CD 2", "Part 3", "Pt. 4", "Disc 1 of 3".
// Removing "|part|pt" stops Part folders counting as discs.
var audiobookDiscDirRE = regexp.MustCompile(`(?i)^(?:disc|disk|cd|part|pt)[\s._-]*(\d{1,3})(?:\s*of\s*\d{1,3})?$`)

const sampleDirName = "sample"

// audiobookExtrasDirNames are folders beside a book's audio that hold bonus
// material rather than another book. The walk does not enter them, so their
// audio is neither a part of the book nor a book of its own.
var audiobookExtrasDirNames = map[string]bool{
	"bonus":          true,
	"bonus material": true,
	"extra":          true,
	"extras":         true,
	sampleDirName:    true,
	"samples":        true,
	"supplement":     true,
	"supplements":    true,
}

func isAudiobookDiscDirName(name string) bool {
	return audiobookDiscNumber(name) > 0
}

func isAudiobookExtrasDirName(name string) bool {
	return audiobookExtrasDirNames[strings.ToLower(strings.TrimSpace(name))]
}

// audiobookDiscNumber returns a disc folder's number, or 0 for any other name
// (including "Disc 0", which is not a disc).
func audiobookDiscNumber(name string) int {
	m := audiobookDiscDirRE.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// audiobookBookParts lists the audio files of the book rooted at root, in part
// order. A book is rooted at a directory or, for a loose single-file book, at
// the file itself. A directory book holds its own audio files and the audio
// directly inside its disc subfolders; parts are ordered by disc number (the
// directory's own files first), then by natural file order.
//
// allowedPaths is the inventory accepted by discovery: the audio files it
// accepted under the ignore rules, and the disc folders it took parts from.
// Only those disc folders are read, so a folder discovery excluded or did not
// treat as a disc (a configured library root, say) adds no parts and cannot
// fail the book. A nil inventory allows every audio file and disc folder. The
// error wraps errFolderHasNoMedia when root is gone or holds no allowed audio.
func audiobookBookParts(root string, allowedPaths map[string]bool) ([]string, error) {
	return (*audiobookWalkedBook)(nil).parts(root, allowedPaths)
}

// audiobookWalkedBook is what the walk already read of a book folder: its
// canonical path and the listings of the folder and of the disc folders it
// took. Parts are listed from it instead of reading the folders again. When
// the walk could not take a listing whole (an ignored link it never
// resolved), listed is false and the folders are read from disk as before.
// A scan holds one for every book until the book is reconciled, so it keeps
// only what parts reads and shares the walk's path strings.
type audiobookWalkedBook struct {
	canonical string
	listed    bool
	root      audiobookDirFiles     // the book folder
	discs     []audiobookWalkedDisc // the disc folders the walk took, if any
}

// audiobookWalkedDisc is a disc folder's audio files.
type audiobookWalkedDisc struct {
	dir   string
	files []string
}

// audiobookInventory is what a scan's walks found: the paths they accepted
// (audiobookRootScan.seenPaths) and what each walk read of its book folders.
type audiobookInventory struct {
	seen  map[string]bool
	walks []map[string]*audiobookWalkedBook
}

func newAudiobookInventory(seen map[string]bool, scans []audiobookRootScan) audiobookInventory {
	inventory := audiobookInventory{seen: seen}
	for _, scan := range scans {
		if len(scan.books) > 0 {
			inventory.walks = append(inventory.walks, scan.books)
		}
	}
	return inventory
}

// book returns what the first walk to find path read of it, as
// groupAudiobookCandidates keeps the first walk's path; nil for none.
func (inv audiobookInventory) book(path string) *audiobookWalkedBook {
	for _, books := range inv.walks {
		if book, ok := books[path]; ok {
			return book
		}
	}
	return nil
}

// audiobookDirFiles is one directory as listAudiobookDir returns it, less
// the entries parts never reads: other files and folders.
type audiobookDirFiles struct {
	files, dirs []string
}

// newAudiobookWalkedBook records the book folder root with the listings of
// root and of the disc folders the walk took. A listing the walk could not
// take whole (nil) leaves the book unlisted, so every folder of it is read
// from disk.
func newAudiobookWalkedBook(canonical, root string, files *audiobookDirFiles, discs []audiobookWalkedDisc, discsWhole bool) *audiobookWalkedBook {
	if canonical == root {
		canonical = root // share the walk's string
	}
	book := &audiobookWalkedBook{canonical: canonical}
	if files != nil && discsWhole {
		book.listed, book.root, book.discs = true, *files, discs
	}
	return book
}

// release drops the listings once the book is reconciled. Only the worker
// reconciling the book reads it.
func (b *audiobookWalkedBook) release() {
	if b != nil {
		b.listed, b.root, b.discs = false, audiobookDirFiles{}, nil
	}
}

// add records one entry that parts can use: an audio file or a disc folder.
// A nil listing stays nil.
func (f *audiobookDirFiles) add(path string, isDir bool) {
	switch {
	case f == nil:
	case isDir:
		if isAudiobookDiscDirName(filepath.Base(path)) {
			f.dirs = append(f.dirs, path)
		}
	case SupportsAudioFile(path):
		f.files = append(f.files, path)
	}
}

// parts is audiobookBookParts over the walk's listings, reading from disk any
// folder the walk did not list. A nil book reads everything from disk.
func (b *audiobookWalkedBook) parts(root string, allowedPaths map[string]bool) ([]string, error) {
	allowed := func(path string) bool {
		return SupportsAudioFile(path) && (allowedPaths == nil || allowedPaths[path])
	}
	list := func(dir string) ([]string, []string, error) {
		if files, dirs, ok := b.listing(root, dir); ok {
			return files, dirs, nil
		}
		return listAudiobookDir(dir)
	}
	if _, _, ok := b.listing(root, root); !ok {
		if parts, err := audiobookFileRoot(root, allowed); err != nil || parts != nil {
			return parts, err
		}
	}

	type part struct {
		path string
		disc int
	}
	var parts []part
	add := func(files []string, disc int) {
		for _, file := range files {
			if allowed(file) {
				parts = append(parts, part{path: file, disc: disc})
			}
		}
	}
	files, dirs, err := list(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("audiobook folder %s: %w", root, errFolderHasNoMedia)
		}
		return nil, err
	}
	add(files, 0)
	for _, dir := range dirs {
		disc := audiobookDiscNumber(filepath.Base(dir))
		if disc == 0 || allowedPaths != nil && !allowedPaths[dir] {
			continue
		}
		discFiles, _, err := list(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		add(discFiles, disc)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("audiobook folder %s: %w", root, errFolderHasNoMedia)
	}
	// Audio parts are commonly named with unpadded numbers (part2, part10).
	// Use the same natural ordering as ebook archive pages so playback follows
	// human order instead of byte/lexical order.
	sort.SliceStable(parts, func(i, j int) bool {
		if parts[i].disc != parts[j].disc {
			return parts[i].disc < parts[j].disc
		}
		return naturalPathLess(parts[i].path, parts[j].path)
	})
	paths := make([]string, len(parts))
	for i, p := range parts {
		paths[i] = p.path
	}
	return paths, nil
}

// audiobookFileRoot returns the one part of a loose file book, or nothing
// when root is a folder. It fails when root is gone or not an allowed audio
// file.
func audiobookFileRoot(root string, allowed func(string) bool) ([]string, error) {
	info, err := fspath.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The book was renamed or deleted between the walk and this read.
			// It holds no media now, so the caller skips it as it would an
			// empty folder rather than counting a scan failure.
			return nil, fmt.Errorf("audiobook %s: %w", root, errFolderHasNoMedia)
		}
		return nil, fmt.Errorf("stat audiobook %s: %w", root, err)
	}
	if info.IsDir() {
		return nil, nil
	}
	if !allowed(root) {
		return nil, fmt.Errorf("audiobook %s: %w", root, errFolderHasNoMedia)
	}
	return []string{root}, nil
}

// listing returns the walk's listing of dir, the book folder root or one of
// its disc folders.
func (b *audiobookWalkedBook) listing(root, dir string) (files, dirs []string, ok bool) {
	if b == nil || !b.listed {
		return nil, nil, false
	}
	if dir == root {
		return b.root.files, b.root.dirs, true
	}
	for _, disc := range b.discs {
		if disc.dir == dir {
			return disc.files, nil, true
		}
	}
	return nil, nil, false
}

// partsOrRelist lists the book's parts and runs use on them. When use fails
// on a part that is gone, the walk's listing is out of date: the parts are
// listed from disk, and use runs again if they differ, as it would have run
// on a fresh listing.
func (b *audiobookWalkedBook) partsOrRelist(root string, allowedPaths map[string]bool, use func([]string) error) error {
	parts, err := b.parts(root, allowedPaths)
	if err != nil {
		return err
	}
	err = use(parts)
	if b == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fresh, freshErr := audiobookBookParts(root, allowedPaths)
	if freshErr != nil {
		return freshErr
	}
	if slices.Equal(fresh, parts) {
		return err
	}
	return use(fresh)
}

// listAudiobookDir returns the files and the subdirectories, symlinks
// resolved, directly inside dir. A symlink whose target is gone is listed as a
// file, which no part inventory admits; one that cannot be inspected fails the
// listing, so a book is not rebuilt without a part it may hold.
func listAudiobookDir(dir string) (files, dirs []string, err error) {
	entries, err := fspath.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read audiobook folder %s: %w", dir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := fspath.Stat(path)
			switch {
			case err == nil:
				isDir = info.IsDir()
			case !errors.Is(err, os.ErrNotExist):
				return nil, nil, fmt.Errorf("inspect audiobook entry %s: %w", path, err)
			}
		}
		if isDir {
			dirs = append(dirs, path)
		} else {
			files = append(files, path)
		}
	}
	return files, dirs, nil
}

// audiobookScope is a scoped walk root with the ignore rules it starts from,
// read while resolving it (see audiobookScopeRoot).
type audiobookScope struct {
	rules   []ignoreRules
	ignored bool
	err     error
}

// audiobookScopeRoot returns the folder W a scoped audiobook scan walks for
// scope, with the ignore rules inherited from above W and whether they skip
// it. W is chosen so that every book the scope touches is classified inside W
// exactly as a library scan classifies it.
//
// Let B be the deepest configured library root that holds the scope; it is
// also the root of the walk that discovers the scope's books. A file is
// scanned through its folder, whose book it is part of. Then every folder from
// there up to, but not including, B is checked: a disc or extras folder
// belongs to, or is excluded by, the book above it, so W widens to its parent.
// No other folder needs widening, because a folder's own book does not depend
// on what its subfolders hold.
//
// W never reaches B: a scope that is itself a configured root does not widen,
// and no scope widens across one. The walk needs no widening to B, because it
// treats disc and extras folders that are configured roots, or sit directly in
// one, as ordinary folders.
//
// Ancestors are read once, outermost first, so the first one that qualifies is
// the widest and ends the pass; an unreadable folder below it cannot fail the
// scope. An ancestor the library walk does not enter, such as a symlink back
// to a folder above it, keeps the scope as it is.
func audiobookScopeRoot(scope string, libraryRoots []string) (string, audiobookScope) {
	scope = filepath.Clean(scope)
	if info, err := fspath.Stat(scope); err == nil && !info.IsDir() {
		scope = filepath.Dir(scope)
	}
	boundary := scopeLibraryRoot(scope, libraryRoots)
	root := scope
	entered := make(map[string]bool)
	unreachable := false
	rules, ignored, err := ancestorIgnoreRules(scope, libraryRoots, fspath.ReadDir, false, func(dir string, rules []ignoreRules, entries []os.DirEntry) bool {
		if unreachable || boundary == "" || !pathWithinAnyRoot(dir, []string{boundary}) {
			return false
		}
		canonical, err := canonicalWalkPath(dir)
		if err != nil || entered[canonical] {
			unreachable = true
			return false
		}
		entered[canonical] = true
		if dir == boundary {
			return false
		}
		// The folder below dir on the way to the scope.
		rel, err := filepath.Rel(dir, scope)
		if err != nil {
			return false
		}
		next := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if !isAudiobookDiscDirName(next) && !isAudiobookExtrasDirName(next) {
			return false
		}
		root = dir
		return true
	})
	return root, audiobookScope{rules: rules, ignored: ignored, err: err}
}
