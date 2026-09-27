package scanner

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestProbeSubtitleContainerIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`"0x3"`, "3"}, {`"0X1a"`, "26"}, {`4`, "4"}, {`"12"`, "12"}, {`"00012"`, "12"}, {`"N/A"`, ""}, {`"0"`, ""}, {`"-1"`, ""}, {`"4294967296"`, ""}, {`null`, ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var raw ffprobeOutput
			if err := json.Unmarshal([]byte(`{"streams":[{"index":7,"id":`+tc.raw+`,"codec_type":"subtitle","codec_name":"mov_text"}],"format":{"format_name":"mov,mp4,m4a"}}`), &raw); err != nil {
				t.Fatal(err)
			}
			file := &models.MediaFile{}
			applyProbeData(file, convertProbeData(&raw), "local")
			if len(file.SubtitleTracks) != 1 || file.SubtitleTracks[0].Index != 7 || file.SubtitleTracks[0].ContainerTrackID != tc.want {
				t.Fatalf("tracks=%+v want container ID %q", file.SubtitleTracks, tc.want)
			}
		})
	}
}
