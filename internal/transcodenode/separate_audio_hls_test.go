package transcodenode

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

func TestSeparateAudioHLSAcknowledgesItsOwnRendition(t *testing.T) {
	tools := t.TempDir()
	ffmpeg := filepath.Join(tools, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte(`#!/bin/sh
printf '#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\naudio_00000.ts\n#EXTINF:4,\naudio_00001.ts\n#EXTINF:4,\naudio_00002.ts\n#EXT-X-ENDLIST\n' > audio.m3u8
printf audio > audio_00000.ts
printf audio > audio_00001.ts
printf audio > audio_00002.ts
`), 0700); err != nil {
		t.Fatal(err)
	}
	probe := `#!/bin/sh
cat <<'JSON'
{"packets":[{"stream_index":0,"dts_time":"0"},{"stream_index":1,"dts_time":"0"}],"streams":[{"index":0,"codec_type":"video"},{"index":1,"codec_type":"audio"}],"format":{"start_time":"0"}}
JSON
`
	if err := os.WriteFile(filepath.Join(tools, "ffprobe"), []byte(probe), 0700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	session, err := playback.StartTranscode(t.Context(), playback.TranscodeOpts{SessionID: "separate-audio", InputPath: "fixture.mkv", OutputDir: dir, FFmpegPath: ffmpeg, TargetCodecVideo: "copy", TargetCodecAudio: "copy", SeparateAudioHLS: true, SegmentDuration: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	server := newTestServer(t)
	server.sessions["separate-audio"] = session
	server.lastAccess["separate-audio"] = time.Now()
	const name = "audio_00001.ts"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	request := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/transcode/separate-audio/segment/"+name, nil), map[string]string{"session_id": "separate-audio", "name": name})
	request.Header.Set(transcodeproxy.RequestHeader, "1")
	response := httptest.NewRecorder()
	server.handleSegment(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("audio segment status = %d", response.Code)
	}
	ack := withNodeRouteParams(httptest.NewRequest(http.MethodPost, "/transcode/separate-audio/segment/"+name+"/downloaded", nil), map[string]string{"session_id": "separate-audio", "name": name})
	ack.Header.Set(transcodeproxy.GenerationHeader, response.Header().Get(transcodeproxy.GenerationHeader))
	acknowledged := httptest.NewRecorder()
	server.handleSegmentDownloaded(acknowledged, ack)
	if acknowledged.Code != http.StatusNoContent {
		t.Fatalf("audio acknowledgement status = %d", acknowledged.Code)
	}
	if session.LastRequestedSegment() != 0 {
		t.Fatal("audio acknowledgement advanced the video ordinal")
	}
	<-session.Done()
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	recovered := httptest.NewRecorder()
	server.handleSegment(recovered, request.Clone(request.Context()))
	if recovered.Code != http.StatusOK || recovered.Body.String() != "audio" {
		t.Fatalf("evicted audio response = %d %q", recovered.Code, recovered.Body.String())
	}
	if recovered.Header().Get(transcodeproxy.GenerationHeader) == response.Header().Get(transcodeproxy.GenerationHeader) {
		t.Fatal("audio recovery did not replace the generation")
	}
	if session.LastRequestedSegment() != 0 {
		t.Fatal("audio recovery advanced the video ordinal")
	}
}

func TestDVConversionStartRequiresTranscoding(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/transcode/start", strings.NewReader(`{"session_id":"dv-policy","input_path":"fixture.mkv","target_codec_video":"copy","copy_fmp4_recipe_version":"4","separate_audio_hls":true,"dv7_to_dv81":true}`))
	response := httptest.NewRecorder()
	server.handleStart(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "transcoding is disabled") {
		t.Fatalf("disabled conversion response = %d %s", response.Code, response.Body.String())
	}
}
