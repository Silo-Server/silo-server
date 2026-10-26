package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestStartupSegmentRequirementScopesFastHardwareWindowsToFreshGenerations(t *testing.T) {
	bitmap := TranscodeOpts{
		TargetCodecVideo:   "h264",
		SubtitleBurnIn:     true,
		SubtitleTrackIndex: 0,
		SubtitleCodec:      "hdmv_pgs_subtitle",
		FastStart:          true,
	}
	tests := []struct {
		name string
		opts TranscodeOpts
		want int
	}{
		{name: "fresh hardware bitmap burn in", opts: func() TranscodeOpts { o := bitmap; o.HWAccel = transcodeHWQSV; return o }(), want: 1},
		{name: "CPU bitmap burn in", opts: func() TranscodeOpts { o := bitmap; o.HWAccel = HWAccelNone; return o }(), want: 3},
		{name: "reconstructed hardware bitmap burn in", opts: func() TranscodeOpts { o := bitmap; o.HWAccel = transcodeHWQSV; o.FastStart = false; return o }(), want: 3},
		{name: "ordinary fresh hardware transcode", opts: TranscodeOpts{TargetCodecVideo: "h264", HWAccel: transcodeHWQSV, FastStart: true}, want: 2},
		{name: "ordinary hardware restart", opts: TranscodeOpts{TargetCodecVideo: "h264", HWAccel: transcodeHWQSV, FastStart: false}, want: 3},
		{name: "ordinary CPU transcode", opts: TranscodeOpts{TargetCodecVideo: "h264", HWAccel: HWAccelNone, FastStart: true}, want: 3},
		{name: "unknown backend falls back to CPU", opts: TranscodeOpts{TargetCodecVideo: "h264", HWAccel: "stale-backend", FastStart: true}, want: 3},
		{name: "copy video", opts: TranscodeOpts{TargetCodecVideo: "copy", HWAccel: transcodeHWQSV, FastStart: true}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := startupSegmentRequirement(tt.opts); got != tt.want {
				t.Fatalf("startupSegmentRequirement() = %d, want %d", got, tt.want)
			}
		})
	}
}

// copyFragmentManifest is a copy-mode fMP4 playlist whose variable-length
// fragments start at media sequence 9.
const copyFragmentManifest = "#EXTM3U\n" +
	"#EXT-X-VERSION:7\n" +
	"#EXT-X-TARGETDURATION:3\n" +
	"#EXT-X-MEDIA-SEQUENCE:9\n" +
	"#EXT-X-INDEPENDENT-SEGMENTS\n" +
	"#EXT-X-MAP:URI=\"init.mp4\"\n" +
	"#EXTINF:2.669000,\n" +
	"seg_00009.m4s\n" +
	"#EXTINF:1.669000,\n" +
	"seg_00010.m4s\n"

// copyFragmentTimelineManifest has three variable-length copy fragments from
// media sequence 9.
const copyFragmentTimelineManifest = "#EXTM3U\n" +
	"#EXT-X-VERSION:7\n" +
	"#EXT-X-TARGETDURATION:3\n" +
	"#EXT-X-MEDIA-SEQUENCE:9\n" +
	"#EXT-X-MAP:URI=\"init.mp4\"\n" +
	"#EXTINF:2.669000,\n" +
	"seg_00009.m4s\n" +
	"#EXTINF:1.669000,\n" +
	"seg_00010.m4s\n" +
	"#EXTINF:1.668000,\n" +
	"seg_00011.m4s\n"

// writeManifestFixture writes stream.m3u8 into dir, plus a one-byte file for
// each named segment so startupFilesReady passes.
func writeManifestFixture(t *testing.T, dir, manifest string, files ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestBuildPlaybackManifest_CopyVideoUsesRealManifest(t *testing.T) {
	realEntries := []string{
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXTINF:2.669000,",
		"#EXTINF:1.669000,",
		"#EXT-X-MAP:URI=\"segment/init.mp4?token=test\"",
		"segment/seg_00009.m4s?token=test",
		"segment/seg_00010.m4s?token=test",
	}
	tests := []struct {
		name          string
		totalDuration float64
		want          []string
		forbidden     []string
		tokenless     bool
	}{
		{
			name:          "known duration",
			totalDuration: 10,
			want:          append([]string{"#EXT-X-PLAYLIST-TYPE:EVENT", "#EXT-X-START:TIME-OFFSET=0.001,PRECISE=YES"}, realEntries...),
			// copy-mode manifest should not be synthetic VOD
			forbidden: []string{"#EXT-X-PLAYLIST-TYPE:VOD"},
			tokenless: true,
		},
		{
			// unknown duration — must fall back to real manifest
			name:          "unknown duration",
			totalDuration: 0,
			want:          realEntries,
			// unknown-duration manifest cannot promise append-only EVENT semantics
			forbidden: []string{"#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-PLAYLIST-TYPE:EVENT", "#EXT-X-START:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			writeManifestFixture(t, tempDir, copyFragmentManifest, "init.mp4", "seg_00009.m4s", "seg_00010.m4s")
			session := &TranscodeSession{
				outputDir: tempDir,
				opts: TranscodeOpts{
					TargetCodecVideo: "copy",
					TargetCodecAudio: "aac",
					SegmentDuration:  2,
					TotalDuration:    tt.totalDuration,
				},
			}

			got, err := session.BuildPlaybackManifest("segment/", "token=test")
			if err != nil {
				t.Fatalf("BuildPlaybackManifest: %v", err)
			}
			text := string(got)
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Fatalf("manifest missing %q:\n%s", want, text)
				}
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(text, forbidden) {
					t.Fatalf("manifest unexpectedly contains %q:\n%s", forbidden, text)
				}
			}
			if !tt.tokenless {
				return
			}

			tokenless, err := session.BuildPlaybackManifest("segment/", "")
			if err != nil {
				t.Fatalf("BuildPlaybackManifest tokenless: %v", err)
			}
			for _, want := range []string{
				"#EXT-X-MAP:URI=\"segment/init.mp4\"",
				"segment/seg_00009.m4s",
				"segment/seg_00010.m4s",
			} {
				if !strings.Contains(string(tokenless), want) {
					t.Fatalf("tokenless manifest missing %q:\n%s", want, tokenless)
				}
			}
			if strings.Contains(string(tokenless), "?st=") || strings.Contains(string(tokenless), "?token=") {
				t.Fatalf("tokenless manifest propagated a credential query:\n%s", tokenless)
			}
		})
	}
}

