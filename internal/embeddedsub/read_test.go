package embeddedsub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

const (
	videoTrack     = 1
	textTrack      = 3
	forcedTrack    = 4
	pgsTrack       = 5
	assTrack       = 6
	strippedTrack  = 7
	lacedTrack     = 8
	scannedTrack   = 9
	uncuedTrack    = 10
	defaultTrack   = 11
	lzoTrack       = 12
	encryptedTrack = 13
)

var assHeader = []byte("[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname\nStyle: Default,Arial\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

// videoBlock is a keyframe of the video track: data no subtitle read
// should touch.
func videoBlock(rel int16, size int) fixtureBlock {
	return fixtureBlock{track: videoTrack, rel: rel, simple: true, frames: [][]byte{bytes.Repeat([]byte{0xAA}, size)}, cue: cueNone}
}

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

// libraryFixture is one file with a track for each layout the reader
// handles. Large video blocks sit between the subtitle blocks.
func libraryFixture(t testing.TB) mkvFixture {
	pgs := zlibBytes(t, bytes.Repeat([]byte{0x16, 0x00, 0x03, 1, 2, 3}, 2000))
	assEvent := func(n int, text string) []byte {
		return zlibBytes(t, []byte(string(rune('0'+n))+",0,Default,,0,0,0,,"+text))
	}
	return mkvFixture{
		tracks: []fixtureTrack{
			{number: videoTrack, uid: 0x11, kind: matroska.TrackTypeVideo, codec: "V_MPEG4/ISO/AVC"},
			{number: textTrack, uid: 0x33, codec: "S_TEXT/UTF8"},
			{number: forcedTrack, uid: 0x44, codec: "S_TEXT/UTF8", extra: [][]byte{
				strEl(matroska.IDLanguage, "ger"), strEl(matroska.IDLanguageBCP47, "de-CH"), strEl(matroska.IDName, "Forced"),
				uintEl(matroska.IDFlagForced, 1), uintEl(matroska.IDFlagDefault, 0), uintEl(matroska.IDFlagHearingImpaired, 1)}},
			{number: pgsTrack, uid: 0x55, codec: "S_HDMV/PGS", extra: [][]byte{encodingEl(1, matroska.CompressionZlib, nil)}},
			{number: assTrack, uid: 0x66, codec: "S_TEXT/ASS", private: zlibBytes(t, assHeader),
				extra: [][]byte{encodingEl(3, matroska.CompressionZlib, nil)}},
			{number: strippedTrack, uid: 0x77, codec: "S_TEXT/UTF8", extra: [][]byte{encodingEl(1, matroska.CompressionHeaderStripping, []byte("Line: "))}},
			{number: lacedTrack, uid: 0x88, codec: "S_TEXT/UTF8"},
			{number: scannedTrack, uid: 0x99, codec: "S_TEXT/UTF8"},
			{number: uncuedTrack, uid: 0xAA, codec: "S_TEXT/UTF8"},
			{number: defaultTrack, uid: 0xBB, codec: "S_TEXT/UTF8", extra: [][]byte{uintEl(matroska.IDDefaultDuration, 1_500_000_000)}},
			{number: lzoTrack, uid: 0xCC, codec: "S_TEXT/UTF8", extra: [][]byte{encodingEl(1, matroska.CompressionLZO, nil)}},
			{number: encryptedTrack, uid: 0xDD, codec: "S_TEXT/UTF8", extra: [][]byte{el(matroska.IDContentEncodings,
				el(matroska.IDContentEncoding, uintEl(matroska.IDContentEncodingType, 1)))}},
		},
		frameCounts: map[uint64]int64{0x33: 3, 0x44: 5},
		clusters: []fixtureCluster{
			{ticks: 0, crc: true, blocks: []fixtureBlock{
				videoBlock(0, 100_000),
				textBlock(textTrack, 500, 2000, "First"),
				textBlock(forcedTrack, 1000, 1500, "Erste"),
				{track: pgsTrack, rel: 1000, frames: [][]byte{pgs}, duration: -1},
				{track: assTrack, rel: 1200, frames: [][]byte{assEvent(1, "{\\i1}Styled{\\i0}")}, duration: 1800},
				{track: strippedTrack, rel: 1500, frames: [][]byte{[]byte("one")}, duration: 700},
				{track: lacedTrack, rel: 2000, lacing: 1, frames: [][]byte{[]byte("x1"), bytes.Repeat([]byte("y"), 300), []byte("x3")}, duration: 3000},
				{track: scannedTrack, rel: 2500, frames: [][]byte{[]byte("scan one")}, duration: 900, cue: cueWithoutPosition},
				videoBlock(3000, 120_000),
				{track: scannedTrack, rel: 3500, simple: true, frames: [][]byte{[]byte("scan two")}, duration: -1, cue: cueWithoutPosition},
				{track: uncuedTrack, rel: 3600, frames: [][]byte{[]byte("no cue")}, duration: 500, cue: cueNone},
				{track: defaultTrack, rel: 3700, simple: true, frames: [][]byte{[]byte("default")}, duration: -1},
			}},
			{ticks: 5000, blocks: []fixtureBlock{
				videoBlock(0, 150_000),
				{track: textTrack, rel: 100, frames: [][]byte{[]byte("Second")}, duration: 1000, cueDuration: true},
				{track: pgsTrack, rel: 200, frames: [][]byte{zlibBytes(t, []byte{0x80, 0x00, 0x00})}, duration: -1},
				{track: lacedTrack, rel: 300, lacing: 3, frames: [][]byte{[]byte("e1"), []byte("e22"), []byte("e3")}, duration: 1000},
				{track: lacedTrack, rel: 2000, lacing: 2, frames: [][]byte{[]byte("f1"), []byte("f2")}, duration: 999},
				{track: strippedTrack, rel: 2500, simple: true, frames: [][]byte{[]byte("two")}, duration: -1, references: true},
				textBlock(textTrack, 4000, 500, "Third {\\an8}"),
			}},
		},
	}
}

func readFixture(t *testing.T, f mkvFixture) (string, *Index) {
	t.Helper()
	path := writeFixture(t, f.build())
	idx, err := ReadIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return path, idx
}

// A Matroska load draws what it builds (metadata read past the file's head,
// decompressed codec private data, cue lists) from the process's shared
// construction budget: past it the load is busy, which callers retry, and
// every byte comes back when the load returns.
func TestMatroskaIndexDrawsOnTheConstructionBudget(t *testing.T) {
	fixture := libraryFixture(t)
	// Enough cued blocks that the Cues element is read past its first probe.
	many := fixtureCluster{ticks: 10_000}
	for i := range 1200 {
		many.blocks = append(many.blocks, textBlock(textTrack, int16(i*20), 10, "x"))
	}
	fixture.clusters = append(fixture.clusters, many)
	path := writeFixture(t, fixture.build())
	previous := indexConstructionBytes
	defer func() { indexConstructionBytes = previous }()
	for _, tc := range []struct {
		room int64
		want error
	}{{64 << 20, nil}, {1, errIndexReadBusy}} {
		indexConstructionBytes = newConstructionBudget(tc.room)
		idx, err := ReadIndex(t.Context(), path)
		if !errors.Is(err, tc.want) || err == nil && len(idx.Tracks) == 0 {
			t.Fatalf("load with %d bytes of room: %v, want %v", tc.room, err, tc.want)
		}
		if left := indexConstructionBytes.left.Load(); left != tc.room {
			t.Fatalf("load with %d bytes of room left %d", tc.room, left)
		}
	}
}

// A shared budget too busy for a track's codec private data fails the load
// instead of marking the track unsupported: the busy load isn't cached, and
// a later one with room reads the track.
func TestBusyCodecPrivateExpansionIsRetried(t *testing.T) {
	for _, algo := range []uint64{matroska.CompressionZlib, matroska.CompressionHeaderStripping} {
		private := bytes.Repeat([]byte("a"), 64<<10)
		stored, settings := zlibBytes(t, private), []byte(nil)
		if algo == matroska.CompressionHeaderStripping {
			stored, settings = private[:1], private[1:]
		}
		path := writeFixture(t, mkvFixture{tracks: []fixtureTrack{{
			number: 1, codec: "S_TEXT/ASS", private: stored,
			extra: [][]byte{encodingEl(matroska.ScopeCodecPrivate, algo, settings)},
		}}}.build())
		previous := indexConstructionBytes
		cache := NewCache(1 << 20)
		indexConstructionBytes = newConstructionBudget(1 << 10)
		_, busyErr := cache.Index(t.Context(), path)
		indexConstructionBytes = newConstructionBudget(64 << 20)
		idx, err := cache.Index(t.Context(), path)
		indexConstructionBytes = previous
		if !errors.Is(busyErr, errIndexReadBusy) {
			t.Fatalf("algorithm %d without room: %v, want busy", algo, busyErr)
		}
		if err != nil || len(idx.Tracks) != 1 || idx.Tracks[0].Unsupported != "" || !bytes.Equal(idx.Tracks[0].CodecPrivate, private) {
			t.Fatalf("algorithm %d with room: %v, tracks %+v", algo, err, idx)
		}
	}
}

func TestFrameCountsBoundsTargets(t *testing.T) {
	statistic := func(value string) []byte {
		return el(matroska.IDSimpleTag, strEl(matroska.IDTagName, "NUMBER_OF_FRAMES"), strEl(matroska.IDTagString, value))
	}
	for _, repeated := range []bool{false, true} {
		name := "distinct unknown UIDs"
		if repeated {
			name = "repeated indexed UID"
		}
		t.Run(name, func(t *testing.T) {
			targets := uintEl(matroska.IDTagTrackUID, 1)
			for i := range 100_000 {
				uid := uint64(i + 10)
				if repeated {
					uid = 1
				}
				targets = append(targets, uintEl(matroska.IDTagTrackUID, uid)...)
			}
			tags := bytes.Join([][]byte{
				el(matroska.IDTag, statistic("7"), el(matroska.IDTargets, targets), statistic("0")),
				el(matroska.IDTag, el(matroska.IDTargets, uintEl(matroska.IDTagTrackUID, 0), uintEl(matroska.IDTagTrackUID, 2)), statistic("2")),
				el(matroska.IDTag, statistic("-1"), el(matroska.IDTargets, uintEl(matroska.IDTagTrackUID, 1))),
			}, nil)
			counts := map[uint64]int64{1: -1, 2: -1, 3: -1}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			parseFrameCounts(tags, counts)
			runtime.ReadMemStats(&after)
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<10 {
				t.Errorf("parsing %d bytes of targets allocated %d bytes for three indexed tracks", len(targets), allocated)
			}
			if len(counts) != 3 || counts[1] != 0 || counts[2] != 2 || counts[3] != -1 {
				t.Errorf("frame counts: %d UIDs, known counts [%d %d %d]; want only the indexed UIDs with [0 2 -1]", len(counts), counts[1], counts[2], counts[3])
			}
		})
	}
}

func TestMatroskaFallsBackForUnsupportedTimingAndCueIdentity(t *testing.T) {
	for _, tc := range []struct {
		name            string
		extra           [][]byte
		withoutPosition bool
	}{
		{name: "codec delay", extra: [][]byte{uintEl(0x56AA, 500_000_000)}},
		{name: "same-time block numbers", withoutPosition: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := textBlock(1, 2000, 1000, "first"), textBlock(1, 2000, 1000, "second")
			if tc.withoutPosition {
				first.cue, second.cue = cueWithoutPosition, cueWithoutPosition
				first.cueBlockNumber, second.cueBlockNumber = 1, 2
			}
			path, idx := readFixture(t, mkvFixture{
				tracks:   []fixtureTrack{{number: 1, uid: 1, codec: "S_TEXT/UTF8", extra: tc.extra}},
				clusters: []fixtureCluster{{blocks: []fixtureBlock{first, second}}},
			})
			if _, err := ReadPackets(t.Context(), path, idx, 1, WholeFrom, WholeTo, ReadOptions{}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unsupported track read = %v, want FFmpeg fallback", err)
			}
		})
	}
}

