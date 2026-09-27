package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type adminNodeReadRepo struct{ url string }

func (r adminNodeReadRepo) GetByID(_ context.Context, id int) (*nodepool.Node, error) {
	if id != 7 {
		return nil, nodepool.ErrNodeNotFound
	}
	return &nodepool.Node{URL: r.url}, nil
}
func TestAdminNodeSessionsSharedRedisReader(t *testing.T) {
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	defer func() { _ = client.Close() }()
	prefix := "silo:sessions:" + uuid.NewString() + ":"
	nodeURL := "https://" + uuid.NewString() + ".invalid"
	keys := []string{prefix + "selected", prefix + "other", prefix + "broken"}
	defer func() { _ = client.Del(context.WithoutCancel(t.Context()), keys...).Err() }()
	selected, _ := json.Marshal(nodesessions.SessionInfo{SessionID: "selected", NodeURL: nodeURL, AuthUserID: 7, ProfileID: "child"})
	other, _ := json.Marshal(nodesessions.SessionInfo{SessionID: "other", NodeURL: nodeURL + "/other"})
	for i, value := range []string{string(selected), string(other), "{"} {
		if err := client.Set(t.Context(), keys[i], value, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}
	svc := &AdminNodeSessionsService{Redis: client, Nodes: adminNodeReadRepo{url: nodeURL}}
	result, err := svc.Read(t.Context(), 7)
	if err != nil || result.Undecodable < 1 || len(result.Sessions) != 1 || result.Sessions[0].SessionID != "selected" || result.Sessions[0].ProfileID != "child" {
		t.Fatal(result, err)
	}
	_, err = svc.Read(t.Context(), 8)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusNotFound {
		t.Fatal(err)
	}
	if (&AdminNodeSessionsService{}).Available() {
		t.Fatal("unconfigured source available")
	}
}
