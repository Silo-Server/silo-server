package embeddedsub

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func be32(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.BigEndian.AppendUint32(b, x)
	}
	return b
}

// textSample is a tx3g sample: a 16-bit length and the text.
func textSample(s string) []byte {
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(s))), s...)
}

// testMP4Track is a 3GPP text track: movie timescale 1000, media timescale
// one microsecond, English, an edit list that starts it 2 s in, and a
// sample description whose display flags mark every sample forced.
func testMP4Track(elst []byte) *mp4Track {
	matrix := be32(0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000)
	mvhd := slices.Concat(be32(0, 0, 0, 1000, 60_000, 0x10000), []byte{1, 0}, make([]byte, 10), matrix, make([]byte, 24), be32(8))
	tkhd := slices.Concat(be32(3, 0, 0, 7, 0, 60_000, 0, 0), []byte{0, 0, 0, 0, 0, 0, 0, 0}, matrix, be32(0, 0))
	mdhd := slices.Concat(be32(0, 0, 0, 1_000_000, 60_000_000), []byte{0x15, 0xC7, 0, 0})
	hdlr := slices.Concat(be32(0, 0), []byte("sbtl"), make([]byte, 12), []byte("SubtitleHandler\x00"))
	tx3g := slices.Concat(make([]byte, 6), []byte{0, 1}, be32(0x80000000), []byte{1, 0xFF}, be32(0),
		be32(0, 0), []byte{0, 0, 0, 0, 0, 1, 0, 18}, be32(0xFFFFFFFF), mp4Box("ftab", []byte{0, 1, 0, 1, 5}, []byte("Serif")))
	m := &mp4Track{
		ftyp: mp4Box(mp4BoxFtyp, []byte("isom\x00\x00\x02\x00isomiso2mp41")),
		mvhd: mp4Box(mp4BoxMvhd, mvhd), tkhd: mp4Box("tkhd", tkhd), mdhd: mp4Box("mdhd", mdhd), hdlr: mp4Box("hdlr", hdlr),
		stsd: mp4FullBox("stsd", slices.Concat(be32(1), mp4Box("tx3g", tx3g))),
	}
	if elst != nil {
		m.edts = mp4Box("edts", mp4FullBox("elst", elst))
	}
	return m
}

