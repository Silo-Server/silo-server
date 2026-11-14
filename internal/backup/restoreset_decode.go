package backup

// Decoding and checking a restore set's documents, once the whole archive is
// read and verified (restoreSetParser.finish): each section's document,
// strictly decoded, with every entry no restore could write left out
// (RestoreSet.Unusable) and anything that breaks the set refused.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/branding"
)

func (p *restoreSetParser) document(name string, into any) (bool, error) {
	data, ok := p.documents[name]
	if !ok {
		return false, nil
	}
	if err := p.decode(data, into); err != nil {
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
		n := len(p.rs.Settings.Settings)
		if err := capEntries("settings", n, maxSettingEntries); err != nil {
			return 0, err
		}
		return n, p.validateSettings()
	case SectionLibraries:
		if ok, err := p.document(spec.document, &p.rs.Libraries); err != nil || !ok {
			return 0, requireDocument(spec, ok, err)
		}
		n := len(p.rs.Libraries.Libraries)
		if err := capEntries("libraries", n, maxLibraryEntries); err != nil {
			return 0, err
		}
		return n, p.validateLibraries()
	case SectionCollections:
		if ok, err := p.document(spec.document, &p.rs.Collections); err != nil || !ok {
			return 0, requireDocument(spec, ok, err)
		}
		d := p.rs.Collections
		n := len(d.Collections) + len(d.TemplateCollections) + len(d.ItemArtwork) + len(d.Branding)
		if err := errors.Join(capEntries("collections", len(d.Collections), maxCollectionEntries),
			capEntries("template collections", len(d.TemplateCollections), maxCollectionEntries),
			capEntries("item artwork entries", len(d.ItemArtwork), maxArtworkEntries),
			capEntries("branding assets", len(d.Branding), maxBrandingEntries)); err != nil {
			return 0, err
		}
		return n, p.validateCollections()
	case SectionHomeSections:
		if ok, err := p.document(spec.document, &p.rs.HomeSections); err != nil || !ok {
			return 0, requireDocument(spec, ok, err)
		}
		n := len(p.rs.HomeSections.Sections)
		if err := capEntries("page sections", n, maxSectionEntries); err != nil {
			return 0, err
		}
		return n, p.validateHomeSections()
	case SectionPlugins:
		return p.decodePlugins(spec)
	case SectionUsers:
		return p.decodeUsers(spec)
	}
	return 0, fmt.Errorf("no reader for section %q", si.ID)
}

// Caps on the entries of one section, at what a large server has: each is
// an item a preview lists and a restore may write, so a set beyond them is
// refused rather than previewed.
const (
	maxSettingEntries    = 5000
	maxLibraryEntries    = 1000
	maxCollectionEntries = 20_000
	maxArtworkEntries    = 50_000
	maxBrandingEntries   = 64
	maxSectionEntries    = 10_000
	maxPluginEntries     = 500
)

// capEntries refuses a set with more than max entries of one kind.
func capEntries(what string, n, maxEntries int) error {
	if n > maxEntries {
		return fmt.Errorf("it holds %d %s, more than the %d a restore reads", n, what, maxEntries)
	}
	return nil
}

// UnusableEntry is an entry of a restore set the reader left out.
type UnusableEntry struct {
	Section string
	// ID is the preview item id the reader gives it; it is never one a
	// restore request can choose.
	ID      string
	Kind    string
	Label   string
	Message string
}

// maxUnusableListed bounds the entries left out that a set lists for one
// section; the rest are counted (summarizeUnusable).
const maxUnusableListed = 100

// unusable records an entry left out, labeled by what the archive names it:
// the first maxUnusableListed of a section, and a count of the rest.
func (p *restoreSetParser) unusable(section, kind, label, message string) {
	if p.unusableCount == nil {
		p.unusableCount = map[string]int{}
	}
	if p.unusableCount[section]++; p.unusableCount[section] > maxUnusableListed {
		return
	}
	label = strings.TrimSpace(strings.ToValidUTF8(label, ""))
	if len(label) > 200 {
		label = strings.ToValidUTF8(label[:200], "")
	}
	if label == "" {
		label = "Unnamed entry"
	}
	p.rs.Unusable = append(p.rs.Unusable, UnusableEntry{Section: section, ID: fmt.Sprintf("unreadable:%d", len(p.rs.Unusable)+1),
		Kind: kind, Label: label, Message: message})
}