func TestBuildPlaybackManifest_AdvancedCopyGenerationKeepsHistoricalRemountPosition(t *testing.T) {
	tempDir := t.TempDir()
	const producedSegments = 160
	lines := []string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXT-X-INDEPENDENT-SEGMENTS",
		"#EXT-X-MAP:URI=\"init.mp4\"",
	}
	for i := range producedSegments {
		lines = append(lines, "#EXTINF:2.000000,", fmt.Sprintf("seg_%05d.m4s", i))
	}
	lines = append(lines, "")
	manifestPath := filepath.Join(tempDir, "stream.m3u8")
	writeManifestFixture(t, tempDir, strings.Join(lines, "\n"), "init.mp4", "seg_00000.m4s", "seg_00001.m4s")

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo: "copy",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    6_519,
		},
	}
	got, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		"#EXT-X-PLAYLIST-TYPE:EVENT",
		"#EXT-X-START:TIME-OFFSET=0.001,PRECISE=YES",
		"segment/seg_00025.m4s?token=test",
		"segment/seg_00159.m4s?token=test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("advanced manifest missing %q:\n%s", want, text)
		}
	}
	timeline, err := parseManifestTimeline(got)
	if err != nil {
		t.Fatalf("parse advanced manifest: %v", err)
	}
	// A 51-second remount belongs to historical segment 25, not the produced
	// edge at segment 159. The stable origin lets Media3 apply that seek after
	// rebuilding its MediaSource instead of projecting the default to the edge.
	historical := timeline.entries[int(51/2)]
	if historical.number != 25 || historical.number >= timeline.entries[len(timeline.entries)-1].number {
		t.Fatalf("historical remount segment = %d, produced edge = %d", historical.number, timeline.entries[len(timeline.entries)-1].number)
	}

	// Manifest stabilization is a pure presentation rewrite: when FFmpeg adds
	// the next segment, cadence advances by exactly one and the same stable tags
	// remain without duplication.
	lines = append(lines[:len(lines)-1], "#EXTINF:2.000000,", "seg_00160.m4s", "")
	if err := os.WriteFile(manifestPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("advance manifest: %v", err)
	}
	advanced, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest after cadence advance: %v", err)
	}
	advancedTimeline, err := parseManifestTimeline(advanced)
	if err != nil {
		t.Fatalf("parse advanced cadence: %v", err)
	}
	if len(advancedTimeline.entries) != len(timeline.entries)+1 || advancedTimeline.entries[len(advancedTimeline.entries)-1].number != 160 {
		t.Fatalf("advanced cadence = %d entries ending at %d", len(advancedTimeline.entries), advancedTimeline.entries[len(advancedTimeline.entries)-1].number)
	}
	if strings.Count(string(advanced), "#EXT-X-PLAYLIST-TYPE:EVENT") != 1 || strings.Count(string(advanced), "#EXT-X-START:") != 1 {
		t.Fatalf("stable tags duplicated after manifest advance:\n%s", advanced)
	}
}

func TestBuildPlaybackManifest_EncodedTranscodeUsesSyntheticVODManifest(t *testing.T) {
	session := &TranscodeSession{
		opts: TranscodeOpts{
			TargetCodecVideo: "h264",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    5.1,
		},
	}

	got, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}

	text := string(got)
	for _, want := range []string{
		"#EXT-X-PLAYLIST-TYPE:VOD",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:2.000000,",
		"#EXTINF:1.100000,",
		"segment/seg_00000.ts?token=test",
		"segment/seg_00002.ts?token=test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "#EXT-X-MAP:") {
		t.Fatalf("encoded manifest should not use fMP4 init map:\n%s", text)
	}
}

