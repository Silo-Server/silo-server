package handlers

import (
	"context"
	"errors"
	"testing"
)

func TestAdminSectionSettingsAtomicGuard(t *testing.T) {
	store := &fakeServerSettingsStore{values: map[string]string{SectionsAllowProfileCustomSettingKey: "false", "unrelated": "preserved"}}
	h := &SectionSettingsHandler{Settings: store}
	ctx := context.Background()
	rejected := errors.New("stale")
	err := h.UpdateAdminSectionSettings(ctx, true, func(current bool) error {
		if current {
			t.Fatal("wrong current flag")
		}
		return rejected
	})
	if !errors.Is(err, rejected) || store.setManyCalls != 0 {
		t.Fatal(err, store.setManyCalls)
	}
	err = h.UpdateAdminSectionSettings(ctx, true, func(current bool) error { return nil })
	if err != nil || store.values[SectionsAllowProfileCustomSettingKey] != "true" || store.values["unrelated"] != "preserved" {
		t.Fatal(err, store.values)
	}
	writes := store.setManyCalls
	err = h.UpdateAdminSectionSettings(ctx, true, func(current bool) error {
		if !current {
			t.Fatal("guard outside current snapshot")
		}
		return nil
	})
	if err != nil || store.setManyCalls != writes {
		t.Fatal("unchanged value wrote", err)
	}
	h.Settings = nil
	if h.UpdateAdminSectionSettings(ctx, false, func(bool) error { return nil }) == nil {
		t.Fatal("missing atomic store accepted")
	}
}
