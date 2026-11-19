package scanner

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeEbookEnrichmentQueue struct {
	err error
}

func (f *fakeEbookEnrichmentQueue) Enqueue(context.Context, string, int) error {
	return f.err
}

func (f *fakeEbookEnrichmentQueue) ReconcileMissing(context.Context, int, int, int) (int, int, bool, error) {
	return 0, 0, false, nil
}

func TestScannerEbookEnrichmentHookDoesNotFailScanOnQueueFailure(t *testing.T) {
	queueErr := errors.New("queue unavailable")
	s := &Scanner{}
	s.SetEbookEnrichmentQueue(&fakeEbookEnrichmentQueue{err: queueErr})

	err := s.enqueueEbookEnrichment(context.Background(), "ebook-1")
	if err != nil {
		t.Fatalf("enqueueEbookEnrichment() error = %v, want nil", err)
	}
}

type fakeFilesystemItemReader struct {
	items []*models.MediaItem
	err   error
}

func (f *fakeFilesystemItemReader) GetByIDs(_ context.Context, _ []string) ([]*models.MediaItem, error) {
	return f.items, f.err
}

func TestParseEbookEPUBMetadataHandlesLatin1OPF(t *testing.T) {
	path := writeTestEPUBWithOPFBytes(t, []byte("<?xml version=\"1.0\" encoding=\"iso-8859-1\"?>\n"+
		"<package xmlns:dc=\"http://purl.org/dc/elements/1.1/\"><metadata>"+
		"<dc:title>Caf\xe9 Society</dc:title>"+
		"<dc:creator>Fran\xe7ois Author</dc:creator>"+
		"<dc:publisher>Cr\xe8me Press</dc:publisher>"+
		"</metadata></package>"))

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Title != "Café Society" || strings.Join(got.Authors, ", ") != "François Author" || got.Publisher != "Crème Press" {
		t.Fatalf("parsed latin1 metadata = title %q authors %v publisher %q", got.Title, got.Authors, got.Publisher)
	}
}

