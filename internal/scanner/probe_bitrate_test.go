package scanner

import (
	"encoding/json"
	"testing"
)

// FFmpeg derives a Matroska file's bit_rate from its size and duration, so
// attached fonts would count as stream data. The expectations are the rates
// FFmpeg reports for the same 154.39 s synthetic encode muxed without fonts.
func TestConvertProbeDataExcludesAttachmentsFromBitrate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want int
	}{
		{
			name: "fonts attached",
			json: `{"format":{"duration":"154.390000","size":"168102906","bit_rate":"8710559"},"streams":[
				{"index":0,"codec_type":"video","codec_name":"h264","avg_frame_rate":"24/1"},
				{"index":1,"codec_type":"audio","codec_name":"flac"},
				{"index":2,"codec_type":"attachment","codec_name":"ttf","extradata_size":44997364},
				{"index":3,"codec_type":"attachment","codec_name":"ttf","extradata_size":44997364}]}`,
			want: 4047,
		},
		{
			name: "no attachments",
			json: `{"format":{"duration":"154.390000","size":"78102183","bit_rate":"4047007"},"streams":[
				{"index":0,"codec_type":"video","codec_name":"h264","avg_frame_rate":"24/1"},
				{"index":1,"codec_type":"audio","codec_name":"flac"}]}`,
			want: 4047,
		},
		{
			name: "no format bitrate",
			json: `{"format":{"duration":"154.390000","size":"168102906"},"streams":[
				{"index":0,"codec_type":"video","codec_name":"h264","avg_frame_rate":"24/1"},
				{"index":1,"codec_type":"attachment","codec_name":"ttf","extradata_size":89994728}]}`,
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var raw ffprobeOutput
			if err := json.Unmarshal([]byte(tt.json), &raw); err != nil {
				t.Fatal(err)
			}
			if got := convertProbeData(&raw).Bitrate; got != tt.want {
				t.Fatalf("Bitrate = %d kbps, want %d", got, tt.want)
			}
		})
	}
}
