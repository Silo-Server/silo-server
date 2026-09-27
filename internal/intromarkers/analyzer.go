package intromarkers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
)

type Analyzer struct {
	repo               introRepository
	extractor          fingerprintExtractor
	refiner            boundaryRefiner
	chromaprintRefiner chromaprintStartRefiner
	config             Config
	logger             *slog.Logger
	// tailSampler runs credits tail passes. Nil leaves credits to chapters
	// and audio.
	tailSampler creditsTailSampler
	// movieSampler runs movie tail passes. Nil leaves movie credits to
	// chapters.
	movieSampler movieTailSampler
	// hardware is where the tail samplers decode keyframes; SetHardwareDecode
	// updates it. Nil when the samplers are replaced.
	hardware *hardwareDecoder
	// movieBudget bounds how long a scheduled run starts new movies; zero
	// means movieCreditsRunBudget. now, when set, replaces time.Now for it.
	movieBudget time.Duration
	now         func() time.Time
	// moviePageSize is how many movies a scheduled run lists at a time;
	// zero means movieCandidatePageSize.
	moviePageSize int
	// node names this server in recorded silence refinement failures, which
	// only defer retries on the server that recorded them.
	node string
	// slotsMu guards workers and the ffmpegSlots pointer, which SetWorkers
	// sets when the markers.detection_workers setting changes.
	slotsMu sync.Mutex
	// workers is how many season groups, and silence backfill shares, run at
	// once. Zero falls back to config.MaxParallelFFmpeg.
	workers int
	// ffmpegSlots bounds the ffmpeg processes this analyzer runs at once across
	// the nightly run and its season workers, with a slot reserved for
	// analysis started from playback (see WithPlaybackPriority). Nil means no
	// shared bound.
	ffmpegSlots *mediasample.Limiter
	// lookupSlots bounds concurrent fingerprint cache reads across every group
	// being analyzed; see fingerprintLookupSlots.
	lookupOnce  sync.Once
	lookupSlots chan struct{}
	// tailWarnOnce logs once that ffmpeg cannot run the credits tail pass.
	tailWarnOnce sync.Once
	// movieWarnOnce logs once that ffmpeg cannot run the movie tail pass.
	movieWarnOnce sync.Once
}

// maxConcurrentFingerprintLookups caps the database connections fingerprint
// cache reads hold at once. Each candidate in a season looks up its cache
// entry concurrently, and several seasons run at once, so without a cap the
// nightly run could take the whole pool and starve API requests.
const maxConcurrentFingerprintLookups = 4

// WithPlaybackPriority marks analysis a viewer is waiting on, letting it use
// the ffmpeg slot reserved for playback and run ffmpeg at normal process
// priority. Background callers, such as admin refreshes, must not use it or
// they would queue ahead of playback.
func WithPlaybackPriority(ctx context.Context) context.Context {
	return mediasample.WithInteractive(ctx)
}

type introRepository interface {
	CountEnabledLibraries(ctx context.Context) (int, error)
	ListEligibleCandidates(ctx context.Context) ([]Candidate, error)
	ListCandidatesForEpisode(ctx context.Context, episodeID string) ([]Candidate, error)
	ListCandidatesForGroup(ctx context.Context, mediaFolderID int, seasonID, analysisGroupKey string) ([]Candidate, error)
	ListChapterSilenceBackfillCandidates(ctx context.Context, limit int, cfg Config, node string) ([]Candidate, error)
	ListMovieCandidates(ctx context.Context, node string, after *movieCandidateCursor, limit int) ([]Candidate, *movieCandidateCursor, error)
	ListMovieCandidatesForItem(ctx context.Context, contentID string) ([]Candidate, error)
	ListMovieCandidatesForFile(ctx context.Context, fileID int) ([]Candidate, error)
	LoadSilenceRefinementAttempt(ctx context.Context, fileID int) (*SilenceRefinementAttempt, error)
	UpsertSilenceRefinementAttempt(ctx context.Context, attempt SilenceRefinementAttempt) error
	PatchMarker(ctx context.Context, patch MarkerPatch) (bool, error)
	LoadSeasonState(ctx context.Context, state SeasonState, analysisHash string) (*SeasonState, error)
	UpsertSeasonState(ctx context.Context, state SeasonState, analysisHash string) error
	LoadFingerprint(ctx context.Context, candidate Candidate, cfg Config) (*Fingerprint, error)
	UpsertFingerprint(ctx context.Context, fp Fingerprint) error
	LoadArtifacts(ctx context.Context, fileIDs []int, key ArtifactKey) (map[int]Artifact, error)
	UpsertArtifact(ctx context.Context, a Artifact) error
	RecordArtifactFailure(ctx context.Context, failure ArtifactFailure) error
}

type fingerprintExtractor interface {
	Preflight(ctx context.Context) error
	// Extract fingerprints a candidate's opening audio for intros.
	Extract(ctx context.Context, candidate Candidate) (Fingerprint, bool, error)
	// ExtractCredits fingerprints a candidate's tail audio for credits.
	ExtractCredits(ctx context.Context, candidate Candidate) (Fingerprint, bool, error)
}

func NewAnalyzer(repo *Repository, config Config, logger *slog.Logger) *Analyzer {
	config = config.normalized()
	if logger == nil {
		logger = slog.Default()
	}
	node, _ := os.Hostname()
	if node == "" {
		node = "silo"
	}
	extractor := NewChromaprintExtractor(config)
	extractor.logger = logger
	return &Analyzer{
		repo:               repo,
		extractor:          extractor,
		tailSampler:        extractor,
		movieSampler:       extractor,
		hardware:           extractor.hardware,
		refiner:            NewSilenceBoundaryRefiner(config),
		chromaprintRefiner: NewDialogueBoundaryRefiner(config),
		config:             config,
		logger:             logger,
		node:               node,
		workers:            config.MaxParallelFFmpeg,
		ffmpegSlots:        mediasample.NewLimiter(config.MaxParallelFFmpeg),
	}
}

// SetHardwareDecode applies the playback.hw_accel and playback.hw_device
// settings to the next credits tail pass without a restart.
func (a *Analyzer) SetHardwareDecode(accel, device string) {
	if a.hardware != nil {
		a.hardware.set(accel, device)
	}
}

// SetWorkers applies the markers.detection_workers setting: how many season
// groups are analyzed at once and how many ffmpeg processes they may run. Less
// than one means DefaultDetectionWorkers. The change applies to new work
// without waiting for a run to finish; after a decrease, extractions already
// running finish before new ones start.
func (a *Analyzer) SetWorkers(n int) {
	if n <= 0 {
		n = DefaultDetectionWorkers
	}
	a.slotsMu.Lock()
	defer a.slotsMu.Unlock()
	if a.workers == n && a.ffmpegSlots != nil {
		return
	}
	a.workers = n
	if a.ffmpegSlots == nil {
		a.ffmpegSlots = mediasample.NewLimiter(n)
		return
	}
	a.ffmpegSlots.Resize(n)
}

// nodeName names this server in recorded analysis failures.
func (a *Analyzer) nodeName() string {
	if a.node != "" {
		return a.node
	}
	return "silo"
}

