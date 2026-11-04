package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/backup/jsonsize"
	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// ErrRestoreSetTooNew refuses a restore set from a newer format.
var ErrRestoreSetTooNew = errors.New("This backup is from a newer Silo. Upgrade this server first.") //nolint:staticcheck // shown to admins as written

// ErrNoRestoreSet reports a backup without a restore set: one taken before
// selective restore existed.
var ErrNoRestoreSet = errors.New("this backup has no restore set")

// Restore sets are untrusted input: an upload can come from anywhere, and a
// destination can be written by something other than this server. These
// limits bound what reading one can cost.
const (
	maxRestoreSetEntries = 200_000
	maxRestoreSetPath    = 1024
	// maxRestoreSetDocument bounds the documents that grow with a library:
	// collections (item lists and item artwork) and accounts (a profile's
	// watch history).
	maxRestoreSetDocument = 64 << 20
	// maxRestoreSetIndex bounds the index, which lists every file. The
	// other limits bound documents that hold a few kilobytes a server
	// (settings, libraries, page sections) or a plugin's configuration,
	// with a wide margin.
	maxRestoreSetIndex  = 32 << 20
	maxSettingsDocument = 4 << 20
	maxPluginDocument   = 4 << 20
	maxSmallDocument    = 8 << 20
	// maxRestoreSetMetadata bounds the index and documents one read holds
	// in memory together, as the preview cache bounds what it keeps
	// (maxCachedRestoreSetBytes). The index lists every document's size, so
	// a set over it is refused before any document is read.
	maxRestoreSetMetadata  = 256 << 20
	maxRestoreSetArchive   = 256 << 20 // the admin plugin upload cap
	maxRestoreSetBlob      = 256 << 20
	maxRestoreSetTotal     = 32 << 30
	maxRestoreSetJSONDepth = 32
	// A document is refused before decoding when what it decodes to
	// (jsonsize) would be over decodedPerByte times its size plus
	// decodedSlack, or would take a set's decoded documents over
	// maxRestoreSetDecoded: a short document of empty elements decodes to
	// whole structs. A document Silo writes decodes to about its size.
	decodedPerByte       = 8
	decodedSlack         = 64 << 10
	maxRestoreSetDecoded = maxRestoreSetMetadata
	// maxJSONElements bounds one array or object of a document.
	maxJSONElements = 1_000_000
	// maxCollectionItems bounds the item lists of a set's collections
	// together: a large library's collections list a few hundred thousand.
	maxCollectionItems = 1_000_000
	// blobIndexCost is about what one blob costs the set's maps and
	// lookups: its key in blobs, blobSizes, and the index's directories.
	blobIndexCost = 256
	// maxRestoreSetTrailer bounds the bytes after the tar end marker.
	maxRestoreSetTrailer = 1 << 20
)

// RestoreSet is a parsed, validated restore set.
type RestoreSet struct {
	Index RestoreSetIndex
	// Digest is the SHA-256 of the archive's bytes as read.
	Digest      string
	Settings    SettingsDocument
	Libraries   LibrariesDocument
	Plugins     []PluginDocument
	Collections CollectionsDocument
	// HomeSections is the home_sections section (version 3 and later).
	HomeSections HomeSectionsDocument
	// MissingBlobs lists keys a section references that the archive does
	// not carry: they were gone when the backup copied them.
	MissingBlobs []string
	// Unusable lists entries the reader left out because a value in them is
	// one no restore could write; the rest of the set stays readable, and a
	// preview lists them as unreadable.
	Unusable []UnusableEntry

	// blobs maps each blob key in the archive to its extracted file, or to
	// "" when the read did not extract.
	blobs map[string]string
	// unverified marks a set a restore job read from a backup this server
	// cannot authenticate; a preview's shared set leaves it unset and says
	// so to its planner.
	unverified bool
	// blobSizes is each blob's size in bytes, extracted or not.
	blobSizes map[string]int64
	dir       string
	// metadataBytes estimates what the set holds in memory: its documents
	// and blob keys, for the preview cache's bound.
	metadataBytes int64
	indexOnce     sync.Once
	index         *restoreSetIndex
	// fromUnverified marks a cached set of a backup this server cannot
	// verify; the cache keeps one at a time.
	fromUnverified bool
}