// TestLongTitlePlaylistsCarryTheSignedQueryOnce renders each path that builds
// or rewrites a v3 media playlist, for a client that negotiated
// hls_variable_substitution_v1, and checks a four-hour title stays well under
// the 4 MiB a Roku player accepts while every segment URI resolves to exactly
// the URL the literal form names. The query is the size of a transcode's
// signed stream token (about 1.7 KB). A growing EVENT playlist must use the
// substituted form from its first fetch, even when the final duration is
// unknown, so its form never changes while it grows.
func TestLongTitlePlaylistsCarryTheSignedQueryOnce(t *testing.T) {
	const (
		fourHours = 4 * 60 * 60.0
		maxBytes  = 1 << 20
	)
	rawQuery := "st=" + strings.Repeat("eyJzaWQiOi", 165)
	encoded := TranscodeOpts{TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, TotalDuration: fourHours}
	copyOpts := TranscodeOpts{TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2, TotalDuration: fourHours, KeyframePlaylist: true}
	growing := copyOpts
	growing.HLSVariableSubstitution = true
	growing.TotalDuration = 0
	ext := hlsSegmentExtension(copyOpts)
	keyframeDurations := slices.Repeat([]float64{2.002}, 7200)

	tests := []struct {
		name   string
		render func(t *testing.T) []byte
		want   []string
	}{
		{
			name: "encoded transcode, synthetic VOD",
			render: func(t *testing.T) []byte {
				return buildManifestForTest(t, &TranscodeSession{opts: encoded}, rawQuery)
			},
			want: literalSegmentURIs(7200, "segment/", ".ts", "?"+rawQuery, false),
		},
		{
			name: "copy remux, complete keyframe playlist",
			render: func(t *testing.T) []byte {
				return buildManifestForTest(t, &TranscodeSession{opts: copyOpts, copyPlan: &copySegmentPlan{durations: keyframeDurations}}, rawQuery)
			},
			want: literalSegmentURIs(7200, "segment/", ext, "?"+rawQuery, videoUsesFMP4(copyOpts)),
		},
		{
			name: "copy remux, EVENT playlist first fetch, duration unknown",
			render: func(t *testing.T) []byte {
				return buildManifestForTest(t, realPlaylistSession(t, growing, ffmpegPlaylistFixture(3, ext, false)), rawQuery)
			},
			want: literalSegmentURIs(3, "segment/", ext, "?"+rawQuery, true),
		},
		{
			name: "node playlist relayed by the API",
			render: func(t *testing.T) []byte {
				node := relayedNodePlaylist(t, ffmpegPlaylistFixture(7200, ext, true))
				return AppendManifestQueryParamWithSubstitution(node, "st", strings.TrimPrefix(rawQuery, "st="))
			},
			want: literalSegmentURIs(7200, "segment/", ext, "?"+rawQuery, true),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := tt.render(t)
			if len(manifest) >= maxBytes {
				t.Fatalf("playlist is %d bytes, want under %d", len(manifest), maxBytes)
			}
			if !bytes.Contains(manifest, []byte("#EXT-X-DEFINE:")) {
				t.Fatal("playlist carries no variable definition")
			}
			got := resolvePlaylistURIs(t, string(manifest))
			if len(got) != len(tt.want) {
				t.Fatalf("playlist resolves %d URIs, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("URI %d resolves to %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestLongTitleSubtitleRenditionPlaylistsCarryTheSignedQueryOnce covers the
// subtitle media playlist of a session with an HLS subtitle rendition, for a
// client that negotiated hls_variable_substitution_v1. The subtitle playlist
// is derived from the video playlist (an encode's synthetic grid, a copy
// remux's keyframe plan or FFmpeg's EVENT playlist, or a node playlist the API
// relays), so it must keep that playlist's definition wherever it keeps a
// reference, and every subtitle segment URI must resolve to the literal URL.
// The multivariant playlist keeps the literal query: a multivariant playlist's
// definitions would not reach the media playlists it names.
func TestLongTitleSubtitleRenditionPlaylistsCarryTheSignedQueryOnce(t *testing.T) {
	const (
		fourHours = 4 * 60 * 60.0
		maxBytes  = 1 << 20
		segments  = 7200
	)
	token := strings.Repeat("eyJzaWQiOi", 165)
	rawQuery := "st=" + token
	rendition := func(opts TranscodeOpts) TranscodeOpts {
		opts.SegmentDuration, opts.TotalDuration = 2, fourHours
		opts.HLSVariableSubstitution = true
		return opts
	}
	encoded := rendition(subtitleRenditionTestOpts(""))
	copyOpts := rendition(copyRemuxRenditionOpts("", false))
	ext := hlsSegmentExtension(copyOpts)
	subtitles := func(t *testing.T, session *TranscodeSession, query string) []byte {
		t.Helper()
		if !session.HasSubtitleRendition() {
			t.Fatal("session publishes no subtitle rendition")
		}
		return buildManifestForTest(t, session, joinQuery(SubtitleRenditionQueryParam+"="+subtitleRenditionSubtitles, query))
	}
	literal := func(n int) []string {
		uris := make([]string, 0, n)
		for i := range n {
			uris = append(uris, fmt.Sprintf("segment/%s%05d%s?%s", subtitleSegmentPrefix, i, subtitleSegmentExt, rawQuery))
		}
		return uris
	}

	tests := []struct {
		name    string
		session *TranscodeSession
		render  func(t *testing.T, session *TranscodeSession) []byte
		want    []string
	}{
		{
			name:    "encoded transcode, synthetic VOD",
			session: &TranscodeSession{opts: encoded},
			want:    literal(segments),
		},
		{
			name:    "copy remux, complete keyframe playlist",
			session: &TranscodeSession{opts: copyOpts, copyPlan: &copySegmentPlan{durations: slices.Repeat([]float64{2.002}, segments)}},
			want:    literal(segments),
		},
		{
			name:    "copy remux, EVENT playlist first fetch",
			session: realPlaylistSession(t, copyOpts, ffmpegPlaylistFixture(3, ext, false)),
			want:    literal(3),
		},
		{
			name:    "node playlist relayed by the API",
			session: realPlaylistSession(t, copyOpts, ffmpegPlaylistFixture(segments, ext, true)),
			// The node strips "st", so it renders without a query and the
			// API's relay appends the token.
			render: func(t *testing.T, session *TranscodeSession) []byte {
				return AppendManifestQueryParamWithSubstitution(subtitles(t, session, ""), "st", token)
			},
			want: literal(segments),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var manifest []byte
			if tt.render != nil {
				manifest = tt.render(t, tt.session)
			} else {
				manifest = subtitles(t, tt.session, rawQuery)
			}
			if len(manifest) >= maxBytes {
				t.Fatalf("subtitle playlist is %d bytes, want under %d", len(manifest), maxBytes)
			}
			if !bytes.Contains(manifest, []byte("#EXT-X-DEFINE:")) {
				t.Fatal("subtitle playlist carries no variable definition")
			}
			got := resolvePlaylistURIs(t, string(manifest))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("subtitle playlist resolves %d URIs, want %d; first %q, want %q", len(got), len(tt.want), got[0], tt.want[0])
			}

			master := buildManifestForTest(t, tt.session, rawQuery)
			if bytes.Contains(master, []byte("#EXT-X-DEFINE:")) || !bytes.Contains(master, []byte("master.m3u8?"+SubtitleRenditionQueryParam+"="+subtitleRenditionSubtitles+"&"+rawQuery+"\"")) {
				t.Fatal("multivariant playlist must name its media playlists with the literal query")
			}
		})
	}
}

func joinQuery(a, b string) string {
	if b == "" {
		return a
	}
	return a + "&" + b
}

// TestRewrittenPlaylistsStayLiteralWithoutSubstitutionFeature pins the
// playlists the server rewrites for a client that did not negotiate
// hls_variable_substitution_v1 (AetherEngine on Apple platforms parses them
// itself and has no EXT-X-DEFINE support): however long the title, every URI
// keeps the literal query, byte for byte.
func TestRewrittenPlaylistsStayLiteralWithoutSubstitutionFeature(t *testing.T) {
	token := strings.Repeat("eyJzaWQiOi", 165)
	opts := TranscodeOpts{TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2, TotalDuration: 4 * 60 * 60}
	ext := hlsSegmentExtension(opts)
	fixture := ffmpegPlaylistFixture(7200, ext, true)
	literal := func(playlist, prefix, suffix string) string {
		lines := strings.Split(playlist, "\n")
		for i, line := range lines {
			switch {
			case strings.HasPrefix(line, "#EXT-X-MAP:URI=\""):
				lines[i] = `#EXT-X-MAP:URI="` + prefix + strings.TrimSuffix(strings.TrimPrefix(line, `#EXT-X-MAP:URI="`), `"`) + suffix + `"`
			case line != "" && line[0] != '#':
				lines[i] = prefix + line + suffix
			}
		}
		return strings.Join(lines, "\n")
	}

	event := buildManifestForTest(t, realPlaylistSession(t, opts, fixture), "st="+token)
	if want := literal(string(stabilizeCopyHLSRemountTimeline([]byte(fixture), opts)), "segment/", "?st="+token); string(event) != want {
		t.Fatal("copy EVENT playlist differs from the literal form")
	}
	relayed := AppendManifestQueryParam(relayedNodePlaylist(t, fixture), "st", token)
	if want := literal(fixture, "segment/", "?st="+token); string(relayed) != want {
		t.Fatal("relayed node playlist differs from the literal form")
	}
}

// literalSegmentURIs lists a playlist's URIs as the literal form names them.
func literalSegmentURIs(n int, prefix, ext, suffix string, init bool) []string {
	uris := make([]string, 0, n+1)
	if init {
		uris = append(uris, prefix+"init.mp4"+suffix)
	}
	for i := range n {
		uris = append(uris, fmt.Sprintf("%sseg_%05d%s%s", prefix, i, ext, suffix))
	}
	return uris
}

func buildManifestForTest(t *testing.T, session *TranscodeSession, rawQuery string) []byte {
	t.Helper()
	manifest, err := session.BuildPlaybackManifest("segment/", rawQuery)
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}
	return manifest
}

func realPlaylistSession(t *testing.T, opts TranscodeOpts, playlist string) *TranscodeSession {
	t.Helper()
	dir := t.TempDir()
	writeManifestFixture(t, dir, playlist)
	return &TranscodeSession{outputDir: dir, opts: opts}
}

// ffmpegPlaylistFixture is FFmpeg's fMP4 copy playlist after n segments.
func ffmpegPlaylistFixture(n int, ext string, ended bool) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i := range n {
		fmt.Fprintf(&b, "#EXTINF:2.002000,\nseg_%05d%s\n", i, ext)
	}
	if ended {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}

// relayedNodePlaylist is what a transcode node serves the API's relay for a
// request without a query: FFmpeg's playlist under segment/.
func relayedNodePlaylist(t *testing.T, playlist string) []byte {
	t.Helper()
	manifest, err := RewriteManifestPaths([]byte(playlist), "segment/", "")
	if err != nil {
		t.Fatalf("RewriteManifestPaths: %v", err)
	}
	return manifest
}

var (
	playlistDefinePattern    = regexp.MustCompile(`^#EXT-X-DEFINE:NAME="([A-Za-z0-9_-]+)",VALUE="([^"]*)"$`)
	playlistReferencePattern = regexp.MustCompile(`\{\$([A-Za-z0-9_-]+)\}`)
	playlistMapURIPattern    = regexp.MustCompile(`URI="([^"]*)"`)
)

// resolvePlaylistURIs returns a media playlist's init and segment URIs after
// HLS variable substitution, failing on a definition that precedes protocol
// version 8, a duplicate definition, or an undefined reference.
func resolvePlaylistURIs(t *testing.T, manifest string) []string {
	t.Helper()
	version := 1
	vars := map[string]string{}
	var uris []string
	for line := range strings.SplitSeq(manifest, "\n") {
		var uri string
		switch {
		case strings.HasPrefix(line, "#EXT-X-VERSION:"):
			version, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-VERSION:"))
			continue
		case strings.HasPrefix(line, "#EXT-X-DEFINE:"):
			m := playlistDefinePattern.FindStringSubmatch(line)
			if m == nil || version < 8 {
				t.Fatalf("invalid definition %.80q at protocol version %d", line, version)
			}
			if _, dup := vars[m[1]]; dup {
				t.Fatalf("variable %q defined twice", m[1])
			}
			vars[m[1]] = m[2]
			continue
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			m := playlistMapURIPattern.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("map without URI: %q", line)
			}
			uri = m[1]
		case line == "" || line[0] == '#':
			continue
		default:
			uri = line
		}
		uris = append(uris, playlistReferencePattern.ReplaceAllStringFunc(uri, func(ref string) string {
			value, ok := vars[ref[2:len(ref)-1]]
			if !ok {
				t.Fatalf("undefined variable reference %q", ref)
			}
			return value
		}))
	}
	return uris
}

func TestBuildPlaybackManifest_LongEncodedTranscodeUsesRealManifest(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:2.000000,",
		"seg_00000.ts",
		"#EXTINF:2.000000,",
		"seg_00001.ts",
		"",
	}, "\n")
	writeManifestFixture(t, tempDir, manifest)

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo: "h264",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    1_000_000,
		},
	}

	got, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}

	text := string(got)
	if strings.Contains(text, "#EXT-X-PLAYLIST-TYPE:VOD") ||
		strings.Contains(text, "seg_499999.ts") {
		t.Fatalf("long encoded manifest should not synthesize every segment:\n%s", text)
	}
	for _, want := range []string{
		"#EXT-X-MEDIA-SEQUENCE:0",
		"segment/seg_00000.ts?token=test",
		"segment/seg_00001.ts?token=test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
}