func TestParseEbookFileAppliesExternalOPFSidecar(t *testing.T) {
	dir := t.TempDir()
	bookPath := filepath.Join(dir, "Dune.cbr")
	if err := os.WriteFile(bookPath, nil, 0o644); err != nil {
		t.Fatalf("write book: %v", err)
	}
	sidecar := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/">
  <metadata>
    <dc:title>Dune</dc:title>
    <dc:creator>Frank Herbert</dc:creator>
    <dc:description>A desert planet and a dangerous inheritance.</dc:description>
    <dc:identifier>9780441172719</dc:identifier>
    <dc:subject>Science Fiction</dc:subject>
  </metadata>
</package>`
	if err := os.WriteFile(filepath.Join(dir, "Dune.opf"), []byte(sidecar), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	got, err := parseEbookFile(bookPath)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if got.Title != "Dune" || len(got.Authors) != 1 || got.Authors[0] != "Frank Herbert" {
		t.Fatalf("sidecar identity = title %q authors %v", got.Title, got.Authors)
	}
	if got.Description == "" || got.ISBN != "9780441172719" {
		t.Fatalf("sidecar metadata = description %q ISBN %q", got.Description, got.ISBN)
	}
	if got.Format != "cbr" {
		t.Fatalf("Format = %q, want original file format cbr", got.Format)
	}
}

// Names that are not UTF-8 reach the parsers fspath-encoded: the archive is
// opened by its real name, and the sidecar passes the same-file check.
func TestParseEbookFileReadsNonUTF8Names(t *testing.T) {
	dir := t.TempDir()
	epub := filepath.Join(dir, "B\xe5rd.epub")
	if err := os.Rename(writeTestEPUB(t, nil), epub); err != nil {
		t.Skipf("file system rejects non-UTF-8 names: %v", err)
	}
	got, err := parseEbookFile(fspath.Encode(epub))
	if err != nil || got.Title != "The Test Ebook" {
		t.Fatalf("parseEbookFile(epub) = %q, %v", got.Title, err)
	}

	book := filepath.Join(dir, "Dune p\xe5 norsk.cbr")
	sidecar := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/"><metadata><dc:title>Dune</dc:title></metadata></package>`
	if err := os.WriteFile(book, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dune p\xe5 norsk.opf"), []byte(sidecar), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = parseEbookFile(fspath.Encode(book))
	if err != nil || got.Title != "Dune" {
		t.Fatalf("parseEbookFile(sidecar) = %q, %v; want the sidecar title", got.Title, err)
	}
}

func TestParseEbookOPFSidecarRejectsSymlinkedSidecar(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside-library.opf")
	sidecar := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/">
  <metadata><dc:title>Smuggled</dc:title></metadata>
</package>`
	if err := os.WriteFile(target, []byte(sidecar), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "Dune.opf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := parseEbookOPFSidecar(link); err == nil {
		t.Fatal("parseEbookOPFSidecar accepted a symlinked sidecar; a leaf swapped to a symlink after the Lstat gate would be followed to its target")
	}
}

func TestInsertEbookISBNProviderIDToleratesISBNOwnedByAnotherItem(t *testing.T) {
	exec := &erroringEbookExecutor{err: &pgconn.PgError{Code: "23505"}}
	if err := insertEbookISBNProviderID(context.Background(), exec, "book-1", "9780441172719"); err != nil {
		t.Fatalf("unique-violation on the (provider, provider_id) constraint must not fail the scan, got %v", err)
	}

	boom := errors.New("connection lost")
	exec = &erroringEbookExecutor{err: boom}
	if err := insertEbookISBNProviderID(context.Background(), exec, "book-1", "9780441172719"); !errors.Is(err, boom) {
		t.Fatalf("non-unique-violation error = %v, want %v", err, boom)
	}
}

type erroringEbookExecutor struct {
	err error
}

func (e *erroringEbookExecutor) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, e.err
}

func TestReadEPUBZipEntryRejectsOversizedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	zw := zip.NewWriter(file)
	w, err := zw.Create("OPS/content.opf")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write([]byte(strings.Repeat("x", maxEPUBMetadataEntrySize+1))); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()

	_, err = readEPUBZipEntry(&reader.Reader, "OPS/content.opf")
	if err == nil || !strings.Contains(err.Error(), "epub entry too large") {
		t.Fatalf("readEPUBZipEntry error = %v, want size limit error", err)
	}
}

// parsePDFFixture writes data, completed with a cross-reference table and
// trailer, to a temp PDF and parses it.
func parsePDFFixture(t *testing.T, data string) parsedEbook {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, completePDFMetadataFixture([]byte(data)), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	return got
}

func TestEbookPeopleWriteAllowedFillsEmptyOnlyWhenCurated(t *testing.T) {
	providerAuthor := []models.ItemPerson{{Person: models.Person{Name: "Provider Author"}, Kind: models.PersonKindAuthor}}
	nonAuthorOnly := []models.ItemPerson{{Person: models.Person{Name: "Manual Writer"}, Kind: models.PersonKindWriter}}

	cases := []struct {
		name     string
		curated  bool
		existing []models.ItemPerson
		want     bool
	}{
		{"pending item always writes", false, providerAuthor, true},
		{"curated with provider authors is read-only", true, providerAuthor, false},
		{"curated without author credits fills empty", true, nil, true},
		{"curated with only non-author credits fills empty", true, nonAuthorOnly, true},
	}
	for _, tc := range cases {
		if got := ebookPeopleWriteAllowed(tc.curated, tc.existing); got != tc.want {
			t.Errorf("%s: ebookPeopleWriteAllowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveEbookMediaItemCreatesNewWhenRootHasNoClaim(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveEbookMediaItem(context.Background(), finder, writer, 7, "/library/Author/Book.epub", &parsedEbook{
		Title: "Book", Year: 2024, Authors: []string{"Author"}, Description: "Overview", Publisher: "Publisher", Genres: []string{"Fiction"}, Language: "en",
	})
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got == "" || len(writer.upserts) != 1 {
		t.Fatalf("contentID/upserts = %q/%d, want id and one upsert", got, len(writer.upserts))
	}
	item := writer.upserts[0]
	if item.Type != "ebook" || item.Title != "Book" || item.Year != 2024 || item.Overview != "Overview" || item.OriginalLanguage != "en" {
		t.Fatalf("upserted ebook item = %+v", item)
	}
	if item.Status != "pending" {
		t.Fatalf("Status = %q, want pending so enrichment can promote it to matched", item.Status)
	}
}

func TestResolveEbookMediaItemReusesRootScopedContentID(t *testing.T) {
	finder := &fakeRootContentFinder{contentID: "ebook-root-id"}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveEbookMediaItem(context.Background(), finder, writer, 7, "/library/Author/Book.epub", &parsedEbook{Title: "Book"})
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got != "ebook-root-id" {
		t.Fatalf("contentID = %q, want root-scoped id", got)
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("unexpected item upsert for existing root: %d", len(writer.upserts))
	}
}

func TestApplyEbookToMediaItemPreservesExistingYearWhenParsedYearMissing(t *testing.T) {
	item := &models.MediaItem{
		Type:  "ebook",
		Title: "Existing",
		Year:  1999,
	}

	applyEbookToMediaItem(item, &parsedEbook{Title: "Updated", Year: 0})

	if item.Title != "Updated" {
		t.Fatalf("Title = %q, want Updated", item.Title)
	}
	if item.Year != 1999 {
		t.Fatalf("Year = %d, want preserved 1999", item.Year)
	}
}

func TestEbookPeopleCreditsEqualAuthorsOnly(t *testing.T) {
	existing := []models.ItemPerson{
		{Person: models.Person{Name: "Ada Writer"}, Kind: models.PersonKindAuthor, SortOrder: 0},
		{Person: models.Person{Name: "Ben Author"}, Kind: models.PersonKindAuthor, SortOrder: 1},
		{Person: models.Person{Name: "Manual Contributor"}, Kind: models.PersonKindWriter, SortOrder: 2},
	}
	desired := []ebookCredit{
		{Name: "Ada Writer", Kind: models.PersonKindAuthor},
		{Name: "Ben Author", Kind: models.PersonKindAuthor},
	}
	if !ebookPeopleCreditsEqual(existing, desired) {
		t.Fatal("expected matching author credits to compare equal while ignoring non-authors")
	}

	existing[1] = models.ItemPerson{Person: models.Person{Name: "Other Author"}, Kind: models.PersonKindAuthor, SortOrder: 1}
	if ebookPeopleCreditsEqual(existing, desired) {
		t.Fatal("expected different author credit to make ebook author set differ")
	}
}

func TestEbookPeopleCreditsEqualRejectsStaleNarratorCredit(t *testing.T) {
	existing := []models.ItemPerson{
		{Person: models.Person{Name: "Ada Writer"}, Kind: models.PersonKindAuthor, SortOrder: 0},
		{Person: models.Person{Name: "Legacy Narrator"}, Kind: models.PersonKindNarrator, SortOrder: 1},
	}
	desired := []ebookCredit{
		{Name: "Ada Writer", Kind: models.PersonKindAuthor},
	}

	if ebookPeopleCreditsEqual(existing, desired) {
		t.Fatal("expected stale narrator credit to force ebook people replacement")
	}
}

func TestEbookPeopleMergePreservesExistingNonAuthorCreditsExceptNarrators(t *testing.T) {
	existing := []models.ItemPerson{
		{Person: models.Person{ID: 10, Name: "Old Author"}, Kind: models.PersonKindAuthor, SortOrder: 0},
		{Person: models.Person{ID: 20, Name: "Manual Writer"}, Kind: models.PersonKindWriter, SortOrder: 1, Character: "essay"},
		{Person: models.Person{ID: 30, Name: "Manual Narrator"}, Kind: models.PersonKindNarrator, SortOrder: 2},
	}
	authors := []ebookResolvedAuthor{
		{ID: 40, Name: "New Author"},
	}

	got := mergeEbookPeople(existing, authors)
	if len(got) != 2 {
		t.Fatalf("merged people len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Person.ID != 20 || got[0].Kind != models.PersonKindWriter || got[0].Character != "essay" {
		t.Fatalf("first preserved non-author = %+v", got[0])
	}
	if got[1].Person.ID != 40 || got[1].Kind != models.PersonKindAuthor || got[1].SortOrder != 1 {
		t.Fatalf("new author credit = %+v", got[1])
	}
}

func TestCollectEbookRootScansExcludesUnmountedRootFromReconciliation(t *testing.T) {
	healthy := t.TempDir()
	if err := os.WriteFile(filepath.Join(healthy, "book.epub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	unmounted := filepath.Join(t.TempDir(), "gone")

	scans, err := collectEbookRootScans(t.Context(), 44, []string{unmounted, healthy}, nil)
	if err != nil {
		t.Fatalf("collectEbookRootScans: %v", err)
	}
	if len(scans) != 2 {
		t.Fatalf("scans = %d, want 2", len(scans))
	}
	if !scans[0].failed() || scans[0].rootErr == nil {
		t.Fatalf("unmounted root scan = %+v, want failed with rootErr", scans[0])
	}
	if scans[1].failed() || len(scans[1].files) != 1 {
		t.Fatalf("healthy root scan = %+v, want 1 file and no failure", scans[1])
	}

	reconcileRoots, sawFiles := splitEbookReconcileRoots(scans)
	if !sawFiles {
		t.Fatal("sawFiles = false, want true for the healthy root")
	}
	if len(reconcileRoots) != 1 || reconcileRoots[0] != healthy {
		t.Fatalf("reconcileRoots = %v, want only the healthy root: an unmounted root must never be reconciled (no mass-missing, no deletion)", reconcileRoots)
	}
}

func TestCollectEbookRootScansTreatsSingleFileRootAsNonReconciling(t *testing.T) {
	dir := t.TempDir()
	fileRoot := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}

	scans, err := collectEbookRootScans(t.Context(), 44, []string{fileRoot}, nil)
	if err != nil {
		t.Fatalf("collectEbookRootScans: %v", err)
	}
	if len(scans) != 1 || scans[0].failed() || !scans[0].fileOnly {
		t.Fatalf("scans = %+v, want one healthy file-only scan", scans)
	}
	// The file itself is still indexed; only reconciliation is withheld.
	if len(scans[0].files) != 1 {
		t.Fatalf("files = %v, want the ebook file root indexed", scans[0].files)
	}
	if roots, _ := splitEbookReconcileRoots(scans); len(roots) != 0 {
		t.Fatalf("reconcileRoots = %v, want none", roots)
	}
}

func TestCollectEbookRootScansMidWalkSubtreeErrorExcludesRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based subtree failure cannot be simulated as root")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "readable.epub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	locked := filepath.Join(root, "zz-locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(locked, "hidden.epub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write hidden ebook: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	scans, err := collectEbookRootScans(t.Context(), 44, []string{root}, nil)
	if err != nil {
		t.Fatalf("collectEbookRootScans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("scans = %d, want 1", len(scans))
	}
	if len(scans[0].walkFailures) == 0 || !scans[0].failed() {
		t.Fatalf("scan = %+v, want walk failure recorded for unreadable subtree", scans[0])
	}
	if len(scans[0].files) != 1 {
		t.Fatalf("files = %v, want the readable ebook still indexed", scans[0].files)
	}
	// The unseen hidden.epub must not be marked missing: the whole root sits
	// out of reconciliation.
	if roots, _ := splitEbookReconcileRoots(scans); len(roots) != 0 {
		t.Fatalf("reconcileRoots = %v, want none after a mid-walk subtree error", roots)
	}
}

func TestCollectEbookRootScansFollowsSymlinkedRoot(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "book.epub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	link := filepath.Join(t.TempDir(), "library")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	scans, err := collectEbookRootScans(t.Context(), 44, []string{link}, nil)
	if err != nil {
		t.Fatalf("collectEbookRootScans: %v", err)
	}
	if len(scans) != 1 || scans[0].failed() {
		t.Fatalf("scans = %+v, want one clean scan through the symlinked root", scans)
	}
	want := filepath.Join(link, "book.epub")
	if len(scans[0].files) != 1 || scans[0].files[0] != want {
		t.Fatalf("files = %v, want %q recorded under the logical (symlink) path", scans[0].files, want)
	}
	if roots, sawFiles := splitEbookReconcileRoots(scans); !sawFiles || len(roots) != 1 {
		t.Fatalf("reconcileRoots/sawFiles = %v/%v, want symlinked root reconcilable", roots, sawFiles)
	}
}

type fakeEbookCleanupGuard struct {
	allowance    bool
	consumeCalls int
	warnings     []string
	consumeErr   error
}

func (f *fakeEbookCleanupGuard) ConsumeEmptyCleanupAllowance(_ context.Context, _ int) (bool, error) {
	f.consumeCalls++
	return f.allowance, f.consumeErr
}

func (f *fakeEbookCleanupGuard) SetScanWarning(_ context.Context, _ int, code, _ string, _ time.Time) error {
	f.warnings = append(f.warnings, code)
	return nil
}

func TestEbookEmptyCleanupAllowedRequiresExplicitAllowance(t *testing.T) {
	guard := &fakeEbookCleanupGuard{allowance: false}

	allowed, err := ebookEmptyCleanupAllowed(context.Background(), guard, 44, true)
	if err != nil {
		t.Fatalf("ebookEmptyCleanupAllowed: %v", err)
	}
	if allowed {
		t.Fatal("allowed = true, want false: an empty root must not reconcile without confirmation")
	}
	if guard.consumeCalls != 1 {
		t.Fatalf("consumeCalls = %d, want 1", guard.consumeCalls)
	}
	if len(guard.warnings) != 1 || guard.warnings[0] != "empty_root" {
		t.Fatalf("warnings = %v, want one empty_root scan warning", guard.warnings)
	}
}

func TestEbookEmptyCleanupAllowedConsumesAllowanceOnce(t *testing.T) {
	guard := &fakeEbookCleanupGuard{allowance: true}

	allowed, err := ebookEmptyCleanupAllowed(context.Background(), guard, 44, true)
	if err != nil {
		t.Fatalf("ebookEmptyCleanupAllowed: %v", err)
	}
	if !allowed {
		t.Fatal("allowed = false, want true once the operator confirmed cleanup")
	}
	if len(guard.warnings) != 0 {
		t.Fatalf("warnings = %v, want none for a confirmed cleanup", guard.warnings)
	}
}

func TestEbookEmptyCleanupAllowedNeverConsumesForSubtreeScans(t *testing.T) {
	guard := &fakeEbookCleanupGuard{allowance: true}

	allowed, err := ebookEmptyCleanupAllowed(context.Background(), guard, 44, false)
	if err != nil {
		t.Fatalf("ebookEmptyCleanupAllowed: %v", err)
	}
	if allowed || guard.consumeCalls != 0 {
		t.Fatalf("allowed/consumeCalls = %v/%d, want subtree scans to skip cleanup without touching the folder allowance", allowed, guard.consumeCalls)
	}
}

func TestEbookEmptyCleanupAllowedNilGuardDeniesCleanup(t *testing.T) {
	allowed, err := ebookEmptyCleanupAllowed(context.Background(), nil, 44, true)
	if err != nil || allowed {
		t.Fatalf("allowed/err = %v/%v, want denial when no guard repository is wired", allowed, err)
	}
}

func writeTestEPUB(t *testing.T, identifiers []string) string {
	return writeTestEPUBWithMeta(t, identifiers, "")
}

func writeTestEPUBWithMeta(t *testing.T, identifiers []string, extraMetaXML string) string {
	return writeTestEPUBWithDescriptionAndMeta(t, identifiers, "Back cover copy", extraMetaXML)
}

func writeTestEPUBWithDescriptionAndMeta(t *testing.T, identifiers []string, description string, extraMetaXML string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "book.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	zw := zip.NewWriter(file)
	add := func(name, body string) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)
	var identifierXML strings.Builder
	for _, identifier := range identifiers {
		identifierXML.WriteString("    <dc:identifier>")
		identifierXML.WriteString(identifier)
		identifierXML.WriteString("</dc:identifier>\n")
	}
	add("OPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>The Test Ebook</dc:title>
    <dc:creator>Ada Writer</dc:creator>
    <dc:creator>Ben Author</dc:creator>
`+identifierXML.String()+`
    <dc:publisher>Silo Press</dc:publisher>
    <dc:date>2024-03-10</dc:date>
    <dc:language>en</dc:language>
    <dc:subject>Fiction</dc:subject>
    <dc:subject>Adventure</dc:subject>
    <dc:description>`+description+`</dc:description>
    <meta name="calibre:series" content="Test Series"/>
    <meta name="calibre:series_index" content="2"/>
`+extraMetaXML+`
  </metadata>
</package>`)
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return path
}

