package main

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type pluginTestFolders struct{}

var pluginTestLibraries = []*models.MediaFolder{
	{ID: 1, Name: "Movies", Type: "movies", Enabled: true},
	{ID: 2, Name: "Shows", Type: "series", Enabled: true},
	{ID: 3, Name: "Private", Type: "movies", Enabled: true},
	{ID: 4, Name: "Retired", Type: "movies", Enabled: false},
}

func (pluginTestFolders) List(context.Context) ([]*models.MediaFolder, error) {
	return pluginTestLibraries, nil
}

func (pluginTestFolders) GetEnabled(context.Context) ([]*models.MediaFolder, error) {
	return pluginTestLibraries[:3], nil
}

func (pluginTestFolders) ListByIDs(_ context.Context, ids []int) ([]*models.MediaFolder, error) {
	var out []*models.MediaFolder
	for _, f := range pluginTestLibraries {
		for _, id := range ids {
			if f.ID == id {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

type pluginTestUsers map[int]*models.User

func (u pluginTestUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if user, ok := u[id]; ok {
		return user, nil
	}
	return nil, auth.ErrNotFound
}

// RuntimeHost.ListLibraries lists only what the named account may see; the
// SDK promises that, and a plugin showing "your libraries" relies on it.
func TestPluginLibrarySourceScopesToTheUser(t *testing.T) {
	source := pluginLibrarySource{folders: pluginTestFolders{}, users: pluginTestUsers{
		1: {ID: 1, Role: models.RoleAdmin, Enabled: true},
		2: {ID: 2, Role: models.RoleUser, Enabled: true, LibraryIDs: []int{1, 2}},
		3: {ID: 3, Role: models.RoleUser, Enabled: false},
	}}
	for _, tc := range []struct {
		userID  string
		want    []string
		wantErr bool
	}{
		{userID: "", want: []string{"1", "2", "3", "4"}},
		{userID: "1", want: []string{"1", "2", "3"}},
		{userID: "2", want: []string{"1", "2"}},
		{userID: "3", want: nil},
		{userID: "99", want: nil},
		{userID: "alice", wantErr: true},
	} {
		records, err := source.List(context.Background(), tc.userID)
		if (err != nil) != tc.wantErr {
			t.Fatalf("user %q: err = %v, want error %v", tc.userID, err, tc.wantErr)
		}
		var got []string
		for _, r := range records {
			got = append(got, r.ID)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("user %q: libraries %v, want %v", tc.userID, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("user %q: libraries %v, want %v", tc.userID, got, tc.want)
			}
		}
	}
}