func TestBuildSourceAlignedPlaybackManifestAnchorsSeekedRealPlaylist(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:8",
		"#EXTINF:2.000000,",
		"seg_00008.ts",
		"#EXTINF:2.000000,",
		"seg_00009.ts",
		"",
	}, "\n")
	writeManifestFixture(t, tempDir, manifest)

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			TargetCodecAudio:   "aac",
			SegmentDuration:    2,
			TotalDuration:      1_000_000,
			SeekSeconds:        17.3,
			StartSegmentNumber: 8,
		},
	}

	got, err := session.BuildSourceAlignedPlaybackManifest("segment/", "source_timeline=1")
	if err != nil {
		t.Fatalf("BuildSourceAlignedPlaybackManifest: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		"#EXT-X-VERSION:8",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXT-X-GAP\n#EXTINF:2.162500,\nsegment/source_timeline_gap.ts?source_timeline=1",
		"segment/seg_00008.ts?source_timeline=1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("source-aligned manifest missing %q:\n%s", want, text)
		}
	}
	if gap := strings.Index(text, "source_timeline_gap.ts"); gap < 0 || gap > strings.Index(text, "seg_00008.ts") {
		t.Fatalf("timeline gap must precede the first real segment:\n%s", text)
	}
	if count := strings.Count(text, "#EXT-X-GAP"); count != 8 {
		t.Fatalf("timeline gap count = %d, want 8:\n%s", count, text)
	}
}

// The gap segments a source-aligned playlist prepends use the same query
// form as the real segments, so a large seek does not bring back a
// credential-sized query on every gap URI.
func TestSourceAlignedGapsUseTheSubstitutedQuery(t *testing.T) {
	tempDir := t.TempDir()
	writeManifestFixture(t, tempDir, strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:1800",
		"#EXTINF:2.000000,",
		"seg_01800.ts",
		"",
	}, "\n"))
	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo:        "h264",
			TargetCodecAudio:        "aac",
			SegmentDuration:         2,
			TotalDuration:           1_000_000,
			SeekSeconds:             3600,
			StartSegmentNumber:      1800,
			HLSVariableSubstitution: true,
		},
	}
	rawQuery := "source_timeline=1&st=" + strings.Repeat("eyJzaWQiOi", 100)
	got, err := session.BuildSourceAlignedPlaybackManifest("segment/", rawQuery)
	if err != nil {
		t.Fatalf("BuildSourceAlignedPlaybackManifest: %v", err)
	}
	if count := strings.Count(string(got), rawQuery); count != 1 {
		t.Fatalf("query appears %d times, want once in its definition:\n%s", count, got)
	}
	uris := resolvePlaylistURIs(t, string(got))
	if len(uris) < 2 || uris[0] != "segment/source_timeline_gap.ts?"+rawQuery || uris[len(uris)-1] != "segment/seg_01800.ts?"+rawQuery {
		t.Fatalf("resolved URIs = %q", uris)
	}
}

func TestCanGenerateSyntheticManifestBoundsSegmentCount(t *testing.T) {
	if !CanGenerateSyntheticManifest(100_000, 2) {
		t.Fatal("historical 50,000-segment manifest should remain supported")
	}
	if CanGenerateSyntheticManifest(100_001, 2) {
		t.Fatal("manifest above 50,000 segments should use the real playlist")
	}
}

