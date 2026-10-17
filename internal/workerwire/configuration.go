package workerwire

import (
	"errors"
	"strings"
)

// Validate rejects settings that cannot safely be applied as one snapshot.
// Retention zero keeps the existing disabled-pruning meaning. The epoch is an
// opaque core-derived rotation identifier, never a signing key or credential.
func (c ExecutionConfig) Validate() error {
	if strings.TrimSpace(c.Revision) == "" || len(c.Revision) > 160 || c.SegmentRetentionSeconds < 0 || c.SegmentRetentionSeconds > 7*24*60*60 || c.SubtitleSyncCapacity < 1 || c.SubtitleSyncCapacity > 1024 {
		return errors.New("invalid worker execution settings")
	}
	if strings.TrimSpace(c.OutputAuthorizationEpoch) == "" || len(c.OutputAuthorizationEpoch) > 256 {
		return errors.New("worker output authorization epoch missing or invalid")
	}
	return nil
}
