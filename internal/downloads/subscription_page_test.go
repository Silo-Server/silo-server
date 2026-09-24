package downloads

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func subscriptionMutationTestRepo(t *testing.T) *SubscriptionRepository {
	t.Helper()
	repo := statusEventTestRepo(t)
	_, err := repo.pool.Exec(t.Context(), `CREATE TABLE download_subscriptions (
 id text PRIMARY KEY,user_id integer NOT NULL,profile_id text NOT NULL,device_id text NOT NULL,series_id text NOT NULL,
 mode text NOT NULL,season_numbers integer[],target_season integer,delete_watched boolean NOT NULL,max_storage_bytes bigint NOT NULL,
 active boolean NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 UNIQUE(user_id,profile_id,device_id,series_id))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.pool.Exec(t.Context(), `CREATE TABLE download_subscription_exclusions (
 subscription_id text NOT NULL REFERENCES download_subscriptions(id) ON DELETE CASCADE,episode_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(subscription_id,episode_id))`)
	if err != nil {
		t.Fatal(err)
	}
	return NewSubscriptionRepository(repo.pool)
}

func TestSubscriptionPagePostgres(t *testing.T) {
	subs := subscriptionMutationTestRepo(t)
	repo := NewRepository(subs.pool)
	svc := &Service{subRepo: subs}
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	for i := range 5 {
		_, err := repo.pool.Exec(t.Context(), `INSERT INTO download_subscriptions VALUES ($1,1,'profile','device',$1,'specific_seasons',ARRAY[0,2],NULL,true,123,false,$2,$2)`, fmt.Sprint(i), at)
		if err != nil {
			t.Fatal(err)
		}
	}
	var after *RegistryPosition
	var ids []string
	for {
		rows, err := svc.ListSubscriptionsPage(t.Context(), 1, "profile", "device", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			ids = append(ids, row.ID)
			if row.Active || !row.DeleteWatched || row.MaxStorageBytes != 123 || len(row.SeasonNumbers) != 2 || row.SeasonNumbers[0] != 0 {
				t.Fatalf("lost options: %+v", row)
			}
		}
		last := rows[len(rows)-1]
		after = &RegistryPosition{CreatedAt: last.CreatedAt, ID: last.ID}
		if len(ids) > 5 {
			t.Fatal("cursor did not advance")
		}
	}
	if fmt.Sprint(ids) != "[4 3 2 1 0]" {
		t.Fatal(ids)
	}
	for _, scope := range []struct {
		user            int
		profile, device string
	}{{2, "profile", "device"}, {1, "other", "device"}, {1, "profile", "other"}} {
		rows, err := svc.ListSubscriptionsPage(t.Context(), scope.user, scope.profile, scope.device, nil, 2)
		if err != nil || len(rows) != 0 {
			t.Fatalf("scope leak: %+v %v", rows, err)
		}
		if _, err := svc.GetSubscription(t.Context(), scope.user, scope.profile, scope.device, "4"); !errors.Is(err, ErrSubscriptionNotFound) {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{0, 102} {
		if _, err := subs.ListPage(t.Context(), 1, "profile", "device", nil, limit); err == nil {
			t.Fatal("unbounded page")
		}
	}
	if _, err := svc.ListSubscriptionsPage(t.Context(), 1, "profile", "", nil, 2); !errors.Is(err, ErrProfileRequired) {
		t.Fatal(err)
	}
	// Mutable options never affect the traversal tuple.
	row, err := svc.GetSubscription(t.Context(), 1, "profile", "device", "4")
	if err != nil {
		t.Fatal(err)
	}
	row.Active = true
	if err := subs.Update(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.ListSubscriptionsPage(t.Context(), 1, "profile", "device", &RegistryPosition{CreatedAt: at, ID: "4"}, 5)
	if err != nil || len(rows) != 4 || rows[0].ID != "3" {
		t.Fatalf("%+v %v", rows, err)
	}
}