func TestHEVCTranscodeManifestAndProgressUseFMP4Segments(t *testing.T) {
	dir := t.TempDir()
	opts := TranscodeOpts{
		OutputDir: dir, TargetCodecVideo: "hevc", TargetCodecAudio: "aac",
		SourceVideoCodec: "av1", SegmentDuration: 2, TotalDuration: 8,
	}
	session := &TranscodeSession{outputDir: dir, opts: opts, running: true}
	manifest := session.GenerateFullManifest("", "")
	text := string(manifest)
	for _, want := range []string{`#EXT-X-MAP:URI="init.mp4"`, "seg_00000.m4s", "seg_00003.m4s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("HEVC manifest missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, ".ts") {
		t.Fatalf("HEVC manifest exposed MPEG-TS segment: %s", text)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seg_00000.m4s"), []byte("segment"), 0o600); err != nil {
		t.Fatal(err)
	}
	progress := session.SegmentProgress(time.Now())
	if progress.ProducedHead != 0 || progress.LastProducedAt.IsZero() {
		t.Fatalf("HEVC segment progress = %#v, want one produced fMP4 segment", progress)
	}
}

func TestBuildPlaybackManifest_UnknownDurationRejectsBrokenManifest(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:0",
		"#EXT-X-MEDIA-SEQUENCE:1390",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:0.000000,",
		"seg_01390.m4s",
		"#EXTINF:0.000000,",
		"seg_01391.m4s",
		"",
	}, "\n")
	writeManifestFixture(t, tempDir, manifest, "init.mp4", "seg_01390.m4s", "seg_01391.m4s")

	session := &TranscodeSession{
		outputDir: tempDir,
		running:   true,
		opts: TranscodeOpts{
			TargetCodecVideo: "copy",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    0, // unknown duration — forces real manifest path
		},
	}

	if _, err := session.BuildPlaybackManifest("segment/", "token=test"); err == nil {
		t.Fatal("expected BuildPlaybackManifest to reject zero-duration manifest")
	} else if !strings.Contains(err.Error(), "invalid copy playback manifest") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRewriteManifestPaths_RejectsInvalidManifest(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
	}{
		{name: "empty", manifest: ""},
		{name: "missing header", manifest: "#EXTINF:2.0,\nseg_00000.m4s\n"},
		{name: "bad map", manifest: "#EXTM3U\n#EXT-X-MAP:BYTERANGE=\"720@0\"\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RewriteManifestPaths([]byte(tc.manifest), "segment/", ""); err == nil {
				t.Fatalf("expected RewriteManifestPaths to fail for %s", tc.name)
			}
		})
	}
}

func TestTranscodeSession_SegmentStartTimeUsesManifestTimeline(t *testing.T) {
	tempDir := t.TempDir()
	writeManifestFixture(t, tempDir, copyFragmentTimelineManifest)

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			SeekSeconds:      18.261,
			TargetCodecVideo: "copy",
		},
	}

	tests := []struct {
		segment int
		want    float64
	}{
		{segment: 9, want: 18.261},
		{segment: 10, want: 20.93},
		{segment: 11, want: 22.599},
	}

	for _, tc := range tests {
		got, ok, err := session.SegmentStartTime(tc.segment)
		if err != nil {
			t.Fatalf("SegmentStartTime(%d): %v", tc.segment, err)
		}
		if !ok {
			t.Fatalf("SegmentStartTime(%d) reported segment missing", tc.segment)
		}
		if math.Abs(got-tc.want) > 0.0001 {
			t.Fatalf("SegmentStartTime(%d) = %.6f, want %.6f", tc.segment, got, tc.want)
		}
	}

	if _, ok, err := session.SegmentStartTime(12); err != nil {
		t.Fatalf("SegmentStartTime(12): %v", err)
	} else if ok {
		t.Fatal("SegmentStartTime(12) should report missing segment")
	}
}

func TestResolveSegmentRecoveryTarget_CopyMapsActualAnchorToManifestNumber(t *testing.T) {
	tests := []struct {
		name             string
		anchorMillis     int
		wantStartSegment int
		wantOrigin       float64
	}{
		{name: "Matroska pre-roll uses preceding manifest slot", anchorMillis: 18000, wantStartSegment: 9, wantOrigin: 18},
		{name: "MP4 exact seek keeps requested manifest slot", anchorMillis: 20669, wantStartSegment: 10, wantOrigin: 20.669},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			writeManifestFixture(t, tempDir, copyFragmentTimelineManifest)
			ffmpegPath := filepath.Join(tempDir, "ffmpeg")
			probe := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '#tb 0: 1/1000'\nprintf '%%s\\n' '0, %d, %d, 41, 1024, 0x12345678'\n", tt.anchorMillis, tt.anchorMillis)
			if err := os.WriteFile(ffmpegPath, []byte(probe), 0o755); err != nil {
				t.Fatalf("write fake ffmpeg: %v", err)
			}

			session := &TranscodeSession{
				outputDir: tempDir,
				opts: TranscodeOpts{
					InputPath:              "/media/movie.mkv",
					FFmpegPath:             ffmpegPath,
					SeekSeconds:            18.261,
					StreamOriginSeconds:    18,
					CopySeekAnchorResolved: true,
					TargetCodecVideo:       "copy",
					SegmentDuration:        2,
					StartSegmentNumber:     9,
				},
			}

			target, ok, err := session.ResolveSegmentRecoveryTarget(context.Background(), 10)
			if err != nil {
				t.Fatalf("ResolveSegmentRecoveryTarget: %v", err)
			}
			if !ok {
				t.Fatal("ResolveSegmentRecoveryTarget returned ok=false")
			}
			if math.Abs(target.SeekSeconds-20.669) > 0.0001 {
				t.Fatalf("SeekSeconds = %.6f, want 20.669", target.SeekSeconds)
			}
			if target.StartSegmentNumber != tt.wantStartSegment {
				t.Fatalf("StartSegmentNumber = %d, want %d", target.StartSegmentNumber, tt.wantStartSegment)
			}
			if math.Abs(target.StreamOriginSeconds-tt.wantOrigin) > 0.0001 || !target.CopySeekAnchorResolved {
				t.Fatalf("copy anchor = %.6f resolved=%v, want %.6f resolved=true", target.StreamOriginSeconds, target.CopySeekAnchorResolved, tt.wantOrigin)
			}
		})
	}
}

