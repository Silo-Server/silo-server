package planstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/playback"
)

var _ playback.InitialReconciliationStoreV3 = (*Postgres)(nil)

// ListInitialReconciliation inventories exact retained intents without granting
// execution, publication or source-write authority. Reconciliation must recheck
// each binding using the existing CAS methods outside this read transaction.
// afterAttemptID is exclusive; pass the final returned attempt ID for the next
// page. A new sweep starts with an empty key so newly eligible older rows recur.
func (s *Postgres) ListInitialReconciliation(ctx context.Context, afterAttemptID string, limit int) ([]playback.InitialActivationV3, error) {
	if limit < 1 || limit > 100 {
		return nil, playback.ErrInitialActivationInvalidV3
	}
	rows, err := s.db.Query(ctx, `SELECT a.user_id,a.control_activation FROM playback_v3_attempts a
 LEFT JOIN playback_source_registrations r ON r.user_id=a.user_id
 WHERE a.playback_attempt_id>$1 AND a.control_activation IS NOT NULL
 AND (a.control_activation->>'phase' IN ('aborting','stopping') OR
 (a.control_activation->>'phase' IN ('pending','installed','activated') AND
 (a.control_lease_expires_at<=clock_timestamp() OR a.expires_at<=clock_timestamp() OR r.user_id IS NULL OR r.admission_state<>'admitting'
 OR r.admission_id::text IS DISTINCT FROM a.control_activation->'binding'->>'admission_id'
 OR r.backend IS DISTINCT FROM a.control_activation->'binding'->'source'->>'Backend'
 OR r.source_id::text IS DISTINCT FROM a.control_activation->'binding'->'source'->>'SourceID'
 OR r.selection_generation::text IS DISTINCT FROM a.control_activation->'binding'->'source'->>'SelectionGeneration')))
 ORDER BY a.playback_attempt_id ASC LIMIT $2`, afterAttemptID, limit)
	if err != nil {
		return nil, fmt.Errorf("list initial reconciliation: %w", err)
	}
	defer rows.Close()
	result := make([]playback.InitialActivationV3, 0, limit)
	for rows.Next() {
		var data []byte
		var accountID int
		if err := rows.Scan(&accountID, &data); err != nil {
			return nil, err
		}
		if len(data) > initialActivationDocumentLimit {
			return nil, playback.ErrInitialActivationInvalidV3
		}
		var state playback.InitialActivationV3
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, err
		}
		if err := validateStoredInitialActivation(state); err != nil {
			return nil, err
		}
		if state.Binding.Source.AccountID != accountID {
			return nil, playback.ErrInitialActivationConflictV3
		}
		result = append(result, state)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
