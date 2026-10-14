package transcodenode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func capabilityAdmissionServer(t *testing.T, limit int) (*Server, string) {
	t.Helper()
	server := ceilingServer(t, limit)
	log := filepath.Join(t.TempDir(), "probes.log")
	script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in\n*\" -f hls \"*) while :; do sleep 0.1; done ;;\nesac\nif [ -f %q ]; then : > %q; while [ -f %q ]; do sleep 0.01; done; fi\nprintf 'probe\\n' >> %q\nif [ -f %q ]; then exit 1; fi\ncase \"$2\" in\n-encoders) echo ' V....D libx264 H.264'; echo ' A....D aac AAC' ;;\n-filters) echo ' ... tonemapx V->V' ;;\nesac\nexit 0\n", log+".block", log+".started", log+".block", log, log+".fail")
	if err := os.WriteFile(server.watcher.Config().Playback.FFmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	return server, log
}

func readAdmissionCapabilities(t *testing.T, server *Server) (playback.HWAccelInfo, *httptest.ResponseRecorder) {
	t.Helper()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, signed(t, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)))
	var info playback.HWAccelInfo
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
	}
	return info, response
}

func TestCapabilityAdmissionServesVerifiedReportAtFiniteCeiling(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			server, log := capabilityAdmissionServer(t, limit)
			verified, warm := readAdmissionCapabilities(t, server)
			if warm.Code != http.StatusOK || verified.CapabilityHash == "" {
				t.Fatalf("warm report: status=%d body=%s", warm.Code, warm.Body)
			}
			for i := range limit {
				if response := postStart(t, server, fmt.Sprintf("retained-%d", i)); response.Code != http.StatusAccepted {
					t.Fatalf("retained playback: %d %s", response.Code, response.Body)
				}
			}
			before, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			current, response := readAdmissionCapabilities(t, server)
			if response.Code != http.StatusOK {
				t.Fatalf("full finite ceiling rejected verified inventory: status=%d body=%s", response.Code, response.Body)
			}
			if current.CapabilityHash != verified.CapabilityHash || server.storedCapabilityHash() != current.CapabilityHash {
				t.Fatal("cached response and published health hash disagree with the verified report")
			}
			after, err := os.ReadFile(log)
			if err != nil || string(after) != string(before) {
				t.Fatalf("cached read started another probe: before=%q after=%q err=%v", before, after, err)
			}
			if response := postStart(t, server, "independent"); response.Code != http.StatusServiceUnavailable {
				t.Fatalf("cached inventory bypassed genuine new-work ceiling: %d %s", response.Code, response.Body)
			}
		})
	}
}

func TestCapabilityAdmissionProjectsCurrentTransportMetadata(t *testing.T) {
	server, _ := capabilityAdmissionServer(t, 1)
	var nodeID atomic.Int64
	nodeID.Store(7)
	server.nodeRowID = func() (int, bool) { id := int(nodeID.Load()); return id, id > 0 }
	server.themeInputs = NewThemeInputAuthorizer(stubThemeSource{})
	verified, warm := readAdmissionCapabilities(t, server)
	if warm.Code != http.StatusOK || !slices.Contains(verified.TransportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1) {
		t.Fatalf("warm node identity feature: status=%d body=%s", warm.Code, warm.Body)
	}
	if response := postStart(t, server, "retained"); response.Code != http.StatusAccepted {
		t.Fatal(response.Body)
	}
	nodeID.Store(0)
	current, response := readAdmissionCapabilities(t, server)
	if response.Code != http.StatusOK {
		t.Fatalf("full ceiling rejected metadata projection: status=%d body=%s", response.Code, response.Body)
	}
	if slices.Contains(current.TransportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1) || slices.Contains(current.TransportFeatures, playback.TransportFeatureThemeAudioExecutionV1) || current.CapabilityHash == verified.CapabilityHash {
		t.Fatal("cached report retained a transport feature after node identity disappeared")
	}
	if server.storedCapabilityHash() != current.CapabilityHash || playback.ComputeCapabilityHash(current) != current.CapabilityHash {
		t.Fatal("projected response hash was not recomputed and published")
	}
	nodeID.Store(7)
	current, response = readAdmissionCapabilities(t, server)
	if response.Code != http.StatusOK || !slices.Contains(current.TransportFeatures, playback.TransportFeatureThemeAudioExecutionV1) {
		t.Fatal("restored node identity did not project current theme feature")
	}
	server.themeInputs = nil
	current, response = readAdmissionCapabilities(t, server)
	if response.Code != http.StatusOK || slices.Contains(current.TransportFeatures, playback.TransportFeatureThemeAudioExecutionV1) || !slices.Contains(current.TransportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1) {
		t.Fatal("current theme removal was not projected independently of node identity")
	}
}

