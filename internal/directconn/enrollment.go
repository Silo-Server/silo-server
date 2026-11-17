package directconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
)

const (
	// enrollmentLease is how long one enrollment, and the ticket that names it,
	// stays valid. Clients renew at half of it.
	enrollmentLease = 10 * time.Minute
	// maxDirectDevicesPerAccount counts only unexpired leases. The store refuses
	// a new device over the cap instead of evicting an old one.
	maxDirectDevicesPerAccount = 32
	// revokeAttempts bounds how often a revocation reads an entry again after it
	// changed between the listing and the delete.
	revokeAttempts = 3
)

var (
	// errNoSession refuses an enrollment that names no issuing login session:
	// no revocation could select its entry.
	errNoSession = errors.New("direct enrollment needs the login session that issues it")
	// errEntryChanging ends a revocation whose entry kept changing.
	errEntryChanging = errors.New("enrolled device kept changing")
)

// devicePolicy is the opaque policy the coordination store keeps for a device.
type devicePolicy struct {
	ConnectionPath string `json:"connection_path"`
	ProfileID      string `json:"profile_id"`
	// SessionID is the login session that last enrolled or renewed the entry,
	// which owns it: its ticket names the session, and ending the session
	// withdraws the entry.
	SessionID string `json:"session_id"`
	// Audited is set once the enrollment audit row for the entry's node key has
	// committed. A renewal of the same key carries it forward; see Enroll.
	Audited bool `json:"audited,omitempty"`
}

// EnrollmentAudit records one audit row for the first enrollment of a node key
// for a device. It returns an error when the row could not be written, and Enroll
// then withdraws the entry it just made unless another request has changed it.
type EnrollmentAudit func(ctx context.Context, deviceID string, nodeKey protocol.NodeKey) error

// CoordinationConfig supplies the store and the facts that tickets, enrollment
// and the capability document need.
type CoordinationConfig struct {
	Store CoordinationStore
	// AuthSecret is the auth JWT secret; the ticket key derives from it.
	AuthSecret string
	Logger     *slog.Logger
	// Clock nil uses time.Now. The store, the coordination server and the
	// ticket checks all read it.
	Clock func() time.Time
	// OnRemoved runs with the node key of an entry that was withdrawn or replaced,
	// after the peer cache and the signaling streams have dropped it. Nil does
	// nothing.
	OnRemoved func(protocol.NodeKey)
	// Host reports the API host for the capability document.
	Host HostSource
	// TLSPin is the SPKI pin of the listener certificate.
	TLSPin string
	// ACKDecimation and ReceiveWindowMiB are the client-side knobs the capability
	// document passes on, already clamped.
	ACKDecimation    bool
	ReceiveWindowMiB int
	// Audit, when set, runs for every enrollment whose stored entry is not an
	// audited entry with the same node key; a renewal of an audited key does not
	// run it. Nil records nothing.
	Audit EnrollmentAudit
}

// Coordination enrolls devices, issues and checks their signaling tickets, and
// revokes enrollments. Its store is shared by every API replica.
type Coordination struct {
	cfg CoordinationConfig
	srv *server.Server
	// directory is the peer cache the terminating replica feeds its engine from.
	directory *server.Directory
	tickets   *ticketSigner
	log       *slog.Logger
	clock     func() time.Time
	// region carries the signaling streams of this replica's engine and devices.
	// It is built on first use, so a replica that never signals builds none.
	region regionSlot
	// draining refuses enrollment and marks the API host draining; stopping makes
	// every netmap answer at once. Shutdown sets them; see shutdown.go.
	draining, stopping atomic.Bool
}

// NewCoordination validates cfg and builds the coordination server over its store.
func NewCoordination(cfg CoordinationConfig) (*Coordination, error) {
	tickets, err := newTicketSigner(cfg.AuthSecret)
	if err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	srv, err := server.New(server.Options{Store: cfg.Store, Clock: cfg.Clock, Logger: cfg.Logger, MaxDevicesPerAccount: maxDirectDevicesPerAccount})
	if err != nil {
		return nil, fmt.Errorf("build coordination server: %w", err)
	}
	directory := server.NewDirectory(cfg.Store, server.DirectoryOptions{Clock: cfg.Clock})
	return &Coordination{cfg: cfg, srv: srv, directory: directory, tickets: tickets, log: cfg.Logger, clock: cfg.Clock}, nil
}

