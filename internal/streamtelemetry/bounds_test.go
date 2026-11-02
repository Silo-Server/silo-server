package streamtelemetry

import (
	"bytes"
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

func TestTelemetryTinyDisplayBounds(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"ab", "ab"}, {"abc", "ab"}, {"日本語", "日本"},
		{" \xffa\x00b\n ", "ab"}, {"🚀🚀🚀", "🚀🚀"},
	} {
		if got := boundedDisplay(test.input, 2); got != test.want || !utf8.ValidString(got) {
			t.Fatalf("normalize %q = %q, want %q", test.input, got, test.want)
		}
	}
	if got := boundedText("a日本", 5); got != "a日" {
		t.Fatalf("byte boundary = %q", got)
	}
	for _, raw := range []string{"node-\xff\x00", "sha256:raw"} {
		got := displayDeviceID(raw)
		if !strings.HasPrefix(got, "sha256:") || len(got) != 71 || !validText(got, maxIdentityBytes) {
			t.Fatalf("device display = %q", got)
		}
	}
	if got := displayDeviceID("设备-1"); got != "设备-1" {
		t.Fatal(got)
	}
}

func TestTelemetryCaptureDetachesAndPreservesIdentityAuthority(t *testing.T) {
	// A short substring can pin a larger request allocation. This fixture is
	// deliberately tiny; no HTTP listener or long request is used.
	source := strings.Clone("GET node client agent profile session extra padding")
	capture := CaptureSet{Method: source[:3], DeviceID: source[4:8], Client: ClientVariant{Name: source[9:15]}, UserAgent: source[16:21]}
	r := NewRegistry(testConfig(), nil, nil)
	o := r.begin(testRoute(ClassPlayback), capture)
	for _, value := range []string{o.Capture.Method, o.Capture.DeviceID, o.Capture.Client.Name, o.Capture.UserAgent} {
		p, begin := uintptr(unsafe.Pointer(unsafe.StringData(value))), uintptr(unsafe.Pointer(unsafe.StringData(source)))
		if p >= begin && p < begin+uintptr(len(source)) {
			t.Fatalf("retained substring %q aliases request", value)
		}
	}
	a := testAttachment("session")
	a.ProfileID = "profile"
	r.attach(o, a)
	o.AddBytes(7)
	r.release(o, httpstream.OutcomeCompleted)
	s := r.Sweep()
	if capture.DeviceID != "node" || a.ProfileID != "profile" || s.Truncated || s.DroppedObservations != 0 || len(s.Sessions) != 1 || s.Sessions[0].BytesAccepted != 7 {
		t.Fatalf("identity/accounting changed: %+v", s)
	}
	raw := CaptureSet{DeviceID: "node-\xff\x00"}
	o = r.begin(testRoute(ClassTransfer), raw)
	r.attach(o, testAttachment("unused"))
	r.release(o, httpstream.OutcomeCompleted)
	s = r.Snapshot()
	if raw.DeviceID != "node-\xff\x00" || s.Truncated || len(s.Transfers) != 1 || s.Transfers[0].DeviceID == raw.DeviceID {
		t.Fatalf("transfer display/authority = %+v", s)
	}
	// Codec validation preserves a captured hash; it never rehashes it.
	b, err := encodeTransfer(s.Transfers[0])
	if err != nil {
		t.Fatal(err)
	}
	v, err := decodeTransfer(b)
	if err != nil || v.DeviceID != s.Transfers[0].DeviceID {
		t.Fatalf("hash round trip: %+v %v", v, err)
	}
	if got := r.sweep(time.Now().Add(time.Second)); len(got.Sessions) != 0 || len(got.Transfers) != 0 {
		t.Fatal("expiry changed")
	}
}

func TestTelemetryConfigProducts(t *testing.T) {
	cfg := testConfig()
	if !retainedConfigFits(cfg) {
		t.Fatal("ordinary configured products do not fit")
	}
	cfg.MaxClientVariantsPerSession = math.MaxInt
	if retainedConfigFits(cfg) || NewRegistry(cfg, nil, nil).Enabled() {
		t.Fatal("overflowing product accepted")
	}
	if cfg.MaxClientVariantsPerSession != math.MaxInt {
		t.Fatal("caller count policy changed")
	}
}

