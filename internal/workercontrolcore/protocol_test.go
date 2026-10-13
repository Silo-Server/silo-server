package workercontrolcore

import (
	"encoding/json"
	"testing"
)

func TestProtocolTokenDTOsRetainRuntimeJSONKeys(t *testing.T) {
	for _, body := range []any{tokenRequest{Token: "opaque"}, outputProtocolRequest{Token: "opaque", NodeURL: "https://worker.example"}} {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]string
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["token"] != "opaque" || fields["Token"] != "" {
			t.Fatalf("token wire keys drifted: %s", raw)
		}
		if len(fields) == 2 && fields["node_url"] != "https://worker.example" {
			t.Fatalf("node wire key drifted: %s", raw)
		}
	}
}
