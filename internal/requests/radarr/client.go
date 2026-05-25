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

func NewClient(httpClient *http.Client) *Client {
	return &Client{httpClient: httpClient}
}

func (c *Client) ListMovieIntegrationOptions(ctx context.Context, integration mediarequests.Integration) (*mediarequests.IntegrationOptions, error) {
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
		if !arrclient.IsEmptyOrTruncatedDecodeError(err) {
			return mediarequests.FulfillmentResult{}, err
		}
		// POST accepted but Radarr returned an empty body. Recover the
		// new movie's Radarr ID by listing movies filtered by TMDB ID;
		// without the ID the reconcile loop cannot advance the request.
		if found, lookErr := c.findMovieByTMDBID(ctx, client, req.TMDBID); lookErr == nil && found.ID > 0 {
			return resultFromMovie(found), nil
		}
		return arrclient.AcceptedWithoutResponse("radarr"), nil
	}
	return resultFromMovie(created), nil
}

func (c *Client) findMovieByTMDBID(ctx context.Context, client *arrclient.Client, tmdbID int) (movieResource, error) {
	values := url.Values{}
	values.Set("tmdbId", strconv.Itoa(tmdbID))
	var matches []movieResource
	if err := client.GetJSON(ctx, "/api/v3/movie?"+values.Encode(), &matches); err != nil {
		return movieResource{}, err
	}
	for _, m := range matches {
		if m.ID > 0 {
			return m, nil
		}
	}
	return movieResource{}, fmt.Errorf("radarr: movie not found after add for tmdb_id %d", tmdbID)
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
	return arrclient.StatusFromQueueEvaluation("radarr", movieID, evaluation), nil
}

func (c *Client) lookupMovie(ctx context.Context, client *arrclient.Client, tmdbID int) (movieResource, error) {
	values := url.Values{}
	values.Set("tmdbId", strconv.Itoa(tmdbID))
	// Radarr's /api/v3/movie/lookup/tmdb returns a single MovieResource, unlike
	// /api/v3/movie/lookup which returns an array. Missing IDs return non-2xx
	// (handled as HTTPError upstream), so a successful response is the movie.
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
