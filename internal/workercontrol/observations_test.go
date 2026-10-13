package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func observationRuntime(t *testing.T) (*Runtime, context.Context) {
	t.Helper()
	t.Setenv("SILO_STREAM_TELEMETRY_ENABLED", "true")
	r := raceRuntime(t)
	if err := r.authority.SetNonce("observation"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(workerwire.Snapshot{Version: 1, NodeID: 17, BootID: r.bootID, Nonce: "observation", Sequence: 1, Active: map[string]uint64{"work": 1}, ValidForMillis: 2000}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), workContextKey{}, "work")
	ctx = context.WithValue(ctx, generationContextKey{}, uint64(1))
	return r, ctx
}

func TestHTTPObservationsBoundRedactAndRetainUntilAcknowledged(t *testing.T) {
	r, ctx := observationRuntime(t)
	o := newObservations(r)
	attrs := playback.FFmpegLogAttrs{InputPath: "/private/input.mkv", OutputDir: "/private/scratch", ExitError: "secret-error", ExecutionMode: "hls", TargetVideoCodec: "h264", HWAccel: "none", LineIndex: 1}
	message := `Conversion failed: 'http://core.invalid/input/private-token' '/private/location/file name.mkv' secret_key=private-secret Authorization: Bearer private-bearer ` + strings.Repeat("x", 1500)
	for i := range 300 {
		if i%50 == 0 {
			if err := r.authority.Apply(workerwire.Snapshot{Version: 1, NodeID: 17, BootID: r.bootID, Nonce: "observation", Sequence: uint64(i/50 + 2), Active: map[string]uint64{"work": 1}, ValidForMillis: 2000}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		o.WriteLine(ctx, "viewer", attrs, message)
	}
	first := o.batch()
	if len(first.Logs) != 128 || first.DroppedLogs != 44 {
		t.Fatalf("bounded queue/batch = %d dropped=%d", len(first.Logs), first.DroppedLogs)
	}
	for _, record := range first.Logs {
		if record.WorkID != "work" || record.Generation != 1 || len(record.Message) > 1024 || !strings.Contains(record.Message, "Conversion failed") {
			t.Fatal("scope, bound or useful diagnostic lost")
		}
	}
	raw, _ := json.Marshal(first)
	for _, secret := range []string{"private-token", "file name.mkv", "private-secret", "private-bearer", "/private/", "secret-error"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("raw location/credential escaped: %s", secret)
		}
	}
	if second := o.batch(); len(second.Logs) != 128 || second.Logs[0].Message != first.Logs[0].Message {
		t.Fatal("failed report discarded pending logs")
	}
	o.ack()
	next := o.batch()
	if len(next.Logs) != 128 {
		t.Fatalf("ack released wrong prefix: %d", len(next.Logs))
	}
	o.WriteEvent(ctx, "viewer", attrs, "later lifecycle")
	o.ack()
	last := o.batch()
	if len(last.Logs) != 1 || last.Logs[0].Message != "later lifecycle" {
		t.Fatal("ack removed a concurrent new lifecycle record")
	}
	o.ack()
	if len(o.batch().Logs) != 0 {
		t.Fatal("ack did not release queued logs")
	}
	o.WriteLine(t.Context(), "unregistered", attrs, "must not report")
	if len(o.batch().Logs) != 0 {
		t.Fatal("unscoped worker log was reported")
	}
}

func TestHTTPObservationsCaptureOnlyScopedSessionsAndPreserveBytes(t *testing.T) {
	r, ctx := observationRuntime(t)
	o := newObservations(r)
	r.tracker.Track(ctx, nodesessions.SessionInfo{SessionID: "viewer", Type: "transcode"})
	route := streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyTranscodeNode, Method: "GET", Pattern: "/transcode/{session_id}/segment/{name}", Class: streamtelemetry.ClassPlayback, Role: streamtelemetry.RoleInternalRelay, Enrolled: true}
	serve := func(session string) {
		handler := o.registry.Observe(route)(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			streamtelemetry.Attach(request.Context(), streamtelemetry.Attachment{SessionID: session, Subject: streamtelemetry.UserSubject(42), ProfileID: "private-profile", MediaFileID: 99})
			_, _ = w.Write([]byte("segment"))
		}))
		request := httptest.NewRequest("GET", "/transcode/work/segment/segment0.ts", nil)
		request.Header.Set("User-Agent", "private-agent")
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	serve("viewer")
	serve("unscoped-viewer")
	batch := o.batch()
	if len(batch.Snapshot.Sessions) != 1 || len(batch.Scopes) != 1 || batch.Scopes[0].SessionID != "viewer" || batch.Scopes[0].Generation != 1 {
		t.Fatalf("unscoped session entered report: %+v", batch)
	}
	view := batch.Snapshot.Sessions[0]
	if view.BytesAccepted != 7 || view.RequestCount != 1 || len(view.Routes) != 1 {
		t.Fatal("scoped registry counters were not preserved")
	}
	if view.Subject.ID != "" || view.ProfileID != "" || view.MediaFileID != 0 || len(view.UserAgents) != 0 || len(view.ViewerIPs) != 0 {
		t.Fatal("unverified worker identity was forwarded")
	}
	if !batch.Snapshot.Truncated || batch.Snapshot.UnattributedObservations == 0 {
		t.Fatal("filtered observation was hidden from completeness counters")
	}
}

