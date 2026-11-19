package requests

func presentMovie(tmdbID int) *fakePresence {
	return &fakePresence{available: map[MediaType]map[int]bool{MediaTypeMovie: {tmdbID: true}}}
}