func TestMP4RoundTrip(t *testing.T) {
	// An empty edit of 2 s, then the media from its start.
	m := testMP4Track(be32(2, 2000, 0xFFFFFFFF, 0x10000, 60_000, 0, 0x10000))
	packets := []Packet{
		{Data: textSample("one"), mp4DTS: 500_000, mp4Delta: 1_000_000},
		{Data: textSample("two {\\an8}"), mp4DTS: 1_500_000, mp4Delta: 500_000},
		{Data: textSample("three"), mp4DTS: 5_000_000, mp4Delta: 2_000_000},
	}
	var file bytes.Buffer
	if err := writeMP4(&file, &Track{mp4: m}, packets); err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, file.Bytes())
	ctx := context.Background()
	idx, err := ReadIndex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := idx.Track(7)
	// The gaps before "one" and "three" become empty samples.
	if !ok || tr.CodecID != "tx3g" || tr.Language != "eng" || !tr.Forced || !tr.Enabled ||
		tr.Completeness != CompletenessComplete || tr.FrameCount != 5 || tr.Unsupported != "" {
		t.Fatalf("track = %+v", tr)
	}
	text := func(ps []Packet) []wantPacket {
		var out []wantPacket
		for _, p := range ps {
			if len(p.Data) > 2 {
				out = append(out, wantPacket{p.PTS, p.Duration, string(p.Data[2:])})
			}
		}
		return out
	}
	res, err := ReadPackets(ctx, path, idx, 7, WholeFrom, WholeTo, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []wantPacket{{ms(2500), time.Second, "one"}, {ms(3500), ms(500), "two {\\an8}"}, {ms(7000), 2 * time.Second, "three"}}
	if got := text(res.Packets); !slices.Equal(got, want) {
		t.Fatalf("packets:\n got %+v\nwant %+v", got, want)
	}
	if len(res.Packets) != 5 || !HasOverrideTags(res.Packets) {
		t.Fatalf("%d packets", len(res.Packets))
	}
	// Neighboring samples come in one read.
	if res.Stats.Reads != 1 {
		t.Fatalf("whole track took %d reads", res.Stats.Reads)
	}
	res, err = ReadPackets(ctx, path, idx, 7, ms(4100), ms(6900), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(res.Packets); len(got) != 0 {
		t.Fatalf("gap window = %+v", got)
	}
	res, err = ReadPackets(ctx, path, idx, 7, ms(3000), ms(3600), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(res.Packets); !slices.Equal(got, want[:2]) {
		t.Fatalf("window = %+v", got)
	}

	// A window's packets written out keep their decode times.
	var again bytes.Buffer
	if err := writeMP4(&again, &Track{mp4: m}, res.Packets[len(res.Packets)-1:]); err != nil {
		t.Fatal(err)
	}
	path = writeFixture(t, again.Bytes())
	if idx, err = ReadIndex(ctx, path); err != nil {
		t.Fatal(err)
	}
	if res, err = ReadPackets(ctx, path, idx, 7, WholeFrom, WholeTo, ReadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := text(res.Packets); !slices.Equal(got, want[1:2]) {
		t.Fatalf("rewritten window = %+v", got)
	}
}

// A timed-out cache load still owns its movie buffer until the read and
// parsing finish. Distinct caches must share construction capacity, and a
// refused load must be retryable when that capacity is returned.
func TestMP4IndexBoundsDetachedConstruction(t *testing.T) {
	var file bytes.Buffer
	if err := writeMP4(&file, &Track{mp4: testMP4Track(nil)}, []Packet{{Data: textSample("x"), mp4Delta: 1000}}); err != nil {
		t.Fatal(err)
	}
	data := file.Bytes()
	moovOffset := int64(binary.BigEndian.Uint32(data))
	moovSize := int(binary.BigEndian.Uint32(data[moovOffset:]))
	for _, failRead := range []bool{false, true} {
		name := "success"
		if failRead {
			name = "read failure"
		}
		t.Run(name, func(t *testing.T) {
			previous := mp4IndexBytes
			// Two small movies fit, including the conservative sample-table
			// allowance, without allocating large movie buffers in the test.
			mp4IndexBytes = newBudget(60 << 20)
			defer func() { mp4IndexBytes = previous }()
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer func() {
					unblock()
					synctest.Wait()
				}()
				var reads atomic.Int32
				reader := mp4ConstructionReader{readAt: func(p []byte, off int64) (int, error) {
					if off == moovOffset && len(p) == moovSize {
						reads.Add(1)
						<-release // an in-progress filesystem read ignores cancellation
						if failRead {
							return 0, io.ErrUnexpectedEOF
						}
					}
					return bytes.NewReader(data).ReadAt(p, off)
				}}
				var caches []*Cache
				var paths []string
				for range 3 {
					path := writeFixture(t, data)
					cache := NewCache(1 << 20)
					cache.readIndex = func(context.Context, string) (*Index, error) {
						// Keep only the top-level header in hand so the parser
						// reads the movie through the controlled ReaderAt.
						return readMP4Index(reader, int64(len(data)), data[:16])
					}
					caches, paths = append(caches, cache), append(paths, path)
				}
				for i := range 2 {
					ctx, cancel := context.WithCancel(t.Context())
					done := make(chan error, 1)
					go func() {
						_, err := caches[i].Index(ctx, paths[i])
						done <- err
					}()
					synctest.Wait()
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatalf("abandoned load %d: %v", i, err)
					}
				}
				// Let the shared load deadline expire as well as the callers.
				if _, err := caches[0].Index(t.Context(), paths[0]); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("shared load deadline: %v", err)
				}
				if _, err := caches[2].Index(t.Context(), paths[2]); !errors.Is(err, errIndexReadBusy) {
					t.Fatalf("construction beyond byte limit: %v, want busy", err)
				}
				if got := reads.Load(); got != 2 {
					t.Fatalf("started %d movie reads, want 2", got)
				}
				unblock()
				synctest.Wait()
				if got := mp4IndexBytes.left.Load(); got != 60<<20 {
					t.Fatalf("construction capacity after workers finish: %d", got)
				}
				// The busy result was not cached or recorded as unsupported.
				if idx, err := caches[2].Index(t.Context(), paths[2]); failRead {
					if !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf("retry after read failure: %v", err)
					}
				} else if err != nil || len(idx.Tracks) != 1 || idx.Tracks[0].Unsupported != "" {
					t.Fatalf("retry after capacity returns: %+v, %v", idx, err)
				}
				if got := mp4IndexBytes.left.Load(); got != 60<<20 {
					t.Fatalf("construction capacity after retry: %d", got)
				}
			})
		})
	}
}

