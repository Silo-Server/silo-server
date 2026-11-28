package transcodenode

import (
	"maps"
	"math"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// The node's own limits: what its operator set in its environment
// (SILO_NODE_MAX_TRANSCODES, SILO_NODE_HW_ACCEL,
// SILO_NODE_DOWNLOAD_ARTIFACT_MAX_BYTES). The core's planner reads the same
// values from the node's row, which the node declares, but whoever runs the
// core can change that row; these hold whatever it says. Without them the
// node runs with what the core sends, as before. The refusal header and its
// values live in nodepool (NodeRefusalHeader), which every caller of the
// node's job routes already imports.

// refusalRetryAfter is the Retry-After of a capacity or storage refusal, in
// seconds. The core places the work elsewhere at once; the header is for a
// player that reached the node through a relay.
const refusalRetryAfter = "5"

const (
	errNodeAtCapacity  = "node is at its job limit"
	errNodeStorageFull = "node's prepared-download directory is at its budget"
)

// SetJobCap sets the most jobs the node runs at once, counted as health
// reports active_jobs; 0 is no cap. Set it before serving.
func (s *Server) SetJobCap(limit int) {
	s.jobCap.Store(int32(min(max(limit, 0), math.MaxInt32)))
}

// SetJobCapsByKind sets the most jobs of each kind the node runs at once,
// under the job cap SetJobCap sets: a kind it does not name has no cap of
// its own. The node reports them on health, where the core's planner reads
// them. Set it before serving.
func (s *Server) SetJobCapsByKind(caps map[nodepool.JobKind]int) {
	s.kindCaps = make(map[nodepool.JobKind]int, len(caps))
	for kind, limit := range caps {
		if limit > 0 {
			s.kindCaps[kind] = limit
		}
	}
}

// SetDeclaredHWAccel sets the hardware acceleration the node's operator
// declared, which a start request cannot widen; see jobHWAccel. Empty
// leaves the request's choice. Set it before serving.
func (s *Server) SetDeclaredHWAccel(accel string) {
	s.declaredHWAccel = strings.ToLower(strings.TrimSpace(accel))
}

// SetArtifactBudget sets the byte budget of the prepared-download directory
// the node's operator declared; 0 is none. Set it before serving.
func (s *Server) SetArtifactBudget(bytes int64) {
	s.artifactBudget.Store(max(bytes, 0))
}

// jobSlot is new work of kind admitted under the job caps and not yet
// counted in activeJobs. The admitted-but-uncounted slots count against the
// caps too, so two starts arriving together at a cap less one cannot both
// pass. A kindOnly slot holds room under the kind's cap alone: it continues
// a job the node holds, which the job cap already counts, as another kind.
type jobSlot struct {
	s        *Server
	kind     nodepool.JobKind
	kindOnly bool
	done     bool
}

// admitJob admits new work of kind under the job cap and the kind's own cap:
// work that is not a session, remux or preparation the node already holds.
// It reports false at either cap, with capKind naming the kind whose own cap
// refused it, or empty when the job cap did (writeAtCapacity). The health
// report counts the slot from here, so the core sees the job while it gets
// ready (a RequireReady start waiting on its first segment). The caller counts
// the slot with countJob once the job runs, and releases it on every other
// return (release after count does nothing).
func (s *Server) admitJob(kind nodepool.JobKind) (slot *jobSlot, capKind nodepool.JobKind, ok bool) {
	return s.admit(kind, false)
}

// admitKind admits work that continues a job the node holds but changes its
// kind, under kind's own cap only: the job cap counts the job it continues,
// and a kind cap would otherwise let a replan from a remux add a video encode
// past its cap. It reports false, with capKind set, at that cap.
func (s *Server) admitKind(kind nodepool.JobKind) (slot *jobSlot, capKind nodepool.JobKind, ok bool) {
	return s.admit(kind, true)
}

func (s *Server) admit(kind nodepool.JobKind, kindOnly bool) (*jobSlot, nodepool.JobKind, bool) {
	s.admission.Lock()
	defer s.admission.Unlock()
	if full, capKind := s.fullLocked(kind, kindOnly); full {
		return nil, capKind, false
	}
	if s.admittedKinds == nil {
		s.admittedKinds = make(map[nodepool.JobKind]int, len(nodepool.JobKinds))
	}
	if !kindOnly {
		s.admitted++
	}
	s.admittedKinds[kind]++
	return &jobSlot{s: s, kind: kind, kindOnly: kindOnly}, "", true
}

// unadmitLocked takes an uncounted slot out of the admitted counts. The caller
// holds s.admission.
func (s *Server) unadmitLocked(slot *jobSlot) {
	slot.done = true
	if !slot.kindOnly {
		s.admitted--
	}
	s.admittedKinds[slot.kind]--
}

// reportedJobs is what the health report counts: the jobs activeJobs counts
// plus the slots admitted and not yet counted, by kind as well. It reads under
// s.admission, which countJob holds while it moves a slot from admitted to
// counted, so a job is in exactly one of the two. A kind-only slot (admitKind)
// is in the counts by kind but not in the total: the job it continues is
// already counted there. A drained node reports only
// the jobs it counts: what it admits from then on is a start it is about to
// refuse.
func (s *Server) reportedJobs(drained bool) (int32, map[nodepool.JobKind]int) {
	if drained {
		return s.activeJobs.Load(), s.jobKinds.snapshot(nil)
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	jobs := int32(min(int(s.activeJobs.Load())+s.admitted, math.MaxInt32)) //nolint:gosec // min clamps it to int32
	return jobs, s.jobKinds.snapshot(s.admittedKinds)
}

// countJob counts a job of kind in activeJobs: its admitted slot when it was
// new work, or none for work the node already held, which the cap never
// refuses. The job ends with endJob of the same kind.
func (s *Server) countJob(slot *jobSlot, kind nodepool.JobKind) {
	if slot == nil {
		s.activeJobs.Add(1)
		s.jobKinds.add(kind, 1)
		return
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	if slot.done {
		return
	}
	s.unadmitLocked(slot)
	s.activeJobs.Add(1)
	s.jobKinds.add(kind, 1)
}

// endJob uncounts a job countJob counted.
func (s *Server) endJob(kind nodepool.JobKind) {
	s.activeJobs.Add(-1)
	s.jobKinds.add(kind, -1)
}

// jobKindCounts breaks activeJobs down by nodepool.JobKind for the health
// report, which the core's planner balances each kind on. It moves with
// activeJobs in countJob and endJob. The zero value is ready to use.
type jobKindCounts struct {
	mu   sync.Mutex
	jobs map[nodepool.JobKind]int
}

func (c *jobKindCounts) add(kind nodepool.JobKind, delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jobs == nil {
		c.jobs = make(map[nodepool.JobKind]int, len(nodepool.JobKinds))
	}
	c.jobs[kind] += delta
}

func (c *jobKindCounts) count(kind nodepool.JobKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return max(c.jobs[kind], 0)
}

// snapshot names every kind, idle ones as zero, so the core can tell a node
// with no jobs of a kind from a node that cannot say. extra adds jobs not
// counted here, such as admitted slots.
func (c *jobKindCounts) snapshot(extra map[nodepool.JobKind]int) map[nodepool.JobKind]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[nodepool.JobKind]int, len(nodepool.JobKinds))
	for _, kind := range nodepool.JobKinds {
		out[kind] = max(c.jobs[kind]+extra[kind], 0)
	}
	return out
}

// sessionJobKind classifies an HLS transcode session from what its recipe
// re-encodes. A session's target codecs never change after it starts, so its
// start and its retirement name the same kind.
func sessionJobKind(session *playback.TranscodeSession) nodepool.JobKind {
	opts := session.Opts()
	return nodepool.CodecJobKind(opts.TargetCodecVideo, opts.TargetCodecAudio)
}

// release gives back a slot that was never counted.
func (slot *jobSlot) release() {
	if slot == nil {
		return
	}
	slot.s.admission.Lock()
	defer slot.s.admission.Unlock()
	if !slot.done {
		slot.s.unadmitLocked(slot)
	}
}

// fullLocked reports whether the counted and admitted jobs fill the job cap,
// or those of kind fill the kind's own cap, and which: capKind is kind when
// its own cap is full, empty when the job cap is. kindOnly checks the kind's
// cap alone (admitKind). The caller holds s.admission.
func (s *Server) fullLocked(kind nodepool.JobKind, kindOnly bool) (full bool, capKind nodepool.JobKind) {
	if limit := int(s.jobCap.Load()); !kindOnly && limit > 0 && int(s.activeJobs.Load())+s.admitted >= limit {
		return true, ""
	}
	if limit := s.kindCaps[kind]; limit > 0 && s.jobKinds.count(kind)+s.admittedKinds[kind] >= limit {
		return true, kind
	}
	return false, ""
}

// setRefusal adds the headers of a capacity or storage refusal.
func setRefusal(w http.ResponseWriter, reason string) {
	w.Header().Set("Retry-After", refusalRetryAfter)
	w.Header().Set(nodepool.NodeRefusalHeader, reason)
}

// writeAtCapacity refuses new work at the job cap, or at capKind's own cap
// when capKind is set: 503, Retry-After and RefusalAtCapacity, a plain-text
// body as a draining refusal has.
func writeAtCapacity(w http.ResponseWriter, capKind nodepool.JobKind) {
	setAtCapacity(w, capKind)
	http.Error(w, errNodeAtCapacity, http.StatusServiceUnavailable)
}

// setAtCapacity adds the headers of a refusal at the job cap, naming capKind
// (nodepool.NodeRefusalKindHeader) when that kind's own cap refused the
// work, so the core holds the node back from that kind only.
func setAtCapacity(w http.ResponseWriter, capKind nodepool.JobKind) {
	setRefusal(w, nodepool.RefusalAtCapacity)
	if capKind != "" {
		w.Header().Set(nodepool.NodeRefusalKindHeader, string(capKind))
	}
}

// admitPreparation admits a prepared-download request under the job cap and
// the declared budget, before its source opens, and writes the refusal when
// it is refused. A preparation of this artifact already running here, or an
// artifact already prepared, is the job the core asks about again: the
// request waits for it or returns it, so neither the cap nor the budget turns
// it away, and the slot is nil.
func (s *Server) admitPreparation(w http.ResponseWriter, req downloadprepare.Request) (slot *jobSlot, refused bool) {
	if _, preparing := s.prepareProgress.get(req.ArtifactID); preparing {
		return nil, false
	}
	if _, prepared := existingDownloadPrepareResult(filepath.Join(s.artifactRoot, req.ArtifactID+".mp4"), req); prepared {
		return nil, false
	}
	return s.admitNewPreparation(w, req.JobKind())
}

// admitNewPreparation admits a preparation the node has to encode, under the
// declared budget and the job cap, and writes the refusal when it is refused.
func (s *Server) admitNewPreparation(w http.ResponseWriter, kind nodepool.JobKind) (slot *jobSlot, refused bool) {
	if s.artifactBudgetReached() {
		setRefusal(w, nodepool.RefusalStorageFull)
		http.Error(w, errNodeStorageFull, http.StatusServiceUnavailable)
		return nil, true
	}
	slot, capKind, ok := s.admitJob(kind)
	if !ok {
		writeAtCapacity(w, capKind)
		return nil, true
	}
	return slot, false
}

// artifactBudgetReached reports whether the prepared-download directory,
// as last measured, holds at least the budget the node declares. With no
// declared budget, or no measurement yet, it never is: the core's own budget
// check still applies.
func (s *Server) artifactBudgetReached() bool {
	budget := s.artifactBudget.Load()
	if budget <= 0 {
		return false
	}
	usage, ok := s.artifactUsage()
	return ok && usage.TotalBytes() >= budget
}

// jobHWAccel is the hardware acceleration a start runs with. The core names
// one in the request, from the node's row; a node whose operator declared an
// acceleration runs that one instead, so a core whose row for the node was
// changed cannot move it to another backend. The core may still ask for none,
// which a software tone map needs. A node that declares auto lets a hardware
// tone map keep the backend the core chose from the node's own capability
// report, since auto names none; a node that declares a backend runs that one
// for a hardware tone map too. A node that declares none never uses its GPU.
func (s *Server) jobHWAccel(requested string, hardwareToneMap bool) string {
	declared := s.declaredHWAccel
	switch {
	case declared == "":
		return requested
	case declared == playback.HWAccelNone, strings.EqualFold(strings.TrimSpace(requested), playback.HWAccelNone):
		return playback.HWAccelNone
	case hardwareToneMap && declared == playback.HWAccelAuto:
		return requested
	default:
		return declared
	}
}

// continuationWindow is how long after this node last served a playback
// session's progressive remux a new request for that session still continues
// it: a seek or a reconnect after a pause, which the cap must not turn away.
// It matches the idle lifetime of an HLS session.
const continuationWindow = sessionIdleTTL

// continuation is the allowance one new transport has to overlap the
// transport it replaces for a playback session, held from the moment it is
// granted until the transport is registered or the request fails.
type continuation struct {
	s                 *Server
	playbackSessionID string
	// kinds are the kinds of the jobs the node holds for the playback
	// session; see covers.
	kinds []nodepool.JobKind
	done  bool
}

// covers reports whether work of kind continues a job of the same kind. Work
// of another kind needs room under its own kind's cap (admitKind): the
// overlap allowance stands in for the job cap only.
func (c *continuation) covers(kind nodepool.JobKind) bool {
	return c != nil && slices.Contains(c.kinds, kind)
}

// claimContinuation reports whether work for playbackSessionID continues a
// job this node holds, and holds that allowance for the caller: an HLS
// session or progressive remux the node runs for that playback session (a
// quality switch or replan starts a new transport for the same session), or
// a remux it served within continuationWindow. Such work is not refused at
// the cap; the planner counts it as the same job.
//
// Only one transport may overlap the one it replaces. The transports the
// node runs for the playback session and the allowances already granted and
// not yet registered are counted together, under the admission lock, so
// starts arriving together for one playback session cannot all pass: a
// playback session with two is not continuing anything, and a third
// transport is new work that needs a slot. Naming one playback session over
// and over therefore cannot run the node past its cap by more than one
// process per session. The caller releases the allowance once its transport
// is registered, and on every other return.
func (s *Server) claimContinuation(playbackSessionID string, now time.Time) (*continuation, bool) {
	if playbackSessionID == "" {
		return nil, false
	}
	running := 0
	var kinds []nodepool.JobKind
	s.mu.RLock()
	for transportID, identity := range s.identities {
		if identity.playbackSessionID == playbackSessionID {
			running++
			if session, ok := s.sessions[transportID]; ok {
				kinds = append(kinds, sessionJobKind(session))
			}
		}
	}
	for _, remux := range s.progressiveRemuxes {
		if remux.playbackSessionID == playbackSessionID {
			running++
			kinds = append(kinds, remux.kind)
		}
	}
	s.mu.RUnlock()
	s.admission.Lock()
	defer s.admission.Unlock()
	held := running + s.continuing[playbackSessionID]
	if held > 1 {
		return nil, false
	}
	if running == 0 {
		// With nothing running, the job continued is the remux served
		// recently: for the first request that resumes it, and for one that
		// arrives with it (a range probe and the real request), which must
		// not be refused at the remux cap the first one is not.
		seen, ok := s.remuxSeen[playbackSessionID]
		recent := ok && now.Sub(seen.at) < continuationWindow
		if held == 0 && !recent {
			return nil, false
		}
		if recent {
			kinds = append(kinds, seen.kind)
		}
	}
	if s.continuing == nil {
		s.continuing = make(map[string]int)
	}
	s.continuing[playbackSessionID]++
	return &continuation{s: s, playbackSessionID: playbackSessionID, kinds: kinds}, true
}

// admitStart admits a start under the job caps, classed by its target codecs
// as sessionJobKind classes the session it starts. One that replaces a session
// the node runs, or that holds the overlap allowance of its playback session,
// continues a job and takes no slot under the job cap; it takes one under its
// kind's cap when the job it continues is of another kind. Any other is new
// work. release gives back whatever was held and is called once the start has
// registered its session or failed. It reports false at a cap, with capKind
// as admitJob reports it.
func (s *Server) admitStart(req TranscodeStartRequest, now time.Time) (slot *jobSlot, release func(), capKind nodepool.JobKind, ok bool) {
	kind := nodepool.CodecJobKind(req.TargetCodecVideo, req.TargetCodecAudio)
	if hosted, hosts := s.hostedSessionKind(req.SessionID); hosts {
		if hosted == kind {
			return nil, func() {}, "", true
		}
		if slot, capKind, ok = s.admitKind(kind); !ok {
			return nil, nil, capKind, false
		}
		return slot, slot.release, "", true
	}
	if overlap, continues := s.claimContinuation(req.PlaybackSessionID, now); continues {
		if overlap.covers(kind) {
			return nil, overlap.release, "", true
		}
		if slot, capKind, ok = s.admitKind(kind); !ok {
			overlap.release()
			return nil, nil, capKind, false
		}
		return slot, func() { slot.release(); overlap.release() }, "", true
	}
	if slot, capKind, ok = s.admitJob(kind); !ok {
		return nil, nil, capKind, false
	}
	return slot, slot.release, "", true
}

// hostedSessionKind returns the kind of the HLS session sessionID the node
// runs, and whether it runs one.
func (s *Server) hostedSessionKind(sessionID string) (nodepool.JobKind, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return "", false
	}
	return sessionJobKind(session), true
}

