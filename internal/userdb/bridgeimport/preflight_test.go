package bridgeimport_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userdb/bridgeimport"
	"github.com/Silo-Server/silo-server/internal/userdb/bridgeimport/testdata"
)

func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "7.db")
	source, err := testdata.NewSource(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	// Build synthetic data against the frozen bridge schema22.
	for _, statement := range []string{
		`INSERT INTO personal_collection_revisions(collection_id,revision) VALUES('deleted-collection',87)`,
		`INSERT INTO favorites VALUES('parent','item','2026-01-01')`,
		`INSERT INTO watchlist VALUES('parent','item','2026-01-01',4)`,
		`INSERT INTO home_item_dismissals VALUES('parent','continue_watching','item','series','2026-01-01','2026-01-02')`,
		`INSERT INTO personal_collections(id,profile_id,creator_profile_id,name,collection_type,is_shared,query_definition,sort_config,created_at,updated_at) VALUES('manual','parent','parent','private list','manual',1,'{}','{}','2026-01-01','2026-01-01'),('smart','child','child','smart','smart',0,'{"rules":[]}','{"field":"title"}','2026-01-01','2026-01-01')`,
		`INSERT INTO personal_collection_items VALUES('manual','item',8,'2026-01-01')`,
		`INSERT INTO personal_collection_profiles VALUES('manual','child')`,
		`INSERT INTO collection_sort_preferences VALUES('parent','user','manual','title','desc','2026-01-01')`,
		`INSERT INTO subtitle_preferences(profile_id,series_id,subtitle_language,subtitle_track_index,external_subtitle_path,subtitle_mode,subtitle_track_signature,show_forced_subtitles,updated_at) VALUES('parent','series',NULL,0,'private subtitle','off','{}',0,'2026-01-01')`,
		`INSERT INTO audio_preferences(profile_id,series_id,audio_track_index,audio_language,audio_track_signature,updated_at) VALUES('parent','series',0,'','{}','2026-01-01')`,
		`INSERT INTO series_playback_preferences(profile_id,series_id,resolution,hdr,codec_video,updated_at) VALUES('parent','series',NULL,0,'','2026-01-01')`,
		`INSERT INTO library_playback_preferences(profile_id,library_id,audio_language,subtitle_language,subtitle_mode,show_forced_subtitles,updated_at) VALUES('parent',3,NULL,'','off',NULL,'2026-01-01')`,
		`INSERT INTO profile_onboarding VALUES('parent','tour','step',NULL,'2026-01-01','2026-01-01')`,
		`INSERT INTO user_settings VALUES('opaque','private value')`,
		`INSERT INTO user_device_settings(profile_id,device_id,key,value,updated_at) VALUES('parent','device','opaque','private value','2026-01-01')`,
		`INSERT INTO user_devices(profile_id,device_id,last_seen_at) VALUES('parent','device','2026-01-01')`,
		`INSERT INTO user_setting_mutations VALUES('mutation','private request hash','{"revision":9}','2026-01-01','2099-01-01')`,
		`INSERT INTO user_setting_migration_rejects(source_table,source_key,identity,value,reason,recorded_at) VALUES('user_settings','invalid','{}','private rejected value','invalid','2026-01-01')`,
		`INSERT INTO profile_section_overrides(id,profile_id,scope,hidden,removed,config,created_at,updated_at) VALUES('override','parent','home',0,1,'{"limit":3}','2026-01-01','2026-01-01')`,

		`INSERT INTO profiles(id,name,pin_hash,is_primary,show_forced_subtitles,created_at,updated_at) VALUES('parent','private name','private hash',1,0,'2026-01-01','2026-01-01'),('child','child',NULL,0,1,'2026-01-01','2026-01-01')`,
		`INSERT INTO profile_allowed_libraries VALUES('child',3)`,
		`INSERT INTO watch_history(id,profile_id,media_item_id,watched_at,duration_seconds,completed,source,watch_identity) VALUES('h1','parent','item','2026-01-01',NULL,0,'local','{}'),('h2','child','item','2026-01-01',0,1,'imported','{"provider":"private"}')`,
		`INSERT INTO hidden_history_items VALUES('parent','item','2026-01-01','2026-01-01')`,
		`INSERT INTO watch_progress(profile_id,media_item_id,position_seconds,duration_seconds,completed,updated_at,event_at) VALUES('parent','item',10,20,0,'2026-01-01','2025-12-31')`,
		`INSERT INTO jellycompat_displayprefs VALUES('prefs','client','opaque private value','2026-01-01')`,
		`INSERT INTO user_setting_values(key,scope,value,created_at,updated_at) VALUES('false','account','false','2026-01-01','2026-01-01'),('null','account','null','2026-01-01','2026-01-01'),('empty','account','""','2026-01-01','2026-01-01')`,
		`INSERT INTO user_setting_values(key,scope,profile_id,client_family,device_id,library_id,series_id,value,revision,created_at,updated_at) VALUES('p','profile','parent',NULL,NULL,NULL,NULL,'false',9,'2026-01-01','2026-01-01'),('c','profile_client','parent','web',NULL,NULL,NULL,'{}',8,'2026-01-01','2026-01-01'),('d','profile_device','parent',NULL,'device',NULL,NULL,'0',7,'2026-01-01','2026-01-01'),('l','profile_library','parent',NULL,NULL,3,NULL,'[]',6,'2026-01-01','2026-01-01'),('s','profile_series','parent',NULL,NULL,NULL,'series','true',5,'2026-01-01','2026-01-01')`,
	} {
		if _, err := source.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompleteSchemaInventoryPreservesSource(t *testing.T) {
	path := fixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := bridgeimport.Inspect(t.Context(), path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.Version != 22 || len(report.Tables) != 29 || len(report.Tables) != len(bridgeimport.Manifest()) {
		t.Fatalf("unexpected inventory: %+v", report)
	}
	for _, table := range report.Tables {
		if table.Name == "personal_collection_revisions" && table.Rows != 3 {
			t.Fatalf("revision inventory lost a live collection or tombstone: %+v", table)
		}
		if table.Name == "personal_collection_order_revision" && table.Rows != 1 {
			t.Fatalf("account order singleton missing: %+v", table)
		}
		if table.Name != "downloads" && table.Name != "playback_sessions" && table.Rows == 0 {
			t.Fatalf("fixture omitted %s", table.Name)
		}
	}
	if len(report.Blockers) != 6 {
		t.Fatalf("unexpected blockers: %v", report.Blockers)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private")) || bytes.Contains(encoded, []byte(path)) {
		t.Fatal("inventory leaked source values or path")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source bytes changed")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("source sidecars created: %v", entries)
	}
}

func TestMissingAndMismatchedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "7.db")
	if _, err := bridgeimport.Inspect(t.Context(), path, 7); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing source created")
	}
	path = fixture(t)
	if _, err := bridgeimport.Inspect(t.Context(), path, 8); err == nil {
		t.Fatal("wrong account accepted")
	}
}