// HasBlob reports whether the archive carries key.
func (rs *RestoreSet) HasBlob(key string) bool {
	_, ok := rs.blobs[key]
	return ok
}

// Cleanup removes extracted files.
func (rs *RestoreSet) Cleanup() {
	if rs != nil && rs.dir != "" {
		_ = os.RemoveAll(rs.dir)
	}
}

// ReadOptions configure ReadRestoreSet.
type ReadOptions struct {
	// ExtractDir, when set, is a directory the read creates its own
	// subdirectory in for plugin archives and blobs. A preview leaves it
	// empty: their bytes are checked and discarded.
	ExtractDir string
	// Extract, when set, picks the blobs an extracting read writes to
	// ExtractDir; the rest are checked and discarded like a preview's.
	Extract func(key string) bool
	// IndexSHA256, when set, is the index's digest from an authenticated
	// manifest: the index must match it before it is decoded.
	IndexSHA256 string
	// Verify, when set, runs once the whole archive has been read and
	// before any document is decoded; its error stops the read. A caller
	// checks the archive against its manifest's digest here, so documents
	// are decoded only from verified bytes.
	Verify func() error
}

// ReadRestoreSet reads and validates a whole restore set:
//
//   - only regular files, at canonical paths, each once;
//   - restore-set.json first, in a format version this server reads;
//   - every other file listed in the index with its size and digest, and
//     every listed file present;
//   - blobs only under their section's prefixes, and only keys the section
//     references or variants stored beside them;
//   - limits on entries, document size, total bytes, and JSON nesting;
//   - documents that match the index counts, with no duplicate ids.
//
// Nothing is applied until the whole archive has been read.
func ReadRestoreSet(ctx context.Context, r io.Reader, opts ReadOptions) (_ *RestoreSet, err error) {
	digest := sha256.New()
	counted := &countingReader{r: io.TeeReader(r, digest)}
	guard := newTarGuard(counted)
	tr := tar.NewReader(guard)
	rs := &RestoreSet{blobs: map[string]string{}, blobSizes: map[string]int64{}}
	if opts.ExtractDir != "" {
		if rs.dir, err = os.MkdirTemp(opts.ExtractDir, "restore-set-"); err != nil {
			return nil, err
		}
		defer func() {
			if err != nil {
				rs.Cleanup()
			}
		}()
	}

	p := &restoreSetParser{rs: rs, opts: opts, seen: map[string]bool{}, documents: map[string][]byte{}}
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("restore set: %w", err)
		}
		if err := guard.checkEntry(h.Size); err != nil {
			return nil, fmt.Errorf("restore set: %w", err)
		}
		if entries >= maxRestoreSetEntries {
			return nil, fmt.Errorf("restore set: more than %d entries", maxRestoreSetEntries)
		}
		if err := p.entry(h, tr, entries == 0); err != nil {
			return nil, fmt.Errorf("restore set: %w", err)
		}
		if counted.n > maxRestoreSetTotal {
			return nil, fmt.Errorf("restore set: larger than %d bytes", int64(maxRestoreSetTotal))
		}
	}
	if p.index == nil {
		return nil, errors.New("restore set: empty archive")
	}
	// Hash what follows the end marker too, so the digest covers the stored
	// bytes exactly; a tar never needs more than padding there.
	if _, err := io.Copy(io.Discard, io.LimitReader(counted, maxRestoreSetTrailer)); err != nil {
		return nil, fmt.Errorf("restore set: %w", err)
	}
	if n, _ := io.Copy(io.Discard, io.LimitReader(counted, 1)); n > 0 {
		return nil, errors.New("restore set: unexpected data after the archive")
	}
	if opts.Verify != nil {
		if err := opts.Verify(); err != nil {
			return nil, err
		}
	}
	if err := p.finish(); err != nil {
		return nil, fmt.Errorf("restore set: %w", err)
	}
	rs.Digest = hex.EncodeToString(digest.Sum(nil))
	return rs, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// restoreSetParser holds what the read has found so far.
