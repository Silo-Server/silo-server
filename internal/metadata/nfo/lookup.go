package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// Directory-level NFO names, matched case-insensitively.
const (
	movieNFOName  = "movie.nfo"
	tvshowNFOName = "tvshow.nfo"
)

// maxNFOBytes caps an NFO read. Real NFO files are a few KiB; the cap keeps
// a large file that happens to end in .nfo from being read into memory.
const maxNFOBytes = 4 << 20

// maxCacheEntries bounds each of the provider's caches. The provider lives
// as long as its cached provider chain, so the caches only need to cover what
// one metadata pass touches several times.
const maxCacheEntries = 256

// stackPartRe matches a stacked part with the part tokens the scanner groups
// into one multi-part movie (naming's presentationPartRe: cd, disc, part, pt
// and a one- or two-digit number), at the start of the name or after a
// separator. Group 1 is the title, group 2 the part number, and group 3 the
// text after the token, which Kodi keeps in the stack name.
var stackPartRe = regexp.MustCompile(`(?i)^(.*?)(?:^|[ _.-]+)(?:cd|disc|part|pt)[ _.-]*(\d{1,2})([ _.-].*)?$`) //nolint:gocritic // the inner ^ matches a part token at the start of the name

// stackPart splits a stacked part's base name into the name its parts share
// (Movie-cd1 → Movie, CD1.Feature → Feature) and its part number, or returns
// "" when base is not a stack part.
func stackPart(base string) (name string, part int) {
	match := stackPartRe.FindStringSubmatch(base)
	if match == nil {
		return "", 0
	}
	name = match[1] + match[3]
	if strings.TrimSpace(match[1]) == "" {
		name = strings.TrimLeft(match[3], " _.-") // a leading token
	}
	if strings.TrimSpace(name) == "" {
		return "", 0
	}
	part, _ = strconv.Atoi(match[2])
	if part <= 0 {
		return "", 0 // the scanner numbers parts from 1
	}
	return name, part
}

// stackGroup normalizes a stack name the way the scanner groups parts:
// dots, underscores, hyphens, and runs of whitespace are one separator, and
// case is ignored.
func stackGroup(name string) string {
	fields := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || unicode.IsSpace(r)
	})
	return strings.Join(fields, " ")
}

// stackBaseName returns the name a stacked part shares with its siblings, or
// "" when base is not a stack part.
func stackBaseName(base string) string {
	name, _ := stackPart(base)
	return name
}

// boundedCache is a small mutex-guarded map that starts over when full. Keys
// change with what they describe (a file's content, a directory's
// modification time), so a change is a miss.
type boundedCache[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]V
}

func (c *boundedCache[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.entries[key]
	return value, ok
}

func (c *boundedCache[K, V]) put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= maxCacheEntries {
		c.entries = make(map[K]V)
	}
	c.entries[key] = value
}

// listingSettleTime is how long a directory with coarse timestamps must go
// unchanged before its listing is cached; it exceeds the coarsest common
// timestamp tick.
const listingSettleTime = 3 * time.Second

// fineListingSettleTime covers a kernel that stamps a directory from its
// coarse clock (a tick of up to 10 ms) while reporting nanoseconds.
const fineListingSettleTime = 20 * time.Millisecond

// coarseTimestamp reports whether t is a whole millisecond, as timestamps
// from FAT, HFS+, and second-resolution network shares always are. A
// nanosecond-resolution timestamp (ext4, XFS, Btrfs, ZFS) almost never is.
func coarseTimestamp(t time.Time) bool {
	return t.Nanosecond()%int(time.Millisecond) == 0
}

// dirCacheKey identifies one state of a directory. Its change time moves
// whenever an entry is added or removed, even when a copy tool restores the
// modification time, so a restored time cannot bring back an old listing.
type dirCacheKey struct {
	path       string
	modTime    int64
	changeTime int64
}

// dirIndex maps lower-cased entry names to the names in a directory.
type dirIndex map[string][]string

