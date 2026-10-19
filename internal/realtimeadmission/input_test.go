package realtimeadmission

import (
	"testing"
	"time"
)

func tinyShieldController() (*Controller, *time.Duration) {
	c, elapsed := tinyController()
	c.config.socketRate, c.config.socketBurst = 1, 1
	c.config.accountRate, c.config.accountBurst = 1, 2
	return c, elapsed
}

func TestInputShieldSocketDenialNeverSpendsAccount(t *testing.T) {
	c, elapsed := tinyShieldController()
	a := c.Socket(1)
	if !a.Allow() || a.Allow() {
		t.Fatal("socket did not enforce its tiny allowance")
	}
	if !c.Socket(1).Allow() {
		t.Fatal("socket refusal spent the account's remaining permit")
	}
	if c.Socket(1).Allow() {
		t.Fatal("reconnect replenished the persistent account bucket")
	}
	if !c.Socket(2).Allow() {
		t.Fatal("one account's shield denied a different account")
	}
	*elapsed += time.Second
	if !a.Allow() || c.Socket(1).Allow() {
		t.Fatal("socket/account buckets did not refill by elapsed time")
	}
}

func TestInputShieldIdleEvictionAndOldSocketRecovery(t *testing.T) {
	c, elapsed := tinyShieldController()
	a := c.Socket(1)
	if !a.Allow() {
		t.Fatal("initial permit refused")
	}
	account := c.accounts[1]
	*elapsed = accountIdle
	if !c.Socket(2).Allow() {
		t.Fatal("other account refused")
	}
	if _, exists := c.accounts[1]; exists {
		t.Fatal("idle account not evicted after ten minutes")
	}
	if !a.Allow() || c.accounts[1] == account {
		t.Fatal("old socket did not use the current account bucket after eviction")
	}
	if c.Socket(0).Allow() || c.Socket(-1).Allow() {
		t.Fatal("unauthenticated identity got shield capacity")
	}
}