type restoreSetParser struct {
	rs    *RestoreSet
	index *RestoreSetIndex
	// files are the index's files by path; blobs its referenced keys by key,
	// with the section that references them.
	files map[string]FileDigest
	blobs map[string]sectionSpec
	// originals are the referenced artwork originals by key, and
	// legacyOriginals those without a revision by directory; the variants
	// stored beside one (ladderSibling) may ride along with it, at most
	// maxLadderVariants of them, counted in variants.
	originals       map[string]bool
	legacyOriginals map[string][]string
	variants        map[string]int
	opts            ReadOptions
	seen            map[string]bool
	documents       map[string][]byte
	// decoded is what the documents decoded so far take (jsonsize).
	decoded int64
	// unusableCount counts each section's entries left out (unusable).
	unusableCount map[string]int
	// logical sums the entries' sizes as their headers state them.
	logical int64
}

func (p *restoreSetParser) entry(h *tar.Header, tr io.Reader, first bool) error {
	if h.Typeflag != tar.TypeReg {
		return fmt.Errorf("%q is not a regular file", h.Name)
	}
	// A PAX sparse file reads as a regular one whose size is its logical
	// size, with holes the tar reader fills from nothing: a tiny archive
	// could expand to any size. The writer never makes one.
	for key := range h.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse.") {
			return fmt.Errorf("%q is a sparse file", h.Name)
		}
	}
	// What the entries say they hold is bounded as the archive's bytes are.
	if p.logical += h.Size; p.logical > maxRestoreSetTotal {
		return fmt.Errorf("restore set: its files hold more than %d bytes", int64(maxRestoreSetTotal))
	}
	if !canonicalArchivePath(h.Name) {
		return fmt.Errorf("%q is not a canonical path", h.Name)
	}
	if p.seen[h.Name] {
		return fmt.Errorf("%q appears twice", h.Name)
	}
	p.seen[h.Name] = true
	if h.Size < 0 {
		return fmt.Errorf("%q has a negative size", h.Name)
	}
	if first != (h.Name == restoreSetIndexName) {
		return fmt.Errorf("%s must be the first entry", restoreSetIndexName)
	}
	if first {
		return p.indexEntry(h, tr)
	}
	if key, ok := strings.CutPrefix(h.Name, restoreSetBlobDir); ok {
		return p.blob(key, h, tr)
	}
	want, listed := p.files[h.Name]
	if !listed {
		return fmt.Errorf("%q is not listed in the index", h.Name)
	}
	if h.Size != want.Size {
		return fmt.Errorf("%q is %d bytes; the index says %d", h.Name, h.Size, want.Size)
	}
	if path.Base(h.Name) == pluginArchiveName {
		return p.archive(h, tr, want)
	}
	data, err := readLimited(tr, h, documentLimit(h.Name))
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want.SHA256 {
		return fmt.Errorf("%q does not match its digest", h.Name)
	}
	p.documents[h.Name] = data
	p.rs.metadataBytes += int64(len(data))
	return nil
}

// indexEntry reads the index, checking it against the manifest's digest
// (ReadOptions.IndexSHA256) before decoding it.
func (p *restoreSetParser) indexEntry(h *tar.Header, tr io.Reader) error {
	data, err := readLimited(tr, h, maxRestoreSetIndex)
	if err != nil {
		return err
	}
	if want := p.opts.IndexSHA256; want != "" {
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
			return fmt.Errorf("%s does not match the digest its manifest records", restoreSetIndexName)
		}
	}
	p.rs.metadataBytes += int64(len(data))
	return p.readIndex(data)
}

// documentLimit is the most a section document named name may hold.
func documentLimit(name string) int64 {
	switch {
	case name == settingsDocumentName:
		return maxSettingsDocument
	case path.Base(name) == pluginDocumentName:
		return maxPluginDocument
	case name == librariesDocumentName || name == homeSectionsDocumentName:
		return maxSmallDocument
	}
	return maxRestoreSetDocument
}

func readLimited(r io.Reader, h *tar.Header, limit int64) ([]byte, error) {
	if h.Size > limit {
		return nil, fmt.Errorf("%q is larger than %d bytes", h.Name, limit)
	}
	data, err := io.ReadAll(io.LimitReader(r, h.Size))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != h.Size {
		return nil, fmt.Errorf("%q is truncated", h.Name)
	}
	return data, nil
}

