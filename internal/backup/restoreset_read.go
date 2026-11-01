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
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/branding"
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
	maxRestoreSetEntries   = 200_000
	maxRestoreSetPath      = 1024
	maxRestoreSetDocument  = 64 << 20
	maxRestoreSetArchive   = 256 << 20 // the admin plugin upload cap
	maxRestoreSetBlob      = 256 << 20
	maxRestoreSetTotal     = 32 << 30
	maxRestoreSetJSONDepth = 32
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
	Users       []UserDocument
	Collections CollectionsDocument
	// HomeSections is the home_sections section (version 3 and later).
	HomeSections HomeSectionsDocument
	// MissingBlobs lists keys a section references that the archive does
	// not carry: they were gone when the backup copied them.
	MissingBlobs []string

	// blobs maps each blob key in the archive to its extracted file, or to
	// "" when the read did not extract.
	blobs map[string]string
	// archives maps a plugin id to its extracted archive file.
	archives map[string]string
	dir      string
	// metadataBytes estimates what the set holds in memory: its documents
	// and blob keys, for the preview cache's bound.
	metadataBytes int64
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
	tr := tar.NewReader(counted)
	rs := &RestoreSet{blobs: map[string]string{}, archives: map[string]string{}}
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

	p := &restoreSetParser{rs: rs, seen: map[string]bool{}, documents: map[string][]byte{}}
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
	// ladderDirs are the image directories of referenced artwork originals;
	// the variants stored beside an original may ride along with it.
	ladderDirs map[string]sectionSpec
	seen       map[string]bool
	documents  map[string][]byte
}

func (p *restoreSetParser) entry(h *tar.Header, tr io.Reader, first bool) error {
	if h.Typeflag != tar.TypeReg {
		return fmt.Errorf("%q is not a regular file", h.Name)
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
		data, err := readLimited(tr, h, maxRestoreSetDocument)
		if err != nil {
			return err
		}
		return p.readIndex(data)
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
	data, err := readLimited(tr, h, maxRestoreSetDocument)
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
	if err := checkJSONDepth(data); err != nil {
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
	if err := decodeStrict(data, &index); err != nil {
		return fmt.Errorf("%s: %w", restoreSetIndexName, err)
	}
	if !validServerID(index.ServerID) {
		return fmt.Errorf("%s: server id %q is not valid", restoreSetIndexName, index.ServerID)
	}
	if err := validateOmitted(index.Omitted); err != nil {
		return fmt.Errorf("%s: %w", restoreSetIndexName, err)
	}
	p.index = &index
	p.rs.Index = index
	p.files = map[string]FileDigest{}
	p.blobs = map[string]sectionSpec{}
	p.ladderDirs = map[string]sectionSpec{}
	sectionsSeen := map[string]bool{}
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
			if strings.HasPrefix(path.Base(key), artworkkey.OriginalVariant+".") {
				if dir := artworkkey.Directory(key); dir != "" {
					p.ladderDirs[dir] = spec
				}
			}
		}
	}
	return nil
}

func validateOmitted(list []OmittedItem) error {
	if len(list) > maxRestoreSetEntries {
		return errors.New("too many omitted items")
	}
	for _, o := range list {
		if !validSectionID(o.Section) || len(o.ID) > 128 || len(o.Label) > maxAccountField || len(o.Reason) > 64 || len(o.Message) > maxOmittedText ||
			len(o.Cause) > 256 || len(o.Error) > maxOmittedText {
			return errors.New("an omitted item is not valid")
		}
	}
	return nil
}

