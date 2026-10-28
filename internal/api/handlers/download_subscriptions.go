package handlers

// subscriptionRequest is the JSON body for POST /downloads/subscriptions.
type subscriptionRequest struct {
	SeriesID        string `json:"series_id"`
	Mode            string `json:"mode"` // all | future | latest_season | specific_seasons
	SeasonNumbers   []int  `json:"season_numbers,omitempty"`
	DeleteWatched   bool   `json:"delete_watched,omitempty"`
	MaxStorageBytes int64  `json:"max_storage_bytes,omitempty"`
}

// patchSubscriptionRequest is the JSON body for PATCH /downloads/subscriptions/{id}.
// Pointer fields distinguish "absent" from a zero value so a partial update only
// touches the fields the client sent.
type patchSubscriptionRequest struct {
	Mode            *string `json:"mode,omitempty"`
	SeasonNumbers   *[]int  `json:"season_numbers,omitempty"`
	DeleteWatched   *bool   `json:"delete_watched,omitempty"`
	MaxStorageBytes *int64  `json:"max_storage_bytes,omitempty"`
	Active          *bool   `json:"active,omitempty"`
}

// subscriptionResponse is one subscription in API responses.
type subscriptionResponse struct {
	ID              string `json:"id"`
	SeriesID        string `json:"series_id"`
	Mode            string `json:"mode"`
	SeasonNumbers   []int  `json:"season_numbers,omitempty"`
	TargetSeason    *int   `json:"target_season,omitempty"`
	DeleteWatched   bool   `json:"delete_watched"`
	MaxStorageBytes int64  `json:"max_storage_bytes"`
	Active          bool   `json:"active"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type subscriptionsListResponse struct {
	Subscriptions []subscriptionResponse `json:"subscriptions"`
}

// subscriptionResultResponse wraps a created/updated subscription plus the count
// of in-scope episodes registered as managed downloads.
type subscriptionResultResponse struct {
	Subscription subscriptionResponse `json:"subscription"`
	Registered   int                  `json:"registered"`
}

// subscriptionSyncResponse reports how many newly in-scope episodes a sync
// registered across the calling device's monitors.
type subscriptionSyncResponse struct {
	Registered int `json:"registered"`
}
