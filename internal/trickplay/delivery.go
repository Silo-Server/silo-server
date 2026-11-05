package trickplay

import (
	"context"
	"fmt"
	"time"
)

const (
	// deliveryRecheck spaces attempts to confirm one revision, long enough
	// for an endpoint's cached miss to expire, and so the background passes:
	// a pass sooner would find nothing new due.
	deliveryRecheck = 15 * time.Minute
	// deliveryBatch bounds the revisions one confirmation step claims.
	deliveryBatch = 64
)

// DeliveryChecker reports whether a client can fetch key through the external
// read endpoint now.
type DeliveryChecker interface {
	ObjectAvailable(ctx context.Context, key string) (bool, error)
}

// deliveryRecords is the Repository surface delivery confirmation uses.
type deliveryRecords interface {
	ClaimDeliveryChecks(ctx context.Context, storeIdentity, scope string, recheck time.Duration, limit int) ([]DeliveryCheck, error)
	RecordDelivery(ctx context.Context, fileID int, revision int64, storeIdentity, scope string) error
}

type externalDelivery struct {
	scope   string
	checker DeliveryChecker
	records deliveryRecords
}

// SetExternalDelivery has the service confirm that clients can fetch each
// published revision's sheets through an external read endpoint, which can
// lag behind a storage write: before publishing a new revision, and in the
// background for revisions published before the endpoint served them or
// under another scope. Scope names the endpoint configuration. Readers give
// out direct endpoint URLs only for a confirmed revision (see
// Reader.SetExternalDelivery). Call it before Start.
func (s *Service) SetExternalDelivery(scope string, checker DeliveryChecker) {
	records, ok := s.queue.(deliveryRecords)
	if !ok || scope == "" || checker == nil {
		return
	}
	s.delivery = &externalDelivery{scope: scope, checker: checker, records: records}
}

// delivered reports whether the endpoint serves fileID's revision now. It
// probes only the last sheet uploaded: an endpoint shows a generation's writes
// in about the order they were made, so the earlier sheets are served by
// then. One probe per revision keeps the check to a small share of the
// upload's own requests.
func (s *Service) delivered(ctx context.Context, fileID int, revision int64, sheets int) (bool, error) {
	if sheets <= 0 {
		return false, nil
	}
	served, err := s.delivery.checker.ObjectAvailable(ctx, SheetKey(fileID, revision, sheets-1))
	return served && err == nil, err
}

// confirmDelivery confirms published revisions until ctx ends, a pass at a
// time.
func (s *Service) confirmDelivery(ctx context.Context) {
	for {
		if err := s.confirmDeliveryPass(ctx); err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "trickplay delivery check failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(deliveryRecheck):
		}
	}
}

// confirmDeliveryPass confirms due revisions a batch at a time. It stops at a
// probe error or a batch that confirms nothing: an endpoint that is down or
// misconfigured would fail every probe of the backlog.
func (s *Service) confirmDeliveryPass(ctx context.Context) error {
	identity := s.store.Identity()
	for ctx.Err() == nil {
		due, err := s.delivery.records.ClaimDeliveryChecks(ctx, identity, s.delivery.scope, deliveryRecheck, deliveryBatch)
		if err != nil {
			return err
		}
		confirmed := 0
		for _, check := range due {
			ok, err := s.delivered(ctx, check.FileID, check.Revision, check.SheetCount)
			if err != nil {
				return fmt.Errorf("file %d: %w", check.FileID, err)
			}
			if !ok {
				continue
			}
			if err := s.delivery.records.RecordDelivery(ctx, check.FileID, check.Revision, identity, s.delivery.scope); err != nil {
				return err
			}
			confirmed++
		}
		if len(due) < deliveryBatch || confirmed == 0 {
			return nil
		}
	}
	return nil
}
