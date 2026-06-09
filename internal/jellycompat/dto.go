package jellycompat

// queryResultDTO mirrors Jellyfin's common paged result envelope.
type queryResultDTO struct {
	Items            []baseItemDTO `json:"Items"`
	TotalRecordCount int           `json:"TotalRecordCount"`
	StartIndex       int           `json:"StartIndex"`
}

type baseItemDTO struct {
	ServerID                 string                       `json:"ServerId,omitempty"`
	ID                       string                       `json:"Id"`
	ChannelID                *string                      `json:"ChannelId"`
	DateCreated              string                       `json:"DateCreated,omitempty"`
	Type                     string                       `json:"Type,omitempty"`
	MediaType                string                       `json:"MediaType,omitempty"`
	Name                     string                       `json:"Name"`
	IsFolder                 bool                         `json:"IsFolder"`
	IsHD                     bool                         `json:"IsHD,omitempty"`
	CanDelete                bool                         `json:"CanDelete,omitempty"`
	CanDownload              bool                         `json:"CanDownload,omitempty"`
	HasSubtitles             bool                         `json:"HasSubtitles,omitempty"`
	SupportsSync             bool                         `json:"SupportsSync,omitempty"`
	EnableMediaSourceDisplay bool                         `json:"EnableMediaSourceDisplay,omitempty"`
	Container                string                       `json:"Container,omitempty"`
	LocationType             string                       `json:"LocationType,omitempty"`
	VideoType                string                       `json:"VideoType,omitempty"`
	PlayAccess               string                       `json:"PlayAccess,omitempty"`
	Etag                     string                       `json:"Etag,omitempty"`
	DisplayPreferencesID     string                       `json:"DisplayPreferencesId,omitempty"`
	CollectionType           string                       `json:"CollectionType,omitempty"`
	RunTimeTicks             int64                        `json:"RunTimeTicks,omitempty"`
	ProductionYear           int                          `json:"ProductionYear,omitempty"`
	OfficialRating           string                       `json:"OfficialRating,omitempty"`
	CommunityRating          *float64                     `json:"CommunityRating,omitempty"`
	CriticRating             *float64                     `json:"CriticRating,omitempty"`
	Overview                 string                       `json:"Overview,omitempty"`
	OriginalTitle            string                       `json:"OriginalTitle,omitempty"`
	PremiereDate             string                       `json:"PremiereDate,omitempty"`
	Path                     string                       `json:"Path,omitempty"`
	ExternalURLs             []map[string]any             `json:"ExternalUrls,omitempty"`
	RemoteTrailers           []map[string]any             `json:"RemoteTrailers,omitempty"`
	Genres                   []string                     `json:"Genres,omitempty"`
	GenreItems               []namePairDTO                `json:"GenreItems,omitempty"`
	Studios                  []namePairDTO                `json:"Studios,omitempty"`
	Taglines                 []string                     `json:"Taglines,omitempty"`
	Tags                     []string                     `json:"Tags,omitempty"`
	ProviderIDs              map[string]string            `json:"ProviderIds,omitempty"`
	ProductionLocations      []string                     `json:"ProductionLocations,omitempty"`
	ImageTags                map[string]string            `json:"ImageTags"`
	BackdropImageTags        []string                     `json:"BackdropImageTags,omitempty"`
	PrimaryImageAspectRatio  *float64                     `json:"PrimaryImageAspectRatio,omitempty"`
	ImageBlurHashes          map[string]map[string]string `json:"ImageBlurHashes,omitempty"`
	UserData                 *itemUserDataDTO             `json:"UserData,omitempty"`
	SeriesID                 string                       `json:"SeriesId,omitempty"`
	SeasonID                 string                       `json:"SeasonId,omitempty"`
	SeasonName               string                       `json:"SeasonName,omitempty"`
	SeriesName               string                       `json:"SeriesName,omitempty"`
	SeriesPrimaryImageTag    string                       `json:"SeriesPrimaryImageTag,omitempty"`
	ParentBackdropImageTags  []string                     `json:"ParentBackdropImageTags,omitempty"`
	ParentBackdropItemID     string                       `json:"ParentBackdropItemId,omitempty"`
	ParentThumbImageTag      string                       `json:"ParentThumbImageTag,omitempty"`
	ParentThumbItemID        string                       `json:"ParentThumbItemId,omitempty"`
	ParentID                 string                       `json:"ParentId,omitempty"`
	SortName                 string                       `json:"SortName,omitempty"`
	ForcedSortName           string                       `json:"ForcedSortName,omitempty"`
	IndexNumber              *int                         `json:"IndexNumber,omitempty"`
	ParentIndexNumber        *int                         `json:"ParentIndexNumber,omitempty"`
	People                   []personDTO                  `json:"People,omitempty"`
	ChildCount               int                          `json:"ChildCount,omitempty"`
	RecursiveItemCount       int                          `json:"RecursiveItemCount,omitempty"`
	LocalTrailerCount        int                          `json:"LocalTrailerCount,omitempty"`
	SpecialFeatureCount      int                          `json:"SpecialFeatureCount,omitempty"`
	MovieCount               int                          `json:"MovieCount,omitempty"`
	SeriesCount              int                          `json:"SeriesCount,omitempty"`
	SeasonCount              int                          `json:"SeasonCount,omitempty"`
	EpisodeCount             int                          `json:"EpisodeCount,omitempty"`
	LockedFields             []string                     `json:"LockedFields,omitempty"`
	LockData                 bool                         `json:"LockData,omitempty"`
	Chapters                 []map[string]any             `json:"Chapters,omitempty"`
	Trickplay                map[string]any               `json:"Trickplay,omitempty"`
	MediaSourceCount         int                          `json:"MediaSourceCount,omitempty"`
	MediaSources             []mediaSourceDTO             `json:"MediaSources,omitempty"`
	MediaStreams             []mediaStreamDTO             `json:"MediaStreams,omitempty"`
	Width                    int                          `json:"Width,omitempty"`
	Height                   int                          `json:"Height,omitempty"`
}

