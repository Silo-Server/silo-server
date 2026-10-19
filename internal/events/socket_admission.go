package events

import (
	"errors"
	"sync"
)

// ClientSocketMode selects whether account and process limits refuse reservations.
type ClientSocketMode string

const (
	// ClientSocketObserve records exceeded thresholds but does not bound resources.
	ClientSocketObserve ClientSocketMode = "observe"
	ClientSocketEnforce ClientSocketMode = "enforce"
)

// ClientSocketRefusal is a bounded refusal reason; the empty value means admitted.
type ClientSocketRefusal string

const (
	ClientSocketAccountLimit ClientSocketRefusal = "account"
	ClientSocketProcessLimit ClientSocketRefusal = "process"
	ClientSocketDraining     ClientSocketRefusal = "draining"
	ClientSocketUnavailable  ClientSocketRefusal = "unavailable"
)

// ClientSocketAdmission accounts for pending and active client sockets in one
// process. All client surfaces must share one instance and pass the owning
// account ID, regardless of profile, session, credential, or device. Trusted
// internal Hub.Subscribe callers remain independent of this allowance.
//
// Construct with NewClientSocketAdmission; the zero value refuses admission.
// An admission must not be copied.
type ClientSocketAdmission struct {
	mu         sync.Mutex
	perAccount int
	perProcess int
	mode       ClientSocketMode
	accounts   map[int]int
	total      int
	draining   bool
	done       chan struct{}
	metrics    clientSocketMetrics
}

// NewClientSocketAdmission creates independent process-local accounting. It
// starts no background work and registers no metrics globally; the owner must
// register this collector with its Prometheus registerer once at startup.
func NewClientSocketAdmission(perAccount, perProcess int, mode ClientSocketMode) (*ClientSocketAdmission, error) {
	if perAccount <= 0 || perProcess <= 0 {
		return nil, errors.New("client socket admission limits must be positive")
	}
	if mode != ClientSocketObserve && mode != ClientSocketEnforce {
		return nil, errors.New("invalid client socket admission mode")
	}
	return &ClientSocketAdmission{
		perAccount: perAccount,
		perProcess: perProcess,
		mode:       mode,
		accounts:   make(map[int]int),
		done:       make(chan struct{}),
	}, nil
}

// ClientSocketReservation holds capacity from before upgrade through connection
// close and unsubscription. Call Release on every failed or completed attempt.
// A reservation must not be copied.
type ClientSocketReservation struct {
	admission *ClientSocketAdmission
	accountID int
	released  bool // protected by admission.mu
}

// Reserve counts pending work immediately. The caller's draining flag applies
// to this attempt; BeginDrain permanently stops admission. Draining is a real
// refusal in either mode. A nil admission has no collector to record its refusal.
func (a *ClientSocketAdmission) Reserve(accountID int, draining bool) (*ClientSocketReservation, ClientSocketRefusal) {
	if a == nil {
		return nil, ClientSocketUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if draining || a.draining {
		a.metrics.refusals[clientSocketDrainingReason]++
		return nil, ClientSocketDraining
	}
	if a.done == nil || accountID <= 0 {
		a.metrics.refusals[clientSocketUnavailableReason]++
		return nil, ClientSocketUnavailable
	}
	accountFull := a.accounts[accountID] >= a.perAccount
	processFull := a.total >= a.perProcess
	if a.mode == ClientSocketObserve {
		// Count both dimensions when both are exceeded, so neither pilot gate
		// can be hidden by the other. Observation offers no resource protection.
		if accountFull {
			a.metrics.wouldRefuse[clientSocketAccountReason]++
		}
		if processFull {
			a.metrics.wouldRefuse[clientSocketProcessReason]++
		}
	}
	// Process capacity takes precedence when both limits are full.
	if a.mode == ClientSocketEnforce && processFull {
		a.metrics.refusals[clientSocketProcessReason]++
		return nil, ClientSocketProcessLimit
	}
	if a.mode == ClientSocketEnforce && accountFull {
		a.metrics.refusals[clientSocketAccountReason]++
		return nil, ClientSocketAccountLimit
	}
	a.total++
	a.accounts[accountID]++
	a.metrics.observeAdmission(a.total, a.accounts[accountID])
	return &ClientSocketReservation{admission: a, accountID: accountID}, ""
}

// RecordDrainingRefusal counts a pre-admission shutdown refusal, without
// reserving capacity or consuming a client's single-use credential.
func (a *ClientSocketAdmission) RecordDrainingRefusal() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.metrics.refusals[clientSocketDrainingReason]++
}

// Release returns capacity exactly once, including when called concurrently.
// The caller must close the socket and unsubscribe before releasing it.
func (r *ClientSocketReservation) Release() {
	if r == nil || r.admission == nil {
		return
	}
	a := r.admission
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.released {
		return
	}
	r.released = true
	a.total--
	a.accounts[r.accountID]--
	if a.accounts[r.accountID] == 0 {
		delete(a.accounts, r.accountID)
	}
	if a.draining && a.total == 0 {
		close(a.done)
	}
}

// BeginDrain permanently refuses new reservations. Done closes after the last
// reservation releases, or immediately if none remain. Repeated calls are safe.
// This does not close sockets; the owner must arrange their teardown.
func (a *ClientSocketAdmission) BeginDrain() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done == nil || a.draining {
		return
	}
	a.draining = true
	if a.total == 0 {
		close(a.done)
	}
}

// Done is stable for the life of this admission, for one startup shutdown-work
// registration. Closure proves all reservations were released, not that handler
// goroutines exited. A nil or uninitialized admission returns a nil channel.
func (a *ClientSocketAdmission) Done() <-chan struct{} {
	if a == nil {
		return nil
	}
	return a.done
}

// Occupancy returns the total pending and active reservations in this process.
func (a *ClientSocketAdmission) Occupancy() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.total
}
