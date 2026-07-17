package scanner

import "testing"

// annexB wraps NAL payloads with 4-byte start codes.
func annexB(nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		out = append(out, 0x00, 0x00, 0x00, 0x01)
		out = append(out, n...)
	}
	return out
}

// pps builds a PPS NAL (header 0x68) from the given RBSP payload bytes.
func pps(rbsp ...byte) []byte {
	return append([]byte{0x68}, rbsp...)
}

func TestAnnexBHasConflictingPPS(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{
			name: "single pps repeated is safe",
			// 0xee -> pic_parameter_set_id=0
			data: annexB(pps(0xee, 0x35, 0x25), pps(0xee, 0x35, 0x25), pps(0xee, 0x35, 0x25)),
			want: false,
		},
		{
			name: "same id redefined with different content is unsafe",
			// both decode to pic_parameter_set_id=0 (leading bit set) but differ.
			data: annexB(pps(0xee, 0x35, 0x25), pps(0xe9, 0x23, 0x52, 0x50)),
			want: true,
		},
		{
			name: "distinct ids each defined once is safe",
			// 0xe8 -> id 0 ; 0x48 -> id 1 ; 0x28 -> id 4
			data: annexB(pps(0xe8), pps(0x48), pps(0x28)),
			want: false,
		},
		{
			name: "empty stream is safe",
			data: nil,
			want: false,
		},
		{
			name: "non-pps nal ignored",
			// 0x65 = IDR slice (type 5), not a PPS.
			data: annexB([]byte{0x65, 0x88, 0x84}, pps(0xee, 0x35)),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := annexBHasConflictingPPS(tt.data); got != tt.want {
				t.Fatalf("annexBHasConflictingPPS = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPPSParameterSetID(t *testing.T) {
	// pic_parameter_set_id is the first ue(v) of the RBSP.
	cases := map[byte]uint{
		0xe8: 0, // 1.... -> 0
		0x48: 1, // 010.. -> 1
		0x68: 2, // 011.. -> 2
		0x20: 3, // 00100 -> 3
		0x28: 4, // 00101 -> 4
	}
	for first, want := range cases {
		if got, ok := ppsParameterSetID([]byte{first, 0x00}); !ok || got != want {
			t.Errorf("ppsParameterSetID(0x%02x) = %d (ok=%v), want %d", first, got, ok, want)
		}
	}
}