type itemUserDataDTO struct {
	PlayedPercentage      float64 `json:"PlayedPercentage,omitempty"`
	PlaybackPositionTicks int64   `json:"PlaybackPositionTicks"`
	Played                bool    `json:"Played"`
	IsFavorite            bool    `json:"IsFavorite"`
	UnplayedItemCount     int     `json:"UnplayedItemCount,omitempty"`
	PlayCount             int     `json:"PlayCount"`
	Key                   string  `json:"Key"`
	ItemID                string  `json:"ItemId"`
	LastPlayedDate        string  `json:"LastPlayedDate,omitempty"`
}

type namePairDTO struct {
	Name string `json:"Name"`
	ID   string `json:"Id,omitempty"`
}

type personDTO struct {
	ID              string `json:"Id"`
	Name            string `json:"Name"`
	Role            string `json:"Role,omitempty"`
	Type            string `json:"Type,omitempty"`
	PrimaryImageTag string `json:"PrimaryImageTag,omitempty"`
}

type virtualFolderDTO struct {
	Name               string               `json:"Name"`
	Locations          []string             `json:"Locations"`
	CollectionType     string               `json:"CollectionType,omitempty"`
	LibraryOptions     virtualLibraryOptDTO `json:"LibraryOptions"`
	ItemID             string               `json:"ItemId"`
	PrimaryImageItemID string               `json:"PrimaryImageItemId,omitempty"`
	RefreshProgress    float64              `json:"RefreshProgress"`
	RefreshStatus      string               `json:"RefreshStatus"`
}

type virtualLibraryOptDTO struct {
	Enabled                       bool     `json:"Enabled"`
	EnableRealtimeMonitor         bool     `json:"EnableRealtimeMonitor"`
	EnableInternetProviders       bool     `json:"EnableInternetProviders"`
	SaveLocalMetadata             bool     `json:"SaveLocalMetadata"`
	EnableAutomaticSeriesGrouping bool     `json:"EnableAutomaticSeriesGrouping"`
	PreferredMetadataLanguage     string   `json:"PreferredMetadataLanguage"`
	MetadataCountryCode           string   `json:"MetadataCountryCode"`
	SeasonZeroDisplayName         string   `json:"SeasonZeroDisplayName"`
	AutomaticRefreshIntervalDays  int      `json:"AutomaticRefreshIntervalDays"`
	EnableEmbeddedTitles          bool     `json:"EnableEmbeddedTitles"`
	EnableEmbeddedExtrasTitles    bool     `json:"EnableEmbeddedExtrasTitles"`
	EnableEmbeddedEpisodeInfos    bool     `json:"EnableEmbeddedEpisodeInfos"`
	TypeOptions                   []string `json:"TypeOptions"`
}

type searchHintResultDTO struct {
	SearchHints      []searchHintDTO `json:"SearchHints"`
	TotalRecordCount int             `json:"TotalRecordCount"`
}

type searchHintDTO struct {
	ItemID           string   `json:"ItemId"`
	ID               string   `json:"Id"`
	Name             string   `json:"Name"`
	Type             string   `json:"Type,omitempty"`
	ProductionYear   int      `json:"ProductionYear,omitempty"`
	RunTimeTicks     int64    `json:"RunTimeTicks,omitempty"`
	PrimaryImageTag  string   `json:"PrimaryImageTag,omitempty"`
	BackdropImageTag string   `json:"BackdropImageTag,omitempty"`
	Series           string   `json:"Series,omitempty"`
	Genres           []string `json:"Genres,omitempty"`
}

type playbackInfoResponseDTO struct {
	PlaySessionID string           `json:"PlaySessionId"`
	MediaSources  []mediaSourceDTO `json:"MediaSources"`
}

