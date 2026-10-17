package nodesessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReplaceSnapshotPreservesReplacementOwner(t *testing.T) {
	for _, replacement := range []string{"http-worker", "legacy-track", "legacy-touch"} {
		t.Run(replacement, func(t *testing.T) {
			rdb := redisTestClient(t)
			id := uuid.NewString()
			tracker := NewTracker(rdb, "https://"+id+".example.test", "", "transcode")
			info := SessionInfo{SessionID: id, NodeURL: tracker.NodeURL(), Type: "transcode"}
			fence := SnapshotFence{Key: "test:session-lease:" + id, Value: "owner"}
			publisher := "publisher-" + id
			other := "successor-" + id
			t.Cleanup(func() {
				_ = rdb.Del(context.Background(), fence.Key, snapshotKeyPrefix+publisher, snapshotKeyPrefix+other, tracker.redisKey(id), sessionOwnerPrefix+tracker.nodeHash+":"+id).Err()
			})
			if err := rdb.Set(t.Context(), fence.Key, fence.Value, time.Minute).Err(); err != nil {
				t.Fatal(err)
			}
			if err := tracker.ReplaceSnapshot(t.Context(), publisher, fence, []SessionInfo{info}); err != nil {
				t.Fatal(err)
			}
			fresh := NewTracker(rdb, tracker.NodeURL(), "", "transcode")
			switch replacement {
			case "http-worker":
				if err := fresh.ReplaceSnapshot(t.Context(), other, fence, []SessionInfo{info}); err != nil {
					t.Fatal(err)
				}
			case "legacy-track":
				fresh.Track(t.Context(), info)
			case "legacy-touch":
				fresh.Touch(t.Context(), info)
			}
			if err := tracker.ReplaceSnapshot(t.Context(), publisher, fence, nil); err != nil {
				t.Fatal(err)
			}
			if count, err := rdb.Exists(t.Context(), tracker.redisKey(id)).Result(); err != nil || count != 1 {
				t.Fatalf("snapshot removal deleted a replacement record: count=%d error=%v", count, err)
			}
		})
	}
}

func TestReplaceSnapshotURLChangeRemovesOnlyOwnedRecords(t *testing.T) {
	rdb := redisTestClient(t)
	id := uuid.NewString()
	old := NewTracker(rdb, "https://old-"+id+".example.test", "", "")
	next := NewTracker(rdb, "https://new-"+id+".example.test", "", "")
	fence := SnapshotFence{Key: "test:session-lease:" + id, Value: "owner"}
	publisher := "publisher-" + id
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), fence.Key, snapshotKeyPrefix+publisher, old.redisKey(id), next.redisKey(id), sessionOwnerPrefix+old.nodeHash+":"+id, sessionOwnerPrefix+next.nodeHash+":"+id).Err()
	})
	if err := rdb.Set(t.Context(), fence.Key, fence.Value, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := old.ReplaceSnapshot(t.Context(), publisher, fence, []SessionInfo{{SessionID: id, NodeURL: old.NodeURL()}}); err != nil {
		t.Fatal(err)
	}
	if err := next.ReplaceSnapshot(t.Context(), publisher, fence, []SessionInfo{{SessionID: id, NodeURL: next.NodeURL()}}); err != nil {
		t.Fatal(err)
	}
	if count, err := rdb.Exists(t.Context(), old.redisKey(id)).Result(); err != nil || count != 0 {
		t.Fatalf("old URL's owned record survived replacement: count=%d error=%v", count, err)
	}
	if count, err := rdb.Exists(t.Context(), next.redisKey(id)).Result(); err != nil || count != 1 {
		t.Fatalf("new URL's record missing: count=%d error=%v", count, err)
	}
}

func TestReplaceSnapshotRejectsLostLeaseCancellationAndOversizedMembership(t *testing.T) {
	rdb := redisTestClient(t)
	id := uuid.NewString()
	tracker := NewTracker(rdb, "https://"+id+".example.test", "", "")
	fence := SnapshotFence{Key: "test:session-lease:" + id, Value: "owner"}
	publisher := "publisher-" + id
	t.Cleanup(func() { _ = rdb.Del(context.Background(), fence.Key, snapshotKeyPrefix+publisher).Err() })
	if err := tracker.ReplaceSnapshot(t.Context(), publisher, fence, nil); !errors.Is(err, ErrSnapshotFenced) {
		t.Fatalf("missing lease error = %v", err)
	}
	if err := rdb.Set(t.Context(), fence.Key, fence.Value, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := tracker.ReplaceSnapshot(ctx, publisher, fence, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot error = %v", err)
	}
	if err := tracker.ReplaceSnapshot(t.Context(), publisher, fence, make([]SessionInfo, maxSnapshotSessions+1)); err == nil {
		t.Fatal("oversized incoming snapshot accepted")
	}
	members := make([]any, maxSnapshotSessions+1)
	for i := range members {
		members[i] = uuid.NewString()
	}
	if err := rdb.SAdd(t.Context(), snapshotKeyPrefix+publisher, members...).Err(); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ReplaceSnapshot(t.Context(), publisher, fence, nil); err == nil {
		t.Fatal("oversized stored membership accepted")
	}
	if count, err := rdb.SCard(t.Context(), snapshotKeyPrefix+publisher).Result(); err != nil || count != maxSnapshotSessions+1 {
		t.Fatalf("rejected snapshot mutated membership: count=%d error=%v", count, err)
	}
}
