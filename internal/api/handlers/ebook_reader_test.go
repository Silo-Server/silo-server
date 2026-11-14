package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type fakeEbookProgressStore struct {
	progress *EbookReaderProgress
	upserted *EbookReaderProgress
	err      error
}

func (s *fakeEbookProgressStore) Get(
	context.Context,
	int,
	string,
	string,
) (*EbookReaderProgress, error) {
	return s.progress, s.err
}

func (s *fakeEbookProgressStore) Upsert(_ context.Context, progress EbookReaderProgress) error {
	if s.err != nil {
		return s.err
	}
	s.upserted = &progress
	return nil
}

type fakeEbookReaderAnnotationStore struct {
	items    []EbookReaderAnnotation
	existing *EbookReaderAnnotation
	created  *EbookReaderAnnotation
	updated  *EbookReaderAnnotation
	deleted  string
	err      error
}

func (s *fakeEbookReaderAnnotationStore) List(context.Context, int, string, string) ([]EbookReaderAnnotation, error) {
	return s.items, s.err
}

func (s *fakeEbookReaderAnnotationStore) Create(_ context.Context, annotation EbookReaderAnnotation) error {
	if s.err != nil {
		return s.err
	}
	s.created = &annotation
	s.items = append(s.items, annotation)
	return nil
}

// Update models the PG store contract: read the stored row, invoke merge with
// it, and persist the merged result, all as one atomic operation.
func (s *fakeEbookReaderAnnotationStore) Update(
	_ context.Context,
	_ int,
	_ string,
	_ string,
	annotationID string,
	merge func(existing EbookReaderAnnotation) (EbookReaderAnnotation, error),
) (*EbookReaderAnnotation, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.existing == nil || s.existing.ID != annotationID {
		return nil, nil
	}
	existing := *s.existing
	merged, err := merge(existing)
	if err != nil {
		return nil, err
	}
	s.updated = &merged
	stored := merged
	s.existing = &stored
	return &merged, nil
}

func (s *fakeEbookReaderAnnotationStore) Delete(_ context.Context, userID int, profileID, contentID, annotationID string) error {
	if s.err != nil {
		return s.err
	}
	s.deleted = annotationID
	return nil
}

func TestEbookMimeTypeNeverReturnsOctetStreamForAdmittedFiles(t *testing.T) {
	cases := []struct {
		path      string
		container string
		wantMime  string
	}{
		{"/library/book.txt", "epub", "application/epub+zip"},
		{"/library/book", "pdf", "application/pdf"},
		{"/library/book.epub", "pdf", "application/epub+zip"}, // known extension wins
	}
	for _, tc := range cases {
		file := &models.MediaFile{FilePath: tc.path, Container: tc.container, BaseType: "ebook"}
		if !isEbookFile(file) {
			t.Fatalf("isEbookFile(%q, %q) = false, want admitted", tc.path, tc.container)
		}
		if got := ebookMimeType(tc.path, tc.container); got != tc.wantMime {
			t.Fatalf("ebookMimeType(%q, %q) = %q, want %q", tc.path, tc.container, got, tc.wantMime)
		}
	}
}

func TestEbookReaderRecognizesReadestFormats(t *testing.T) {
	cases := map[string]string{
		"book.epub":    "application/epub+zip",
		"book.pdf":     "application/pdf",
		"book.mobi":    "application/x-mobipocket-ebook",
		"book.azw":     "application/vnd.amazon.ebook",
		"book.azw3":    "application/vnd.amazon.mobi8-ebook",
		"book.cbz":     "application/vnd.comicbook+zip",
		"book.cbr":     "application/vnd.comicbook-rar",
		"book.fb2":     "application/x-fictionbook+xml",
		"book.fbz":     "application/x-zip-compressed-fb2",
		"book.fb2.zip": "application/x-zip-compressed-fb2",
		"book.md":      "application/octet-stream",
		"book.unknown": "application/octet-stream",
	}
	rejected := map[string]bool{"book.md": true, "book.unknown": true}

	for name, wantMime := range cases {
		t.Run(name, func(t *testing.T) {
			container := name
			if name == "book.fb2.zip" {
				container = ""
			}
			file := &models.MediaFile{FilePath: "/library/" + name, Container: container}
			if rejected[name] {
				file.BaseType = "ebook"
				if isEbookFile(file) {
					t.Fatalf("%s should not be treated as an ebook reader format", name)
				}
				if got := ebookMimeType(file.FilePath, file.Container); got != wantMime {
					t.Fatalf("ebookMimeType() = %q, want %q", got, wantMime)
				}
				return
			}
			file.BaseType = "ebook"
			if !isEbookFile(file) {
				t.Fatal("expected ebook reader format")
			}
			if got := ebookMimeType(file.FilePath, file.Container); got != wantMime {
				t.Fatalf("ebookMimeType() = %q, want %q", got, wantMime)
			}
		})
	}
}

