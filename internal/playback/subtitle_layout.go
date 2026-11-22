package playback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Virtual release rotation: a playback session plans a subtitle URL against
// the exact subtitle layout a provider release had when it was probed, but the
// pinned URI can resolve to a rotated rendition with a different layout by
// extraction time. The helpers in this file let the native virtual extraction
// path verify the live layout cheaply and re-map the plan ordinal onto a
// same-class live track instead of letting ffmpeg fail on `0:s:N`.

// subtitleProbeTimeout is the floor for the live-layout ffprobe. Subtitle
// streams are described near the container head, so a healthy probe finishes in
// well under a second. The floor is not a wall: a provider link that just took
// seconds to answer a listing gets a proportionally longer probe (see
// AdaptiveProbeBudget), because the same upstream first-byte spike that slowed
// the listing also delays the header this probe reads.
const subtitleProbeTimeout = RemoteProbeFloor

// SubtitleProbeMax caps an adaptive layout-probe budget. A slow link gets room
// to answer, but a stuck upstream must not hold the subtitle request: past this
// bound the caller fails closed and the client falls back to subtitles off.
const SubtitleProbeMax = 15 * time.Second

// SubtitleLayoutsEqual reports whether two embedded subtitle layouts are
// track-for-track identical on every attribute the extraction path depends on:
// container index, codec, language, forced/default dispositions,
// hearing-impaired flag, and the container track ID used as the plan-time
// identity pin. Callers compare a catalog row's layout against the session's
// plan-time evidence to detect a virtual release rotation.
func SubtitleLayoutsEqual(a, b []models.SubtitleTrack) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		at, bt := a[i], b[i]
		if at.Index != bt.Index ||
			normalizeCodecV3(at.Codec) != normalizeCodecV3(bt.Codec) ||
			lang.Canonical(at.Language) != lang.Canonical(bt.Language) ||
			at.Forced != bt.Forced ||
			at.Default != bt.Default ||
			at.HearingImpaired != bt.HearingImpaired ||
			canonicalSubtitleContainerTrackID(at.ContainerTrackID) != canonicalSubtitleContainerTrackID(bt.ContainerTrackID) {
			return false
		}
	}
	return true
}

// ExternalSubtitleLayoutsEqual reports whether two sidecar subtitle layouts are
// identical on every attribute the extraction path depends on: path, language,
// format, forced/default, and hearing-impaired. A release rotation replaces the
// sidecar set the same way it replaces embedded tracks, so drift detection must
// compare externals too or a rotated sidecar layout is served on the previous
// release's ordinal.
func ExternalSubtitleLayoutsEqual(a, b []models.ExternalSubtitle) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		at, bt := a[i], b[i]
		if at.Path != bt.Path ||
			lang.Canonical(at.Language) != lang.Canonical(bt.Language) ||
			normalizeCodecV3(at.Format) != normalizeCodecV3(bt.Format) ||
			at.Forced != bt.Forced ||
			at.Default != bt.Default ||
			at.HearingImpaired != bt.HearingImpaired {
			return false
		}
	}
	return true
}

// SubtitleLayoutsEqualIncludingExternal compares the full combined subtitle
// inventory (externals then embedded) of two sources. It is the drift check the
// serve path uses: the published combined ordinal ranges over both segments, so
// a change confined to the sidecar set changes the meaning of every embedded
// ordinal after it.
func SubtitleLayoutsEqualIncludingExternal(
	aEmbedded []models.SubtitleTrack, aExternal []models.ExternalSubtitle,
	bEmbedded []models.SubtitleTrack, bExternal []models.ExternalSubtitle,
) bool {
	return SubtitleLayoutsEqual(aEmbedded, bEmbedded) && ExternalSubtitleLayoutsEqual(aExternal, bExternal)
}

