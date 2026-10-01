package naming

import "testing"

func TestParseSeriesReleaseYear(t *testing.T) {
	for _, tt := range []struct {
		path, title string
		year        int
		want        bool
	}{
		{"/tv/Example.Show.2020.S02E03.1080p.WEB-DL.mkv", "Example Show", 2020, true},
		{"/tv/Example_Show_2020_2x03.mkv", "Example Show", 2020, true},
		{"/tv/Space.1999.S01E01.mkv", "Space", 1999, true},
		{"/tv/Class.of.2007.S01E01.mkv", "Class of", 2007, true},
		{"/tv/1899.2022.S01E01.mkv", "1899", 2022, true},
		{"/tv/1899.S01E01.mkv", "", 0, false},
		{"/tv/Example.Show.(2020).S01E01.mkv", "", 0, false},
		{"/tv/Example.Show.2020.mkv", "", 0, false},
		{"/tv/Example.Show.2020.E03.mkv", "", 0, false},
		{"/tv/Example Show/Example.Show.2020.S01E01.mkv", "", 0, false},
		{"/tv/Class of 2007/Season 1/Class.of.2007.S01E01.mkv", "", 0, false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			title, year, ok := ParseSeriesReleaseYear(tt.path, "/tv")
			if title != tt.title || year != tt.year || ok != tt.want {
				t.Fatalf("alternate = %q, %d, %v; want %q, %d, %v", title, year, ok, tt.title, tt.year, tt.want)
			}
			if tt.want {
				primary := ParseFilename(tt.path, "series", "/tv")
				if primary.Title == title || primary.Year != 0 {
					t.Fatalf("alternate changed primary numeric title: %+v", primary)
				}
			}
		})
	}
}
