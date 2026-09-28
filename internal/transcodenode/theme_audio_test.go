package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

type denyInputPaths struct{}

func (denyInputPaths) Allowed(context.Context, string) (bool, error) { return false, nil }

type stubThemeSource struct {
	id   int64
	path string
	err  error
}

func (s stubThemeSource) IsActiveTheme(_ context.Context, id int64, path string) (bool, error) {
	return id == s.id && path == s.path, s.err
}

func writeNodeTheme(t *testing.T) (string, int64, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "theme.ogg")
	if err := os.WriteFile(path, []byte("theme source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, info.Size(), info.ModTime().Truncate(time.Microsecond)
}

func TestThemeInputAuthorizerRequiresExactActiveUnchangedFile(t *testing.T) {
	path, size, modified := writeNodeTheme(t)
	authorizer := NewThemeInputAuthorizer(stubThemeSource{id: 9, path: path})
	allowed := func(id int64, path string, size int64, modified time.Time) bool {
		t.Helper()
		ok, err := authorizer.AllowedTheme(t.Context(), id, path, size, modified)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !allowed(9, path, size, modified) {
		t.Fatal("active theme was refused")
	}
	if allowed(10, path, size, modified) || allowed(9, path+".other", size, modified) || allowed(9, "theme.ogg", size, modified) {
		t.Fatal("theme approval accepted a mismatched id or path")
	}
	if allowed(9, path, size+1, modified) || allowed(9, path, size, modified.Add(time.Second)) {
		t.Fatal("theme approval accepted a replaced file")
	}
	if _, err := NewThemeInputAuthorizer(stubThemeSource{err: errors.New("db down")}).AllowedTheme(t.Context(), 9, path, size, modified); err == nil {
		t.Fatal("theme authority error was swallowed")
	}
}

func themeRemuxRequest(t *testing.T, token, transport string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/remux/"+transport, nil)
	req.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", transport)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func TestHandleRemuxConvertsApprovedThemeOnly(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 21, true }
	// Theme files are not media_files rows: the catalog authority refuses them
	// and must not be what approves a theme conversion.
	server.inputPaths = denyInputPaths{}
	path, size, modified := writeNodeTheme(t)
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf theme-aac-bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "theme-transport", MediaPath: path, PlayMethod: streamtoken.PlayMethodThemeAAC, AudioOnly: true,
		TranscodeAudio: true, TargetCodecAudio: "aac", TargetAudioChannels: 2, TargetAudioBitrateKbps: 192,
		TranscodeNode: "http://node", TranscodeTransportID: "theme-transport", ThemeID: 9, ThemeSize: size, ThemeModifiedUnixNano: modified.UnixNano(),
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 21, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 11,
	}
	card := playback.RecipeCardFromClaims(&claims)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	sign := func(claims streamtoken.Claims) string {
		t.Helper()
		token, err := streamtoken.Sign(claims, testSecret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token := sign(claims)

	recorder := httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, token, "theme-transport"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("theme without a theme authority = %d %q", recorder.Code, recorder.Body.String())
	}

	server.SetThemeInputAuthorizer(NewThemeInputAuthorizer(stubThemeSource{id: 9, path: path}))
	recorder = httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, token, "theme-transport"))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "theme-aac-bytes" {
		t.Fatalf("approved theme = %d %q", recorder.Code, recorder.Body.String())
	}

	other := claims
	other.ThemeID = 10
	recorder = httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, sign(other), "theme-transport"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unapproved theme = %d %q", recorder.Code, recorder.Body.String())
	}

	// A video remux token naming the theme file still goes through the catalog
	// authority, so the theme authority cannot widen what video tokens reach.
	video := claims
	video.PlayMethod = string(playback.PlayRemux)
	videoCard := playback.RecipeCardFromClaims(&video)
	server.SetRecipeStore(&stubRecipeStore{card: &videoCard, ok: true})
	recorder = httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, sign(video), "theme-transport"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("video token for a theme path = %d %q", recorder.Code, recorder.Body.String())
	}

	notAAC := claims
	notAAC.TargetCodecAudio = "opus"
	recorder = httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, sign(notAAC), "theme-transport"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("non-AAC theme recipe = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestHWCapabilitiesAdvertisesThemeExecutionOnlyWithAuthority(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 42, true }
	features := func() []string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)
		request.Header.Set("Authorization", "Bearer "+testSecret)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code == http.StatusServiceUnavailable {
			t.Skip("this host's ffmpeg cannot answer a capability probe")
		}
		var info playback.HWAccelInfo
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &info) != nil {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		return info.TransportFeatures
	}
	if slices.Contains(features(), playback.TransportFeatureThemeAudioExecutionV1) {
		t.Fatal("theme execution advertised without a theme authority")
	}
	server.SetThemeInputAuthorizer(NewThemeInputAuthorizer(stubThemeSource{}))
	if !slices.Contains(features(), playback.TransportFeatureThemeAudioExecutionV1) {
		t.Fatal("theme execution not advertised with a theme authority")
	}
}
