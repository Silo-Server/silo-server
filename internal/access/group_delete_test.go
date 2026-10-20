package access

import (
	"context"
	"errors"
	"testing"
)

func TestGroupDeletionPreservesMembershipDB(t *testing.T) {
	for name, remove := range map[string]func(context.Context, *GroupStore, int64) error{
		"legacy": func(ctx context.Context, s *GroupStore, id int64) error {
			return s.Delete(ctx, id)
		},
		"conditional": func(ctx context.Context, s *GroupStore, id int64) error {
			return s.DeleteConditional(ctx, id, GroupPrecondition{Any: true})
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := guardedGroupStore(t)
			ctx := t.Context()
			destination, err := s.Create(ctx, CreateGroupInput{Name: "Default", IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			source, err := s.Create(ctx, CreateGroupInput{Name: "Source"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.pool.Exec(ctx, `INSERT INTO users(id,access_group_id,access_policy_revision) VALUES (1,$1,7),(2,$1,11),(3,$2,19)`, source.ID, destination.ID); err != nil {
				t.Fatal(err)
			}
			if err := remove(ctx, s, source.ID); err != nil {
				t.Fatal(err)
			}
			for id, wantRevision := range map[int]int64{1: 8, 2: 12, 3: 19} {
				var groupID *int64
				var revision int64
				if err := s.pool.QueryRow(ctx, `SELECT access_group_id,access_policy_revision FROM users WHERE id=$1`, id).Scan(&groupID, &revision); err != nil {
					t.Fatal(err)
				}
				if groupID == nil || *groupID != destination.ID || revision != wantRevision {
					t.Errorf("member %d: group=%v revision=%d, want default %d revision %d", id, groupID, revision, destination.ID, wantRevision)
				}
				policy, err := s.GetPolicyForUser(ctx, id)
				if err != nil || policy == nil || policy.ID != destination.ID || policy.DownloadAllowed || policy.RequestsAllowed {
					t.Errorf("member %d policy=%+v error=%v, want restrictive default", id, policy, err)
				}
			}
			if _, err := s.Get(ctx, source.ID); !errors.Is(err, ErrGroupNotFound) {
				t.Fatalf("deleted group lookup=%v", err)
			}
		})
	}
}

func TestGroupDeletionWithoutDefaultDB(t *testing.T) {
	s := guardedGroupStore(t)
	ctx := t.Context()
	source, err := s.Create(ctx, CreateGroupInput{Name: "Source"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO users(id,access_group_id,access_policy_revision) VALUES (1,$1,7)`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConditional(ctx, source.ID, GroupPrecondition{Any: true}); !errors.Is(err, ErrGroupDestinationRequired) {
		t.Errorf("populated deletion without default=%v, want ErrGroupDestinationRequired", err)
	}
	var groupID *int64
	var revision int64
	if err := s.pool.QueryRow(ctx, `SELECT access_group_id,access_policy_revision FROM users WHERE id=1`).Scan(&groupID, &revision); err != nil {
		t.Fatal(err)
	}
	if groupID == nil || *groupID != source.ID || revision != 7 {
		t.Errorf("failed deletion changed membership: group=%v revision=%d", groupID, revision)
	}
	if _, err := s.Get(ctx, source.ID); err != nil {
		t.Errorf("failed deletion removed source: %v", err)
	}
	// No destination is needed when the source has no members.
	empty, err := s.Create(ctx, CreateGroupInput{Name: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, empty.ID); err != nil {
		t.Fatalf("empty deletion without default=%v", err)
	}
}

func TestGroupDeletionMoveRollsBackDB(t *testing.T) {
	s := guardedGroupStore(t)
	ctx := t.Context()
	if _, err := s.Create(ctx, CreateGroupInput{Name: "Default", IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	source, err := s.Create(ctx, CreateGroupInput{Name: "Source"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO users(id,access_group_id,access_policy_revision) VALUES (1,$1,7)`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `CREATE FUNCTION reject_group_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic delete failure'; END $$; CREATE TRIGGER reject_group_delete BEFORE DELETE ON access_groups FOR EACH ROW EXECUTE FUNCTION reject_group_delete()`); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, source.ID); err == nil {
		t.Fatal("injected deletion failure succeeded")
	}
	var groupID *int64
	var revision int64
	if err := s.pool.QueryRow(ctx, `SELECT access_group_id,access_policy_revision FROM users WHERE id=1`).Scan(&groupID, &revision); err != nil {
		t.Fatal(err)
	}
	if groupID == nil || *groupID != source.ID || revision != 7 {
		t.Fatalf("rollback changed membership: group=%v revision=%d", groupID, revision)
	}
	if _, err := s.Get(ctx, source.ID); err != nil {
		t.Fatalf("rollback removed source: %v", err)
	}
}