// dirListing is one directory's listing. A nil listing means the directory
// may be traversed but not listed, so lookups fall back to the exact
// candidate name. Its stack index is built on first use and shared by every
// lookup that reuses the cached listing, so a flat folder of many multi-part
// movies is swept once per change, not once per item and phase.
type dirListing struct {
	dir        string
	index      dirIndex
	stacksOnce sync.Once
	stacks     map[string][]stackEntry // stack group → entries named as its parts
}

type stackEntry struct {
	name string
	part int
}

// stackParts returns the video entries named as parts of a stack group.
func (d *dirListing) stackParts(group string) []stackEntry {
	if d == nil {
		return nil
	}
	d.stacksOnce.Do(func() {
		d.stacks = make(map[string][]stackEntry)
		for _, entries := range d.index {
			for _, entry := range entries {
				if entryGroup, part, ok := videoStackPart(filepath.Join(d.dir, entry)); ok {
					d.stacks[entryGroup] = append(d.stacks[entryGroup], stackEntry{entry, part})
				}
			}
		}
	})
	return d.stacks[group]
}

// videoStackPart returns the stack group and part number of a video file
// named as a stacked part.
func videoStackPart(path string) (group string, part int, ok bool) {
	if !scanner.SupportsVideoFile(path) {
		return "", 0, false
	}
	base := filepath.Base(path)
	name, part := stackPart(strings.TrimSuffix(base, filepath.Ext(base)))
	if name == "" {
		return "", 0, false
	}
	return stackGroup(name), part, true
}

// clone copies the slices a caller could append to, so cached results are
// never shared with a metadata accumulator.
func (p *parsedNFO) clone() *parsedNFO {
	if p == nil {
		return nil
	}
	c := *p
	c.Genres = slices.Clone(p.Genres)
	c.Studios = slices.Clone(p.Studios)
	c.Countries = slices.Clone(p.Countries)
	c.Keywords = slices.Clone(p.Keywords)
	c.Networks = slices.Clone(p.Networks)
	c.People = slices.Clone(p.People)
	c.Videos = slices.Clone(p.Videos)
	c.Art = slices.Clone(p.Art)
	c.RatingSources = maps.Clone(p.RatingSources)
	c.SeasonNames = maps.Clone(p.SeasonNames)
	return &c
}

// sidecarLookup finds and reads NFO files for one provider call. It matches
// names case-insensitively against a directory listing, so Movie.NFO and
// TVSHOW.nfo are found on case-sensitive filesystems. Listings are cached on
// the provider by directory modification time, so a flat folder of thousands
// of movies is listed once per change rather than once per item and phase.
type sidecarLookup struct {
	ctx      context.Context
	provider *Provider
	listings map[string]*dirListing // this call's listings, without re-statting
	stacks   map[stackKey]bool      // this call's isStacked answers
	folders  map[string]error       // this call's folderMedia answers
}

type stackKey struct{ dir, name string }

func (p *Provider) newLookup(ctx context.Context) *sidecarLookup {
	return &sidecarLookup{
		ctx:      ctx,
		provider: p,
		listings: make(map[string]*dirListing),
		stacks:   make(map[stackKey]bool),
		folders:  make(map[string]error),
	}
}

// mediaPresent checks that a media file the catalog lists as present is
// still a file. An unmounted share often leaves an empty mountpoint, and a
// folder that no longer holds its media file says nothing about its
// sidecars, so that is unavailable, not "no NFO". Like the scanner, it
// follows symlinks and counts only a regular file, never a directory.
func mediaPresent(file string) error {
	info, err := os.Stat(file)
	if err != nil {
		return unavailable(file, err)
	}
	if !info.Mode().IsRegular() {
		return unavailable(file, errNotRegularFile)
	}
	return nil
}

var (
	errNotRegularFile = errors.New("not a regular file")
	errNFOReplaced    = errors.New("file replaced or changed while it was read")
)

