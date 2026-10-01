package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type orderedAutoscanReschedule struct {
	persisted *bool
	calls     int
	err       error
}

func (f *orderedAutoscanReschedule) UpdateTriggers(_ string, configs []taskmanager.TriggerConfig) error {
	if !*f.persisted {
		panic("reschedule before persistence")
	}
	f.calls++
	if len(configs) != 1 || configs[0].IntervalMs != 120000 {
		panic("wrong interval")
	}
	return f.err
}
func TestUpdateAdminAutoscanSettings(t *testing.T) {
	persisted := false
	var storeError error
	store := &fakeAutoscanStore{updateSettingsFn: func(s autoscan.Settings) (autoscan.Settings, error) {
		persisted = storeError == nil
		return s, storeError
	}}
	h := NewAutoscanHandler(store, new(fakeAutoscanTriggerer))
	input := autoscan.Settings{Enabled: true, DefaultPollIntervalSeconds: 120, DebounceSeconds: 0}
	out, err := h.UpdateAdminAutoscanSettings(t.Context(), input)
	if err != nil || out.RescheduleState != "not_configured" {
		t.Fatal(out, err)
	}
	trigger := &orderedAutoscanReschedule{persisted: &persisted}
	h.triggers = trigger
	out, err = h.UpdateAdminAutoscanSettings(t.Context(), input)
	if err != nil || out.RescheduleState != "applied" || trigger.calls != 1 {
		t.Fatal(out, err)
	}
	trigger.err = errors.New("private-rescheduler")
	out, err = h.UpdateAdminAutoscanSettings(t.Context(), input)
	if err != nil || out.RescheduleState != "failed" || out.Settings != input {
		t.Fatal(out, err)
	}
	storeError = errors.New("store failed")
	before := trigger.calls
	_, err = h.UpdateAdminAutoscanSettings(t.Context(), input)
	if !errors.Is(err, storeError) || trigger.calls != before {
		t.Fatal(err)
	}
	input.DefaultPollIntervalSeconds = 0
	_, err = h.UpdateAdminAutoscanSettings(t.Context(), input)
	if !errors.Is(err, ErrAdminAutoscanSettingsWriteInvalid) {
		t.Fatal(err)
	}
}