type wantPacket struct {
	pts, duration time.Duration
	data          string
}

func TestReadPacketsRefusals(t *testing.T) {
	path, idx := readFixture(t, libraryFixture(t))
	// The PGS track's first packet decompresses to 12 kB.
	if _, err := ReadPackets(context.Background(), path, idx, pgsTrack, WholeFrom, WholeTo, ReadOptions{MaxBytes: 8 << 10}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the memory limit: err = %v", err)
	}
	// A limit hit while walking a cluster for cues without relative
	// positions is the same refusal, never a partial read reported as damage.
	for limit := int64(1); limit <= 64; limit++ {
		res, err := ReadPackets(context.Background(), path, idx, scannedTrack, WholeFrom, WholeTo, ReadOptions{MaxBytes: limit})
		if err == nil && len(res.Packets) == 2 {
			continue
		}
		if !errors.Is(err, ErrTooLarge) || errors.Is(err, ErrDamaged) {
			t.Fatalf("scanned read with a %d-byte limit: %d packets, err = %v", limit, len(res.Packets), err)
		}
	}
	tests := []struct {
		number uint64
		want   error
	}{
		{uncuedTrack, ErrNoCues},
		{forcedTrack, ErrIncompleteCues},
		{lzoTrack, ErrUnsupported},
		{encryptedTrack, ErrUnsupported},
		{42, ErrTrackNotFound},
	}
	for _, tt := range tests {
		if _, err := ReadPackets(context.Background(), path, idx, tt.number, WholeFrom, WholeTo, ReadOptions{}); !errors.Is(err, tt.want) {
			t.Errorf("track %d: err = %v, want %v", tt.number, err, tt.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadPackets(ctx, path, idx, textTrack, WholeFrom, WholeTo, ReadOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v", err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackets(context.Background(), path, idx, textTrack, WholeFrom, WholeTo, ReadOptions{}); !errors.Is(err, ErrFileChanged) {
		t.Errorf("changed file: err = %v", err)
	}
}

func text(idx *Index) *Track {
	t, _ := idx.Track(textTrack)
	return t
}

func TestReadIndexRejectsOtherFiles(t *testing.T) {
	header := el(matroska.IDEBML, strEl(matroska.IDDocType, "matroska"))
	for name, tt := range map[string]struct {
		data []byte
		want error
	}{
		"empty":                        {nil, ErrNotSupportedContainer},
		"text":                         {[]byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"), ErrNotSupportedContainer},
		"webm-ish but another doctype": {el(matroska.IDEBML, strEl(matroska.IDDocType, "notmkv")), ErrNotSupportedContainer},
		// Damage the caller falls back to FFmpeg for, unlike a failed read.
		"Matroska without a segment": {slices.Concat(header, el(matroska.IDVoid, make([]byte, 8))), ErrMalformed},
		"Matroska without tracks":    {slices.Concat(header, el(matroska.IDSegment, el(matroska.IDInfo))), ErrMalformed},
		"Matroska with too many track entries": {slices.Concat(header, el(matroska.IDSegment, slices.Concat(el(matroska.IDInfo),
			el(matroska.IDTracks, bytes.Repeat(el(matroska.IDTrackEntry), maxTrackEntries+1))))), ErrMalformed},
		"MP4 box running past the end": {mp4Box(mp4BoxFtyp, []byte("isom"))[:10], ErrMalformed},
		"MP4 without a moov":           {slices.Concat(mp4Box(mp4BoxFtyp, []byte("isom")), mp4Box("mdat", make([]byte, 64))), ErrMalformed},
	} {
		path := writeFixture(t, tt.data)
		if _, err := ReadIndex(context.Background(), path); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tt.want)
		}
	}
}

// Cue entries are bounded across an index's subtitle tracks before any is
// kept: past the bound the Cues element isn't trusted, as a malformed one
// isn't, and the tracks read as having none.
func TestCollectCuesBoundsEntries(t *testing.T) {
	// CueTrackPositions for track 2 at cluster 0, in eight bytes.
	position := []byte{0xB7, 0x86, 0xF7, 0x81, 0x02, 0xF1, 0x81, 0x00}
	cues := func(n int) []byte {
		return el(matroska.IDCuePoint, uintEl(matroska.IDCueTime, 1), bytes.Repeat(position, n))
	}
	tracks := []Track{{Number: 2}}
	if ok, err := collectCues(cues(10), tracks, 0, 1_000_000, nil); !ok || err != nil || len(tracks[0].Cues) == 0 {
		t.Fatalf("a few cues: %+v, %v", tracks, err)
	}
	tracks = []Track{{Number: 2}}
	if ok, err := collectCues(cues(maxIndexCues+1), tracks, 0, 1_000_000, nil); ok || err != nil || tracks[0].Cues != nil {
		t.Fatalf("cues past the bound were kept: %v", err)
	}
}

func TestParseBlockRejectsBadLacing(t *testing.T) {
	head := []byte{0x83, 0x00, 0x00}
	tests := map[string][]byte{
		"no lace count":            append(head, 1<<1),
		"Xiph sizes past the end":  append(head, 1<<1, 2, 200, 1),
		"fixed sizes don't divide": append(head, 2<<1, 1, 'a', 'b', 'c'),
		"EBML first size past end": append(head, 3<<1, 1, 0x90, 'a'),
		"header cut off":           {0x83, 0x00},
	}
	for name, b := range tests {
		if _, _, err := parseBlock(b); !errors.Is(err, errCorrupt) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCacheKeysOnFileVersion(t *testing.T) {
	path, _ := readFixture(t, libraryFixture(t))
	c := NewCache(1 << 20)
	ctx := context.Background()
	a, err := c.Index(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Index(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("second lookup read the index again")
	}
	// Every lookup stats the file within its context, cached or not, so a
	// stat on a stalled mount can't hold a caller whose context ended.
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Index(ended, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("lookup with an ended context: err = %v", err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	fresh, err := c.Index(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == a {
		t.Fatal("a changed file kept its old index")
	}

	// A budget smaller than two indexes keeps only the latest.
	small := NewCache(a.memSize() + 1)
	other, _ := readFixture(t, libraryFixture(t))
	if _, err := small.Index(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := small.Index(ctx, other); err != nil {
		t.Fatal(err)
	}
	if len(small.entries) != 1 || small.used > small.maxBytes {
		t.Fatalf("cache holds %d entries, %d of %d bytes", len(small.entries), small.used, small.maxBytes)
	}
}

func TestCacheBoundsAbandonedIndexReads(t *testing.T) {
	previous := indexLoadTimeout
	indexLoadTimeout = 100 * time.Millisecond
	t.Cleanup(func() { indexLoadTimeout = previous })
	for _, sameFile := range []bool{true, false} {
		name := "across caches"
		if sameFile {
			name = "repeated file"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer func() {
					unblock()
					synctest.Wait()
				}()
				var reads atomic.Int32
				stalled := func(ctx context.Context, path string) (*Index, error) {
					reads.Add(1)
					<-release // models a kernel read that cannot observe cancellation
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					return ReadIndex(ctx, path)
				}
				fixture := (mkvFixture{tracks: []fixtureTrack{{number: 1, codec: "S_TEXT/UTF8"}}}).build()
				path := writeFixture(t, fixture)
				cache := NewCache(1 << 20)
				cache.readIndex = stalled
				attempts := maxIndexReads + 1
				if sameFile {
					attempts = 3
				}
				for attempt := range attempts {
					if !sameFile {
						path = writeFixture(t, fixture)
						cache = NewCache(1 << 20)
						cache.readIndex = stalled
					}
					_, err := cache.Index(t.Context(), path)
					if !sameFile && attempt == maxIndexReads {
						if !errors.Is(err, errIndexReadBusy) {
							t.Fatalf("load beyond process limit = %v, want busy", err)
						}
					} else if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("stalled load = %v, want shared deadline", err)
					}
				}
				want := int32(maxIndexReads)
				if sameFile {
					want = 1
				}
				synctest.Wait()
				if got := reads.Load(); got != want {
					t.Fatalf("started %d stalled index reads, want %d", got, want)
				}
				unblock()
				synctest.Wait()
				if _, err := cache.Index(t.Context(), path); err != nil {
					t.Fatalf("read after stalled workers finish: %v", err)
				}
			})
		})
	}
}

func BenchmarkReadPackets(b *testing.B) {
	data := libraryFixture(b).build()
	path := writeFixture(b, data)
	ctx := context.Background()
	idx, err := ReadIndex(ctx, path)
	if err != nil {
		b.Fatal(err)
	}
	for _, bench := range []struct {
		name     string
		from, to time.Duration
	}{{"whole", WholeFrom, WholeTo}, {"window", ms(4000), ms(10_000)}} {
		b.Run(bench.name, func(b *testing.B) {
			var stats ReadStats
			for b.Loop() {
				res, err := ReadPackets(ctx, path, idx, textTrack, bench.from, bench.to, ReadOptions{})
				if err != nil {
					b.Fatal(err)
				}
				stats = res.Stats
			}
			b.ReportMetric(float64(stats.Reads), "reads/op")
			b.ReportMetric(float64(stats.Bytes), "bytes-read/op")
			b.ReportMetric(float64(len(data)), "file-bytes")
		})
	}
}

func TestCodecPrivateBudgetSharedAcrossTracks(t *testing.T) {
	private := bytes.Repeat([]byte("a"), 4<<20)
	compressed := zlibBytes(t, private)
	var fixture mkvFixture
	for i := range 5 {
		fixture.tracks = append(fixture.tracks, fixtureTrack{
			number: uint64(i + 1), codec: "S_TEXT/ASS", private: compressed,
			extra: [][]byte{encodingEl(matroska.ScopeCodecPrivate, matroska.CompressionZlib, nil)},
		})
	}
	_, idx := readFixture(t, fixture)
	var retained, supported, unsupported int
	for _, track := range idx.Tracks {
		retained += cap(track.CodecPrivate)
		if track.Unsupported != "" {
			unsupported++
			if len(track.CodecPrivate) != 0 {
				t.Fatal("unsupported track retained codec private data")
			}
		} else {
			supported++
			if !bytes.Equal(track.CodecPrivate, private) {
				t.Fatal("supported track lost codec private data")
			}
		}
	}
	if retained > maxTracksSize || supported == 0 || unsupported == 0 {
		t.Fatalf("codec private data retained %d bytes across %d supported and %d unsupported tracks; want at most %d bytes and fallback for overflow", retained, supported, unsupported, maxTracksSize)
	}
}

func TestCompressionSettingsCountAgainstCacheBudget(t *testing.T) {
	const prefixBytes = 2 << 20
	track, err := newTrack(matroska.TrackEntry{
		Number: 1, Type: matroska.TrackTypeSubtitle, CodecID: "S_TEXT/UTF8", TimestampScale: 1,
		Encodings: []matroska.ContentEncoding{{
			Scope: matroska.ScopeFrames, CompAlgo: matroska.CompressionHeaderStripping,
			CompSettings: bytes.Repeat([]byte("a"), prefixBytes),
		}},
	}, newBudget(maxTracksSize))
	if err != nil {
		t.Fatal(err)
	}
	idx := &Index{Tracks: []Track{track}}
	cache := NewCache(prefixBytes / 2)
	cache.add(cacheKey{path: "fixture"}, idx)
	if len(cache.entries) != 0 {
		t.Fatalf("cache retained a %d-byte compression prefix under a %d-byte budget; estimated %d bytes", prefixBytes, cache.maxBytes, idx.memSize())
	}
}

func TestLargeBlockReadStaysWithinBudget(t *testing.T) {
	const payloadBytes = 8 << 20
	for _, simple := range []bool{true, false} {
		t.Run(map[bool]string{true: "SimpleBlock", false: "BlockGroup"}[simple], func(t *testing.T) {
			block := (fixtureBlock{track: textTrack, simple: simple, duration: 1000,
				frames: [][]byte{bytes.Repeat([]byte("a"), payloadBytes)}}).element()
			const offset = 17
			file := append(bytes.Repeat([]byte{0xCC}, offset), block...)
			first := bytes.Clone(block[:blockWindow])
			m := matroskaRead{r: newFileReader(t.Context(), bytes.NewReader(file), 1), budget: newBudget(int64(len(block)))}
			id, _, hl, ok := matroska.ParseElementHeader(block)
			if !ok {
				t.Fatal("invalid fixture block")
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			element, held, err := m.readElement(offset, first)
			runtime.ReadMemStats(&after)
			if err != nil || element.id != id || !bytes.Equal(element.data, block[hl:]) {
				t.Fatalf("large block read: id = %#x, bytes = %d, err = %v", element.id, len(element.data), err)
			}
			if held != len(block) || m.budget.left.Load() != 0 {
				t.Fatalf("large block held %d bytes, budget left %d; want %d held and zero left", held, m.budget.left.Load(), len(block))
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("assembled %d bytes under a %d-byte charge with %d allocated bytes", len(block), held, allocated)
			// Allow allocator rounding and reader setup, but not a second copy
			// of the block alongside the buffer covered by the budget.
			if allocated > uint64(len(block))+64<<10 {
				t.Fatalf("large block allocated %d bytes under a %d-byte charge", allocated, held)
			}
		})
	}
}

func TestPacketBudgetBoundsDecompression(t *testing.T) {
	const expandedBytes = 4 << 20
	const packetBudget = 64 << 10
	for _, algorithm := range []uint64{matroska.CompressionZlib, matroska.CompressionHeaderStripping} {
		t.Run(map[uint64]string{matroska.CompressionZlib: "zlib", matroska.CompressionHeaderStripping: "header stripping"}[algorithm], func(t *testing.T) {
			compression := &Compression{Algorithm: algorithm}
			payload := []byte("a")
			if algorithm == matroska.CompressionZlib {
				payload = zlibBytes(t, bytes.Repeat([]byte("a"), expandedBytes))
			} else {
				compression.Settings = bytes.Repeat([]byte("a"), expandedBytes)
			}
			m := &matroskaRead{
				budget: newBudget(packetBudget), idx: &Index{TimestampScale: 1_000_000},
				t: &Track{Number: 1, Compression: compression},
			}
			element := blockElement{id: matroska.IDSimpleBlock, data: append([]byte{0x81, 0, 0, 0x80}, payload...)}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := m.blockPackets(element, 0, &clusterHead{}, nil)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("oversized decoded packet: %v", err)
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			// Permit decoder setup and allocation growth, but not a full decoded
			// packet that is sixty-four times the available shared budget.
			if allocated >= expandedBytes {
				t.Fatalf("allocated %d bytes before rejecting a packet under a %d-byte budget", allocated, packetBudget)
			}
		})
	}
}

func TestPacketJobsWaitForCanceledWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer func() {
			unblock()
			synctest.Wait()
		}()
		started := make(chan struct{}, 2)
		finished := make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			_, err := runJobs(ctx, 2, 2, func(int) bool {
				started <- struct{}{}
				<-release // a packet read blocked in the kernel cannot observe ctx
				return true
			})
			finished <- err
		}()
		for range 2 {
			<-started
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-finished:
			t.Fatalf("packet jobs returned with two workers still blocked: %v", err)
		default:
		}
		unblock()
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Fatalf("completed canceled packet jobs: %v", err)
		}
	})
}

func TestPacketReadsBoundAbandonedWork(t *testing.T) {
	for _, sameWindow := range []bool{true, false} {
		name := "across windows"
		if sameWindow {
			name = "repeated window"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer func() {
					unblock()
					synctest.Wait()
				}()
				var started atomic.Int32
				key := packetReadKey{source: cacheKey{path: "/media/a.mkv"}, number: 3}
				for i := range maxPacketReads + 1 {
					if !sameWindow {
						key.from = time.Duration(i) * time.Second
					}
					ctx, cancel := context.WithCancel(t.Context())
					done := make(chan error, 1)
					go func() {
						_, err := runPacketRead(ctx, key, func() (*Result, error) {
							_, err := runJobs(ctx, 2, 2, func(int) bool {
								started.Add(1)
								<-release
								return true
							})
							return nil, err
						})
						done <- err
					}()
					synctest.Wait()
					cancel()
					wantErr := context.Canceled
					if (sameWindow && i > 0) || i == maxPacketReads {
						wantErr = ErrReadBusy
					}
					if err := <-done; !errors.Is(err, wantErr) {
						t.Fatalf("window %d: %v, want %v", i, err, wantErr)
					}
				}
				want := int32(2 * maxPacketReads)
				if sameWindow {
					want = 2
				}
				if got := started.Load(); got != want {
					t.Fatalf("started %d blocked packet workers, want %d", got, want)
				}
				unblock()
				synctest.Wait()
				if _, err := runPacketRead(t.Context(), key, func() (*Result, error) { return &Result{}, nil }); err != nil {
					t.Fatalf("read after blocked workers exit: %v", err)
				}
				if _, err := runPacketRead(t.Context(), key, func() (*Result, error) { panic("bad packet") }); err == nil {
					t.Fatal("packet read panic did not fail the caller")
				}
				if _, err := runPacketRead(t.Context(), key, func() (*Result, error) { return &Result{}, nil }); err != nil {
					t.Fatalf("read after a panic: %v", err)
				}
			})
		})
	}
}

