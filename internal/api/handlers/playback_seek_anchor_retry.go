package handlers

import (
	"strconv"
	"sync"
	"time"
)

const (
	// copySeekAnchorRefusalWindow is how long a retryable seek-anchor refusal
	// names the next start for the same profile and file as the viewer's retry.
	// The retry card is the one affordance the viewer is offered, and the client
	// re-issues a fresh start (a new playback attempt and session) from the
	// position continuity left on screen. There is no wire field to carry "this
	// is the retry", so the server has to remember the refusal it just answered;
	// a couple of minutes covers the time a viewer takes to press the button
	// without letting an old refusal change a later, unrelated start.
	copySeekAnchorRefusalWindow = 2 * time.Minute
	// copySeekAnchorRefusalLimit bounds the tracker. Entries expire on read and
	// record, so a large installation cannot accumulate one per refused seek
	// forever.
	copySeekAnchorRefusalLimit = 256
)

// copySeekAnchorRefusalTracker remembers which (profile, requested file) pairs
// were just refused a copy-video seek anchor with a retryable verdict. The next
// start for that pair is the viewer's retry: it gets the extended probe window
// and, if the probe still cannot answer, the keyframe-origin fallback rather
// than the same card a second time.
type copySeekAnchorRefusalTracker struct {
	mu    sync.Mutex
	marks map[string]time.Time
	now   func() time.Time
}

func (t *copySeekAnchorRefusalTracker) clock() time.Time {
	if t != nil && t.now != nil {
		return t.now()
	}
	return time.Now()
}

// record notes that a retryable seek-anchor refusal was answered for key.
func (t *copySeekAnchorRefusalTracker) record(key string) {
	if t == nil || key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.marks == nil {
		t.marks = make(map[string]time.Time)
	}
	now := t.clock()
	for mark, observedAt := range t.marks {
		if now.Sub(observedAt) >= copySeekAnchorRefusalWindow {
			delete(t.marks, mark)
		}
	}
	if len(t.marks) >= copySeekAnchorRefusalLimit {
		for mark := range t.marks {
			delete(t.marks, mark)
			break
		}
	}
	t.marks[key] = now
}

// pending reports whether key has an unexpired refusal. It does not consume the
// mark: a retry that fails before it reaches the timeline (a provider listing
// outage, for example) leaves it in place for the next attempt, and a successful
// playback clears it explicitly.
func (t *copySeekAnchorRefusalTracker) pending(key string) bool {
	if t == nil || key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	observedAt, ok := t.marks[key]
	if !ok {
		return false
	}
	if t.clock().Sub(observedAt) >= copySeekAnchorRefusalWindow {
		delete(t.marks, key)
		return false
	}
	return true
}

// clear drops the mark once a start has produced a timeline. Until then every
// attempt for the same pair stays in the retry state, so a retry can never fall
// back into the fast first-attempt refusal it is answering.
func (t *copySeekAnchorRefusalTracker) clear(key string) {
	if t == nil || key == "" {
		return
	}
	t.mu.Lock()
	delete(t.marks, key)
	t.mu.Unlock()
}

// seekAnchorRefusalKey names one profile's refused copy-video seek on one
// requested file. The requested row is stable across the retry: the client
// re-issues its start with the plan's requested_media_file_id, while the
// effective row can move with a candidate resolve. The position is deliberately
// not part of the key: the refusal card keeps the surviving playhead, so the
// retry starts where continuity left it, not at the target that never played.
func seekAnchorRefusalKey(profileID string, requestedFileID int) string {
	if requestedFileID <= 0 {
		return ""
	}
	return profileID + "\x00" + strconv.Itoa(requestedFileID)
}

// recordCopySeekAnchorRefusalV3 remembers a retryable copy-video seek-anchor
// refusal so the viewer's next start for the same file is treated as the retry
// it is. Only the transcode_start_failed seek card is recorded: other failures
// have their own remedies and must not inherit this one.
func (h *PlaybackHandler) recordCopySeekAnchorRefusalV3(profileID string, requestedFileID int, failure *transportErrorV3) {
	if h == nil || h.copySeekAnchorRefusals == nil {
		return
	}
	if failure == nil || !failure.retryable || !failure.seekFailure || failure.reason != transcodeStartFailedReasonV3 {
		return
	}
	h.copySeekAnchorRefusals.record(seekAnchorRefusalKey(profileID, requestedFileID))
}
