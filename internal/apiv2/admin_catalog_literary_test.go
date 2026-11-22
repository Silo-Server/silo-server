package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

type fakeAdminLiterary struct {
	calls, limit, userID int
	source, target, work string
	ids                  []string
	err                  error
}

func (f *fakeAdminLiterary) ListCandidates(_ context.Context, id string, limit int) ([]literaryworks.Candidate, error) {
	f.calls++
	f.source, f.limit = id, limit
	return []literaryworks.Candidate{{SourceContentID: id, TargetContentID: "audio-1", Score: 0.9, LinkSource: "metadata_match"}}, f.err
}
func (f *fakeAdminLiterary) LinkItems(_ context.Context, work string, ids []string) (string, error) {
	f.calls++
	f.work, f.ids = work, ids
	return "work-1", f.err
}
func (f *fakeAdminLiterary) UnlinkItem(_ context.Context, work, id string) error {
	f.calls++
	f.work, f.source = work, id
	return f.err
}
func (f *fakeAdminLiterary) ConfirmMatch(_ context.Context, source, target string, userID int) (string, error) {
	f.calls++
	f.source, f.target, f.userID = source, target, userID
	return "work-1", f.err
}
func (f *fakeAdminLiterary) IgnoreMatch(_ context.Context, source, target string, userID int) error {
	f.calls++
	f.source, f.target, f.userID = source, target, userID
	return f.err
}
