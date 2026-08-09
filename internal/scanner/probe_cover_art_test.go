package scanner

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Embedded cover art is reported by ffprobe as a video stream carrying
// disposition.attached_pic. It must never become a video track: doing so
// misroutes audio-only files into the video planner, and — when the picture is
// ordered ahead of the real stream — makes the flat codec/resolution columns
// describe the poster instead of the movie.
func TestConvertProbeDataExcludesCoverArt(t *testing.T) {
	tests := []struct {
		name           string
		baseType       string
		rawJSON        string
		wantVideoCount int
		wantCodecVideo string
		wantResolution string
		wantAudioOnly  bool
	}{
		{
			name:     "audiobook with embedded cover stays audio-only",
			baseType: "audiobook",
			rawJSON: `{"streams":[
				{"codec_type":"video","codec_name":"mjpeg","width":500,"height":500,"disposition":{"attached_pic":1}},
				{"codec_type":"audio","codec_name":"aac","channels":2}
			]}`,
			wantVideoCount: 0,
			wantCodecVideo: "",
			wantResolution: "",
			wantAudioOnly:  true,
		},
		{
			name: "cover art ordered first does not poison the flat video columns",
			rawJSON: `{"streams":[
				{"codec_type":"video","codec_name":"mjpeg","width":480,"height":480,"disposition":{"attached_pic":1}},
				{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"color_range":"tv"},
				{"codec_type":"audio","codec_name":"aac","channels":2}
			]}`,
			wantVideoCount: 1,
			wantCodecVideo: "h264",
			wantResolution: "1080p",
			wantAudioOnly:  false,
		},
		{
			name: "a genuine MJPEG video is not mistaken for cover art",
			rawJSON: `{"streams":[
				{"codec_type":"video","codec_name":"mjpeg","width":320,"height":182},
				{"codec_type":"audio","codec_name":"vorbis","channels":2}
			]}`,
			wantVideoCount: 1,
			wantCodecVideo: "mjpeg",
			wantResolution: "480p",
			wantAudioOnly:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw ffprobeOutput
			if err := json.Unmarshal([]byte(tt.rawJSON), &raw); err != nil {
				t.Fatalf("unmarshal ffprobe output: %v", err)
			}

			file := &models.MediaFile{BaseType: tt.baseType}
			applyProbeData(file, convertProbeData(&raw), "local")

			if got := len(file.VideoTracks); got != tt.wantVideoCount {
				t.Errorf("VideoTracks length = %d, want %d", got, tt.wantVideoCount)
			}
			if file.CodecVideo != tt.wantCodecVideo {
				t.Errorf("CodecVideo = %q, want %q", file.CodecVideo, tt.wantCodecVideo)
			}
			if file.Resolution != tt.wantResolution {
				t.Errorf("Resolution = %q, want %q", file.Resolution, tt.wantResolution)
			}
			if got := file.IsAudioOnly(); got != tt.wantAudioOnly {
				t.Errorf("IsAudioOnly() = %v, want %v", got, tt.wantAudioOnly)
			}
		})
	}
}
