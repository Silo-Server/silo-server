package playback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

// copySegmentPlan is a copy-video session's complete segment list, worked out
// from the source's keyframe index before FFmpeg starts (#1466). The session
// serves it as a VOD playlist instead of FFmpeg's growing one, so a player
// never sees a partial playlist: Safari's native HLS can fall back to one and
// stop mid-stream.
//
// FFmpeg's HLS muxer, run from the start of the file, cuts copied video at
// the planned keyframes (keyframes.PlanSegments), so its seg_NNNNN files are
// the planned segments. A seek restart would cut at different keyframes, so
// a planned session restarts from the beginning instead (see restart).
type copySegmentPlan struct {
	durations []float64
}

// loadKeyframeIndex reads a source's keyframe index, and
// loadVerifiedKeyframeIndex waits for its full check, stopping when ctx is
// done; loadKeyframeSource and loadVerifiedKeyframeSource do the same for a
// transcode node's relayed source. Tests replace them.
var (
	loadKeyframeIndex          = keyframes.Load
	loadVerifiedKeyframeIndex  = keyframes.LoadVerified
	loadKeyframeSource         = keyframes.LoadSource
	loadVerifiedKeyframeSource = keyframes.LoadSourceVerified
)

// loadCopyKeyframeIndex reads the keyframe index of opts' source: a local
// file by its path, a source relayed to a transcode node (nodesource) through
// its handle, cached under its identity rather than the job's loopback URL.
// A rebuilt session waits for the full check (see planCopySegments).
func loadCopyKeyframeIndex(ctx context.Context, opts TranscodeOpts) (keyframes.Index, error) {
	if h, ok := nodesource.Lookup(opts.InputPath); ok {
		source := keyframes.Source{
			Identity: h.Identity(),
			Size:     h.Size(),
			Context:  h.Context(),
			Open: func(ctx context.Context) (keyframes.ReaderAtCloser, error) {
				return h.ReaderAt(ctx)
			},
			// The full check reads every block header: streamed, it takes a
			// few requests and a deadline that grows with the source,
			// rather than a request per page within the page reader's two
			// minutes.
			OpenSequential: func(ctx context.Context) (keyframes.ReaderAtCloser, error) {
				return h.SequentialReaderAt(ctx)
			},
		}
		if opts.Reconstructed {
			return loadVerifiedKeyframeSource(ctx, source)
		}
		return loadKeyframeSource(ctx, source)
	}
	if opts.Reconstructed {
		return loadVerifiedKeyframeIndex(ctx, opts.InputPath)
	}
	return loadKeyframeIndex(opts.InputPath)
}

// planCopySegments returns the segment plan for a session, or nil when the
// session keeps FFmpeg's real playlist: the setting is off, the video isn't
// copied, the stream doesn't start at the beginning, or the source has no
// keyframe index.
//
// A first start whose index can't be read keeps FFmpeg's playlist. A rebuilt
// session can't: it may already have served the planned playlist (a clean
// shutdown removes the plan record with the output directory), and the plan
// follows from the verified index alone, so it waits for the full check, and
// a read that fails, runs out of time or budget, or stops with the rebuild's
// request returns that error rather than a different playlist. A later rebuild
// tries again; once the check has run, the index is cached.
func planCopySegments(ctx context.Context, opts TranscodeOpts) (*copySegmentPlan, error) {
	if !opts.KeyframePlaylist ||
		!strings.EqualFold(opts.TargetCodecVideo, "copy") ||
		opts.SeekSeconds > 0 || opts.StartSegmentNumber > 0 ||
		!CanGenerateSyntheticManifest(opts.TotalDuration, opts.SegmentDuration) {
		return nil, nil
	}
	idx, err := loadCopyKeyframeIndex(ctx, opts)
	if err != nil {
		if errors.Is(err, keyframes.ErrNoIndex) {
			return nil, nil
		}
		if opts.Reconstructed {
			return nil, fmt.Errorf("read keyframe index: %w", err)
		}
		log.Printf("playback: read keyframe index for %s: %v; using FFmpeg's playlist", opts.SessionID, err)
		return nil, nil
	}
	segmentSeconds := float64(opts.SegmentDuration)
	if segmentSeconds <= 0 {
		segmentSeconds = defaultSegmentDuration
	}
	starts := keyframes.PlanSegments(idx.Keyframes, segmentSeconds)
	if len(starts) == 0 || len(starts) > maxSyntheticManifestSegments {
		return nil, nil
	}
	// FFmpeg's last segment ends with the copied video, even when an audio
	// or subtitle track runs on: the container's duration can be longer.
	if idx.VideoEnd <= idx.Keyframes[len(idx.Keyframes)-1] {
		// The video's end is unknown, so the plan can't describe the last
		// segment.
		return nil, nil
	}
	durations := keyframes.SegmentDurations(starts, idx.VideoEnd)
	return &copySegmentPlan{durations: durations}, nil
}