// summarizeUnusable adds, for a section with more entries left out than it
// lists, one entry saying how many more there are.
func (p *restoreSetParser) summarizeUnusable(section string) {
	more := p.unusableCount[section] - maxUnusableListed
	if more <= 0 {
		return
	}
	kind := ""
	for _, e := range p.rs.Unusable {
		if e.Section == section {
			kind = e.Kind
		}
	}
	p.rs.Unusable = append(p.rs.Unusable, UnusableEntry{Section: section, ID: fmt.Sprintf("unreadable:%d", len(p.rs.Unusable)+1),
		Kind: kind, Label: fmt.Sprintf("%d more entries", more),
		Message: fmt.Sprintf("They cannot be restored either; only the first %d are listed.", maxUnusableListed)})
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

func (p *restoreSetParser) validateSettings() error {
	d := &p.rs.Settings
	seen := map[string]bool{}
	kept := d.Settings[:0]
	for _, s := range d.Settings {
		if s.Key == "" || len(s.Key) > 256 || !utf8.ValidString(s.Key) {
			p.unusable(SectionSettings, KindSettingsPage, s.Key, "Its setting key is not one a server can have.")
			continue
		}
		if seen[s.Key] {
			return fmt.Errorf("setting %q is listed twice", s.Key)
		}
		seen[s.Key] = true
		if entryTooLarge(s) {
			p.unusable(SectionSettings, KindSettingsPage, s.Key, msgEntryTooLarge)
			continue
		}
		kept = append(kept, s)
	}
	d.Settings = kept
	return nil
}

func (p *restoreSetParser) validateLibraries() error {
	d := &p.rs.Libraries
	seen := map[int]bool{}
	kept := d.Libraries[:0]
	for _, l := range d.Libraries {
		if seen[l.ID] && l.ID > 0 {
			return fmt.Errorf("library %d is listed twice", l.ID)
		}
		seen[l.ID] = true
		if err := p.requireReferenced(SectionLibraries, l.Poster); err != nil {
			return err
		}
		if msg := libraryProblem(l); msg != "" {
			p.unusable(SectionLibraries, KindLibrary, l.Name, msg)
			continue
		}
		kept = append(kept, l)
	}
	d.Libraries = kept
	return nil
}

// libraryProblem says why a library cannot be restored, or "".
func libraryProblem(l LibraryEntry) string {
	if l.ID <= 0 || strings.TrimSpace(l.Name) == "" || strings.TrimSpace(l.Type) == "" || len(l.Paths) == 0 {
		return "It has no valid id, name, type, or folders."
	}
	if entryTooLarge(l) {
		return msgEntryTooLarge
	}
	if len(l.Paths) > maxLibraryFolders {
		return fmt.Sprintf("It has more than %d folders.", maxLibraryFolders)
	}
	seen := make(map[string]bool, len(l.Paths))
	for _, folder := range l.Paths {
		if !filepath.IsAbs(folder) || filepath.Clean(folder) != folder {
			return "A folder is not a clean absolute path."
		}
		if seen[folder] {
			return "A folder is listed twice."
		}
		seen[folder] = true
	}
	return ""
}

// repeatsItem reports whether ids holds an id twice.
func repeatsItem(ids []string) bool {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

// maxLibraryFolders bounds a restored library's folders, each of which a
// preview checks on this server's disk. The library editor sets no limit;
// a library has a handful.
const maxLibraryFolders = 64

func (p *restoreSetParser) validateCollections() error {
	d := &p.rs.Collections
	seen := map[string]bool{}
	claim := func(id string) error {
		if seen[id] {
			return fmt.Errorf("item %q is listed twice", id)
		}
		seen[id] = true
		return nil
	}
	items := 0
	for _, c := range d.Collections {
		items += len(c.Items)
	}
	if items > maxCollectionItems {
		return fmt.Errorf("its collections list more than %d items", maxCollectionItems)
	}
	collections := d.Collections[:0]
	for _, c := range d.Collections {
		if err := p.requireCollectionImages(c.Poster, c.Backdrop); err != nil {
			return err
		}
		if !validItemSegment(c.ID) || strings.TrimSpace(c.Title) == "" || c.LibraryID <= 0 {
			p.unusable(SectionCollections, KindCollection, c.Title, "It has no valid id, title, or library.")
			continue
		}
		if err := claim(collectionItemID(c.ID)); err != nil {
			return err
		}
		if !validRawJSON(c.QueryDefinition, c.SortConfig, c.SourceConfig) {
			p.unusable(SectionCollections, KindCollection, c.Title, "Its rules or sync source are not valid JSON.")
			continue
		}
		if collectionTooLarge(c) {
			p.unusable(SectionCollections, KindCollection, c.Title, msgEntryTooLarge)
			continue
		}
		// No editor lists an item twice; a document that repeats one would
		// multiply the work its restore does under the lock.
		if repeatsItem(c.Items) {
			p.unusable(SectionCollections, KindCollection, c.Title, "It lists an item more than once.")
			continue
		}
		collections = append(collections, c)
	}
	d.Collections = collections
	if err := p.validateTemplateCollections(claim); err != nil {
		return err
	}
	artwork := d.ItemArtwork[:0]
	for _, a := range d.ItemArtwork {
		if err := p.requireReferenced(SectionCollections, a.Path); err != nil {
			return err
		}
		if a.ContentID == "" || a.ImageType == "" || a.TargetType == "" {
			p.unusable(SectionCollections, KindItemArtwork, a.ContentID, "It does not say which item and image it is.")
			continue
		}
		if err := claim(a.itemID()); err != nil {
			return err
		}
		if entryTooLarge(a) {
			p.unusable(SectionCollections, KindItemArtwork, a.ContentID, msgEntryTooLarge)
			continue
		}
		artwork = append(artwork, a)
	}
	d.ItemArtwork = artwork
	brandingEntries := d.Branding[:0]
	for _, b := range d.Branding {
		if err := p.requireReferenced(SectionCollections, b.Blob); err != nil {
			return err
		}
		if err := validBrandingEntry(b); err != nil {
			p.unusable(SectionCollections, KindBranding, b.Key, "It is not a branding image this server can set.")
			continue
		}
		if err := claim(b.itemID()); err != nil {
			return err
		}
		if entryTooLarge(b) {
			p.unusable(SectionCollections, KindBranding, b.Key, msgEntryTooLarge)
			continue
		}
		brandingEntries = append(brandingEntries, b)
	}
	d.Branding = brandingEntries
	return nil
}

// validateTemplateCollections checks the template collections (version 3
// and later): a management key naming their own bundle, template, and
// library, and images listed for the section.
func (p *restoreSetParser) validateTemplateCollections(claim func(string) error) error {
	d := &p.rs.Collections
	if len(d.TemplateCollections) > 0 && p.index.Version < 3 {
		return errors.New("template collections are not part of this format version")
	}
	kept := d.TemplateCollections[:0]
	for _, t := range d.TemplateCollections {
		if err := p.requireCollectionImages(t.Poster, t.Backdrop); err != nil {
			return err
		}
		bundle, template, library, ok := ParseTemplateManagementKey(t.ManagementKey)
		if !validItemSegment(t.ID) || !ok || bundle != t.BundleID || template != t.TemplateID || library != t.LibraryID ||
			len(t.ManagementKey) > 512 || strings.TrimSpace(t.Title) == "" {
			p.unusable(SectionCollections, KindTemplateCollection, t.Title, "It has no valid id, title, or template.")
			continue
		}
		if err := claim(t.itemID()); err != nil {
			return err
		}
		if !validRawJSON(t.SortConfig, t.SourceConfig) {
			p.unusable(SectionCollections, KindTemplateCollection, t.Title, "Its sort or sync source is not valid JSON.")
			continue
		}
		if entryTooLarge(t) {
			p.unusable(SectionCollections, KindTemplateCollection, t.Title, msgEntryTooLarge)
			continue
		}
		kept = append(kept, t)
	}
	d.TemplateCollections = kept
	return nil
}

// validateHomeSections checks each section's identity, scope, and JSON, and
// that a carried collection is the one its configuration names. An id
// listed twice is fatal; any other problem leaves that section out.
func (p *restoreSetParser) validateHomeSections() error {
	d := &p.rs.HomeSections
	seen := map[string]bool{}
	kept := d.Sections[:0]
	for _, e := range d.Sections {
		if e.WebOnly && p.index.Version < 4 {
			return errors.New("a web-only section is not part of this format version")
		}
		if (e.TitleKey != "" || len(e.TitleArgs) > 0) && p.index.Version < restoreSetSectionTitleKeys {
			return errors.New("a section title key is not part of this format version")
		}
		if validItemSegment(e.ID) && seen[e.ID] {
			return fmt.Errorf("section %q is listed twice", e.ID)
		}
		seen[e.ID] = true
		if msg := homeSectionProblem(e); msg != "" {
			p.unusable(SectionHomeSections, KindHomeSection, e.Title, msg)
			continue
		}
		kept = append(kept, e)
	}
	d.Sections = kept
	return nil
}

// homeSectionProblem says why a section cannot be restored, or "".
func homeSectionProblem(e HomeSectionEntry) string {
	homePage := e.Scope == pageScopeHome && e.LibraryID == nil
	libraryPage := e.Scope == pageScopeLibrary && e.LibraryID != nil && *e.LibraryID > 0
	switch {
	case !validItemSegment(e.ID):
		return "It has no valid id."
	case !homePage && !libraryPage:
		return "It has no valid page."
	// An empty title is kept: bulk section creation persists one.
	case !validSectionID(e.SectionType) || len(e.Title) > 512 || e.ItemLimit < 0:
		return "It has no valid type, title, or item limit."
	case len(e.TitleKey) > maxSectionTitleKey || len(e.TitleArgs) > maxSectionTitleArgs:
		return "It has no valid title key."
	case !validRawJSON(e.Config):
		return "Its configuration is not valid JSON."
	}
	if c := e.Collection; c != nil {
		switch {
		case !validItemSegment(c.ID) || c.ID != sectionCollectionID(e.Config) || strings.TrimSpace(c.Title) == "" || c.LibraryID <= 0:
			return "It carries a collection its configuration does not name."
		case !validRawJSON(c.QueryDefinition, c.SortConfig, c.SourceConfig):
			return "Its collection's rules are not valid JSON."
		case storedKey(c.Poster) || storedKey(c.Backdrop):
			return "Its collection names an image the backup cannot carry."
		case collectionTooLarge(c.CollectionEntry):
			return msgEntryTooLarge
		// As for an admin collection: a repeated item would multiply the
		// work its restore does under the lock.
		case repeatsItem(c.Items):
			return "Its collection lists an item more than once."
		}
	}
	section := e
	section.Collection = nil
	if entryTooLarge(section) {
		return msgEntryTooLarge
	}
	return ""
}

// maxRestoredEntryBytes bounds every entry a restore writes (a setting, a
// library, a plugin configuration, a collection, a template collection, an
// item artwork, a branding asset, a page section, and in account documents
// each profile), encoded, as the v2 API's request body cap
// (apiv2.MaxJSONBodyBytes, which this package cannot import) bounds what one
// create or update writes: a restore stores no more for one than its editor
// could. A collection's items are not counted; they are added one by one.
const maxRestoredEntryBytes = 1 << 20

// msgEntryTooLarge leaves out an entry over maxRestoredEntryBytes.
const msgEntryTooLarge = "It holds more than this server's editors accept in one request."

// entryTooLarge reports an entry over maxRestoredEntryBytes, encoded.
func entryTooLarge(v any) bool {
	data, err := json.Marshal(v)
	return err != nil || len(data) > maxRestoredEntryBytes
}

// collectionTooLarge is entryTooLarge for a collection without its items.
func collectionTooLarge(c CollectionEntry) bool {
	c.Items = nil
	return entryTooLarge(c)
}

// maxThumbhashLength bounds a thumbhash: base64 of at most 25 bytes.
const maxThumbhashLength = 64

// Bounds on a section's title key and its placeholder values: a key is a
// catalog id such as home.section.recentlyAddedIn, and a message names few
// placeholders.
const (
	maxSectionTitleKey  = 128
	maxSectionTitleArgs = 8
)

// validThumbhash reports whether s could be a thumbhash; a restore stores
// none that is not.
func validThumbhash(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > maxThumbhashLength {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
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
	if err := capEntries("plugins", len(p.itemIDs(spec)), maxPluginEntries); err != nil {
		return 0, err
	}
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
		if msg := pluginConfigProblem(doc); msg != "" {
			p.unusable(SectionPlugins, KindPlugin, doc.PluginID, msg)
			continue
		}
		p.rs.Plugins = append(p.rs.Plugins, doc)
	}
	return len(p.itemIDs(spec)), nil
}

// pluginConfigProblem says why a plugin's configuration cannot be restored,
// or "".
func pluginConfigProblem(doc PluginDocument) string {
	keys := map[string]bool{}
	for _, c := range doc.Configs {
		if c.Key == "" || keys[c.Key] {
			return "A configuration key is empty or listed twice."
		}
		keys[c.Key] = true
		if !json.Valid(c.Value) {
			return "A configuration value is not valid JSON."
		}
		if entryTooLarge(c) {
			return msgEntryTooLarge
		}
	}
	return ""
}