// workerCount is the current number of season workers.
func (a *Analyzer) workerCount() int {
	a.slotsMu.Lock()
	defer a.slotsMu.Unlock()
	if a.workers > 0 {
		return a.workers
	}
	return max(1, a.config.normalized().MaxParallelFFmpeg)
}

func (a *Analyzer) sharedSlots() *mediasample.Limiter {
	a.slotsMu.Lock()
	defer a.slotsMu.Unlock()
	return a.ffmpegSlots
}

// acquireFFmpeg waits for an ffmpeg slot and returns its release. Interactive
// analysis may also take the reserved slot, so it never waits behind the
// nightly queue for more than one extraction.
func (a *Analyzer) acquireFFmpeg(ctx context.Context) (func(), error) {
	slots := a.sharedSlots()
	if slots == nil {
		return func() {}, ctx.Err()
	}
	return slots.Acquire(ctx)
}

// fingerprintLookupSlots returns the analyzer-wide bound on concurrent
// fingerprint cache reads, created on first use so analyzers built without
// NewAnalyzer share one too.
func (a *Analyzer) fingerprintLookupSlots() chan struct{} {
	a.lookupOnce.Do(func() {
		a.lookupSlots = make(chan struct{}, maxConcurrentFingerprintLookups)
	})
	return a.lookupSlots
}

// fingerprintLookup is what the cache holds for a candidate's fingerprint.
type fingerprintLookup int

const (
	// fingerprintMissing means the fingerprint has to be extracted.
	fingerprintMissing fingerprintLookup = iota
	// fingerprintCached means a cached fingerprint was returned.
	fingerprintCached
	// fingerprintUnusable means the file has no audio to fingerprint.
	fingerprintUnusable
	// fingerprintDeferred means this server's last extraction failed and is
	// backing off.
	fingerprintDeferred
)

// withLookupSlot runs a fingerprint cache read within the lookup bound. The
// slot covers only the read, not the wait for ffmpeg that may follow a miss.
func (a *Analyzer) withLookupSlot(ctx context.Context, read func() error) error {
	slots := a.fingerprintLookupSlots()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-slots }()
	return read()
}

// fingerprintLookupFunc returns how ensureFingerprints reads a candidate's
// cached intro fingerprint.
func (a *Analyzer) fingerprintLookupFunc() func(context.Context, Candidate) (*Fingerprint, fingerprintLookup, error) {
	return func(ctx context.Context, candidate Candidate) (*Fingerprint, fingerprintLookup, error) {
		var fp *Fingerprint
		err := a.withLookupSlot(ctx, func() error {
			var err error
			fp, err = a.repo.LoadFingerprint(ctx, candidate, a.config)
			return err
		})
		if err != nil || fp == nil {
			return nil, fingerprintMissing, err
		}
		return fp, fingerprintCached, nil
	}
}

// ffmpegAcquirer returns how one analysis waits for an ffmpeg slot.
// Analyzers built without NewAnalyzer have no shared limiter; each analysis
// then bounds its own ffmpeg processes.
func (a *Analyzer) ffmpegAcquirer() func(context.Context) (func(), error) {
	if a.sharedSlots() == nil {
		local := &Analyzer{ffmpegSlots: mediasample.NewLimiter(a.workerCount())}
		return local.acquireFFmpeg
	}
	return a.acquireFFmpeg
}

type ProgressFunc func(percent float64, message string)

// Preflight reports what this server's ffmpeg lacks for Chromaprint
// comparison. Without it, Run still reads chapters and refines them with
// silence, but compares no season groups. The error matches
// mediasample.ErrUnsupported only when ffmpeg's capability listing succeeded
// and lacks Chromaprint.
func (a *Analyzer) Preflight(ctx context.Context) error {
	return a.extractor.Preflight(ctx)
}

// Run analyzes every library with marker detection enabled for the kinds
// selected: episodes for intros, credits, or both, then movies for credits
// within the movie budget. Movies are skipped when credits are not
// selected.
func (a *Analyzer) Run(ctx context.Context, kinds EpisodeMarkerKinds, progress ProgressFunc) (RunSummary, error) {
	return a.run(ctx, kinds, progress, runPasses{episodes: true, movies: true})
}

// RunEpisodes analyzes the episodes of every library with marker detection
// enabled for the kinds selected, as Run does, and leaves movies alone. A
// server without Chromaprint runs it outside the cluster lock, then
// RunMovies under it, so the movie pass stays on one server.
func (a *Analyzer) RunEpisodes(ctx context.Context, kinds EpisodeMarkerKinds, progress ProgressFunc) (RunSummary, error) {
	return a.run(ctx, kinds, progress, runPasses{episodes: true})
}

// RunMovies analyzes the movies of every library with marker detection
// enabled for credits within the movie budget, as Run does after the
// episodes, and leaves episodes alone. It does nothing when kinds leaves
// credits out.
func (a *Analyzer) RunMovies(ctx context.Context, kinds EpisodeMarkerKinds, progress ProgressFunc) (RunSummary, error) {
	return a.run(ctx, kinds, progress, runPasses{movies: true})
}

// runPasses selects the passes of a scheduled run.
type runPasses struct {
	episodes, movies bool
}

func (a *Analyzer) run(ctx context.Context, kinds EpisodeMarkerKinds, progress ProgressFunc, passes runPasses) (RunSummary, error) {
	report := func(percent float64, message string) {
		if progress != nil {
			progress(percent, message)
		}
	}

	summary := RunSummary{}
	// Movies are checked for credits only.
	movies := passes.movies && kinds.Credits
	if !passes.episodes && !movies {
		report(100, "Credits detection is turned off")
		return summary, nil
	}
	libraries, err := a.repo.CountEnabledLibraries(ctx)
	if err != nil {
		return summary, err
	}
	summary.LibrariesScanned = libraries
	if libraries == 0 {
		report(100, "No libraries with marker detection enabled")
		return summary, nil
	}
	if !kinds.Any() {
		report(100, "Intro and credits detection are turned off")
		return summary, nil
	}

	if !movies {
		episodeSummary, err := a.runEpisodes(ctx, kinds, report)
		mergeRunSummary(&summary, episodeSummary)
		if err != nil {
			return summary, err
		}
		if passes.movies {
			report(100, "Intro detection completed; credits detection is turned off")
		}
		return summary, nil
	}

	// With both passes, episodes take the first 85 percent of the progress
	// bar and movies the rest.
	moviesFrom := 0.0
	if passes.episodes {
		moviesFrom = 85
		episodeSummary, err := a.runEpisodes(ctx, kinds, func(percent float64, message string) {
			report(percent*moviesFrom/100, message)
		})
		mergeRunSummary(&summary, episodeSummary)
		if err != nil {
			return summary, err
		}
	}

	report(moviesFrom, "Checking movies for credits")
	movieSummary, err := a.runMovies(ctx, func(done int, budgetUsed float64) {
		report(moviesFrom+budgetUsed*(100-moviesFrom), fmt.Sprintf("Checked %d movies for credits", done))
	})
	mergeRunSummary(&summary, movieSummary)
	if err != nil {
		return summary, err
	}
	if summary.MovieBudgetExhausted {
		report(100, "Marker detection completed; remaining movies wait for the next run")
		return summary, nil
	}
	report(100, "Marker detection completed")
	return summary, nil
}

