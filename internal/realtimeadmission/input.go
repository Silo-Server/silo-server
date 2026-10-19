package realtimeadmission

import (
	"maps"
	"time"

	"golang.org/x/time/rate"
)

type inputAccount struct {
	bucket   *rate.Limiter
	lastUsed time.Time
}

// InputShield is per socket. Allow runs after a bounded data-frame read and
// before decoding or shared work. A false result asks the handler to close only
// this socket with code 1008 and InputCloseReason, after at most one notice.
type InputShield struct {
	controller *Controller
	userID     int
	bucket     *rate.Limiter
}

// Socket does not create or replenish the persistent account bucket.
func (c *Controller) Socket(userID int) *InputShield {
	return &InputShield{
		controller: c, userID: userID,
		bucket: rate.NewLimiter(rate.Limit(c.config.socketRate), c.config.socketBurst),
	}
}

func (s *InputShield) Allow() bool {
	if s.userID <= 0 {
		return false
	}
	c := s.controller
	now := c.config.now()
	if !s.bucket.AllowN(now, 1) {
		return false // A denied socket never touches or spends account capacity.
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now = c.config.now() // Sample under the account lock so concurrent sockets cannot reorder time.
	if !now.Before(c.nextSweep) {
		maps.DeleteFunc(c.accounts, func(_ int, account *inputAccount) bool {
			return now.Sub(account.lastUsed) >= accountIdle
		})
		c.nextSweep = now.Add(time.Minute)
	}
	account := c.accounts[s.userID]
	if account == nil || now.Sub(account.lastUsed) >= accountIdle {
		account = &inputAccount{bucket: rate.NewLimiter(rate.Limit(c.config.accountRate), c.config.accountBurst)}
		c.accounts[s.userID] = account
	}
	account.lastUsed = now
	return account.bucket.AllowN(now, 1)
}