func writeTestEPUBWithOPFBytes(t *testing.T, opf []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "book.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	zw := zip.NewWriter(file)
	add := func(name string, body []byte) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	add("META-INF/container.xml", []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`))
	add("OPS/content.opf", opf)
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return path
}

func TestUpdateExistingEbookMediaItemPreservesCuratedMatchedItem(t *testing.T) {
	reader := &fakeFilesystemItemReader{items: []*models.MediaItem{{
		ContentID: "ebook-root-id",
		Type:      "ebook",
		Title:     "Curated Title",
		Year:      2020,
		Status:    "matched",
	}}}
	writer := &fakeFilesystemItemWriter{}

	curated, err := updateExistingEbookMediaItem(context.Background(), reader, writer, "ebook-root-id", &parsedEbook{
		Title: "Embedded File Title",
		Year:  2026,
	})
	if err != nil {
		t.Fatalf("updateExistingEbookMediaItem: %v", err)
	}
	if !curated {
		t.Fatal("curated = false, want true for a matched item")
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0: matched items must not be overwritten by file metadata", len(writer.upserts))
	}
}

func TestParseEbookPDFReadsInfoDictionaryFromFileTail(t *testing.T) {
	// Non-linearized PDFs put the Info dictionary at the end of the file and
	// typically have no Info keys in the head at all; the parser must find the
	// trailing dictionary past the head scan window and use it to fill every
	// key the head did not produce.
	head := "%PDF-1.7\n" +
		"1 0 obj\n" +
		"<< /Author (Ada Writer)\n" +
		">>\nendobj\n"
	tail := "2 0 obj\n" +
		"<< /Title (Tail Info Title)\n" +
		"   /CreationDate (D:20240102030405Z)\n" +
		">>\nendobj\n" +
		"trailer\n<< /Info 2 0 R >>\n%%EOF"
	padding := strings.Repeat("x", maxPDFMetadataScanSize)

	got := parsePDFFixture(t, head+padding+tail)
	if got.Title != "Tail Info Title" {
		t.Fatalf("Title = %q, want tail Info dictionary value", got.Title)
	}
	if strings.Join(got.Authors, ", ") != "Ada Writer" {
		t.Fatalf("Authors = %v, want head-only value preserved", got.Authors)
	}
	if got.Year != 2024 {
		t.Fatalf("Year = %d, want 2024 from tail CreationDate", got.Year)
	}
}

func TestParseEbookFB2RejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.fb2")
	if err := os.WriteFile(path, make([]byte, maxEPUBMetadataEntrySize+1), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}

	_, err := parseEbookFile(path)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("parseEbookFile error = %v, want size-cap rejection", err)
	}
}

// buildTestMOBI assembles a minimal but valid MOBI/AZW container: a PDB header,
// a single record-info entry pointing at record 0, and record 0 holding a
// PalmDOC header, a MOBI header, an EXTH block, and the full-title string.
func buildTestMOBI(t *testing.T) string {
	t.Helper()

	exthRec := func(typ uint32, val string) []byte {
		b := make([]byte, 8+len(val))
		binary.BigEndian.PutUint32(b[0:4], typ)
		binary.BigEndian.PutUint32(b[4:8], uint32(8+len(val)))
		copy(b[8:], val)
		return b
	}
	var recs []byte
	recs = append(recs, exthRec(100, "A H Lee")...)          // author
	recs = append(recs, exthRec(503, "The Sea: A Novel")...) // updated title
	recs = append(recs, exthRec(104, "9780306406157")...)    // ISBN
	exth := []byte("EXTH")
	exth = binary.BigEndian.AppendUint32(exth, uint32(12+len(recs))) // block length
	exth = binary.BigEndian.AppendUint32(exth, 3)                    // record count
	exth = append(exth, recs...)

	const mobiHeaderLen = 232
	mobi := make([]byte, mobiHeaderLen)
	copy(mobi[0:4], "MOBI")
	binary.BigEndian.PutUint32(mobi[4:8], mobiHeaderLen)
	binary.BigEndian.PutUint32(mobi[12:16], 65001) // text encoding: UTF-8

	fullName := []byte("The Sea")
	nameOff := 16 + mobiHeaderLen + len(exth) // relative to record 0 start
	binary.BigEndian.PutUint32(mobi[0x44:0x48], uint32(nameOff))
	binary.BigEndian.PutUint32(mobi[0x48:0x4C], uint32(len(fullName)))

	var rec0 []byte
	rec0 = append(rec0, make([]byte, 16)...) // PalmDOC header (zeroed)
	rec0 = append(rec0, mobi...)
	rec0 = append(rec0, exth...)
	rec0 = append(rec0, fullName...)

	const rec0Off = 86 // 78-byte PDB header + one 8-byte record-info entry
	pdb := make([]byte, rec0Off)
	copy(pdb[0:32], "The Sea") // PDB database name (last-resort title)
	copy(pdb[60:64], "BOOK")
	copy(pdb[64:68], "MOBI")
	binary.BigEndian.PutUint16(pdb[76:78], 1)       // record count
	binary.BigEndian.PutUint32(pdb[78:82], rec0Off) // record 0 offset

	path := filepath.Join(t.TempDir(), "book.mobi")
	if err := os.WriteFile(path, slices.Concat(pdb, rec0), 0o644); err != nil {
		t.Fatalf("write test mobi: %v", err)
	}
	return path
}

func TestParseEbookMOBIEXTH(t *testing.T) {
	got, err := parseEbookFile(buildTestMOBI(t))
	if err != nil {
		t.Fatalf("parseEbookFile error = %v", err)
	}
	if got.Format != "mobi" {
		t.Fatalf("Format = %q, want mobi", got.Format)
	}
	if got.Title != "The Sea: A Novel" {
		t.Fatalf("Title = %q, want EXTH updated title", got.Title)
	}
	if len(got.Authors) != 1 || got.Authors[0] != "A H Lee" {
		t.Fatalf("Authors = %v, want [A H Lee]", got.Authors)
	}
	if got.ISBN != "9780306406157" {
		t.Fatalf("ISBN = %q, want 9780306406157", got.ISBN)
	}
}