func TestRestartSeekTarget(t *testing.T) {
	tests := []struct {
		name     string
		manifest string // written to the output dir when non-empty
		opts     TranscodeOpts
		segment  int
		wantOK   bool
		want     float64
	}{
		// Copy-mode fragments have variable durations, so a segment the manifest
		// can't yet place must NOT fall back to fixed-duration seg×dur math (which
		// would seek FFmpeg to the wrong source time and desync A/V). Instead it
		// reports the seek target as unresolved (0, false, nil).
		{
			name: "copy segment outside manifest is unresolved",
			manifest: strings.Join([]string{
				"#EXTM3U",
				"#EXT-X-VERSION:7",
				"#EXT-X-TARGETDURATION:3",
				"#EXT-X-MEDIA-SEQUENCE:9",
				"#EXT-X-MAP:URI=\"init.mp4\"",
				"#EXTINF:2.669000,",
				"seg_00009.m4s",
				"#EXTINF:1.669000,",
				"seg_00010.m4s",
				"",
			}, "\n"),
			opts:    TranscodeOpts{SeekSeconds: 18.261, TargetCodecVideo: "copy", SegmentDuration: 2},
			segment: 50,
		},
		{
			name:    "encoded uses fixed duration math",
			opts:    TranscodeOpts{TargetCodecVideo: "h264", SegmentDuration: 2},
			segment: 50,
			wantOK:  true,
			want:    100,
		},
		// A freshly reconstructed copy-mode window has a near-empty/absent manifest
		// (ErrManifestNotReady). The seek target must be reported unresolved
		// (0, false, nil) so the caller retries instead of seeking to a fabricated
		// seg×dur position.
		{
			name:    "copy manifest not ready is unresolved",
			opts:    TranscodeOpts{TargetCodecVideo: "copy", SegmentDuration: 2},
			segment: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &TranscodeSession{outputDir: t.TempDir(), opts: tt.opts}
			if tt.manifest != "" {
				writeManifestFixture(t, session.outputDir, tt.manifest)
			}
			got, ok, err := session.RestartSeekTarget(tt.segment)
			if err != nil {
				t.Fatalf("RestartSeekTarget: %v", err)
			}
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("RestartSeekTarget(%d) = %f, %v; want %f, %v", tt.segment, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestWaitForSegment_RestartingSessionReturnsNotFoundInsteadOfTranscodeFailed(t *testing.T) {
	session := &TranscodeSession{
		outputDir:  t.TempDir(),
		restarting: &restartFlight{done: make(chan struct{})},
		waitErr:    errors.New("signal: killed"),
	}

	_, err := session.WaitForSegment("seg_00010.m4s", 5*time.Millisecond)
	if !errors.Is(err, ErrSegmentNotFound) {
		t.Fatalf("WaitForSegment error = %v, want ErrSegmentNotFound", err)
	}
}

func noRestart(t *testing.T, d SegmentRecoveryDecision) {
	t.Helper()
	if d.RestartOnTimeout {
		t.Fatal("RestartOnTimeout = true, want false")
	}
}

func waitsWithoutRestart(t *testing.T, d SegmentRecoveryDecision) {
	t.Helper()
	if d.WaitTimeout != 12*time.Second {
		t.Fatalf("WaitTimeout = %s, want 12s", d.WaitTimeout)
	}
	noRestart(t, d)
}

var segmentRecoveryDecisionCases = []struct {
	name                 string
	setup                func(t *testing.T, dir string, now time.Time)
	running, restarting  bool
	lastRequestedSegment int
	startSegment         int
	request              int
	wantWait             bool
	wantReason           string
	check                func(*testing.T, SegmentRecoveryDecision)
}{
	{
		name: "waits for fresh next segment",
		setup: func(t *testing.T, dir string, now time.Time) {
			writeManifestRange(t, dir, 224, 227, ".ts")
			writeSegmentFile(t, dir, "seg_00224.ts", []byte("x"), now.Add(-2*time.Second))
			writeSegmentFile(t, dir, "seg_00225.ts", []byte("x"), now.Add(-time.Second))
			writeSegmentFile(t, dir, "seg_00226.ts", nil, now)
		},
		running: true, lastRequestedSegment: 225, startSegment: 224, request: 226,
		wantWait: true, wantReason: "near_produced_head",
		check: func(t *testing.T, d SegmentRecoveryDecision) {
			waitsWithoutRestart(t, d)
			if p := d.Progress; p.ProducedHead != 225 || !p.HasManifest {
				t.Fatalf("progress = %+v, want ready files ending at 225 with a manifest", p)
			}
		},
	},
	{
		name:    "uses longer wait for startup segment",
		running: true, startSegment: 0, request: 0,
		wantWait: true, wantReason: "startup_manifest_not_ready",
		check: waitsWithoutRestart,
	},
	{
		name: "restarts when produced output is stale",
		setup: func(t *testing.T, dir string, now time.Time) {
			writeManifestRange(t, dir, 224, 226, ".ts")
			writeSegmentFile(t, dir, "seg_00224.ts", []byte("x"), now.Add(-12*time.Second))
			writeSegmentFile(t, dir, "seg_00225.ts", []byte("x"), now.Add(-10*time.Second))
		},
		running: true, lastRequestedSegment: 225, startSegment: 224, request: 226,
		wantReason: "produced_output_stale",
		check: func(t *testing.T, d SegmentRecoveryDecision) {
			if !d.RestartOnTimeout {
				t.Fatal("RestartOnTimeout = false, want true")
			}
		},
	},
	{
		name: "does not use stale request history as produced head",
		setup: func(t *testing.T, dir string, now time.Time) {
			writeManifestRange(t, dir, 833, 833, ".ts")
			writeSegmentFile(t, dir, "seg_00833.ts", []byte("x"), now.Add(-time.Second))
		},
		running: true, lastRequestedSegment: 2446, startSegment: 833, request: 1597,
		wantReason: "request_beyond_produced_window",
		check: func(t *testing.T, d SegmentRecoveryDecision) {
			if d.Progress.ProducedHead != 833 {
				t.Fatalf("ProducedHead = %d, want 833", d.Progress.ProducedHead)
			}
		},
	},
	// Half of issue #243's seek-freeze: while a restart is already in
	// flight, a concurrent segment request must WAIT for the restart's
	// output rather than trigger another restart. Without this, pipelined
	// HLS segment requests spawn dueling ffmpeg restarts that keep
	// preempting the segment the player is blocked on. A timed-out wait
	// must re-decide, not blindly restart.
	{
		name:       "waits while restarting",
		restarting: true, startSegment: 0, request: 10,
		wantWait: true, wantReason: "transcode_restarting",
		check: noRestart,
	},
}

func TestSegmentRecoveryDecision(t *testing.T) {
	for _, tt := range segmentRecoveryDecisionCases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			now := time.Now()
			if tt.setup != nil {
				tt.setup(t, dir, now)
			}
			session := &TranscodeSession{
				outputDir:            dir,
				running:              tt.running,
				lastRequestedSegment: tt.lastRequestedSegment,
				opts: TranscodeOpts{
					TargetCodecVideo:   "h264",
					SegmentDuration:    2,
					StartSegmentNumber: tt.startSegment,
				},
			}
			if tt.restarting {
				session.restarting = &restartFlight{done: make(chan struct{})}
			}

			decision := session.SegmentRecoveryDecision(tt.request, now)
			if decision.Wait != tt.wantWait {
				t.Fatalf("Wait = %v, want %v (reason=%s)", decision.Wait, tt.wantWait, decision.Reason)
			}
			if decision.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %s", decision.Reason, tt.wantReason)
			}
			tt.check(t, decision)
		})
	}
}

