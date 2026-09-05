package apiv2

import (
	"context"
	"net/http"
)

// The viewer-facing library list: the enabled libraries the acting principal
// may browse, with the simplified fields a client shows (no paths or scan
// state; those are the admin library operations).

// UserLibrary is one library the caller may browse.
type UserLibrary struct {
	ID        ID     `json:"id" doc:"Library identifier" example:"1"`
	Name      string `json:"name" example:"Movies"`
	Type      string `json:"type" doc:"Library kind as stored (movies, series, ...)" example:"movies"`
	SortOrder int    `json:"sort_order" doc:"Administrator-chosen display order" example:"0"`
	PosterURL string `json:"poster_url,omitempty" doc:"Short-lived signed URL of the library poster; absent when none is set" example:"https://s3.example.test/silo/posters/1.jpg?X-Amz-Signature=..."`
}

// UserLibraryCollection is the listUserLibraries response: the bounded set
// of libraries, not paginated.
type UserLibraryCollection struct {
	Collection[UserLibrary]
}

// UserLibraryCollectionOutput is the listUserLibraries response.
type UserLibraryCollectionOutput struct {
	Body UserLibraryCollection
}

func registerUserLibraries(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/user/libraries", "listUserLibraries", "libraries",
			"List the libraries the acting principal may browse."),
		// Profile scoped without a required header, as v1: with X-Profile-Id
		// the profile's allowlist applies, without it the account's
		// effective policy does.
		Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true,
	}, reg.listUserLibraries)
}

// listUserLibraries answers from the same access-filtered listing v1 GET
// /user/libraries uses.
func (reg *Registry) listUserLibraries(ctx context.Context, _ *struct{}) (*UserLibraryCollectionOutput, error) {
	if reg.deps.UserLibraries == nil {
		return nil, unavailable("library")
	}
	if claimsFrom(ctx) == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	libraries, err := reg.deps.UserLibraries.ListUserLibraries(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]UserLibrary, 0, len(libraries))
	for _, l := range libraries {
		items = append(items, UserLibrary{ID: IDFromInt(int64(l.ID)), Name: l.Name, Type: l.Type, SortOrder: l.SortOrder, PosterURL: l.PosterURL})
	}
	return &UserLibraryCollectionOutput{Body: UserLibraryCollection{NewCollection(items)}}, nil
}
