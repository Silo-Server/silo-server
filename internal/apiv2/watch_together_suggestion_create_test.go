package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeSuggestionCreate struct {
	calls                    int
	id, room, profile, proof string
	user                     int
	input                    watchtogether.CreateSuggestionInput
	err                      error
}

func (f *fakeSuggestionCreate) CheckSuggestionRoomProof(room string, user int, profile, proof string) error {
	f.room = room
	f.user = user
	f.profile = profile
	f.proof = proof
	if proof != "proof" {
		return &handlers.APIError{Status: 403, Code: "forbidden", Message: "Invalid proof."}
	}
	return nil
}
func (f *fakeSuggestionCreate) CreateRoomSuggestion(_ context.Context, room, id string, user int, profile string, input watchtogether.CreateSuggestionInput) (string, error) {
	f.calls++
	f.id = id
	f.input = input
	return id, f.err
}
func TestSuggestionCreateV2(t *testing.T) {
	f := new(fakeSuggestionCreate)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherSuggestionCreate = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/suggestions"
	body := `{"suggestion_id":"1c2b9e11-f942-436c-af63-e68bc387681b","content_id":"movie","content_type":"movie","title":"Title","note":"Note"}`
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, body, profileOwner()), TypePermissionDenied)
	headers := profileOwner()
	headers["X-Room-Token"] = "proof"
	for _, invalid := range []string{`{}`, `{"suggestion_id":"bad","content_id":"movie","content_type":"movie","title":"Title"}`} {
		requireProblem(t, do(t, h, http.MethodPost, path, invalid, headers), TypeValidationFailed)
	}
	if f.calls != 0 {
		t.Fatal("invalid dispatch")
	}
	rec := do(t, h, http.MethodPost, path, body, headers)
	if rec.Code != 201 || f.calls != 1 || f.id != "1c2b9e11-f942-436c-af63-e68bc387681b" || f.user != 1 || f.profile != "p-owner" || f.room != "room" || f.input.Note != "Note" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrSuggestionIdentityConflict, 409}, {watchtogether.ErrRoomClosed, 409}, {watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrInvalidSelection, 422}, {watchtogether.ErrSuggestionCreateUnavailable, 503}} {
		f.err = tc.err
		rec = do(t, h, http.MethodPost, path, body, headers)
		if rec.Code != tc.status {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	deps.WatchTogetherSuggestionCreate = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, body, headers), TypeDependencyUnavailable)
}
