package dovirpu

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Fixtures are dovi_tool's: a 00 00 00 01 start code, then the escaped RPU.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b[4:]
}

// dovi_tool ships no direct FEL → 8.1 pair (its fel_to_81.bin goes through
// MEL first and keeps the mapping), so FEL output is checked by the corpus
// test; here it only has to convert into a Profile 8 RPU.
func TestConvertRPUMatchesDoviTool(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"mel_orig.bin", "mel_to_81.bin"},
		{"fel_orig.bin", ""},
	} {
		got, err := ConvertRPU(fixture(t, tc.in))
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if tc.want != "" {
			if want := fixture(t, tc.want); !bytes.Equal(got, want) {
				t.Errorf("%s:\n got %s\nwant %s", tc.in, hex.EncodeToString(got), hex.EncodeToString(want))
			}
		}
		if _, err := ConvertRPU(got); !errors.Is(err, ErrNotProfile7) {
			t.Errorf("%s: converting again: %v, want ErrNotProfile7", tc.in, err)
		}
	}
}

// TestConvertRPUCorpus compares against dovi_tool -m 2 on real discs. Set
// SILO_DOVI_CORPUS to a directory of <name>/p7.bin and <name>/p81.bin pairs
// from `dovi_tool extract-rpu` (without and with -m 2).
func TestConvertRPUCorpus(t *testing.T) {
	dir := os.Getenv("SILO_DOVI_CORPUS")
	if dir == "" {
		t.Skip("SILO_DOVI_CORPUS not set")
	}
	pairs, _ := filepath.Glob(filepath.Join(dir, "*", "p7.bin"))
	if len(pairs) == 0 {
		t.Fatalf("no */p7.bin under %s", dir)
	}
	for _, p7 := range pairs {
		in := splitRPUs(t, p7)
		want := splitRPUs(t, filepath.Join(filepath.Dir(p7), "p81.bin"))
		if len(in) != len(want) {
			t.Fatalf("%s: %d RPUs, %d converted", p7, len(in), len(want))
		}
		for i := range in {
			got, err := ConvertRPU(in[i])
			if err != nil {
				t.Fatalf("%s frame %d: %v", p7, i, err)
			}
			if !bytes.Equal(got, want[i]) {
				t.Fatalf("%s frame %d:\n got %s\nwant %s", p7, i, hex.EncodeToString(got), hex.EncodeToString(want[i]))
			}
		}
		t.Logf("%s: %d frames match", p7, len(in))
	}
}

func splitRPUs(t *testing.T, path string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, p := range bytes.Split(b, []byte{0, 0, 0, 1}) {
		if len(p) > 0 {
			out = append(out, p)
		}
	}
	return out
}

func FuzzConvertRPU(f *testing.F) {
	for _, name := range []string{"mel_orig.bin", "fel_orig.bin", "mel_to_81.bin"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b[4:])
	}
	f.Fuzz(func(t *testing.T, rpu []byte) {
		_, _ = ConvertRPU(rpu)
	})
}

// A garbage polynomial order must fail at once, not loop over it.
func TestConvertRPURejectsHugePolynomialOrder(t *testing.T) {
	w := bitWriter{}
	w.bits(2, 6)   // rpu_type
	w.bits(18, 11) // rpu_format
	w.bits(1, 4)   // vdr_rpu_profile
	w.bits(0, 4)   // vdr_rpu_level
	w.flag(true)   // vdr_seq_info_present_flag
	w.flag(false)  // chroma_resampling_explicit_filter_flag
	w.bits(0, 2)   // coefficient_data_type
	w.ue(23)       // coefficient_log2_denom
	w.bits(1, 2)   // vdr_rpu_normalized_idc
	w.flag(false)  // bl_video_full_range_flag
	w.ue(2)        // bl_bit_depth_minus8
	w.ue(2)        // el_bit_depth_minus8
	w.ue(4)        // vdr_bit_depth_minus8
	w.flag(false)  // spatial_resampling_filter_flag
	w.bits(0, 3)   // reserved_zero_3bits
	w.flag(true)   // el_spatial_resampling_filter_flag
	w.flag(false)  // disable_residual_flag
	w.flag(false)  // vdr_dm_metadata_present_flag
	w.flag(false)  // use_prev_vdr_rpu_flag
	w.ue(0)        // vdr_rpu_id
	w.ue(0)        // mapping_color_space
	w.ue(0)        // mapping_chroma_format_idc
	for range 3 {
		w.ue(0)
		w.bits(0, 10)
		w.bits(1023, 10)
	}
	w.bits(0, 3)     // nlq_method_idc
	w.bits(0, 10)    // nlq_pred_pivot_value
	w.bits(1023, 10) // nlq_pred_pivot_value
	w.ue(0)          // num_x_partitions_minus1
	w.ue(0)          // num_y_partitions_minus1
	w.ue(0)          // mapping_idc: polynomial
	w.ue(1<<32 - 1)  // poly_order_minus1
	w.align()
	body := append([]byte{0x19}, w.out...)
	rpu := binary.BigEndian.AppendUint32(body, crc32MPEG2(w.out))
	rpu = append(rpu, 0x80)
	if _, err := ConvertRPU(escape(rpu)); err == nil || errors.Is(err, ErrNotProfile7) {
		t.Fatalf("err = %v, want a parse error", err)
	}
}
