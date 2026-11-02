package downloads

import (
	"testing"
)

func subscriptionMutationTestRepo(t *testing.T) *SubscriptionRepository {
	t.Helper()
	repo := statusEventTestRepo(t)
	_, err := repo.pool.Exec(t.Context(), `CREATE TABLE download_subscriptions (
 id text PRIMARY KEY,user_id integer NOT NULL,profile_id text NOT NULL,device_id text NOT NULL,series_id text NOT NULL,
 mode text NOT NULL,season_numbers integer[],target_season integer,delete_watched boolean NOT NULL,max_storage_bytes bigint NOT NULL,
 active boolean NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,quality text NOT NULL DEFAULT 'original',
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