func (p *restoreSetParser) readIndex(data []byte) error {
	// The format and version are read first and alone, so a newer format is
	// refused with its own message, whatever else changed in it.
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if _, err := jsonsize.Check(data, &head, decodeLimits(len(data))); err != nil {
		return fmt.Errorf("%s: %w", restoreSetIndexName, err)
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s: %w", restoreSetIndexName, err)
	}
	if head.Format != restoreSetFormat {
		return fmt.Errorf("%s: format %q is not a Silo restore set", restoreSetIndexName, head.Format)
	}
	if head.Version > restoreSetVersion {
		return ErrRestoreSetTooNew
	}
	if head.Version < 1 {
		return fmt.Errorf("%s: version %d is not valid", restoreSetIndexName, head.Version)
	}
	var index RestoreSetIndex
	if err := p.decode(data, &index); err != nil {
		return fmt.Errorf("%s: %w", restoreSetIndexName, err)
	}
	if !validServerID(index.ServerID) {
		return fmt.Errorf("%s: server id %q is not valid", restoreSetIndexName, index.ServerID)
	}
	p.index = &index
	p.rs.Index = index
	p.files = map[string]FileDigest{}
	p.blobs = map[string]sectionSpec{}
	p.originals = map[string]bool{}
	p.legacyOriginals = map[string][]string{}
	p.variants = map[string]int{}
	sectionsSeen := map[string]bool{}
	// documents sums the listed documents' sizes.
	var documents int64
	for _, si := range index.Sections {
		spec, ok := sectionByID(si.ID)
		if !ok {
			return fmt.Errorf("%s: section %q is not known", restoreSetIndexName, si.ID)
		}
		if index.Version < spec.minVersion {
			return fmt.Errorf("%s: section %q is not part of format version %d", restoreSetIndexName, si.ID, index.Version)
		}
		if sectionsSeen[si.ID] {
			return fmt.Errorf("%s: section %q is listed twice", restoreSetIndexName, si.ID)
		}
		sectionsSeen[si.ID] = true
		if si.Count < 0 {
			return fmt.Errorf("%s: section %q has a negative count", restoreSetIndexName, si.ID)
		}
		for _, f := range si.Files {
			if !canonicalArchivePath(f.Path) || strings.HasPrefix(f.Path, restoreSetBlobDir) || f.Path == restoreSetIndexName {
				return fmt.Errorf("%s: %q is not a section file", restoreSetIndexName, f.Path)
			}
			if _, owned := spec.ownsFile(f.Path); !owned {
				return fmt.Errorf("%s: %q is not a file of section %q", restoreSetIndexName, f.Path, si.ID)
			}
			if _, dup := p.files[f.Path]; dup {
				return fmt.Errorf("%s: %q is listed twice", restoreSetIndexName, f.Path)
			}
			if f.Size < 0 || !sha256Hex.MatchString(f.SHA256) {
				return fmt.Errorf("%s: %q has no valid size and digest", restoreSetIndexName, f.Path)
			}
			if path.Base(f.Path) != pluginArchiveName {
				if f.Size > documentLimit(f.Path) {
					return fmt.Errorf("%s: %q is larger than %d bytes", restoreSetIndexName, f.Path, documentLimit(f.Path))
				}
				if documents += f.Size; documents+int64(len(data)) > maxRestoreSetMetadata {
					return fmt.Errorf("%s: its documents hold more than the %d bytes a restore set may", restoreSetIndexName, int64(maxRestoreSetMetadata))
				}
			}
			p.files[f.Path] = f
		}
		for _, key := range si.Blobs {
			if _, ok := spec.blobStore(key); !ok {
				return fmt.Errorf("%s: blob %q is not one section %q may carry", restoreSetIndexName, key, si.ID)
			}
			if _, dup := p.blobs[key]; dup {
				return fmt.Errorf("%s: blob %q is listed twice", restoreSetIndexName, key)
			}
			p.blobs[key] = spec
			p.noteOriginal(key)
		}
	}
	return nil
}