func TestCapabilityAdmissionCanceledWaiterReservesNoJob(t *testing.T) {
	server, _ := capabilityAdmissionServer(t, 2)
	server.capabilityBuildMu.Lock()
	locked := true
	defer func() {
		if locked {
			server.capabilityBuildMu.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Handler().ServeHTTP(response, signed(t, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil).WithContext(ctx)))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.gpu.mu.Lock()
		waiting := server.gpu.workers > 0
		server.gpu.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capability request did not enter the build wait")
		}
		runtime.Gosched()
	}
	server.jobs.mu.Lock()
	reserved := len(server.jobs.refs)
	server.jobs.mu.Unlock()
	if reserved != 0 {
		t.Errorf("waiting metadata reader reserved %d encoding identities", reserved)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Error("canceled capability reader remained blocked behind another build")
	}
	server.capabilityBuildMu.Unlock()
	locked = false
	<-done
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("canceled reader status=%d want503", response.Code)
	}
	server.jobs.mu.Lock()
	remaining := len(server.jobs.refs)
	server.jobs.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("canceled reader leaked %d encoding identities", remaining)
	}
}

func awaitCapabilityReceipt(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("fake capability probe did not report its start")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCapabilityAdmissionSerializesReadersBeforeJob(t *testing.T) {
	server, log := capabilityAdmissionServer(t, 2)
	if _, warm := readAdmissionCapabilities(t, server); warm.Code != http.StatusOK {
		t.Fatal(warm.Body)
	}
	if response := postStart(t, server, "retained"); response.Code != http.StatusAccepted {
		t.Fatal(response.Body)
	}
	block := log + ".block"
	if err := os.WriteFile(block, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(block) })
	launch := func() <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, signed(t, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)))
			done <- response
		}()
		return done
	}
	first := launch()
	awaitCapabilityReceipt(t, log+".started")
	second := launch()
	var early *httptest.ResponseRecorder
	deadline := time.Now().Add(5 * time.Second)
waitForReader:
	for {
		select {
		case early = <-second:
			t.Errorf("second reader returned while only first probe owned the available job: %d %s", early.Code, early.Body)
			break waitForReader
		default:
		}
		server.gpu.mu.Lock()
		waiting := server.gpu.workers >= 2
		server.gpu.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second reader did not enter serialization")
		}
		runtime.Gosched()
	}
	server.jobs.mu.Lock()
	identities := len(server.jobs.refs)
	server.jobs.mu.Unlock()
	if identities != 2 {
		t.Errorf("retained playback+one real probe have %d identities, want2", identities)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if response := <-first; response.Code != http.StatusOK {
		t.Errorf("first report: %d %s", response.Code, response.Body)
	}
	if early == nil {
		early = <-second
	}
	if early.Code != http.StatusOK {
		t.Errorf("serialized second report: %d %s", early.Code, early.Body)
	}
}