// AudioLayoutsEqual reports whether two audio inventories are identical on
// every attribute the selection and extraction path depends on: container
// index, codec, language/languages, layout, channel count, and the display and
// embedded titles. Titles are identity evidence: a release can reorder a main
// and a commentary track that agree on language, codec, channels and layout, so
// without them the carried ordinal would silently move to the other stream. A
// same-row virtual candidate rotation replaces the row's probed tracks without
// moving the row id, so callers compare this fingerprint instead of id equality
// to decide whether a carried selection still names the same track.
func AudioLayoutsEqual(a, b []models.AudioTrack) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		at, bt := a[i], b[i]
		if at.Index != bt.Index ||
			normalizeCodecV3(at.Codec) != normalizeCodecV3(bt.Codec) ||
			lang.Canonical(at.Language) != lang.Canonical(bt.Language) ||
			!strings.EqualFold(strings.TrimSpace(at.Layout), strings.TrimSpace(bt.Layout)) ||
			at.Channels != bt.Channels ||
			at.Profile != bt.Profile ||
			!trackStringEqual(at.Title, bt.Title) ||
			!trackStringEqual(at.EmbeddedTitle, bt.EmbeddedTitle) ||
			!stringSlicesEqualFold(at.Languages, bt.Languages) {
			return false
		}
	}
	return true
}

// stringSlicesEqualFold compares two string slices order-independently by
// canonical lowercase form, so a MULTI/DUAL track's language list changes are
// detected regardless of the order the provider reports them in.
func stringSlicesEqualFold(a, b []string) bool {
	canonical := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, value := range in {
			value = strings.ToLower(strings.TrimSpace(value))
			if value != "" {
				out = append(out, value)
			}
		}
		sort.Strings(out)
		return out
	}
	// Normalize before comparing lengths: trimming can drop blank tokens, so a
	// raw-length check would admit differently-sized normalized slices and then
	// index past the shorter one (or, in the other order, silently compare only
	// a prefix and report false equality). Comparing the normalized slices keeps
	// the comparison symmetric.
	ca, cb := canonical(a), canonical(b)
	if len(ca) != len(cb) {
		return false
	}
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

// ProbeSubtitleLayout runs a bounded ffprobe against inputURL (a registered
// relay URL for virtual sources — the relay injects the upstream auth headers)
// and returns the container's embedded subtitle layout in stream order. Each
// returned track's position in the slice is the ffmpeg `0:s:N` ordinal the
// extraction path maps with; Index carries the container stream index.
//
// ffprobePathOrFFmpeg may be either an ffprobe path or the configured ffmpeg
// path (ffprobe ships next to ffmpeg in every distribution Silo supports).
// The probe runs under the floor budget (subtitleProbeTimeout); callers that
// observed a slower link use ProbeSubtitleLayoutWithin. A failure returns an
// error so the caller can decide whether a spawn can proceed unverified.
func ProbeSubtitleLayout(ctx context.Context, ffprobePathOrFFmpeg, inputURL string) ([]models.SubtitleTrack, error) {
	return ProbeSubtitleLayoutWithin(ctx, ffprobePathOrFFmpeg, inputURL, subtitleProbeTimeout)
}

