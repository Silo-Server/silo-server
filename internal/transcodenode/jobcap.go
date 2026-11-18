package transcodenode

import (
	"maps"
	"math"
	"net/http"
	"path/filepath"
	"strings"
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

// jobSlot is new work admitted under the job cap and not yet counted in
// activeJobs. The admitted-but-uncounted slots count against the cap too, so
// two starts arriving together at the cap less one cannot both pass.
type jobSlot struct {
	s    *Server
	done bool
}

// admitJob admits new work under the job cap: work that is not a session,
// remux or preparation the node already holds. It reports false at the cap.
// The caller counts the slot with countJob once the job runs, and releases
// it on every other return (release after count does nothing).
func (s *Server) admitJob() (*jobSlot, bool) {
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.fullLocked() {
		return nil, false
	}
	s.admitted++
	return &jobSlot{s: s}, true
}

// countJob counts a job in activeJobs: its admitted slot when it was new
// work, or none for work the node already held, which the cap never refuses.
func (s *Server) countJob(slot *jobSlot) {
	if slot == nil {
		s.activeJobs.Add(1)
		return
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	if slot.done {
		return
	}
	slot.done = true
	s.admitted--
	s.activeJobs.Add(1)
}

// release gives back a slot that was never counted.
func (slot *jobSlot) release() {
	if slot == nil {
		return
	}
	slot.s.admission.Lock()
	defer slot.s.admission.Unlock()
	if !slot.done {
		slot.done = true
		slot.s.admitted--
	}
}

// fullLocked reports whether the counted and admitted jobs fill the cap. The
// caller holds s.admission.
func (s *Server) fullLocked() bool {
	limit := int(s.jobCap.Load())
	return limit > 0 && int(s.activeJobs.Load())+s.admitted >= limit
}

// setRefusal adds the headers of a capacity or storage refusal.
func setRefusal(w http.ResponseWriter, reason string) {
	w.Header().Set("Retry-After", refusalRetryAfter)
	w.Header().Set(nodepool.NodeRefusalHeader, reason)
}

// writeAtCapacity refuses new work at the job cap: 503, Retry-After and
// RefusalAtCapacity, a plain-text body as a draining refusal has.
func writeAtCapacity(w http.ResponseWriter) {
	setRefusal(w, nodepool.RefusalAtCapacity)
	http.Error(w, errNodeAtCapacity, http.StatusServiceUnavailable)
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
	return s.admitNewPreparation(w)
}

// admitNewPreparation admits a preparation the node has to encode, under the
// declared budget and the job cap, and writes the refusal when it is refused.
func (s *Server) admitNewPreparation(w http.ResponseWriter) (slot *jobSlot, refused bool) {
	if s.artifactBudgetReached() {
		setRefusal(w, nodepool.RefusalStorageFull)
		http.Error(w, errNodeStorageFull, http.StatusServiceUnavailable)
		return nil, true
	}
	slot, ok := s.admitJob()
	if !ok {
		writeAtCapacity(w)
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
	done              bool
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
	s.mu.RLock()
	for _, identity := range s.identities {
		if identity.playbackSessionID == playbackSessionID {
			running++
		}
	}
	for _, remux := range s.progressiveRemuxes {
		if remux.playbackSessionID == playbackSessionID {
			running++
		}
	}
	s.mu.RUnlock()
	s.admission.Lock()
	defer s.admission.Unlock()
	held := running + s.continuing[playbackSessionID]
	switch {
	case held > 1:
		return nil, false
	case held == 0:
		if last, ok := s.remuxSeen[playbackSessionID]; !ok || now.Sub(last) >= continuationWindow {
			return nil, false
		}
	}
	if s.continuing == nil {
		s.continuing = make(map[string]int)
	}
	s.continuing[playbackSessionID]++
	return &continuation{s: s, playbackSessionID: playbackSessionID}, true
}

// admitStart admits a start under the job cap. One that replaces a session
// the node runs, or that holds the overlap allowance of its playback session,
// continues a job and takes no slot; any other is new work. release gives
// back whatever was held and is called once the start has registered its
// session or failed. It reports false at the cap.
func (s *Server) admitStart(req TranscodeStartRequest, now time.Time) (slot *jobSlot, release func(), ok bool) {
	if s.hostsSession(req.SessionID) {
		return nil, func() {}, true
	}
	if overlap, continues := s.claimContinuation(req.PlaybackSessionID, now); continues {
		return nil, overlap.release, true
	}
	slot, ok = s.admitJob()
	if !ok {
		return nil, nil, false
	}
	return slot, slot.release, true
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

// noteRemux records that the node serves a progressive remux for
// playbackSessionID now, and drops sessions it has not served for
// continuationWindow.
func (s *Server) noteRemux(playbackSessionID string, now time.Time) {
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.remuxSeen == nil {
		s.remuxSeen = make(map[string]time.Time)
	}
	s.remuxSeen[playbackSessionID] = now
	if now.Sub(s.remuxSeenPruned) < stopFencePruneInterval {
		return
	}
	s.remuxSeenPruned = now
	for id, last := range s.remuxSeen {
		if now.Sub(last) >= continuationWindow {
			delete(s.remuxSeen, id)
		}
	}
}

// jobCapRefusalHeaders are the headers of a 503 that refuses new work at the
// node's own job cap or declared budget, for the routes' protocol entries.
func jobCapRefusalHeaders() map[string]*huma.Param {
	return map[string]*huma.Param{
		nodepool.NodeRefusalHeader: {Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{nodepool.RefusalAtCapacity, nodepool.RefusalStorageFull}},
			Description: "Why the node refused new work: at_capacity at the job cap it declares (SILO_NODE_MAX_TRANSCODES), storage_full at the prepared-download budget it declares. Absent on every other 503, a draining refusal included; a caller that does not know it treats the 503 as a draining refusal."},
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