// anyMediaPresent checks that dir still holds one of the given catalog files
// that lie under it. A series root or season folder comes from the episode
// files the catalog lists there, so a folder holding none of them (an
// unmounted share's mountpoint, perhaps with a .keep, lost+found, or
// unrelated video) is unavailable, not a folder without an NFO. With no
// files known under dir there is nothing to check.
func anyMediaPresent(dir string, files []string) error {
	var err error
	for _, file := range files {
		if !within(dir, file) {
			continue
		}
		if err == nil {
			// A folder that can't be reached answers for every file in it.
			if _, dirErr := os.Stat(dir); dirErr != nil {
				return unavailable(dir, dirErr)
			}
		}
		fileErr := mediaPresent(file)
		if fileErr == nil {
			return nil // one present file proves the folder, whatever came first
		}
		err = keepReadErr(err, fileErr)
	}
	return err
}

// keepReadErr accumulates the read errors of a lookup: the first one wins,
// except that a temporary one (a missing share, an I/O error) outranks a
// permission problem. Matching goes ahead past a permission problem but
// waits for a temporary one, so the order paths come in must not decide
// whether an outage waits or an identity is committed.
func keepReadErr(current, next error) error {
	if current == nil || (next != nil && errors.Is(current, fs.ErrPermission) && !errors.Is(next, fs.ErrPermission)) {
		return next
	}
	return current
}

// folderMedia is anyMediaPresent kept per folder for the call (a call passes
// the same files for every folder), so a run of missing episodes, or seasons
// sharing one folder, check that folder once. A re-check after a lookup
// (confirmMounted) asks the filesystem again.
func (l *sidecarLookup) folderMedia(dir string, files []string) error {
	err, ok := l.folders[dir]
	if !ok {
		err = anyMediaPresent(dir, files)
		l.folders[dir] = err
	}
	return err
}

// deletedFromLiveFolder reports a cataloged file that is gone while another
// of the item's files in its folder is still there: the share is mounted and
// the file was deleted or renamed before the next scan, so it simply has no
// sidecars. It is not a reason to report the item's NFO unreadable.
func (l *sidecarLookup) deletedFromLiveFolder(err error, file string, files []string) bool {
	return isAbsent(err) && l.folderMedia(filepath.Dir(file), files) == nil
}

// within reports whether path lies under dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// find returns the first parseable NFO named name in dir (ignoring case,
// exact spelling first) whose root type is wantType. An empty wantType
// matches any type, and a URL-only file whose links leave its type open
// matches a movie or a show. A missing file is (nil, nil). A sidecar that
// exists but cannot be read, when no other spelling of the name parses, or a
// folder that can't be listed and has nothing at the exact name, returns an
// error wrapping metadata.ErrLocalMetadataUnavailable.
func (l *sidecarLookup) find(dir, name, wantType string) (string, *parsedNFO, error) {
	names, err := l.names(dir, name)
	if err != nil {
		return "", nil, err
	}
	var firstErr error
	for _, entry := range names {
		path := filepath.Join(dir, entry)
		parsed, err := l.load(path)
		if err != nil {
			firstErr = keepReadErr(firstErr, err)
			continue
		}
		if parsed != nil && typeMatches(parsed, wantType) {
			return path, parsed, nil
		}
	}
	if firstErr == nil && l.listings[dir] == nil {
		// A folder that can't be listed was probed only at the exact name, so
		// a differently cased sidecar may still be there: unreadable, not absent.
		firstErr = unavailable(dir, fs.ErrPermission)
	}
	return "", nil, firstErr
}

func typeMatches(parsed *parsedNFO, wantType string) bool {
	switch {
	case wantType == "" || parsed.Type == wantType:
		return true
	case parsed.Type == "":
		return wantType == typeMovie || wantType == typeSeries
	}
	return false
}