// ProbeSubtitleLayoutWithin is ProbeSubtitleLayout with an explicit budget,
// normally playback.AdaptiveProbeBudget sized from the link's recent provider
// latency. A non-positive budget means no probe time is left and is reported
// without spawning ffprobe, so a caller that cannot afford a normal probe fails
// fast instead of being killed by its own deadline mid-read.
func ProbeSubtitleLayoutWithin(ctx context.Context, ffprobePathOrFFmpeg, inputURL string, budget time.Duration) ([]models.SubtitleTrack, error) {
	// A layout probe against a relay URL is a remote read like any other
	// subtitle demux, so it is admitted on the same per-source budget; a
	// probe that is skipped this way is a "can't validate right now" answer
	// whose callers already fail closed. A local input is never admitted.
	ctx, releaseRelayDemux, demuxErr := acquireRelayDemuxRead(ctx, inputURL)
	if demuxErr != nil {
		return nil, fmt.Errorf("probe subtitle layout skipped: %w", demuxErr)
	}
	defer releaseRelayDemux()
	if budget <= 0 {
		return nil, fmt.Errorf("probe subtitle layout skipped for %s: no remaining probe budget", inputURL)
	}

	probeCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	var stdout bytes.Buffer
	// The guarded launcher admits a relayed input only for the relay's own
	// protocols and demuxers; this probe reads a relay URL like any other
	// subtitle demux.
	cmd := fspath.CommandContext(probeCtx, probeSubtitleBin(ffprobePathOrFFmpeg),
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "s",
		inputURL,
	)
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		// No process: a missing or unexecutable ffprobe is not a probe run.
		return nil, fmt.Errorf("probe subtitle layout failed for %s: %w", inputURL, err)
	}
	// The child exists, so this is a real probe even if it later times out or
	// reports a bad layout.
	recordSubtitleLayoutProbeRan()
	if err := cmd.Wait(); err != nil {
		if probeCtx.Err() != nil {
			return nil, fmt.Errorf("probe subtitle layout timed out for %s: %w", inputURL, probeCtx.Err())
		}
		return nil, fmt.Errorf("probe subtitle layout failed for %s: %w", inputURL, err)
	}
	output := stdout.Bytes()

	var probed struct {
		Streams []mediaprobe.Stream `json:"streams"`
	}
	if err := json.Unmarshal(output, &probed); err != nil {
		return nil, fmt.Errorf("parse subtitle layout probe for %s: %w", inputURL, err)
	}

	tracks := make([]models.SubtitleTrack, 0, len(probed.Streams))
	for _, stream := range probed.Streams {
		if stream.CodecType != "subtitle" {
			continue
		}
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		if codec == "" {
			continue
		}
		tracks = append(tracks, models.SubtitleTrack{
			ContainerTrackID: canonicalSubtitleContainerTrackID(string(stream.ID)),
			Index:            stream.Index,
			Codec:            codec,
			Language:         lang.Canonical(stream.Tags["language"]),
			Forced:           stream.Disposition.Forced == 1,
			Default:          stream.Disposition.Default == 1,
			HearingImpaired:  subtitleTagBoolFlag(stream.Tags, "hearing_impaired"),
		})
	}
	return tracks, nil
}

// probeSubtitleBin resolves the ffprobe executable from either an ffprobe path
// or the configured ffmpeg path (whose sibling ffprobe is used).
func probeSubtitleBin(ffprobePathOrFFmpeg string) string {
	raw := strings.TrimSpace(ffprobePathOrFFmpeg)
	if raw == "" {
		return ffprobeBinary
	}
	if strings.Contains(strings.ToLower(filepath.Base(raw)), ffprobeBinary) {
		return raw
	}
	return ffprobePathFromFFmpeg(raw)
}

