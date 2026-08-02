package main

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/contracts/complexv22"
)

func writeFixtures(w io.Writer) error {
	timestamp := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	logo, etag := "https://silo.example.test/logo.png", `"logo-v1"`
	session := complexv22.SnapshotSession{SessionID: "session-1", SessionGeneration: "generation-1", UserID: 101, Username: "contract-user", StartedAt: timestamp.Add(-time.Minute), State: "playing", ClientLabel: "Contract Player", IsTranscoded: true, HasPlaybackControl: true}
	fixture := map[string]any{
		"capabilities":        complexv22.SystemCapabilitiesResponse{APIVersion: "2.2", Capabilities: complexv22.Capabilities},
		"branding":            complexv22.BrandingResponse{ServerName: "Contract Silo", LogoURL: &logo, LogoETag: &etag},
		"complete_snapshot":   complexv22.SessionSnapshotResponse{SnapshotID: "11111111-1111-4111-8111-111111111111", GeneratedAt: timestamp, Complete: true, Sessions: []complexv22.SnapshotSession{session}},
		"incomplete_snapshot": complexv22.SessionSnapshotResponse{SnapshotID: "22222222-2222-4222-8222-222222222222", GeneratedAt: timestamp, Complete: false, IncompleteReason: "stale_reporting_node", Sessions: []complexv22.SnapshotSession{}},
		"terminate_request":   complexv22.TerminateRequest{SessionGeneration: "generation-1", SnapshotID: "11111111-1111-4111-8111-111111111111", ReasonCode: "global_stream_limit", IdempotencyKey: "contract-key-1"},
		"terminate_response":  complexv22.TerminateResponse{CommandID: "command-1", Status: "terminated"},
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(fixture)
}
func main() {
	if err := writeFixtures(os.Stdout); err != nil {
		panic(err)
	}
}