// blob accepts a referenced key, or a variant stored in a referenced
// original's directory, under its section's prefixes.
func (p *restoreSetParser) blob(key string, h *tar.Header, tr io.Reader) error {
	spec, ok := p.blobs[key]
	if !ok {
		if dir := artworkkey.Directory(key); dir != "" && path.Dir(key)+"/" == dir {
			spec, ok = p.ladderDirs[dir]
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
	target, err := p.extract("blob", h, tr, "")
	if err != nil {
		return err
	}
	p.rs.blobs[key] = target
	p.rs.metadataBytes += int64(len(key) + len(target))
	return nil
}

func (p *restoreSetParser) archive(h *tar.Header, tr io.Reader, want FileDigest) error {
	if h.Size > maxRestoreSetArchive {
		return fmt.Errorf("%q is larger than %d bytes", h.Name, maxRestoreSetArchive)
	}
	target, err := p.extract("archive", h, tr, want.SHA256)
	if err != nil {
		return err
	}
	pluginID := path.Base(path.Dir(h.Name))
	p.rs.archives[pluginID] = target
	return nil
}

// extract copies an entry to a new file in the extraction directory, or
// discards it, checking its digest when one is expected.
func (p *restoreSetParser) extract(kind string, h *tar.Header, tr io.Reader, wantSHA string) (string, error) {
	sum := sha256.New()
	var w io.Writer = sum
	var file *os.File
	if p.rs.dir != "" {
		var err error
		if file, err = os.CreateTemp(p.rs.dir, kind+"-"); err != nil {
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
	for _, si := range p.index.Sections {
		count, err := p.decodeSection(si)
		if err != nil {
			return fmt.Errorf("section %q: %w", si.ID, err)
		}
		if count != si.Count {
			return fmt.Errorf("section %q holds %d items; the index says %d", si.ID, count, si.Count)
		}
	}
	return nil
}

func (p *restoreSetParser) document(name string, into any) (bool, error) {
	data, ok := p.documents[name]
	if !ok {
		return false, nil
	}
	if err := checkJSONDepth(data); err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	if err := decodeStrict(data, into); err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return true, nil
}

func (p *restoreSetParser) decodeSection(si SectionIndex) (int, error) {
	spec, _ := sectionByID(si.ID)
	switch si.ID {
	case SectionSettings:
		if ok, err := p.document(spec.document, &p.rs.Settings); err != nil || !ok {
			return 0, requireDocument(spec, ok, err)
		}
		return len(p.rs.Settings.Settings), validateSettingsDocument(p.rs.Settings)
	case SectionLibraries:
		if ok, err := p.document(spec.document, &p.rs.Libraries); err != nil || !ok {
			return 0, requireDocument(spec, ok, err)
		}
		return len(p.rs.Libraries.Libraries), p.validateLibraries()
	case SectionCollections:
		if ok, err := p.document(spec.document, &p.rs.Collections); err != nil || !ok {
			return 0, requireDocument(spec, ok, err)
		}
		d := p.rs.Collections
		return len(d.Collections) + len(d.TemplateCollections) + len(d.ItemArtwork) + len(d.Branding), p.validateCollections()
	case SectionHomeSections:
		if ok, err := p.document(spec.document, &p.rs.HomeSections); err != nil || !ok {
			return 0, requireDocument(spec, ok, err)
		}
		return len(p.rs.HomeSections.Sections), validateHomeSections(p.rs.HomeSections)
	case SectionPlugins:
		return p.decodePlugins(spec)
	case SectionUsers:
		return p.decodeUsers(spec)
	}
	return 0, fmt.Errorf("no reader for section %q", si.ID)
}

func requireDocument(spec sectionSpec, ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s is missing", spec.document)
	}
	return nil
}

func validateSettingsDocument(d SettingsDocument) error {
	seen := map[string]bool{}
	for _, s := range d.Settings {
		if s.Key == "" || len(s.Key) > 256 || !utf8.ValidString(s.Key) {
			return fmt.Errorf("setting key %q is not valid", s.Key)
		}
		if seen[s.Key] {
			return fmt.Errorf("setting %q is listed twice", s.Key)
		}
		seen[s.Key] = true
	}
	return nil
}

func (p *restoreSetParser) validateLibraries() error {
	seen := map[int]bool{}
	for _, l := range p.rs.Libraries.Libraries {
		if l.ID <= 0 {
			return fmt.Errorf("library id %d is not valid", l.ID)
		}
		if seen[l.ID] {
			return fmt.Errorf("library %d is listed twice", l.ID)
		}
		seen[l.ID] = true
		if strings.TrimSpace(l.Name) == "" || strings.TrimSpace(l.Type) == "" || len(l.Paths) == 0 {
			return fmt.Errorf("library %d has no name, type, or paths", l.ID)
		}
		for _, folder := range l.Paths {
			if !filepath.IsAbs(folder) || filepath.Clean(folder) != folder {
				return fmt.Errorf("library %d path %q is not a clean absolute path", l.ID, folder)
			}
		}
		if err := p.requireReferenced(SectionLibraries, l.Poster); err != nil {
			return err
		}
	}
	return nil
}

func (p *restoreSetParser) validateCollections() error {
	d := p.rs.Collections
	seen := map[string]bool{}
	claim := func(id string) error {
		if seen[id] {
			return fmt.Errorf("item %q is listed twice", id)
		}
		seen[id] = true
		return nil
	}
	for _, c := range d.Collections {
		if !validItemSegment(c.ID) || strings.TrimSpace(c.Title) == "" || c.LibraryID <= 0 {
			return fmt.Errorf("collection %q has no valid id, title, or library", c.ID)
		}
		if err := claim(collectionItemID(c.ID)); err != nil {
			return err
		}
		if !validRawJSON(c.QueryDefinition, c.SortConfig, c.SourceConfig) {
			return fmt.Errorf("collection %q carries invalid JSON", c.ID)
		}
		if err := p.requireCollectionImages(c.Poster, c.Backdrop); err != nil {
			return err
		}
	}
	if err := p.validateTemplateCollections(claim); err != nil {
		return err
	}
	for _, a := range d.ItemArtwork {
		if a.ContentID == "" || a.ImageType == "" || a.TargetType == "" {
			return fmt.Errorf("item artwork %q is incomplete", a.itemID())
		}
		if err := claim(a.itemID()); err != nil {
			return err
		}
		if err := p.requireReferenced(SectionCollections, a.Path); err != nil {
			return err
		}
	}
	for _, b := range d.Branding {
		if err := validBrandingEntry(b); err != nil {
			return err
		}
		if err := claim(b.itemID()); err != nil {
			return err
		}
		if err := p.requireReferenced(SectionCollections, b.Blob); err != nil {
			return err
		}
	}
	return nil
}

// validateTemplateCollections checks the template collections (version 3
// and later): a management key naming their own bundle, template, and
// library, and images listed for the section.
func (p *restoreSetParser) validateTemplateCollections(claim func(string) error) error {
	d := p.rs.Collections
	if len(d.TemplateCollections) > 0 && p.index.Version < 3 {
		return errors.New("template collections are not part of this format version")
	}
	for _, t := range d.TemplateCollections {
		bundle, template, library, ok := ParseTemplateManagementKey(t.ManagementKey)
		if !validItemSegment(t.ID) || !ok || bundle != t.BundleID || template != t.TemplateID || library != t.LibraryID ||
			len(t.ManagementKey) > 512 || strings.TrimSpace(t.Title) == "" {
			return fmt.Errorf("template collection %q has no valid id, title, or management key", t.ID)
		}
		if err := claim(t.itemID()); err != nil {
			return err
		}
		if !validRawJSON(t.SortConfig, t.SourceConfig) {
			return fmt.Errorf("template collection %q carries invalid JSON", t.ID)
		}
		if err := p.requireCollectionImages(t.Poster, t.Backdrop); err != nil {
			return err
		}
	}
	return nil
}

// validateHomeSections checks each section's identity, scope, and JSON, and
// that a carried collection is the one its configuration names.
func validateHomeSections(d HomeSectionsDocument) error {
	seen := map[string]bool{}
	for _, e := range d.Sections {
		if !validItemSegment(e.ID) || seen[e.ID] {
			return fmt.Errorf("section %q has no valid id or is listed twice", e.ID)
		}
		seen[e.ID] = true
		switch {
		case e.Scope == pageScopeHome && e.LibraryID == nil:
		case e.Scope == pageScopeLibrary && e.LibraryID != nil && *e.LibraryID > 0:
		default:
			return fmt.Errorf("section %q has no valid scope", e.ID)
		}
		if !validSectionID(e.SectionType) || strings.TrimSpace(e.Title) == "" || len(e.Title) > 512 || e.ItemLimit < 0 {
			return fmt.Errorf("section %q has no valid type or title", e.ID)
		}
		if !validRawJSON(e.Config) {
			return fmt.Errorf("section %q carries invalid JSON", e.ID)
		}
		if c := e.Collection; c != nil {
			if !validItemSegment(c.ID) || c.ID != sectionCollectionID(e.Config) || strings.TrimSpace(c.Title) == "" || c.LibraryID <= 0 {
				return fmt.Errorf("section %q carries a collection its configuration does not name", e.ID)
			}
			if !validRawJSON(c.QueryDefinition, c.SortConfig, c.SourceConfig) {
				return fmt.Errorf("section %q's collection carries invalid JSON", e.ID)
			}
			if storedKey(c.Poster) || storedKey(c.Backdrop) {
				return fmt.Errorf("section %q's collection names an image the set cannot carry", e.ID)
			}
		}
	}
	return nil
}

// validBrandingEntry admits only a branding asset setting whose reference
// is one safe file name naming the entry's blob: a branding entry is written
// as a setting, so anything else would let an archive set any setting.
func validBrandingEntry(b BrandingEntry) error {
	if !slices.Contains(branding.AssetSettingKeys(), b.Key) {
		return fmt.Errorf("branding entry %q is not a branding asset setting", b.Key)
	}
	if !validItemSegment(b.Ref) || branding.AssetObjectKey(b.Key, b.Ref) != b.Blob {
		return fmt.Errorf("branding entry %q does not name its own asset", b.Key)
	}
	return nil
}

// validRawJSON reports whether every non-empty value is valid JSON.
func validRawJSON(values ...json.RawMessage) bool {
	for _, raw := range values {
		if len(raw) > 0 && !json.Valid(raw) {
			return false
		}
	}
	return true
}

// requireCollectionImages requires each uploaded collection image
// (collection-images/) among keys to be listed for the collections section.
func (p *restoreSetParser) requireCollectionImages(keys ...string) error {
	for _, key := range keys {
		if strings.HasPrefix(key, "collection-images/") {
			if err := p.requireReferenced(SectionCollections, key); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireReferenced checks that a document's blob key is one its section's
// index entry lists: a document cannot point a restore at a key the index
// did not admit.
func (p *restoreSetParser) requireReferenced(section, key string) error {
	if key == "" {
		return nil
	}
	spec, ok := p.blobs[key]
	if !ok || spec.id != section {
		return fmt.Errorf("blob %q is not listed for section %q", key, section)
	}
	return nil
}

// itemIDs lists, sorted, the items whose files the index lists for a
// per-item section.
func (p *restoreSetParser) itemIDs(spec sectionSpec) []string {
	ids := map[string]bool{}
	for name := range p.files {
		if item, ok := spec.ownsFile(name); ok {
			ids[item] = true
		}
	}
	return slices.Sorted(maps.Keys(ids))
}

func (p *restoreSetParser) decodePlugins(spec sectionSpec) (int, error) {
	for _, id := range p.itemIDs(spec) {
		var doc PluginDocument
		docPath := spec.itemDir + id + "/" + pluginDocumentName
		ok, err := p.document(docPath, &doc)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("%s is missing", docPath)
		}
		if doc.PluginID != id {
			return 0, fmt.Errorf("%s names plugin %q", docPath, doc.PluginID)
		}
		archivePath := spec.itemDir + id + "/" + pluginArchiveName
		listed, ok := p.files[archivePath]
		if !ok {
			return 0, fmt.Errorf("%s is missing", archivePath)
		}
		if doc.Archive.Size != listed.Size || doc.Archive.SHA256 != listed.SHA256 {
			return 0, fmt.Errorf("%s does not describe its archive", docPath)
		}
		keys := map[string]bool{}
		for _, c := range doc.Configs {
			if c.Key == "" || keys[c.Key] {
				return 0, fmt.Errorf("%s: configuration key %q is empty or listed twice", docPath, c.Key)
			}
			keys[c.Key] = true
			if !json.Valid(c.Value) {
				return 0, fmt.Errorf("%s: configuration %q is not JSON", docPath, c.Key)
			}
		}
		p.rs.Plugins = append(p.rs.Plugins, doc)
	}
	return len(p.rs.Plugins), nil
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

// decodeStrict decodes one JSON value, refusing unknown fields and trailing
// data: within a format version every field is known.
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

// checkJSONDepth refuses JSON nested deeper than maxRestoreSetJSONDepth
// before it is decoded.
func checkJSONDepth(data []byte) error {
	depth := 0
	inString, escaped := false, false
	for _, c := range data {
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			depth++
			if depth > maxRestoreSetJSONDepth {
				return fmt.Errorf("JSON nested deeper than %d", maxRestoreSetJSONDepth)
			}
		case c == '}' || c == ']':
			depth--
		}
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
	for other := range rs.blobs {
		if other != key && path.Dir(other)+"/" == dir {
			out = append(out, other)
		}
	}
	sort.Strings(out[1:])
	return out
}

// pluginArchive reads an extracted plugin archive.
func (rs *RestoreSet) pluginArchive(pluginID string) ([]byte, error) {
	file, ok := rs.archives[pluginID]
	if !ok || file == "" {
		return nil, ErrNotFound
	}
	return os.ReadFile(file) //nolint:gosec // G304: a file this read created
}