func existingEbookReaderAnnotation() *EbookReaderAnnotation {
	createdAt := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	return &EbookReaderAnnotation{
		ID:           "ann-1",
		UserID:       1,
		ProfileID:    "profile-1",
		ContentID:    "ebook-1",
		Kind:         "highlight",
		CFIRange:     "epubcfi(/6/4,/1:0,/1:12)",
		SelectedText: "sample text",
		Note:         "original note",
		Style:        "highlight",
		Color:        "#facc15",
		Metadata:     json.RawMessage(`{"chapter":"one"}`),
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
}

func TestInlineContentDispositionProducesParseableHeaders(t *testing.T) {
	cases := map[string]string{
		"book.epub":            "book.epub",
		`bo"ok\.epub`:          `bo"ok\.epub`,
		`trailing-slash\`:      `trailing-slash\`,
		"börk \U0001F4DA.epub": "börk \U0001F4DA.epub",
		"":                     "ebook",
		".":                    "ebook",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := inlineContentDisposition(name)
			mediaType, params, err := mime.ParseMediaType(got)
			if err != nil {
				t.Fatalf("header %q is malformed: %v", got, err)
			}
			if mediaType != "inline" {
				t.Fatalf("media type = %q", mediaType)
			}
			if params["filename"] != want {
				t.Fatalf("filename = %q, want %q", params["filename"], want)
			}
		})
	}
}

func TestEbookReaderFileResolutionHidesNonReadableFiles(t *testing.T) {
	ebook := &models.MediaFile{ID: 42, ContentID: "ebook-1", FilePath: "/tmp/book.epub", Container: "epub", BaseType: "ebook"}
	cases := []struct {
		name      string
		resolver  stubMediaFileResolver
		contentID string
	}{
		{"missing file", stubMediaFileResolver{err: scanner.ErrFileNotFound}, "ebook-1"},
		{"non-ebook file", stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: "/tmp/movie.mkv", Container: "mkv", BaseType: "movie"}}, "movie-1"},
		{"file of another item", stubMediaFileResolver{file: ebook}, "ebook-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewEbookReaderHandler(&MediaFileAuthorizer{FileResolver: tc.resolver, ItemAccess: stubItemAccessChecker{}})
			if _, err := h.ResolveReaderFile(context.Background(), tc.contentID, 42, catalog.AccessFilter{}); !errors.Is(err, catalog.ErrItemNotFound) {
				t.Fatalf("ResolveReaderFile() error = %v, want ErrItemNotFound", err)
			}
		})
	}
}

func TestEbookReaderAnnotationPatchMergesOntoStoredState(t *testing.T) {
	cases := []struct {
		name    string
		patch   string
		invalid bool
		check   func(t *testing.T, original, updated *EbookReaderAnnotation)
	}{
		{name: "absent fields keep stored values", patch: `{"note":"updated","color":"#bfdbfe"}`, check: func(t *testing.T, original, updated *EbookReaderAnnotation) {
			if updated.Note != "updated" || updated.Color != "#bfdbfe" {
				t.Fatalf("patched fields not applied: %+v", updated)
			}
			if updated.Kind != original.Kind || updated.CFIRange != original.CFIRange || string(updated.Metadata) != string(original.Metadata) {
				t.Fatalf("absent fields changed: %+v", updated)
			}
			if !updated.UpdatedAt.After(original.UpdatedAt) {
				t.Fatalf("updated_at %v did not advance past %v", updated.UpdatedAt, original.UpdatedAt)
			}
		}},
		{name: "explicitly empty fields clear", patch: `{"note":"","metadata":{}}`, check: func(t *testing.T, _, updated *EbookReaderAnnotation) {
			if updated.Note != "" || string(updated.Metadata) != `{}` {
				t.Fatalf("note/metadata not cleared: %+v", updated)
			}
		}},
		{name: "kind change without location", patch: `{"kind":"bookmark"}`, invalid: true},
		{name: "clearing required cfi_range", patch: `{"cfi_range":""}`, invalid: true},
		{name: "unknown kind", patch: `{"kind":"doodle"}`, invalid: true},
		{name: "non-object metadata", patch: `{"metadata":["bad"]}`, invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := existingEbookReaderAnnotation()
			store := &fakeEbookReaderAnnotationStore{existing: existingEbookReaderAnnotation()}
			h := NewEbookReaderHandler(&MediaFileAuthorizer{FileResolver: stubMediaFileResolver{}, ItemAccess: stubItemAccessChecker{}})
			h.AnnotationStore = store
			var patch EbookAnnotationPatch
			if err := json.Unmarshal([]byte(tc.patch), &patch); err != nil {
				t.Fatalf("decode patch: %v", err)
			}
			scope := EbookAnnotationScope{UserID: 1, ProfileID: "profile-1", ContentID: "ebook-1"}
			updated, err := h.UpdateReaderAnnotation(context.Background(), scope, "ann-1", patch, nil)
			if tc.invalid {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
					t.Fatalf("UpdateReaderAnnotation() error = %v, want 400", err)
				}
				if store.updated != nil {
					t.Fatalf("invalid patch reached the store: %+v", store.updated)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateReaderAnnotation() error = %v", err)
			}
			tc.check(t, original, updated)
		})
	}
}