// noteOriginal records a referenced key that is an artwork original, whose
// variants may ride along with it.
func (p *restoreSetParser) noteOriginal(key string) {
	dir := artworkkey.Directory(key)
	if !strings.HasPrefix(path.Base(key), artworkkey.OriginalVariant+".") || dir == "" {
		return
	}
	p.originals[key] = true
	if artworkkey.Revision(key) == "" && legacyOriginalName(key) {
		p.legacyOriginals[dir] = append(p.legacyOriginals[dir], key)
	}
}

// variant finds the referenced original key is a variant of
// (ladderSibling), and counts it against maxLadderVariants.
func (p *restoreSetParser) variant(key string) (sectionSpec, bool, error) {
	// A variant of a revision names its original (artworkkey.OriginalOf);
	// one without a revision may belong to any legacy original beside it,
	// of which a directory holds one per image format.
	var candidates []string
	if legacyVariantName(key) {
		candidates = p.legacyOriginals[artworkkey.Directory(key)]
	}
	if artworkkey.Revision(key) != "" {
		candidates = nil
		if original := artworkkey.OriginalOf(key); p.originals[original] {
			candidates = []string{original}
		}
	}
	for _, original := range candidates {
		if !ladderSibling(original, key) {
			continue
		}
		if p.variants[original]++; p.variants[original] > maxLadderVariants {
			return sectionSpec{}, false, fmt.Errorf("blob %q: more than %d variants of %q", key, maxLadderVariants, original)
		}
		return p.blobs[original], true, nil
	}
	return sectionSpec{}, false, nil
}

// blob accepts a referenced key, or a variant stored beside a referenced
// original (ladderSibling), under its section's prefixes.
func (p *restoreSetParser) blob(key string, h *tar.Header, tr io.Reader) error {
	spec, ok := p.blobs[key]
	if !ok {
		var err error
		if spec, ok, err = p.variant(key); err != nil {
			return err
		}
	}
	if !ok {
		return fmt.Errorf("blob %q is not referenced by any section", key)
	}
	if _, allowed := spec.blobStore(key); !allowed {
		return fmt.Errorf("blob %q is outside section %q", key, spec.id)
	}
	if h.Size > maxRestoreSetBlob {
		return fmt.Errorf("blob %q is larger than %d bytes", key, maxRestoreSetBlob)
	}
	dir := p.rs.dir
	if p.opts.Extract != nil && !p.opts.Extract(key) {
		dir = ""
	}
	target, err := p.copyEntry(dir, "blob", h, tr, "")
	if err != nil {
		return err
	}
	p.rs.blobs[key] = target
	p.rs.blobSizes[key] = h.Size
	p.rs.metadataBytes += int64(2*len(key)+len(target)) + 8
	return nil
}

func (p *restoreSetParser) archive(h *tar.Header, tr io.Reader, want FileDigest) error {
	if h.Size > maxRestoreSetArchive {
		return fmt.Errorf("%q is larger than %d bytes", h.Name, maxRestoreSetArchive)
	}
	// A plugin's archive is checked against its digest and never kept: a
	// restore never installs or runs a plugin from a backup (see
	// plugins.ErrPluginNotInstalled), so nothing reads it.
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(tr, h.Size))
	switch {
	case err != nil:
		return fmt.Errorf("reading %q: %w", h.Name, err)
	case n != h.Size:
		return fmt.Errorf("%q is truncated", h.Name)
	case hex.EncodeToString(sum.Sum(nil)) != want.SHA256:
		return fmt.Errorf("%q does not match its digest", h.Name)
	}
	return nil
}

