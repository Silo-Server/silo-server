package requests

// BundledStudio is a curated movie studio surfaced in the request discover
// section. LogoPath is a TMDB image file path that is rendered through the
// duotone filter for uniform white-on-gray presentation.
type BundledStudio struct {
	TMDBID      int
	Slug        string
	DisplayName string
	LogoPath    string
}

// BundledNetwork is a curated TV network surfaced in the request discover
// section. LogoPath is a TMDB image file path rendered through the duotone
// filter.
type BundledNetwork struct {
	TMDBID      int
	Slug        string
	DisplayName string
	LogoPath    string
}

// BundledGenre is a curated genre. MovieID is the TMDB movie genre ID;
// SeriesID is the TMDB tv genre ID, or 0 when no TV equivalent exists.
type BundledGenre struct {
	Slug         string
	DisplayName  string
	GradientFrom string
	GradientTo   string
	MovieID      int
	SeriesID     int
}

// BundledStudios is the compile-time list of studios shown in the Studios
// carousel. TMDB IDs and logo paths follow the curated set used by Overseerr
// so logos resolve reliably through the duotone filter.
var BundledStudios = []BundledStudio{
	{TMDBID: 2, Slug: "disney", DisplayName: "Disney", LogoPath: "/wdrCwmRnLFJhEoH8GSfymY85KHT.png"},
	{TMDBID: 3, Slug: "pixar", DisplayName: "Pixar", LogoPath: "/1TjvGVDMYsj6JBxOAkUHpPEwLf7.png"},
	{TMDBID: 420, Slug: "marvel-studios", DisplayName: "Marvel Studios", LogoPath: "/hUzeosd33nzE5MCNsZxCGEKTXaQ.png"},
	{TMDBID: 174, Slug: "warner-bros-pictures", DisplayName: "Warner Bros. Pictures", LogoPath: "/ky0xOc5OrhzkZ1N6KyUxacfQsCk.png"},
	{TMDBID: 33, Slug: "universal-pictures", DisplayName: "Universal Pictures", LogoPath: "/8lvHyhjr8oUKOOy2dKXoALWKdp0.png"},
	{TMDBID: 4, Slug: "paramount-pictures", DisplayName: "Paramount Pictures", LogoPath: "/fycMZt242LVjagMByZOLUGbCvv3.png"},
	{TMDBID: 34, Slug: "sony-pictures", DisplayName: "Sony Pictures", LogoPath: "/GagSvqWlyPdkFHMfQ3pNq6ix9P.png"},
	{TMDBID: 127928, Slug: "20th-century-studios", DisplayName: "20th Century Studios", LogoPath: "/h0rjX5vjW5r8yEnUBStFarjcLT4.png"},
	{TMDBID: 521, Slug: "dreamworks", DisplayName: "DreamWorks", LogoPath: "/kP7t6RwGz2AvvTkvnI1uteEwHet.png"},
	{TMDBID: 41077, Slug: "a24", DisplayName: "A24", LogoPath: "/1ZXsGaFPgrgS6ZZGS37AqD5uU12.png"},
}

// BundledNetworks is the compile-time list of networks shown in the Networks
// carousel.
var BundledNetworks = []BundledNetwork{
	{TMDBID: 213, Slug: "netflix", DisplayName: "Netflix", LogoPath: "/wwemzKWzjKYJFfCeiB57q3r4Bcm.png"},
	{TMDBID: 2739, Slug: "disney-plus", DisplayName: "Disney+", LogoPath: "/gJ8VX6JSu3ciXHuC2dDGAo2lvwM.png"},
	{TMDBID: 1024, Slug: "prime-video", DisplayName: "Prime Video", LogoPath: "/ifhbNuuVnlwYy5oXA5VIb2YR8AZ.png"},
	{TMDBID: 2552, Slug: "apple-tv-plus", DisplayName: "Apple TV+", LogoPath: "/4KAy34EHvRM25Ih8wb82AuGU7zJ.png"},
	{TMDBID: 453, Slug: "hulu", DisplayName: "Hulu", LogoPath: "/pqUTCleNUiTLAVlelGxUgWn1ELh.png"},
	{TMDBID: 49, Slug: "hbo", DisplayName: "HBO", LogoPath: "/tuomPhY2UtuPTqqFnKMVHvSb724.png"},
	{TMDBID: 4330, Slug: "paramount-plus", DisplayName: "Paramount+", LogoPath: "/fi83B1oztoS47xxcemFdPMhIzK.png"},
	{TMDBID: 174, Slug: "amc", DisplayName: "AMC", LogoPath: "/pmvRmATOCaDykE6JrVoeYxlFHw3.png"},
	{TMDBID: 4, Slug: "bbc-one", DisplayName: "BBC One", LogoPath: "/mVn7xESaTNmjBUyUtGNvDQd3CT1.png"},
	{TMDBID: 3353, Slug: "peacock", DisplayName: "Peacock", LogoPath: "/gIAcGTjKKr0KOHL5s4O36roJ8p7.png"},
}

// BundledGenres is the compile-time list of genres shown in the Genres
// carousel. SeriesID = 0 means the genre has no direct TV equivalent and
// the browse page hides the Series tab.
var BundledGenres = []BundledGenre{
	{Slug: "action", DisplayName: "Action", GradientFrom: "#dc2626", GradientTo: "#7f1d1d", MovieID: 28, SeriesID: 10759},
	{Slug: "comedy", DisplayName: "Comedy", GradientFrom: "#fbbf24", GradientTo: "#b45309", MovieID: 35, SeriesID: 35},
	{Slug: "drama", DisplayName: "Drama", GradientFrom: "#64748b", GradientTo: "#1e293b", MovieID: 18, SeriesID: 18},
	{Slug: "sci-fi", DisplayName: "Sci-Fi", GradientFrom: "#7c3aed", GradientTo: "#312e81", MovieID: 878, SeriesID: 10765},
	{Slug: "horror", DisplayName: "Horror", GradientFrom: "#7f1d1d", GradientTo: "#1f2937", MovieID: 27, SeriesID: 0},
	{Slug: "romance", DisplayName: "Romance", GradientFrom: "#ec4899", GradientTo: "#831843", MovieID: 10749, SeriesID: 0},
	{Slug: "animation", DisplayName: "Animation", GradientFrom: "#06b6d4", GradientTo: "#155e75", MovieID: 16, SeriesID: 16},
	{Slug: "documentary", DisplayName: "Documentary", GradientFrom: "#475569", GradientTo: "#0f172a", MovieID: 99, SeriesID: 99},
}

// FindStudioBySlug looks up a bundled studio by slug. Returns (zero, false)
// if not found.
func FindStudioBySlug(slug string) (BundledStudio, bool) {
	for _, s := range BundledStudios {
		if s.Slug == slug {
			return s, true
		}
	}
	return BundledStudio{}, false
}

// FindNetworkBySlug looks up a bundled network by slug.
func FindNetworkBySlug(slug string) (BundledNetwork, bool) {
	for _, n := range BundledNetworks {
		if n.Slug == slug {
			return n, true
		}
	}
	return BundledNetwork{}, false
}

// FindGenreBySlug looks up a bundled genre by slug.
func FindGenreBySlug(slug string) (BundledGenre, bool) {
	for _, g := range BundledGenres {
		if g.Slug == slug {
			return g, true
		}
	}
	return BundledGenre{}, false
}
