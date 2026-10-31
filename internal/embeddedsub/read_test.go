package embeddedsub

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
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

func TestReadIndex(t *testing.T) {
	path, idx := readFixture(t, libraryFixture(t))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Container != ContainerMatroska || idx.TimestampScale != 1_000_000 || idx.Size != info.Size() {
		t.Fatalf("index = %+v", idx)
	}
	var numbers []uint64
	for _, tr := range idx.Tracks {
		numbers = append(numbers, tr.Number)
	}
	if want := []uint64{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}; !slices.Equal(numbers, want) {
		t.Fatalf("subtitle tracks = %v, want %v", numbers, want)
	}
	// The index reads the head, Tags and Cues, never a cluster.
	if idx.Stats.Reads > 4 || idx.Stats.Bytes > info.Size()/4 {
		t.Fatalf("index cost %d reads, %d bytes of %d", idx.Stats.Reads, idx.Stats.Bytes, info.Size())
	}

	tests := []struct {
		number       uint64
		cues         int
		completeness Completeness
		frames       int64
		unsupported  string
	}{
		{textTrack, 3, CompletenessComplete, 3, ""},
		{forcedTrack, 1, CompletenessIncomplete, 5, ""},
		{pgsTrack, 2, CompletenessUnknown, -1, ""},
		{scannedTrack, 2, CompletenessUnknown, -1, ""},
		{uncuedTrack, 0, CompletenessNone, -1, ""},
		{lzoTrack, 0, CompletenessNone, -1, "compression algorithm 2"},
		{encryptedTrack, 0, CompletenessNone, -1, "encrypted"},
	}
	for _, tt := range tests {
		tr, ok := idx.Track(tt.number)
		if !ok {
			t.Fatalf("track %d missing", tt.number)
		}
		if len(tr.Cues) != tt.cues || tr.Completeness != tt.completeness || tr.FrameCount != tt.frames || tr.Unsupported != tt.unsupported {
			t.Errorf("track %d: %d cues, %s, frames %d, unsupported %q; want %d, %s, %d, %q",
				tt.number, len(tr.Cues), tr.Completeness, tr.FrameCount, tr.Unsupported,
				tt.cues, tt.completeness, tt.frames, tt.unsupported)
		}
	}

}

func TestReadIndexTrackFields(t *testing.T) {
	_, idx := readFixture(t, libraryFixture(t))
	forced, _ := idx.Track(forcedTrack)
	if forced.Language != "ger" || forced.LanguageBCP47 != "de-CH" || forced.Name != "Forced" ||
		!forced.Forced || forced.Default || !forced.HearingImpaired || forced.UID != 0x44 {
		t.Errorf("forced track = %+v", forced)
	}
	text, _ := idx.Track(textTrack)
	if text.Language != "eng" || !text.Default || text.Forced {
		t.Errorf("text track defaults = %+v", text)
	}
	// The second cue carries its duration; positions point into clusters.
	if c := text.Cues[1]; c.Time != ms(5100) || c.Duration != time.Second || c.RelativePosition <= 0 {
		t.Errorf("text cue = %+v", c)
	}
	if c := text.Cues[0]; c.Duration != 0 || c.ClusterPosition >= text.Cues[1].ClusterPosition {
		t.Errorf("first text cue = %+v", c)
	}
	scanned, _ := idx.Track(scannedTrack)
	if scanned.Cues[0].RelativePosition != -1 {
		t.Errorf("scanned cue = %+v", scanned.Cues[0])
	}
	ass, _ := idx.Track(assTrack)
	if !bytes.Equal(ass.CodecPrivate, assHeader) || ass.Compression == nil || ass.Compression.Algorithm != matroska.CompressionZlib {
		t.Errorf("ASS private %q, compression %+v", ass.CodecPrivate, ass.Compression)
	}
	stripped, _ := idx.Track(strippedTrack)
	if stripped.Compression == nil || string(stripped.Compression.Settings) != "Line: " {
		t.Errorf("stripped compression = %+v", stripped.Compression)
	}
}

type wantPacket struct {
	pts, duration time.Duration
	data          string
}