// runEpisodes analyzes the episodes of every enabled library for kinds,
// reporting progress from 0 to 100.
func (a *Analyzer) runEpisodes(ctx context.Context, kinds EpisodeMarkerKinds, report ProgressFunc) (RunSummary, error) {
	summary := RunSummary{}
	candidates, err := a.repo.ListEligibleCandidates(ctx)
	if err != nil {
		return summary, err
	}
	summary.FilesConsidered = len(candidates)
	if len(candidates) == 0 {
		report(100, "No eligible episode files")
		return summary, nil
	}

	report(10, fmt.Sprintf("Checking embedded chapters for %d files", len(candidates)))
	if kinds.Intro {
		_, chapterSummary := a.processChapterCandidates(ctx, candidates, chapterProcessingOptions{
			allowEpisodeCopy: true,
			progress: func(i, total int) {
				if i%25 == 0 {
					report(10+float64(i)/float64(total)*20, fmt.Sprintf("Checked %d/%d files for chapter markers", i+1, total))
				}
			},
		})
		mergeRunSummary(&summary, chapterSummary)
		if err := ctx.Err(); err != nil {
			return summary, err
		}
	}
	if kinds.Credits {
		mergeRunSummary(&summary, a.processCreditsChapters(ctx, candidates))
		if err := ctx.Err(); err != nil {
			return summary, err
		}
	}

	// Each kind compares the files whose marker of that kind local analysis
	// may write, so an online intro does not keep a file out of the credits
	// comparison.
	creditsTail := kinds.Credits && a.creditsTailReady(ctx)
	var jobs []groupJob
	for _, kind := range kinds.markerKinds() {
		for _, group := range groupCandidates(ownCandidates(candidates, kind), minimumGroupEpisodes(kind, creditsTail)) {
			jobs = append(jobs, groupJob{kind: kind, group: group})
		}
	}
	countGroupJobs(&summary, jobs)
	if len(jobs) == 0 {
		backfillSummary, err := a.runSilenceBackfill(ctx, kinds)
		mergeRunSummary(&summary, backfillSummary)
		if err != nil {
			return summary, err
		}
		report(100, "No season groups eligible for Chromaprint")
		return summary, nil
	}

	report(35, "Checking FFmpeg Chromaprint support")
	if err := a.extractor.Preflight(ctx); err != nil {
		summary.ChromaprintSupported = false
		summary.ChromaprintSupportMessage = err.Error()
		backfillSummary, backfillErr := a.runSilenceBackfill(ctx, kinds)
		mergeRunSummary(&summary, backfillSummary)
		if backfillErr != nil {
			return summary, backfillErr
		}
		report(100, "Chromaprint unsupported; chapter detection completed")
		return summary, nil
	}
	summary.ChromaprintSupported = true

	groupSummary := a.analyzeGroups(ctx, jobs, analyzeGroupOptions{persistState: true, creditsTail: creditsTail}, func(done int) {
		report(40+float64(done)/float64(len(jobs))*55, fmt.Sprintf("Analyzed season group %d/%d", done, len(jobs)))
	})
	mergeRunSummary(&summary, groupSummary)
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	backfillSummary, err := a.runSilenceBackfill(ctx, kinds)
	mergeRunSummary(&summary, backfillSummary)
	if err != nil {
		return summary, err
	}
	report(100, "Episode marker detection completed")
	return summary, nil
}

// EpisodeMarkerKinds selects the marker kinds an episode analysis looks for.
type EpisodeMarkerKinds struct {
	Intro   bool
	Credits bool
}

// Any reports whether k selects at least one kind.
func (k EpisodeMarkerKinds) Any() bool { return k.Intro || k.Credits }

// And returns the kinds both k and other select.
func (k EpisodeMarkerKinds) And(other EpisodeMarkerKinds) EpisodeMarkerKinds {
	return EpisodeMarkerKinds{Intro: k.Intro && other.Intro, Credits: k.Credits && other.Credits}
}

// Or returns the kinds either k or other selects.
func (k EpisodeMarkerKinds) Or(other EpisodeMarkerKinds) EpisodeMarkerKinds {
	return EpisodeMarkerKinds{Intro: k.Intro || other.Intro, Credits: k.Credits || other.Credits}
}

// Without returns the kinds k selects that other does not.
func (k EpisodeMarkerKinds) Without(other EpisodeMarkerKinds) EpisodeMarkerKinds {
	return EpisodeMarkerKinds{Intro: k.Intro && !other.Intro, Credits: k.Credits && !other.Credits}
}

// markerKinds lists the kinds k selects, intro first.
func (k EpisodeMarkerKinds) markerKinds() []markerKind {
	var out []markerKind
	if k.Intro {
		out = append(out, kindIntro)
	}
	if k.Credits {
		out = append(out, kindCredits)
	}
	return out
}

// allMarkerKinds selects every kind local analysis finds.
var allMarkerKinds = EpisodeMarkerKinds{Intro: true, Credits: true}

// SettingsReader reads server settings.
type SettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// snapshotSettingsReader reads several settings in one snapshot; keys without
// a value are absent from the map. *catalog.EncryptedSettingsRepo implements
// it.
type snapshotSettingsReader interface {
	GetMany(ctx context.Context, keys ...string) (map[string]string, error)
}

// EnabledMarkerKinds reads markers.detect_intros and markers.detect_credits:
// the kinds local detection finds server-wide. A nil reader enables both.
// The settings are read on every call, so a change applies to the next
// analysis without a restart. Both are read in one snapshot, so a save that
// changes them together is never seen half-applied.
func EnabledMarkerKinds(ctx context.Context, settings SettingsReader) (EpisodeMarkerKinds, error) {
	if settings == nil {
		return allMarkerKinds, nil
	}
	// Separate reads could straddle a save that changes both settings
	// together, so a store without snapshot reads is an error.
	snapshot, ok := settings.(snapshotSettingsReader)
	if !ok {
		return EpisodeMarkerKinds{}, fmt.Errorf("loading %s and %s: settings store does not support snapshot reads", markers.SettingDetectIntros, markers.SettingDetectCredits)
	}
	values, err := snapshot.GetMany(ctx, markers.SettingDetectIntros, markers.SettingDetectCredits)
	if err != nil {
		return EpisodeMarkerKinds{}, fmt.Errorf("loading %s and %s: %w", markers.SettingDetectIntros, markers.SettingDetectCredits, err)
	}
	return EpisodeMarkerKinds{
		Intro:   markers.DetectionToggleEnabled(values[markers.SettingDetectIntros]),
		Credits: markers.DetectionToggleEnabled(values[markers.SettingDetectCredits]),
	}, nil
}

