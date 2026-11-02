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
