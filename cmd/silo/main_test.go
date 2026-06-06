package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestConfigureS3Clients_SetsCORSOnPublicAssetsBucket(t *testing.T) {
	publicServer := newS3BucketRecorder(t)

	cfg := &config.Config{
		S3: config.S3Config{
			Public: config.S3PublicAssetsSettings{
				S3BucketSettings: config.S3BucketSettings{
					Endpoint:  publicServer.URL(),
					Region:    "us-east-1",
					Bucket:    "public-assets",
					AccessKey: "test",
					SecretKey: "test",
					PathStyle: true,
				},
			},
		},
	}

	deps := &api.Dependencies{}
	configureS3Clients(cfg, deps)

	if deps.S3Public == nil {
		t.Fatal("S3Public should be configured")
	}
	if got := publicServer.CORSRequests(); got != 1 {
		t.Fatalf("public assets bucket CORS requests = %d, want 1", got)
	}
}

func TestConfigureS3Clients_PassesPublicKeyPrefix(t *testing.T) {
	publicServer := newS3BucketRecorder(t)

	cfg := &config.Config{
		S3: config.S3Config{
			Public: config.S3PublicAssetsSettings{
				S3BucketSettings: config.S3BucketSettings{
					Endpoint:  publicServer.URL(),
					Region:    "us-east-1",
					Bucket:    "public-assets",
					KeyPrefix: "silo/dev",
					AccessKey: "test",
					SecretKey: "test",
					PathStyle: true,
				},
			},
		},
	}

	deps := &api.Dependencies{}
	configureS3Clients(cfg, deps)

	if deps.S3Public == nil {
		t.Fatal("S3Public should be configured")
	}

	url, err := deps.S3Public.PublicURL(deps.S3Public.Bucket(), "catalog-seeds/export.json.gz")
	if err != nil {
		t.Fatalf("PublicURL() returned error: %v", err)
	}
	if !strings.Contains(url, "/silo/dev/catalog-seeds/export.json.gz") {
		t.Fatalf("PublicURL() = %q, want prefixed path", url)
	}
}

type s3BucketRecorder struct {
	server       *httptest.Server
	mu           sync.Mutex
	corsRequests int
}

func newS3BucketRecorder(t *testing.T) *s3BucketRecorder {
	t.Helper()

	recorder := &s3BucketRecorder{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()

		if r.Method == http.MethodPut && r.URL.Query().Has("cors") {
			recorder.mu.Lock()
			recorder.corsRequests++
			recorder.mu.Unlock()
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(recorder.server.Close)

	return recorder
}

func (r *s3BucketRecorder) URL() string {
	return r.server.URL
}

func (r *s3BucketRecorder) CORSRequests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.corsRequests
}

func TestBuildLiveSessionSync_UsesTransportPlayMethod(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		session playback.Session
		want    string
	}{
		{
			name: "transcode transport remains transcode when base method is remux",
			session: playback.Session{
				ID:                   "session-1",
				UserID:               7,
				ProfileID:            "profile-1",
				MediaFileID:          42,
				RequestedMediaFileID: 41,
				PlayMethod:           playback.PlayTranscode,
				BasePlayMethod:       playback.PlayRemux,
				TranscodeHWAccel:     "qsv",
				Position:             125.5,
				IsPaused:             true,
			},
			want: "transcode",
		},
		{
			name: "remux transport stays remux",
			session: playback.Session{
				ID:                   "session-2",
				UserID:               8,
				ProfileID:            "profile-2",
				MediaFileID:          99,
				RequestedMediaFileID: 99,
				PlayMethod:           playback.PlayRemux,
				BasePlayMethod:       playback.PlayRemux,
			},
			want: "remux",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := buildLiveSessionSync(&tc.session, "node-a")
			if got.PlayMethod != tc.want {
				t.Fatalf("PlayMethod = %q, want %q", got.PlayMethod, tc.want)
			}
			if got.ReportingNode != "node-a" {
				t.Fatalf("ReportingNode = %q, want %q", got.ReportingNode, "node-a")
			}
			if got.SessionID != tc.session.ID {
				t.Fatalf("SessionID = %q, want %q", got.SessionID, tc.session.ID)
			}
			if got.ProfileID != tc.session.ProfileID {
				t.Fatalf("ProfileID = %q, want %q", got.ProfileID, tc.session.ProfileID)
			}
			if got.PositionSeconds != tc.session.Position {
				t.Fatalf("PositionSeconds = %v, want %v", got.PositionSeconds, tc.session.Position)
			}
			if got.IsPaused != tc.session.IsPaused {
				t.Fatalf("IsPaused = %v, want %v", got.IsPaused, tc.session.IsPaused)
			}
			if got.TranscodeHWAccel != tc.session.TranscodeHWAccel {
				t.Fatalf("TranscodeHWAccel = %q, want %q", got.TranscodeHWAccel, tc.session.TranscodeHWAccel)
			}
		})
	}
}