func TestObservationsForwardOnlyThroughSignedHTTPReport(t *testing.T) {
	t.Setenv("SILO_STREAM_TELEMETRY_ENABLED", "true")
	corePub, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePub, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	reports := make(chan workerwire.Report, 1)
	var core *httptest.Server
	const nodeURL = "http://worker.example.test"
	verifier := &workeridentity.Verifier{Domain: workeridentity.DomainNodeProof, AllowHTTP: true, ResolveKey: func(node int) (ed25519.PublicKey, error) { return nodePub, nil }}
	core = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != "POST" || request.URL.Path != "/api/internal/workers/17/report" || verifier.Verify(request, 17) != nil {
			w.WriteHeader(401)
			return
		}
		var report workerwire.Report
		if json.NewDecoder(request.Body).Decode(&report) != nil {
			w.WriteHeader(400)
			return
		}
		if request.Header.Get("Authorization") != "" {
			t.Error("worker sent cluster authorization")
		}
		raw, _ := json.Marshal(map[string]bool{"accepted": true})
		encoded, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: workeridentity.DomainReportGrant, Audience: nodeURL, NodeID: 17, ExpiresAt: time.Now().Add(time.Minute).Unix(), Body: raw}, coreKey)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"grant": encoded})
		select {
		case reports <- report:
		default:
		}
	}))
	defer core.Close()
	verifier.Origin = core.URL
	r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: corePub, NodePrivateKey: nodeKey, NodeURL: nodeURL, AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(context.Background()) }()
	if err := r.authority.SetNonce("http-report"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(workerwire.Snapshot{Version: 1, NodeID: 17, BootID: r.bootID, Nonce: "http-report", Sequence: 1, Active: map[string]uint64{"work": 1}, ValidForMillis: 2000}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), workContextKey{}, "work")
	ctx = context.WithValue(ctx, generationContextKey{}, uint64(1))
	r.tracker.Track(ctx, nodesessions.SessionInfo{SessionID: "viewer", Type: "transcode"})
	r.observations.WriteEvent(ctx, "viewer", playback.FFmpegLogAttrs{}, "ffmpeg process started")
	go r.reportLoop()
	select {
	case report := <-reports:
		if report.BootID != r.bootID || report.Sequence != 1 || len(report.Sessions) != 1 || report.Sessions[0].WorkID != "work" || report.Observations == nil || len(report.Observations.Logs) != 1 || report.Observations.Logs[0].Generation != 1 {
			t.Fatalf("signed observations missing scope: %+v", report)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not forward signed report")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(r.observations.batch().Logs) != 0 {
		select {
		case <-deadline.C:
			t.Fatal("signed acknowledgement did not release log queue")
		case <-tick.C:
		}
	}
}
