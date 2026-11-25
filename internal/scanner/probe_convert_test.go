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

// FFprobe reports subtitle flags under disposition; Matroska never writes the
// hearing-impaired flag as a stream tag.
func TestConvertProbeDataReadsSubtitleDispositions(t *testing.T) {
	t.Parallel()

	const probe = `{"format":{"duration":"10.0"},"streams":[
		{"index":0,"codec_type":"video","codec_name":"h264"},
		{"index":2,"codec_type":"subtitle","codec_name":"subrip","disposition":{"default":0,"forced":0,"hearing_impaired":1},"tags":{"language":"eng"}},
		{"index":3,"codec_type":"subtitle","codec_name":"subrip","disposition":{"default":1,"forced":1,"hearing_impaired":0},"tags":{"language":"eng"}}]}`
	var raw ffprobeOutput
	if err := json.Unmarshal([]byte(probe), &raw); err != nil {
		t.Fatal(err)
	}

	tracks := convertProbeData(&raw).SubtitleTracks
	if len(tracks) != 2 {
		t.Fatalf("got %d subtitle tracks, want 2", len(tracks))
	}
	want := []struct{ hearingImpaired, forced, isDefault bool }{
		{hearingImpaired: true},
		{forced: true, isDefault: true},
	}
	for i, w := range want {
		got := tracks[i]
		if got.HearingImpaired != w.hearingImpaired || got.Forced != w.forced || got.Default != w.isDefault {
			t.Errorf("track %d: hearing_impaired=%v forced=%v default=%v, want %v %v %v",
				got.Index, got.HearingImpaired, got.Forced, got.Default, w.hearingImpaired, w.forced, w.isDefault)
		}
	}
}
