package mediasample

import (
	"context"
	"math"
	"time"
)

// A sampled list gives each sample the keyframe at or before its time. Two
// kinds of cell come out of that run worse than the input allows, and a
// repair reads their samples again, after the run and before the sheets are
// encoded:
//
//   - Missing cells decoded no frame. The demuxer could not seek to a
//     keyframe before the sample, as in a container that flags no keyframes
//     (VC-1 in Matroska, whose I-frames only the decoder recognizes), or the
//     HEVC decoder dropped the keyframe as a duplicate picture order count
//     (see watchDuplicatePOC). The repair lists them again, keyframes only,
//     each entry lasting until the next sample: the decoder starts afresh
//     and finds the keyframe before the sample, or failing that the first
//     one after it.
//   - Repeated cells show the same moment as the cell before, because no
//     keyframe lies between the two samples: a long GOP. Two cells whose
//     frames lie within repeatShare of the time between their samples count,
//     since an open-GOP keyframe can bring a leading picture just before it
//     that one sample keeps and the next does not. The repair lists them
//     again decoding every reference frame (-skip_frame noref) from the
//     keyframe on, and keeps the last frame at or before the sample's time,
//     so the cell shows its own moment instead of a repeat.
//
// Each repaired sample keeps its nearest frame: the last at or before its
// time, else the first after it. A repair that fails or runs out of time
// keeps the frames it already placed, which ffmpeg wrote and logged in
// order, and changes nothing else; the run's own result stands. Repairs
// share one time box, so a long-GOP 4K file decoded in software adds at most
// repairBaseSeconds plus repairSecondsPerSample per sample to its attempt.
const (
	repairBaseSeconds      = 30
	repairSecondsPerSample = 0.25
	// repeatShare is how close, as a share of the time between their
	// samples, two cells' frames lie when they show the same moment.
	repeatShare = 0.1
	// exactSkipFrame decodes the reference frames a repeated cell's moment
	// may lie on, skipping the frames nothing else is predicted from.
	exactSkipFrame = "noref"
	// keyframeSkipFrame decodes only keyframes, as every list does.
	keyframeSkipFrame = "nokey"
)

// sheetsList is how a Sheets list reads its samples: each entry lasts span
// seconds, and the decoder skips frames as -skip_frame:v skip says.
type sheetsList struct {
	span float64
	skip string
}

// sampledSheetsList is how the first run of a sampled list reads.
var sampledSheetsList = sheetsList{span: sheetSampleSpanSeconds, skip: keyframeSkipFrame}

// sheetCandidate is the frame a repair keeps for one sample so far.
type sheetCandidate struct {
	frame  []byte
	source float64
	// before marks a frame at or before the sample's time.
	before bool
}

// offer keeps frame as cell's candidate when it lies nearer the sample's
// time than the candidate so far: a frame at or before the time replaces any
// earlier one, since frames arrive in presentation order, and a frame after
// it only fills an empty candidate.
func (a *sheetAssembler) offer(cell int, source float64, frame []byte) {
	candidate := &a.candidates[cell]
	before := !math.IsNaN(source) && source <= a.times[cell]+sampleMatchSeconds
	if candidate.frame != nil && !before {
		a.recycle(frame)
		return
	}
	a.recycle(candidate.frame)
	*candidate = sheetCandidate{frame: frame, source: source, before: before}
}

// repairs returns the cells of a closed sampled run worth a second read:
// those without a frame, and those whose frame is the keyframe the cell
// before already shows. A run that decoded nothing is repaired too: a
// container that flags no keyframes can miss every short list entry.
func (a *sheetAssembler) repairs() (missing, repeated []int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.sampled {
		return nil, nil
	}
	for cell := range a.times {
		switch {
		case !a.placed(cell):
			missing = append(missing, cell)
		case cell > 0 && a.placed(cell-1) && math.Abs(a.sources[cell]-a.sources[cell-1]) < repeatShare*(a.times[cell]-a.times[cell-1]):
			repeated = append(repeated, cell)
		}
	}
	return missing, repeated
}

