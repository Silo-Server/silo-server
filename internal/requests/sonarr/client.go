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

func NewClient(httpClient *http.Client) *Client {
	return &Client{httpClient: httpClient}
}

func (c *Client) ListSeriesIntegrationOptions(ctx context.Context, integration mediarequests.Integration) (*mediarequests.IntegrationOptions, error) {
	client := arrclient.New(integration.BaseURL, integration.APIKeyRef, c.httpClient)
	rootFolders, err := arrclient.ListRootFolders(ctx, client)
	if err != nil {
		return nil, err
	}
	qualityProfiles, err := arrclient.ListQualityProfiles(ctx, client)
	if err != nil {
		return nil, err
	}
	tags, err := arrclient.ListTags(ctx, client)
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
		if !arrclient.IsEmptyOrTruncatedDecodeError(err) {
			return mediarequests.FulfillmentResult{}, err
		}
		// POST accepted but Sonarr returned an empty body. Recover the
		// new series' Sonarr ID by listing series filtered by TVDB ID;
		// without the ID the reconcile loop cannot advance the request.
		if found, lookErr := c.findSeriesByTVDBID(ctx, client, *req.TVDBID); lookErr == nil && found.ID > 0 {
			return resultFromSeries(found), nil
		}
		return arrclient.AcceptedWithoutResponse("sonarr"), nil
	}
	return resultFromSeries(created), nil
}

func (c *Client) findSeriesByTVDBID(ctx context.Context, client *arrclient.Client, tvdbID int) (seriesResource, error) {
	values := url.Values{}
	values.Set("tvdbId", strconv.Itoa(tvdbID))
	var matches []seriesResource
	if err := client.GetJSON(ctx, "/api/v3/series?"+values.Encode(), &matches); err != nil {
		return seriesResource{}, err
	}
	for _, s := range matches {
		if s.ID > 0 && s.TVDBID == tvdbID {
			return s, nil
		}
	}
	return seriesResource{}, fmt.Errorf("sonarr: series not found after add for tvdb_id %d", tvdbID)
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
	return arrclient.StatusFromQueueEvaluation("sonarr", seriesID, evaluation), nil
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