func (l *sidecarLookup) listing(dir string) (*dirListing, error) {
	if listing, ok := l.listings[dir]; ok {
		return listing, nil
	}
	listing, err := l.provider.listDir(dir)
	if err != nil {
		return nil, err
	}
	l.listings[dir] = listing
	return listing, nil
}

func (l *sidecarLookup) names(dir, name string) ([]string, error) {
	listing, err := l.listing(dir)
	if err != nil {
		return nil, err
	}
	if listing == nil {
		return []string{name}, nil
	}
	matches := listing.index[strings.ToLower(name)]
	// Several case variants can coexist on a case-sensitive filesystem; the
	// exact spelling goes first.
	if i := slices.Index(matches, name); i > 0 {
		matches = append([]string{name}, slices.Delete(slices.Clone(matches), i, i+1)...)
	}
	return matches, nil
}

// listDir returns dir's listing. Every directory the provider looks in
// holds, or is the root of, a media file the catalog lists as present, so a
// directory that is gone (an unmounted share often leaves an empty
// mountpoint) is unavailable, not empty. A directory the server may traverse
// but not list gives a nil listing, so the exact candidate names are still
// tried.
func (p *Provider) listDir(dir string) (*dirListing, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, unavailable(dir, err)
	}
	if !info.IsDir() {
		return nil, unavailable(dir, syscall.ENOTDIR)
	}
	key := dirCacheKey{path: dir, modTime: info.ModTime().UnixNano(), changeTime: changeTime(info)}
	if listing, ok := p.listings.get(key); ok {
		return listing, nil
	}
	listedAt := time.Now()
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrPermission):
		return nil, nil
	case err != nil:
		return nil, unavailable(dir, err)
	}
	listing := &dirListing{dir: dir, index: make(dirIndex, len(entries))}
	for _, entry := range entries {
		key := strings.ToLower(entry.Name())
		listing.index[key] = append(listing.index[key], entry.Name())
	}
	// A file added later in the timestamp tick this listing was read in
	// leaves the directory's time unchanged, so a listing is cached only once
	// its directory has gone a tick unchanged: seconds on a filesystem with
	// coarse timestamps (FAT's are 2 s), and a kernel clock tick otherwise.
	settle := fineListingSettleTime
	if coarseTimestamp(info.ModTime()) {
		settle = listingSettleTime
	}
	if listedAt.Sub(info.ModTime()) > settle {
		p.listings.put(key, listing)
	}
	return listing, nil
}

// load reads and parses one NFO file. Like sidecar artwork discovery, it
// skips symlinked leaves, non-regular files, and files over maxNFOBytes, and
// it confirms the opened file is the one it checked. A file that does not
// parse is (nil, nil), so the caller falls through to the next candidate.
func (l *sidecarLookup) load(path string) (*parsedNFO, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if isAbsent(err) {
			return nil, nil
		}
		return nil, unavailable(path, err)
	}
	if !info.Mode().IsRegular() {
		if info.Mode()&fs.ModeSymlink != 0 {
			slog.WarnContext(l.ctx, "nfo: ignoring symlinked NFO file", "component", "metadata", "path", path)
		}
		return nil, nil
	}
	if info.Size() > maxNFOBytes {
		slog.WarnContext(l.ctx, "nfo: ignoring oversized NFO file", "component", "metadata",
			"path", path, "size_bytes", info.Size(), "limit_bytes", maxNFOBytes)
		return nil, nil
	}
	data, err := readNFOFile(path)
	if err != nil {
		if isAbsent(err) {
			return nil, nil
		}
		return nil, unavailable(path, err)
	}
	if data == nil {
		return nil, nil
	}
	// Keyed on content, not size and modification time: a same-size rewrite
	// within a coarse filesystem's timestamp tick must not reuse a stale parse.
	key := sha256.Sum256(data)
	if parsed, ok := l.provider.parses.get(key); ok {
		return parsed.clone(), nil
	}
	parsed, err := parseNFOData(data)
	if err != nil {
		slog.DebugContext(l.ctx, "nfo: ignoring unparseable NFO file", "component", "metadata", "path", path, "error", err)
		parsed = nil
	}
	l.provider.parses.put(key, parsed)
	return parsed.clone(), nil
}