// sourceIdentity names the revision of opts' source: SourceIdentity when the
// caller knows it, else nodesource.Identity of InputPath (a local file's path,
// size and modification time), or "" when the source can't be described.
func sourceIdentity(opts TranscodeOpts) string {
	if opts.SourceIdentity != "" {
		return opts.SourceIdentity
	}
	identity, err := nodesource.Identity(opts.InputPath)
	if err != nil {
		return ""
	}
	return identity
}

// copyPlanRecordFile keeps a session's planning decision in its output
// directory, which a session rebuilt from its recipe reuses, unless the
// session records it elsewhere (TranscodeOpts.CopyPlanRecordPath).
const copyPlanRecordFile = "keyframe-plan.json"

// CopyPlanRecordDirName is the directory under a transcode node's transcode
// directory that holds its sessions' keyframe-plan records
// (TranscodeOpts.CopyPlanRecordPath).
const CopyPlanRecordDirName = "keyframe-plans"

// copyPlanRecordPath is where opts' session records its planning decision.
func copyPlanRecordPath(opts TranscodeOpts) string {
	if opts.CopyPlanRecordPath != "" {
		return opts.CopyPlanRecordPath
	}
	return filepath.Join(opts.OutputDir, copyPlanRecordFile)
}

type copyPlanRecord struct {
	// SourceIdentity names the source revision the decision was made for
	// (sourceIdentity). A rebuild on another revision refuses the record.
	// Records written before it existed have none and are followed as before.
	SourceIdentity string    `json:"source_identity,omitempty"`
	Planned        bool      `json:"planned"`
	Durations      []float64 `json:"durations,omitempty"`
}

// ErrCopyPlanUnavailable is a rebuilt session whose keyframe plan could not
// be worked out now: the source's index could not be read, ran out of its
// time or budget, or its request ended (see planCopySegments). A later
// rebuild may succeed.
var ErrCopyPlanUnavailable = errors.New("the rebuilt session's keyframe plan is unavailable")

// ErrCopyPlanSourceChanged is a rebuilt session whose recorded plan belongs to
// another revision of its source: its player's playlist may not match the
// file any more, and no playlist this source gives can be promised to. It is
// terminal for the session: no later rebuild can succeed either.
var ErrCopyPlanSourceChanged = errors.New("the recorded keyframe plan belongs to another revision of the source")