// EnrollRequest names the device an authenticated account enrolls.
type EnrollRequest struct {
	UserID    int
	ProfileID string
	// SessionID is the login session that makes the request. It becomes the
	// entry's owner, and the ticket names it.
	SessionID      string
	DeviceID       string
	NodeKey        protocol.NodeKey
	ConnectionPath string
}

// Enrollment is the ticket for one enrolled device and the end of its lease.
type Enrollment struct {
	Ticket    string
	ExpiresAt time.Time
}

// Enroll reserves or renews the device's lease and returns its ticket. It
// returns server.ErrDeviceLimit when the account has no free slot,
// server.ErrNodeKeyConflict when another device holds the node key, ErrDraining
// once the server has begun to shut down, an error for a request that names no
// login session, and an error wrapping
// server.ErrUnavailable when the store cannot answer, the read of the stored
// entry included. A new node key for a device that is already enrolled replaces
// the entry and drops the old key. When the config has an Audit, every
// enrollment that is not a renewal of an audited entry with the same key runs it
// before a ticket is minted; an audit error withdraws the entry this call wrote
// and is returned, wrapped.
func (c *Coordination) Enroll(ctx context.Context, req EnrollRequest) (Enrollment, error) {
	if c.draining.Load() {
		return Enrollment{}, ErrDraining
	}
	if req.SessionID == "" {
		return Enrollment{}, errNoSession
	}
	ref := deviceRef(req.UserID, req.DeviceID)
	// A stored entry that cannot be read is unknown, not absent: the store
	// reports server.ErrUnavailable and the enrollment is refused.
	stored, err := c.cfg.Store.Device(ctx, ref, c.clock())
	if err != nil && !errors.Is(err, server.ErrNotFound) {
		return Enrollment{}, fmt.Errorf("read enrolled device: %w", err)
	}
	previous := stored.NodeKey
	// The audit marker is carried only by a renewal of an audited entry with the
	// same key. A new key, a missing entry and an entry whose audit has not
	// committed (still pending, failed, or lost with its process) all audit.
	carried := previous == req.NodeKey && policyAudited(stored.Policy)
	written := devicePolicy{ConnectionPath: req.ConnectionPath, ProfileID: req.ProfileID, SessionID: req.SessionID, Audited: carried}
	policy := marshalPolicy(written)
	now := c.clock()
	// The ticket carries whole seconds, so the lease end is cut to match: the
	// ticket, the reported expiry and the stored lease then end together.
	expires := now.Add(enrollmentLease).Truncate(time.Second)
	replaced, err := c.srv.Enroll(ctx, ref, req.NodeKey, policy, expires)
	if err != nil {
		if errors.Is(err, server.ErrDeviceLimit) {
			c.log.WarnContext(ctx, "direct connections: device limit reached")
		}
		return Enrollment{}, err
	}
	if replaced {
		c.log.InfoContext(ctx, "direct connections: enrollment replaced")
		c.removed(previous, req.NodeKey)
	}
	// No ticket is minted for an unaudited entry before an audit row for its key
	// has committed, so every first enrollment has a record. An audit that cannot
	// be written ends the enrollment. Two overlapping requests may both audit
	// the same key; a duplicate row is accepted.
	if c.cfg.Audit != nil && !carried {
		if err := c.cfg.Audit(ctx, req.DeviceID, req.NodeKey); err != nil {
			c.withdrawUnaudited(ctx, ref, req.NodeKey, policy)
			return Enrollment{}, fmt.Errorf("record enrollment audit: %w", err)
		}
		c.markAudited(ctx, ref, req.NodeKey, written)
	}
	ticket, err := c.tickets.mint(req.UserID, req.ProfileID, req.SessionID, req.DeviceID, req.NodeKey, now, expires)
	if err != nil {
		return Enrollment{}, err
	}
	return Enrollment{Ticket: ticket, ExpiresAt: expires}, nil
}

// markAudited sets the audit marker on the entry Enroll wrote, once its audit
// row has committed. It writes only while the entry still holds this request's
// key and policy, so it never overwrites another request's renewal. A marker
// that is not written costs one more audit row at the next renewal.
func (c *Coordination) markAudited(ctx context.Context, ref server.DeviceRef, key protocol.NodeKey, written devicePolicy) {
	audited := written
	audited.Audited = true
	if _, err := c.cfg.Store.SwapDevicePolicy(ctx, ref, key, marshalPolicy(written), marshalPolicy(audited), c.clock()); err != nil {
		c.log.WarnContext(ctx, "direct connections: marking an audited enrollment failed", "error", err)
	}
}