// readNFOFile reads at most maxNFOBytes from path. It opens without
// following a symlink and without blocking, so neither a symlink nor a FIFO
// swapped in after Lstat is read or can stall the worker; either is an
// error, not a missing NFO, so the caller reports it unavailable rather than
// letting remote values replace it. A regular file that replaced the one
// Lstat saw (an editor's atomic save) is read; so is one saved after the
// open, whose earlier version is still read whole, and the next pass sees
// the save.
func readNFOFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, errNFOReplaced
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() {
		return nil, errNFOReplaced
	}
	// An editor rewriting the file in place while it is read can leave a
	// partial document, so the read is checked against the file afterwards
	// and tried once more before the file is reported unavailable.
	for attempt := 1; ; attempt++ {
		data, err := readFromStart(f)
		if err != nil {
			return nil, err
		}
		after, err := f.Stat()
		if err != nil {
			return nil, err
		}
		stable := after.Size() == opened.Size() && after.ModTime().Equal(opened.ModTime())
		if stable && coarseTimestamp(after.ModTime()) && time.Since(after.ModTime()) <= listingSettleTime {
			// A same-size rewrite within a coarse timestamp tick leaves both
			// unchanged, so a recently written file is read twice.
			again, err := readFromStart(f)
			if err != nil {
				return nil, err
			}
			if after, err = f.Stat(); err != nil {
				return nil, err
			}
			stable = bytes.Equal(data, again) &&
				after.Size() == opened.Size() && after.ModTime().Equal(opened.ModTime())
		}
		if stable {
			if len(data) > maxNFOBytes {
				return nil, nil
			}
			return data, nil
		}
		if attempt == 2 {
			return nil, errNFOReplaced
		}
		opened = after
	}
}

// readFromStart reads at most maxNFOBytes+1 bytes of f from its start.
func readFromStart(f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if testHookBeforeNFORead != nil {
		testHookBeforeNFORead()
	}
	return io.ReadAll(io.LimitReader(f, maxNFOBytes+1))
}

// testHookBeforeNFORead lets a test change a file between its check and its
// read. It is nil outside tests.
var testHookBeforeNFORead func()

func isAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func unavailable(path string, err error) error {
	return fmt.Errorf("%w: %s: %w", metadata.ErrLocalMetadataUnavailable, path, err)
}

// directoryNFONames lists the directory-level NFO names for a content type.
func directoryNFONames(contentType string) []string {
	switch contentType {
	case typeMovie:
		return []string{movieNFOName}
	case typeSeries:
		return []string{tvshowNFOName}
	}
	return []string{movieNFOName, tvshowNFOName}
}

