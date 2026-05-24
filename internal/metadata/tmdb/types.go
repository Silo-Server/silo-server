package tmdb

// paginatedResponse is a generic TMDB collection/list response.
type paginatedResponse[T any] struct {
	Page         int `json:"page"`
	TotalPages   int `json:"total_pages"`
	TotalResults int `json:"total_results"`
	Results      []T `json:"results"`
}

// MovieResult is a single movie from a TMDB collection endpoint.
type MovieResult struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// TVResult is a single TV show from a TMDB collection endpoint.
type TVResult struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// TrendingResult is a single item from the TMDB trending endpoint.
type TrendingResult struct {
	ID        int    `json:"id"`
	MediaType string `json:"media_type"`
	Title     string `json:"title"`
	Name      string `json:"name"`
}

// CollectionResult is a normalized item returned from a TMDB collection preset.
type CollectionResult struct {
	ID        int
	MediaType string
	Title     string
}

// MediaResult is a normalized TMDB movie or TV result for request search and
// discovery surfaces. MediaType is Silo-facing: "movie" or "series".
type MediaResult struct {
	ID           int
	MediaType    string
	Title        string
	Overview     string
	PosterPath   string
	BackdropPath string
	ReleaseDate  string
	Year         int
	Popularity   float64
	VoteAverage  float64
}

// MediaPage is a paginated TMDB search/discovery response normalized for the
// request system.
type MediaPage struct {
	Page         int
	TotalPages   int
	TotalResults int
	Results      []MediaResult
}

// DiscoverParams mirrors the TMDB `/discover/{movie,tv}` query parameters and
// is shaped to map 1:1 onto TMDBDiscoverSpec. Limit caps the total results
// the client paginates over.
type DiscoverParams struct {
	WithGenres       []int
	WithoutGenres    []int
	WithCompanies    []int
	WithNetworks     []int
	SortBy           string
	VoteCountGte     int
	VoteAverageGte   float64
	ReleaseDateGte   string
	ReleaseDateLte   string
	Certifications   []string
	CertificationLte string
	WithRuntimeGte   int
	WithRuntimeLte   int
	OriginalLanguage string
	Limit            int
}

// ExternalIDs holds cross-references used for matching collection entries.
type ExternalIDs struct {
	IMDbID string `json:"imdb_id"`
	TVDBID int    `json:"tvdb_id"`
}

// Collection is the decoded payload of TMDB's /collection/{id} endpoint.
// TMDB collections only contain movies (franchises, sagas), so each Part is
// implicitly a movie. The Parts slice preserves TMDB's ordering, which is
// curated and (for most franchises) chronological by release date.
type Collection struct {
	ID    int
	Name  string
	Parts []CollectionPart
}

// CollectionPart is a single movie inside a TMDB collection.
type CollectionPart struct {
	ID          int
	MediaType   string // "movie" — TMDB collection endpoints only return movies
	Title       string
	ReleaseDate string
}

// collectionResponse is the wire format of /collection/{id}. Fields we don't
// use (overview, poster_path, backdrop_path, original_name, etc.) are
// intentionally omitted so we don't drag unused decoding work into the
// library-collection sync hot path.
type collectionResponse struct {
	ID    int                      `json:"id"`
	Name  string                   `json:"name"`
	Parts []collectionResponsePart `json:"parts"`
}

type collectionResponsePart struct {
	ID          int    `json:"id"`
	MediaType   string `json:"media_type"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
}

type mediaMovieResponse struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	ReleaseDate  string  `json:"release_date"`
	Popularity   float64 `json:"popularity"`
	VoteAverage  float64 `json:"vote_average"`
}

type mediaTVResponse struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	FirstAirDate string  `json:"first_air_date"`
	Popularity   float64 `json:"popularity"`
	VoteAverage  float64 `json:"vote_average"`
}

type externalIDsResponse struct {
	ExternalIDs *ExternalIDs `json:"external_ids"`
}

type apiError struct {
	StatusMessage string `json:"status_message"`
	StatusCode    int    `json:"status_code"`
}

// MediaDetail is a normalized TMDB detail payload for the request system.
// MediaType is Silo-facing: "movie" or "series". Series-specific fields are
// zero-valued for movies and vice versa.
type MediaDetail struct {
	MediaType           string
	ID                  int
	IMDbID              string
	TVDBID              int
	Title               string
	OriginalTitle       string
	Tagline             string
	Overview            string
	PosterPath          string
	BackdropPath        string
	ReleaseDate         string
	Year                int
	Runtime             int
	Genres              []string
	VoteAverage         float64
	VoteCount           int
	Status              string
	Homepage            string
	ContentRating       string
	ProductionCompanies []string

	NumberOfSeasons  int
	NumberOfEpisodes int
	FirstAirDate     string
	LastAirDate      string
	Networks         []string

	Cast            []MediaCastMember
	Director        string
	Creators        []string
	Recommendations []MediaResult
}

// MediaCastMember is a single cast member entry from a TMDB credits response,
// normalized for the request detail surface.
type MediaCastMember struct {
	Name        string
	Character   string
	ProfilePath string
	Order       int
}

// genreEntry / companyEntry / networkEntry / personEntry mirror small object
// shapes from the TMDB JSON. They're internal to the decode path.
type genreEntry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type companyEntry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type networkEntry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type creditsResponse struct {
	Cast []castEntry `json:"cast"`
	Crew []crewEntry `json:"crew"`
}

type castEntry struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profile_path"`
	Order       int    `json:"order"`
}

