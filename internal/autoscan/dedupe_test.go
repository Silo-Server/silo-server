package autoscan

import (
	"reflect"
	"testing"
)

func TestGroupByParentDir(t *testing.T) {
	in := []string{
		"/mnt/media/Show/Season 01/E01.mkv",
		"/mnt/media/Movie/Movie.mkv",
		"/mnt/media/Show/Season 01/E02.mkv",
		"/mnt/media/Show/Season 01/E01.mkv",
		"",
	}
	got := groupByParentDir(in)
	want := []parentDirGroup{
		{Dir: "/mnt/media/Show/Season 01", Paths: []string{"/mnt/media/Show/Season 01/E01.mkv", "/mnt/media/Show/Season 01/E02.mkv"}},
		{Dir: "/mnt/media/Movie", Paths: []string{"/mnt/media/Movie/Movie.mkv"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groupByParentDir = %+v, want %+v", got, want)
	}
}