// release gives the allowance back; nil and repeated calls do nothing.
func (c *continuation) release() {
	if c == nil {
		return
	}
	c.s.admission.Lock()
	defer c.s.admission.Unlock()
	if c.done {
		return
	}
	c.done = true
	if c.s.continuing[c.playbackSessionID] <= 1 {
		delete(c.s.continuing, c.playbackSessionID)
		return
	}
	c.s.continuing[c.playbackSessionID]--
}

// remuxSighting is when the node last served a playback session's
// progressive remux, and the remux's kind.
type remuxSighting struct {
	at   time.Time
	kind nodepool.JobKind
}

// noteRemux records that the node serves a progressive remux of kind for
// playbackSessionID now, and drops sessions it has not served for
// continuationWindow.
func (s *Server) noteRemux(playbackSessionID string, kind nodepool.JobKind, now time.Time) {
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.remuxSeen == nil {
		s.remuxSeen = make(map[string]remuxSighting)
	}
	s.remuxSeen[playbackSessionID] = remuxSighting{at: now, kind: kind}
	if now.Sub(s.remuxSeenPruned) < stopFencePruneInterval {
		return
	}
	s.remuxSeenPruned = now
	for id, seen := range s.remuxSeen {
		if now.Sub(seen.at) >= continuationWindow {
			delete(s.remuxSeen, id)
		}
	}
}

