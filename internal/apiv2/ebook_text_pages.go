package apiv2

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/ebookrender"
)

// Bound extraction across all routers in this process, including archive and page
// allocations. Saturated requests fail immediately instead of queuing work.
var ebookTextJobs = make(chan struct{}, 2)

type EbookTextPageInput struct {
	ContentID string `path:"content_id" minLength:"1"`
	FileID    ID     `path:"file_id"`
	PageIndex int    `path:"page_index" minimum:"0"`
}

type EbookTextPage struct {
	Revision     string `json:"revision"`
	Location     string `json:"location" doc:"Opaque normalized text location; not EPUB CFI and must not replace browser reader progress."`
	Text         string `json:"text" doc:"At most 2048 Unicode scalar values; not a measured visual page."`
	PageIndex    int    `json:"page_index"`
	TotalPages   int    `json:"total_pages"`
	PreviousPage *int   `json:"previous_page"`
	NextPage     *int   `json:"next_page"`
}

type EbookTextPageOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         EbookTextPage
}

func registerEbookTextPages(reg *Registry) {
	op := humaOp(http.MethodGet, Prefix+"/ebooks/{content_id}/files/{file_id}/text-pages/{page_index}", "getEbookTextPage", "ebooks", "Extract one bounded plain text chunk from an authorized reflowable EPUB. No progress is saved.")
	op.Errors = append(op.Errors, http.StatusRequestTimeout, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType)
	Register(reg, Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}, reg.getEbookTextPage)
}

func (reg *Registry) getEbookTextPage(ctx context.Context, in *EbookTextPageInput) (*EbookTextPageOutput, error) {
	if reg.deps.EbookFiles == nil {
		return nil, unavailable("ebook text pages")
	}
	fileID, p := in.FileID.positive("path.file_id")
	if p != nil {
		return nil, p
	}
	file, err := reg.deps.EbookFiles.ResolveReaderFile(ctx, in.ContentID, fileID, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, ebookProblem(err)
	}
	if file == nil {
		return nil, NewProblem(TypeNotFound, "Ebook not found.")
	}
	if !strings.EqualFold(file.Container, "epub") {
		return nil, NewProblem(TypeUnsupportedMediaType, "Text pages require an EPUB file.")
	}
	select {
	case ebookTextJobs <- struct{}{}:
		defer func() { <-ebookTextJobs }()
	default:
		return nil, NewProblem(TypeDependencyUnavailable, "Ebook text extraction is busy; retry later.")
	}
	// Reject special files before opening. Nonblocking open and the final f.Stat
	// also reject a FIFO substituted between this check and open.
	info, err := os.Stat(file.FilePath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, NewProblem(TypeDependencyUnavailable, "Ebook file is unavailable.")
	}
	f, err := os.OpenFile(file.FilePath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, NewProblem(TypeDependencyUnavailable, "Ebook file is unavailable.")
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, NewProblem(TypeDependencyUnavailable, "Ebook file is unavailable.")
	}
	if info.Size() > ebookrender.MaxArchiveBytes {
		return nil, NewProblem(TypePayloadTooLarge, "EPUB exceeds text extraction limits.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	book, err := ebookrender.Parse(ctx, f, info.Size())
	if err != nil {
		return nil, ebookTextProblem(err)
	}
	pages, err := book.Pages(2048)
	if err != nil {
		return nil, ebookTextProblem(err)
	}
	if in.PageIndex < 0 || in.PageIndex >= len(pages) {
		return nil, NewProblem(TypeNotFound, "Text page not found.")
	}
	page := pages[in.PageIndex]
	out := &EbookTextPageOutput{CacheControl: "private, no-store", Body: EbookTextPage{Revision: book.Revision, Location: page.Location, Text: page.Text, PageIndex: in.PageIndex, TotalPages: len(pages)}}
	if in.PageIndex > 0 {
		out.Body.PreviousPage = new(in.PageIndex - 1)
	}
	if in.PageIndex+1 < len(pages) {
		out.Body.NextPage = new(in.PageIndex + 1)
	}
	return out, nil
}

func ebookTextProblem(err error) *Problem {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return NewProblem(TypeRequestTimeout, "EPUB text extraction timed out.")
	case errors.Is(err, ebookrender.ErrLimit):
		return NewProblem(TypePayloadTooLarge, "EPUB exceeds text extraction limits.")
	case errors.Is(err, ebookrender.ErrUnsupported):
		return NewProblem(TypeUnsupportedMediaType, "EPUB is outside the supported reflowable subset.")
	default:
		return NewProblem(TypeValidationFailed, "EPUB text could not be extracted.")
	}
}
