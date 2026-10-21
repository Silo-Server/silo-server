package bandwidthprobe

import (
	"bytes"
	"testing"
)

func TestWriteSendsExactlySizeBytes(t *testing.T) {
	for _, size := range []int{1, 32768, 100_000} {
		var buf bytes.Buffer
		if err := Write(&buf, size); err != nil {
			t.Fatalf("Write(%d): %v", size, err)
		}
		if buf.Len() != size {
			t.Fatalf("Write(%d) wrote %d bytes", size, buf.Len())
		}
	}
}

func TestWriteIsNotCompressible(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, 4096); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(buf.Bytes(), buf.Bytes()[:1]) > 256 {
		t.Fatal("probe payload repeats one byte value; a compressing proxy would shrink it")
	}
}
