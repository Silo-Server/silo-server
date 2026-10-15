package noderecipe

import "testing"

func TestMoveRequestRoundTrip(t *testing.T) {
	payload, err := EncodeMoveRequest(MoveRequest{TransportID: "upstream-1", NodeURL: "http://node-a:8080"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	request, ok := DecodeMoveRequest(payload)
	if !ok || request.TransportID != "upstream-1" || request.NodeURL != "http://node-a:8080" {
		t.Fatalf("decode = %+v, %v", request, ok)
	}
	if _, err := EncodeMoveRequest(MoveRequest{TransportID: "upstream-1"}); err == nil {
		t.Fatal("encoded a request without a node")
	}
	for _, payload := range []string{"", "not json", `{"transport_id":"x"}`, `{"node_url":"http://n"}`} {
		if _, ok := DecodeMoveRequest(payload); ok {
			t.Errorf("decoded invalid payload %q", payload)
		}
	}
}
