package downloads

import (
	"errors"
	"testing"
)

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
