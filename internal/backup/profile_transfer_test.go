package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/backup/jsonsize"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// TestParseProfileExportRefusesHostileDocuments: a profile import reads an
// untrusted upload. It carries only the profile's own data, so a document
// holding account or access fields, a newer or foreign format, unknown
// fields or data, or one too large or too deep is refused before anything
// is written.
func TestParseProfileExportRefusesHostileDocuments(t *testing.T) {
	head := `"format":"silo-profile-export","version":1,"server_id":"s1","exported_at":"2026-01-01T00:00:00Z","profile_name":"Main"`
	valid := `{` + head + `,"data":{"ratings":{"ratings":[{"item":"m1","rating":4,"rated_at":"2026-01-01T00:00:00Z"}]}},"items":[]}`
	if _, err := ParseProfileExport([]byte(valid)); err != nil {
		t.Fatalf("a valid document: %v", err)
	}
	cases := []struct {
		name string
		doc  string
		want error
	}{
		{"a PIN hash", `{` + head + `,"pin_hash":"x","data":{},"items":[]}`, ErrProfileImportAccessFields},
		{"access fields in preferences", `{` + head + `,"data":{"preferences":{"profile":{"max_playback_quality":"","library_restrictions_enabled":false}}},"items":[]}`, ErrProfileImportAccessFields},
		{"a newer version", `{"format":"silo-profile-export","version":2,"data":{}}`, ErrProfileExportTooNew},
		{"another format", `{"format":"silo-restore-set","version":1}`, nil},
		{"unknown fields", `{` + head + `,"extra":true,"data":{},"items":[]}`, nil},
		{"unknown data", `{` + head + `,"data":{"passwords":{}},"items":[]}`, nil},
		{"unknown fields inside data", `{` + head + `,"data":{"ratings":{"ratings":[],"owner":1}},"items":[]}`, nil},
		{"too deep", `{` + head + `,"data":{"ratings":` + strings.Repeat("[", 64) + strings.Repeat("]", 64) + `},"items":[]}`, nil},
		{"too large", `{` + head + `,"profile_name":"` + strings.Repeat("x", MaxProfileImportBytes) + `"}`, ErrProfileImportTooLarge},
		{"repeated items", `{` + head + `,"data":{},"items":[{"content_id":"m1"},{"content_id":"m1"}]}`, nil},
		// Each would decode to far more than the document holds.
		{"empty items", `{` + head + `,"data":{},"items":[` + strings.Repeat(`{},`, 99_999) + `{}]}`, jsonsize.ErrTooLarge},
		{"empty ratings", `{` + head + `,"data":{"ratings":{"ratings":[` + strings.Repeat(`{},`, 99_999) + `{}]}},"items":[]}`, jsonsize.ErrTooLarge},
		{"many members", `{` + head + `,` + strings.Repeat(`"x":0,`, 99_999) + `"x":0}`, jsonsize.ErrTooLarge},
		// A repeat would multiply the import's writes; no export has one.
		{"a repeated rating", `{` + head + `,"data":{"ratings":{"ratings":[{"item":"m1","rating":4,"rated_at":"2026-01-01T00:00:00Z"},{"item":"m1","rating":5,"rated_at":"2026-01-02T00:00:00Z"}]}},"items":[]}`, nil},
		{"a repeated favorite", `{` + head + `,"data":{"favorites":{"favorites":[{"item":"m1"},{"item":"m1"}],"watchlist":[],"titles":[]}},"items":[]}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseProfileExport([]byte(c.doc))
			if err == nil {
				t.Fatal("accepted")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.want == nil {
				if _, ok := errors.AsType[*InvalidProfileExportError](err); !ok {
					t.Fatalf("err = %v, want an invalid document", err)
				}
			}
		})
	}
}

// Imports are admitted before they take a pool connection: one per
// account, and maxConcurrentTransfers on the server, exports and imports
// alike.
func TestProfileImportAdmission(t *testing.T) {
	transfer := NewProfileTransfer(nil, nil)
	first, err := transfer.admit(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.admit(1); !errors.Is(err, ErrProfileTransferBusy) {
		t.Fatalf("a second import for one account = %v, want busy", err)
	}
	second, err := transfer.admit(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.admit(3); !errors.Is(err, ErrProfileTransferBusy) {
		t.Fatalf("an import past the server's limit = %v, want busy", err)
	}
	first()
	third, err := transfer.admit(1)
	if err != nil {
		t.Fatalf("an import after the account's last ended = %v", err)
	}
	second()
	third()

	// A slot the caller took before reading the body (Admit) carries to
	// the transfer, which does not take another; any other context does.
	ctx, release, err := transfer.Admit(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	held, err := transfer.slotFor(ctx, 1)
	if err != nil {
		t.Fatalf("the admitted import took another slot: %v", err)
	}
	held()
	if _, err := transfer.slotFor(t.Context(), 1); !errors.Is(err, ErrProfileTransferBusy) {
		t.Fatalf("an export beside an admitted import = %v, want busy", err)
	}
	release()
}

// A restored password or PIN hash this server would not check at its own
// cost is replaced with one nobody knows, locking the account or profile
// instead of making every sign-in pay the document's cost.
func TestRestoredCredentialsAreLockedOutsideTheServersCost(t *testing.T) {
	hash := func(cost string) string { return "$2a$" + cost + "$" + strings.Repeat("a", 53) }
	doc := UserDocument{Account: AccountEntry{PasswordHash: hash("31")}, Profiles: []ProfileEntry{{PINHash: hash("04")}, {PINHash: "plain"}, {}}}
	if err := doc.lockCredentials(); err != nil {
		t.Fatal(err)
	}
	if doc.Account.PasswordHash == hash("31") || doc.Profiles[0].PINHash != hash("04") || doc.Profiles[1].PINHash == "plain" ||
		doc.Profiles[2].PINHash != "" || doc.lockedCredentials != 2 {
		t.Fatalf("locked %d: password %q, pins %q %q %q", doc.lockedCredentials, doc.Account.PasswordHash,
			doc.Profiles[0].PINHash, doc.Profiles[1].PINHash, doc.Profiles[2].PINHash)
	}
}

// sampleAt is a time in the samples.
const sampleAt = `"2026-01-01T00:00:00Z"`

// importerSample is a document as head + entry + tail.
type importerSample struct{ head, entry, tail string }

// importerSamples are one valid entry of each data type's document, as
// head + entry + tail.
var importerSamples = map[string]importerSample{
	userdata.WatchHistory + " progress": {`{"progress":[`, `{"item":"m1","position_seconds":1,"duration_seconds":2,"completed":false,"updated_at":` + sampleAt + `}`, `],"history":[],"hidden":[]}`},
	userdata.WatchHistory + " plays":    {`{"progress":[],"history":[`, `{"item":"m1","watched_at":` + sampleAt + `,"completed":true,"source":"playback"}`, `],"hidden":[]}`},
	userdata.WatchHistory + " hidden":   {`{"progress":[],"history":[],"hidden":[`, `{"item":"m1","hidden_before":` + sampleAt + `,"updated_at":` + sampleAt + `,"removed_before":` + sampleAt + `}`, `]}`},
	userdata.Ratings:                    {`{"ratings":[`, `{"item":"m1","rating":4,"rated_at":` + sampleAt + `}`, `]}`},
	userdata.Favorites + " favorites":   {`{"favorites":[`, `{"item":"m1","at":` + sampleAt + `}`, `],"watchlist":[],"titles":[]}`},
	userdata.Favorites + " watchlist":   {`{"favorites":[],"watchlist":[`, `{"item":"m1","at":` + sampleAt + `}`, `],"titles":[]}`},
	userdata.Dismissals + " home": {`{"home":[`, `{"surface":"continue_watching","item":"m1","series":"s1","progress_updated_at":null,"dismissed_at":` + sampleAt + `}`,
		`],"not_interested":[],"dropped_series":[]}`},
	userdata.Dismissals + " not interested": {`{"home":[],"not_interested":[`, `{"item":"m1","at":` + sampleAt + `}`, `],"dropped_series":[]}`},
	userdata.Dismissals + " dropped":        {`{"home":[],"not_interested":[],"dropped_series":[`, `{"item":"s1","at":` + sampleAt + `}`, `]}`},
	userdata.Notifications:                  {`{"muted_series":[`, `{"series":"s1","muted_at":` + sampleAt + `}`, `]}`},
	userdata.Collections: {`{"collections":[`, `{"id":"c1","name":"A","type":"mdblist","watch_filter":"all","media_filter":"all","items":[],"shared_with":[],` +
		`"sort_config":{"field":"title","order":"asc"},"source_url":"https://mdblist.com/lists/a/b",` +
		`"source_config":{"mode":"mdblist_json","url":"https://mdblist.com/lists/a/b","library_ids":[1]}}`, `]}`},
	userdata.Preferences + " settings": {`{"settings":[`, `{"key":"playback.audio_language","scope":"profile_series","series":"s1","value":"\"en\""}`, `]}`},
	userdata.Preferences + " audio":    {`{"audio":[`, `{"series":"s1","track_index":1,"language":"en","signature":null,"updated_at":` + sampleAt + `}`, `]}`},
	userdata.Preferences + " subtitles": {`{"subtitles":[`,
		`{"series":"s1","language":"en","track_index":1,"external_path":"","mode":"always","show_forced_subtitles":null,"signature":null,"updated_at":` + sampleAt + `}`, `]}`},
	userdata.Preferences + " library playback": {`{"library_playback":[`,
		`{"library":1,"audio_language":"en","subtitle_language":null,"subtitle_mode":null,"show_forced_subtitles":null,"updated_at":` + sampleAt + `}`, `]}`},
	userdata.Preferences + " series playback": {`{"series_playback":[`,
		`{"series":"s1","resolution":null,"hdr":false,"codec_video":null,"file_id":null,"edition_key":null,"updated_at":` + sampleAt + `}`, `]}`},
	userdata.Preferences + " library sort":   {`{"collection_sort":[`, `{"kind":"library","collection":"c1","field":"title","order":"asc","updated_at":` + sampleAt + `}`, `]}`},
	userdata.Preferences + " watchlist sort": {`{"collection_sort":[`, `{"kind":"watchlist","collection":"c1","field":"title","order":"asc","updated_at":` + sampleAt + `}`, `]}`},
	userdata.Preferences + " favorites sort": {`{"collection_sort":[`, `{"kind":"favorites","collection":"c1","field":"title","order":"asc","updated_at":` + sampleAt + `}`, `]}`},
}

// TestImportedCollectionSourcesAreTheEditors: an imported collection's
// sort and sync source are stored as the editors encode them, and one the
// editors would not store is refused when the document is checked: a list
// URL on loopback or link-local, a mode its type does not sync from, or a
// member neither type has.
func TestImportedCollectionSourcesAreTheEditors(t *testing.T) {
	dt, _ := userDataType(userdata.Collections)
	doc := func(collection string) json.RawMessage {
		return json.RawMessage(`{"collections":[{"id":"c1","name":"A","watch_filter":"all","media_filter":"all","items":[],"shared_with":[],` + collection + `}]}`)
	}
	if _, err := dt.Validate(doc(`"type":"mdblist","source_url":"https://mdblist.com/lists/a/b","source_config":{"mode":"mdblist_json","url":"https://mdblist.com/lists/a/b"}`)); err != nil {
		t.Fatalf("an MDBList collection was refused: %v", err)
	}
	for name, collection := range map[string]string{
		"a loopback list":           `"type":"mdblist","source_config":{"mode":"mdblist_json","url":"http://127.0.0.1:8080/lists/a/b"}`,
		"a link-local source url":   `"type":"mdblist","source_url":"http://169.254.169.254/lists/a","source_config":{}`,
		"a TMDB list elsewhere":     `"type":"tmdb","source_config":{"mode":"tmdb_list","url":"http://10.0.0.1/list/1"}`,
		"a manual collection syncs": `"type":"manual","source_config":{"mode":"mdblist_json","url":"https://mdblist.com/lists/a/b"}`,
		"an unknown source member":  `"type":"tmdb","source_config":{"mode":"tmdb_preset","preset":"popular","endpoint":"http://127.0.0.1/"}`,
		"an unknown sort member":    `"type":"manual","sort_config":{"field":"title","order":"asc","blob":"x"}`,
	} {
		if _, err := dt.Validate(doc(collection)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestEveryImporterRefusesDuplicates: each data type's document holds one
// entry per identity, as its export writes it. A document of one entry is
// accepted; 100,000 copies of it are refused when the document is checked,
// quickly, before any import plans or writes them, except plays, whose
// repeats are the same play and collapse to one.
func TestEveryImporterRefusesDuplicates(t *testing.T) {
	docs := importerSamples
	for name, d := range docs {
		t.Run(name, func(t *testing.T) {
			id, part, _ := strings.Cut(name, " ")
			dt, ok := userDataType(id)
			if !ok {
				t.Fatalf("no data type %s", id)
			}
			if _, err := dt.Validate(json.RawMessage(d.head + d.entry + d.tail)); err != nil {
				t.Fatalf("one entry: %v", err)
			}
			started := time.Now()
			sum, err := dt.Validate(json.RawMessage(d.head + strings.Repeat(d.entry+",", 99_999) + d.entry + d.tail))
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("checking took %v", elapsed)
			}
			if part == "plays" {
				if err != nil || sum.Count != 1 {
					t.Fatalf("100,000 copies of one play = %d (%v), want them collapsed to one", sum.Count, err)
				}
				return
			}
			if err == nil {
				t.Fatal("100,000 copies of one entry were accepted")
			}
		})
	}
}

// jsonMutations lists, for a JSON document, copies of it with one field
// made too large: each string replaced by 2 MiB, each object given 100
// more members, each array of strings given 500,001.
func jsonMutations(t *testing.T, doc []byte) map[string][]byte {
	t.Helper()
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	var walk func(v any, path string, set func(any))
	walk = func(v any, path string, set func(any)) {
		emit := func(what string, replacement any) {
			set(replacement)
			data, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			out[path+" "+what] = data
			set(v)
		}
		switch x := v.(type) {
		case string:
			emit("too long", strings.Repeat("x", 2<<20))
		case map[string]any:
			bigger := maps.Clone(x)
			for i := range 100 {
				bigger[fmt.Sprintf("zz%d", i)] = "v"
			}
			emit("too many members", bigger)
			for k, child := range x {
				walk(child, path+"."+k, func(r any) { x[k] = r })
			}
		case []any:
			if len(x) > 0 {
				if s, ok := x[0].(string); ok {
					emit("too many elements", slices.Repeat([]any{s}, 500_001))
				}
			}
			for i, child := range x {
				walk(child, fmt.Sprintf("%s[%d]", path, i), func(r any) { x[i] = r })
			}
		}
	}
	walk(root, "$", func(r any) { root = r })
	return out
}

// TestEveryImportedFieldIsBounded: in every importer's document and in a
// restore set's account document, each field made too large on its own (a
// 2 MiB string, 100 more members, 500,001 elements) is refused, so a field
// added without a bound fails here.
func TestEveryImportedFieldIsBounded(t *testing.T) {
	for name, d := range importerSamples {
		id, _, _ := strings.Cut(name, " ")
		dt, ok := userDataType(id)
		if !ok {
			t.Fatalf("no data type %s", id)
		}
		for what, doc := range jsonMutations(t, []byte(d.head+d.entry+d.tail)) {
			if _, err := dt.Validate(doc); err == nil {
				t.Errorf("%s %s was accepted", name, what)
			}
		}
	}
	user := mustJSON(testUserDocument(5, "upload:profile-avatars/5/p1/original.webp"))
	for what, doc := range jsonMutations(t, user) {
		var parsed UserDocument
		err := decodeStrict(doc, &parsed)
		if err == nil {
			err = userdata.CheckBounds(&parsed)
		}
		if err == nil {
			_, err = validateUserDocument(&parsed)
		}
		if err == nil {
			t.Errorf("account document %s was accepted", what)
		}
	}
	// A profile export whose data names more items than an import reads,
	// with an empty items list: refused before the import's transaction.
	ratings := make([]string, maxProfileItems+1)
	for i := range ratings {
		ratings[i] = fmt.Sprintf(`{"item":"m%d","rating":4,"rated_at":%s}`, i, sampleAt)
	}
	export := func(ratings []string) []byte {
		return []byte(`{"format":"` + ProfileExportFormat + `","version":1,"exported_at":` + sampleAt + `,"profile_name":"P","items":[],` +
			`"data":{"` + userdata.Ratings + `":{"ratings":[` + strings.Join(ratings, ",") + `]}}}`)
	}
	if _, err := ParseProfileExport(export(ratings[:10])); err != nil {
		t.Fatalf("a profile export naming 10 items: %v", err)
	}
	if _, err := ParseProfileExport(export(ratings)); err == nil {
		t.Errorf("a profile export naming %d items in its data was accepted", maxProfileItems+1)
	}
}
