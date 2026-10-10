package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type UserLibraryService interface {
	ListUserLibraries(ctx context.Context, userID int, includeHidden bool) ([]handlers.UserLibraryView, error)
}

// UserLibraryListInput is the listUserLibraries request.
type UserLibraryListInput struct {
	IncludeHidden bool `query:"include_hidden" doc:"Also list libraries the profile hid itself (ui.disabled_library_ids), for a screen that shows them again. Without an account, profile, or policy library limit they are listed either way. Navigation should omit it and filter by the setting" example:"false"`
}

type UserLibrary struct {
	ID        ID     `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	SortOrder int    `json:"sort_order"`
	PosterURL string `json:"poster_url,omitempty"`
}

type UserLibraryListOutput struct{ Body Collection[UserLibrary] }
type UserLibraryCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         UserLibraryCapabilitiesOutputBody
}
type UserLibraryCapabilitiesOutputBody struct {
	Capability
	Available             bool `json:"available"`
	SupportsIncludeHidden bool `json:"supports_include_hidden" doc:"Whether listUserLibraries accepts include_hidden" example:"true"`
}

func registerUserLibraries(reg *Registry) {
	operation := func(path, id string) Operation {
		return Operation{Operation: humaOp(http.MethodGet, Prefix+path, id, "libraries", "Discover enabled libraries visible to the account or selected household profile."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}
	}
	Register(reg, operation("/user/libraries/capabilities", "getUserLibraryCapabilities"), func(_ context.Context, _ *CapabilityInput) (*UserLibraryCapabilitiesOutput, error) {
		out := new(UserLibraryCapabilitiesOutput)
		out.Body.Available = reg.deps.UserLibraries != nil
		out.Body.SupportsIncludeHidden = out.Body.Available
		return out, nil
	})
	Register(reg, operation("/user/libraries", "listUserLibraries"), func(ctx context.Context, in *UserLibraryListInput) (*UserLibraryListOutput, error) {
		if reg.deps.UserLibraries == nil {
			return nil, unavailable("user libraries")
		}
		views, err := reg.deps.UserLibraries.ListUserLibraries(ctx, claimsFrom(ctx).UserID, in.IncludeHidden)
		if err != nil {
			return nil, serviceProblem(err)
		}
		items := make([]UserLibrary, 0, len(views))
		for _, view := range views {
			items = append(items, UserLibrary{ID: ID(strconv.Itoa(view.ID)), Name: view.Name, Type: view.Type, SortOrder: view.SortOrder, PosterURL: view.PosterURL})
		}
		return &UserLibraryListOutput{Body: NewCollection(items)}, nil
	})
}

func (c UserLibraryCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
