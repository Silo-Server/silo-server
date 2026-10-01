package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherSuggestionCreateService interface {
	CheckSuggestionRoomProof(string, int, string, string) error
	CreateRoomSuggestion(context.Context, string, string, int, string, watchtogether.CreateSuggestionInput) (string, error)
}
type WatchTogetherSuggestionCreateInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
	Body      struct {
		SuggestionID string `json:"suggestion_id" format:"uuid"`
		ContentID    string `json:"content_id" minLength:"1" maxLength:"512"`
		ContentType  string `json:"content_type" enum:"movie,episode"`
		Title        string `json:"title" minLength:"1" maxLength:"4096"`
		Subtitle     string `json:"subtitle,omitempty" maxLength:"4096"`
		PosterURL    string `json:"poster_url,omitempty" maxLength:"8192"`
		Note         string `json:"note,omitempty" maxLength:"8192"`
	}
}
type WatchTogetherSuggestionCreateOutput struct {
	Body struct {
		SuggestionID ID `json:"suggestion_id"`
	}
}

func registerWatchTogetherSuggestionCreate(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms/{room_id}/suggestions", "createWatchTogetherSuggestion", "realtime", "Create a suggestion with a stable caller-selected identity. Exact live replay returns the identity; changed or deleted identities conflict without resurrection or repeat broadcast."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyUniqueConstraint}
	op.DefaultStatus = http.StatusCreated
	op.MaxBodyBytes = 32768
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherSuggestionCreateInput) (*WatchTogetherSuggestionCreateOutput, error) {
		svc := reg.deps.WatchTogetherSuggestionCreate
		if svc == nil {
			return nil, unavailable("suggestion creation")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.CheckSuggestionRoomProof(in.RoomID, user, profile, in.RoomToken); err != nil {
			return nil, serviceProblem(err)
		}
		b := in.Body
		id, err := svc.CreateRoomSuggestion(ctx, in.RoomID, b.SuggestionID, user, profile, watchtogether.CreateSuggestionInput{ContentID: b.ContentID, ContentType: b.ContentType, Title: b.Title, Subtitle: b.Subtitle, PosterURL: b.PosterURL, Note: b.Note})
		if err != nil {
			switch {
			case errors.Is(err, watchtogether.ErrSuggestionIdentityConflict):
				return nil, NewProblem(TypeConflict, "Suggestion identity was already used or deleted.")
			case errors.Is(err, watchtogether.ErrSuggestionCreateUnavailable):
				return nil, unavailable("suggestion creation")
			case errors.Is(err, watchtogether.ErrInvalidSelection):
				return nil, NewProblem(TypeValidationFailed, "Invalid suggestion.")
			default:
				return nil, suggestionProblem(err)
			}
		}
		out := new(WatchTogetherSuggestionCreateOutput)
		out.Body.SuggestionID = ID(id)
		return out, nil
	})
}
