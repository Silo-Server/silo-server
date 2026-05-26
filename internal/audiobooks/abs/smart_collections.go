package abs

import (
	"context"
	"encoding/json"
	"time"
)

// SmartCollectionStore is the narrow slice of abs_smart_collections
// the handlers need.
type SmartCollectionStore interface {
	ListUserSmartCollections(ctx context.Context, userID, profileID string) ([]SmartCollection, error)
	GetSmartCollection(ctx context.Context, id string) (SmartCollection, error)
	CreateSmartCollection(ctx context.Context, c SmartCollection) error
	UpdateSmartCollection(ctx context.Context, c SmartCollection) error
	DeleteSmartCollection(ctx context.Context, id string) error
}

// SmartCollection mirrors an abs_smart_collections row. QueryDef holds
// the raw JSONB bytes (decoded only on the /items route where rules
// are evaluated).
type SmartCollection struct {
	ID          string
	UserID      string
	ProfileID   string
	Name        string
	Description string
	Color       string
	IsPublic    bool
	IsPinned    bool
	QueryDef    []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// smartCollectionToABS shapes a SmartCollection in the ABS wire format.
// QueryDef is emitted as a nested JSON object (decoded once at
// serialisation time). Empty/nil QueryDef becomes the empty object.
func smartCollectionToABS(c SmartCollection) map[string]any {
	var qd any = map[string]any{}
	if len(c.QueryDef) > 0 {
		var decoded any
		if err := json.Unmarshal(c.QueryDef, &decoded); err == nil {
			qd = decoded
		}
	}
	return map[string]any{
		"id":          c.ID,
		"userId":      c.UserID,
		"name":        c.Name,
		"description": c.Description,
		"color":       c.Color,
		"isPublic":    c.IsPublic,
		"isPinned":    c.IsPinned,
		"queryDef":    qd,
		"createdAt":   c.CreatedAt.UnixMilli(),
		"updatedAt":   c.UpdatedAt.UnixMilli(),
	}
}