// findItemNFO returns the movie or show NFO in Kodi's order: <basename>.nfo
// beside each media file (then, for a stacked movie, the name its parts
// share), then movie.nfo or tvshow.nfo in the sidecar directories. Upstream
// code passes only single-content directories as sidecarDirs
// (canUseObservedRootForDirectorySidecars), so a stray movie.nfo in a flat
// multi-movie folder applies to none of its movies. A show's NFO is only ever
// tvshow.nfo, so its episode files are not probed.
//
// A candidate that cannot be read does not stop the search: a version in a
// readable folder can still supply the NFO. When none does, the first read
// error is returned rather than "no NFO".
func (l *sidecarLookup) findItemNFO(mediaFiles, sidecarDirs []string, contentType string) (string, *parsedNFO, error) {
	type candidate struct{ dir, name string }
	var candidates []candidate
	seen := make(map[candidate]struct{})
	add := func(dir, name string) {
		key := candidate{dir, strings.ToLower(name)}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate{dir, name})
	}
	var firstErr error
	if contentType != typeSeries {
		var stacks []candidate
		for _, file := range mediaFiles {
			if err := mediaPresent(file); err != nil {
				if !l.deletedFromLiveFolder(err, file, mediaFiles) {
					firstErr = keepReadErr(firstErr, err)
				}
				continue
			}
			dir := filepath.Dir(file)
			base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
			add(dir, base+".nfo")
			if stack := stackBaseName(base); stack != "" {
				stacks = append(stacks, candidate{dir, stack})
			}
		}
		// A stacked movie's shared name comes after every file's own name.
		for _, stack := range stacks {
			if l.isStacked(stack.dir, stack.name, mediaFiles) {
				add(stack.dir, stack.name+".nfo")
			}
		}
	}
	// A sidecar directory that has lost the item's cataloged files (for a
	// show, one episode stands in for its folders) is not read, so a stale
	// movie.nfo or tvshow.nfo left on an unmounted share's mountpoint is never
	// applied. Media the server may not reach for permission is still there.
	for _, dir := range sidecarDirs {
		if err := l.folderMedia(dir, mediaFiles); err != nil {
			firstErr = keepReadErr(firstErr, err)
			if !errors.Is(err, fs.ErrPermission) {
				continue
			}
		}
		for _, name := range directoryNFONames(contentType) {
			add(dir, name)
		}
	}
	for _, c := range candidates {
		path, parsed, err := l.find(c.dir, c.name, contentType)
		if parsed != nil {
			if err := confirmMounted(c.dir, mediaFiles); err != nil {
				return "", nil, err
			}
			return path, parsed, nil
		}
		firstErr = keepReadErr(firstErr, err)
	}
	if firstErr == nil {
		confirmed := make(map[string]bool)
		for _, c := range candidates {
			if confirmed[c.dir] {
				continue
			}
			confirmed[c.dir] = true
			if err := confirmMounted(c.dir, mediaFiles); err != nil {
				return "", nil, err
			}
		}
	}
	return "", nil, firstErr
}

// confirmMounted re-checks, after a lookup in dir, that dir still holds one
// of the item's media files. A share that dropped while the lookup ran
// exposes the mountpoint underneath, whose sidecars (or lack of them) say
// nothing about the item. A permission problem means the share is still
// there, so it does not count.
func confirmMounted(dir string, files []string) error {
	if err := anyMediaPresent(dir, files); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return nil
}

// isStacked reports whether dir holds two or more distinct video parts that
// share the stack name, as the scanner requires before it groups a
// multi-part movie. Only files the scanner treats as video count, so a
// subtitle or NFO named like a part never does, and parts may differ in
// container (Movie-cd1.mkv, Movie-cd2.mp4) as the scanner allows. It counts
// the folder's entries as well as the request's files, because an initial
// match carries only the representative part. A lone "Movie Part 2" (or two
// copies of it) never reads "Movie.nfo". Names are compared as the scanner
// groups them, so Film-part1-1080p and Film part2 1080p are one stack.
func (l *sidecarLookup) isStacked(dir, stack string, mediaFiles []string) bool {
	group := stackGroup(stack)
	key := stackKey{dir, group}
	if stacked, ok := l.stacks[key]; ok {
		return stacked
	}
	listing, err := l.listing(dir)
	if err != nil {
		return false // the lookup itself reports the folder's error
	}
	parts := make(map[int]struct{})
	// The scanner follows symlinks and skips directories, so only a part
	// that is still a regular file counts: a folder named like a part, or a
	// cataloged part that has gone, does not.
	count := func(path string, part int) {
		if mediaPresent(path) == nil {
			parts[part] = struct{}{}
		}
	}
	for _, file := range mediaFiles {
		if filepath.Dir(file) != dir {
			continue
		}
		if fileGroup, part, ok := videoStackPart(file); ok && fileGroup == group {
			count(file, part)
		}
	}
	for _, entry := range listing.stackParts(group) {
		count(filepath.Join(dir, entry.name), entry.part)
	}
	l.stacks[key] = len(parts) > 1
	return l.stacks[key]
}
