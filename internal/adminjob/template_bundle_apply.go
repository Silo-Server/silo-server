package adminjob

import (
	"encoding/json"
	"fmt"
)

const JobTypeTemplateBundleApply = "template_bundle_apply"

type TemplateBundleApplyRequest struct {
	BundleID       string                              `json:"bundle_id"`
	LibraryIDs     []int                               `json:"library_ids"`
	DeleteExisting bool                                `json:"delete_existing"`
	Featured       *TemplateBundleApplyFeaturedRequest `json:"featured,omitempty"`
}

type TemplateBundleApplyFeaturedRequest struct {
	Home      *TemplateBundleApplyFeaturedHome `json:"home,omitempty"`
	Libraries map[int]string                   `json:"libraries,omitempty"`
}

type TemplateBundleApplyFeaturedHome struct {
	LibraryID  int    `json:"library_id"`
	TemplateID string `json:"template_id"`
}

func decodeTemplateBundleApplyRequest(data json.RawMessage) (TemplateBundleApplyRequest, error) {
	var req TemplateBundleApplyRequest
	if len(data) > 0 {
		if err := json.Unmarshal(data, &req); err != nil {
			return req, fmt.Errorf("invalid template bundle apply payload: %w", err)
		}
	}
	if req.BundleID == "" {
		return req, fmt.Errorf("bundle_id is required")
	}
	if len(req.LibraryIDs) == 0 {
		return req, fmt.Errorf("library_ids is required")
	}
	return req, nil
}