type crewEntry struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Job        string `json:"job"`
	Department string `json:"department"`
}

type personEntry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type recommendationsMovieResponse struct {
	Results []mediaMovieResponse `json:"results"`
}

type recommendationsTVResponse struct {
	Results []mediaTVResponse `json:"results"`
}

type releaseDatesResponse struct {
	Results []releaseDatesCountryEntry `json:"results"`
}

type releaseDatesCountryEntry struct {
	ISO3166      string             `json:"iso_3166_1"`
	ReleaseDates []releaseDateEntry `json:"release_dates"`
}

type releaseDateEntry struct {
	Certification string `json:"certification"`
	ReleaseDate   string `json:"release_date"`
	Type          int    `json:"type"`
}

type contentRatingsResponse struct {
	Results []contentRatingEntry `json:"results"`
}

type contentRatingEntry struct {
	ISO3166 string `json:"iso_3166_1"`
	Rating  string `json:"rating"`
}

type movieDetailResponse struct {
	ID                  int                           `json:"id"`
	IMDbID              string                        `json:"imdb_id"`
	Title               string                        `json:"title"`
	OriginalTitle       string                        `json:"original_title"`
	Tagline             string                        `json:"tagline"`
	Overview            string                        `json:"overview"`
	PosterPath          string                        `json:"poster_path"`
	BackdropPath        string                        `json:"backdrop_path"`
	ReleaseDate         string                        `json:"release_date"`
	Runtime             int                           `json:"runtime"`
	Genres              []genreEntry                  `json:"genres"`
	VoteAverage         float64                       `json:"vote_average"`
	VoteCount           int                           `json:"vote_count"`
	Status              string                        `json:"status"`
	Homepage            string                        `json:"homepage"`
	ProductionCompanies []companyEntry                `json:"production_companies"`
	Credits             *creditsResponse              `json:"credits"`
	ExternalIDs         *ExternalIDs                  `json:"external_ids"`
	Recommendations     *recommendationsMovieResponse `json:"recommendations"`
	ReleaseDates        *releaseDatesResponse         `json:"release_dates"`
}

type tvDetailResponse struct {
	ID               int                        `json:"id"`
	Name             string                     `json:"name"`
	OriginalName     string                     `json:"original_name"`
	Tagline          string                     `json:"tagline"`
	Overview         string                     `json:"overview"`
	PosterPath       string                     `json:"poster_path"`
	BackdropPath     string                     `json:"backdrop_path"`
	FirstAirDate     string                     `json:"first_air_date"`
	LastAirDate      string                     `json:"last_air_date"`
	EpisodeRunTime   []int                      `json:"episode_run_time"`
	NumberOfSeasons  int                        `json:"number_of_seasons"`
	NumberOfEpisodes int                        `json:"number_of_episodes"`
	Genres           []genreEntry               `json:"genres"`
	VoteAverage      float64                    `json:"vote_average"`
	VoteCount        int                        `json:"vote_count"`
	Status           string                     `json:"status"`
	Homepage         string                     `json:"homepage"`
	Networks         []networkEntry             `json:"networks"`
	CreatedBy        []personEntry              `json:"created_by"`
	Credits          *creditsResponse           `json:"credits"`
	ExternalIDs      *ExternalIDs               `json:"external_ids"`
	Recommendations  *recommendationsTVResponse `json:"recommendations"`
	ContentRatings   *contentRatingsResponse    `json:"content_ratings"`
}
