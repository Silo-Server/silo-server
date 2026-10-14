package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type admission struct {
	ctx        context.Context
	until      time.Time
	generation uint64
	count      int
	stop       context.CancelFunc
}
type authorityResult struct {
	active     bool
	until      time.Time
	generation uint64
}
type authorityKey struct {
	workKey
	generation uint64
}
type authorityCheck struct {
	revision uint64
	users    int
}
type Service struct {
	options         Options
	mu              sync.Mutex
	admissions      map[workKey]admission
	authority       map[authorityKey]authorityResult
	authorityChecks map[authorityKey]*authorityCheck
	transports      workeridentity.TransportPool
}

func New(o Options) (*Service, error) {
	if len(o.PrivateKey) != ed25519.PrivateKeySize || o.Nodes == nil || o.Store == nil {
		return nil, ErrUnavailable
	}
	origin, err := workeridentity.ValidateOrigin(o.Origin, o.AllowHTTP)
	if err != nil {
		return nil, err
	}
	o.Origin = origin
	o.PrivateKey = append(ed25519.PrivateKey(nil), o.PrivateKey...)
	return &Service{
		options: o, admissions: map[workKey]admission{},
		authority: map[authorityKey]authorityResult{}, authorityChecks: map[authorityKey]*authorityCheck{},
	}, nil
}
func (s *Service) SignRequest(r *http.Request) error {
	out, err := s.PrepareRequest(r)
	if err == nil {
		*r = *out
	}
	return err
}
func (s *Service) WrapTransport(base http.RoundTripper) http.RoundTripper { return &transport{s, base} }

type transport struct {
	service *Service
	base    http.RoundTripper
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	out, err := t.service.PrepareRequest(r)
	if err != nil {
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if out.Header.Get(workeridentity.HeaderDomain) == workeridentity.DomainCoreCommand {
		base, err = t.service.transports.Get(base)
		if err != nil {
			_ = t.service.FinishRequest(out, nil, err)
			return nil, err
		}
	}
	response, err := base.RoundTrip(out)
	if finishErr := t.service.FinishRequest(out, response, err); finishErr != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, finishErr
	}
	return response, err
}