// slowReaderAt takes delay over every read, as a mount of remote storage
// that fetches a large chunk for each small read does.
type slowReaderAt struct {
	r     io.ReaderAt
	delay time.Duration
	reads atomic.Int32
}

func (s *slowReaderAt) ReadAt(p []byte, off int64) (int, error) {
	s.reads.Add(1)
	time.Sleep(s.delay) //nolint:forbidigo // Inside a synctest bubble this only advances the fake clock.
	return s.r.ReadAt(p, off)
}

// On storage where every read takes a second, a whole read with SlowRead
// gives way after its first round of reads instead of reading every cue for
// minutes. A track whose reads all went out in that round still comes back
// whole: nothing is left to refuse.
func TestSlowReadsGiveWay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cues     int
		giveWay  bool
		maxReads int32
	}{
		{name: "many cues", cues: 64, giveWay: true, maxReads: 2 * slowReadSample},
		{name: "one round of cues", cues: slowReadSample, maxReads: slowReadSample},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mkvFixture{tracks: []fixtureTrack{{number: textTrack, uid: 0x3003, codec: "S_TEXT/UTF8"}}}
			for i := range tc.cues {
				f.clusters = append(f.clusters, fixtureCluster{ticks: uint64(i) * 10_000,
					blocks: []fixtureBlock{textBlock(textTrack, 0, 1000, "line")}})
			}
			data := f.build()
			_, idx := readFixture(t, f)
			track, ok := idx.Track(textTrack)
			if !ok {
				t.Fatal("fixture has no text track")
			}
			synctest.Test(t, func(t *testing.T) {
				src := &slowReaderAt{r: bytes.NewReader(data), delay: time.Second}
				r := newFileReader(t.Context(), src, DefaultConcurrency)
				r.giveWayWhenSlow(250 * time.Millisecond)
				res, err := readMatroskaPackets(t.Context(), r, newBudget(0), idx, track, WholeFrom, WholeTo, ReadOptions{}, DefaultConcurrency)
				if got := src.reads.Load(); got > tc.maxReads {
					t.Fatalf("made %d reads, want at most %d", got, tc.maxReads)
				}
				if tc.giveWay {
					if !errors.Is(err, ErrSlowReads) {
						t.Fatalf("err = %v, want ErrSlowReads", err)
					}
					return
				}
				if err != nil || len(res.Packets) != tc.cues {
					t.Fatalf("read %d packets, err %v; want %d", len(res.Packets), err, tc.cues)
				}
			})
		})
	}
}