func TestTranscodeThrottlerUsesProducedHeadForGap(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, tempDir, 225, 293, ".ts")
	for _, i := range []int{225, 254, 293} {
		writeSegmentFile(t, tempDir, segmentFilename(i, TranscodeOpts{TargetCodecVideo: "h264"}), []byte("x"), now)
	}

	session := &TranscodeSession{
		outputDir:            tempDir,
		running:              true,
		lastRequestedSegment: 225,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 225,
		},
	}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(session, writer, 60, 2)

	throttler.CheckOnce()
	if !throttler.paused {
		t.Fatal("expected throttler to pause")
	}
	if writer.writes != "p" {
		t.Fatalf("writes = %q, want p", writer.writes)
	}

	writeManifestRange(t, tempDir, 225, 254, ".ts")
	throttler.CheckOnce()
	if throttler.paused {
		t.Fatal("expected throttler to resume")
	}
	if writer.writes != "pu" {
		t.Fatalf("writes = %q, want pu", writer.writes)
	}
}

type recordingWriteCloser struct {
	writes string
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	w.writes += string(p)
	return len(p), nil
}

func (w *recordingWriteCloser) Close() error {
	return nil
}

func writeManifestRange(t testing.TB, dir string, first int, last int, ext string) {
	t.Helper()

	lines := []string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:" + strconv.Itoa(first),
		"#EXT-X-INDEPENDENT-SEGMENTS",
	}
	for i := first; i <= last; i++ {
		lines = append(lines, "#EXTINF:2.000000,", fmt.Sprintf("seg_%05d%s", i, ext))
	}
	lines = append(lines, "")

	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func writeSegmentFile(t *testing.T, dir string, name string, data []byte, modTime time.Time) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write segment %s: %v", name, err)
	}
	if err := os.Chtimes(filepath.Join(dir, name), modTime, modTime); err != nil {
		t.Fatalf("chtimes segment %s: %v", name, err)
	}
}

func TestSegmentProgressFindsNewestListedSegment(t *testing.T) {
	// Each playlist lists segments 0-9. Segments in present are written in
	// order, two seconds apart, as FFmpeg writes them; empty ones after them.
	tests := []struct {
		name     string
		start    int
		present  []int
		empty    []int
		wantHead int
		wantLast int // the segment whose write time is reported, or -1 for none
	}{
		{name: "pruned prefix", present: []int{6, 7, 8, 9}, wantHead: 9, wantLast: 9},
		{
			name:    "missing and zero-byte tail, gap, unlisted newer file",
			present: []int{0, 1, 2, 5, 6, 12}, empty: []int{7},
			wantHead: 6, wantLast: 6,
		},
		{name: "playlist left behind a later start", start: 40, present: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, wantHead: 39, wantLast: 9},
		{name: "nothing listed on disk", start: 3, wantHead: 2, wantLast: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifestRange(t, dir, 0, 9, ".ts")
			written := time.Now().Add(-time.Hour).Truncate(time.Second)
			writtenAt := map[int]time.Time{}
			write := func(n int, data []byte) {
				written = written.Add(2 * time.Second)
				writeSegmentFile(t, dir, fmt.Sprintf("seg_%05d.ts", n), data, written)
				writtenAt[n] = written
			}
			for _, n := range tt.present {
				write(n, []byte("x"))
			}
			for _, n := range tt.empty {
				write(n, nil)
			}
			session := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{
				TargetCodecVideo: "h264", SegmentDuration: 2, StartSegmentNumber: tt.start,
			}}

			got := session.SegmentProgress(time.Now())
			if got.ProducedHead != tt.wantHead || !got.LastProducedAt.Equal(writtenAt[tt.wantLast]) || !got.HasManifest {
				t.Fatalf("progress head %d, last produced %v, manifest %t; want head %d, last produced %v (segment %d), manifest true",
					got.ProducedHead, got.LastProducedAt, got.HasManifest, tt.wantHead, writtenAt[tt.wantLast], tt.wantLast)
			}
		})
	}
}

// BenchmarkSegmentProgress reads progress from a playlist whose segments are
// all still on disk: 3,600 entries is a two-hour title at the default segment
// length, and 50,000 is the playlist cap FFmpeg is started with.
func BenchmarkSegmentProgress(b *testing.B) {
	for _, entries := range []int{3_600, maxSyntheticManifestSegments} {
		b.Run(strconv.Itoa(entries), func(b *testing.B) {
			dir := b.TempDir()
			writeManifestRange(b, dir, 0, entries-1, ".ts")
			for n := range entries {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("seg_%05d.ts", n)), []byte("x"), 0o644); err != nil {
					b.Fatal(err)
				}
			}
			session := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{TargetCodecVideo: "h264", SegmentDuration: 2}}
			b.ReportAllocs()
			for b.Loop() {
				if progress := session.SegmentProgress(time.Now()); progress.ProducedHead != entries-1 {
					b.Fatalf("ProducedHead = %d, want %d", progress.ProducedHead, entries-1)
				}
			}
		})
	}
}

func TestCleanStaleOutputForRestart_CopyToToneMapEncodeRemovesStaleOutput(t *testing.T) {
	tempDir := t.TempDir()

	files := map[string]bool{
		"something.txt": true,
		"init.mp4":      true,  // codec config is source-derived — survives
		"stream.m3u8":   false, // describes the copy stream — removed
		"seg_00005.m4s": true,  // before the restart point — survives
		"seg_00006.m4s": true,  // before the restart point — survives
		"seg_00007.m4s": false, // copy bitstream at/after restart — removed
		"seg_00120.m4s": false, // copy bitstream far ahead — removed
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(tempDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	previous := TranscodeOpts{TargetCodecVideo: "copy", SegmentDuration: 2}
	next := TranscodeOpts{
		TargetCodecVideo: "h264",
		SegmentDuration:  2,
		HWAccel:          "qsv",
		ToneMapMode:      tonemap.ModeHardware,
		ToneMapFilter:    "tonemap_opencl",
	}

	session := &TranscodeSession{outputDir: tempDir, opts: previous}

	if !session.cleanStaleOutputForRestart(previous, next, 7) {
		t.Fatal("cleanStaleOutputForRestart = false, want true for a copy -> tone-map restart")
	}

	for name, shouldExist := range files {
		_, err := os.Stat(filepath.Join(tempDir, name))
		if exists := err == nil; exists != shouldExist {
			if shouldExist {
				t.Errorf("expected %s to survive cleanup, but it was removed", name)
			} else {
				t.Errorf("expected %s to be removed, but it still exists", name)
			}
		}
	}
}

func TestCleanStaleOutputForRestart_EncodedRecipe(t *testing.T) {
	hardware := TranscodeOpts{TargetCodecVideo: "h264", HWAccel: "qsv", ToneMapMode: tonemap.ModeHardware, ToneMapFilter: "tonemap_opencl"}
	tests := []struct {
		name      string
		next      TranscodeOpts
		wantClean bool
	}{
		{
			name:      "tone-map mode change removes stale output",
			next:      TranscodeOpts{TargetCodecVideo: "h264", HWAccel: HWAccelNone, ToneMapMode: tonemap.ModeSoftware, ToneMapFilter: "tonemap"},
			wantClean: true,
		},
		// A backward seek within one generation keeps its segments reusable.
		{name: "same encoded recipe keeps segments", next: hardware},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			writeManifestRange(t, tempDir, 7, 9, ".ts")
			if err := os.WriteFile(filepath.Join(tempDir, "seg_00008.ts"), []byte("x"), 0o644); err != nil {
				t.Fatalf("write segment: %v", err)
			}
			session := &TranscodeSession{outputDir: tempDir, opts: hardware}

			if got := session.cleanStaleOutputForRestart(hardware, tt.next, 7); got != tt.wantClean {
				t.Fatalf("cleanStaleOutputForRestart = %v, want %v", got, tt.wantClean)
			}
			for _, name := range []string{"stream.m3u8", "seg_00008.ts"} {
				_, err := os.Stat(filepath.Join(tempDir, name))
				if tt.wantClean && err == nil {
					t.Errorf("expected the previous generation's %s to be removed", name)
				}
				if !tt.wantClean && err != nil {
					t.Errorf("expected %s to survive: %v", name, err)
				}
			}
		})
	}
}