// jobCapRefusalHeaders are the headers of a 503 that refuses new work at the
// node's own job cap or declared budget, for the routes' protocol entries.
func jobCapRefusalHeaders() map[string]*huma.Param {
	return map[string]*huma.Param{
		nodepool.NodeRefusalHeader: {Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{nodepool.RefusalAtCapacity, nodepool.RefusalStorageFull}},
			Description: "Why the node refused new work: at_capacity at the job cap it declares (SILO_NODE_MAX_TRANSCODES) or the cap it declares for the work's kind (SILO_NODE_MAX_VIDEO_TRANSCODES, SILO_NODE_MAX_AUDIO_TRANSCODES, SILO_NODE_MAX_REMUXES), storage_full at the prepared-download budget it declares. Absent on every other 503, a draining refusal included; a caller that does not know it treats the 503 as a draining refusal."},
		nodepool.NodeRefusalKindHeader: {Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{string(nodepool.JobKindVideoTranscode), string(nodepool.JobKindAudioTranscode), string(nodepool.JobKindRemux), string(nodepool.JobKindTrickplay)}},
			Description: "With at_capacity, the job kind whose own cap refused the work; absent when the node's job cap did. The core then holds the node back from that kind only."},
		"Retry-After": {Schema: &huma.Schema{Type: huma.TypeString}, Description: "Seconds, with the refusal header."},
	}
}

// withJobCapRefusal adds the refusal headers to an operation's 503 and notes
// the cap in its description.
func withJobCapRefusal(op workerprotocol.Operation, continued string) workerprotocol.Operation {
	response := op.Responses["503"]
	if response.Headers == nil {
		response.Headers = map[string]*huma.Param{}
	}
	maps.Copy(response.Headers, jobCapRefusalHeaders())
	op.Description += " A node that declares a job cap refuses new work once it runs that many jobs (the jobs health reports as active_jobs, admitted and not yet counted included) with 503, " +
		nodepool.NodeRefusalHeader + ": " + nodepool.RefusalAtCapacity + " and Retry-After"
	if continued != "" {
		op.Description += "; " + continued + " continues a job the node holds and is never refused at the cap"
	}
	op.Description += "."
	return op
}
