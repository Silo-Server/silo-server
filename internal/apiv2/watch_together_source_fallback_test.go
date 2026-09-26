package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeSourceFallback struct {
	fakeRoomSelection
	proof   string
	request watchtogether.SourceFallbackInput
}

func (f *fakeSourceFallback) FallbackWatchTogetherSource(ctx context.Context, room string, user int, profile, proof string, input watchtogether.SourceFallbackInput) (watchtogether.Snapshot, string, error) {
	f.proof, f.request = proof, input
	return f.SelectWatchTogetherItem(ctx, room, user, profile, watchtogether.SelectItemInput{})
}

func TestWatchTogetherSourceFallback(t *testing.T) {
	f := new(fakeSourceFallback)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherSourceFallback = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/source-fallback"
	body := `{"selection_revision":3,"failed_file_id":"7","reason":"no_alternate_version"}`
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
	for _, invalid := range []string{`{}`, `{"selection_revision":0,"failed_file_id":"7","reason":"no_alternate_version"}`, `{"selection_revision":1,"failed_file_id":"0","reason":"no_alternate_version"}`, `{"selection_revision":1,"failed_file_id":7,"reason":"no_alternate_version"}`, `{"selection_revision":1,"failed_file_id":"7","reason":"network_error"}`} {
		requireProblem(t, do(t, h, http.MethodPost, path, invalid, profileOwner()), TypeValidationFailed)
	}
	if f.calls != 0 {
		t.Fatal("invalid fallback reached service")
	}
	headers := profileOwner()
	headers["X-Room-Token"] = "proof"
	response := do(t, h, http.MethodPost, path, body, headers)
	if response.Code != http.StatusOK || f.proof != "proof" || f.request.SelectionRevision != 3 || f.request.FailedFileID != 7 || f.request.Reason != "no_alternate_version" {
		t.Fatalf("response = %d %s, request = %+v", response.Code, response.Body.String(), f)
	}
	for _, test := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomForbidden, 403}, {watchtogether.ErrRoomClosed, 409}, {watchtogether.ErrSourceFallbackUnavailable, 409}, {watchtogether.ErrInvalidSelection, 422}} {
		f.err = test.err
		response = do(t, h, http.MethodPost, path, body, headers)
		if response.Code != test.status {
			t.Fatalf("error %v returned %d", test.err, response.Code)
		}
	}
}
