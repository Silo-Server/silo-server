package apiv2

import (
	"context"
	"net/http"
	"strings"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

// Section catalog-letters: the letter index behind an alphabetical jump bar.
// It counts a title-ordered query-source browse by initial and reports where
// each letter starts, so a client can seek straight to it in the list
// queryCatalogItems pages, and which letter each stretch of the list is under.

const (
	opQueryCatalogLetters = "queryCatalogLetters"
	// lettersSortTitle and lettersOrderDesc spell the title order the letter
	// index always counts in, in the browse grammar.
	lettersSortTitle = "title"
	lettersOrderDesc = "desc"
)

// CatalogLettersInput is the queryCatalogLetters request.
type CatalogLettersInput struct {
	Body CatalogLettersQuery
}

// CatalogLettersQuery is the queryCatalogLetters body: the scope and rule
// groups of a query-source browse, which is always in title order.
type CatalogLettersQuery struct {
	LibraryID ID                  `json:"library_id,omitempty" doc:"Restrict to one library" example:"1"`
	Type      string              `json:"type,omitempty" doc:"Media scope, as the listCatalogItems type parameter" example:"movie"`
	Match     string              `json:"match,omitempty" enum:"all,any" doc:"How the groups combine; default all"`
	Groups    []CatalogQueryGroup `json:"groups,omitempty" doc:"Rule groups, as on queryCatalogItems; empty matches everything"`
	Order     string              `json:"order,omitempty" enum:"asc,desc" doc:"Title order the positions count in; default asc"`
}

// CatalogLetter is one letter of the index.
type CatalogLetter struct {
	Letter   string `json:"letter" doc:"A to Z, or # for titles that start with a digit, a symbol, or a non-Latin letter. Titles file under their sort title, so \"The Hobbit\" sorted as \"Hobbit, The\" is under H, and accented Latin initials fold to their base letter" example:"H"`
	Count    int    `json:"count" doc:"Titles filed under the letter" example:"42"`
	Position int    `json:"position" doc:"Zero-based list position of the letter's first title in the same query sorted by title in the same order; pass it as seek to queryCatalogItems" example:"318"`
}

// CatalogLetterRun is one block of consecutive titles under one letter.
type CatalogLetterRun struct {
	Letter   string `json:"letter" doc:"The letter every title in the block files under" example:"H"`
	Position int    `json:"position" doc:"Zero-based list position of the block's first title; the block ends where the next one starts, or at total" example:"318"`
}

// CatalogLetters is the queryCatalogLetters response.
type CatalogLetters struct {
	Total   int                `json:"total" doc:"Titles in the whole result: the sum of the counts" example:"1240"`
	Letters []CatalogLetter    `json:"letters" doc:"Letters with at least one title, in list order; empty, never null"`
	Runs    []CatalogLetterRun `json:"runs" doc:"Blocks of consecutive titles under one letter, in list order, covering the whole result; empty, never null. A letter usually has one block, but the database's collation can sort some of its titles apart from the rest (under C, accented initials sort after Z; most collations put non-Latin scripts after the Latin letters), so use these, not letters, to tell which letter a list position is under"`
}

// CatalogLettersOutput is the queryCatalogLetters response.
type CatalogLettersOutput struct {
	Body CatalogLetters
}

func registerCatalogLetters(reg *Registry) {
	op := humaOp(http.MethodPost, Prefix+"/catalog/letters", opQueryCatalogLetters, "catalog",
		"Count a title-ordered catalog query by initial letter, with each letter's list position, for an alphabetical jump bar.")
	op.DefaultStatus = http.StatusOK
	operation := viewerOperation(op)
	operation.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, operation, reg.queryCatalogLetters)
}

func (reg *Registry) queryCatalogLetters(ctx context.Context, in *CatalogLettersInput) (*CatalogLettersOutput, error) {
	svc, p := reg.catalogBrowse()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, imagesize.Request{}, "", "")
	if p != nil {
		return nil, p
	}
	req, p := in.Body.catalogRequest()
	if p != nil {
		return nil, p
	}
	index, err := svc.LetterIndex(ctx, viewer, req)
	if err != nil {
		return nil, catalogProblem(err, "body")
	}
	out := CatalogLetters{
		Letters: make([]CatalogLetter, 0, len(index.Letters)),
		Runs:    make([]CatalogLetterRun, 0, len(index.Runs)),
	}
	for _, b := range index.Letters {
		out.Letters = append(out.Letters, CatalogLetter{Letter: b.Letter, Count: b.Count, Position: b.Position})
		out.Total += b.Count
	}
	for _, run := range index.Runs {
		out.Runs = append(out.Runs, CatalogLetterRun{Letter: run.Letter, Position: run.Position})
	}
	return &CatalogLettersOutput{Body: out}, nil
}

// catalogRequest parses the body through the browse grammar, as a
// query-source browse sorted by title, so its scope and rules mean what they
// mean on queryCatalogItems. Refusals name body locations.
func (q CatalogLettersQuery) catalogRequest() (catalogpkg.CatalogRequest, *Problem) {
	req, p := q.parse()
	if p != nil {
		for i := range p.Errors {
			p.Errors[i].Location = "body." + strings.TrimPrefix(p.Errors[i].Location, "query.")
		}
	}
	return req, p
}

func (q CatalogLettersQuery) parse() (catalogpkg.CatalogRequest, *Problem) {
	sort := lettersSortTitle
	if q.Order == lettersOrderDesc {
		sort = "-" + lettersSortTitle
	}
	browse := CatalogBrowseInput{Source: string(catalogpkg.CatalogSourceQuery), LibraryID: q.LibraryID, Type: q.Type, Match: q.Match, Sort: sort}
	values, p := browse.catalogValues()
	if p != nil {
		return catalogpkg.CatalogRequest{}, p
	}
	req, p := parseCatalogRequest(values)
	if p != nil {
		return catalogpkg.CatalogRequest{}, p
	}
	req.Query.Groups = catalogQueryGroups(q.Groups)
	if err := req.ValidateQueryDefinition(); err != nil {
		return catalogpkg.CatalogRequest{}, NewProblem(TypeValidationFailed, "Invalid structured filters.").
			WithErrors(ProblemError{Location: locationQueryGroups, Code: codeInvalid, Detail: err.Error()})
	}
	return req, nil
}

// catalogQueryGroups converts wire rule groups to the catalog's.
func catalogQueryGroups(groups []CatalogQueryGroup) []catalogpkg.QueryGroup {
	out := make([]catalogpkg.QueryGroup, 0, len(groups))
	for _, g := range groups {
		rules := make([]catalogpkg.QueryRule, 0, len(g.Rules))
		for _, rule := range g.Rules {
			rules = append(rules, catalogpkg.QueryRule{Field: rule.Field, Op: rule.Op, Value: rule.Value})
		}
		out = append(out, catalogpkg.QueryGroup{Match: g.Match, Rules: rules})
	}
	return out
}