// resolveCopyPlan returns the plan a session serves. The first start decides
// and records the decision; a rebuilt session follows the record, so it
// serves the playlist its player already has. Deciding afresh could differ:
// a file's index that was still being checked may be verified by then.
//
// By default the record lives in the output directory, which a clean
// shutdown removes, and a fresh start that finds one there follows it too. A
// transcode node keeps it apart from the output (CopyPlanRecordPath), so it
// survives an idle reap: there a fresh start always decides and replaces it,
// and only a rebuild follows it, without reading the source again. A rebuilt
// session without a record plans again from the verified index (see
// planCopySegments), which gives the same plan as the first start. If that
// start had fallen back to FFmpeg's playlist, the rebuilt session now plans:
// a planned session copies from the beginning, where FFmpeg cuts the same
// segments, so the player's earlier segments still match.
//
// The node's record is the transport's, which a replacement start shares
// with the live session it may replace. So a node session that decides
// writes a candidate record beside it, whose path resolveCopyPlan returns,
// and publishes it only once the start has succeeded
// (TranscodeSession.CommitCopyPlanRecord); a start that fails discards it,
// and the live session keeps the record it follows.
//
// An error is a rebuilt session that can't be given its playlist now (see
// planCopySegments) or ever (ErrCopyPlanSourceChanged); nothing is recorded.
func resolveCopyPlan(ctx context.Context, opts TranscodeOpts) (*copySegmentPlan, string, error) {
	if !opts.KeyframePlaylist || opts.OutputDir == "" && opts.CopyPlanRecordPath == "" {
		plan, err := planCopySegments(ctx, opts)
		return plan, "", err
	}
	identity := sourceIdentity(opts)
	path := copyPlanRecordPath(opts)
	followRecord := opts.Reconstructed || opts.CopyPlanRecordPath == ""
	if data, err := os.ReadFile(path); err == nil && followRecord {
		var rec copyPlanRecord
		if err := json.Unmarshal(data, &rec); err == nil {
			if rec.SourceIdentity != "" && identity != "" && rec.SourceIdentity != identity {
				return nil, "", ErrCopyPlanSourceChanged
			}
			if opts.CopyPlanRecordPath != "" {
				// A record that is followed is in use: keep the age sweep
				// (playback.SweepCopyPlanRecords) off it.
				now := time.Now()
				_ = os.Chtimes(path, now, now)
			}
			if !rec.Planned {
				return nil, "", nil
			}
			if plan := rec.plan(); plan != nil {
				return plan, "", nil
			}
		}
		log.Printf("playback: session %s has an unreadable keyframe plan record; using FFmpeg's playlist", opts.SessionID)
		return nil, "", nil
	}

	// Record the decision whatever the start's fate: StartTranscode launches
	// FFmpeg even if its caller has gone, and that session serves this
	// playlist.
	plan, err := planCopySegments(ctx, opts)
	if err != nil {
		// The rebuild's check failed or its request went away. Its outcome
		// says nothing about the file, so don't record it: StartTranscode
		// abandons this rebuild, and the next one plans.
		return nil, "", err
	}
	if plan == nil && opts.Reconstructed && ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	rec := copyPlanRecord{SourceIdentity: identity, Planned: plan != nil}
	if plan != nil {
		rec.Durations = plan.durations
	}
	candidate, err := writeCopyPlanRecord(path, rec, opts.CopyPlanRecordPath == "")
	if err != nil {
		// Without a record a rebuilt session can't promise the same
		// playlist, so don't serve a planned one.
		log.Printf("playback: record keyframe plan for %s: %v; using FFmpeg's playlist", opts.SessionID, err)
		return nil, "", nil
	}
	return plan, candidate, nil
}

// copyPlanCandidateInfix marks a candidate record, written beside the
// record it would replace (see resolveCopyPlan).
const copyPlanCandidateInfix = ".candidate-"

// writeCopyPlanRecord writes rec to a new file beside path. With publish it
// renames the file over path and returns ""; without, it returns the file,
// a candidate the caller publishes or removes.
func writeCopyPlanRecord(path string, rec copyPlanRecord, publish bool) (string, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	infix := ".tmp-"
	if !publish {
		infix = copyPlanCandidateInfix
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+infix)
	if err != nil {
		return "", err
	}
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil && publish {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if publish {
		return "", nil
	}
	return tmp.Name(), nil
}

// SweepCopyPlanRecords removes the keyframe-plan records in dir, a transcode
// node's CopyPlanRecordPath directory, that no session has written or
// followed for maxAge, except those keep names. It returns how many it
// removed. Records are small, so the sweep only bounds their number.
func SweepCopyPlanRecords(dir string, keep map[string]struct{}, maxAge time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, entry := range entries {
		if _, ok := keep[entry.Name()]; ok {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, entry.Name())) == nil {
			removed++
		}
	}
	return removed
}

