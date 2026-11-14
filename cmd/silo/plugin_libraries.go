package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type pluginLibraryFolders interface {
	List(ctx context.Context) ([]*models.MediaFolder, error)
	GetEnabled(ctx context.Context) ([]*models.MediaFolder, error)
	ListByIDs(ctx context.Context, ids []int) ([]*models.MediaFolder, error)
}

type pluginLibraryUsers interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// pluginLibrarySource answers RuntimeHost.ListLibraries. Without a user_id it
// lists every library, as before. With one it lists only the libraries that
// account may see, by the same account policy GET /user/libraries applies
// when no profile is selected, as the SDK documents.
type pluginLibrarySource struct {
	folders pluginLibraryFolders
	users   pluginLibraryUsers
	groups  access.GroupPolicyProvider
}

func (s pluginLibrarySource) List(ctx context.Context, userID string) ([]pluginhost.LibraryRecord, error) {
	if s.folders == nil {
		return nil, nil
	}
	folders, err := s.foldersFor(ctx, strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	out := make([]pluginhost.LibraryRecord, 0, len(folders))
	for _, f := range folders {
		out = append(out, pluginhost.LibraryRecord{
			ID:        strconv.Itoa(f.ID),
			Name:      f.Name,
			MediaType: mapFolderTypeToMediaType(f.Type),
		})
	}
	return out, nil
}

func (s pluginLibrarySource) foldersFor(ctx context.Context, userID string) ([]*models.MediaFolder, error) {
	if userID == "" {
		return s.folders.List(ctx)
	}
	id, err := strconv.Atoi(userID)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("user_id %q is not a Silo user id", userID)
	}
	if s.users == nil {
		// No way to check the account: refuse rather than list everything.
		return nil, errors.New("user-scoped library listing is unavailable")
	}
	user, err := s.users.GetByID(ctx, id)
	if errors.Is(err, auth.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up user: %w", err)
	}
	if !user.Enabled {
		return nil, nil
	}
	policy, err := access.EffectivePolicyForUser(ctx, user, s.groups)
	if err != nil {
		return nil, fmt.Errorf("resolve user library access: %w", err)
	}
	if policy.LibraryIDs == nil {
		return s.folders.GetEnabled(ctx)
	}
	return s.folders.ListByIDs(ctx, policy.LibraryIDs)
}
