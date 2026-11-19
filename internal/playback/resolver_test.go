package playback_test

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestNormalizeAndValidateVideoDecode(t *testing.T) {
	tests := []struct {
		name    string
		caps    playback.ClientCapabilities
		wantErr bool
	}{
		{
			name: "declared evidence with flat lists only",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.EvidenceDeclaredV3,
				CodecsVideo:   []string{"h264"},
				CodecsAudio:   []string{"aac"},
				Containers:    []string{"mp4"},
			},
		},
		{
			name: "legacy flat payload",
			caps: playback.ClientCapabilities{CodecsVideo: []string{"h264"}},
		},
		{
			name: "platform attested entries",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.EvidencePlatformAttestedV3,
				CodecsVideo:   []string{"H264"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "H264", MaxWidth: 1920, MaxHeight: 1080, Hardware: true,
				}},
			},
		},
		{
			name: "entries with declared evidence",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.EvidenceDeclaredV3,
				CodecsVideo:   []string{"av1"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "av1", Hardware: true,
				}},
			},
			wantErr: true,
		},
		{
			name: "entries without evidence",
			caps: playback.ClientCapabilities{
				CodecsVideo: []string{"av1"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "av1", Hardware: true,
				}},
			},
			wantErr: true,
		},
		{
			name: "malformed entry",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.EvidencePlatformAttestedV3,
				CodecsVideo:   []string{"av1"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "av1", MaxWidth: -1, Hardware: true,
				}},
			},
			wantErr: true,
		},
		{
			// A typo'd tier on a flat payload is a client bug the v3 playback
			// path rejects outright. Silently resolving it from the flat lists
			// here would hide the bug behind a working-looking answer.
			name: "unknown evidence tier without entries",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.CapabilityEvidenceV3("exat"),
				CodecsVideo:   []string{"h264"},
				CodecsAudio:   []string{"aac"},
				Containers:    []string{"mp4"},
			},
			wantErr: true,
		},
		{
			name: "unknown evidence tier with entries",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.CapabilityEvidenceV3("platform-attested"),
				CodecsVideo:   []string{"av1"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "av1", MaxWidth: 1920, MaxHeight: 1080, Hardware: true,
				}},
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := tc.caps
			err := caps.NormalizeAndValidateVideoDecode()
			if tc.wantErr {
				if err == nil {
					t.Fatal("err = nil, want a validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}
