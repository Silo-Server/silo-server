package jellycompat

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
)

//nolint:nestif // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestRecipePublicationCanReplacePreReceiptVersion(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "sql-rejected"}[rejected], func(t *testing.T) {
			pool, client, recipes, state := recipeDurableFixture(t)
			work, card := recipeFixtureSession(t, state, recipes, true)
			_, version, err := recipes.RecipeVersion(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			// Exact old PutTTL shape: versioned record and authority stamps,
			// no publication receipt, and no accepted publication ID in SQL.
			if err = client.Del(t.Context(), "silo:noderecipe-undo:"+version).Err(); err != nil {
				t.Fatal(err)
			}
			if err = state.Update(work, func(s *PlaybackSession) error { s.RecipePublicationID = ""; return nil }); err != nil {
				t.Fatal(err)
			}
			keys := []string{noderecipe.KeyPrefix + work, "silo:noderecipe-authority-record:" + work, "silo:noderecipe-authority-digest:" + work}
			before, err := client.MGet(t.Context(), keys...).Result()
			if err != nil {
				t.Fatal(err)
			}
			expires := make([]time.Duration, len(keys))
			for i, key := range keys {
				expires[i], err = client.PExpireTime(t.Context(), key).Result()
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := recipes.Get(t.Context(), work); !ok {
				t.Fatal("legacy authority was not valid before replacement")
			}
			if rejected {
				if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_legacy_candidate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data->'Recipe'->>'input_path'='/approved/replacement' THEN RAISE EXCEPTION 'injected legacy replacement rejection'; END IF;RETURN NEW;END $$; CREATE TRIGGER reject_legacy_candidate BEFORE UPDATE ON jellycompat_playback_sessions FOR EACH ROW EXECUTE FUNCTION reject_legacy_candidate()`); err != nil {
					t.Fatal(err)
				}
			}
			card.InputPath = "/approved/replacement"
			err = state.PublishRecipe(t.Context(), work, work, card, recipes)
			if rejected {
				if err == nil || errors.Is(err, noderecipe.ErrPublicationUncertain) {
					t.Fatal("legacy replacement did not reach definite SQL rejection", err)
				}
				after, readErr := client.MGet(t.Context(), keys...).Result()
				if readErr != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("SQL rejection did not restore exact legacy authority", readErr)
				}
				for i, key := range keys {
					expiry, readErr := client.PExpireTime(t.Context(), key).Result()
					if readErr != nil || expiry != expires[i] {
						t.Fatal("SQL rejection extended prior authority expiry", key, readErr)
					}
				}
				row := readRecipeRow(t, pool, work)
				if row.Recipe == nil || row.Recipe.InputPath != "/approved/prior" || row.RecipePublicationID != "" {
					t.Fatal("SQL rejection changed accepted legacy row")
				}
				return
			}
			if err != nil {
				t.Fatal("exact locked legacy authority could not be replaced", err)
			}
			row := readRecipeRow(t, pool, work)
			if row.Recipe == nil || row.Recipe.InputPath != card.InputPath || row.RecipePublicationID == "" {
				t.Fatal("replacement did not commit accepted publication")
			}
			got, ok := recipes.Get(t.Context(), work)
			if !ok || got.InputPath != card.InputPath {
				t.Fatal("replacement did not commit Redis recipe")
			}
		})
	}
}

func TestRecipePublicationLegacyRebuildUsesFreshRow(t *testing.T) {
	pool, client, recipes, state := recipeDurableFixture(t)
	work, card := recipeFixtureSession(t, state, recipes, true)
	_, version, err := recipes.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Del(t.Context(), "silo:noderecipe-undo:"+version).Err(); err != nil {
		t.Fatal(err)
	}
	if err = state.Update(work, func(s *PlaybackSession) error { s.RecipePublicationID = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	// Another replica changed durable acceptance; the local cache still carries
	// the old exact Redis bytes and must not authorize baseline reconstruction.
	if _, err = pool.Exec(t.Context(), `UPDATE jellycompat_playback_sessions SET data=jsonb_set(data,'{Recipe,input_path}','"/approved/newer-durable"'::jsonb) WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	card.InputPath = "/approved/unaccepted"
	if err = state.PublishRecipe(t.Context(), work, work, card, recipes); !errors.Is(err, noderecipe.ErrPublicationUncertain) {
		t.Fatal("stale cached recipe authorized legacy reconstruction", err)
	}
	got, current, err := recipes.RecipeVersion(t.Context(), work)
	if err != nil || got == nil || got.InputPath != "/approved/prior" || current != version {
		t.Fatal("unproven legacy authority was changed", err)
	}
	if readRecipeRow(t, pool, work).Recipe.InputPath != "/approved/newer-durable" {
		t.Fatal("unaccepted replacement changed durable recipe")
	}
	if markers, err := client.Exists(t.Context(), "silo:noderecipe-undo:"+version).Result(); err != nil || markers != 0 {
		t.Fatal("stale cached acceptance finalized the old token", markers, err)
	}
}
