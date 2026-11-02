package handlers

import (
	"context"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestFilePathsFollowActingAdmin: on-disk paths come with admin reach only
// for the acting admin. Another profile on the admin's account (a child
// profile) falls back to an assigned metadata curation grant, which this
// handler cannot find without a user repository, so it sees none.
func TestFilePathsFollowActingAdmin(t *testing.T) {
	h := &ItemsHandler{}
	for _, tc := range []struct {
		name   string
		acting bool
		err    error
		want   bool
	}{
		{name: "acting admin", acting: true, want: true},
		{name: "child profile on the admin account"},
		{name: "decision unavailable", acting: true, err: errors.New("profile store down")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 1, Role: models.RoleAdmin})
			ctx = apimw.SetActingAdminDecider(ctx, func(context.Context, apimw.ActingAdminRequest) (bool, error) {
				return tc.acting, tc.err
			})
			if got := h.canViewFilePaths(ctx); got != tc.want {
				t.Fatalf("canViewFilePaths = %v, want %v", got, tc.want)
			}
		})
	}
}