// MatchEmbeddedSubtitleTrack finds the unique live track that best matches the
// requested plan-time track under class preservation. "Class" is the output
// muxer family the sidecar URL promises: webvtt-family under .vtt, ass-copy
// under .ass, sup (PGS) under .sup. The match narrows the live candidates by
// class, then by language (when the requested track names one), then forced,
// then hearing-impaired, and only returns a match when exactly one candidate
// survives — a different-language or ambiguous layout is deliberately not
// served. The returned int is the live track's ordinal (its position in live),
// which is the ffmpeg `0:s:N` operand for the rotated source.
func MatchEmbeddedSubtitleTrack(requested models.SubtitleTrack, live []models.SubtitleTrack) (liveOrdinal int, matched models.SubtitleTrack, ok bool) {
	requestedClass := subtitleLayoutClass(requested.Codec)
	if requestedClass == "" || len(live) == 0 {
		return 0, models.SubtitleTrack{}, false
	}

	type candidate struct {
		ordinal int
		track   models.SubtitleTrack
	}
	candidates := make([]candidate, 0, len(live))
	for ordinal, track := range live {
		if subtitleLayoutClass(track.Codec) == requestedClass {
			candidates = append(candidates, candidate{ordinal: ordinal, track: track})
		}
	}
	if len(candidates) == 0 {
		return 0, models.SubtitleTrack{}, false
	}

	filter := func(match func(models.SubtitleTrack) bool) []candidate {
		out := make([]candidate, 0, len(candidates))
		for _, c := range candidates {
			if match(c.track) {
				out = append(out, c)
			}
		}
		return out
	}
	single := func(set []candidate) (int, models.SubtitleTrack, bool) {
		if len(set) == 1 {
			return set[0].ordinal, set[0].track, true
		}
		return 0, models.SubtitleTrack{}, false
	}

	// A requested language is a hard constraint, applied before the single-candidate
	// shortcut below: one surviving class candidate is still the wrong track when
	// the client asked for a different language.
	requestedLanguage := lang.Canonical(requested.Language)
	if requestedLanguage != "" {
		sameLanguage := filter(func(t models.SubtitleTrack) bool {
			return lang.Canonical(t.Language) == requestedLanguage
		})
		if len(sameLanguage) == 0 {
			return 0, models.SubtitleTrack{}, false
		}
		if ordinal, track, unique := single(sameLanguage); unique {
			return ordinal, track, true
		}
		candidates = sameLanguage
	}
	// With the language already pinned, a lone survivor needs no further
	// narrowing.
	if len(candidates) == 1 {
		return candidates[0].ordinal, candidates[0].track, true
	}

	sameForced := filter(func(t models.SubtitleTrack) bool { return t.Forced == requested.Forced })
	if len(sameForced) == 0 {
		return 0, models.SubtitleTrack{}, false
	}
	if ordinal, track, unique := single(sameForced); unique {
		return ordinal, track, true
	}
	candidates = sameForced

	sameHI := filter(func(t models.SubtitleTrack) bool { return t.HearingImpaired == requested.HearingImpaired })
	if len(sameHI) == 0 {
		return 0, models.SubtitleTrack{}, false
	}
	return single(sameHI)
}

// SubtitleExtractMuxer returns the ffmpeg output muxer an extraction of codec
// would use, honoring an explicitly forced target format (currently only
// "vtt"). The native stream handler uses it as the class-preservation gate for
// a virtual remap: the URL extension was minted at plan time, so a remap may
// only land on a live codec that produces the same muxer.
func SubtitleExtractMuxer(codec, targetFormat string) string {
	_, muxer := streamExtractOutput(codec, targetFormat)
	return muxer
}

// SubtitleCarryGenerationV3 names the evidence generation a carried subtitle
// selection was minted against. It travels with the carry key so a warning or a
// log can say which candidate, catalog row and source revision the ordinal
// belonged to; it is never used to widen a match across releases.
type SubtitleCarryGenerationV3 struct {
	CandidateURI   string
	EvidenceRowID  int
	SourceRevision string
}

// SubtitleCarryKeyV3 is the identity of one selected subtitle track as it was
// minted against an evidence generation. A virtual release rotation replaces
// the inventory behind the same catalog row, so the selection has to be carried
// by identity rather than by its old ordinal.
//
// The gates are deliberately narrow and fail closed:
//
//   - Source is a hard gate: external, embedded and downloaded tracks are never
//     crossed for one another.
//   - MuxerClass is a hard gate and must be one of webvtt, ass, or sup: the
//     subtitle URL extension was minted at plan time, so a carried selection may
//     only land on a codec that extracts to the same muxer family.
//   - Language is a hard gate (canonical form; an empty language matches only an
//     empty language).
//   - Forced and HearingImpaired are hard gates.
//   - CodecNormalized and Title are tiebreaks only. They can disambiguate
//     several tracks that already agree on every hard gate, but a difference in
//     either produces a fuzzy carry, never a miss.
//
// Downloaded subtitles have no file-shaped representation and are resolved by
// their stable row id first (DownloadedRowID), then by language and codec.
type SubtitleCarryKeyV3 struct {
	Source          string
	Language        string
	MuxerClass      string
	CodecNormalized string
	Forced          bool
	HearingImpaired bool
	Title           string
	// DownloadedRowID is the durable downloaded-subtitle row the selection
	// names. It is the only stable identity for a downloaded entry; its
	// language, codec and label can collide across rows.
	DownloadedRowID int
	Generation      SubtitleCarryGenerationV3
}