// AnalyzeEpisode analyzes the season groups of an episode's files for every
// marker kind, as AnalyzeEpisodeKinds does. It is the all-kinds shorthand.
func (a *Analyzer) AnalyzeEpisode(ctx context.Context, episodeID string) (RunSummary, error) {
	return a.AnalyzeEpisodeKinds(ctx, episodeID, allMarkerKinds)
}

// AnalyzeEpisodeKinds analyzes the season groups of an episode's files for
// the kinds selected, comparing each group again even when its stored
// analysis still stands. Admin refresh and re-detection use it.
func (a *Analyzer) AnalyzeEpisodeKinds(ctx context.Context, episodeID string, kinds EpisodeMarkerKinds) (RunSummary, error) {
	return a.analyzeEpisode(ctx, episodeID, kinds, true)
}

// AnalyzeEpisodeForPlayback analyzes only the marker kinds a played file
// lacks. Intro groups are compared again as in AnalyzeEpisode. A credits
// group whose stored analysis still stands is skipped: most episodes have no
// credits local analysis can find, and playback would otherwise repeat the
// season comparison on every start.
func (a *Analyzer) AnalyzeEpisodeForPlayback(ctx context.Context, episodeID string, kinds EpisodeMarkerKinds) (RunSummary, error) {
	return a.analyzeEpisode(ctx, episodeID, kinds, false)
}

func (a *Analyzer) analyzeEpisode(ctx context.Context, episodeID string, kinds EpisodeMarkerKinds, forceCredits bool) (RunSummary, error) {
	summary := RunSummary{}
	if !kinds.Any() {
		return summary, nil
	}
	candidates, err := a.repo.ListCandidatesForEpisode(ctx, episodeID)
	if err != nil {
		return summary, err
	}
	summary.FilesConsidered = len(candidates)
	if len(candidates) == 0 {
		return summary, nil
	}

	var jobKinds []markerKind
	if kinds.Intro {
		_, chapterSummary := a.processChapterCandidates(ctx, candidates, chapterProcessingOptions{
			forceExistingScanner: true,
			allowEpisodeCopy:     true,
		})
		mergeRunSummary(&summary, chapterSummary)
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		jobKinds = append(jobKinds, kindIntro)
	}
	if kinds.Credits {
		mergeRunSummary(&summary, a.processCreditsChapters(ctx, candidates))
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		jobKinds = append(jobKinds, kindCredits)
	}

	creditsTail := kinds.Credits && a.creditsTailReady(ctx)
	var jobs []groupJob
	seasonCandidates := map[string][]Candidate{}
	for _, kind := range jobKinds {
		remaining := ownCandidates(candidates, kind)
		if len(remaining) == 0 {
			continue
		}
		groups, err := a.episodeGroups(ctx, kind, remaining, seasonCandidates, minimumGroupEpisodes(kind, creditsTail))
		if err != nil {
			return summary, err
		}
		targetFileIDs := candidateFileIDs(remaining)
		for _, group := range groups {
			jobs = append(jobs, groupJob{kind: kind, group: group, patchFileIDs: targetFileIDs})
		}
	}
	if len(jobs) == 0 {
		return summary, nil
	}

	if err := a.extractor.Preflight(ctx); err != nil {
		summary.ChromaprintSupported = false
		summary.ChromaprintSupportMessage = err.Error()
		return summary, nil
	}
	summary.ChromaprintSupported = true

	countGroupJobs(&summary, jobs)
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		groupSummary, err := a.analyzeJob(ctx, job, analyzeGroupOptions{
			force:        job.kind == kindIntro || forceCredits,
			patchFileIDs: job.patchFileIDs,
			creditsTail:  creditsTail,
		})
		mergeRunSummary(&summary, groupSummary)
		if err != nil {
			a.logger.WarnContext(ctx, "marker episode group analysis failed",
				"kind", job.kind.String(),
				"episode_id", episodeID,
				"season_id", job.group.SeasonID,
				"media_folder_id", job.group.MediaFolderID,
				"group_key", job.group.AnalysisGroupKey,
				"error", err)
		}
	}

	return summary, nil
}

// episodeGroups returns the season groups an episode's remaining files of
// kind belong to, each holding every file of the group whose marker of kind
// local analysis may write, so a file's result and confidence do not depend
// on how its analysis started. Groups with fewer than minEpisodes episodes
// are left out. seasonCandidates caches each group's files by key across
// kinds.
func (a *Analyzer) episodeGroups(ctx context.Context, kind markerKind, remaining []Candidate, seasonCandidates map[string][]Candidate, minEpisodes int) ([]candidateGroup, error) {
	groupsByKey := map[string]candidateGroup{}
	for _, candidate := range remaining {
		key := fmt.Sprintf("%d:%s:%s", candidate.MediaFolderID, candidate.SeasonID, candidate.AnalysisGroupKey())
		if _, ok := groupsByKey[key]; ok {
			continue
		}
		groupCandidates, loaded := seasonCandidates[key]
		if !loaded {
			var err error
			groupCandidates, err = a.repo.ListCandidatesForGroup(ctx, candidate.MediaFolderID, candidate.SeasonID, candidate.AnalysisGroupKey())
			if err != nil {
				return nil, err
			}
			seasonCandidates[key] = groupCandidates
		}
		groupCandidates = ownCandidates(groupCandidates, kind)
		if distinctEpisodeCount(groupCandidates) < minEpisodes {
			continue
		}
		groupsByKey[key] = candidateGroup{
			SeasonID:         candidate.SeasonID,
			MediaFolderID:    candidate.MediaFolderID,
			AnalysisGroupKey: candidate.AnalysisGroupKey(),
			Candidates:       groupCandidates,
		}
	}
	groups := make([]candidateGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		groups = append(groups, group)
	}
	sortCandidateGroups(groups)
	return groups, nil
}

type chapterProcessingOptions struct {
	forceExistingScanner bool
	allowEpisodeCopy     bool
	deadline             time.Time
	progress             func(index, total int)
}

type chapterSourceMarker struct {
	candidate Candidate
	segment   Segment
}

const episodeVersionCopyDurationToleranceSeconds = 3.0

