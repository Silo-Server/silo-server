package storetest

import (
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunPreferenceSnapshot commits a writer between reads to prove that overlays
// cannot combine track identity and canonical values from different commits.
func RunPreferenceSnapshot(t *testing.T, newStore func(*testing.T) userstore.UserStore) {
	const snapshotSeries = "snapshot-series"
	ctx := t.Context()
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Main"}); err != nil {
		t.Fatal(err)
	}
	id := userstore.SettingIdentity{Key: "playback.audio_language", Scope: settingscontract.ScopeProfileSeries, ProfileID: "p1", SeriesID: snapshotSeries}
	writer, ok := store.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		t.Fatal("store lacks preference transactions")
	}
	write := func(track int) error {
		return writer.WithPreferenceSettingsTransaction(ctx, func(w userstore.PreferenceSettingsWriter) error {
			if err := w.SetAudioPreference(ctx, userstore.AudioPreference{ProfileID: "p1", SeriesID: snapshotSeries, AudioTrackIndex: track}); err != nil {
				return err
			}
			if err := w.SetSubtitlePreference(ctx, userstore.SubtitlePreference{ProfileID: "p1", SeriesID: snapshotSeries, SubtitleTrackIndex: track}); err != nil {
				return err
			}
			_, err := w.UpsertSettingValue(ctx, id, []byte(fmt.Sprintf(`"language-%d"`, track)))
			return err
		})
	}
	if err := write(1); err != nil {
		t.Fatal(err)
	}
	reader, ok := store.(userstore.PreferenceSettingsSnapshotter)
	if !ok {
		t.Fatal("store lacks preference snapshots")
	}
	check := func(w userstore.PreferenceSettingsReader, want int) error {
		subtitle, err := w.GetSubtitlePreference(ctx, "p1", snapshotSeries)
		if err != nil {
			return err
		}
		row, err := w.GetSettingValue(ctx, id)
		if err != nil {
			return err
		}
		if subtitle.SubtitleTrackIndex != want || string(row.Value) != fmt.Sprintf(`"language-%d"`, want) {
			return fmt.Errorf("torn snapshot: subtitle=%d setting=%s want generation %d", subtitle.SubtitleTrackIndex, row.Value, want)
		}
		return nil
	}
	if err := reader.WithPreferenceSettingsSnapshot(ctx, func(r userstore.PreferenceSettingsReader) error {
		audio, err := r.GetAudioPreference(ctx, "p1", snapshotSeries)
		if err != nil {
			return err
		}
		if audio.AudioTrackIndex != 1 {
			return fmt.Errorf("initial audio=%d", audio.AudioTrackIndex)
		}
		if err := write(2); err != nil {
			return err
		}
		return check(r, 1)
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.WithPreferenceSettingsSnapshot(ctx, func(r userstore.PreferenceSettingsReader) error { return check(r, 2) }); err != nil {
		t.Fatal(err)
	}
}