// SubtitleCarryMuxerClassV3 maps a subtitle codec (or sidecar format) to the
// output muxer family a carry may preserve. It mirrors subtitleLayoutClass, so
// an unrenderable codec has no class and can never be carried onto a deliverable
// one.
func SubtitleCarryMuxerClassV3(codec string) string {
	return subtitleLayoutClass(codec)
}

// SubtitleCarryCodecV3 normalizes a subtitle codec for the carry's codec
// tiebreak. The matcher normalizes both sides again, so this is a convenience
// for callers building a key.
func SubtitleCarryCodecV3(codec string) string {
	return normalizeCodecV3(codec)
}

// SubtitleCarryExternalLabelV3 is the displayed label of an external sidecar.
// It mirrors the inventory label so a key built from a file row compares equal
// to one built from published evidence.
func SubtitleCarryExternalLabelV3(sub models.ExternalSubtitle) string {
	for _, value := range []string{sub.Title, sub.EmbeddedTitle, filepath.Base(sub.Path), sub.Language} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// SubtitleCarryEmbeddedLabelV3 is the displayed label of an embedded track.
func SubtitleCarryEmbeddedLabelV3(track models.SubtitleTrack) string {
	for _, value := range []string{track.Title, track.EmbeddedTitle, track.Language} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// MatchSubtitleCarryTrackV3 finds the unique track on target that carries the
// wanted selection across a virtual release rotation. It reports the target's
// published combined ordinal, whether the match is exact (both tiebreaks agree)
// and whether a unique match exists. Ambiguity is a miss, never a guess: two
// tracks that agree on every hard gate and both tiebreaks cannot be told apart,
// so the caller must fail closed rather than serve whichever one sorts first.
//
// targetDownloaded is the target's live downloaded tail in repository order; it
// is only consulted for a downloaded selection, which is resolved by stable row
// id first and then by language + codec (never by title).
func MatchSubtitleCarryTrackV3(wanted SubtitleCarryKeyV3, target *models.MediaFile, targetDownloaded []SubtitleInventoryEntryV3) (publishedOrdinal int, exact bool, ok bool) {
	if target == nil {
		return 0, false, false
	}
	switch wanted.Source {
	case SubtitleSourceExternalV3, SubtitleSourceEmbeddedV3:
		return matchOwnedSubtitleCarryV3(wanted, target)
	case SubtitleSourceDownloadedV3:
		return matchDownloadedSubtitleCarryV3(wanted, target, targetDownloaded)
	default:
		return 0, false, false
	}
}

// subtitleCarryCandidateV3 is one target track reduced to the attributes the
// carry matches on.
type subtitleCarryCandidateV3 struct {
	published int
	language  string
	muxer     string
	codec     string
	forced    bool
	hi        bool
	title     string
}

func matchOwnedSubtitleCarryV3(wanted SubtitleCarryKeyV3, target *models.MediaFile) (int, bool, bool) {
	wantedLanguage := lang.Canonical(wanted.Language)
	wantedMuxer := strings.TrimSpace(wanted.MuxerClass)
	if wantedMuxer == "" {
		return 0, false, false
	}
	candidates := make([]subtitleCarryCandidateV3, 0, len(target.ExternalSubtitles)+len(target.SubtitleTracks))
	for index, sub := range target.ExternalSubtitles {
		published, mapped := SubtitleInventoryOwnPublishedIndexV3(target, index)
		if !mapped {
			continue
		}
		candidates = append(candidates, subtitleCarryCandidateV3{
			published: published,
			language:  lang.Canonical(sub.Language),
			muxer:     subtitleLayoutClass(sub.Format),
			codec:     normalizeCodecV3(sub.Format),
			forced:    sub.Forced,
			hi:        sub.HearingImpaired,
			title:     SubtitleCarryExternalLabelV3(sub),
		})
	}
	for index, track := range target.SubtitleTracks {
		published, mapped := SubtitleInventoryOwnPublishedIndexV3(target, len(target.ExternalSubtitles)+index)
		if !mapped {
			continue
		}
		candidates = append(candidates, subtitleCarryCandidateV3{
			published: published,
			language:  lang.Canonical(track.Language),
			muxer:     subtitleLayoutClass(track.Codec),
			codec:     normalizeCodecV3(track.Codec),
			forced:    track.Forced,
			hi:        track.HearingImpaired,
			title:     SubtitleCarryEmbeddedLabelV3(track),
		})
	}
	survivors := make([]subtitleCarryCandidateV3, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.muxer == "" || candidate.muxer != wantedMuxer {
			continue
		}
		if candidate.language != wantedLanguage {
			continue
		}
		if candidate.forced != wanted.Forced || candidate.hi != wanted.HearingImpaired {
			continue
		}
		survivors = append(survivors, candidate)
	}
	return resolveSubtitleCarrySurvivorsV3(wanted, survivors)
}

// resolveSubtitleCarrySurvivorsV3 applies the codec and title tiebreaks in that
// order. A unique survivor is a match even when a tiebreak differs (fuzzy); a
// tiebreak that still leaves several candidates is fail-closed.
func resolveSubtitleCarrySurvivorsV3(wanted SubtitleCarryKeyV3, survivors []subtitleCarryCandidateV3) (int, bool, bool) {
	wantedCodec := normalizeCodecV3(wanted.CodecNormalized)
	wantedTitle := wanted.Title
	exactWith := func(candidate subtitleCarryCandidateV3) bool {
		return candidate.codec == wantedCodec && subtitleCarryTitleEqualV3(candidate.title, wantedTitle)
	}
	switch len(survivors) {
	case 0:
		return 0, false, false
	case 1:
		return survivors[0].published, exactWith(survivors[0]), true
	}
	codecMatches := make([]subtitleCarryCandidateV3, 0, len(survivors))
	for _, candidate := range survivors {
		if candidate.codec == wantedCodec {
			codecMatches = append(codecMatches, candidate)
		}
	}
	switch len(codecMatches) {
	case 0:
		// Every surviving track has a different codec. Only a unique title can
		// still identify the selection, and that is a fuzzy carry.
	case 1:
		return codecMatches[0].published, subtitleCarryTitleEqualV3(codecMatches[0].title, wantedTitle), true
	default:
		survivors = codecMatches
	}
	titleMatches := make([]subtitleCarryCandidateV3, 0, len(survivors))
	for _, candidate := range survivors {
		if subtitleCarryTitleEqualV3(candidate.title, wantedTitle) {
			titleMatches = append(titleMatches, candidate)
		}
	}
	if len(titleMatches) == 1 {
		return titleMatches[0].published, titleMatches[0].codec == wantedCodec, true
	}
	return 0, false, false
}

// matchDownloadedSubtitleCarryV3 resolves a downloaded selection against the
// target's live tail. The stable row id is the identity; when the row was
// dropped or renumbered, only an exact language + codec match may carry it, and
// two such rows are ambiguous and fail closed. Title is never a tiebreak for a
// downloaded row (labels are provider-authored and collide).
func matchDownloadedSubtitleCarryV3(wanted SubtitleCarryKeyV3, target *models.MediaFile, targetDownloaded []SubtitleInventoryEntryV3) (int, bool, bool) {
	// Downloaded rows carry no forced disposition; a requested forced carry has
	// no representable identity here.
	if wanted.Forced {
		return 0, false, false
	}
	wantedLanguage := lang.Canonical(wanted.Language)
	wantedCodec := normalizeCodecV3(wanted.CodecNormalized)
	type downloadedCandidate struct {
		published int
		index     int
	}
	candidates := make([]downloadedCandidate, 0, len(targetDownloaded))
	for index, entry := range targetDownloaded {
		if entry.Source != "" && entry.Source != SubtitleSourceDownloadedV3 {
			continue
		}
		if entry.HearingImpaired != wanted.HearingImpaired {
			continue
		}
		candidates = append(candidates, downloadedCandidate{published: index, index: index})
	}
	if len(candidates) == 0 {
		return 0, false, false
	}
	base := len(BuildSubtitleInventoryV3(target, nil))
	if wanted.DownloadedRowID > 0 {
		for _, candidate := range candidates {
			entry := targetDownloaded[candidate.index]
			if entry.DownloadedSubtitleID == wanted.DownloadedRowID {
				return base + candidate.published, true, true
			}
		}
	}
	languageMatches := make([]downloadedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		entry := targetDownloaded[candidate.index]
		if lang.Canonical(entry.Language) != wantedLanguage {
			continue
		}
		if normalizeCodecV3(entry.Codec) != wantedCodec {
			continue
		}
		languageMatches = append(languageMatches, candidate)
	}
	if len(languageMatches) != 1 {
		// Zero: no equivalent. More than one: two rows share the language and
		// format, so the carry cannot prove which row the viewer picked.
		return 0, false, false
	}
	return base + languageMatches[0].published, false, true
}

// subtitleCarryTitleEqualV3 compares displayed labels for the title tiebreak.
// An empty label matches only an empty label.
func subtitleCarryTitleEqualV3(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// subtitleLayoutClass returns the deliverable class a codec extracts as, or ""
// for codecs with no sidecar representation. It deliberately mirrors the
// stream handler's output mapping (webvtt for text, ass-copy for ASS/SSA,
// .sup for PGS) and never the catch-all webvtt default, so an unrenderable
// codec is never class-matched onto a text track.
func subtitleLayoutClass(codec string) string {
	switch {
	case IsASS(codec):
		return "ass"
	case IsPGS(codec):
		return "sup"
	case isTextSubtitleV3(codec):
		return "webvtt"
	default:
		return ""
	}
}

// SubtitleLayoutClass is the deliverable class a codec extracts as ("sup",
// "ass", "webvtt"), or "" for a codec with no sidecar representation. It is the
// same narrowing MatchEmbeddedSubtitleTrack applies, exposed so a diagnosis can
// name the class it was looking for.
func SubtitleLayoutClass(codec string) string {
	return subtitleLayoutClass(codec)
}

// SubtitleLayoutClassInventory counts the live tracks that share codec's
// extraction class and lists the languages they declare (deduplicated, in live
// order, "und" for an undeclared one). A probe that found no match uses it to
// say whether the class is missing entirely or present in other languages —
// answers that look identical otherwise.
func SubtitleLayoutClassInventory(codec string, live []models.SubtitleTrack) (count int, languages []string) {
	class := subtitleLayoutClass(codec)
	if class == "" {
		return 0, nil
	}
	seen := make(map[string]struct{}, len(live))
	for _, track := range live {
		if subtitleLayoutClass(track.Codec) != class {
			continue
		}
		count++
		language := strings.TrimSpace(track.Language)
		if language == "" {
			language = "und"
		}
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}
		languages = append(languages, language)
	}
	return count, languages
}

// canonicalSubtitleContainerTrackID mirrors the catalog scanner's container
// track ID normalization so a live probe compares equal to plan-time evidence.
// FFprobe emits MP4 track IDs as hexadecimal strings; the canonical form is
// the decimal identifier, and absent IDs are never guessed.
func canonicalSubtitleContainerTrackID(raw string) string {
	raw = strings.TrimSpace(raw)
	base := 10
	if strings.HasPrefix(raw, "0x") || strings.HasPrefix(raw, "0X") {
		raw = raw[2:]
		base = 16
	}
	id, err := strconv.ParseUint(raw, base, 32)
	if err != nil || id == 0 {
		return ""
	}
	return strconv.FormatUint(id, 10)
}

// subtitleTagBoolFlag reports whether an ffprobe stream tag carries a truthy
// boolean flag (1/true/yes), matching how catalog scans derive flags that have
// no dedicated disposition field.
func subtitleTagBoolFlag(tags map[string]string, key string) bool {
	if tags == nil {
		return false
	}
	value := strings.ToLower(strings.TrimSpace(tags[key]))
	return value == "1" || value == "true" || value == "yes"
}
