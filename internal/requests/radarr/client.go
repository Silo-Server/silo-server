package radarr

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

type movieResource struct {
	ID                  int             `json:"id,omitempty"`
	Title               string          `json:"title,omitempty"`
	TMDBID              int             `json:"tmdbId,omitempty"`
	Year                int             `json:"year,omitempty"`
	TitleSlug           string          `json:"titleSlug,omitempty"`
	QualityProfileID    int             `json:"qualityProfileId,omitempty"`
	RootFolderPath      string          `json:"rootFolderPath,omitempty"`
	Monitored           bool            `json:"monitored"`
	MinimumAvailability string          `json:"minimumAvailability,omitempty"`
	Tags                []int           `json:"tags,omitempty"`
	AddOptions          addMovieOptions `json:"addOptions,omitempty"`
}

type addMovieOptions struct {
	SearchForMovie bool   `json:"searchForMovie"`
	Monitor        string `json:"monitor,omitempty"`
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

func (c *Client) ListMovieIntegrationOptions(ctx context.Context, integration mediarequests.Integration) (*mediarequests.IntegrationOptions, error) {
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
		Kind:            "radarr",
		RootFolders:     rootFolders,
		QualityProfiles: qualityProfiles,
		Tags:            tags,
	}, nil
}

func (c *Client) SubmitMovie(ctx context.Context, req mediarequests.Request, integration mediarequests.Integration) (mediarequests.FulfillmentResult, error) {
	if req.MediaType != mediarequests.MediaTypeMovie {
		return mediarequests.FulfillmentResult{}, fmt.Errorf("radarr: request is not a movie")
	}
	if integration.QualityProfileID == nil {
		return mediarequests.FulfillmentResult{}, fmt.Errorf("radarr: quality profile is required")
	}

	client := arrclient.New(integration.BaseURL, integration.APIKeyRef, c.httpClient)
	movie, err := c.lookupMovie(ctx, client, req.TMDBID)
	if err != nil {
		return mediarequests.FulfillmentResult{}, err
	}
	movie.RootFolderPath = integration.RootFolder
	movie.QualityProfileID = *integration.QualityProfileID
	movie.Monitored = arrclient.BoolOption(integration.Options, "monitored", true)
	movie.MinimumAvailability = arrclient.StringOption(integration.Options, "minimum_availability", "released")
	movie.Tags = integration.Tags
	movie.AddOptions = addMovieOptions{
		SearchForMovie: arrclient.BoolOption(
			integration.Options,
			"search_for_movie",
			arrclient.BoolOption(integration.Options, "search_on_add", true),
		),
		Monitor: arrclient.StringOption(integration.Options, "monitor", "movieOnly"),
	}

	var created movieResource
	if err := client.PostJSON(ctx, "/api/v3/movie", movie, &created); err != nil {
		if arrclient.IsEmptyOrTruncatedDecodeError(err) {
			return acceptedWithoutResponse("radarr"), nil
		}
		return mediarequests.FulfillmentResult{}, err
	}
	return resultFromMovie(created), nil
}

func (c *Client) CheckMovieStatus(ctx context.Context, req mediarequests.Request, integration mediarequests.Integration) (mediarequests.FulfillmentStatus, error) {
	client := arrclient.New(integration.BaseURL, integration.APIKeyRef, c.httpClient)
	movieID, _ := strconv.Atoi(req.ExternalID)
	if movieID <= 0 {
		return mediarequests.FulfillmentStatus{
			Status:          mediarequests.StatusQueued,
			IntegrationKind: "radarr",
			ExternalStatus:  "external_id_unavailable",
		}, nil
	}

	queues, err := c.queueDetails(ctx, client, movieID)
	if err != nil {
		return mediarequests.FulfillmentStatus{}, err
	}
	evaluation := arrclient.EvaluateQueue(queues)
	return statusFromQueueEvaluation("radarr", movieID, evaluation), nil
}

func (c *Client) lookupMovie(ctx context.Context, client *arrclient.Client, tmdbID int) (movieResource, error) {
	values := url.Values{}
	values.Set("tmdbId", strconv.Itoa(tmdbID))
	var movie movieResource
	if err := client.GetJSON(ctx, "/api/v3/movie/lookup/tmdb?"+values.Encode(), &movie); err != nil {
		return movieResource{}, err
	}
	if movie.TMDBID == 0 {
		movie.TMDBID = tmdbID
	}
	return movie, nil
}

func (c *Client) queueDetails(ctx context.Context, client *arrclient.Client, movieID int) ([]arrclient.QueueResource, error) {
	values := url.Values{}
	values.Set("movieId", strconv.Itoa(movieID))
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

func resultFromMovie(movie movieResource) mediarequests.FulfillmentResult {
	externalID := ""
	if movie.ID > 0 {
		externalID = strconv.Itoa(movie.ID)
	}
	return mediarequests.FulfillmentResult{
		IntegrationKind: "radarr",
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