func (a *Analyzer) processChapterCandidates(ctx context.Context, candidates []Candidate, opts chapterProcessingOptions) ([]Candidate, RunSummary) {
	summary := RunSummary{}
	remaining := make([]Candidate, 0, len(candidates))
	directByEpisode := map[string]chapterSourceMarker{}
	directFileIDs := map[int]struct{}{}

	for i, candidate := range candidates {
		if opts.progress != nil {
			opts.progress(i, len(candidates))
		}
		if err := ctx.Err(); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
			return remaining, summary
		}
		if !opts.deadline.IsZero() && time.Now().After(opts.deadline) {
			return remaining, summary
		}
		if candidate.hasHigherPriority(kindIntro, models.MarkerSourceScanner) {
			continue
		}

		hasIntro := candidate.marker(kindIntro).present()
		effectiveSource := candidate.effectiveSource(kindIntro)
		if hasIntro && effectiveSource != "" && effectiveSource != models.MarkerSourceScanner {
			continue
		}
		if hasIntro && !opts.forceExistingScanner {
			continue
		}

		segment, ok := DetectChapterIntro(candidate.Chapters)
		if !ok {
			remaining = append(remaining, candidate)
			continue
		}

		segment, refined := a.refineChapterSegment(ctx, candidate, segment, opts.deadline, &summary)
		if !refined {
			// The run's time budget ran out while waiting for an ffmpeg slot.
			remaining = append(remaining, candidate)
			continue
		}
		applied, patchErr := a.repo.PatchMarker(ctx, MarkerPatch{
			Kind:         kindIntro,
			ExpectedFile: candidate.expectedFile(),
			FileID:       candidate.FileID,
			Start:        segment.Start,
			End:          segment.End,
			Source:       models.MarkerSourceScanner,
			Confidence:   segment.Confidence,
			Algorithm:    segment.Algorithm,
			DetectedAt:   time.Now().UTC(),
		})
		if patchErr != nil {
			summary.Errors = append(summary.Errors, patchErr.Error())
			a.logger.WarnContext(ctx, "intro marker chapter patch failed", "file_id", candidate.FileID, "error", patchErr)
			remaining = append(remaining, candidate)
			continue
		}
		if applied {
			summary.ChapterMarkersWritten++
		}
		directFileIDs[candidate.FileID] = struct{}{}
		setBestChapterSource(directByEpisode, candidate, segment)
	}

	if !opts.allowEpisodeCopy || len(directByEpisode) == 0 || len(remaining) == 0 {
		return remaining, summary
	}

	unresolved := remaining[:0]
	for _, candidate := range remaining {
		if err := ctx.Err(); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
			unresolved = append(unresolved, candidate)
			continue
		}
		if !opts.deadline.IsZero() && time.Now().After(opts.deadline) {
			unresolved = append(unresolved, candidate)
			continue
		}
		if _, ok := directFileIDs[candidate.FileID]; ok {
			continue
		}
		source, ok := directByEpisode[candidate.EpisodeID]
		if !ok {
			unresolved = append(unresolved, candidate)
			continue
		}
		if candidate.hasHigherPriority(kindIntro, models.MarkerSourceScanner) || !compatibleEpisodeVersionDuration(source.candidate, candidate) {
			unresolved = append(unresolved, candidate)
			continue
		}
		if candidate.marker(kindIntro).present() && candidate.effectiveSource(kindIntro) != models.MarkerSourceScanner {
			continue
		}

		confidence := 0.85
		if source.segment.Algorithm == ChapterSilenceAlgorithm {
			confidence = 0.90
		}
		applied, patchErr := a.repo.PatchMarker(ctx, MarkerPatch{
			Kind:         kindIntro,
			ExpectedFile: candidate.expectedFile(),
			FileID:       candidate.FileID,
			Start:        source.segment.Start,
			End:          source.segment.End,
			Source:       models.MarkerSourceScanner,
			Confidence:   confidence,
			Algorithm:    EpisodeVersionCopyAlgorithm,
			DetectedAt:   time.Now().UTC(),
		})
		if patchErr != nil {
			msg := fmt.Sprintf("file %d: %v", candidate.FileID, patchErr)
			summary.Errors = append(summary.Errors, msg)
			a.logger.WarnContext(ctx, "intro marker episode version copy failed", "file_id", candidate.FileID, "source_file_id", source.candidate.FileID, "error", patchErr)
			unresolved = append(unresolved, candidate)
			continue
		}
		if applied {
			summary.EpisodeVersionMarkersCopied++
		}
	}

	return unresolved, summary
}

