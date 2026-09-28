package mediasample

import (
	"reflect"
	"testing"
)

func TestDecodeRawFingerprintKeepsBinaryWhitespaceBytes(t *testing.T) {
	output := []byte{0x20, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x0a}

	points := DecodeRawFingerprint(output)

	want := []uint32{0x20, 0x0a000000}
	if !reflect.DeepEqual(points, want) {
		t.Fatalf("DecodeRawFingerprint() = %#v, want %#v", points, want)
	}
}

func TestRawFingerprintRoundTrip(t *testing.T) {
	points := []uint32{0, 1, 0xdeadbeef, 0xffffffff}
	if got := DecodeRawFingerprint(EncodeRawFingerprint(points)); !reflect.DeepEqual(got, points) {
		t.Fatalf("round trip = %#v, want %#v", got, points)
	}
	if got := DecodeRawFingerprint([]byte{1, 2, 3}); got != nil {
		t.Fatalf("partial point decoded to %#v, want nil", got)
	}
}
