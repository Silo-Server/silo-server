package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestProbeVirtualSourceFailsClosedWhenH264SafetyScanFails(t *testing.T) {
	ffprobe := writeExecutable(t, "ffprobe", `#!/bin/sh
printf '%s' '{"format":{"format_name":"matroska","duration":"120"},"streams":[{"index":0,"codec_name":"h264","codec_type":"video","width":1920,"height":1080,"color_range":"tv"},{"index":1,"codec_name":"aac","codec_type":"audio","channels":2}]}'
`)
	ffmpeg := writeExecutable(t, "ffmpeg", "#!/bin/sh\nexit 1\n")
	file := &models.MediaFile{ID: 7, FilePath: "virtual://movie/tt7?result=one"}
	probed, err := ProbeVirtualSource(context.Background(), ffprobe, ffmpeg, "http://127.0.0.1/source", file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(probed.VideoTracks) != 1 || !probed.VideoTracks[0].VideoCopyUnsafe || probed.VideoTracks[0].MultiplePPS != nil {
		t.Fatalf("copy safety = %+v, want unknown/unsafe", probed.VideoTracks)
	}
}

func TestProbeVirtualSourceRecordsRemoteDVStripVerdict(t *testing.T) {
	ffprobe := writeExecutable(t, "ffprobe", `#!/bin/sh
printf '%s' '{"format":{"format_name":"matroska","duration":"120"},"streams":[{"index":0,"codec_name":"hevc","codec_type":"video","width":3840,"height":2160,"color_range":"tv","side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":7}]},{"index":1,"codec_name":"eac3","codec_type":"audio","channels":6}]}'
`)
	file := &models.MediaFile{ID: 8, FilePath: "virtual://movie/tt8?result=dv"}
	called := false
	probed, err := ProbeVirtualSource(context.Background(), ffprobe, "", "http://127.0.0.1/source", file, func(_ context.Context, input string) bool {
		called = input == "http://127.0.0.1/source"
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || len(probed.VideoTracks) != 1 || probed.VideoTracks[0].DVRPUStrippable == nil || *probed.VideoTracks[0].DVRPUStrippable {
		t.Fatalf("DV safety verdict was not preserved: %+v", probed.VideoTracks)
	}
}

func writeExecutable(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