func packetList(ps []Packet) []wantPacket {
	out := make([]wantPacket, len(ps))
	for i, p := range ps {
		out[i] = wantPacket{p.PTS, p.Duration, string(p.Data)}
	}
	return out
}

func TestReadPacketsWholeTrack(t *testing.T) {
	path, idx := readFixture(t, libraryFixture(t))
	pgs := string(bytes.Repeat([]byte{0x16, 0x00, 0x03, 1, 2, 3}, 2000))
	tests := []struct {
		name   string
		number uint64
		opts   ReadOptions
		want   []wantPacket
	}{
		{"text", textTrack, ReadOptions{}, []wantPacket{
			{ms(500), ms(2000), "First"}, {ms(5100), ms(1000), "Second"}, {ms(9000), ms(500), "Third {\\an8}"}}},
		{"forced, incomplete allowed", forcedTrack, ReadOptions{AllowIncomplete: true}, []wantPacket{{ms(1000), ms(1500), "Erste"}}},
		{"zlib PGS without durations", pgsTrack, ReadOptions{}, []wantPacket{{ms(1000), 0, pgs}, {ms(5200), 0, "\x80\x00\x00"}}},
		{"zlib ASS", assTrack, ReadOptions{}, []wantPacket{{ms(1200), ms(1800), "1,0,Default,,0,0,0,,{\\i1}Styled{\\i0}"}}},
		{"header stripping", strippedTrack, ReadOptions{}, []wantPacket{{ms(1500), ms(700), "Line: one"}, {ms(7500), 0, "Line: two"}}},
		{"Xiph, EBML and fixed lacing", lacedTrack, ReadOptions{}, []wantPacket{
			{ms(2000), ms(1000), "x1"}, {ms(3000), ms(1000), string(bytes.Repeat([]byte("y"), 300))}, {ms(4000), ms(1000), "x3"},
			{ms(5300), ms(333), "e1"}, {ms(5633), ms(333), "e22"}, {ms(5966), ms(334), "e3"},
			{ms(7000), ms(499), "f1"}, {ms(7499), ms(500), "f2"}}},
		{"cues without relative positions", scannedTrack, ReadOptions{}, []wantPacket{{ms(2500), ms(900), "scan one"}, {ms(3500), 0, "scan two"}}},
		{"default duration", defaultTrack, ReadOptions{}, []wantPacket{{ms(3700), 1500 * time.Millisecond, "default"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ReadPackets(context.Background(), path, idx, tt.number, WholeFrom, WholeTo, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := packetList(res.Packets); !slices.Equal(got, tt.want) {
				t.Fatalf("packets:\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestReadPacketsReadsOnlyTheTracksBlocks(t *testing.T) {
	path, idx := readFixture(t, libraryFixture(t))
	res, err := ReadPackets(context.Background(), path, idx, textTrack, WholeFrom, WholeTo, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Three cues in two clusters: one read per cluster head and one per
	// block.
	if s := res.Stats; s.Cues != 3 || s.Clusters != 2 || s.Reads > 5 || s.Bytes > 40<<10 {
		t.Fatalf("stats = %+v", s)
	}
	res, err = ReadPackets(context.Background(), path, idx, scannedTrack, WholeFrom, WholeTo, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s := res.Stats; s.ScannedClusters != 1 || s.Reads > 5 || s.Bytes > 256<<10 {
		t.Fatalf("scan stats = %+v", s)
	}
}

func TestReadPacketsKeyframesAndDurationsForRemux(t *testing.T) {
	path, idx := readFixture(t, libraryFixture(t))
	res, err := ReadPackets(context.Background(), path, idx, strippedTrack, WholeFrom, WholeTo, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The second block references another, so FFmpeg reads it as no
	// keyframe; neither block's duration is inferred.
	if p := res.Packets; p[0].nonKeyframe || !p[1].nonKeyframe || p[0].inferredDuration {
		t.Fatalf("packets = %+v", p)
	}
	res, err = ReadPackets(context.Background(), path, idx, lacedTrack, WholeFrom, WholeTo, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range res.Packets {
		// FFmpeg marks only a block's first frame a keyframe.
		if p.nonKeyframe != (p.lace > 0) || p.inferredDuration {
			t.Fatalf("laced packet %d = %+v", i, p)
		}
	}
	res, err = ReadPackets(context.Background(), path, idx, defaultTrack, WholeFrom, WholeTo, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Packets[0].inferredDuration {
		t.Fatal("a default duration was taken as the block's")
	}
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

func TestReadPacketsReportsDamage(t *testing.T) {
	f := libraryFixture(t)
	f.clusters[1].blocks[1].corrupt = true // the text track's "Second"
	path, idx := readFixture(t, f)
	res, err := ReadPackets(context.Background(), path, idx, textTrack, WholeFrom, WholeTo, ReadOptions{})
	var damaged *DamagedError
	if !errors.As(err, &damaged) || !errors.Is(err, ErrDamaged) || damaged.Cues != 1 {
		t.Fatalf("err = %v, want one damaged cue", err)
	}
	if got := packetList(res.Packets); len(got) != 2 || got[0].data != "First" || got[1].data != "Third {\\an8}" {
		t.Fatalf("packets around the damage = %+v", got)
	}

	// A file cut off inside the second cluster.
	data := libraryFixture(t).build()
	full := writeFixture(t, data)
	idx, err = ReadIndex(context.Background(), full)
	if err != nil {
		t.Fatal(err)
	}
	cut := text(idx).Cues[2].ClusterPosition + 50_000
	if err := os.WriteFile(full, data[:cut], 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	// Index the cut file through the intact index's Cues, as a copy cut
	// off after its index was cached would read.
	idx.Size, idx.ModTime = info.Size(), info.ModTime()
	res, err = ReadPackets(context.Background(), full, idx, textTrack, WholeFrom, WholeTo, ReadOptions{})
	if !errors.As(err, &damaged) || damaged.Cues != 2 {
		t.Fatalf("cut file: err = %v, want two damaged cues", err)
	}
	if len(res.Packets) != 1 || string(res.Packets[0].Data) != "First" {
		t.Fatalf("cut file packets = %+v", packetList(res.Packets))
	}
}

func text(idx *Index) *Track {
	t, _ := idx.Track(textTrack)
	return t
}

func TestReadPacketsWindows(t *testing.T) {
	const (
		cued   = 3 // cues carry durations
		uncued = 4 // durations only in the blocks
		pgs    = 5 // no durations at all
	)
	f := mkvFixture{
		tracks: []fixtureTrack{
			{number: cued, uid: 3, codec: "S_TEXT/UTF8"},
			{number: uncued, uid: 4, codec: "S_TEXT/UTF8"},
			{number: pgs, uid: 5, codec: "S_HDMV/PGS"},
		},
	}
	// One cluster per 10 s. A long sign at 0 s shows for 50 s; on the
	// uncued track two blocks at 30 and 32.5 s outlast the 10 s lookback.
	block := func(track uint64, rel int16, dur int64, text string, cueDur bool) fixtureBlock {
		b := textBlock(track, rel, dur, text)
		b.cueDuration = cueDur
		return b
	}
	for c := range uint64(10) {
		blocks := []fixtureBlock{
			block(cued, 1000, 2000, "c", true),
			block(uncued, 1000, 2000, "u", false),
			{track: pgs, rel: 1000, frames: [][]byte{{0x16}}, duration: -1},
		}
		switch c {
		case 0:
			blocks = append(blocks, block(cued, 0, 50_000, "long", true))
		case 3:
			blocks = append(blocks, block(uncued, 0, 15_000, "chain2", false), block(uncued, 2500, 12_000, "chain1", false))
		}
		f.clusters = append(f.clusters, fixtureCluster{ticks: c * 10_000, blocks: blocks})
	}
	path, idx := readFixture(t, f)
	read := func(track uint64, from, to time.Duration, opts ReadOptions) ([]wantPacket, ReadStats) {
		t.Helper()
		res, err := ReadPackets(context.Background(), path, idx, track, from, to, opts)
		if err != nil {
			t.Fatal(err)
		}
		return packetList(res.Packets), res.Stats
	}

	// Cue durations bound the lookback exactly: the long sign, then the
	// window's own cue.
	got, stats := read(cued, ms(41_000), ms(47_000), ReadOptions{})
	if want := []wantPacket{{0, ms(50_000), "long"}, {ms(41_000), ms(2000), "c"}}; !slices.Equal(got, want) || stats.Cues != 2 {
		t.Fatalf("cued window = %+v (%d cues read)", got, stats.Cues)
	}
	// Without cue durations the lookback starts at 10 s; the block at
	// 32.5 s lasts 12 s, so it widens to find the one at 30 s, which lasts
	// 15 s.
	got, _ = read(uncued, ms(41_000), ms(47_000), ReadOptions{Lookback: 10 * time.Second})
	if want := []wantPacket{{ms(30_000), ms(15_000), "chain2"}, {ms(32_500), ms(12_000), "chain1"}, {ms(41_000), ms(2000), "u"}}; !slices.Equal(got, want) {
		t.Fatalf("uncued window = %+v", got)
	}
	// A window keeps a laced block whole, as FFmpeg converts its frames.
	lacedPath, lacedIdx := readFixture(t, libraryFixture(t))
	res, err := ReadPackets(context.Background(), lacedPath, lacedIdx, lacedTrack, ms(3500), ms(3600), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := packetList(res.Packets); len(got) != 3 || got[0].data != "x1" || got[2].data != "x3" {
		t.Fatalf("laced window = %+v", got)
	}
	// A packet without a duration shows until the next starts: the window
	// starting between two gets the earlier one.
	got, _ = read(pgs, ms(45_000), ms(52_000), ReadOptions{})
	if want := []wantPacket{{ms(41_000), 0, "\x16"}, {ms(51_000), 0, "\x16"}}; !slices.Equal(got, want) {
		t.Fatalf("PGS window = %+v", got)
	}
	// Nothing shows in a gap.
	got, _ = read(cued, ms(53_000), ms(60_000), ReadOptions{})
	if len(got) != 0 {
		t.Fatalf("gap window = %+v", got)
	}
}

func TestReadIndexWithoutCues(t *testing.T) {
	f := libraryFixture(t)
	f.noCues = true
	path, idx := readFixture(t, f)
	for _, tr := range idx.Tracks {
		if tr.Completeness != CompletenessNone || len(tr.Cues) != 0 {
			t.Fatalf("track %d: %s with %d cues", tr.Number, tr.Completeness, len(tr.Cues))
		}
	}
	if _, err := ReadPackets(context.Background(), path, idx, textTrack, WholeFrom, WholeTo, ReadOptions{}); !errors.Is(err, ErrNoCues) {
		t.Fatalf("err = %v, want ErrNoCues", err)
	}

	// Cues ahead of the clusters are found the same way.
	f = libraryFixture(t)
	f.cuesFirst = true
	_, idx = readFixture(t, f)
	if got := len(text(idx).Cues); got != 3 {
		t.Fatalf("cues first: %d text cues", got)
	}
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
		"MP4 box running past the end": {mp4Box("ftyp", []byte("isom"))[:10], ErrMalformed},
		"MP4 without a moov":           {slices.Concat(mp4Box("ftyp", []byte("isom")), mp4Box("mdat", make([]byte, 64))), ErrMalformed},
	} {
		path := writeFixture(t, tt.data)
		if _, err := ReadIndex(context.Background(), path); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tt.want)
		}
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

func TestReadClusterHeadSkipsLeadingElements(t *testing.T) {
	cluster := el(matroska.IDCluster, el(matroska.IDCRC32, []byte{1, 2, 3, 4}), el(matroska.IDVoid, make([]byte, 300)),
		uintEl(matroska.IDClusterTimestamp, 4242), textBlock(3, 0, 10, "x").element())
	data := append(bytes.Repeat([]byte{0}, 100), cluster...)
	m := &matroskaRead{r: newFileReader(context.Background(), bytes.NewReader(data), 0), idx: &Index{segmentEnd: int64(len(data))}}
	start, end, ts, err := m.readClusterHead(100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if start != 112 || end != int64(len(data)) || ts != 4242 {
		t.Fatalf("head = %d, %d, %d", start, end, ts)
	}
	// A block before any timestamp is damage, not a guess.
	bad := el(matroska.IDCluster, textBlock(3, 0, 10, "x").element())
	m.r = newFileReader(context.Background(), bytes.NewReader(bad), 0)
	if _, _, _, err := m.readClusterHead(0, nil); !errors.Is(err, errCorrupt) {
		t.Fatalf("no timestamp: err = %v", err)
	}
}

func TestHasOverrideTags(t *testing.T) {
	tests := map[string]bool{
		"{\\an8}On top":            true,
		"plain <i>italic</i>":      false,
		"{\\i1}italic{\\i0}":       true,
		"a {brace} only":           false,
		"{\\} empty":               false,
		"open {\\an8 never closed": false,
		"{\\pos(10,20)}there":      true,
		"two\nlines {\\b1}x}":      true,
		"{\\an8\n}split":           false,
	}
	for text, want := range tests {
		if got := HasOverrideTags([]Packet{{Data: []byte("x")}, {Data: []byte(text)}}); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
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

// A read that hangs fails its callers at the load deadline and doesn't hold
// the next caller: opening a FIFO with no writer blocks, as a stalled mount
// does.
func TestCacheGivesUpOnAHungRead(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "stalled.mkv")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	t.Cleanup(func() {
		// Let the abandoned reads finish.
		for range 2 {
			if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		}
	})
	previous := indexLoadTimeout
	indexLoadTimeout = 100 * time.Millisecond
	t.Cleanup(func() { indexLoadTimeout = previous })
	c := NewCache(1 << 20)
	for attempt := range 2 {
		started := time.Now()
		if _, err := c.Index(context.Background(), fifo); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: err = %v, want the load deadline", attempt, err)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("attempt %d waited %v", attempt, elapsed)
		}
	}
}

func TestWriteMKSSplitsClustersAtTheTimestampRange(t *testing.T) {
	tr := &Track{Number: 9, UID: 7, CodecID: "S_TEXT/UTF8", Language: "eng", Default: true, Enabled: true}
	packets := []Packet{
		{PTS: -ms(10), Duration: ms(500), Data: []byte("before zero")},
		{PTS: ms(1000), Duration: ms(500), Data: []byte("a")},
		{PTS: ms(40_000), Duration: ms(500), Data: []byte("b"), inferredDuration: true},
		{PTS: ms(100_000), Data: []byte("c"), nonKeyframe: true},
	}
	var out bytes.Buffer
	if err := WriteMKS(&out, tr, 1_000_000, packets); err != nil {
		t.Fatal(err)
	}
	type block struct {
		ticks       int64
		duration    int64
		hasDuration bool
		keyframe    bool
		data        string
	}
	var got []block
	b := out.Bytes()
	_, size, hl, _ := matroska.ParseElementHeader(b)
	b = b[int64(hl)+size:]
	_, _, hl, _ = matroska.ParseElementHeader(b) // the unknown-size segment
	matroska.Children(b[hl:], func(id uint64, cluster []byte) {
		if id != matroska.IDCluster {
			return
		}
		var ts int64
		matroska.Children(cluster, func(id uint64, v []byte) {
			switch id {
			case matroska.IDClusterTimestamp:
				ts = int64(matroska.Uint(v))
			case matroska.IDBlockGroup:
				blk, err := parseBlockElement(id, v)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, block{ts + int64(blk.rel), blk.duration, blk.hasDuration, blk.keyframe, string(blk.frames[0])})
			}
		})
	})
	want := []block{
		{-10, 500, true, true, "before zero"},
		{1000, 500, true, true, "a"},
		{40_000, 0, false, true, "b"}, // an inferred duration stays out
		{100_000, 0, false, false, "c"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("blocks:\n got %+v\nwant %+v", got, want)
	}
	if err := WriteMKS(&out, tr, 1_000_000, []Packet{{PTS: -ms(40_000)}}); err == nil {
		t.Fatal("a timestamp far before zero was written")
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
