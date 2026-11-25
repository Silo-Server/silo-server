package handlers

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// Resolution classes on the Devices tab use the scanner's width+height buckets
// (scanner.ResolutionLabel), the scale media_files.resolution is stored on.
// A transcode target is recorded as its exact output height ("1600p" for a
// full-size encode of a 3840x1600 file, "800p" for that file's 1080p rung), so
// it is classified with the source's aspect ratio before comparing: bucketing
// heights alone would call every cinema-scope encode a downscale.
var insightsResolutionClassRank = map[string]int{"480p": 1, "720p": 2, "1080p": 3, "2160p": 4, "4320p": 5}

// insightsSourceResolutionClass classifies a source file from its probed
// dimensions, falling back to its stored label for files probed before
// video_tracks carried dimensions.
func insightsSourceResolutionClass(label string, width, height int) string {
	if width > 0 && height > 0 {
		return scanner.ResolutionLabel(width, height)
	}
	switch label = strings.ToLower(strings.TrimSpace(label)); label {
	case "4k", "uhd":
		return "2160p"
	case "8k":
		return "4320p"
	case "sd":
		return "480p"
	}
	if lines, ok := insightsResolutionLines(label); ok {
		return scanner.ResolutionLabel(lines*16/9, lines)
	}
	return insightsUnknownKey
}

// insightsDeliveredResolutionClass classifies what a session sent. Direct play,
// remux and transcodes that copied the video deliver the source frame, as
// does a recorded transcode with no target resolution (it kept the source
// size; see the live session view). An "Np" target takes the source's aspect
// ratio, or 16:9 when the source has no dimensions. A transcode recorded
// before route facts has no target and stays unknown.
func insightsDeliveredResolutionClass(playMethod, videoDecision, targetVideoCodec, target string, routeFacts bool, sourceClass string, srcWidth, srcHeight int) string {
	switch canonicalPlayMethod(playMethod) {
	case string(playback.PlayDirect), string(playback.PlayRemux):
		return sourceClass
	case string(playback.PlayTranscode):
		// Classified from the decision and target below.
	default:
		return insightsUnknownKey
	}
	if videoDecision == effectivePlayMethodDirect || videoDecision == effectivePlayMethodRemux || strings.EqualFold(targetVideoCodec, "copy") {
		return sourceClass
	}
	target = strings.ToLower(strings.TrimSpace(target))
	if lines, ok := insightsResolutionLines(target); ok {
		// The planner's width for a height: the source's aspect, rounded down
		// to an even pixel count (see the explicit-rung path in plan_v3.go).
		width := lines * 16 / 9
		if srcWidth > 0 && srcHeight > 0 {
			width = srcWidth * lines / srcHeight
		}
		width -= width % 2
		return scanner.ResolutionLabel(width, lines)
	}
	if routeFacts && (target == "" || target == "original") {
		return sourceClass
	}
	return insightsUnknownKey
}

// insightsResolutionLines parses the line count of an "Np" label, ignoring a
// rung suffix such as "1080p-high".
func insightsResolutionLines(label string) (int, bool) {
	label, _, _ = strings.Cut(label, "-")
	digits, ok := strings.CutSuffix(label, "p")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// queryInsightsResolutions fills the source and delivered resolution
// breakdowns and the downscale pairs. It groups the window's sessions by the
// raw facts and classifies them here, so the classifier stays the scanner's.
func queryInsightsResolutions(ctx context.Context, tx pgx.Tx, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	rows, err := tx.Query(ctx, `
		SELECT
			COALESCE(mf.resolution, ''),
			CASE WHEN (mf.video_tracks->0->>'width') ~ '^[0-9]{1,5}$' THEN (mf.video_tracks->0->>'width')::int ELSE 0 END,
			CASE WHEN (mf.video_tracks->0->>'height') ~ '^[0-9]{1,5}$' THEN (mf.video_tracks->0->>'height')::int ELSE 0 END,
			LOWER(BTRIM(h.play_method)), LOWER(BTRIM(COALESCE(h.video_decision, ''))),
			LOWER(BTRIM(COALESCE(h.target_video_codec, ''))), COALESCE(h.target_resolution, ''),
			h.route_facts_recorded,
			COUNT(*)::bigint
		FROM admin_playback_history h
		LEFT JOIN media_files mf ON mf.id = COALESCE(h.effective_media_file_id, h.media_file_id)
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
		WHERE `+whereSQL+`
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights resolutions: %w", err)
	}
	defer rows.Close()
	source := make(map[string]int64)
	delivered := make(map[string]int64)
	type change struct{ from, to string }
	changes := make(map[change]int64)
	for rows.Next() {
		var label, method, videoDecision, targetCodec, target string
		var width, height int
		var routeFacts bool
		var sessions int64
		if err := rows.Scan(&label, &width, &height, &method, &videoDecision, &targetCodec, &target, &routeFacts, &sessions); err != nil {
			return fmt.Errorf("scanning insights resolutions: %w", err)
		}
		src := insightsSourceResolutionClass(label, width, height)
		dst := insightsDeliveredResolutionClass(method, videoDecision, targetCodec, target, routeFacts, src, width, height)
		source[src] += sessions
		delivered[dst] += sessions
		if srcRank, dstRank := insightsResolutionClassRank[src], insightsResolutionClassRank[dst]; srcRank > 0 && dstRank > 0 && dstRank < srcRank {
			changes[change{src, dst}] += sessions
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out.SourceResolutions = sortedResolutionCounts(source)
	out.DeliveredResolutions = sortedResolutionCounts(delivered)
	for c, n := range changes {
		out.ResolutionChanges = append(out.ResolutionChanges, AdminInsightsResolutionChange{From: c.from, To: c.to, Sessions: n})
	}
	slices.SortFunc(out.ResolutionChanges, func(a, b AdminInsightsResolutionChange) int {
		if c := cmp.Compare(b.Sessions, a.Sessions); c != 0 {
			return c
		}
		if c := cmp.Compare(insightsResolutionClassRank[b.From], insightsResolutionClassRank[a.From]); c != 0 {
			return c
		}
		return cmp.Compare(insightsResolutionClassRank[b.To], insightsResolutionClassRank[a.To])
	})
	return nil
}

// sortedResolutionCounts orders classes from highest to lowest resolution,
// unknown last, so the two breakdowns line up.
func sortedResolutionCounts(m map[string]int64) []AdminInsightsLabeledCount {
	res := make([]AdminInsightsLabeledCount, 0, len(m))
	for k, v := range m {
		res = append(res, AdminInsightsLabeledCount{Key: k, Sessions: v})
	}
	slices.SortFunc(res, func(a, b AdminInsightsLabeledCount) int {
		return cmp.Compare(insightsResolutionClassRank[b.Key], insightsResolutionClassRank[a.Key])
	})
	return res
}