// placed reports whether cell holds a frame of its own.
func (a *sheetAssembler) placed(cell int) bool {
	canvas := a.open[cell/a.perSheet]
	return canvas != nil && canvas.set[cell%a.perSheet]
}

// adopt places the candidates a repair over cells collected. A missing cell
// takes any candidate; a repeated cell only one at or before its time and
// later than the frame it shows.
func (a *sheetAssembler) adopt(cells []int, repair *sheetAssembler) {
	repair.mu.Lock()
	defer repair.mu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, cell := range cells {
		candidate := repair.candidates[i]
		if candidate.frame == nil {
			continue
		}
		missing := !a.placed(cell)
		if !missing && (!candidate.before || !(candidate.source > a.sources[cell]+sampleMatchSeconds)) {
			continue
		}
		canvas := a.canvas(cell / a.perSheet)
		slot := cell % a.perSheet
		canvas.write(slot, candidate.frame)
		canvas.set[slot] = true
		a.sources[cell] = candidate.source
		if missing {
			a.decoded++
		}
		if !a.refinedCells[cell] {
			a.refinedCells[cell] = true
			a.refined++
		}
	}
}

// repairSheets runs the repairs a closed sampled run needs, within their
// time box and the attempt's.
func (a attemptRun) repairSheets(req Request, run *sheetAssembler, inputStart float64) {
	missing, repeated := run.repairs()
	if len(missing) == 0 && len(repeated) == 0 {
		return
	}
	budget := time.Duration((repairBaseSeconds + repairSecondsPerSample*float64(len(run.times))) * float64(time.Second))
	ctx, cancel := context.WithTimeout(a.attemptCtx, budget)
	defer cancel()
	repair := a
	repair.attemptCtx = ctx
	if len(missing) > 0 {
		repair.repair(req, run, missing, inputStart, sheetsList{span: missingRepairSpan(run.times), skip: keyframeSkipFrame})
		// A recovered cell can show the keyframe of the cell after it.
		_, repeated = run.repairs()
	}
	if len(repeated) > 0 && ctx.Err() == nil {
		repair.repair(req, run, repeated, inputStart, sheetsList{span: sheetSampleSpanSeconds, skip: exactSkipFrame})
	}
}

// repair reads the samples of cells again as list says and adopts what it
// finds. A read that ffmpeg finishes must line up with its log, as a run's
// does; one cut short keeps the frames it placed, each written and logged in
// order before the end.
func (a attemptRun) repair(req Request, run *sheetAssembler, cells []int, inputStart float64, list sheetsList) {
	times := make([]float64, len(cells))
	for i, cell := range cells {
		times[i] = run.times[cell]
	}
	repairReq := req
	repairReq.Samples = &Samples{Seconds: times}
	plan := samplePlan(times, inputStart)
	args, stdinBytes, err := buildSheetsArgs(repairReq, a.attempt, a.hw, plan.entries, a.sheetsGraph, "", list)
	if err != nil {
		return
	}
	collector := newSheetAssembler(*req.Sheets, times, true, 0, list.span)
	collector.plan = plan
	collector.candidates = make([]sheetCandidate, len(times))
	finished := a.exec(req, args, stdinBytes, collector, collector.line) == nil
	if (finished && collector.close() != nil) || (!finished && collector.broken()) {
		return
	}
	run.adopt(cells, collector)
}

// broken reports whether the frames seen so far stopped lining up with the
// log.
func (a *sheetAssembler) broken() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err != nil
}

// missingRepairSpan is how long a missing sample's repair entry lasts: until
// the next sample, the shortest gap between the run's samples, so a frame it
// finds after the sample's time stays in the sample's own stretch. A run of
// one sample takes a second.
func missingRepairSpan(times []float64) float64 {
	span := math.Inf(1)
	for i := 1; i < len(times); i++ {
		span = math.Min(span, times[i]-times[i-1])
	}
	if math.IsInf(span, 1) {
		return 1
	}
	return math.Max(span, sheetSampleSpanSeconds)
}