// plan rebuilds a recorded plan, or nil if the record is inconsistent.
func (r copyPlanRecord) plan() *copySegmentPlan {
	if len(r.Durations) == 0 {
		return nil
	}
	for _, d := range r.Durations {
		if !(d > 0) || math.IsInf(d, 0) {
			return nil
		}
	}
	return &copySegmentPlan{durations: r.Durations}
}

// plannedSegmentWait is how long a request for a segment FFmpeg hasn't
// reached yet waits. A planned stream doesn't seek-restart forward, so a far
// seek waits for FFmpeg to copy up to it; stream copy runs many times faster
// than playback, and a missing segment can end Safari's native playback.
const plannedSegmentWait = 30 * time.Second

// plannedSegmentRecoveryDecision handles a missing segment in a planned
// session. One FFmpeg hasn't reached is waited for, and recorded as the
// player's position so the throttler doesn't hold FFmpeg back from it. One
// that is gone (pruned behind the player) or will never come (FFmpeg stopped)
// regenerates the stream from the beginning.
func (s *TranscodeSession) plannedSegmentRecoveryDecision(segNum int, decision SegmentRecoveryDecision) SegmentRecoveryDecision {
	progress := decision.Progress
	switch {
	case progress.Restarting:
		decision.Wait = true
		decision.WaitTimeout = activeSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = segmentReasonRestarting
	case progress.Running && segNum > progress.ProducedHead:
		s.mu.Lock()
		if segNum > s.lastRequestedSegment {
			s.lastRequestedSegment = segNum
		}
		s.mu.Unlock()
		decision.Wait = true
		decision.WaitTimeout = plannedSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = "planned_segment_ahead"
	default:
		decision.Wait = false
		decision.RestartOnTimeout = true
		decision.Reason = "planned_segment_regenerate"
	}
	return decision
}

// manifest returns the plan as a complete VOD playlist for the session's
// segment files, in the same shape as GenerateFullManifest.
func (p *copySegmentPlan) manifest(opts TranscodeOpts, segPrefix, rawQuery string) []byte {
	queryDefinition, suffix, queryVersion := syntheticManifestQuery(len(p.durations), rawQuery)
	segExt := hlsSegmentExtension(opts)
	fmp4 := videoUsesFMP4(opts)
	hlsVersion := 3
	if fmp4 {
		hlsVersion = 7
	}
	hlsVersion = max(hlsVersion, queryVersion)

	// EXT-X-TARGETDURATION must cover every segment's rounded duration. A
	// keyframe gap longer than the segment length makes a longer segment.
	target := 1
	for _, d := range p.durations {
		target = max(target, int(math.Round(d)))
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	fmt.Fprintf(&buf, "#EXT-X-VERSION:%d\n", hlsVersion)
	buf.WriteString(queryDefinition)
	fmt.Fprintf(&buf, "#EXT-X-TARGETDURATION:%d\n", target)
	buf.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	buf.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	if fmp4 {
		fmt.Fprintf(&buf, "#EXT-X-MAP:URI=\"%sinit.mp4%s\"\n", segPrefix, suffix)
	}
	for i, d := range p.durations {
		fmt.Fprintf(&buf, "#EXTINF:%.6f,\n", d)
		fmt.Fprintf(&buf, "%sseg_%05d%s%s\n", segPrefix, i, segExt, suffix)
	}
	buf.WriteString("#EXT-X-ENDLIST\n")
	return buf.Bytes()
}