func TestWALSourceRefusedWithoutChanges(t *testing.T) {
	path := fixture(t)
	wal := []byte("uncheckpointed private WAL")
	if err := os.WriteFile(path+"-wal", wal, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := bridgeimport.Inspect(t.Context(), path, 7); err == nil {
		t.Fatal("WAL ignored")
	}
	after, err := os.ReadFile(path + "-wal")
	if err != nil || !bytes.Equal(wal, after) {
		t.Fatal("WAL modified")
	}
}

func TestUnknownFutureAndLegacyBlockers(t *testing.T) {
	path := fixture(t)
	source, err := testdata.OpenSource(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE "private secret" (value TEXT)`, `INSERT INTO "private secret" VALUES('secret')`, `PRAGMA user_version=99`, `INSERT INTO playback_sessions(session_id,profile_id,media_file_id,play_method,started_at,updated_at) VALUES('s','parent',1,'direct','now','now')`} {
		if _, err := source.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := bridgeimport.Inspect(t.Context(), path, 7)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(report.Blockers, "\n")
	for _, want := range []string{"schema is not current", "unmapped source table", "nonempty playback_sessions"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q: %s", want, joined)
		}
	}
	encoded, _ := json.Marshal(report)
	if bytes.Contains(encoded, []byte("private secret")) {
		t.Fatal("unknown table name leaked")
	}
}

func TestOldVersionRemainsUnchanged(t *testing.T) {
	path := fixture(t)
	source, err := testdata.OpenSource(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec("DROP TABLE personal_collection_revisions; DROP TABLE personal_collection_order_revision; PRAGMA user_version=21"); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := bridgeimport.Inspect(t.Context(), path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != 21 || len(report.Blockers) != 9 {
		t.Fatalf("old version not blocked: %+v", report)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("old schema upgraded")
	}
}

func TestCorruptSourceFailsWithoutLeakingOrWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "7.db")
	original := []byte("not a database containing private information")
	if err := os.WriteFile(path, original, 0400); err != nil {
		t.Fatal(err)
	}
	_, err := bridgeimport.Inspect(t.Context(), path, 7)
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), path) {
		t.Fatalf("unexpected error: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("corrupt source modified")
	}
}
