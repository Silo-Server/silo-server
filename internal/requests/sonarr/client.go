package sonarr

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/requests/arrclient"
)

type Client struct {
	httpClient *http.Client
}

type seriesResource struct {
	ID               int              `json:"id,omitempty"`
	Title            string           `json:"title,omitempty"`
	TVDBID           int              `json:"tvdbId,omitempty"`
	TMDBID           int              `json:"tmdbId,omitempty"`
	TitleSlug        string           `json:"titleSlug,omitempty"`
	QualityProfileID int              `json:"qualityProfileId,omitempty"`
	RootFolderPath   string           `json:"rootFolderPath,omitempty"`
	SeasonFolder     bool             `json:"seasonFolder"`
	Monitored        bool             `json:"monitored"`
	SeriesType       string           `json:"seriesType,omitempty"`
	Tags             []int            `json:"tags,omitempty"`
	AddOptions       addSeriesOptions `json:"addOptions,omitempty"`
}

type addSeriesOptions struct {
	Monitor                      string `json:"monitor,omitempty"`
	SearchForMissingEpisodes     bool   `json:"searchForMissingEpisodes"`
	SearchForCutoffUnmetEpisodes bool   `json:"searchForCutoffUnmetEpisodes,omitempty"`
}

type rootFolderResource struct {
	Path       string `json:"path"`
	FreeSpace  int64  `json:"freeSpace"`
	TotalSpace int64  `json:"totalSpace"`
	Accessible bool   `json:"accessible"`
}

type qualityProfileResource struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type tagResource struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

func NewClient(httpClient *http.Client) *Client {
	return &Client{httpClient: httpClient}
}

func (c *Client) ListSeriesIntegrationOptions(ctx context.Context, integration mediarequests.Integration) (*mediarequests.IntegrationOptions, error) {
	client := arrclient.New(integration.BaseURL, integration.APIKeyRef, c.httpClient)
	rootFolders, err := c.rootFolders(ctx, client)
	if err != nil {
		return nil, err
	}
	qualityProfiles, err := c.qualityProfiles(ctx, client)
	if err != nil {
		return nil, err
	}
	tags, err := c.tags(ctx, client)
	if err != nil {
		return nil, err
	}
	return &mediarequests.IntegrationOptions{
		Kind:            "sonarr",
		RootFolders:     rootFolders,
		QualityProfiles: qualityProfiles,
		Tags:            tags,
	}, nil
}

func (c *Client) SubmitSeries(ctx context.Context, req mediarequests.Request, integration mediarequests.Integration) (mediarequests.FulfillmentResult, error) {
	if req.MediaType != mediarequests.MediaTypeSeries {
		return mediarequests.FulfillmentResult{}, fmt.Errorf("sonarr: request is not a series")
	}
	if integration.QualityProfileID == nil {
		return mediarequests.FulfillmentResult{}, fmt.Errorf("sonarr: quality profile is required")
	}
	if req.TVDBID == nil || *req.TVDBID <= 0 {
		return mediarequests.FulfillmentResult{}, fmt.Errorf("sonarr: tvdb_id is required")
	}

	client := arrclient.New(integration.BaseURL, integration.APIKeyRef, c.httpClient)
	series, err := c.lookupSeries(ctx, client, *req.TVDBID)
	if err != nil {
		return mediarequests.FulfillmentResult{}, err
	}
	series.RootFolderPath = integration.RootFolder
	series.QualityProfileID = *integration.QualityProfileID
	series.SeasonFolder = arrclient.BoolOption(integration.Options, "season_folder", true)
	series.Monitored = arrclient.BoolOption(integration.Options, "monitored", true)
	series.SeriesType = arrclient.StringOption(integration.Options, "series_type", "standard")
	series.Tags = integration.Tags
	series.AddOptions = addSeriesOptions{
		Monitor: arrclient.StringOption(integration.Options, "monitor", "all"),
		SearchForMissingEpisodes: arrclient.BoolOption(
			integration.Options,
			"search_for_missing_episodes",
			arrclient.BoolOption(integration.Options, "search_on_add", true),
		),
		SearchForCutoffUnmetEpisodes: arrclient.BoolOption(integration.Options, "search_for_cutoff_unmet", false),
	}

	var created seriesResource
	if err := client.PostJSON(ctx, "/api/v3/series", series, &created); err != nil {
		if arrclient.IsEmptyOrTruncatedDecodeError(err) {
			return acceptedWithoutResponse("sonarr"), nil
		}
		return mediarequests.FulfillmentResult{}, err
	}
	return resultFromSeries(created), nil
}

