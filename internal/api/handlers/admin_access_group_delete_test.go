package handlers

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/access"
)

// memberMovingTestStore is a guarded group store whose delete moves members,
// recording what the handler asks it to do.
type memberMovingTestStore struct {
	*accessGroupHandlerTestStore
	moved        []int
	deleteErr    error
	plainDeletes int
	tx           *recordingTx
}

// recordingTx is the transaction the fake store hands the handler's
// in-transaction callback. It records each statement; revocation issues only
// Exec, and any other method would panic on the nil embedded interface.
type recordingTx struct {
	pgx.Tx
	statements []string
	args       [][]any
}

func (tx *recordingTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.statements = append(tx.statements, sql)
	tx.args = append(tx.args, args)
	return pgconn.CommandTag{}, nil
}

func (s *memberMovingTestStore) ListPage(context.Context, *access.GroupPageKey, int) ([]access.Group, bool, error) {
	return nil, false, nil
}

func (s *memberMovingTestStore) UpdateConditional(context.Context, int64, access.UpdateGroupInput, access.GroupPrecondition) (*access.Group, error) {
	return nil, errors.New("not used")
}

func (s *memberMovingTestStore) DeleteConditional(context.Context, int64, access.GroupPrecondition) error {
	s.plainDeletes++
	return nil
}

// DeleteMovingMembers runs the handler's callback inside a recording
// transaction, as the real store runs it inside the delete's transaction.
func (s *memberMovingTestStore) DeleteMovingMembers(ctx context.Context, _ int64, _ access.GroupPrecondition, onMoved func(context.Context, pgx.Tx, []int) error) ([]int, error) {
	if s.deleteErr != nil {
		return nil, s.deleteErr
	}
	if len(s.moved) > 0 {
		s.tx = &recordingTx{}
		if err := onMoved(ctx, s.tx, s.moved); err != nil {
			return nil, err
		}
	}
	return s.moved, nil
}

func TestDeleteAdminAccessGroupMovesMembersThroughTheStore(t *testing.T) {
	store := &memberMovingTestStore{accessGroupHandlerTestStore: newAccessGroupHandlerTestStore()}
	handler := NewAccessGroupHandler(store)
	var notified []int
	handler.OnUserSessionsRevoked = func(_ context.Context, userID int) { notified = append(notified, userID) }

	if err := handler.DeleteAdminAccessGroup(t.Context(), 5, access.GroupPrecondition{Any: true}); err != nil {
		t.Fatalf("DeleteAdminAccessGroup() error: %v", err)
	}
	if store.plainDeletes != 0 {
		t.Fatal("used the plain delete instead of moving members")
	}
	if len(notified) != 0 {
		t.Fatalf("notified %v for a group without members", notified)
	}
}

func TestDeleteAdminAccessGroupRevokesAndNotifiesMovedMembers(t *testing.T) {
	store := &memberMovingTestStore{
		accessGroupHandlerTestStore: newAccessGroupHandlerTestStore(),
		moved:                       []int{7, 9},
	}
	handler := NewAccessGroupHandler(store)
	var notified []int
	handler.OnUserSessionsRevoked = func(_ context.Context, userID int) { notified = append(notified, userID) }

	if err := handler.DeleteAdminAccessGroup(t.Context(), 5, access.GroupPrecondition{Any: true}); err != nil {
		t.Fatalf("DeleteAdminAccessGroup() error: %v", err)
	}
	// Each moved member's sign-ins were revoked inside the delete's transaction.
	if store.tx == nil {
		t.Fatal("the handler's callback never ran in the delete's transaction")
	}
	revoked := map[int]bool{}
	for i, sql := range store.tx.statements {
		if strings.Contains(sql, "UPDATE auth_sessions SET revoked_at") {
			// One set-based UPDATE revokes every moved member.
			for _, userID := range store.tx.args[i][0].([]int) {
				revoked[userID] = true
			}
		}
	}
	if !revoked[7] || !revoked[9] || len(revoked) != 2 {
		t.Fatalf("revoked login sessions for %v, want users 7 and 9", revoked)
	}
	if !slices.Equal(notified, []int{7, 9}) {
		t.Fatalf("notified %v, want [7 9]", notified)
	}
}

func TestDeleteAdminAccessGroupKeepsStoreErrors(t *testing.T) {
	store := &memberMovingTestStore{
		accessGroupHandlerTestStore: newAccessGroupHandlerTestStore(),
		deleteErr:                   access.ErrDefaultGroupRequired,
	}
	handler := NewAccessGroupHandler(store)
	notified := false
	handler.OnUserSessionsRevoked = func(context.Context, int) { notified = true }

	err := handler.DeleteAdminAccessGroup(t.Context(), 1, access.GroupPrecondition{Any: true})
	if !errors.Is(err, access.ErrDefaultGroupRequired) {
		t.Fatalf("error = %v, want ErrDefaultGroupRequired", err)
	}
	if notified {
		t.Fatal("notified sessions after a failed delete")
	}
}
