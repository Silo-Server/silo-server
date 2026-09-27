package apiv2

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeInviteCodes struct {
	writes, reads, before int
	create                models.CreateInviteCodeInput
	update                models.UpdateInviteCodeInput
	additional            int
	err                   error
}

func fixtureInviteCode() *models.InviteCode {
	return &models.InviteCode{ID: 7, Code: "SYNTHETIC", Label: "Fixture", MaxUses: 10, UseCount: 2, CreatedBy: 2, Enabled: true, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}
}
func (f *fakeInviteCodes) ListPage(_ context.Context, before, limit int) ([]*models.InviteCode, bool, error) {
	f.reads++
	f.before = before
	return []*models.InviteCode{fixtureInviteCode()}, before == 0, f.err
}
func (f *fakeInviteCodes) CreateNamed(_ context.Context, in models.CreateInviteCodeInput) (*models.InviteCode, error) {
	f.writes++
	f.create = in
	return fixtureInviteCode(), f.err
}
func (f *fakeInviteCodes) Update(_ context.Context, _ int, in models.UpdateInviteCodeInput) error {
	f.writes++
	f.update = in
	return f.err
}
func (f *fakeInviteCodes) TopUp(_ context.Context, _, additional int) (*models.InviteCode, error) {
	f.writes++
	f.additional = additional
	return fixtureInviteCode(), f.err
}
func (f *fakeInviteCodes) Delete(context.Context, int) error { f.writes++; return f.err }
func TestAdminInviteCodeLifecycle(t *testing.T) {
	f := new(fakeInviteCodes)
	deps := pilotDeps(nil, nil)
	deps.AdminInviteCodes = f
	h := NewHandler(deps)
	path := Prefix + "/admin/invite-codes"
	created := do(t, h, "POST", path, `{"code":"SYNTHETIC","label":"Fixture","max_uses":10}`, actingRequestAdmin)
	var row AdminInviteCode
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if created.Code != 201 || row.ID != "7" || row.CreatedBy != "2" || f.create.CreatedBy != 2 {
		t.Fatal(created.Code, created.Body.String(), f.create)
	}
	updated := do(t, h, "PUT", path+"/7", `{"enabled":false,"max_uses":12}`, actingRequestAdmin)
	if updated.Code != 204 || f.update.Enabled == nil || *f.update.Enabled || f.update.MaxUses == nil || *f.update.MaxUses != 12 {
		t.Fatal(updated.Code, updated.Body.String())
	}
	topped := do(t, h, "POST", path+"/7/top-up", `{"additional_uses":3}`, actingRequestAdmin)
	if topped.Code != 200 || f.additional != 3 {
		t.Fatal(topped.Code, topped.Body.String())
	}
	f.err = auth.ErrInviteCodeConflict
	requireProblem(t, do(t, h, "POST", path, `{"code":"SYNTHETIC","max_uses":10}`, actingRequestAdmin), TypeConflict)
	f.err = auth.ErrInviteCodeNotFound
	requireProblem(t, do(t, h, "DELETE", path+"/7", "", actingRequestAdmin), TypeNotFound)
}
func TestAdminInviteCodeGatesAndCursor(t *testing.T) {
	f := new(fakeInviteCodes)
	deps := pilotDeps(nil, nil)
	deps.AdminInviteCodes = f
	h := NewHandler(deps)
	path := Prefix + "/admin/invite-codes"
	requireProblem(t, do(t, h, "GET", path, "", requestOwner), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	for _, body := range []string{`{"max_uses":1}`, `{"code":"","max_uses":1}`, `{"code":"SYNTHETIC","max_uses":0}`, `{"code":"SYNTHETIC","max_uses":1,"label":null}`} {
		requireProblem(t, do(t, h, "POST", path, body, actingRequestAdmin), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, "PUT", path+"/7", `{"enabled":null}`, actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, "POST", path+"/7/top-up", `{"additional_uses":0}`, actingRequestAdmin), TypeValidationFailed)
	if f.writes != 0 || f.reads != 0 {
		t.Fatal("rejected request reached service")
	}
	first := do(t, h, "GET", path+"?limit=1", "", actingRequestAdmin)
	var page Collection[AdminInviteCode]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if first.Code != 200 || page.Page == nil || page.Page.NextCursor == "" {
		t.Fatal(first.Code, first.Body.String())
	}
	nextPath := path + "?limit=1&cursor=" + url.QueryEscape(page.Page.NextCursor)
	requireProblem(t, do(t, h, "GET", strings.Replace(nextPath, "limit=1", "limit=2", 1), "", actingRequestAdmin), TypeInvalidCursor)
	next := do(t, h, "GET", nextPath, "", actingRequestAdmin)
	if next.Code != 200 || f.before != 7 {
		t.Fatal(next.Code, next.Body.String())
	}
}