func TestCapabilityAdmissionFencesChangedInputsAtFullCeiling(t *testing.T) {
	for _, changed := range []string{"device", "backend", "binary-rewrite", "binary-replace"} {
		t.Run(changed, func(t *testing.T) {
			server, _ := capabilityAdmissionServer(t, 1)
			verified, warm := readAdmissionCapabilities(t, server)
			if warm.Code != http.StatusOK {
				t.Fatal(warm.Body)
			}
			if response := postStart(t, server, "retained"); response.Code != http.StatusAccepted {
				t.Fatal(response.Body)
			}
			path := server.watcher.Config().Playback.FFmpegPath
			switch changed {
			case "device":
				server.watcher.Config().Playback.HWDevice = "/different-device"
			case "backend":
				server.watcher.Config().Playback.HWAccel = "qsv"
			case "binary-rewrite", "binary-replace":
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				body = append(body, []byte("\n# changed executable\n")...)
				if changed == "binary-replace" {
					if err := os.WriteFile(path+".new", body, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path+".new", path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, body, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if _, response := readAdmissionCapabilities(t, server); response.Code != http.StatusServiceUnavailable {
				t.Fatalf("changed %s reused stale report: %d %s", changed, response.Code, response.Body)
			}
			if server.storedCapabilityHash() != verified.CapabilityHash {
				t.Fatal("failed changed-input read overwrote last published hash")
			}
		})
	}
}

func TestCapabilityAdmissionRefreshesWhenCapacityIsAvailable(t *testing.T) {
	server, log := capabilityAdmissionServer(t, 1)
	if _, warm := readAdmissionCapabilities(t, server); warm.Code != http.StatusOK {
		t.Fatal(warm.Body)
	}
	before, _ := os.ReadFile(log)
	if _, refreshed := readAdmissionCapabilities(t, server); refreshed.Code != http.StatusOK {
		t.Fatal(refreshed.Body)
	}
	after, _ := os.ReadFile(log)
	if len(after) <= len(before) {
		t.Fatal("spare-capacity read suppressed actual registry refresh")
	}
}

func TestCapabilityAdmissionColdFullAndCanceledReadersCannotUseFallback(t *testing.T) {
	server, _ := capabilityAdmissionServer(t, 1)
	if response := postStart(t, server, "retained"); response.Code != http.StatusAccepted {
		t.Fatal(response.Body)
	}
	if _, response := readAdmissionCapabilities(t, server); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("cold full node ran unadmitted probes: %d", response.Code)
	}
	stopSession(t, server, "retained")
	if _, response := readAdmissionCapabilities(t, server); response.Code != http.StatusOK {
		t.Fatal(response.Body)
	}
	if response := postStart(t, server, "retained-again"); response.Code != http.StatusAccepted {
		t.Fatal(response.Body)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, signed(t, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil).WithContext(ctx)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("canceled request served fallback: %d", response.Code)
	}
}

func TestCapabilityAdmissionPublishesHashBeforeReturningReport(t *testing.T) {
	server, _ := capabilityAdmissionServer(t, 1)
	server.storeCapabilityHash("previous")
	info, err := server.buildCapabilitySnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if server.storedCapabilityHash() != info.CapabilityHash {
		t.Fatal("completed report returned before matching health hash was published")
	}
}

func TestCapabilityAdmissionChangedConfigurationDuringBuildDoesNotPublish(t *testing.T) {
	server, log := capabilityAdmissionServer(t, 1)
	verified, warm := readAdmissionCapabilities(t, server)
	if warm.Code != http.StatusOK {
		t.Fatal(warm.Body)
	}
	block := log + ".block"
	if err := os.WriteFile(block, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(block) })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, signed(t, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)))
		done <- response
	}()
	awaitCapabilityReceipt(t, log+".started")
	cfg := *server.watcher.Config()
	cfg.Playback.HWDevice = "/changed-during-probe"
	server.watcher.SetConfigForTest(&cfg)
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	response := <-done
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "capability probe unavailable") {
		t.Fatalf("mixed configuration was published: %d %s", response.Code, response.Body)
	}
	if server.storedCapabilityHash() != verified.CapabilityHash {
		t.Fatal("mixed build replaced the previously published hash")
	}
}

func TestCapabilityAdmissionRejectedReprobeKeepsFallback(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			server, _ := capabilityAdmissionServer(t, 1)
			if _, warm := readAdmissionCapabilities(t, server); warm.Code != http.StatusOK {
				t.Fatal(warm.Body)
			}
			if full {
				_, release, admitted := server.acquireJobContext(t.Context(), "retained-compute")
				if !admitted {
					t.Fatal("could not retain compute identity")
				}
				defer release()
				if response := postReprobe(t, server); response.Code != http.StatusServiceUnavailable {
					t.Fatalf("over-limit re-probe=%d", response.Code)
				}
			} else {
				if response := postStart(t, server, "retained"); response.Code != http.StatusAccepted {
					t.Fatal(response.Body)
				}
				if response := postReprobe(t, server); response.Code != http.StatusConflict {
					t.Fatalf("busy re-probe=%d", response.Code)
				}
			}
			if _, response := readAdmissionCapabilities(t, server); response.Code != http.StatusOK {
				t.Fatalf("rejected re-probe discarded valid fallback: %d %s", response.Code, response.Body)
			}
		})
	}
}