type mediaSourceDTO struct {
	Protocol                            string            `json:"Protocol,omitempty"`
	ID                                  string            `json:"Id"`
	Path                                string            `json:"Path,omitempty"`
	Type                                string            `json:"Type,omitempty"`
	Container                           string            `json:"Container,omitempty"`
	Size                                int64             `json:"Size,omitempty"`
	Name                                string            `json:"Name,omitempty"`
	IsRemote                            bool              `json:"IsRemote"`
	ETag                                string            `json:"ETag,omitempty"`
	RunTimeTicks                        int64             `json:"RunTimeTicks,omitempty"`
	ReadAtNativeFramerate               bool              `json:"ReadAtNativeFramerate"`
	IgnoreDts                           bool              `json:"IgnoreDts"`
	IgnoreIndex                         bool              `json:"IgnoreIndex"`
	GenPtsInput                         bool              `json:"GenPtsInput"`
	SupportsTranscoding                 bool              `json:"SupportsTranscoding"`
	SupportsDirectStream                bool              `json:"SupportsDirectStream"`
	SupportsDirectPlay                  bool              `json:"SupportsDirectPlay"`
	IsInfiniteStream                    bool              `json:"IsInfiniteStream"`
	UseMostCompatibleTranscodingProfile bool              `json:"UseMostCompatibleTranscodingProfile"`
	RequiresOpening                     bool              `json:"RequiresOpening"`
	RequiresClosing                     bool              `json:"RequiresClosing"`
	RequiresLooping                     bool              `json:"RequiresLooping"`
	SupportsProbing                     bool              `json:"SupportsProbing"`
	VideoType                           string            `json:"VideoType,omitempty"`
	HasSegments                         bool              `json:"HasSegments"`
	Formats                             []string          `json:"Formats"`
	RequiredHTTPHeaders                 map[string]string `json:"RequiredHttpHeaders"`
	MediaAttachments                    []map[string]any  `json:"MediaAttachments"`
	DirectStreamURL                     string            `json:"DirectStreamUrl,omitempty"`
	TranscodingURL                      string            `json:"TranscodingUrl,omitempty"`
	TranscodingSubProtocol              string            `json:"TranscodingSubProtocol"`
	TranscodingContainer                string            `json:"TranscodingContainer,omitempty"`
	Bitrate                             int               `json:"Bitrate,omitempty"`
	DefaultAudioStreamIndex             *int              `json:"DefaultAudioStreamIndex,omitempty"`
	DefaultSubtitleStreamIndex          *int              `json:"DefaultSubtitleStreamIndex,omitempty"`
	MediaStreams                        []mediaStreamDTO  `json:"MediaStreams,omitempty"`
}

type mediaStreamDTO struct {
	Index                  int     `json:"Index"`
	Type                   string  `json:"Type"`
	Codec                  string  `json:"Codec,omitempty"`
	Language               string  `json:"Language,omitempty"`
	TimeBase               string  `json:"TimeBase,omitempty"`
	DisplayTitle           string  `json:"DisplayTitle,omitempty"`
	Title                  string  `json:"Title,omitempty"`
	IsDefault              bool    `json:"IsDefault"`
	IsExternal             bool    `json:"IsExternal"`
	IsForced               bool    `json:"IsForced"`
	IsHearingImpaired      bool    `json:"IsHearingImpaired"`
	IsTextSubtitleStream   bool    `json:"IsTextSubtitleStream"`
	SupportsExternalStream bool    `json:"SupportsExternalStream"`
	DeliveryURL            string  `json:"DeliveryUrl,omitempty"`
	DeliveryMethod         string  `json:"DeliveryMethod,omitempty"`
	Path                   string  `json:"Path,omitempty"`
	IsExternalURL          *bool   `json:"IsExternalUrl,omitempty"`
	IsInterlaced           bool    `json:"IsInterlaced"`
	IsAVC                  bool    `json:"IsAVC"`
	IsAnamorphic           bool    `json:"IsAnamorphic"`
	NalLengthSize          string  `json:"NalLengthSize,omitempty"`
	BitDepth               int     `json:"BitDepth,omitempty"`
	RefFrames              int     `json:"RefFrames,omitempty"`
	Profile                string  `json:"Profile,omitempty"`
	Level                  int     `json:"Level,omitempty"`
	AspectRatio            string  `json:"AspectRatio,omitempty"`
	VideoRange             string  `json:"VideoRange,omitempty"`
	VideoRangeType         string  `json:"VideoRangeType,omitempty"`
	ColorPrimaries         string  `json:"ColorPrimaries,omitempty"`
	ColorSpace             string  `json:"ColorSpace,omitempty"`
	ColorTransfer          string  `json:"ColorTransfer,omitempty"`
	PixelFormat            string  `json:"PixelFormat,omitempty"`
	AudioSpatialFormat     string  `json:"AudioSpatialFormat,omitempty"`
	AverageFrameRate       float64 `json:"AverageFrameRate,omitempty"`
	RealFrameRate          float64 `json:"RealFrameRate,omitempty"`
	ReferenceFrameRate     float64 `json:"ReferenceFrameRate,omitempty"`
	Height                 int     `json:"Height"`
	Width                  int     `json:"Width"`
	Channels               int     `json:"Channels,omitempty"`
	BitRate                int     `json:"BitRate,omitempty"`
}