func TestReadBudgetIncludesMetadata(t *testing.T) {
	t.Run("cue jobs", func(t *testing.T) {
		cues := make([]Cue, 1024)
		for i := range cues {
			cues[i] = Cue{Time: 0, Duration: 2 * time.Second, ClusterPosition: int64(i + 1)}
		}
		track := &Track{Number: 1, Cues: cues}
		_, err := readMatroskaPackets(t.Context(), nil, newBudget(64<<10), &Index{TimestampScale: 1_000_000}, track, 0, time.Second, ReadOptions{}, 16)
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("oversized cue metadata: %v, want ErrTooLarge", err)
		}
	})
	t.Run("laced packet structs", func(t *testing.T) {
		m := matroskaRead{budget: newBudget(16 << 10), idx: &Index{TimestampScale: 1_000_000}, t: &Track{Number: 1}}
		_, err := m.blockPackets(blockElement{id: matroska.IDSimpleBlock, data: []byte{0x81, 0, 0, 0x84, 255}}, 0, &clusterHead{}, nil)
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("256 empty packet structs: %v, want ErrTooLarge", err)
		}
	})
	t.Run("MP4 packet structs", func(t *testing.T) {
		samples := make([]mp4Sample, 1024)
		for i := range samples {
			samples[i] = mp4Sample{dts: int64(i), delta: 1}
		}
		track := &Track{mp4: &mp4Track{samples: samples, timescale: 1000}}
		_, err := readMP4Packets(t.Context(), nil, newBudget(64<<10), track, 0, 2*time.Second, 16)
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("empty MP4 packet metadata: %v, want ErrTooLarge", err)
		}
	})
}

// The Info, Tracks, Cues and Tags one index read holds share one budget: an
// element read past it fails the index as malformed instead of allocating.
func TestMatroskaMetadataSharesABudget(t *testing.T) {
	// Larger than the first read, so the element needs its own buffer.
	size := 4 * elementProbeSize
	r := bytes.NewReader(el(matroska.IDInfo, make([]byte, size)))
	if _, err := readTopElement(r, nil, 0, matroska.IDInfo, maxInfoSize, newBudget(int64(size)-1)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("element over the shared budget: err = %v, want ErrMalformed", err)
	}
	if data, err := readTopElement(r, nil, 0, matroska.IDInfo, maxInfoSize, newBudget(int64(size))); err != nil || len(data) != size {
		t.Fatalf("element within the budget: %d bytes, %v", len(data), err)
	}
}
