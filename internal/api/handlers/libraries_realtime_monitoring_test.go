package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The real-time monitoring switch is a v2-only member. The frozen /api/v1
// library bodies must neither read nor write it.
func TestV1LibraryBodiesOmitRealtimeMonitoring(t *testing.T) {
	var create createLibraryRequest
	if err := json.Unmarshal([]byte(`{"paths":["/m"],"type":"movies","name":"M","realtime_monitoring":false}`), &create); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if create.RealtimeMonitoring != nil {
		t.Fatalf("v1 create decoded realtime_monitoring = %v, want nil", *create.RealtimeMonitoring)
	}

	var update updateLibraryRequest
	if err := json.Unmarshal([]byte(`{"name":"M","realtime_monitoring":false}`), &update); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if update.RealtimeMonitoring != nil {
		t.Fatalf("v1 update decoded realtime_monitoring = %v, want nil", *update.RealtimeMonitoring)
	}

	view := toLibraryResponse(&models.MediaFolder{ID: 1, Name: "M", RealtimeMonitoring: true})
	if !view.RealtimeMonitoring {
		t.Fatal("library view dropped RealtimeMonitoring")
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("encode view: %v", err)
	}
	if strings.Contains(string(raw), "realtime_monitoring") {
		t.Fatalf("v1 library response carries realtime_monitoring: %s", raw)
	}
}
