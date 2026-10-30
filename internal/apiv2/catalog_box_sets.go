package apiv2

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

// Section catalog-box-sets: movie box sets. A library with box sets on
// browses the movies TMDB lists in one collection (a franchise such as Harry
// Potter) as one box_set card under group=box_set; these reads search box
// sets and open one. getCatalogSearchCapabilities.box_sets advertises them
// to viewers, and getLibraryCapabilities.box_sets to administrators.

const (
	groupBoxSet         = "box_set"
	opSearchBoxSets     = "searchCatalogBoxSets"
	opGetCatalogBoxSet  = "getCatalogBoxSet"
	opGetItemBoxSet     = "getCatalogItemBoxSet"
	boxSetSearchDefault = 5
)

// BoxSetService is the slice of *handlers.CatalogHandler the box set reads
// use.
type BoxSetService interface {
	SearchBoxSets(ctx context.Context, v handlers.ItemViewer, query string, libraryID, limit int) ([]handlers.BoxSetView, error)
	BoxSet(ctx context.Context, v handlers.ItemViewer, collectionID int64, libraryID int) (handlers.BoxSetDetailView, error)
	ItemBoxSet(ctx context.Context, v handlers.ItemViewer, contentID string, libraryID int) (*handlers.BoxSetDetailView, error)
}

// CatalogBoxSet is a box set as one viewer sees it in one library.
type CatalogBoxSet struct {
	ID                ID     `json:"id" doc:"The TMDB collection ID" example:"1241"`
	LibraryID         ID     `json:"library_id" doc:"The library whose movies make up the box set; pass it to getCatalogBoxSet" example:"1"`
	Title             string `json:"title" doc:"The collection's TMDB name, with a trailing \"Collection\" shown as \"Box Set\"" example:"Harry Potter Box Set"`
	Overview          string `json:"overview,omitempty"`
	PosterURL         string `json:"poster_url,omitempty" format:"uri-reference" doc:"The collection's poster, cached like item artwork (presigned, short-lived), or TMDB's URL while it is not cached; absent when TMDB has none"`
	PosterThumbhash   string `json:"poster_thumbhash,omitempty"`
	BackdropURL       string `json:"backdrop_url,omitempty" format:"uri-reference" doc:"The collection's backdrop, cached like item artwork (presigned, short-lived), or TMDB's URL while it is not cached; absent when TMDB has none"`
	BackdropThumbhash string `json:"backdrop_thumbhash,omitempty"`
	MovieCount        int    `json:"movie_count" doc:"The collection's movies in the library that the viewer can see" example:"8"`
	PlayedCount       int    `json:"played_count" doc:"How many of those movies the profile has played" example:"5"`
	FirstYear         int    `json:"first_year,omitempty" doc:"Release year of the earliest of those movies" example:"2001"`
	LastYear          int    `json:"last_year,omitempty" doc:"Release year of the latest of those movies" example:"2011"`
}

// CatalogBoxSetDetail is a box set with its movies.
type CatalogBoxSetDetail struct {
	CatalogBoxSet
	Items []CatalogItem `json:"items" doc:"The box set's movies in the library that the viewer can see, in collection (release) order"`
}

// CatalogBoxSetList is the searchCatalogBoxSets response.
type CatalogBoxSetList struct {
	Items []CatalogBoxSet `json:"items" doc:"Matching box sets, best match first; empty, never null"`
}

// CatalogBoxSetSearchInput is the searchCatalogBoxSets query.
type CatalogBoxSetSearchInput struct {
	Q         string `query:"q" required:"true" minLength:"1" maxLength:"200" doc:"Search text; a box set matches when its name contains every word, or when two or more of its movies' titles do" example:"harry potter"`
	LibraryID ID     `query:"library_id" doc:"Only box sets of this library; absent searches every library with box sets in search" example:"1"`
	Limit     int    `query:"limit" minimum:"1" maximum:"20" default:"5" doc:"Most box sets to return; default 5, maximum 20"`
}

// CatalogBoxSetInput names one box set of one library.
type CatalogBoxSetInput struct {
	ID        ID `path:"id" doc:"The TMDB collection ID" example:"1241"`
	LibraryID ID `query:"library_id" required:"true" doc:"The library whose movies make up the box set" example:"1"`
	ArtworkQuery
}

// CatalogItemBoxSetInput names the movie whose box set a page shows.
type CatalogItemBoxSetInput struct {
	ID        string `path:"id" doc:"Content id of the movie" example:"movie:heat-1995"`
	LibraryID ID     `query:"library_id" doc:"The library the movie is being viewed in; only its box set is returned. Absent uses the first of the movie's libraries with box sets on" example:"1"`
	ArtworkQuery
}

// CatalogItemBoxSet is the getCatalogItemBoxSet response.
type CatalogItemBoxSet struct {
	BoxSet *CatalogBoxSetDetail `json:"box_set,omitempty" doc:"The movie's box set with its movies, as getCatalogBoxSet answers it; absent when the movie is in no box set the viewer sees"`
}

