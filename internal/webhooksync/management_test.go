package webhooksync

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManagementPagingDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var userID, otherID int
	for _, target := range []*int{&userID, &otherID} {
		if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "webhook-page-"+uuid.NewString()).Scan(target); err != nil {
			t.Fatal(err)
		}
		defer func(id int) { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", id) }(*target)
	}
	cipher, err := secret.New([]byte("test-management-key-with-enough-entropy-for-cipher"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool, cipher), nil, nil)

	for _, owner := range []int{userID, otherID} {
		if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'management-p','Profile')", owner); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.CreateConnection(ctx, userID, CreateConnectionInput{Provider: ProviderEmby, ServerName: "Test", DefaultProfileID: "missing"}, ""); !errors.Is(err, historyimport.ErrProfileNotFound) {
		t.Fatalf("foreign create profile=%v", err)
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC)
	ids := []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000003"}
	// Unique UUID prefix avoids colliding with other test runs.
	prefix := uuid.NewString()[:24]
	for i := range ids {
		ids[i] = prefix + ids[i][24:]
	}
	for _, id := range ids {
		if _, err := pool.Exec(ctx, "INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret,created_at) VALUES($1,$2,'plex',$3,$4)", id, userID, uuid.NewString(), stamp); err != nil {
			t.Fatal(err)
		}
	}
	foreign := uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret) VALUES($1,$2,'plex',$3)", foreign, otherID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	page, more, err := service.ListConnectionsPage(ctx, userID, nil, 2)
	if err != nil || !more || len(page) != 2 || page[0].ID != ids[2] || page[1].ID != ids[1] {
		t.Fatalf("connections=%+v %v %v", page, more, err)
	}
	key := &PageKey{At: page[1].CreatedAt, ID: page[1].ID}
	if _, err := pool.Exec(ctx, "DELETE FROM webhook_sync_connections WHERE id=$1", ids[2]); err != nil {
		t.Fatal(err)
	}
	newID := uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret,created_at) VALUES($1,$2,'plex',$3,$4)", newID, userID, uuid.NewString(), stamp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	page, more, err = service.ListConnectionsPage(ctx, userID, key, 2)
	if err != nil || more || len(page) != 1 || page[0].ID != ids[0] {
		t.Fatalf("next=%+v %v %v", page, more, err)
	}
	if _, _, err := service.ListEventLogsPage(ctx, userID, foreign, nil, 2); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("foreign events=%v", err)
	}

	if _, err := service.UpdateConnection(ctx, userID, foreign, UpdateConnectionInput{ServerName: new("changed")}); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("foreign update=%v", err)
	}
	if err := service.DeleteConnection(ctx, userID, foreign); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("foreign delete=%v", err)
	}
	if _, err := service.RotateWebhook(ctx, userID, foreign, ""); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("foreign rotation=%v", err)
	}
	if _, err := service.GetProfileMappings(ctx, userID, foreign); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("foreign mappings=%v", err)
	}
	if _, err := service.UpdateProfileMappings(ctx, userID, foreign, UpdateProfileMappingsInput{}); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("foreign mapping update=%v", err)
	}
	eventIDs := make([]int64, 3)
	for i := range eventIDs {
		if err := pool.QueryRow(ctx, "INSERT INTO webhook_sync_event_logs(connection_id,received_at,http_status,outcome,summary) VALUES($1,$2,204,'applied','test') RETURNING id", ids[0], stamp).Scan(&eventIDs[i]); err != nil {
			t.Fatal(err)
		}
	}
	events, more, err := service.ListEventLogsPage(ctx, userID, ids[0], nil, 2)
	if err != nil || !more || len(events) != 2 || events[0].ID != eventIDs[2] {
		t.Fatalf("events=%+v %v %v", events, more, err)
	}
	key = &PageKey{At: events[1].ReceivedAt, ID: strconv.FormatInt(events[1].ID, 10)}
	if _, err := pool.Exec(ctx, "DELETE FROM webhook_sync_event_logs WHERE id=$1", eventIDs[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO webhook_sync_event_logs(connection_id,received_at,http_status,outcome,summary) VALUES($1,$2,204,'applied','new')", ids[0], stamp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	events, more, err = service.ListEventLogsPage(ctx, userID, ids[0], key, 2)
	if err != nil || more || len(events) != 1 || events[0].ID != eventIDs[0] {
		t.Fatalf("next events=%+v %v %v", events, more, err)
	}
	for i := range 205 {
		if _, err := pool.Exec(ctx, "INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret,created_at) VALUES($1,$2,'emby',$3,$4)", uuid.NewString(), userID, uuid.NewString(), stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO webhook_sync_event_logs(connection_id,received_at,http_status,outcome,summary) VALUES($1,$2,204,'applied',$3)", ids[0], stamp, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	events, more, err = service.ListEventLogsPage(ctx, userID, ids[0], nil, math.MaxInt)
	if err != nil || !more || len(events) != 200 {
		t.Fatalf("bounded events=%d %v %v", len(events), more, err)
	}
	page, more, err = service.ListConnectionsPage(ctx, userID, nil, math.MaxInt)
	if err != nil || !more || len(page) != 200 {
		t.Fatalf("bounded connections=%d %v %v", len(page), more, err)
	}

}