// copyEntry copies an entry to a new file in dir, or discards it when dir
// is "", checking its digest when one is expected.
func (p *restoreSetParser) copyEntry(dir, kind string, h *tar.Header, tr io.Reader, wantSHA string) (string, error) {
	sum := sha256.New()
	var w io.Writer = sum
	var file *os.File
	if dir != "" {
		var err error
		if file, err = os.CreateTemp(dir, kind+"-"); err != nil {
			return "", err
		}
		defer func() { _ = file.Close() }()
		w = io.MultiWriter(file, sum)
	}
	n, err := io.Copy(w, io.LimitReader(tr, h.Size))
	if err != nil {
		return "", fmt.Errorf("reading %q: %w", h.Name, err)
	}
	if n != h.Size {
		return "", fmt.Errorf("%q is truncated", h.Name)
	}
	if wantSHA != "" && hex.EncodeToString(sum.Sum(nil)) != wantSHA {
		return "", fmt.Errorf("%q does not match its digest", h.Name)
	}
	if file == nil {
		return "", nil
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return file.Name(), nil
}

// finish requires every listed file, decodes the documents, and checks them
// against the index.
func (p *restoreSetParser) finish() error {
	for name := range p.files {
		if !p.seen[name] {
			return fmt.Errorf("%q is listed in the index but missing", name)
		}
	}
	for key := range p.blobs {
		if _, ok := p.rs.blobs[key]; !ok {
			p.rs.MissingBlobs = append(p.rs.MissingBlobs, key)
		}
	}
	sort.Strings(p.rs.MissingBlobs)
	defer func() {
		// What the set holds once read: its documents decoded (jsonsize's
		// estimate) and its lookup index, built here so its cost is known
		// before the set is cached, besides the bytes counted as read.
		p.rs.lookup()
		p.rs.metadataBytes += p.decoded + int64(len(p.rs.blobs))*blobIndexCost
	}()
	for _, si := range p.index.Sections {
		count, err := p.decodeSection(si)
		if err != nil {
			return fmt.Errorf("section %q: %w", si.ID, err)
		}
		p.summarizeUnusable(si.ID)
		if count != si.Count {
			return fmt.Errorf("section %q holds %d items; the index says %d", si.ID, count, si.Count)
		}
	}
	return nil
}

// canonicalArchivePath accepts a relative slash path with no empty, ".", or
// ".." segment, no backslash or NUL, valid UTF-8, bounded length.
func canonicalArchivePath(name string) bool {
	if name == "" || len(name) > maxRestoreSetPath || !utf8.ValidString(name) {
		return false
	}
	if strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return path.Clean(name) == name
}

// allowedRestoreSetKey is a valid store key that is never a backup's own
// object.
func allowedRestoreSetKey(key string) bool {
	return canonicalArchivePath(key) && blobstore.ValidateKey(key) == nil && !strings.HasPrefix(key, RootPrefix)
}

// decodeLimits bound decoding a document of n bytes on its own.
func decodeLimits(n int) jsonsize.Limits {
	return jsonsize.Limits{Depth: maxRestoreSetJSONDepth, Elements: maxJSONElements, Bytes: decodedPerByte*int64(n) + decodedSlack}
}

// decode decodes one of the set's documents strictly (decodeStrict), first
// checking what it decodes to against its own bound and what the set's
// documents decoded so far leave of maxRestoreSetDecoded.
func (p *restoreSetParser) decode(data []byte, into any) error {
	limits := decodeLimits(len(data))
	limits.Bytes = min(limits.Bytes, maxRestoreSetDecoded-p.decoded)
	size, err := jsonsize.Check(data, into, limits)
	if err != nil {
		return err
	}
	p.decoded += size
	return decodeStrict(data, into)
}

// decodeStrict decodes one JSON value, refusing unknown fields and trailing
// data: within a format version every field is known. Data from an
// untrusted source is checked with jsonsize first (restoreSetParser.decode).
func decodeStrict(data []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the document")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the document")
	}
	return nil
}

// openBlob opens an extracted blob.
func (rs *RestoreSet) openBlob(key string) (*os.File, int64, error) {
	file, ok := rs.blobs[key]
	if !ok || file == "" {
		return nil, 0, ErrNotFound
	}
	f, err := os.Open(file) //nolint:gosec // G304: a file this read created
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// blobFamily is key and the variants the archive carries beside it, in a
// stable order: an artwork original with its stored ladder.
func (rs *RestoreSet) blobFamily(key string) []string {
	if !rs.HasBlob(key) {
		return nil
	}
	out := []string{key}
	if !strings.HasPrefix(path.Base(key), artworkkey.OriginalVariant+".") {
		return out
	}
	dir := artworkkey.Directory(key)
	if dir == "" {
		return out
	}
	return append(out, rs.lookup().variants[key]...)
}
