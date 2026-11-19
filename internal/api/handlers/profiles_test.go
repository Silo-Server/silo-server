package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type testProfileUserRepo struct {
	user *models.User
	err  error
}

func (r testProfileUserRepo) GetByID(context.Context, int) (*models.User, error) {
	return r.user, r.err
}

func newProfileTestStore(t *testing.T) userstore.UserStore {
	return newProfileTestStoreWithSeed(t, true)
}

func newProfileTestStoreWithSeed(t *testing.T, seedProfile bool) userstore.UserStore {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if seedProfile {
		if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
			t.Fatalf("create profile: %v", err)
		}
	}

	return store
}

// profileLimitGroups serves one access group's policy for every account.
type profileLimitGroups struct{ policy access.GroupPolicy }

func (g profileLimitGroups) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	policy := g.policy
	return &policy, nil
}

type recordingProfilePurger struct {
	calls []string
}

func (p *recordingProfilePurger) PurgeProfile(_ context.Context, userID int, profileID string) error {
	p.calls = append(p.calls, fmt.Sprintf("%d/%s", userID, profileID))
	return nil
}

type recordingWatchlistRequestWithdrawer struct {
	calls []string
}

func (w *recordingWatchlistRequestWithdrawer) WithdrawProfileWatchlistRequests(_ context.Context, userID int, profileID string) error {
	w.calls = append(w.calls, fmt.Sprintf("%d/%s", userID, profileID))
	return nil
}

type stubPlaybackSessionsLoader struct {
	sessions []playbackSessionRow
	userID   int
	err      error
}

func (s *stubPlaybackSessionsLoader) Load(
	_ context.Context,
	query PlaybackSessionsQuery,
) ([]playbackSessionRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	if query.UserID != s.userID {
		return []playbackSessionRow{}, nil
	}
	return s.sessions, nil
}