func TestTelemetryTinyRecordAndCodecBudgets(t *testing.T) {
	v := SessionView{SessionID: "s", DeviceIDs: []string{"日", "a"}}
	encoded, err := encodeSessionBounded(v, 1024, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeSessionBounded(v, 1024, 4); err == nil {
		t.Fatal("aggregate encode string budget ignored")
	}
	if _, err := decodeSessionBounded(encoded, len(encoded), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSessionBounded(encoded, len(encoded), 4); err == nil {
		t.Fatal("aggregate decode string budget ignored")
	}
	if _, err := decodeSessionBounded(encoded, len(encoded)-1, 5); err == nil {
		t.Fatal("raw byte budget ignored")
	}
	if _, err := encodeSessionBounded(v, len(encoded)-1, 5); err == nil {
		t.Fatal("encoded byte budget ignored")
	}
	if _, err := decodeSession([]byte("{\"v\":1,\"sid\":\"\xff\"}")); err == nil {
		t.Fatal("invalid raw UTF-8 accepted")
	}
	if _, err := decodeSession([]byte(`{"v":1,"dids":["a\u0000b"]}`)); err == nil {
		t.Fatal("escaped control accepted")
	}
	if _, err := encodeSession(SessionView{Outcomes: map[httpstream.StreamOutcome]int64{"bad\x00key": 1}}); err == nil {
		t.Fatal("map key accepted")
	}
	if _, err := decodeTransfer([]byte(`{"v":1,"ua":"a\u0000b"}`)); err == nil {
		t.Fatal("transfer control accepted")
	}
	if _, err := encodeMeta(publisherMeta{NodeID: "bad\x00node"}); err == nil {
		t.Fatal("meta control accepted")
	}
	if validText("日本", 5) || !validText("日本", 6) {
		t.Fatal("UTF-8 field boundary")
	}
	if err := validateTransferText(wireTransfer{ID: "a\x00b"}, 3); err != nil {
		t.Fatal(err)
	}
	if err := validateTransferText(wireTransfer{ID: "a\x00b"}, 2); err == nil {
		t.Fatal("composite ID separators uncounted")
	}
	// Escaped wire bytes differ from retained string bytes.
	escaped := SessionView{SessionID: "s", DeviceIDs: []string{"<>&"}}
	a, err := encodeSessionBounded(escaped, 1024, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(a, []byte(`\u003c`)) {
		t.Fatal("fixture no longer exercises JSON escaping")
	}
	for range 3 {
		b, err := encodeSessionBounded(escaped, 1024, 4)
		if err != nil || !bytes.Equal(a, b) {
			t.Fatal("serialization unstable")
		}
	}
}

func TestTelemetryPublicationOmitsOnlyInvalidRecord(t *testing.T) {
	store := NewRedisStore(nil, testConfig(), nil) // no network transport
	snapshot := Snapshot{PublisherID: "p", CapturedAt: time.Unix(100, 0), Sessions: []SessionView{{SessionID: "a"}, {SessionID: "b"}}}
	markPlanPublished(t, store, snapshot)
	snapshot.Sessions[1].DeviceIDs = []string{"bad\x00label"}
	sets, dels, fields, full, err := store.plan(snapshot)
	if err != nil || full || !reflect.DeepEqual(dels, []string{"s:b"}) || len(sets) != 1 {
		t.Fatalf("delta: sets=%v dels=%v full=%v err=%v", keysOf(sets), dels, full, err)
	}
	meta, err := decodeMeta(fields[publisherMetaField])
	if err != nil || !meta.Truncated || meta.SessionCount != 1 {
		t.Fatalf("meta = %+v %v", meta, err)
	}
	store.needFullResync = true
	if sets, _, _, full, err := store.plan(snapshot); err != nil || !full || len(sets) != 2 {
		t.Fatalf("full recovery: %v %v", full, err)
	}
	values := make(map[string]string)
	for k, v := range fields {
		values[k] = string(v)
	}
	decoded, problem, err := decodeSnapshotHash("p", values, 2, 2)
	if err != nil || problem.Reason != "" || len(decoded.Sessions) != 1 {
		t.Fatalf("decoded: %+v %+v %v", decoded, problem, err)
	}
	// Local publication keeps bounded string references; it does not serialize.
	local := NewLocalStore()
	if err := local.Publish(context.Background(), decoded); err != nil {
		t.Fatal(err)
	}
	set, err := local.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	view := BuildGlobalView(set, snapshot.CapturedAt, globalTestParams())
	if view.Complete || len(view.Sessions) != 1 {
		t.Fatalf("merge omitted-record state: %+v", view)
	}
}