// refineChapterSegment runs silence refinement on a chapter intro. It returns
// false, leaving the file untouched, when deadline passed while it waited for
// an ffmpeg slot.
func (a *Analyzer) refineChapterSegment(ctx context.Context, candidate Candidate, segment Segment, deadline time.Time, summary *RunSummary) (Segment, bool) {
	if a.refiner == nil || !a.config.normalized().SilenceRefinementEnabled {
		return segment, true
	}
	release, err := a.acquireFFmpeg(ctx)
	if err != nil {
		summary.SilenceRefinementsAttempted++
		summary.SilenceRefinementErrors++
		return segment, true
	}
	if !deadline.IsZero() && time.Now().After(deadline) {
		release()
		return segment, false
	}
	summary.SilenceRefinementsAttempted++
	refined, ok, err := a.refiner.RefineChapterEnd(ctx, candidate, segment)
	release()
	if err != nil {
		summary.SilenceRefinementErrors++
		a.logger.WarnContext(ctx, "intro marker silence refinement failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
		if ctx.Err() == nil {
			a.recordSilenceAttempt(ctx, candidate, segment, err, summary)
		}
		return segment, true
	}
	if ok {
		summary.SilenceRefinementsApplied++
		return refined, true
	}
	a.recordSilenceAttempt(ctx, candidate, segment, nil, summary)
	return segment, true
}

// recordSilenceAttempt persists a refinement that kept the chapter boundary so
// the backfill stops spending its budget on the same unchanged file every run.
// A clean no-improvement result stands until the inputs change; a failure is
// retried with exponential backoff.
func (a *Analyzer) recordSilenceAttempt(ctx context.Context, candidate Candidate, segment Segment, refineErr error, summary *RunSummary) {
	attempt := SilenceRefinementAttempt{
		MediaFileID:     candidate.FileID,
		ConfigHash:      a.config.SilenceConfigHash(),
		FileHash:        candidate.FileHash,
		FileSize:        candidate.FileSize,
		DurationSeconds: candidate.DurationSeconds,
		ChaptersHash:    candidate.ChaptersHash,
		IntroStart:      segment.Start,
		IntroEnd:        segment.End,
		Status:          silenceAttemptNoImprovement,
		RecordedBy:      a.node,
		AttemptedAt:     time.Now().UTC(),
	}
	if refineErr != nil {
		previous, err := a.repo.LoadSilenceRefinementAttempt(ctx, candidate.FileID)
		if err != nil {
			summary.Errors = append(summary.Errors, fmt.Sprintf("file %d: %v", candidate.FileID, err))
			a.logger.WarnContext(ctx, "intro marker silence attempt load failed", "file_id", candidate.FileID, "error", err)
			return
		}
		attempt.Status = silenceAttemptFailed
		attempt.LastError = refineErr.Error()
		attempt.FailureCount = 1
		retryAfter := attempt.AttemptedAt.Add(retryDelay(1))
		// Backoff escalates only for this server's own consecutive failures; a
		// failure recorded elsewhere may come from that server's environment.
		if previous != nil && previous.Status == silenceAttemptFailed && previous.RecordedBy == attempt.RecordedBy &&
			previous.sameInputs(attempt) {
			if previous.RetryAfter != nil && attempt.AttemptedAt.Before(*previous.RetryAfter) {
				// A forced episode analysis failed inside the backoff window. That
				// is not a retry, so it must not escalate the backoff.
				attempt.FailureCount = previous.FailureCount
				retryAfter = *previous.RetryAfter
			} else {
				attempt.FailureCount = previous.FailureCount + 1
				retryAfter = attempt.AttemptedAt.Add(retryDelay(attempt.FailureCount))
			}
		}
		attempt.RetryAfter = &retryAfter
	}
	if err := a.repo.UpsertSilenceRefinementAttempt(ctx, attempt); err != nil {
		summary.Errors = append(summary.Errors, fmt.Sprintf("file %d: %v", candidate.FileID, err))
		a.logger.WarnContext(ctx, "intro marker silence attempt record failed", "file_id", candidate.FileID, "error", err)
	}
}

func setBestChapterSource(sources map[string]chapterSourceMarker, candidate Candidate, segment Segment) {
	existing, ok := sources[candidate.EpisodeID]
	if !ok || chapterSourceRank(segment) > chapterSourceRank(existing.segment) {
		sources[candidate.EpisodeID] = chapterSourceMarker{candidate: candidate, segment: segment}
	}
}

func chapterSourceRank(segment Segment) int {
	if segment.Algorithm == ChapterSilenceAlgorithm {
		return 2
	}
	if segment.Algorithm == ChapterAlgorithm {
		return 1
	}
	return 0
}

func compatibleEpisodeVersionDuration(source, target Candidate) bool {
	if source.DurationSeconds <= 0 || target.DurationSeconds <= 0 {
		return false
	}
	diff := source.DurationSeconds - target.DurationSeconds
	if diff < 0 {
		diff = -diff
	}
	return diff <= episodeVersionCopyDurationToleranceSeconds
}

// runSilenceBackfill refines chapter intros that were written before silence
// refinement ran. It is intro work, so it waits while kinds leaves intros out.
func (a *Analyzer) runSilenceBackfill(ctx context.Context, kinds EpisodeMarkerKinds) (RunSummary, error) {
	summary := RunSummary{}
	cfg := a.config.normalized()
	if !kinds.Intro || !cfg.SilenceRefinementEnabled || cfg.SilenceBackfillLimit <= 0 {
		return summary, nil
	}
	candidates, err := a.repo.ListChapterSilenceBackfillCandidates(ctx, cfg.SilenceBackfillLimit, cfg, a.node)
	if err != nil {
		return summary, err
	}
	summary.SilenceBackfillConsidered = len(candidates)
	if len(candidates) == 0 {
		return summary, nil
	}
	// Backfill files are independent: no episode-version copies are made, so
	// the candidates split across workers.
	opts := chapterProcessingOptions{
		forceExistingScanner: true,
		deadline:             time.Now().Add(cfg.SilenceBackfillMaxDuration),
	}
	workers := min(len(candidates), a.workerCount())
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for w := range workers {
		share := make([]Candidate, 0, len(candidates)/workers+1)
		for i := w; i < len(candidates); i += workers {
			share = append(share, candidates[i])
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, shareSummary := a.processChapterCandidates(ctx, share, opts)
			mu.Lock()
			mergeRunSummary(&summary, shareSummary)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return summary, nil
}

// groupJob is one marker kind's analysis of one season group.
type groupJob struct {
	kind  markerKind
	group candidateGroup
	// patchFileIDs limits which files the analysis may write; nil allows
	// every file in the group.
	patchFileIDs map[int]struct{}
}

// countGroupJobs counts the season groups each marker kind considers.
func countGroupJobs(summary *RunSummary, jobs []groupJob) {
	for _, job := range jobs {
		if job.kind == kindCredits {
			summary.CreditsSeasonGroupsConsidered++
		} else {
			summary.SeasonGroupsConsidered++
		}
	}
}

// analyzeJob analyzes a season group for the job's marker kind.
func (a *Analyzer) analyzeJob(ctx context.Context, job groupJob, opts analyzeGroupOptions) (RunSummary, error) {
	if job.kind == kindCredits {
		return a.analyzeCreditsGroup(ctx, job.group, opts)
	}
	return a.analyzeGroup(ctx, job.group, opts)
}

// analyzeGroups runs season group jobs on workerCount workers with opts.
// Most jobs are skipped or served from cached fingerprints, so workers keep
// the ffmpeg slots busy while other jobs compare or wait on the database.
// progress is called with the number of jobs finished.
func (a *Analyzer) analyzeGroups(ctx context.Context, jobs []groupJob, opts analyzeGroupOptions, progress func(done int)) RunSummary {
	var (
		mu      sync.Mutex
		summary RunSummary
		done    int
		wg      sync.WaitGroup
	)
	work := make(chan groupJob)
	for range min(len(jobs), a.workerCount()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range work {
				groupSummary, err := a.analyzeJob(ctx, job, opts)
				if err != nil {
					a.logger.WarnContext(ctx, "marker group analysis failed",
						"kind", job.kind.String(),
						"season_id", job.group.SeasonID,
						"media_folder_id", job.group.MediaFolderID,
						"group_key", job.group.AnalysisGroupKey,
						"error", err)
				}
				mu.Lock()
				mergeRunSummary(&summary, groupSummary)
				done++
				if progress != nil {
					progress(done)
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, job := range jobs {
		select {
		case work <- job:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	return summary
}

type candidateGroup struct {
	SeasonID         string
	MediaFolderID    int
	AnalysisGroupKey string
	Candidates       []Candidate
}

type analyzeGroupOptions struct {
	force        bool
	patchFileIDs map[int]struct{}
	persistState bool
	// creditsTail runs the credits tail pass; see Analyzer.creditsTailReady.
	creditsTail bool
}

// minimumGroupEpisodes is how many episodes a season group of kind needs to
// be analyzed. Intros and credits audio compare episodes, so they need two;
// with the credits tail pass, a lone episode can still get credits from
// video.
func minimumGroupEpisodes(kind markerKind, creditsTail bool) int {
	if kind == kindCredits && creditsTail {
		return 1
	}
	return 2
}

// creditsTailReady reports whether credits analysis can run tail passes: the
// analyzer has a sampler and ffmpeg offers what the pass needs. Without it,
// credits come from chapters and audio alone.
func (a *Analyzer) creditsTailReady(ctx context.Context) bool {
	if a.tailSampler == nil {
		return false
	}
	if err := a.tailSampler.PreflightCreditsTail(ctx); err != nil {
		if ctx.Err() == nil {
			a.tailWarnOnce.Do(func() {
				a.logger.WarnContext(ctx, "credits visuals unavailable; credits use chapters and audio only", "error", err)
			})
		}
		return false
	}
	return true
}

// groupCandidates groups candidates by season group, leaving out groups
// with fewer than minEpisodes episodes.
func groupCandidates(candidates []Candidate, minEpisodes int) []candidateGroup {
	byKey := map[string]*candidateGroup{}
	for _, candidate := range candidates {
		key := fmt.Sprintf("%d:%s:%s", candidate.MediaFolderID, candidate.SeasonID, candidate.AnalysisGroupKey())
		group := byKey[key]
		if group == nil {
			group = &candidateGroup{
				SeasonID:         candidate.SeasonID,
				MediaFolderID:    candidate.MediaFolderID,
				AnalysisGroupKey: candidate.AnalysisGroupKey(),
			}
			byKey[key] = group
		}
		group.Candidates = append(group.Candidates, candidate)
	}

	groups := make([]candidateGroup, 0, len(byKey))
	for _, group := range byKey {
		episodeIDs := map[string]struct{}{}
		for _, candidate := range group.Candidates {
			episodeIDs[candidate.EpisodeID] = struct{}{}
		}
		if len(episodeIDs) < minEpisodes {
			continue
		}
		groups = append(groups, *group)
	}
	sortCandidateGroups(groups)
	return groups
}

func sortCandidateGroups(groups []candidateGroup) {
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].MediaFolderID != groups[j].MediaFolderID {
			return groups[i].MediaFolderID < groups[j].MediaFolderID
		}
		if groups[i].SeasonID != groups[j].SeasonID {
			return groups[i].SeasonID < groups[j].SeasonID
		}
		return groups[i].AnalysisGroupKey < groups[j].AnalysisGroupKey
	})
}

func (a *Analyzer) analyzeGroup(ctx context.Context, group candidateGroup, opts analyzeGroupOptions) (RunSummary, error) {
	summary := RunSummary{}
	state := SeasonState{
		SeasonID:         group.SeasonID,
		MediaFolderID:    group.MediaFolderID,
		AnalysisGroupKey: group.AnalysisGroupKey,
		InputSignature:   InputSignature(group.Candidates),
		EpisodeCount:     distinctEpisodeCount(group.Candidates),
		FileCount:        len(group.Candidates),
	}
	analysisHash := a.config.AnalysisConfigHash()
	existing, err := a.repo.LoadSeasonState(ctx, state, analysisHash)
	if err != nil {
		return summary, err
	}
	if !opts.force && existing != nil && existing.InputSignature == state.InputSignature && existing.settled(time.Now()) {
		summary.GroupsSkipped++
		return summary, nil
	}

	inputs, counts, err := a.ensureFingerprints(ctx, group.Candidates)
	summary.FingerprintCacheHits += counts.hits
	summary.FingerprintsComputed += counts.computed
	summary.FingerprintExtractionErrors += counts.failed
	settle := func(status string) { settleSeasonState(&state, status, counts) }
	if err != nil {
		state.Status = seasonStatusFailed
		state.LastError = err.Error()
		if opts.persistState {
			_ = a.repo.UpsertSeasonState(ctx, state, analysisHash)
		}
		summary.Errors = append(summary.Errors, err.Error())
		return summary, err
	}
	if distinctFingerprintEpisodeCount(inputs) < 2 {
		state.LastError = "too few fingerprints"
		settle(seasonStatusNotFound)
		if opts.persistState {
			if err := a.repo.UpsertSeasonState(ctx, state, analysisHash); err != nil {
				return summary, err
			}
		}
		summary.GroupsNotFound++
		return summary, nil
	}

	segments := CompareFingerprints(inputs, a.config)
	if len(segments) == 0 {
		settle(seasonStatusNotFound)
		if opts.persistState {
			if err := a.repo.UpsertSeasonState(ctx, state, analysisHash); err != nil {
				return summary, err
			}
		}
		summary.GroupsNotFound++
		return summary, nil
	}

	byFileID := make(map[int]Candidate, len(group.Candidates))
	for _, candidate := range group.Candidates {
		byFileID[candidate.FileID] = candidate
	}
	// When subtitle refinement fails, a file that already has a
	// subtitle-refined marker keeps it: the unrefined result would outrank it.
	// Other files still get the unrefined marker. Either way the group is
	// recorded as failed so the next run retries the refinement.
	refinementFailures := 0
	for fileID, segment := range segments {
		if !shouldPatchGroupFile(fileID, opts.patchFileIDs) {
			continue
		}
		candidate := byFileID[fileID]
		var refineErr error
		segment, refineErr = a.refineChromaprintSegment(ctx, candidate, segment, &summary)
		if refineErr != nil {
			if err := ctx.Err(); err != nil {
				return summary, err
			}
			refinementFailures++
			if candidate.hasSubtitleRefinedIntro() {
				continue
			}
		}
		applied, patchErr := a.repo.PatchMarker(ctx, MarkerPatch{
			Kind:         kindIntro,
			ExpectedFile: candidate.expectedFile(),
			FileID:       fileID,
			Start:        segment.Start,
			End:          segment.End,
			Source:       models.MarkerSourceScanner,
			Confidence:   segment.Confidence,
			Algorithm:    segment.Algorithm,
			DetectedAt:   time.Now().UTC(),
		})
		if patchErr != nil {
			msg := fmt.Sprintf("file %d: %v", fileID, patchErr)
			summary.Errors = append(summary.Errors, msg)
			a.logger.WarnContext(ctx, "intro marker chromaprint patch failed", "file_id", fileID, "path", candidate.FilePath, "error", patchErr)
			continue
		}
		if applied {
			summary.ChromaprintMarkersWritten++
		}
	}

	if opts.persistState {
		settle(seasonStatusComplete)
		if refinementFailures > 0 {
			state.Status = seasonStatusFailed
			state.LastError = fmt.Sprintf("subtitle refinement failed for %d file(s)", refinementFailures)
		}
		state.MarkersWritten = summary.ChromaprintMarkersWritten
		if err := a.repo.UpsertSeasonState(ctx, state, analysisHash); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

// refineChromaprintSegment returns the segment to write, unchanged when
// refinement is disabled or does not apply, and the refinement error if any.
func (a *Analyzer) refineChromaprintSegment(ctx context.Context, candidate Candidate, segment Segment, summary *RunSummary) (Segment, error) {
	if a.chromaprintRefiner == nil || !a.config.normalized().DialogueRefinementEnabled {
		return segment, nil
	}
	summary.DialogueRefinementsAttempted++
	refined, ok, err := a.chromaprintRefiner.RefineChromaprintStart(ctx, candidate, segment)
	if err != nil {
		summary.DialogueRefinementErrors++
		a.logger.WarnContext(ctx, "intro marker dialogue refinement failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
		return segment, err
	}
	if ok {
		summary.DialogueRefinementsApplied++
		// A later start can leave a short intro, which rates as one.
		if refined.End-refined.Start < shortIntroSeconds {
			refined.Confidence = min(refined.Confidence, chromaprintShortConfidence)
		}
		return refined, nil
	}
	return segment, nil
}

// hasSubtitleRefinedIntro reports whether the file's current intro came from
// Chromaprint with subtitle refinement, in any version.
func (c Candidate) hasSubtitleRefinedIntro() bool {
	return c.IntroMarkersAlgorithm != nil &&
		strings.HasPrefix(*c.IntroMarkersAlgorithm, chromaprintDialogueAlgorithmPrefix)
}

func candidateFileIDs(candidates []Candidate) map[int]struct{} {
	fileIDs := make(map[int]struct{}, len(candidates))
	for _, candidate := range candidates {
		fileIDs[candidate.FileID] = struct{}{}
	}
	return fileIDs
}

func shouldPatchGroupFile(fileID int, allowed map[int]struct{}) bool {
	if allowed == nil {
		return true
	}
	_, ok := allowed[fileID]
	return ok
}

// fingerprintCounts tallies how a group's fingerprints were obtained.
type fingerprintCounts struct {
	hits     int
	computed int
	// failed counts extractions that failed in this analysis.
	failed int
	// deferred counts files skipped while an earlier failure backs off.
	deferred int
}

// ensureFingerprints loads cached intro fingerprints and extracts missing
// ones. It returns the inputs with how they were obtained; a database error
// or cancellation is returned as err. Credits inputs come from
// ensureCreditsInputs.
func (a *Analyzer) ensureFingerprints(ctx context.Context, candidates []Candidate) ([]fingerprintInput, fingerprintCounts, error) {
	var (
		mu       sync.Mutex
		inputs   []fingerprintInput
		counts   fingerprintCounts
		firstErr error
	)
	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	count := func(field *int, input *fingerprintInput) {
		mu.Lock()
		*field++
		if input != nil {
			inputs = append(inputs, *input)
		}
		mu.Unlock()
	}
	lookupFingerprint := a.fingerprintLookupFunc()
	acquire := a.ffmpegAcquirer()
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				setErr(err)
				return
			}
			cached, lookup, err := lookupFingerprint(ctx, candidate)
			if err != nil {
				setErr(err)
				return
			}
			switch lookup {
			case fingerprintCached:
				count(&counts.hits, &fingerprintInput{Candidate: candidate, Points: cached.Points, WindowStart: cached.WindowStartSeconds})
				return
			case fingerprintUnusable:
				return
			case fingerprintDeferred:
				count(&counts.deferred, nil)
				return
			}

			release, err := acquire(ctx)
			if err != nil {
				setErr(err)
				return
			}
			fp, ok, err := a.extractor.Extract(ctx, candidate)
			release()
			if err != nil {
				if ctx.Err() != nil {
					setErr(ctx.Err())
					return
				}
				a.logger.WarnContext(ctx, "marker fingerprint extraction failed", "kind", kindIntro.String(), "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
				count(&counts.failed, nil)
				return
			}
			if !ok {
				return
			}
			if err := a.repo.UpsertFingerprint(ctx, fp); err != nil {
				setErr(err)
				return
			}
			count(&counts.computed, &fingerprintInput{Candidate: candidate, Points: fp.Points, WindowStart: fp.WindowStartSeconds})
		}()
	}
	wg.Wait()
	sort.Slice(inputs, func(i, j int) bool {
		return inputs[i].Candidate.FileID < inputs[j].Candidate.FileID
	})
	return inputs, counts, firstErr
}

// settleSeasonState records a group's analysis status. A failed extraction
// (unlike a file with no audio to fingerprint) may succeed later, so the
// group stays partial and is retried even though its inputs have not
// changed. A failed status stands, since partial would settle it for a while.
func settleSeasonState(state *SeasonState, status string, counts fingerprintCounts) {
	state.Status = status
	if status == seasonStatusFailed {
		return
	}
	if failed := counts.failed + counts.deferred; failed > 0 {
		state.Status = seasonStatusPartial
		state.LastError = fmt.Sprintf("%d fingerprint extraction(s) failed", failed)
	}
}

func distinctEpisodeCount(candidates []Candidate) int {
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		seen[candidate.EpisodeID] = struct{}{}
	}
	return len(seen)
}

func distinctFingerprintEpisodeCount(inputs []fingerprintInput) int {
	seen := map[string]struct{}{}
	for _, input := range inputs {
		seen[input.Candidate.EpisodeID] = struct{}{}
	}
	return len(seen)
}

// Merge adds src's counts to s, as when one task runs RunEpisodes and
// RunMovies. Both passes count the same libraries, so s keeps the larger
// library count rather than their sum.
func (s *RunSummary) Merge(src RunSummary) {
	libraries := max(s.LibrariesScanned, src.LibrariesScanned)
	mergeRunSummary(s, src)
	s.LibrariesScanned = libraries
}

func mergeRunSummary(dst *RunSummary, src RunSummary) {
	dst.LibrariesScanned += src.LibrariesScanned
	dst.FilesConsidered += src.FilesConsidered
	dst.SeasonGroupsConsidered += src.SeasonGroupsConsidered
	dst.FingerprintsComputed += src.FingerprintsComputed
	dst.FingerprintCacheHits += src.FingerprintCacheHits
	dst.ChapterMarkersWritten += src.ChapterMarkersWritten
	dst.ChromaprintMarkersWritten += src.ChromaprintMarkersWritten
	dst.GroupsNotFound += src.GroupsNotFound
	dst.GroupsSkipped += src.GroupsSkipped
	dst.FingerprintExtractionErrors += src.FingerprintExtractionErrors
	dst.Errors = append(dst.Errors, src.Errors...)
	if src.ChromaprintSupported {
		dst.ChromaprintSupported = true
	}
	if src.ChromaprintSupportMessage != "" {
		dst.ChromaprintSupportMessage = src.ChromaprintSupportMessage
	}
	dst.SilenceRefinementsAttempted += src.SilenceRefinementsAttempted
	dst.SilenceRefinementsApplied += src.SilenceRefinementsApplied
	dst.SilenceRefinementErrors += src.SilenceRefinementErrors
	dst.EpisodeVersionMarkersCopied += src.EpisodeVersionMarkersCopied
	dst.SilenceBackfillConsidered += src.SilenceBackfillConsidered
	dst.DialogueRefinementsAttempted += src.DialogueRefinementsAttempted
	dst.DialogueRefinementsApplied += src.DialogueRefinementsApplied
	dst.DialogueRefinementErrors += src.DialogueRefinementErrors
	dst.CreditsSeasonGroupsConsidered += src.CreditsSeasonGroupsConsidered
	dst.CreditsGroupsNotFound += src.CreditsGroupsNotFound
	dst.CreditsGroupsSkipped += src.CreditsGroupsSkipped
	dst.CreditsChapterMarkersWritten += src.CreditsChapterMarkersWritten
	dst.CreditsVersionMarkersCopied += src.CreditsVersionMarkersCopied
	dst.CreditsFingerprintsComputed += src.CreditsFingerprintsComputed
	dst.CreditsFingerprintCacheHits += src.CreditsFingerprintCacheHits
	dst.CreditsFingerprintErrors += src.CreditsFingerprintErrors
	dst.CreditsAudioMarkersWritten += src.CreditsAudioMarkersWritten
	dst.CreditsRejected += src.CreditsRejected
	dst.CreditsTailScansComputed += src.CreditsTailScansComputed
	dst.CreditsTailCacheHits += src.CreditsTailCacheHits
	dst.CreditsTailScanErrors += src.CreditsTailScanErrors
	dst.CreditsTailUnusable += src.CreditsTailUnusable
	dst.CreditsAudioVideoMarkersWritten += src.CreditsAudioVideoMarkersWritten
	dst.CreditsVideoMarkersWritten += src.CreditsVideoMarkersWritten
	dst.MoviesConsidered += src.MoviesConsidered
	dst.MovieCreditsMarkersWritten += src.MovieCreditsMarkersWritten
	if src.MovieBudgetExhausted {
		dst.MovieBudgetExhausted = true
	}
}
