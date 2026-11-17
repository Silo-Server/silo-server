package directconn

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrDraining is returned by Enroll once the server has begun to shut down.
var ErrDraining = errors.New("direct connections: server is shutting down")

// hostDeleteTimeout bounds the removal of the API host entry at shutdown. It runs
// under its own deadline, so a drain that used the whole shutdown budget still
// removes the entry instead of leaving it to lapse after the presence period.
const hostDeleteTimeout = storeTimeout

// drainReserve is the part of the shutdown budget that the HTTP/3 drain leaves
// for the steps after it: closing the region, removing the host entry, closing
// the engine and releasing the engine lease. Streams still open when the budget
// minus this reserve has passed are closed.
const drainReserve = 5 * time.Second

// BeginDrain is the first shutdown step, taken when readiness fails. Enrollment
// answers ErrDraining and the capability document marks the API host draining.
// Everything else serves as normal until Shutdown, so devices keep their
// connections while load balancers route new requests elsewhere.
func (s *Service) BeginDrain() {
	if c, _ := s.coordination(); c != nil {
		c.draining.Store(true)
	}
}

// beginStop makes every netmap answer at once and releases the ones that are
// held, so no request waits out its hold while the HTTP servers drain. It is
// safe to call more than once.
func (s *Service) beginStop() {
	if c, _ := s.coordination(); c != nil {
		c.draining.Store(true)
		c.stopping.Store(true)
		c.srv.WakeAll()
	}
}

// closeForShutdown runs after the HTTP/3 server has drained: it closes the region
// and every signaling stream, removes the API host entry when this replica ran
// the engine, and wakes any netmap that began to hold in the meantime. Another
// replica's host entry is never removed, which is why a replica that never ran
// the engine passes ranEngine false. A removal the store refuses because another
// process now holds the engine lease leaves that successor's entry; it is
// logged, not a shutdown failure.
func (c *Coordination) closeForShutdown(ranEngine bool) error {
	err := c.Close()
	if ranEngine {
		ctx, cancel := context.WithTimeout(context.Background(), hostDeleteTimeout)
		defer cancel()
		switch deleteErr := c.srv.DeleteHost(ctx, coordinationHostLabel); {
		case deleteErr == nil:
		case errors.Is(deleteErr, errNotLeaseHolder):
			c.log.InfoContext(ctx, "direct connections: API host entry left to the engine lease's new holder", "component", "directconn")
		default:
			err = errors.Join(err, fmt.Errorf("remove the API host entry: %w", deleteErr))
		}
	}
	c.srv.WakeAll()
	return err
}