// CatalogItemBoxSetOutput is the getCatalogItemBoxSet response.
type CatalogItemBoxSetOutput struct {
	Body CatalogItemBoxSet
}

// CatalogBoxSetListOutput is the searchCatalogBoxSets response.
type CatalogBoxSetListOutput struct {
	Body CatalogBoxSetList
}

// CatalogBoxSetOutput is the getCatalogBoxSet response.
type CatalogBoxSetOutput struct {
	Body CatalogBoxSetDetail
}

func registerCatalogBoxSets(reg *Registry) {
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/box-sets", opSearchBoxSets, "catalog",
		"Search the box sets of libraries with box sets in search, for listing ahead of their movies.")), reg.searchCatalogBoxSets)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/box-sets/{id}", opGetCatalogBoxSet, "catalog",
		"One box set of a library with its movies the viewer can see there, in collection order. 404 when the library has box sets off or the viewer can see too few of its movies.")), reg.getCatalogBoxSet)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/items/{id}/box-set", opGetItemBoxSet, "catalog",
		"The box set a movie's page shows: the movie's box set with its movies the viewer can see, from library_id or the first of the movie's libraries with box sets on. box_set is absent when there is none; 404 only when the item is missing or hidden.")), reg.getCatalogItemBoxSet)
}

func (reg *Registry) boxSets() (BoxSetService, *Problem) {
	if reg.deps.BoxSets == nil || reg.deps.CatalogAccess == nil {
		return nil, unavailable("box sets")
	}
	return reg.deps.BoxSets, nil
}

func (reg *Registry) searchCatalogBoxSets(ctx context.Context, in *CatalogBoxSetSearchInput) (*CatalogBoxSetListOutput, error) {
	svc, p := reg.boxSets()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, imagesize.Request{}, "", "")
	if p != nil {
		return nil, p
	}
	libraryID := 0
	if in.LibraryID != "" {
		if libraryID, p = in.LibraryID.positive("query.library_id"); p != nil {
			return nil, p
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = boxSetSearchDefault
	}
	views, err := svc.SearchBoxSets(ctx, viewer, strings.TrimSpace(in.Q), libraryID, limit)
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := CatalogBoxSetList{Items: make([]CatalogBoxSet, 0, len(views))}
	for _, v := range views {
		out.Items = append(out.Items, catalogBoxSetOf(v))
	}
	return &CatalogBoxSetListOutput{Body: out}, nil
}

func (reg *Registry) getCatalogBoxSet(ctx context.Context, in *CatalogBoxSetInput) (*CatalogBoxSetOutput, error) {
	svc, p := reg.boxSets()
	if p != nil {
		return nil, p
	}
	id, p := in.ID.positive64(locationPathID)
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.Artwork(), in.LibraryID, "")
	if p != nil {
		return nil, p
	}
	libraryID, p := in.LibraryID.positive("query.library_id")
	if p != nil {
		return nil, p
	}
	view, err := svc.BoxSet(ctx, viewer, id, libraryID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &CatalogBoxSetOutput{Body: reg.catalogBoxSetDetailOf(ctx, view)}, nil
}

func (reg *Registry) getCatalogItemBoxSet(ctx context.Context, in *CatalogItemBoxSetInput) (*CatalogItemBoxSetOutput, error) {
	svc, p := reg.boxSets()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.Artwork(), in.LibraryID, "")
	if p != nil {
		return nil, p
	}
	libraryID := 0
	if in.LibraryID != "" {
		if libraryID, p = in.LibraryID.positive("query.library_id"); p != nil {
			return nil, p
		}
	}
	view, err := svc.ItemBoxSet(ctx, viewer, in.ID, libraryID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	if view == nil {
		return &CatalogItemBoxSetOutput{}, nil
	}
	return &CatalogItemBoxSetOutput{Body: CatalogItemBoxSet{BoxSet: new(reg.catalogBoxSetDetailOf(ctx, *view))}}, nil
}

func (reg *Registry) catalogBoxSetDetailOf(ctx context.Context, view handlers.BoxSetDetailView) CatalogBoxSetDetail {
	sel := reg.ratingSelection(ctx)
	out := CatalogBoxSetDetail{CatalogBoxSet: catalogBoxSetOf(view.BoxSetView), Items: make([]CatalogItem, 0, len(view.Items))}
	for _, item := range view.Items {
		out.Items = append(out.Items, catalogItemOfListing(item, sel))
	}
	return out
}

func catalogBoxSetOf(v handlers.BoxSetView) CatalogBoxSet {
	return CatalogBoxSet{
		ID:                ID(strconv.FormatInt(v.CollectionID, 10)),
		LibraryID:         IDFromInt(int64(v.LibraryID)),
		Title:             v.Title,
		Overview:          v.Overview,
		PosterURL:         v.PosterURL,
		PosterThumbhash:   v.PosterThumbhash,
		BackdropURL:       v.BackdropURL,
		BackdropThumbhash: v.BackdropThumbhash,
		MovieCount:        v.MemberCount,
		PlayedCount:       v.PlayedCount,
		FirstYear:         v.FirstYear,
		LastYear:          v.LastYear,
	}
}