func (c *Client) CheckSeriesStatus(ctx context.Context, req mediarequests.Request, integration mediarequests.Integration) (mediarequests.FulfillmentStatus, error) {
	client := arrclient.New(integration.BaseURL, integration.APIKeyRef, c.httpClient)
	seriesID, _ := strconv.Atoi(req.ExternalID)
	if seriesID <= 0 {
		return mediarequests.FulfillmentStatus{
			Status:          mediarequests.StatusQueued,
			IntegrationKind: "sonarr",
			ExternalStatus:  "external_id_unavailable",
		}, nil
	}

	queues, err := c.queueDetails(ctx, client, seriesID)
	if err != nil {
		return mediarequests.FulfillmentStatus{}, err
	}
	evaluation := arrclient.EvaluateQueue(queues)
	return statusFromQueueEvaluation("sonarr", seriesID, evaluation), nil
}

func (c *Client) lookupSeries(ctx context.Context, client *arrclient.Client, tvdbID int) (seriesResource, error) {
	values := url.Values{}
	values.Set("term", "tvdb:"+strconv.Itoa(tvdbID))
	var matches []seriesResource
	if err := client.GetJSON(ctx, "/api/v3/series/lookup?"+values.Encode(), &matches); err != nil {
		return seriesResource{}, err
	}
	for _, match := range matches {
		if match.TVDBID == tvdbID {
			return match, nil
		}
	}
	if len(matches) > 0 {
		return matches[0], nil
	}
	return seriesResource{}, fmt.Errorf("sonarr: no series found for tvdb_id %d", tvdbID)
}

func (c *Client) queueDetails(ctx context.Context, client *arrclient.Client, seriesID int) ([]arrclient.QueueResource, error) {
	values := url.Values{}
	values.Set("seriesId", strconv.Itoa(seriesID))
	var queues []arrclient.QueueResource
	if err := client.GetJSON(ctx, "/api/v3/queue/details?"+values.Encode(), &queues); err != nil {
		return nil, err
	}
	return queues, nil
}

func (c *Client) rootFolders(ctx context.Context, client *arrclient.Client) ([]mediarequests.IntegrationRootFolder, error) {
	var resources []rootFolderResource
	if err := client.GetJSON(ctx, "/api/v3/rootfolder", &resources); err != nil {
		return nil, err
	}
	out := make([]mediarequests.IntegrationRootFolder, 0, len(resources))
	for _, resource := range resources {
		out = append(out, mediarequests.IntegrationRootFolder{
			Path:       resource.Path,
			FreeSpace:  resource.FreeSpace,
			TotalSpace: resource.TotalSpace,
			Accessible: resource.Accessible,
		})
	}
	return out, nil
}

func (c *Client) qualityProfiles(ctx context.Context, client *arrclient.Client) ([]mediarequests.IntegrationQualityProfile, error) {
	var resources []qualityProfileResource
	if err := client.GetJSON(ctx, "/api/v3/qualityprofile", &resources); err != nil {
		return nil, err
	}
	out := make([]mediarequests.IntegrationQualityProfile, 0, len(resources))
	for _, resource := range resources {
		out = append(out, mediarequests.IntegrationQualityProfile{
			ID:   resource.ID,
			Name: resource.Name,
		})
	}
	return out, nil
}

func (c *Client) tags(ctx context.Context, client *arrclient.Client) ([]mediarequests.IntegrationTag, error) {
	var resources []tagResource
	if err := client.GetJSON(ctx, "/api/v3/tag", &resources); err != nil {
		return nil, err
	}
	out := make([]mediarequests.IntegrationTag, 0, len(resources))
	for _, resource := range resources {
		out = append(out, mediarequests.IntegrationTag{
			ID:    resource.ID,
			Label: resource.Label,
		})
	}
	return out, nil
}

func resultFromSeries(series seriesResource) mediarequests.FulfillmentResult {
	externalID := ""
	if series.ID > 0 {
		externalID = strconv.Itoa(series.ID)
	}
	return mediarequests.FulfillmentResult{
		IntegrationKind: "sonarr",
		ExternalID:      externalID,
		ExternalStatus:  "queued",
	}
}

func acceptedWithoutResponse(kind string) mediarequests.FulfillmentResult {
	return mediarequests.FulfillmentResult{
		IntegrationKind: kind,
		ExternalStatus:  "accepted_without_response",
	}
}

func statusFromQueueEvaluation(kind string, externalID int, evaluation arrclient.QueueEvaluation) mediarequests.FulfillmentStatus {
	status := mediarequests.StatusQueued
	outcome := mediarequests.Outcome("")
	if evaluation.State == arrclient.QueueStateDownloading {
		status = mediarequests.StatusDownloading
	}
	if evaluation.State == arrclient.QueueStateFailed {
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
