package directconn

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// SignalingTicketHeader carries the ticket on every coordination request.
	SignalingTicketHeader = "Silo-Signaling-Ticket"

	signalingTicketType = "direct_signal"
	ticketKeyLabel      = "silo/direct-signal/v1"
)

// ErrInvalidTicket is the one error every ticket refusal returns, so a caller
// cannot tell which check failed. A store outage is not a refusal and keeps
// its own error.
var ErrInvalidTicket = errors.New("invalid signaling ticket")

type ticketClaims struct {
	Type      string `json:"typ"`
	UserID    int    `json:"uid"`
	ProfileID string `json:"pid"`
	// SessionID is the login session that enrolled the entry the ticket names.
	SessionID string `json:"sid"`
	DeviceID  string `json:"did"`
	NodeKey   string `json:"nk"`
	jwt.RegisteredClaims
}

// ticketSigner signs and verifies signaling tickets with a key derived from the
// auth JWT secret, so no other token signed with that secret verifies as one.
type ticketSigner struct {
	key []byte
}

func newTicketSigner(secret string) (*ticketSigner, error) {
	if secret == "" {
		return nil, errors.New("direct signaling tickets need the auth JWT secret")
	}
	key, err := hkdf.Key(sha256.New, []byte(secret), nil, ticketKeyLabel, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("derive signaling ticket key: %w", err)
	}
	return &ticketSigner{key: key}, nil
}

func (s *ticketSigner) mint(uid int, profileID, sessionID, deviceID string, nodeKey protocol.NodeKey, now, expires time.Time) (string, error) {
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, ticketClaims{
		Type: signalingTicketType, UserID: uid, ProfileID: profileID, SessionID: sessionID, DeviceID: deviceID, NodeKey: nodeKey.String(),
		RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expires)},
	}).SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("sign signaling ticket: %w", err)
	}
	return signed, nil
}

// principal checks the signature, the expiry and the token type, and returns the
// principal and the login session the ticket names. It does not check the store
// entry.
func (s *ticketSigner) principal(token string, now time.Time) (server.Principal, string, error) {
	claims := ticketClaims{}
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return s.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || claims.Type != signalingTicketType || claims.UserID < 1 || claims.SessionID == "" || claims.DeviceID == "" || claims.ExpiresAt == nil {
		return server.Principal{}, "", ErrInvalidTicket
	}
	var nodeKey protocol.NodeKey
	if err := nodeKey.UnmarshalText([]byte(claims.NodeKey)); err != nil || nodeKey.IsZero() {
		return server.Principal{}, "", ErrInvalidTicket
	}
	return server.Principal{Ref: deviceRef(claims.UserID, claims.DeviceID), NodeKey: nodeKey, Expires: claims.ExpiresAt.Time}, claims.SessionID, nil
}

func accountID(userID int) server.AccountID { return server.AccountID(strconv.Itoa(userID)) }

func deviceRef(userID int, deviceID string) server.DeviceRef {
	return server.DeviceRef{Account: accountID(userID), Device: server.DeviceID(deviceID)}
}
