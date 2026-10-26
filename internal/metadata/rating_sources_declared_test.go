package metadata

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A plugin reports a source of its own only after declaring it; an undeclared
// unknown source is still dropped, so a typo cannot store a stray source.
func TestRatingSourcesFromStructKeepsDeclaredSources(t *testing.T) {
	number := structpb.NewNumberValue
	ratings := ratingsStructWithSources(t, map[string]*structpb.Value{
		"imdb":      sourceEntry(map[string]*structpb.Value{"score": number(81)}),
		"Kinopoisk": sourceEntry(map[string]*structpb.Value{"score": number(72), "votes": number(3100)}),
		"douban":    sourceEntry(map[string]*structpb.Value{"score": number(90)}),
	})

	got := ratingSourcesFromStruct(ratings, "kino", map[string]struct{}{"kinopoisk": {}})
	want := map[string]RatingSource{
		models.RatingSourceIMDB: {Score: 81, Provider: "kino"},
		"kinopoisk":             {Score: 72, Votes: 3100, Provider: "kino"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ratingSourcesFromStruct() = %+v, want %+v", got, want)
	}
}

func TestExtractRatingSources(t *testing.T) {
	metadataJSON := []byte(`{"metadata": {"rating_sources": [
		{"id": "kinopoisk", "name": "Kinopoisk", "scale": 10},
		{"id": "rt_critic", "name": "RT", "label": "Rotten Tomatoes critics", "scale": 100, "percent": true},
		{"id": "douban", "name": " Douban ", "label": "A label much too long to show in the administrator list of sources", "scale": 10, "percent": false},
		{"id": "fresh_meter", "name": "Fresh", "scale": 100, "percent": true},
		{"id": "approval", "name": "Approval", "scale": 10, "percent": true},
		{"id": "liked", "name": "Liked", "percent": true},
		{"id": "rt_audience", "name": "RT Audience", "scale": 10},
		{"id": "kinopoisk", "name": "Duplicate", "scale": 5},
		{"id": "Letterboxd_Fans", "name": "Fans", "scale": 5},
		{"id": "typed", "name": "Typed", "scale": "10"},
		{"id": "imdb", "name": "Not IMDb", "scale": 10},
		{"id": "Bad Id", "name": "Bad", "scale": 10},
		{"id": "noname", "name": "", "scale": 10},
		{"id": "longname", "name": "A name far too long to fit on a card", "scale": 10},
		{"id": "noscale", "name": "No scale"},
		{"id": "hugescale", "name": "Huge", "scale": 1000}
	]}}`)

	got := extractRatingSources(metadataJSON)
	want := []models.RatingSourceDefinition{
		{Source: "kinopoisk", Name: "Kinopoisk", Label: "Kinopoisk", Scale: 10},
		{Source: "rt_critic", Name: "RT", Label: "Rotten Tomatoes critics", Scale: 100, Percent: true},
		{Source: "douban", Name: "Douban", Label: "Douban", Scale: 10},
		{Source: "fresh_meter", Name: "Fresh", Label: "Fresh", Scale: 100, Percent: true},
		{Source: "approval", Name: "Approval", Label: "Approval", Scale: 100, Percent: true},
		{Source: "liked", Name: "Liked", Label: "Liked", Scale: 100, Percent: true},
		{Source: "rt_audience", Name: "RT Audience", Label: "RT Audience", Scale: 100, Percent: true},
		{Source: "letterboxd_fans", Name: "Fans", Label: "Fans", Scale: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractRatingSources() = %+v\nwant %+v", got, want)
	}

	if got := extractRatingSources([]byte(`{"rating_sources": "kinopoisk"}`)); got != nil {
		t.Fatalf("a malformed declaration = %+v, want nil", got)
	}
}

func TestExtractRatingSourcesCapsTheCount(t *testing.T) {
	metadataJSON := []byte(`{"rating_sources": [
		{"id": "s1", "name": "S1", "scale": 10}, {"id": "s2", "name": "S2", "scale": 10},
		{"id": "s3", "name": "S3", "scale": 10}, {"id": "s4", "name": "S4", "scale": 10},
		{"id": "s5", "name": "S5", "scale": 10}, {"id": "s6", "name": "S6", "scale": 10},
		{"id": "s7", "name": "S7", "scale": 10}, {"id": "s8", "name": "S8", "scale": 10},
		{"id": "s9", "name": "S9", "scale": 10}
	]}`)
	if got := extractRatingSources(metadataJSON); len(got) != maxDeclaredRatingSources {
		t.Fatalf("kept %d sources, want %d", len(got), maxDeclaredRatingSources)
	}
}
