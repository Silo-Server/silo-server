package downloads

import (
	"errors"
	"fmt"
	"testing"
)

func TestSubscriptionMutationsPostgres(t *testing.T) {
	repo := subscriptionMutationTestRepo(t)
	original := &Subscription{ID: "monitor", UserID: 1, ProfileID: "profile", DeviceID: "device", SeriesID: "series", Mode: SubModeAll}
	stored, err := repo.CreateOrGet(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	initial := stored.UpdatedAt
	stale := errors.New("stale validator")
	check := func(row *Subscription) error {
		if !row.UpdatedAt.Equal(initial) {
			return stale
		}
		return nil
	}
	// Two clients edit the same captured version concurrently. Only one mutation
	// can pass validation against the row held by the transaction lock.
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func() {
			<-start
			_, err := repo.Mutate(t.Context(), 1, "profile", "device", "monitor", false, func(row *Subscription) error {
				if err := check(row); err != nil {
					return err
				}
				row.Active = false
				row.MaxStorageBytes = int64(i + 1)
				return nil
			})
			results <- err
		}()
	}
	close(start)
	accepted, rejected := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, stale):
			rejected++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
	current, err := repo.GetByID(t.Context(), "monitor", 1, "profile", "device")
	if err != nil {
		t.Fatal(err)
	}
	if !current.UpdatedAt.After(initial) || current.Active {
		t.Fatalf("%+v", current)
	}
	// Repeating create cannot unpause or reset options after an edit, including
	// when the repeat generated a fresh candidate ID.
	original.ID = "retry"
	original.Mode = SubModeFuture
	replay, err := repo.CreateOrGet(t.Context(), original)
	if err != nil || replay.ID != "monitor" || replay.Active || replay.Mode != SubModeAll || !replay.UpdatedAt.Equal(current.UpdatedAt) {
		t.Fatalf("replay %+v %v", replay, err)
	}
	for _, scope := range []struct {
		user            int
		profile, device string
	}{{2, "profile", "device"}, {1, "other", "device"}, {1, "profile", "other"}} {
		called := false
		_, err := repo.Mutate(t.Context(), scope.user, scope.profile, scope.device, "monitor", true, func(*Subscription) error { called = true; return nil })
		if !errors.Is(err, ErrSubscriptionNotFound) || called {
			t.Fatalf("unauthorized callback %v %v", called, err)
		}
	}
	if _, err := repo.Mutate(t.Context(), 1, "profile", "device", "monitor", true, check); !errors.Is(err, stale) {
		t.Fatal(err)
	}
	if _, err := repo.Mutate(t.Context(), 1, "profile", "device", "monitor", true, func(row *Subscription) error {
		if !row.UpdatedAt.Equal(current.UpdatedAt) {
			return fmt.Errorf("unexpected winner")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetByID(t.Context(), "monitor", 1, "profile", "device"); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatal(err)
	}
}

func TestSubscriptionSharedPatchMerge(t *testing.T) {
	svc := &Service{}
	sub := &Subscription{Mode: SubModeSpecificSeasons, SeasonNumbers: []int{0}, Active: true, MaxStorageBytes: 100}
	sync, err := svc.applySubscriptionPatch(t.Context(), sub, SubscriptionPatch{Active: new(false), SeasonNumbers: new([]int{0, 2})})
	if err != nil || sync || sub.Active || len(sub.SeasonNumbers) != 2 {
		t.Fatalf("%+v %v %v", sub, sync, err)
	}
	sync, err = svc.applySubscriptionPatch(t.Context(), sub, SubscriptionPatch{Active: new(true)})
	if err != nil || !sync {
		t.Fatalf("reactivation %v %v", sync, err)
	}
	sync, err = svc.applySubscriptionPatch(t.Context(), sub, SubscriptionPatch{MaxStorageBytes: new(int64(0))})
	if err != nil || !sync {
		t.Fatalf("unlimited budget %v %v", sync, err)
	}
	if _, err := svc.applySubscriptionPatch(t.Context(), sub, SubscriptionPatch{SeasonNumbers: new([]int{-1})}); !errors.Is(err, ErrInvalidSeasonNumbers) {
		t.Fatal(err)
	}
}