func TestCapabilityAdmissionAcceptedReprobeReplacesOrInvalidatesFallback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			server, log := capabilityAdmissionServer(t, 1)
			verified, warm := readAdmissionCapabilities(t, server)
			if warm.Code != http.StatusOK {
				t.Fatal(warm.Body)
			}
			if fail {
				if err := os.WriteFile(log+".fail", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			response := postReprobe(t, server)
			if fail {
				if response.Code != http.StatusServiceUnavailable || server.storedCapabilityHash() != verified.CapabilityHash {
					t.Fatalf("failed re-probe replaced published hash: %d %s", response.Code, response.Body)
				}
				if err := os.Remove(log + ".fail"); err != nil {
					t.Fatal(err)
				}
			} else if response.Code != http.StatusOK {
				t.Fatalf("accepted re-probe: %d %s", response.Code, response.Body)
			}
			if response := postStart(t, server, "retained"); response.Code != http.StatusAccepted {
				t.Fatal(response.Body)
			}
			_, current := readAdmissionCapabilities(t, server)
			want := http.StatusOK
			if fail {
				want = http.StatusServiceUnavailable
			}
			if current.Code != want {
				t.Fatalf("accepted re-probe fallback status=%d want=%d body=%s", current.Code, want, current.Body)
			}
		})
	}
}

func TestCapabilityAdmissionPublicationRechecksAfterRuntimeMetadata(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, cancelRequest := range []bool{false, true} {
			t.Run(fmt.Sprintf("cached=%t/canceled=%t", cached, cancelRequest), func(t *testing.T) {
				server, _ := capabilityAdmissionServer(t, 1)
				verified, warm := readAdmissionCapabilities(t, server)
				if warm.Code != http.StatusOK {
					t.Fatal(warm.Body)
				}
				if cached {
					if response := postStart(t, server, "retained"); response.Code != http.StatusAccepted {
						t.Fatal(response.Body)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				server.nodeRowID = func() (int, bool) {
					if cancelRequest {
						cancel()
					} else {
						server.watcher.Config().Playback.HWDevice = "/changed-during-metadata"
					}
					return 7, true
				}
				response := httptest.NewRecorder()
				server.Handler().ServeHTTP(response, signed(t, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil).WithContext(ctx)))
				if response.Code != http.StatusServiceUnavailable {
					t.Fatalf("metadata callback invalidated publication but report escaped: %d %s", response.Code, response.Body)
				}
				if server.storedCapabilityHash() != verified.CapabilityHash {
					t.Fatal("invalidated metadata publication replaced prior hash")
				}
			})
		}
	}
}

func TestCapabilityAdmissionFailedOrCanceledBuildKeepsPriorFallback(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			server, log := capabilityAdmissionServer(t, 1)
			verified, warm := readAdmissionCapabilities(t, server)
			if warm.Code != http.StatusOK {
				t.Fatal(warm.Body)
			}
			flag := log + ".fail"
			if canceled {
				flag = log + ".block"
			}
			if err := os.WriteFile(flag, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(flag) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				response := httptest.NewRecorder()
				server.Handler().ServeHTTP(response, signed(t, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil).WithContext(ctx)))
				done <- response
			}()
			if canceled {
				awaitCapabilityReceipt(t, log+".started")
				cancel()
			}
			select {
			case response := <-done:
				if response.Code != http.StatusServiceUnavailable {
					t.Fatalf("incomplete build returned %d %s", response.Code, response.Body)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled/failed builder did not return")
			}
			if err := os.Remove(flag); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				server.jobs.mu.Lock()
				identities := len(server.jobs.refs)
				server.jobs.mu.Unlock()
				server.gpu.mu.Lock()
				workers := server.gpu.workers
				server.gpu.mu.Unlock()
				if identities == 0 && workers == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("finished builder retained identities=%d gpu_workers=%d", identities, workers)
				}
				time.Sleep(time.Millisecond)
			}
			if server.storedCapabilityHash() != verified.CapabilityHash {
				t.Fatal("incomplete builder replaced published hash")
			}
			if response := postStart(t, server, "retained"); response.Code != http.StatusAccepted {
				t.Fatal(response.Body)
			}
			current, response := readAdmissionCapabilities(t, server)
			if response.Code != http.StatusOK || current.CapabilityHash != verified.CapabilityHash {
				t.Fatalf("ordinary incomplete build discarded valid fallback: %d %s", response.Code, response.Body)
			}
		})
	}
}
