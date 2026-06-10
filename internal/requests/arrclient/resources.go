package arrclient

import (
	"context"
	"strconv"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

type RootFolderResource struct {
	Path       string `json:"path"`
	FreeSpace  int64  `json:"freeSpace"`
	TotalSpace int64  `json:"totalSpace"`
	Accessible bool   `json:"accessible"`
}

type QualityProfileResource struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type TagResource struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// ListRootFolders returns the arr root folders. It yields the local
// RootFolderResource shape directly; the schema-driven config form consumes
// generic RouterOptions from the plugin instead of arr-specific option types.
func ListRootFolders(ctx context.Context, client *Client) ([]RootFolderResource, error) {
	var resources []RootFolderResource
	if err := client.GetJSON(ctx, "/api/v3/rootfolder", &resources); err != nil {
		return nil, err
	}
	return resources, nil
}

func ListQualityProfiles(ctx context.Context, client *Client) ([]QualityProfileResource, error) {
	var resources []QualityProfileResource
	if err := client.GetJSON(ctx, "/api/v3/qualityprofile", &resources); err != nil {
		return nil, err
	}
	return resources, nil
}

func ListTags(ctx context.Context, client *Client) ([]TagResource, error) {
	var resources []TagResource
	if err := client.GetJSON(ctx, "/api/v3/tag", &resources); err != nil {
		return nil, err
	}
	return resources, nil
}

// AcceptedWithoutResponse returns a FulfillmentResult marking the submission
// as accepted by the downstream integration but with no external id captured
// (typically a 201 with empty body when lookup recovery also fails).
func AcceptedWithoutResponse(kind string) mediarequests.FulfillmentResult {
	return mediarequests.FulfillmentResult{
		IntegrationKind: kind,
		ExternalStatus:  "accepted_without_response",
	}
}

// StatusFromQueueEvaluation translates an arrclient.QueueEvaluation into the
// mediarequests.FulfillmentStatus shape shared by Radarr and Sonarr clients.
func StatusFromQueueEvaluation(kind string, externalID int, evaluation QueueEvaluation) mediarequests.FulfillmentStatus {
	status := mediarequests.StatusQueued
	outcome := mediarequests.Outcome("")
	if evaluation.State == QueueStateDownloading {
		status = mediarequests.StatusDownloading
	}
	if evaluation.State == QueueStateFailed {
		outcome = mediarequests.OutcomeFailed
	}
	return mediarequests.FulfillmentStatus{
		Status:          status,
		Outcome:         outcome,
		IntegrationKind: kind,
		ExternalID:      strconv.Itoa(externalID),
		ExternalStatus:  evaluation.ExternalStatus,
		Message:         evaluation.Message,
	}
}
