package push

import (
	"context"
	"time"
)

// Transport identifiers (also stored in user_devices.push_transport and
// push_deliveries.transport).
const (
	TransportAPNs    = "apns"
	TransportFCM     = "fcm"
	TransportWebPush = "webpush"
)

// Delivery statuses.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusDead    = "dead"
)

// Device is a push-eligible device row.
type Device struct {
	UserID    int
	ProfileID string
	DeviceID  string
	Transport string
	Token     string // bare token (apns/fcm) or JSON subscription (webpush)
}

// Delivery is one queued push for one device.
type Delivery struct {
	ID             int64
	NotificationID int64
	UserID         int
	ProfileID      string
	DeviceID       string
	Transport      string
	Status         string
	Attempts       int
	NotBefore      time.Time
}

// Payload is the content sent to a device. Built from a notification row at
// send time so edits/expiry are reflected.
type Payload struct {
	NotificationID int64
	Title          string
	Body           string
	Link           string
	Category       string
}

// SendResult classifies a transport attempt.
type SendResult int

const (
	// ResultSent: delivered (or accepted by the provider).
	ResultSent SendResult = iota
	// ResultSoftFail: retryable (timeout, 5xx, rate-limit).
	ResultSoftFail
	// ResultDead: token is permanently invalid; prune it.
	ResultDead
)

// Transport delivers a payload to one device token. Implementations are
// selected by Device.Transport.
type Transport interface {
	// Name returns the transport id (TransportAPNs/FCM/WebPush).
	Name() string
	// Configured reports whether provider credentials are present.
	Configured() bool
	// Send delivers payload to token. err is for logging only; the SendResult
	// drives queue state. retryAfter (>0) parks the transport when rate-limited.
	Send(ctx context.Context, token string, payload Payload) (res SendResult, retryAfter time.Duration, err error)
}
