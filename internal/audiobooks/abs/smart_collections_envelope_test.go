package abs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSmartCollectionEnvelope_HasRequiredKeys(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	out := smartCollectionToABS(SmartCollection{
		ID:          "01HSC",
		UserID:      "1",
		Name:        "x",
		Description: "",
		Color:       "",
		IsPublic:    false,
		IsPinned:    false,
		QueryDef:    []byte(`{"match":"all","groups":[]}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	body, _ := json.Marshal(out)
	js := string(body)
	for _, key := range []string{
		`"id":`, `"userId":`, `"name":`, `"description":`, `"color":`,
		`"isPublic":`, `"isPinned":`, `"queryDef":`, `"createdAt":`, `"updatedAt":`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("envelope missing %s; got %s", key, js)
		}
	}
	if _, ok := out["queryDef"].(map[string]any); !ok {
		t.Errorf("queryDef should marshal as nested object, got %T: %v", out["queryDef"], out["queryDef"])
	}
}

func TestSmartCollectionEnvelope_EmptyQueryDef(t *testing.T) {
	out := smartCollectionToABS(SmartCollection{
		ID: "x", UserID: "1", Name: "y",
		QueryDef:  nil,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if _, has := out["queryDef"]; !has {
		t.Errorf("queryDef missing when stored bytes are nil")
	}
}
