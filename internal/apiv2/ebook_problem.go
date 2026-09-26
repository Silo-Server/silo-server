package apiv2

import (
	"errors"
	"github.com/Silo-Server/silo-server/internal/api/handlers"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// Ebook services deliberately hide inaccessible or mismatched catalog files.
// Catalog sentinels are not generic APIError values; preserve their 404 meaning
// at each reader transport boundary rather than letting serviceProblem map
// them to an internal error.
func ebookProblem(err error) *Problem {
	if errors.Is(err, catalogpkg.ErrItemNotFound) || errors.Is(err, catalogpkg.ErrEpisodeNotFound) || errors.Is(err, handlers.ErrEbookAnnotationNotFound) {
		return NewProblem(TypeNotFound, "Ebook not found.")
	}
	return catalogActionProblem(err)
}
