package autoscan

import (
	"reflect"
	"sort"
	"testing"
)

func TestUniqueParentDirs(t *testing.T) {
	in := []string{
		"/mnt/media/Show/Season 01/E01.mkv",
		"/mnt/media/Show/Season 01/E02.mkv",
		"/mnt/media/Movie/Movie.mkv",
		"",
	}
	got := uniqueParentDirs(in)
	sort.Strings(got)
	want := []string{"/mnt/media/Movie", "/mnt/media/Show/Season 01"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueParentDirs = %v, want %v", got, want)
	}
}
