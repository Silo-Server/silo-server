package notifications

import (
	"context"
	"errors"
)

var errDeliveryFailed = errors.New("delivery_failed")

// deliveryObservationError never forwards recipient/URL-bearing sender errors.
func deliveryObservationError(ctx context.Context, ok bool) error {
	if ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errDeliveryFailed
}
