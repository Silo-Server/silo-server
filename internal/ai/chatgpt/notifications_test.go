package chatgpt

import (
	"context"
	"maps"
	"testing"
)

type cancelAfterCommitStore struct {
	*memoryStore
	cancel context.CancelFunc
}

func (s cancelAfterCommitStore) UpdateValueAndSettingsAtomic(ctx context.Context, key string, update func(map[string]string) (map[string]string, error)) error {
	err := s.memoryStore.UpdateValueAndSettingsAtomic(ctx, key, update)
	if err == nil {
		s.cancel()
	}
	return err
}

func TestAutomaticModelNotificationsFollowCommittedChanges(t *testing.T) {
	for _, outcome := range []string{"changed", "unchanged", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "old", "ai.reasoning_effort": "high"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s.store = cancelAfterCommitStore{memoryStore: store, cancel: cancel}
			store.failNext = outcome == "failed"
			notifications := map[string]string{}
			s.OnSettingUpdated = func(notifyCtx context.Context, key, value string) {
				if notifyCtx.Err() != nil {
					t.Fatal("committed change notification inherited request cancellation")
				}
				if _, ok := notifyCtx.Deadline(); !ok {
					t.Fatal("notification context has no deadline")
				}
				values, err := store.GetMany(notifyCtx, key)
				if err != nil || values[key] != value {
					t.Fatal("notification preceded commit or ran inside the store lock")
				}
				notifications[key] = value
			}
			_, err := s.mutateSettingsWithSnapshot(ctx, func(_ *credentials, values map[string]string) error {
				if outcome != "unchanged" {
					values[chatModelSetting], values[reasoningEffortSetting] = "gpt-4o-mini", ""
				}
				return nil
			})
			if (err != nil) != (outcome == "failed") {
				t.Fatalf("unexpected commit result: %v", err)
			}
			want := map[string]string{}
			if outcome == "changed" {
				want = map[string]string{chatModelSetting: "gpt-4o-mini", reasoningEffortSetting: ""}
			}
			if !maps.Equal(notifications, want) {
				t.Fatalf("notifications=%v, want %v", notifications, want)
			}
		})
	}
}
