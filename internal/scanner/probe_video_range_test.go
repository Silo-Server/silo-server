package scanner

import (
	"encoding/json"
	"testing"
)

// TestProbePipelineDistinguishesMissingAndExplicitZeroCompatibilityID verifies absent and zero values remain distinct.
func TestProbePipelineDistinguishesMissingAndExplicitZeroCompatibilityID(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		present bool
	}{
		{name: "missing"},
		{name: "explicit zero", field: `,"dv_bl_signal_compatibility_id":0`, present: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawJSON := `{"streams":[{"codec_type":"video","codec_name":"hevc","side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":8` + tt.field + `}]}]}`
			var raw ffprobeOutput
			if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
				t.Fatal(err)
			}
			track := convertProbeData(&raw).VideoTracks[0]
			if track.DVBLCompatIDPresent != tt.present {
				t.Fatalf("DVBLCompatIDPresent = %t, want %t", track.DVBLCompatIDPresent, tt.present)
			}
		})
	}
}
