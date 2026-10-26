package playback

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

func TestNativeHLSCodecUsesInitializationConstraints(t *testing.T) {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(90000, "video", "und")
	entry := mp4.CreateVisualSampleEntryBox("hvc1", 1920, 1080, &mp4.HvcCBox{DecConfRec: hevc.DecConfRec{ConfigurationVersion: 1, GeneralProfileIDC: 2, GeneralProfileCompatibilityFlags: 0x20000000, GeneralConstraintIndicatorFlags: 0x900000000000, GeneralLevelIDC: 153, LengthSizeMinusOne: 3}})
	init.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(entry)
	var data bytes.Buffer
	if err := init.Encode(&data); err != nil {
		t.Fatal(err)
	}
	if got := nativeHLSVideoCodec(data.Bytes()); got != "hvc1.2.4.L153.90" {
		t.Fatalf("codec = %q", got)
	}
	entry.AddChild(mp4.CreateDoViConfigurationBox(1, 0, 8, 6, true, false, true, 1))
	entry.SetType("dvh1")
	data.Reset()
	if err := init.Encode(&data); err != nil {
		t.Fatal(err)
	}
	if got := nativeHLSVideoCodec(data.Bytes()); got != "dvh1.08.06" {
		t.Fatalf("converted codec = %q", got)
	}
}

func TestNativeHLSCodecOmitsIncompleteInitialization(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("invalid")} {
		if got := nativeHLSVideoCodec(data); got != "" {
			t.Fatalf("invalid codec = %q", got)
		}
	}
}

func nativeHLSDVInitFixture(t *testing.T) []byte {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(90000, "video", "und")
	entry := mp4.CreateVisualSampleEntryBox("dvh1", 1920, 1080, &mp4.HvcCBox{DecConfRec: hevc.DecConfRec{ConfigurationVersion: 1, LengthSizeMinusOne: 3}})
	entry.AddChild(mp4.CreateDoViConfigurationBox(1, 0, 8, 6, true, false, true, 1))
	init.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(entry)
	var data bytes.Buffer
	if err := init.Encode(&data); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