type mp4ConstructionReader struct {
	readAt func([]byte, int64) (int, error)
}

func (r mp4ConstructionReader) ReadAt(p []byte, off int64) (int, error) {
	return r.readAt(p, off)
}

func TestMP4TracksTheReaderRefuses(t *testing.T) {
	tests := map[string]struct {
		elst, stsd  []byte
		unsupported string
	}{
		"edit at double speed": {elst: be32(1, 60_000, 0, 0x20000), unsupported: unsupportedEditList},
		"two media edits":      {elst: be32(2, 1000, 0, 0x10000, 1000, 5000, 0x10000), unsupported: unsupportedEditList},
		"WebVTT in MP4":        {stsd: mp4FullBox("stsd", slices.Concat(be32(1), mp4Box("wvtt", make([]byte, 8)))), unsupported: `MP4 sample entry "wvtt"`},
		// A real sample description is a few hundred bytes.
		"oversized sample description": {stsd: mp4FullBox("stsd", slices.Concat(be32(1), mp4Box("tx3g", make([]byte, maxMP4KeptBox)))), unsupported: unsupportedKeptBoxes},
	}
	for name, tt := range tests {
		m := testMP4Track(tt.elst)
		if tt.stsd != nil {
			m.stsd = tt.stsd
		}
		var file bytes.Buffer
		if err := writeMP4(&file, &Track{mp4: m}, []Packet{{Data: textSample("x"), mp4Delta: 1000}}); err != nil {
			t.Fatal(err)
		}
		idx, err := ReadIndex(context.Background(), writeFixture(t, file.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if tr, ok := idx.Track(7); !ok || tr.Unsupported != tt.unsupported {
			t.Errorf("%s: track = %+v", name, tr)
		}
	}
}

func TestExpandSamplesRejectsOffsetsPastTheFile(t *testing.T) {
	stbl := stblBoxes{
		stts: be32(0, 1, 1, 1000), stsc: be32(0, 1, 1, 1, 1), stsz: be32(0, 0x200, 1),
		// A chunk offset whose end overflows int64.
		stco: slices.Concat(be32(0, 1), be32(0x7FFFFFFF, 0xFFFFFF00)), co64: true,
	}
	left := maxMP4Samples
	if _, err := expandSamples(stbl, 1<<20, &left); err == nil {
		t.Fatal("a sample past the end of the file was listed")
	}
}

// FFmpeg drops the samples that start at or after a media edit's end and
// keeps one that runs past it whole, and so does the reader.
func TestMP4EditListEndDropsLaterSamples(t *testing.T) {
	m := testMP4Track(be32(1, 1, 0, 0x10000)) // the edit ends 1 ms in
	packets := []Packet{
		{Data: textSample("runs past the end"), mp4Delta: 2000},
		{Data: textSample("starts after it"), mp4DTS: 2000, mp4Delta: 1000},
	}
	var file bytes.Buffer
	if err := writeMP4(&file, &Track{mp4: m}, packets); err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, file.Bytes())
	idx, err := ReadIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ReadPackets(context.Background(), path, idx, 7, WholeFrom, WholeTo, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Packets) != 1 || res.Packets[0].Duration != 2*time.Millisecond {
		t.Fatalf("packets = %+v, want the first, whole", res.Packets)
	}
}

// Uniform sample tables are cheap to declare, so the samples of all an
// index's text tracks share one budget, checked before a table is expanded.
func TestMP4IndexBoundsSamplesAcrossTracks(t *testing.T) {
	var file bytes.Buffer
	if err := writeMP4(&file, &Track{mp4: testMP4Track(nil)}, []Packet{{Data: textSample("x"), mp4Delta: 1000}}); err != nil {
		t.Fatal(err)
	}
	var ftyp, mvhd, trak []byte
	walkBoxes(file.Bytes(), func(kind string, box, payload []byte) bool {
		switch kind {
		case mp4BoxFtyp:
			ftyp = box
		case mp4BoxMoov:
			walkBoxes(payload, func(kind string, box, _ []byte) bool {
				switch kind {
				case mp4BoxMvhd:
					mvhd = box
				case mp4BoxTrak:
					trak = bytes.Clone(box)
				}
				return true
			})
		}
		return true
	})
	// The track declares 600,000 one-byte samples in its stsz, in place.
	at := bytes.Index(trak, []byte("stsz"))
	if ftyp == nil || mvhd == nil || at < 0 {
		t.Fatal("written MP4 lacks its boxes")
	}
	copy(trak[at+8:], be32(1, 600_000))
	movie := func(traks int) string {
		return writeFixture(t, slices.Concat(ftyp, mp4Box(mp4BoxMoov, append([][]byte{mvhd}, slices.Repeat([][]byte{trak}, traks)...)...)))
	}
	if _, err := ReadIndex(context.Background(), movie(1)); errors.Is(err, ErrMalformed) {
		t.Fatalf("one track within the budget: err = %v", err)
	}
	if _, err := ReadIndex(context.Background(), movie(2)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("two tracks over the budget: err = %v, want ErrMalformed", err)
	}
	if _, err := ReadIndex(context.Background(), movie(maxTrackEntries+1)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("%d tracks: err = %v, want ErrMalformed", maxTrackEntries+1, err)
	}
}

// What an index's tracks keep of their boxes is bounded in all: tracks past
// maxMP4KeptBytes keep nothing and are unsupported, and the index still reads.
func TestMP4IndexBoundsKeptBoxesAcrossTracks(t *testing.T) {
	m := testMP4Track(nil)
	m.stsd = mp4FullBox("stsd", slices.Concat(be32(1), mp4Box("tx3g", make([]byte, maxMP4KeptBox-4096))))
	var file bytes.Buffer
	if err := writeMP4(&file, &Track{mp4: m}, []Packet{{Data: textSample("x"), mp4Delta: 1000}}); err != nil {
		t.Fatal(err)
	}
	var ftyp, mvhd, trak []byte
	walkBoxes(file.Bytes(), func(kind string, box, payload []byte) bool {
		switch kind {
		case mp4BoxFtyp:
			ftyp = box
		case mp4BoxMoov:
			walkBoxes(payload, func(kind string, box, _ []byte) bool {
				switch kind {
				case mp4BoxMvhd:
					mvhd = box
				case mp4BoxTrak:
					trak = box
				}
				return true
			})
		}
		return true
	})
	traks := 2 * maxMP4KeptBytes / len(trak)
	idx, err := ReadIndex(context.Background(), writeFixture(t, slices.Concat(ftyp, mp4Box(mp4BoxMoov, append([][]byte{mvhd}, slices.Repeat([][]byte{trak}, traks)...)...))))
	if err != nil {
		t.Fatal(err)
	}
	kept, refused := 0, 0
	for i := range idx.Tracks {
		tr := &idx.Tracks[i]
		if tr.Unsupported == unsupportedKeptBoxes {
			refused++
			if tr.mp4.boxBytes() != len(ftyp)+len(mvhd) || len(tr.CodecPrivate) != 0 {
				t.Fatalf("a refused track kept %d bytes of boxes", tr.mp4.boxBytes())
			}
			continue
		}
		kept += tr.mp4.boxBytes() - len(ftyp) - len(mvhd)
	}
	if refused == 0 || kept > maxMP4KeptBytes {
		t.Fatalf("%d tracks refused, %d bytes kept; want refusals and at most %d", refused, kept, maxMP4KeptBytes)
	}
}

func TestMP4WriteBoundsSyntheticSamples(t *testing.T) {
	// An edit can map this decode time near zero on the presentation
	// timeline. One tiny window must not expand it to a million samples.
	for _, tt := range []struct {
		name         string
		gaps         []int64
		wantTooLarge bool
	}{
		{"one large gap", []int64{1<<20 - 1}, true},
		{"gaps share the limit", []int64{2049, 2049}, true},
		{"gaps at the limit", []int64{2048, 2048}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var packets []Packet
			var now int64
			for _, gap := range tt.gaps {
				now += gap * math.MaxUint32
				packets = append(packets, Packet{Data: textSample("x"), mp4DTS: now, mp4Delta: 1000})
				now += 1000
			}
			err := writeMP4(io.Discard, &Track{mp4: testMP4Track(nil)}, packets)
			if tt.wantTooLarge {
				if !errors.Is(err, ErrTooLarge) {
					t.Fatalf("write error = %v, want ErrTooLarge", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
