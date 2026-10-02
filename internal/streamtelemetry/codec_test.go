package streamtelemetry

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

func populatedSessionView() SessionView {
	t1 := time.Unix(1_700_000_000, 123)
	t2 := time.Unix(1_700_000_100, 456)
	return SessionView{
		Subject: Subject{Kind: SubjectUser, ID: "42"}, ProfileID: "家庭", SessionID: "session-1", MediaFileID: 7, PlayMethod: "direct",
		MediaFileIDs: []int{7, 8}, MediaFileIDsOverflowed: true, PlayMethods: []string{"direct", "remux"}, PlayMethodsOverflowed: true,
		StartedAt: t1, StartedAtSource: StartedAtSourceClaim, StartedAtDegraded: true, BytesAccepted: 99,
		LastByteAccepted: t2, LastObservationEnd: time.Time{}, OpenObservations: 2, RealtimeConnectionAlive: true, RequestCount: 3,
		Routes: []RouteActivityView{{Method: "GET", Pattern: "/媒体/{id}", Role: RoleViewerEgress, Class: ClassPlayback, CapRelevant: true, Open: 1, Requests: 2, BytesAccepted: 90, LastByteAccepted: t2}}, RoutesOverflowed: true,
		ViewerIPs: []string{"192.0.2.1"}, ViewerIPsOverflowed: true, DeviceIDs: []string{"device"}, DeviceIDsOverflowed: true,
		ClientVariants: []ClientVariant{{Name: "客户端", Version: "1", Build: "2", Channel: "beta"}}, ClientVariantsOverflowed: true,
		UserAgents: []string{"播放器/日本語 🚀"}, UserAgentsOverflowed: true, TokenIssuedAts: []time.Time{t1, {}}, TokenIssuedAtsOverflowed: true,
		TokenIssuedAtSources: map[TokenIssuedAtSource]int64{TokenIssuedAtSourceVerified: 2, TokenIssuedAtSource("future"): 1},
		Outcomes:             map[httpstream.StreamOutcome]int64{httpstream.OutcomeCompleted: 1, httpstream.StreamOutcome("future_outcome"): 2},
		HasIdentityConflict:  true, IdentityConflicts: []IdentityConflict{{Field: "profile_id", Existing: "a", Offered: "b", ObservedAt: t2}}, IdentityConflictsOverflowed: true,
	}
}

func TestSessionCodecRoundTrip(t *testing.T) {
	want := populatedSessionView()
	encoded, err := encodeSession(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeSession(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestSessionCodecEmptyRoundTrip(t *testing.T) {
	encoded, err := encodeSession(SessionView{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeSession(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, SessionView{}) {
		t.Fatalf("empty round trip = %#v", got)
	}
}

func TestTransferCodecRoundTrip(t *testing.T) {
	want := TransferView{ID: "transfer", Subject: Subject{Kind: SubjectIP, ID: "203.0.113.4"}, ProfileID: "p", MediaFileID: 5,
		Method: "GET", Pattern: "/download", Role: RoleViewerEgress, BytesAccepted: 12, LastByteAccepted: time.Unix(10, 11),
		OpenObservations: 1, RequestCount: 2, ViewerIP: "203.0.113.4", DeviceID: "d", Client: ClientVariant{Name: "c"}, UserAgent: "ua",
		Outcomes: map[httpstream.StreamOutcome]int64{httpstream.OutcomeCompleted: 1}}
	encoded, err := encodeTransfer(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeTransfer(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestPublisherMetaCodecRoundTrip(t *testing.T) {
	want := publisherMeta{PublisherID: "pub", NodeID: "node", Epoch: 4, Sequence: 5, CapturedAtUnixNano: 6, Truncated: true,
		DroppedObservations: 7, DroppedBytes: 8, UnattributedObservations: 9, UnattributedBytes: 10, SessionCount: 11, TransferCount: 12}
	encoded, err := encodeMeta(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMeta(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want.V = codecVersion
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestCodecRejectsMalformedAndUnsupportedWithoutPanic(t *testing.T) {
	for name, data := range map[string][]byte{"truncated": []byte(`{"v":1`), "unsupported": []byte(`{"v":999}`)} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("decode panicked: %v", recovered)
				}
			}()
			_, err := decodeSession(data)
			if err == nil {
				t.Fatal("decode succeeded")
			}
			if name == "unsupported" {
				var unsupported errUnsupportedCodecVersion
				if !errors.As(err, &unsupported) {
					t.Fatalf("error = %T %v", err, err)
				}
			}
		})
	}
}

func TestCodecKeepsUnknownOutcomeKey(t *testing.T) {
	got, err := decodeSession([]byte(`{"v":1,"out":{"from_the_future":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcomes[httpstream.StreamOutcome("from_the_future")] != 3 {
		t.Fatalf("outcomes = %#v", got.Outcomes)
	}
}

func TestCodecRejectsNegativeCounter(t *testing.T) {
	if _, err := decodeSession([]byte(`{"v":1,"ba":-1}`)); err == nil {
		t.Fatal("negative counter accepted")
	}
	if _, err := decodeMeta([]byte(`{"v":1,"do":-1}`)); err == nil {
		t.Fatal("negative metadata counter accepted")
	}
}