func TestTranscodeThrottlerIgnoresOutputFromAnEarlierGeneration(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	staleTime := now.Add(-time.Minute)

	// A previous copy generation raced hundreds of segments ahead and left its
	// manifest behind; the current process has produced nothing yet.
	writeManifestRange(t, tempDir, 225, 293, ".ts")
	if err := os.Chtimes(filepath.Join(tempDir, "stream.m3u8"), staleTime, staleTime); err != nil {
		t.Fatalf("chtimes manifest: %v", err)
	}
	for _, i := range []int{225, 293} {
		writeSegmentFile(t, tempDir, segmentFilename(i, TranscodeOpts{TargetCodecVideo: "h264"}), []byte("x"), staleTime)
	}

	session := &TranscodeSession{
		outputDir:            tempDir,
		running:              true,
		lastRequestedSegment: 225,
		generationStartedAt:  now,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 225,
		},
	}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(session, writer, 60, 2)

	throttler.CheckOnce()
	if throttler.paused {
		t.Fatal("throttler paused on output produced before the current generation started")
	}
	if writer.writes != "" {
		t.Fatalf("writes = %q, want no command", writer.writes)
	}

	// A throttler that already paused on stale output must let ffmpeg go again.
	throttler.paused = true
	throttler.CheckOnce()
	if throttler.paused {
		t.Fatal("throttler stayed paused on stale output")
	}
	if writer.writes != "u" {
		t.Fatalf("writes = %q, want u", writer.writes)
	}

	// Once this generation writes its own manifest, throttling works normally.
	//
	// The mtime is set rather than inherited from the write. Staleness is
	// decided by ManifestModTime.Before(GenerationStartedAt), and a filesystem
	// with coarse mtime granularity — which a CI runner's overlay can have and a
	// developer's APFS does not — truncates a write made at `now` back below it,
	// so the fresh manifest reads as older than the generation that produced it.
	fresh := now.Add(time.Second)
	writeManifestRange(t, tempDir, 225, 293, ".ts")
	if err := os.Chtimes(filepath.Join(tempDir, "stream.m3u8"), fresh, fresh); err != nil {
		t.Fatalf("chtimes manifest: %v", err)
	}
	throttler.CheckOnce()
	if !throttler.paused {
		t.Fatal("expected throttler to pause on this generation's own produced head")
	}
	if writer.writes != "up" {
		t.Fatalf("writes = %q, want up", writer.writes)
	}
}

func TestAppendManifestQueryParam(t *testing.T) {
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MAP:URI=\"segment/init.mp4\"",
		"#EXTINF:2.000000,",
		"segment/seg_00000.ts",
		"#EXTINF:2.000000,",
		"segment/seg_00001.ts?existing=1",
		"",
	}, "\n")

	got := string(AppendManifestQueryParam([]byte(manifest), "st", "TOKEN"))
	for _, want := range []string{
		"#EXT-X-MAP:URI=\"segment/init.mp4?st=TOKEN\"",
		"segment/seg_00000.ts?st=TOKEN",
		"segment/seg_00001.ts?existing=1&st=TOKEN",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten manifest missing %q:\n%s", want, got)
		}
	}
	// Tags and EXTINF lines must be untouched.
	if !strings.Contains(got, "#EXT-X-TARGETDURATION:2") || strings.Contains(got, "#EXT-X-TARGETDURATION:2?st") {
		t.Fatalf("non-URI tag was rewritten:\n%s", got)
	}
}

func TestAppendManifestQueryParam_NonManifestUnchanged(t *testing.T) {
	body := []byte("not a manifest\nsegment/seg_00000.ts\n")
	if got := AppendManifestQueryParam(body, "st", "TOKEN"); string(got) != string(body) {
		t.Fatalf("non-manifest body should be unchanged, got:\n%s", got)
	}
	if got := AppendManifestQueryParam([]byte("#EXTM3U\nseg.ts\n"), "", "TOKEN"); !strings.Contains(string(got), "seg.ts\n") || strings.Contains(string(got), "seg.ts?") {
		t.Fatalf("empty key should be a no-op, got:\n%s", got)
	}
}

func TestSourceAlignedCopyStartHintSurvivesReload(t *testing.T) {
	for _, tc := range []struct {
		name              string
		requested, origin float64
		segment           int
	}{
		{"forward", 900, 898.125, 449},
		{"backward", 300, 298.125, 149},
		{"origin at zero", 0.5, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := TranscodeOpts{TargetCodecVideo: "copy", SegmentDuration: 2, TotalDuration: 3600, SeekSeconds: tc.requested, StreamOriginSeconds: tc.origin, CopySeekAnchorResolved: true, StartSegmentNumber: tc.segment}
			raw := fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2.700000,\nseg_%05d.m4s\n", tc.segment, tc.segment)
			for reload := range 3 {
				manifest := stabilizeCopyHLSRemountTimeline([]byte(raw), opts)
				if !bytes.Contains(manifest, []byte("#EXT-X-START:TIME-OFFSET=0.001,")) {
					t.Fatal("fixture lacks generation-relative start")
				}
				aligned, err := AlignRealManifestToSourceTimeline(manifest, opts, "gap.m4s")
				if err != nil {
					t.Fatal(err)
				}
				expected := fmt.Sprintf("#EXT-X-START:TIME-OFFSET=%.6f,PRECISE=YES", tc.requested)
				if !bytes.Contains(aligned, []byte(expected)) || bytes.Count(aligned, []byte("#EXT-X-START:")) != 1 {
					t.Fatalf("reload %d wrong source-time start:\n%s", reload, aligned)
				}
				if bytes.Contains(aligned, []byte("#EXT-X-START:TIME-OFFSET=0.001,")) {
					t.Fatal("start still points into unavailable gap prefix")
				}
				raw += fmt.Sprintf("#EXTINF:1.700000,\nseg_%05d.m4s\n", tc.segment+reload+1)
			}
		})
	}
}