// withdrawUnaudited withdraws the entry Enroll just made when its audit row
// could not be written, but only while the entry still holds this request's key
// and unaudited policy: an entry that a later request replaced, renewed or
// audited stays. It runs past a canceled request, and a failure is logged: the
// entry then lapses at its lease end, and no client holds its ticket.
func (c *Coordination) withdrawUnaudited(ctx context.Context, ref server.DeviceRef, key protocol.NodeKey, policy []byte) {
	withdrawCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	removed, err := c.cfg.Store.DeleteDeviceIf(withdrawCtx, ref, key, policy, c.clock())
	if err != nil {
		c.log.WarnContext(ctx, "direct connections: withdrawing an unaudited enrollment failed", "error", err)
		return
	}
	if removed {
		c.srv.Wake(ref)
		c.removed(key, protocol.NodeKey{})
	}
}

// marshalPolicy encodes a device policy; three strings and a bool always encode.
func marshalPolicy(p devicePolicy) []byte {
	b, _ := json.Marshal(p)
	return b
}

// policyAudited reports whether stored policy bytes carry the audit marker.
// Unreadable bytes are unaudited.
func policyAudited(policy []byte) bool {
	var p devicePolicy
	return json.Unmarshal(policy, &p) == nil && p.Audited
}

// policyOwnedBy reports whether stored policy bytes record sessionID as the
// entry's issuing login session. Unreadable bytes and an empty sessionID are
// owned by no session.
func policyOwnedBy(policy []byte, sessionID string) bool {
	var p devicePolicy
	return sessionID != "" && json.Unmarshal(policy, &p) == nil && p.SessionID == sessionID
}

// removed drops an entry's old node key from the peer cache, closes its
// signaling streams and tells the caller that it is gone. A replacement that
// keeps the same key leaves nothing to remove.
func (c *Coordination) removed(old, current protocol.NodeKey) {
	if old.IsZero() || old == current {
		return
	}
	c.directory.Invalidate(old)
	if r := c.region.peek(); r != nil {
		r.Disconnect(old)
	}
	if c.cfg.OnRemoved != nil {
		c.cfg.OnRemoved(old)
	}
}

// Withdraw deletes the device's entry. Withdrawing a device with no entry
// succeeds.
func (c *Coordination) Withdraw(ctx context.Context, userID int, deviceID string) error {
	ref := deviceRef(userID, deviceID)
	var key protocol.NodeKey
	if d, err := c.cfg.Store.Device(ctx, ref, c.clock()); err == nil {
		key = d.NodeKey
	}
	if err := c.srv.Withdraw(ctx, ref); err != nil {
		return err
	}
	c.removed(key, protocol.NodeKey{})
	return nil
}

// Authorize checks a ticket and the store entry it names. The entry must also
// record the ticket's login session as its owner, so a ticket stops authorizing
// once another session has enrolled or renewed the same device. Every refusal
// returns ErrInvalidTicket; a store outage returns an error wrapping
// server.ErrUnavailable.
func (c *Coordination) Authorize(ctx context.Context, ticket string) (server.Principal, server.Device, error) {
	principal, sessionID, err := c.tickets.principal(ticket, c.clock())
	if err != nil {
		return server.Principal{}, server.Device{}, err
	}
	device, err := c.srv.Authorize(ctx, principal)
	if errors.Is(err, server.ErrUnauthorized) {
		return server.Principal{}, server.Device{}, ErrInvalidTicket
	}
	if err != nil {
		return server.Principal{}, server.Device{}, err
	}
	if !policyOwnedBy(device.Policy, sessionID) {
		return server.Principal{}, server.Device{}, ErrInvalidTicket
	}
	return principal, device, nil
}

// Established reports whether d, an entry Authorize returned, holds a live
// registration: its lease and its presence are both unexpired at the
// coordination clock. That is the rule by which the module's peer directory
// admits a device to the engine's peer set (devicePeer in
// coordination/server/directory.go), so an established device is one the
// engine is serving now. A device idle past its presence is not established.
func (c *Coordination) Established(d server.Device) bool {
	now := c.clock()
	return d.Registration != nil && now.Before(d.LeaseExpires) && now.Before(d.PresenceExpires)
}

