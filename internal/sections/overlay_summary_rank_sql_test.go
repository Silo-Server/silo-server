package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/overlays"
)

// TestOverlaySummaryRankSQLMatchesGo pins the SQL mirror of overlays.BestFile.
// ListOverlaySummaries reduces each card to one file inside PostgreSQL, so the
// ordering encoded in overlaySummaryResolutionRankSQL and
// overlaySummaryRangeRankSQL has to agree with overlays.ResolutionRank and
// overlays.RangeRank. Drift here silently changes which file a card's badges
// come from, which no other test would catch.
//
// The expressions are evaluated over a VALUES list aliased as mf, so the test
// needs a PostgreSQL connection but no schema and no fixtures.
func TestOverlaySummaryRankSQLMatchesGo(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	cases := []struct {
		name      string
		file      models.MediaFile
		rawTracks string
	}{
		{"tab newline resolution", models.MediaFile{Resolution: "\t2160p\n"}, ""},
		{"all ASCII whitespace", models.MediaFile{Resolution: " \t\n\v\f\r2160p \t\n\v\f\r"}, ""},
		{"leading plus", models.MediaFile{Resolution: "+2160p"}, ""},
		{"leading zeros", models.MediaFile{Resolution: "0000002160p"}, ""},
		{"padded signed zeros", models.MediaFile{Resolution: "\t+002160p\t"}, ""},
		{"negative", models.MediaFile{Resolution: "-2160p"}, ""},
		{"18 digits", models.MediaFile{Resolution: "999999999999999999p"}, ""},
		{"null dolby vision", models.MediaFile{Resolution: "1080p"}, `[{"dolby_vision":null}]`},
		{"null dolby vision with HDR10", models.MediaFile{Resolution: "1080p"}, `[{"dolby_vision":null,"video_range_type":"HDR10"}]`},
		{"absent dolby vision", models.MediaFile{Resolution: "1080p"}, `[{}]`},
		{"empty", models.MediaFile{}, ""},
		{"1080p", models.MediaFile{Resolution: "1080p"}, ""},
		{"2160p", models.MediaFile{Resolution: "2160p"}, ""},
		{"480p", models.MediaFile{Resolution: "480p"}, ""},
		{"4k alias", models.MediaFile{Resolution: "4K"}, ""},
		{"uhd alias", models.MediaFile{Resolution: "UHD"}, ""},
		{"padded resolution", models.MediaFile{Resolution: "  1080P  "}, ""},
		{"unparseable resolution", models.MediaFile{Resolution: "hd"}, ""},
		{"numeric without p", models.MediaFile{Resolution: "1080"}, ""},
		{"hdr bool only", models.MediaFile{Resolution: "1080p", HDR: true}, ""},
		{"dolby vision string", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{DolbyVision: "profile 8"}}}, ""},
		{"dv profile", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{DVProfile: 5}}}, ""},
		{"dovi range type", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: "DOVIWithHDR10"}}}, ""},
		{"dovi range type padded", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: " DOVIWithHLG"}}}, ""},
		{"hdr10 plus flag", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{HDR10Plus: true}}}, ""},
		{"hdr10 plus range", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: "HDR10Plus"}}}, ""},
		{"hdr10 range", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: "HDR10"}}}, ""},
		{"hdr10 range padded", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: " HDR10 "}}}, ""},
		{"with hdr10 suffix", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: "SomethingWithHDR10"}}}, ""},
		{"hlg range", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: "HLG"}}}, ""},
		{"with hlg suffix", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: "SomethingWithHLG"}}}, ""},
		{"smpte2084 transfer", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{ColorTransfer: "SMPTE2084"}}}, ""},
		{"arib transfer", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{ColorTransfer: "ARIB-STD-B67"}}}, ""},
		{"sdr tracks", models.MediaFile{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "SDR"}}}, ""},
		{"sdr tracks with hdr bool", models.MediaFile{Resolution: "1080p", HDR: true, VideoTracks: []models.VideoTrack{{VideoRangeType: "SDR"}}}, ""},
		{"second track carries hdr", models.MediaFile{Resolution: "2160p", VideoTracks: []models.VideoTrack{{VideoRangeType: "SDR"}, {VideoRangeType: "HDR10"}}}, ""},
	}

	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	query := fmt.Sprintf(
		"SELECT %s, %s, %s FROM (VALUES ($1::text, $2::boolean, $3::jsonb)) AS mf(resolution, hdr, video_tracks)",
		overlaySummaryResolutionRankSQL, overlaySummaryRangeRankSQL, catalog.MediaFileQualityCeilingSQL("mf", "1080p"),
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := tc.file
			tracks, err := json.Marshal(file.VideoTracks)
			if err != nil {
				t.Fatalf("marshaling video tracks: %v", err)
			}

			if tc.rawTracks != "" {
				tracks = []byte(tc.rawTracks)
				if err := json.Unmarshal(tracks, &file.VideoTracks); err != nil {
					t.Fatalf("decoding raw video tracks: %v", err)
				}
			}

			var sqlAllowed bool
			var sqlResolution, sqlRange int
			if err := conn.QueryRow(ctx, query, file.Resolution, file.HDR, string(tracks)).Scan(&sqlResolution, &sqlRange, &sqlAllowed); err != nil {
				t.Fatalf("evaluating rank SQL: %v", err)
			}

			if want := catalog.FileAllowedByAccess(&file, catalog.AccessFilter{MaxPlaybackQuality: "1080p"}); sqlAllowed != want {
				t.Errorf("quality ceiling for %q: SQL %t, Go %t", file.Resolution, sqlAllowed, want)
			}
			if want := overlays.ResolutionRank(file.Resolution); sqlResolution != want {
				t.Errorf("resolution rank for %q: SQL %d, Go %d", file.Resolution, sqlResolution, want)
			}
			if want := overlays.RangeRank(&file); sqlRange != want {
				t.Errorf("range rank for %+v: SQL %d, Go %d", file.VideoTracks, sqlRange, want)
			}
		})
	}
}