// RevokeSession withdraws every entry of the account that the login session last
// enrolled or renewed, whatever device ID the entries name. An entry that another
// session has renewed since stays. An empty session ID selects nothing.
func (c *Coordination) RevokeSession(ctx context.Context, userID int, sessionID string) {
	if sessionID == "" {
		return
	}
	c.revokeOwned(ctx, userID, func(d server.Device) bool { return policyOwnedBy(d.Policy, sessionID) })
}

// RevokeProfileDevice withdraws the account's entry for one device when it
// belongs to the given profile.
func (c *Coordination) RevokeProfileDevice(ctx context.Context, userID int, deviceID, profileID string) {
	if deviceID == "" {
		return
	}
	c.revoke(ctx, userID, func(d server.Device) bool {
		var policy devicePolicy
		return string(d.Ref.Device) == deviceID && json.Unmarshal(d.Policy, &policy) == nil && policy.ProfileID == profileID
	})
}

// RevokeAccount withdraws every entry the account holds.
func (c *Coordination) RevokeAccount(ctx context.Context, userID int) {
	c.revoke(ctx, userID, func(server.Device) bool { return true })
}

// RevokeAccountExceptSession withdraws every entry the account holds except the
// one for keptDeviceID that keptSessionID last enrolled or renewed. An empty
// keptSessionID or keptDeviceID keeps nothing, and so does an entry whose policy
// records no readable issuer.
func (c *Coordination) RevokeAccountExceptSession(ctx context.Context, userID int, keptSessionID, keptDeviceID string) {
	c.revokeOwned(ctx, userID, func(d server.Device) bool {
		kept := keptDeviceID != "" && string(d.Ref.Device) == keptDeviceID && policyOwnedBy(d.Policy, keptSessionID)
		return !kept
	})
}

// RevokeProfile withdraws every entry of the account that records the profile.
// An empty profile ID selects nothing.
func (c *Coordination) RevokeProfile(ctx context.Context, userID int, profileID string) {
	if profileID == "" {
		return
	}
	c.revoke(ctx, userID, func(d server.Device) bool {
		var policy devicePolicy
		return json.Unmarshal(d.Policy, &policy) == nil && policy.ProfileID == profileID
	})
}

// revoke withdraws each selected entry of the account at once.
func (c *Coordination) revoke(ctx context.Context, userID int, selected func(server.Device) bool) {
	c.revokeEach(ctx, userID, func(d server.Device) error {
		if !selected(d) {
			return nil
		}
		if err := c.srv.Withdraw(ctx, d.Ref); err != nil {
			return err
		}
		c.removed(d.NodeKey, protocol.NodeKey{})
		return nil
	})
}

// revokeOwned withdraws each selected entry of the account while it is still the
// entry that was listed. A request can renew, replace or audit an entry between
// the listing and the delete, and the selection judged the listed bytes: when
// the entry changed, it is read again and judged again, so an entry that a
// surviving session has renewed stays and one the ending session still owns goes.
func (c *Coordination) revokeOwned(ctx context.Context, userID int, selected func(server.Device) bool) {
	c.revokeEach(ctx, userID, func(d server.Device) error {
		for range revokeAttempts {
			if !selected(d) {
				return nil
			}
			removed, err := c.cfg.Store.DeleteDeviceIf(ctx, d.Ref, d.NodeKey, d.Policy, c.clock())
			if err != nil {
				return fmt.Errorf("withdraw enrolled device: %w", err)
			}
			if removed {
				c.srv.Wake(d.Ref)
				c.removed(d.NodeKey, protocol.NodeKey{})
				return nil
			}
			d, err = c.cfg.Store.Device(ctx, d.Ref, c.clock())
			if errors.Is(err, server.ErrNotFound) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("read changed enrolled device: %w", err)
			}
		}
		return errEntryChanging
	})
}

// revokeEach lists the account's entries from the store, never from a peer
// cache, so an enrolled device that never registered is revoked too, and runs
// withdraw for each. A failure is logged once and ends the call: the entry then
// lapses at its lease end.
func (c *Coordination) revokeEach(ctx context.Context, userID int, withdraw func(server.Device) error) {
	devices, err := c.cfg.Store.ListDevices(ctx, accountID(userID), c.clock())
	for i := 0; err == nil && i < len(devices); i++ {
		err = withdraw(devices[i])
	}
	if err != nil {
		reason := "store_error"
		switch {
		case errors.Is(err, server.ErrUnavailable):
			reason = "state_store_unavailable"
		case errors.Is(err, errEntryChanging):
			reason = "entry_changing"
		}
		c.log.WarnContext(ctx, "direct connections: revocation failed", "reason", reason)
	}
}
